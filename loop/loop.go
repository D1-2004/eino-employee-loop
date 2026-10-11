// Package loop runs one employee wake on a CloudWeGo Eino chat model.
//
// A wake is: take the Host's frozen messages -> ask the model -> validate
// the native tool batch -> let the Host execute every call -> stop on a
// terminal decision or ask again within the budget. The Host owns context
// assembly, permissions, receipts and effects; the loop owns ordering,
// validation, budgets and the rule that nothing counts as done without a
// Host receipt.
//
//	out, err := loop.New(cfg, model, host).Run(ctx, loop.Wake{Identity: id, Messages: msgs})
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const defaultFinalTextLimit = 8000

var errTerminalRequired = errors.New("employee loop: a native terminal tool call is required")

// Loop runs wakes with one configuration. It holds no state between runs.
type Loop struct {
	cfg   Config
	model model.ToolCallingChatModel
	host  Host
}

// New returns a loop over an Eino tool-calling model and a Host.
func New(cfg Config, m model.ToolCallingChatModel, host Host) *Loop {
	return &Loop{cfg: cfg, model: m, host: host}
}

type run struct {
	ctx   context.Context
	cfg   Config
	model model.ToolCallingChatModel
	host  Host
	id    Identity
	tools map[string]Tool
	infos []*schema.ToolInfo
	// offered holds the tools of the current model call.
	offered   map[string]bool
	msgs      []*schema.Message
	out       Outcome
	lastErr   error
	attempted bool
}

// Run executes one wake.
func (l *Loop) Run(ctx context.Context, w Wake) (Outcome, error) {
	if l.model == nil {
		return Outcome{}, errors.New("employee loop: missing model")
	}
	cfg := l.cfg
	if cfg.Budget == (Budget{}) {
		cfg.Budget = DefaultBudget()
	}
	if b := cfg.Budget; b.MaxModelResponses <= 0 || b.MaxConsecutiveErrors <= 0 || b.MaxPhysicalAttempts <= 0 {
		return Outcome{}, fmt.Errorf("employee loop: invalid budget %+v", b)
	}
	if cfg.FinalTextLimit <= 0 {
		cfg.FinalTextLimit = defaultFinalTextLimit
	}
	if len(w.Messages) == 0 {
		return Outcome{}, errors.New("employee loop: a wake needs messages")
	}
	r := &run{ctx: ctx, cfg: cfg, model: l.model, host: l.host, id: w.Identity, tools: map[string]Tool{}, msgs: append([]*schema.Message(nil), w.Messages...)}
	for _, t := range cfg.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return Outcome{}, errors.New("employee loop: a tool needs a name")
		}
		if _, dup := r.tools[t.Name]; dup {
			return Outcome{}, fmt.Errorf("employee loop: duplicate tool %q", t.Name)
		}
		r.tools[t.Name] = t
	}
	var err error
	if r.infos, err = ToolInfos(cfg.Tools); err != nil {
		return Outcome{}, err
	}
	out, err := r.loop()
	out.Transcript = append([]*schema.Message(nil), r.msgs...)
	return out, err
}

// bind offers the tools of the next model call (all of them unless
// Config.TurnTools narrows the list).
func (r *run) bind() (model.ToolCallingChatModel, []model.Option, error) {
	infos := r.infos
	r.offered = map[string]bool{}
	if r.cfg.TurnTools != nil {
		allowed := map[string]bool{}
		for _, name := range r.cfg.TurnTools(r.out.ModelCalls+1, r.out.ToolOutcomes) {
			allowed[name] = true
		}
		infos = nil
		for _, info := range r.infos {
			if allowed[info.Name] {
				infos = append(infos, info)
			}
		}
	}
	for _, info := range infos {
		r.offered[info.Name] = true
	}
	if len(infos) == 0 {
		return r.model, nil, nil
	}
	bound, err := r.model.WithTools(infos)
	if err != nil {
		return nil, nil, err
	}
	var opts []model.Option
	if r.cfg.RequireNativeToolCall {
		opts = append(opts, model.WithToolChoice(schema.ToolChoiceForced))
	}
	return bound, opts, nil
}

