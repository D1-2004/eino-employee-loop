// Package kernel runs one Employee Loop wake on CloudWeGo Eino components.
//
// A wake is: freeze context -> ask the model -> validate the native tool
// batch -> let the Host execute every call -> stop on a terminal decision or
// ask again within the budget. The Host owns permissions, receipts and
// effects; the kernel owns ordering, validation, budgets and the rule that
// nothing counts as done without a Host receipt.
//
// The loop is explicit rather than delegated to a generic ReAct agent because
// durable Hosts need exactly-once effects, per-wake budgets, batch-level
// validation and terminal dispositions decided by the Host at run time.
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Budget errors are joined with contract.ErrBudget.
var (
	ErrConsecutiveErrors = errors.New("kernel: consecutive model errors exhausted")
	ErrResponseBudget    = errors.New("kernel: valid model response budget exhausted")
	ErrAttemptBudget     = errors.New("kernel: provider attempt budget exhausted")
	errTerminalRequired  = errors.New("kernel: a native terminal tool call is required")
)

// Default per-call timeouts by tool kind (a spec's Timeout wins).
const (
	DefaultReadTimeout   = 30 * time.Second
	DefaultEffectTimeout = 120 * time.Second
)

// FinalTextOrigin marks the synthetic outcome of an accepted final text.
const FinalTextOrigin = "host_final_text"

// Options configure one wake. Model, Host and Tools are injected; nothing in
// the kernel names a vendor, a channel or a language.
type Options struct {
	Model model.ToolCallingChatModel
	Host  contract.Host
	// Tools are the specs offered in this wake, with descriptions resolved.
	Tools  []contract.ToolSpec
	Budget contract.Budget
	// RequireTerminal makes plain assistant text private: only a Host-accepted
	// terminal decision ends the wake (unless FinalText accepts it).
	RequireTerminal bool
	// ForceToolCall asks the provider for a tool call (tool_choice=forced)
	// without choosing which one.
	ForceToolCall bool
	// Validator and TextGuard add Host-specific batch and public-text rules on
	// top of the kernel's own schema validation.
	Validator contract.BatchValidator
	TextGuard contract.TextGuard
	// FinalText optionally accepts a first plain answer when RequireTerminal.
	FinalText contract.FinalTextHost
	// Permanent classifies provider errors that end the wake immediately
	// (rejected parameters, an exhausted durable reservation).
	Permanent func(error) bool
	// Messages and Locale resolve repair instructions and rendered labels.
	Messages *i18n.Bundle
	Locale   string
	Observer Observer
	// Now is injectable for tests.
	Now func() time.Time
}

// Wake is the frozen input of one wake. Either Transcript (Host-rendered
// messages, system first) or Instructions plus Blocks must be set.
type Wake struct {
	Identity     contract.Identity
	Admission    *contract.Admission
	Window       *contract.Window
	Transcript   []*schema.Message
	Instructions string
	Blocks       []contract.ContextBlock
}

// Result is the wake outcome plus the accepted transcript (rejected model
// output is never kept in history).
type Result struct {
	contract.Outcome
	Transcript []*schema.Message
}

// Observer receives best-effort notifications; it must not block.
type Observer interface {
	ModelCall(ctx context.Context, attempt int, resp *schema.Message, err error, elapsed time.Duration)
	ToolCall(ctx context.Context, call contract.ToolCall, result contract.ToolResult, err error, elapsed time.Duration)
	BatchRejected(ctx context.Context, calls []contract.ToolCall, err error)
	// ResponseFailed reports every rejected response or provider error that
	// consumed budget, after the repair instruction was queued.
	ResponseFailed(ctx context.Context, err error)
}

type runner struct {
	ctx       context.Context
	o         Options
	w         Wake
	specs     map[string]contract.ToolSpec
	msgs      []*schema.Message
	res       Result
	lastErr   error
	attempted bool
}

