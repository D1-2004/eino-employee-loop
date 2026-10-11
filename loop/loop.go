// Package loop runs one employee wake.
//
//	out, err := loop.New(cfg, model, host).Run(ctx, loop.Wake{Identity: id, Messages: msgs})
//
// A wake is: take the Host's frozen messages -> ask the model -> validate
// the native tool batch -> let the Host execute every call -> stop on a
// terminal decision or ask again within the budget. The Host owns context
// assembly, permissions, receipts and effects; the loop owns ordering,
// validation, budgets and the rule that nothing counts as done without a
// Host receipt.
//
// Since v0.3 a CloudWeGo Eino ADK ChatModelAgent runs the wake. Provider
// requests are byte-identical to v0.2, the hand-written loop, and behaviour
// is the same (testdata/parity.golden.gz pins both), except that:
//   - a panic in the Host, the Host model or any Config callback is
//     recovered by ADK and returned as Run's error ("panic error: <value>"
//     with the stack) instead of crashing the caller;
//   - the Host, the Host model and the Config callbacks run on ADK's run
//     goroutine while Run waits for it, so runtime.Goexit there (t.FailNow
//     in a test fake) ends only that goroutine and Run returns "the wake
//     ended without a decision";
//   - global Eino callback handlers also see ADK's own graph nodes (agent,
//     graph, lambdas, ToolsNode; no extra ChatModel or Tool callbacks), and
//     a wake that ends on a terminal tool reports them as failed with
//     "employee loop: wake decided";
//   - a nil wake message is rejected before any provider attempt.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const defaultFinalTextLimit = 8000

var (
	errNoMessage = errors.New("employee loop: model returned no message")
	// errAttempt is all ADK sees of a failed provider attempt: decide judges
	// the attempt itself, so ADK's own handling of special errors (its
	// interrupts, stream cancellation) never applies to the Host model's.
	errAttempt = errors.New("employee loop: provider attempt not accepted")
	// errDecided ends ADK's run once a batch reached a terminal decision. ADK's
	// ReturnDirectly cannot: it picks the call by tool name before it runs,
	// keeps the last match and also ends on a failed call, while a wake ends on
	// the Host's results.
	errDecided = errors.New("employee loop: wake decided")
)

// classified keeps the text of err, which hosts have persisted since v0.2,
// and adds a sentinel for errors.Is. err's own chain still matches.
type classified struct {
	err, sentinel error
}

func classify(sentinel, err error) error { return &classified{err: err, sentinel: sentinel} }

func (c *classified) Error() string   { return c.err.Error() }
func (c *classified) Unwrap() []error { return []error{c.sentinel, c.err} }

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

// run is the state of one wake. ADK calls the extension points one at a
// time on its run goroutine; Run reads the state after the run ended.
type run struct {
	ctx   context.Context
	cfg   Config
	model model.ToolCallingChatModel
	host  Host
	id    Identity
	tools map[string]Tool
	infos []*schema.ToolInfo
	// offered holds the tools of the current provider attempt.
	offered map[string]bool
	// msgs is the accepted conversation and the request the Host model
	// receives. ADK keeps its own copies, with message IDs in Extra.
	msgs      []*schema.Message
	out       Outcome
	lastErr   error
	attempted bool
	cur       attempt // the provider attempt decide judges
	batch     *batch  // the accepted batch ADK's ToolsNode runs
	done      bool
	err       error // the error the wake ends with; ADK reports it wrapped
}

// attempt is what one provider attempt returned.
type attempt struct {
	seen  bool
	resp  *schema.Message
	err   error
	fatal error
}