func (r *run) loop() (Outcome, error) {
	for {
		if err := r.budgetErr(); err != nil {
			return r.out, errors.Join(ErrModelBudget, err, r.lastErr)
		}
		m, opts, err := r.bind()
		if err != nil {
			return r.out, err
		}
		r.out.ModelCalls++
		request := append([]*schema.Message(nil), r.msgs...)
		started := time.Now()
		resp, err := m.Generate(r.ctx, request, opts...)
		if r.cfg.OnModelCall != nil {
			r.cfg.OnModelCall(r.ctx, ModelCall{Attempt: r.out.ModelCalls, Request: request, Response: resp, Err: err, Elapsed: time.Since(started)})
		}
		if r.ctx.Err() != nil {
			return r.out, r.ctx.Err()
		}
		if err != nil {
			if r.cfg.Permanent != nil && r.cfg.Permanent(err) {
				return r.out, err
			}
			r.fail(err)
			continue
		}
		if resp == nil {
			r.fail(errors.New("employee loop: model returned no message"))
			continue
		}
		finish := ""
		if meta := resp.ResponseMeta; meta != nil {
			finish = meta.FinishReason
			if meta.Usage != nil {
				r.out.Usage.PromptTokens += meta.Usage.PromptTokens
				r.out.Usage.CompletionTokens += meta.Usage.CompletionTokens
			}
		}
		var done bool
		switch {
		case len(resp.ToolCalls) > 0 && (finish == "tool_calls" || finish == "stop" || finish == ""):
			done, err = r.batch(resp)
		case len(resp.ToolCalls) == 0 && (finish == "stop" || finish == ""):
			done, err = r.text(resp)
		default:
			r.fail(fmt.Errorf("employee loop: incomplete model response: finish_reason=%q", finish))
		}
		if err != nil || done {
			return r.out, err
		}
	}
}

func (r *run) text(resp *schema.Message) (bool, error) {
	text := strings.TrimSpace(resp.Content)
	if !r.cfg.RequireTerminalTool {
		if text == "" {
			r.fail(errors.New("employee loop: a plain reply needs text"))
			return false, nil
		}
		if err := r.guard(text); err != nil {
			r.fail(err)
			return false, nil
		}
		r.accept(resp, true)
		r.out.Decision = Decision{Kind: Reply, Reply: text}
		return true, nil
	}
	final, ok := r.host.(FinalTextHost)
	if !r.cfg.AcceptFinalText || !ok || r.out.ModelCalls != 1 || r.attempted || text == "" {
		r.fail(errTerminalRequired)
		return false, nil
	}
	if len(text) > r.cfg.FinalTextLimit {
		r.fail(fmt.Errorf("employee loop: final text exceeds %d bytes", r.cfg.FinalTextLimit))
		return false, nil
	}
	if err := r.guard(text); err != nil {
		r.fail(err)
		return false, nil
	}
	// The Host may commit the reply, so a failure here ends the wake rather
	// than asking the model again.
	r.attempted = true
	result, err := final.AcceptFinalText(r.ctx, r.id, text)
	outcome := ToolOutcome{Origin: FinalTextOrigin, ToolName: FinalTextOrigin, Result: result}
	if err == nil && result.Terminal == nil {
		err = errors.New("employee loop: final text produced no terminal decision")
	}
	if err != nil {
		outcome.Error = err.Error()
	}
	r.out.ToolOutcomes = append(r.out.ToolOutcomes, outcome)
	if result.Receipt != "" {
		r.out.Receipts = append(r.out.Receipts, Receipt{ToolName: FinalTextOrigin, ID: result.Receipt})
	}
	if err != nil {
		return false, err
	}
	r.accept(resp, true)
	r.out.Decision = *result.Terminal
	return true, nil
}