// Run executes one wake.
func Run(ctx context.Context, o Options, w Wake) (res Result, err error) {
	if o.Model == nil {
		return Result{}, errors.New("kernel: missing model")
	}
	if o.Host == nil {
		return Result{}, errors.New("kernel: missing host")
	}
	if o.Budget == (contract.Budget{}) {
		o.Budget = contract.DefaultBudget()
	}
	if err := o.Budget.Validate(); err != nil {
		return Result{}, err
	}
	if o.Messages == nil {
		o.Messages = i18n.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &runner{ctx: ctx, o: o, w: w, specs: map[string]contract.ToolSpec{}}
	for _, s := range o.Tools {
		if err := s.Validate(); err != nil {
			return Result{}, err
		}
		if _, dup := r.specs[s.Name]; dup {
			return Result{}, fmt.Errorf("kernel: duplicate tool %q", s.Name)
		}
		r.specs[s.Name] = s
	}
	msgs, err := r.initialMessages()
	if err != nil {
		return Result{}, err
	}
	r.msgs = msgs
	infos, err := ToolInfos(o.Tools)
	if err != nil {
		return Result{}, err
	}
	bound := o.Model
	if len(infos) > 0 {
		if bound, err = o.Model.WithTools(infos); err != nil {
			return Result{}, err
		}
	}
	var opts []model.Option
	if o.ForceToolCall && len(infos) > 0 {
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForced))
	}
	// The returned Result is copied before deferred calls run, so the
	// transcript is attached to the named result, not to r.res.
	defer func() { res.Transcript = append([]*schema.Message(nil), r.msgs...) }()
	for {
		if err := r.budgetErr(); err != nil {
			return r.res, errors.Join(contract.ErrBudget, err, r.lastErr)
		}
		r.res.Usage.Attempts++
		started := o.Now()
		resp, err := bound.Generate(ctx, r.msgs, opts...)
		if o.Observer != nil {
			o.Observer.ModelCall(ctx, r.res.Usage.Attempts, resp, err, o.Now().Sub(started))
		}
		if ctx.Err() != nil {
			return r.res, ctx.Err()
		}
		if err != nil {
			if errors.Is(err, ErrJournal) || (o.Permanent != nil && o.Permanent(err)) {
				return r.res, err
			}
			r.fail(err)
			continue
		}
		if resp == nil {
			r.fail(errors.New("kernel: model returned no message"))
			continue
		}
		finish := ""
		if resp.ResponseMeta != nil {
			finish = resp.ResponseMeta.FinishReason
			if u := resp.ResponseMeta.Usage; u != nil {
				r.res.Usage.PromptTokens += u.PromptTokens
				r.res.Usage.CompletionTokens += u.CompletionTokens
			}
		}
		done, err := r.handle(ctx, resp, finish)
		if err != nil {
			return r.res, err
		}
		if done {
			return r.res, nil
		}
	}
}

func (r *runner) handle(ctx context.Context, resp *schema.Message, finish string) (bool, error) {
	switch {
	case len(resp.ToolCalls) > 0 && (finish == "tool_calls" || finish == "stop" || finish == ""):
		return r.handleTools(ctx, resp)
	case len(resp.ToolCalls) == 0 && (finish == "stop" || finish == ""):
		return r.handleText(ctx, resp)
	default:
		r.fail(fmt.Errorf("kernel: incomplete model response: finish_reason=%q", finish))
		return false, nil
	}
}

func (r *runner) handleText(ctx context.Context, resp *schema.Message) (bool, error) {
	text := strings.TrimSpace(resp.Content)
	if r.o.RequireTerminal {
		if r.o.FinalText != nil && r.res.Usage.Attempts == 1 && !r.attempted && text != "" {
			if err := r.guard(text); err != nil {
				r.fail(err)
				return false, nil
			}
			result, err := r.o.FinalText.AcceptFinalText(ctx, r.w.Identity, text)
			outcome := contract.ToolOutcome{Origin: FinalTextOrigin, Tool: FinalTextOrigin, Result: result}
			if err == nil && result.Terminal != nil {
				err = result.Terminal.Validate()
			}
			if err != nil || result.Terminal == nil {
				if err == nil {
					err = errors.New("kernel: final text produced no terminal decision")
				}
				outcome.Error = err.Error()
				r.res.ToolOutcomes = append(r.res.ToolOutcomes, outcome)
				r.fail(err)
				return false, nil
			}
			r.res.ToolOutcomes = append(r.res.ToolOutcomes, outcome)
			if result.Receipt != "" {
				r.res.Receipts = append(r.res.Receipts, contract.Receipt{Tool: FinalTextOrigin, ID: result.Receipt})
			}
			r.msgs = append(r.msgs, resp)
			r.accepted(true)
			r.res.Decision = *result.Terminal
			return true, nil
		}
		r.fail(errTerminalRequired)
		return false, nil
	}
	if text == "" {
		r.fail(errors.New("kernel: normal completion requires nonempty text"))
		return false, nil
	}
	if err := r.guard(text); err != nil {
		r.fail(err)
		return false, nil
	}
	r.msgs = append(r.msgs, resp)
	r.accepted(true)
	r.res.Decision = contract.Decision{Kind: contract.Reply, Reply: text}
	return true, nil
}