// batch collects the Host results of one accepted batch.
type batch struct {
	calls      map[string]ToolCall
	ran        int
	terminals  []Decision
	err        error // effect failures: the wake fails after the batch
	noProgress error
	progress   bool
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
	if slices.Contains(w.Messages, nil) {
		return Outcome{}, errors.New("employee loop: a wake message is nil")
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
	err = r.wake()
	r.out.Transcript = append([]*schema.Message(nil), r.msgs...)
	return r.out, err
}

// wake runs the wake as one ADK ChatModelAgent run: its ReAct graph (model
// -> tools -> model), its retry wrapper re-asking after a rejected response
// and its ToolsNode running the calls of a batch in order. The loop plugs
// into three extension points: the model the agent calls (provider), the
// ShouldRetry policy that judges every response (decide) and the tools of
// the ToolsNode (hostTool). The agent is built per wake: its tools close
// over this wake's identity and Host.
//
// Cancellation is the loop's: the wake ends on r.ctx after a provider
// attempt and before each call of a batch, as in v0.2, and the Host model,
// the Host and every Config callback get r.ctx itself.
func (r *run) wake() error {
	tools := make([]tool.BaseTool, 0, len(r.cfg.Tools))
	for i, t := range r.cfg.Tools {
		tools = append(tools, hostTool{r: r, tool: t, info: r.infos[i]})
	}
	// ADK runs without cancellation, and Eino callback handlers carried by
	// r.ctx do not see ADK's graph, as in v0.2.
	ctx := callbacks.InitCallbacks(context.WithoutCancel(r.ctx), nil)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:  "employee_loop",
		Model: provider{r},
		// ADK runs the calls of a batch in parallel by default.
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}},
		// The wake budgets end the run first; ADK's limits only back them up.
		// An iteration is one model node entry: one per accepted batch plus
		// the entry whose attempt finds the budget spent (max avoids an
		// overflow, which ADK would read as its default of 20).
		MaxIterations: max(r.cfg.Budget.MaxModelResponses, r.cfg.Budget.MaxModelResponses+1),
		ModelRetryConfig: &adk.ModelRetryConfig{
			MaxRetries:  r.cfg.Budget.MaxPhysicalAttempts,
			ShouldRetry: r.decide,
			// A repair is asked at once; a zero delay does not sleep.
			BackoffFunc: func(context.Context, int) time.Duration { return 0 },
		},
	})
	if err != nil {
		return err
	}
	// A wake never streams: ADK would emit a streamed response before
	// ShouldRetry judged it.
	events := agent.Run(ctx, &adk.AgentInput{Messages: detached(r.msgs)})
	var runErr error
	for event, ok := events.Next(); ok; event, ok = events.Next() {
		if event.Err != nil {
			runErr = event.Err
		}
	}
	switch {
	case r.err != nil:
		return r.err
	case r.done:
		return nil
	case runErr != nil:
		// A recovered panic, or an ADK failure outside the extension points.
		return runErr
	}
	// For example after runtime.Goexit in a callee.
	return errors.New("employee loop: the wake ended without a decision")
}

// provider is the model ADK calls. ADK places it innermost, below its retry
// and message-ID layers, so it sees every provider attempt and the
// provider's own message. It sends the transcript, not ADK's copies of it,
// and binds the tools of each attempt itself through the Host model's
// WithTools. ADK's options carry only its whole, never narrowed, tool list,
// which option-reading models such as eino-ext openai would prefer over the
// bound tools, so they are not forwarded.
type provider struct{ r *run }

// IsCallbacksEnabled keeps ADK from wrapping the Host model in a second
// ChatModel callback layer; the Host model reports its own callbacks.
func (provider) IsCallbacksEnabled() bool { return true }

func (p provider) Generate(_ context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	r := p.r
	r.cur = attempt{seen: true}
	if err := sameConversation(in, r.msgs); err != nil {
		r.cur.fatal = err
		return nil, errAttempt
	}
	if err := r.budgetErr(); err != nil {
		r.cur.fatal = errors.Join(ErrModelBudget, err, r.lastErr)
		return nil, errAttempt
	}
	m, opts, err := r.bind()
	if err != nil {
		r.cur.fatal = err
		return nil, errAttempt
	}
	r.out.ModelCalls++
	request := append([]*schema.Message(nil), r.msgs...)
	started := time.Now()
	resp, err := m.Generate(r.ctx, request, opts...)
	if r.cfg.OnModelCall != nil {
		r.cfg.OnModelCall(r.ctx, ModelCall{Attempt: r.out.ModelCalls, Request: request, Response: resp, Err: err, Elapsed: time.Since(started)})
	}
	r.cur.resp, r.cur.err = resp, err
	if err != nil || resp == nil {
		return nil, errAttempt
	}
	return assistant(resp), nil
}

// assistant returns resp as ADK may hold it. ADK's ToolsNode refuses a batch
// whose role is not assistant, while v0.2 never read the role; the
// transcript keeps resp itself, so requests are unchanged.
func assistant(resp *schema.Message) *schema.Message {
	if resp.Role == schema.Assistant {
		return resp
	}
	cp := *resp
	cp.Role = schema.Assistant
	return &cp
}

func (provider) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("employee loop: streaming is not used")
}

// bind offers the tools of the next provider attempt (all of them unless
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

// detached returns copies of messages for ADK's state, so ADK never holds
// (or stamps its message IDs on) the transcript's own messages.
func detached(msgs []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, len(msgs))
	for i, m := range msgs {
		cp := *m
		out[i] = &cp
	}
	return out
}