func (r *run) batch(resp *schema.Message) (bool, error) {
	calls, err := parseCalls(resp)
	if err == nil {
		err = r.validate(calls)
	}
	if err != nil {
		if r.cfg.OnBatchRejected != nil {
			r.cfg.OnBatchRejected(calls, err)
		}
		r.fail(err)
		return false, nil
	}
	r.accept(resp, false)
	var terminals []Decision
	var batchErr, noProgress error
	progress := false
	for _, call := range calls {
		if err := r.ctx.Err(); err != nil {
			return false, errors.Join(batchErr, err)
		}
		tool := r.tools[call.Name]
		r.attempted = true
		result, callErr := r.execute(tool, call)
		if result.Terminal != nil {
			t := *result.Terminal
			result.Terminal = &t
		}
		outcome := ToolOutcome{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, Result: result}
		receipt := strings.TrimSpace(result.Receipt)
		if receipt != "" {
			r.out.Receipts = append(r.out.Receipts, Receipt{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, ID: result.Receipt})
		}
		if callErr == nil && tool.Effect && receipt == "" {
			callErr = fmt.Errorf("%w: %s", ErrMissingReceipt, call.Name)
		}
		if callErr == nil && result.Terminal != nil && result.Terminal.Kind == Dispatched && receipt == "" {
			callErr = fmt.Errorf("%w: dispatch", ErrMissingReceipt)
		}
		if callErr != nil {
			outcome.Error = callErr.Error()
			r.out.ToolOutcomes = append(r.out.ToolOutcomes, outcome)
			content, _ := json.Marshal(map[string]string{"error": callErr.Error()})
			r.msgs = append(r.msgs, schema.ToolMessage(string(content), call.NativeToolCallID, schema.WithToolName(call.Name)))
			noProgress = errors.Join(noProgress, callErr)
			refused := errors.Is(callErr, ErrToolRefused) && receipt == ""
			if (tool.Effect || receipt != "" || errors.Is(callErr, ErrMissingReceipt)) && !refused {
				batchErr = errors.Join(batchErr, fmt.Errorf("employee loop: tool %s failed: %w", call.Name, callErr))
			}
			continue
		}
		r.out.ToolOutcomes = append(r.out.ToolOutcomes, outcome)
		progress = true
		r.msgs = append(r.msgs, schema.ToolMessage(result.Content, call.NativeToolCallID, schema.WithToolName(call.Name)))
		if result.Terminal != nil {
			terminals = append(terminals, *result.Terminal)
		}
	}
	if progress {
		r.out.BudgetUsage.ConsecutiveErrors = 0
		r.lastErr = nil
	} else {
		r.out.BudgetUsage.ConsecutiveErrors++
		r.lastErr = noProgress
	}
	decision, err := aggregate(terminals)
	if err = errors.Join(batchErr, err); err != nil {
		return false, err
	}
	if decision.Kind != "" {
		r.out.Decision = decision
		return true, nil
	}
	return false, nil
}

func (r *run) execute(tool Tool, call ToolCall) (ToolResult, error) {
	if r.host == nil {
		return ToolResult{}, errors.New("employee loop: missing host for tool execution")
	}
	ctx := r.ctx
	if tool.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tool.Timeout)
		defer cancel()
	}
	started := time.Now()
	result, err := r.host.Execute(ctx, r.id, call)
	if r.cfg.OnToolCall != nil {
		r.cfg.OnToolCall(r.ctx, call, result, err, time.Since(started))
	}
	return result, err
}

// accept records an accepted response. A plain or terminal answer is
// progress; a tool batch counts as progress only once a call succeeds.
func (r *run) accept(resp *schema.Message, progress bool) {
	r.msgs = append(r.msgs, resp)
	r.out.BudgetUsage.ModelResponses++
	if progress {
		r.out.BudgetUsage.ConsecutiveErrors = 0
		r.lastErr = nil
	}
}

// fail records a rejected response or provider error and appends a repair
// instruction. The rejected output itself never enters the transcript.
func (r *run) fail(err error) {
	r.lastErr = err
	r.out.BudgetUsage.ConsecutiveErrors++
	var repair string
	if r.cfg.RepairText != nil {
		repair = r.cfg.RepairText(err)
	} else {
		repair = text(r.cfg.Locale, "repair.invalid")
		if r.cfg.RequireTerminalTool {
			repair += " " + r.terminalInstruction()
		}
	}
	if strings.TrimSpace(repair) != "" {
		r.msgs = append(r.msgs, schema.UserMessage(repair))
	}
	if r.cfg.OnModelFailure != nil {
		r.cfg.OnModelFailure(err)
	}
}