func (r *runner) handleTools(ctx context.Context, resp *schema.Message) (bool, error) {
	calls, err := parseCalls(resp, r.res.Usage.Attempts-1)
	if err == nil {
		err = r.validateBatch(calls)
	}
	if err != nil {
		if r.o.Observer != nil {
			r.o.Observer.BatchRejected(ctx, calls, err)
		}
		r.fail(err)
		return false, nil
	}
	r.msgs = append(r.msgs, resp)
	r.accepted(false)
	var terminals []contract.Decision
	var batchErr, noProgress error
	progress := false
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return false, errors.Join(batchErr, err)
		}
		spec := r.specs[call.Name]
		r.attempted = true
		result, callErr := r.execute(ctx, spec, call)
		if result.Terminal != nil {
			t := *result.Terminal
			result.Terminal = &t
		}
		outcome := contract.ToolOutcome{NativeID: call.NativeID, Tool: call.Name, Result: result}
		if result.Receipt != "" {
			r.res.Receipts = append(r.res.Receipts, contract.Receipt{NativeID: call.NativeID, Tool: call.Name, ID: result.Receipt})
		}
		if callErr != nil {
			outcome.Error = callErr.Error()
			r.res.ToolOutcomes = append(r.res.ToolOutcomes, outcome)
			if errors.Is(callErr, ErrJournal) {
				return false, errors.Join(batchErr, callErr)
			}
			r.toolError(call, callErr)
			noProgress = errors.Join(noProgress, callErr)
			refused := errors.Is(callErr, contract.ErrToolRefused) && strings.TrimSpace(result.Receipt) == ""
			if (spec.Effect() || result.Receipt != "") && !refused {
				batchErr = errors.Join(batchErr, fmt.Errorf("kernel: tool %s failed: %w", call.Name, callErr))
			}
			continue
		}
		var resultErr error
		if spec.Effect() && strings.TrimSpace(result.Receipt) == "" {
			resultErr = fmt.Errorf("%w: %s", contract.ErrMissingReceipt, call.Name)
		}
		if result.Terminal != nil {
			resultErr = errors.Join(resultErr, result.Terminal.Validate())
			if result.Terminal.Kind == contract.Dispatched && strings.TrimSpace(result.Receipt) == "" {
				resultErr = errors.Join(resultErr, fmt.Errorf("%w: dispatch", contract.ErrMissingReceipt))
			}
		}
		if resultErr != nil {
			outcome.Error = resultErr.Error()
			r.res.ToolOutcomes = append(r.res.ToolOutcomes, outcome)
			r.toolError(call, resultErr)
			batchErr = errors.Join(batchErr, resultErr)
			continue
		}
		r.res.ToolOutcomes = append(r.res.ToolOutcomes, outcome)
		progress = true
		r.msgs = append(r.msgs, schema.ToolMessage(result.Content, call.NativeID, schema.WithToolName(call.Name)))
		if result.Terminal != nil {
			terminals = append(terminals, *result.Terminal)
		}
	}
	if progress {
		r.res.Usage.ConsecutiveErrors = 0
		r.lastErr = nil
	} else {
		r.res.Usage.ConsecutiveErrors++
		r.lastErr = noProgress
	}
	decision, err := aggregate(terminals)
	if err = errors.Join(batchErr, err); err != nil {
		return false, err
	}
	if decision.Kind != "" {
		r.res.Decision = decision
		return true, nil
	}
	return false, nil
}