// sameConversation fails the wake if ADK's state and the transcript ever
// disagree, for example after an Eino upgrade changes how ADK keeps it.
func sameConversation(in, msgs []*schema.Message) error {
	diverged := errors.New("employee loop: the ADK conversation diverged from the transcript")
	if len(in) != len(msgs) {
		return diverged
	}
	for i, a := range in {
		b := msgs[i]
		if a == nil {
			return diverged
		}
		// ADK holds an accepted batch with the assistant role (see assistant).
		sameRole := a.Role == b.Role || a.Role == schema.Assistant && len(b.ToolCalls) > 0
		if !sameRole || a.Content != b.Content || a.ToolCallID != b.ToolCallID || a.ToolName != b.ToolName || len(a.ToolCalls) != len(b.ToolCalls) {
			return diverged
		}
		for j := range a.ToolCalls {
			if a.ToolCalls[j].ID != b.ToolCalls[j].ID {
				return diverged
			}
		}
	}
	return nil
}

// decide is ADK's ShouldRetry. It judges every provider attempt before any
// tool runs or any event leaves the agent: Retry asks again with the repair
// instruction, RewriteError ends the run and an empty decision accepts.
func (r *run) decide(_ context.Context, rc *adk.RetryContext) *adk.RetryDecision {
	cur := r.cur
	r.cur = attempt{}
	switch {
	case !cur.seen:
		return r.stop(errors.Join(errors.New("employee loop: ADK judged an attempt the provider never saw"), rc.Err))
	case cur.fatal != nil:
		return r.stop(cur.fatal)
	case r.ctx.Err() != nil:
		return r.stop(r.ctx.Err())
	case cur.err != nil && r.cfg.Permanent != nil && r.cfg.Permanent(cur.err):
		return r.stop(cur.err)
	case cur.err != nil:
		return r.repair(rc, cur.err)
	case cur.resp == nil:
		return r.repair(rc, classify(ErrIncompleteResponse, errNoMessage))
	}
	finish := ""
	if meta := cur.resp.ResponseMeta; meta != nil {
		finish = meta.FinishReason
		if meta.Usage != nil {
			r.out.Usage.PromptTokens += meta.Usage.PromptTokens
			r.out.Usage.CompletionTokens += meta.Usage.CompletionTokens
		}
	}
	switch calls := len(cur.resp.ToolCalls); {
	case calls > 0 && (finish == "tool_calls" || finish == "stop" || finish == ""):
		return r.acceptBatch(rc, cur.resp)
	case calls == 0 && (finish == "stop" || finish == ""):
		return r.text(rc, cur.resp)
	}
	return r.repair(rc, classify(ErrIncompleteResponse, fmt.Errorf("employee loop: incomplete model response: finish_reason=%q", finish)))
}

// repair records a rejected response or provider error and asks again; the
// next attempt checks the budget. The rejected output never enters the
// conversation.
func (r *run) repair(rc *adk.RetryContext, err error) *adk.RetryDecision {
	decision := &adk.RetryDecision{Retry: true}
	if msg := r.fail(err); msg != nil {
		decision.ModifiedInputMessages = append(slices.Clip(rc.InputMessages), detached([]*schema.Message{msg})...)
		decision.PersistModifiedInputMessages = true
	}
	return decision
}

func (r *run) stop(err error) *adk.RetryDecision {
	return &adk.RetryDecision{RewriteError: r.end(err)}
}

// end records the error the wake ends with; ADK wraps what it reports, so
// Run returns this one.
func (r *run) end(err error) error {
	if r.err == nil {
		r.err = err
	}
	return err
}

// text judges a plain answer.
func (r *run) text(rc *adk.RetryContext, resp *schema.Message) *adk.RetryDecision {
	text := strings.TrimSpace(resp.Content)
	if !r.cfg.RequireTerminalTool {
		if text == "" {
			return r.repair(rc, classify(ErrIncompleteResponse, errors.New("employee loop: a plain reply needs text")))
		}
		if err := r.guard(text); err != nil {
			return r.repair(rc, err)
		}
		r.accept(resp, true)
		r.out.Decision = Decision{Kind: Reply, Reply: text}
		r.done = true
		return &adk.RetryDecision{}
	}
	final, ok := r.host.(FinalTextHost)
	if !r.cfg.AcceptFinalText || !ok || text == "" || !r.finalTextAllowed() {
		return r.repair(rc, ErrTerminalRequired)
	}
	if len(text) > r.cfg.FinalTextLimit {
		return r.repair(rc, classify(ErrFinalTextRejected, fmt.Errorf("employee loop: final text exceeds %d bytes", r.cfg.FinalTextLimit)))
	}
	if err := r.guard(text); err != nil {
		return r.repair(rc, err)
	}
	// The Host may commit the reply, so a failure here ends the wake rather
	// than asking the model again.
	r.attempted = true
	result, err := final.AcceptFinalText(r.ctx, r.id, text)
	outcome := ToolOutcome{Origin: FinalTextOrigin, ToolName: FinalTextOrigin, Result: result}
	if err == nil && result.Terminal == nil {
		err = ErrNoTerminalDecision
	}
	if err != nil {
		outcome.Error = err.Error()
	}
	r.out.ToolOutcomes = append(r.out.ToolOutcomes, outcome)
	if result.Receipt != "" {
		r.out.Receipts = append(r.out.Receipts, Receipt{ToolName: FinalTextOrigin, ID: result.Receipt})
	}
	if err != nil {
		return r.stop(err)
	}
	r.accept(resp, true)
	r.out.Decision = *result.Terminal
	r.done = true
	return &adk.RetryDecision{}
}