func (r *run) terminalInstruction() string {
	var names []string
	for _, t := range r.cfg.Tools {
		if t.Terminal != "" {
			names = append(names, t.Name)
		}
	}
	instruction := text(r.cfg.Locale, "terminal.required")
	if len(names) > 0 {
		instruction += " " + strings.ReplaceAll(text(r.cfg.Locale, "terminal.tools"), "{tools}", strings.Join(names, ", "))
	}
	return instruction
}

func (r *run) guard(text string) error {
	if r.cfg.ValidateText == nil {
		return nil
	}
	return r.cfg.ValidateText(text)
}

func (r *run) budgetErr() error {
	u, b := r.out, r.cfg.Budget
	switch {
	case u.BudgetUsage.ConsecutiveErrors >= b.MaxConsecutiveErrors:
		return ErrConsecutiveModelErrors
	case u.BudgetUsage.ModelResponses >= b.MaxModelResponses:
		return ErrModelResponseBudget
	case u.ModelCalls >= b.MaxPhysicalAttempts:
		return ErrPhysicalModelBudget
	}
	return nil
}

func parseCalls(resp *schema.Message) ([]ToolCall, error) {
	seen := map[string]bool{}
	calls := make([]ToolCall, 0, len(resp.ToolCalls))
	for _, c := range resp.ToolCalls {
		if (c.Type != "" && c.Type != "function") || strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.Function.Name) == "" {
			return nil, errors.New("employee loop: invalid or duplicate native tool call identity")
		}
		seen[c.ID] = true
		var args map[string]any
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil || args == nil {
			return nil, fmt.Errorf("employee loop: invalid arguments for %q", c.Function.Name)
		}
		calls = append(calls, ToolCall{NativeToolCallID: c.ID, Name: c.Function.Name, Arguments: args})
	}
	return calls, nil
}

// validate rejects a batch before any call runs. Every rejection is
// repairable.
func (r *run) validate(calls []ToolCall) error {
	var disposition Disposition
	for _, c := range calls {
		tool, ok := r.tools[c.Name]
		if !ok || !r.offered[c.Name] {
			return fmt.Errorf("employee loop: unknown tool %q", c.Name)
		}
		if tool.Terminal != "" {
			if disposition != "" && disposition != tool.Terminal {
				return fmt.Errorf("%w: %s and %s in one batch", ErrTerminalConflict, disposition, tool.Terminal)
			}
			disposition = tool.Terminal
		}
		if tool.Exclusive && len(calls) > 1 {
			return fmt.Errorf("employee loop: %s must be the only call of its batch", c.Name)
		}
		if problems := ValidateArguments(tool.Schema, c.Arguments); len(problems) > 0 {
			return fmt.Errorf("employee loop: invalid params for %s: %s", c.Name, strings.Join(problems, "; "))
		}
		if reply, ok := c.Arguments["reply"].(string); ok {
			if err := r.guard(reply); err != nil {
				return err
			}
		}
	}
	if r.cfg.ValidateBatch != nil {
		return r.cfg.ValidateBatch(calls)
	}
	return nil
}

// aggregate combines the terminal decisions of one batch; static conflicts
// were rejected by validate, this catches run-time ones.
func aggregate(terminals []Decision) (Decision, error) {
	if len(terminals) == 0 {
		return Decision{}, nil
	}
	kind := terminals[0].Kind
	seen := map[string]bool{}
	var replies []string
	for _, t := range terminals {
		if t.Kind != kind {
			return Decision{}, ErrTerminalConflict
		}
		if reply := strings.TrimSpace(t.Reply); reply != "" && !seen[reply] {
			replies = append(replies, reply)
			seen[reply] = true
		}
	}
	return Decision{Kind: kind, Reply: strings.Join(replies, "\n")}, nil
}