func (r *runner) execute(ctx context.Context, spec contract.ToolSpec, call contract.ToolCall) (contract.ToolResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultReadTimeout
		if spec.Effect() {
			timeout = DefaultEffectTimeout
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := r.o.Now()
	result, err := r.o.Host.Execute(cctx, r.w.Identity, call)
	if r.o.Observer != nil {
		r.o.Observer.ToolCall(ctx, call, result, err, r.o.Now().Sub(started))
	}
	return result, err
}

func (r *runner) toolError(call contract.ToolCall, err error) {
	content, _ := json.Marshal(map[string]string{"error": err.Error()})
	r.msgs = append(r.msgs, schema.ToolMessage(string(content), call.NativeID, schema.WithToolName(call.Name)))
}

func (r *runner) accepted(progress bool) {
	r.res.Usage.Responses++
	if progress {
		r.res.Usage.ConsecutiveErrors = 0
		r.lastErr = nil
	}
}

// fail records an invalid response or provider error and appends a repair
// instruction. Invalid output itself never enters the transcript.
func (r *runner) fail(err error) {
	r.lastErr = err
	r.res.Usage.ConsecutiveErrors++
	repair := r.o.Messages.Text(r.o.Locale, "kernel.repair.invalid")
	if r.o.RequireTerminal {
		repair += " " + r.terminalInstruction()
	}
	r.msgs = append(r.msgs, schema.UserMessage(repair))
	if r.o.Observer != nil {
		r.o.Observer.ResponseFailed(r.ctx, err)
	}
}

func (r *runner) terminalInstruction() string {
	var names []string
	for _, s := range r.o.Tools {
		if s.Terminal != "" {
			names = append(names, s.Name)
		}
	}
	text := r.o.Messages.Text(r.o.Locale, "kernel.terminal.required")
	if len(names) > 0 {
		text += " " + r.o.Messages.Text(r.o.Locale, "kernel.terminal.tools", "tools", strings.Join(names, ", "))
	}
	return text
}

func (r *runner) guard(text string) error {
	if r.o.TextGuard == nil {
		return nil
	}
	return r.o.TextGuard(text)
}

func (r *runner) budgetErr() error {
	u, b := r.res.Usage, r.o.Budget
	switch {
	case u.ConsecutiveErrors >= b.MaxConsecutiveErrors:
		return ErrConsecutiveErrors
	case u.Responses >= b.MaxResponses:
		return ErrResponseBudget
	case u.Attempts >= b.MaxAttempts:
		return ErrAttemptBudget
	}
	return nil
}

// parseCalls converts the native calls of model turn turn.
func parseCalls(resp *schema.Message, turn int) ([]contract.ToolCall, error) {
	seen := map[string]bool{}
	calls := make([]contract.ToolCall, 0, len(resp.ToolCalls))
	for _, c := range resp.ToolCalls {
		if (c.Type != "" && c.Type != "function") || strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.Function.Name) == "" {
			return nil, errors.New("kernel: invalid or duplicate native tool call identity")
		}
		if strings.HasPrefix(c.ID, contract.HostCallPrefix) {
			return nil, errors.New("kernel: provider call uses a reserved host identity")
		}
		seen[c.ID] = true
		var args map[string]any
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil || args == nil {
			return nil, fmt.Errorf("kernel: invalid arguments for %q", c.Function.Name)
		}
		calls = append(calls, contract.ToolCall{NativeID: c.ID, Name: c.Function.Name, Arguments: args, Turn: turn})
	}
	return calls, nil
}

// validateBatch rejects a batch before any call runs: unknown tools,
// exclusive tools in company, conflicting static terminal dispositions,
// invalid arguments and guarded public text. Every rejection is repairable.
func (r *runner) validateBatch(calls []contract.ToolCall) error {
	var disposition contract.Disposition
	for _, c := range calls {
		spec, ok := r.specs[c.Name]
		if !ok {
			return fmt.Errorf("kernel: unknown tool %q", c.Name)
		}
		if spec.Terminal != "" {
			if disposition != "" && disposition != spec.Terminal {
				return fmt.Errorf("%w: %s and %s in one batch", contract.ErrTerminalConflict, disposition, spec.Terminal)
			}
			disposition = spec.Terminal
		}
		if spec.Exclusive && len(calls) > 1 {
			return fmt.Errorf("kernel: %s must be the only call in its batch", c.Name)
		}
		if problems := ValidateArguments(spec.Parameters, c.Arguments); len(problems) > 0 {
			return fmt.Errorf("kernel: invalid params for %s: %s", c.Name, strings.Join(problems, "; "))
		}
		if reply, ok := c.Arguments["reply"].(string); ok {
			if err := r.guard(reply); err != nil {
				return err
			}
		}
	}
	if r.o.Validator != nil {
		return r.o.Validator.ValidateBatch(calls)
	}
	return nil
}

// aggregate combines compatible terminal decisions of one batch. Static
// dispositions are checked by validateBatch; this catches run-time ones.
func aggregate(terminals []contract.Decision) (contract.Decision, error) {
	if len(terminals) == 0 {
		return contract.Decision{}, nil
	}
	kind := terminals[0].Kind
	seen := map[string]bool{}
	var replies []string
	for _, t := range terminals {
		if t.Kind != kind {
			return contract.Decision{}, contract.ErrTerminalConflict
		}
		reply := strings.TrimSpace(t.Reply)
		if reply != "" && !seen[reply] {
			replies = append(replies, reply)
			seen[reply] = true
		}
	}
	return contract.Decision{Kind: kind, Reply: strings.Join(replies, "\n")}, nil
}