// finalTextAllowed reports whether the current response may be final text:
// by default only the first, before any tool ran.
func (r *run) finalTextAllowed() bool {
	if r.cfg.FinalTextAllowed != nil {
		return r.cfg.FinalTextAllowed(r.out.ModelCalls, r.out.ToolOutcomes)
	}
	return r.out.ModelCalls == 1 && !r.attempted
}

// acceptBatch validates a native batch before any call runs; ADK then runs
// it through hostTool.
func (r *run) acceptBatch(rc *adk.RetryContext, resp *schema.Message) *adk.RetryDecision {
	calls, err := parseCalls(resp)
	if err == nil {
		err = r.validate(calls)
	}
	if err != nil {
		if r.cfg.OnBatchRejected != nil {
			r.cfg.OnBatchRejected(calls, err)
		}
		return r.repair(rc, err)
	}
	r.accept(resp, false)
	r.batch = &batch{calls: make(map[string]ToolCall, len(calls))}
	for _, c := range calls {
		r.batch.calls[c.NativeToolCallID] = c
	}
	if r.cfg.OnBatchAccepted != nil {
		r.cfg.OnBatchAccepted(calls)
	}
	return &adk.RetryDecision{}
}

// hostTool runs one Host tool in ADK's ToolsNode. A Host error is answered
// to the model as tool content, never returned to ADK: compose fails the
// whole run on a tool error, while a wake fails only after the batch and
// only on effect failures.
type hostTool struct {
	r    *run
	tool Tool
	info *schema.ToolInfo
}

// IsCallbacksEnabled keeps ADK from wrapping Host calls in Tool callbacks.
func (hostTool) IsCallbacksEnabled() bool { return true }

func (t hostTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

// InvokableRun executes the call with the arguments decide validated, and
// settles the batch after its last call.
func (t hostTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	r, b, id := t.r, t.r.batch, compose.GetToolCallID(ctx)
	switch {
	case r.err != nil:
		// The ToolsNode runs the remaining calls of a failed batch too.
		return "", r.err
	case b == nil || b.calls[id].Name != t.tool.Name:
		return "", r.end(fmt.Errorf("employee loop: tool call %q was not validated", id))
	case r.ctx.Err() != nil:
		return "", r.end(errors.Join(b.err, r.ctx.Err()))
	}
	call := b.calls[id]
	r.attempted = true
	result, callErr := r.execute(t.tool, call)
	if result.Terminal != nil {
		d := *result.Terminal
		result.Terminal = &d
	}
	outcome := ToolOutcome{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, Result: result}
	receipt := strings.TrimSpace(result.Receipt)
	if receipt != "" {
		r.out.Receipts = append(r.out.Receipts, Receipt{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, ID: result.Receipt})
	}
	if callErr == nil && t.tool.Effect && receipt == "" {
		callErr = fmt.Errorf("%w: %s", ErrMissingReceipt, call.Name)
	}
	if callErr == nil && result.Terminal != nil && result.Terminal.Kind == Dispatched && receipt == "" {
		callErr = fmt.Errorf("%w: dispatch", ErrMissingReceipt)
	}
	content := result.Content
	if callErr != nil {
		outcome.Error = callErr.Error()
		raw, _ := json.Marshal(map[string]string{"error": callErr.Error()})
		content = string(raw)
		b.noProgress = errors.Join(b.noProgress, callErr)
		refused := errors.Is(callErr, ErrToolRefused) && receipt == ""
		if (t.tool.Effect || receipt != "" || errors.Is(callErr, ErrMissingReceipt)) && !refused {
			b.err = errors.Join(b.err, classify(ErrToolFailed, fmt.Errorf("employee loop: tool %s failed: %w", call.Name, callErr)))
		}
	} else {
		if r.cfg.Progress == nil || r.cfg.Progress(call) {
			b.progress = true
		}
		if result.Terminal != nil {
			b.terminals = append(b.terminals, *result.Terminal)
		}
	}
	r.out.ToolOutcomes = append(r.out.ToolOutcomes, outcome)
	r.msgs = append(r.msgs, schema.ToolMessage(content, call.NativeToolCallID, schema.WithToolName(call.Name)))
	b.ran++
	if b.ran == len(b.calls) {
		if err := r.afterBatch(); err != nil {
			return "", err
		}
	}
	return content, nil
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

// afterBatch settles a batch on the Host's results once its last call ran,
// before ADK asks the model again.
func (r *run) afterBatch() error {
	b := r.batch
	r.batch = nil
	if b.progress {
		r.out.BudgetUsage.ConsecutiveErrors = 0
		r.lastErr = nil
	} else {
		r.out.BudgetUsage.ConsecutiveErrors++
		r.lastErr = b.noProgress
	}
	decision, err := aggregate(b.terminals)
	if err = errors.Join(b.err, err); err != nil {
		return r.end(err)
	}
	if decision.Kind != "" {
		r.out.Decision = decision
		r.done = true
		return errDecided
	}
	return nil
}

// accept records an accepted response. A plain or terminal answer is
// progress; a tool batch counts as progress only once a call succeeds that
// Config.Progress counts.
func (r *run) accept(resp *schema.Message, progress bool) {
	r.msgs = append(r.msgs, resp)
	r.out.BudgetUsage.ModelResponses++
	if progress {
		r.out.BudgetUsage.ConsecutiveErrors = 0
		r.lastErr = nil
	}
}

// fail records a rejected response or provider error and returns the
// repair instruction it appended, if any.
func (r *run) fail(err error) *schema.Message {
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
	var msg *schema.Message
	if strings.TrimSpace(repair) != "" {
		msg = schema.UserMessage(repair)
		r.msgs = append(r.msgs, msg)
	}
	if r.cfg.OnModelFailure != nil {
		r.cfg.OnModelFailure(err)
	}
	return msg
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

// budgetErr checks the wake budgets before a provider attempt. ADK's own
// counters cannot: its retry count restarts at every model node entry and
// it has no error streak.
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
			return nil, ErrInvalidToolCall
		}
		seen[c.ID] = true
		var args map[string]any
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil || args == nil {
			return nil, classify(ErrInvalidArguments, fmt.Errorf("employee loop: invalid arguments for %q", c.Function.Name))
		}
		calls = append(calls, ToolCall{NativeToolCallID: c.ID, Name: c.Function.Name, Arguments: args})
	}
	return calls, nil
}

// validate rejects a batch before any call runs. Every rejection is
// repairable. ADK has none of these rules: its ToolsNode runs any
// registered tool, ends the run on an unknown one and only unmarshals
// arguments.
func (r *run) validate(calls []ToolCall) error {
	var disposition Disposition
	for _, c := range calls {
		tool, ok := r.tools[c.Name]
		if !ok || !r.offered[c.Name] {
			return classify(ErrUnknownTool, fmt.Errorf("employee loop: unknown tool %q", c.Name))
		}
		if tool.Terminal != "" {
			if disposition != "" && disposition != tool.Terminal {
				return fmt.Errorf("%w: %s and %s in one batch", ErrTerminalConflict, disposition, tool.Terminal)
			}
			disposition = tool.Terminal
		}
		if tool.Exclusive && len(calls) > 1 {
			return classify(ErrExclusiveTool, fmt.Errorf("employee loop: %s must be the only call of its batch", c.Name))
		}
		if problems := r.arguments(tool, c.Arguments); len(problems) > 0 {
			return classify(ErrInvalidArguments, fmt.Errorf("employee loop: invalid params for %s: %s", c.Name, strings.Join(problems, "; ")))
		}
		for _, name := range tool.publicArgs() {
			if text, ok := c.Arguments[name].(string); ok {
				if err := r.guard(text); err != nil {
					return err
				}
			}
		}
	}
	if r.cfg.ValidateBatch != nil {
		return r.cfg.ValidateBatch(calls)
	}
	return nil
}

// arguments checks one call's arguments with the Host's rules, or the
// built-in ones when the Host has none.
func (r *run) arguments(tool Tool, args map[string]any) []string {
	if r.cfg.ValidateArguments != nil {
		return r.cfg.ValidateArguments(tool, args)
	}
	return ValidateArguments(tool.Schema, args)
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
