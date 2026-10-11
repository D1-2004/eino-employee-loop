// Package hosttest checks a Host implementation against the loop's
// contract. A Host runs the checks from its own tests, against its real Host
// and model adapter over fake transports (a fake channel, an outbox in a
// test database, an httptest provider):
//
//	target := hosttest.Target{Host: host, Identity: id, Effects: outbox.Len}
//	target.Effect(t, loop.ToolCall{NativeToolCallID: "c1", Name: "reply", Arguments: args})
//	target.Refused(t, loop.ToolCall{NativeToolCallID: "c2", Name: "reply", Arguments: unknownSource})
//	target.FinalText(t, "hello")
//	target.Wake(t, cfg, messages, steps, loop.Decision{Kind: loop.Reply, Reply: "hi"})
//	target.ReusedID(t, cfg, messages, note, last, loop.Decision{Kind: loop.Quiet})
//	hosttest.Model(t, adapter, provider)
//
// Effect, FinalText, Wake and ReusedID need a Host state in which their
// calls have not run yet. A fake transport that calls Committed after each
// commit lets Effect cancel ctx right then. The local package's tests run
// the checks against its chat Host and the OpenAI model of cmd/eel, as the
// reference: all of them but ReusedID, which needs an effect that does not
// end the wake.
//
// The kit drives the Host and the model adapter only: what the Host does
// once Run has returned (keeping the Outcome of a failed wake, delivery) is
// left to its own tests.
package hosttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
)

// Target is a Host under test.
type Target struct {
	Host     loop.Host
	Identity loop.Identity
	// Effects reports how many effects the Host has committed so far, for
	// example the messages its fake channel sent or its outbox rows.
	Effects func() int
}

func (tg Target) ready(t testing.TB) {
	t.Helper()
	if tg.Host == nil || tg.Effects == nil {
		t.Fatal("hosttest: a Target needs a Host and an Effects counter")
	}
}

// Effect executes an effect call, then executes it again with the same
// identity and NativeToolCallID, as a re-claimed wake does. The first
// execution must commit an effect and return a receipt, also when ctx is
// cancelled right after the commit (see Committed). The replay must return
// the same result and commit nothing: the result is part of the next
// provider request, which a re-claim must send unchanged, and the receipt is
// the only proof of the effect. Effect returns the result.
func (tg Target) Effect(t testing.TB, call loop.ToolCall) loop.ToolResult {
	t.Helper()
	tg.ready(t)
	what := fmt.Sprintf("%s %s", call.Name, call.NativeToolCallID)
	before := tg.Effects()
	ctx, cancel := context.WithCancel(t.Context())
	c := &commit{cancel: cancel}
	first, err := tg.Host.Execute(context.WithValue(ctx, commitKey{}, c), tg.Identity, call)
	cancel()
	note := ""
	if c.seen.Load() {
		note = " (ctx was cancelled right after the commit, and the receipt is still due)"
	}
	receipt := strings.TrimSpace(first.Receipt) != ""
	if err != nil && (note == "" || !receipt) {
		t.Fatalf("hosttest: %s: the effect failed: %v%s", what, err, note)
	}
	if !receipt {
		t.Errorf("hosttest: %s: a committed effect returns a receipt%s", what, note)
	}
	committed := tg.Effects()
	if committed <= before {
		t.Errorf("hosttest: %s: no effect was committed (%d before, %d after)", what, before, committed)
	}
	again, err := tg.Host.Execute(t.Context(), tg.Identity, reparsed(t, call))
	tg.replayed(t, what, first, again, err, committed)
	return first
}

// commitKey marks the context Effect passes to the Host.
type commitKey struct{}

// commit is how Committed reaches Effect.
type commit struct {
	cancel context.CancelFunc
	seen   atomic.Bool
}

// Committed is called by a Host's fake transport (its fake channel or test
// outbox) right after it commits an effect, with the context it was given.
// Under Effect it cancels that context, as a deadline or a shutdown may
// right after a commit; elsewhere it does nothing, so the transport may call
// it on every commit.
func Committed(ctx context.Context) {
	if c, ok := ctx.Value(commitKey{}).(*commit); ok {
		c.seen.Store(true)
		c.cancel()
	}
}

// Refused executes a call the Host must refuse before any effect, for
// example one whose argument names no known source. The error must wrap
// loop.ErrToolRefused, the result must have no receipt and nothing may be
// committed; the loop then shows the error to the model, which may correct
// the call within the budget.
func (tg Target) Refused(t testing.TB, call loop.ToolCall) {
	t.Helper()
	tg.ready(t)
	what := fmt.Sprintf("%s %s", call.Name, call.NativeToolCallID)
	before := tg.Effects()
	result, err := tg.Host.Execute(t.Context(), tg.Identity, call)
	if !errors.Is(err, loop.ErrToolRefused) {
		t.Errorf("hosttest: %s: got error %v, want one wrapping loop.ErrToolRefused", what, err)
	}
	if strings.TrimSpace(result.Receipt) != "" {
		t.Errorf("hosttest: %s: a refusal has no receipt, got %q", what, result.Receipt)
	}
	if n := tg.Effects(); n != before {
		t.Errorf("hosttest: %s: a refusal committed %d effects", what, n-before)
	}
}

// FinalText offers text to the Host's AcceptFinalText, then offers it again
// with the same identity, as a re-claimed wake does. The first offer must
// return a terminal decision (the loop fails the wake with
// loop.ErrNoTerminalDecision otherwise) and commit the reply; the second
// must return the same result and commit nothing.
func (tg Target) FinalText(t testing.TB, text string) loop.ToolResult {
	t.Helper()
	tg.ready(t)
	final, ok := tg.Host.(loop.FinalTextHost)
	if !ok {
		t.Fatalf("hosttest: %T does not implement loop.FinalTextHost", tg.Host)
	}
	before := tg.Effects()
	first, err := final.AcceptFinalText(t.Context(), tg.Identity, text)
	if err != nil {
		t.Fatalf("hosttest: final text: %v", err)
	}
	if first.Terminal == nil {
		t.Errorf("hosttest: final text: an accepted final text returns a terminal decision")
	}
	committed := tg.Effects()
	if committed <= before {
		t.Errorf("hosttest: final text: the reply was not committed (%d effects before, %d after)", before, committed)
	}
	again, err := final.AcceptFinalText(t.Context(), tg.Identity, text)
	tg.replayed(t, "final text", first, again, err, committed)
	return first
}

func (tg Target) replayed(t testing.TB, what string, first, again loop.ToolResult, err error, committed int) {
	t.Helper()
	if err != nil {
		t.Errorf("hosttest: %s: the replay failed: %v", what, err)
	} else if !reflect.DeepEqual(first, again) {
		t.Errorf("hosttest: %s: the replay returned %s, want the recorded %s", what, show(again), show(first))
	}
	if n := tg.Effects(); n != committed {
		t.Errorf("hosttest: %s: the replay committed %d more effects", what, n-committed)
	}
}

// Wake runs a scripted wake through loop.New(cfg, model, Host).Run and checks
// that it ends with want and no error, and that every call that succeeded
// and must commit (an Effect tool, or the final text) committed an effect. It
// then runs the wake again with the same Host and responses, as a re-claim
// after a crash that replays its journal: the re-run must end with the same
// decision, send byte-identical provider requests and tools (Config
// callbacks and tool results are deterministic) and commit no further
// effect. It returns the first outcome.
func (tg Target) Wake(t testing.TB, cfg loop.Config, messages []*schema.Message, steps []looptest.Step, want loop.Decision) loop.Outcome {
	t.Helper()
	tg.ready(t)
	w := loop.Wake{Identity: tg.Identity, Messages: messages}
	var first, again []string
	before := tg.Effects()
	out, err := loop.New(cfg, recorder{m: looptest.New(steps...), log: &first}, tg.Host).Run(t.Context(), w)
	if err != nil {
		t.Fatalf("hosttest: the wake failed: %v", err)
	}
	if out.Decision != want {
		t.Errorf("hosttest: the wake decided %+v, want %+v", out.Decision, want)
	}
	committed := tg.Effects()
	if n := effectCalls(cfg, out); committed-before < n {
		t.Errorf("hosttest: %d effect calls succeeded but %d effects were committed; key effects by turn as well as by native call id, which a later response may reuse", n, committed-before)
	}
	re, err := loop.New(cfg, recorder{m: looptest.New(steps...), log: &again}, tg.Host).Run(t.Context(), w)
	if err != nil {
		t.Fatalf("hosttest: the re-claimed wake failed: %v", err)
	}
	if re.Decision != out.Decision {
		t.Errorf("hosttest: the re-claimed wake decided %+v, first %+v", re.Decision, out.Decision)
	}
	if !slices.Equal(first, again) {
		t.Errorf("hosttest: the re-claimed wake sent other provider requests:\nfirst: %q\nagain: %q", first, again)
	}
	if n := tg.Effects(); n != committed {
		t.Errorf("hosttest: the re-claimed wake committed %d more effects", n-committed)
	}
	return out
}

// effectCalls counts the calls of out that succeeded and must have committed
// an effect: calls to Effect tools and an accepted final text.
func effectCalls(cfg loop.Config, out loop.Outcome) int {
	n := 0
	for _, o := range out.ToolOutcomes {
		effect := o.Origin == loop.FinalTextOrigin || slices.ContainsFunc(cfg.Tools, func(tool loop.Tool) bool { return tool.Effect && tool.Name == o.ToolName })
		if effect && o.Error == "" {
			n++
		}
	}
	return n
}

// ReusedID checks that the Host keys effects by turn as well as by native
// call id: ids are unique within one response only, and providers that
// number them per response ("call_0") reuse them in every response. Through
// Wake it runs three responses, call twice with the same id and then last.
// call must be to an Effect tool of cfg that does not end the wake; both of
// its calls must commit, with distinct receipts. Wake runs a scripted model,
// so the Host takes the turn from cfg.OnModelCall (docs/host.md §5).
func (tg Target) ReusedID(t testing.TB, cfg loop.Config, messages []*schema.Message, call looptest.Call, last looptest.Step, want loop.Decision) loop.Outcome {
	t.Helper()
	i := slices.IndexFunc(cfg.Tools, func(tool loop.Tool) bool { return tool.Name == call.Name })
	if i < 0 || !cfg.Tools[i].Effect || cfg.Tools[i].Terminal != "" {
		t.Fatalf("hosttest: ReusedID needs a call to an Effect tool of cfg that does not end the wake, got %q", call.Name)
	}
	out := tg.Wake(t, cfg, messages, []looptest.Step{looptest.Tools(call), looptest.Tools(call), last}, want)
	var receipts []string
	for _, o := range out.ToolOutcomes {
		if o.NativeToolCallID == call.ID && o.ToolName == call.Name && o.Error == "" {
			receipts = append(receipts, o.Result.Receipt)
		}
	}
	if len(receipts) != 2 || receipts[0] == receipts[1] {
		t.Errorf("hosttest: two responses calling %s with native id %q got receipts %q, want two distinct ones", call.Name, call.ID, receipts)
	}
	return out
}

// recorder logs every provider request of a wake with the tools bound for
// it and its options, then answers from the script.
type recorder struct {
	m     *looptest.Model
	tools []*schema.ToolInfo
	log   *[]string
}

func (r recorder) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	r.tools = tools
	return r, nil
}

func (r recorder) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	raw, err := json.Marshal(map[string]any{"messages": in, "tools": r.tools, "options": model.GetCommonOptions(&model.Options{}, opts...)})
	if err != nil {
		return nil, err
	}
	*r.log = append(*r.log, string(raw))
	return r.m.Generate(ctx, in, opts...)
}

func (r recorder) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return r.m.Stream(ctx, in, opts...)
}

// Request is what a fake provider received, in provider-neutral terms.
type Request struct {
	// Tools are the names of the offered tools, in order.
	Tools []string
	// Forced is set when the request requires a tool call (OpenAI
	// tool_choice "required").
	Forced bool
}

// Provider is the fake provider behind a model adapter under test, run by
// the Host's test (for example an httptest server speaking the provider's
// protocol). It answers every request with the same native tool call batch,
// such as one call to hosttest_read with id "call_1".
type Provider struct {
	// Requests returns the requests received so far, oldest first.
	Requests func() []Request
	// FailNext makes the next request fail with an error an SDK would retry
	// (HTTP 500 for an HTTP provider).
	FailNext func()
	// Usage, if set, is the token usage the provider reports with every
	// answer.
	Usage *loop.Usage
}

// The tools Model binds.
var modelTools = []loop.Tool{
	{Name: "hosttest_read", Description: "Read a record.", Schema: map[string]any{
		"type": "object", "required": []any{"ref"}, "properties": map[string]any{"ref": map[string]any{"type": "string"}},
	}},
	{Name: "hosttest_reply", Description: "Reply in the conversation.", Schema: map[string]any{
		"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}},
	}},
}

// Model checks a Host's model adapter, the Eino model the Host gives
// loop.New, over a fake provider. Like the loop, it binds tools with
// WithTools right before each Generate.
//   - Each Generate sends exactly one provider request, also when the
//     provider fails: the loop counts every attempt against
//     Budget.MaxPhysicalAttempts and asks again itself, so the adapter must
//     not retry inside.
//   - The tools of the latest WithTools are the tools sent, in order, and a
//     narrower binding replaces them (Config.TurnTools). The unbound model,
//     which an attempt without tools uses, sends none.
//   - model.WithToolChoice(schema.ToolChoiceForced) forces a tool call
//     (Config.RequireNativeToolCall); without it the call is not forced.
//   - The response has native tool calls the loop accepts: a non-blank id
//     unique in the response, type "function" or empty, a name, JSON object
//     arguments and finish_reason "tool_calls", "stop" or empty. The same
//     provider answer gives the same ids: they are the provider's own.
//   - With Provider.Usage set, the response carries that usage in
//     ResponseMeta.Usage, which the loop sums into Outcome.Usage.
//
// How the adapter sends each tool's schema is its own choice
// (ToolInfo.Extra[loop.ExtraParameters] is offered, not required; it is
// the exact schema, see loop.ToolInfos).
func Model(t testing.TB, m model.ToolCallingChatModel, p Provider) {
	t.Helper()
	if m == nil || p.Requests == nil || p.FailNext == nil {
		t.Fatal("hosttest: Model needs a model and a Provider with Requests and FailNext")
	}
	infos, err := loop.ToolInfos(modelTools)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []*schema.Message{schema.SystemMessage("You are a test employee."), schema.UserMessage("Read record r1.")}
	// generate binds tools, if any, like the loop, and sends one request.
	generate := func(tools []*schema.ToolInfo, opts ...model.Option) (*schema.Message, Request, error) {
		t.Helper()
		bound := m
		if len(tools) > 0 {
			var err error
			if bound, err = m.WithTools(tools); err != nil {
				t.Fatalf("hosttest: WithTools: %v", err)
			}
		}
		n := len(p.Requests())
		resp, err := bound.Generate(t.Context(), msgs, opts...)
		sent := p.Requests()
		if len(sent) != n+1 {
			t.Fatalf("hosttest: one Generate sent %d provider requests, want 1", len(sent)-n)
		}
		return resp, sent[n], err
	}
	var ids [][]string
	for _, c := range []struct {
		name   string
		tools  []*schema.ToolInfo
		opts   []model.Option
		forced bool
	}{
		{"two tools", infos, nil, false},
		{"forced", infos, []model.Option{model.WithToolChoice(schema.ToolChoiceForced)}, true},
		{"narrowed", infos[:1], nil, false},
	} {
		resp, req, err := generate(c.tools, c.opts...)
		if err != nil {
			t.Fatalf("hosttest: %s: %v", c.name, err)
		}
		var names []string
		for _, info := range c.tools {
			names = append(names, info.Name)
		}
		if !slices.Equal(req.Tools, names) || req.Forced != c.forced {
			t.Errorf("hosttest: %s: the provider got tools %v forced=%v, want %v forced=%v", c.name, req.Tools, req.Forced, names, c.forced)
		}
		ids = append(ids, callIDs(t, c.name, resp))
		if p.Usage != nil {
			var got loop.Usage
			if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
				got = loop.Usage{PromptTokens: resp.ResponseMeta.Usage.PromptTokens, CompletionTokens: resp.ResponseMeta.Usage.CompletionTokens}
			}
			if got != *p.Usage {
				t.Errorf("hosttest: %s: the response carries usage %+v, want the provider's %+v in ResponseMeta.Usage", c.name, got, *p.Usage)
			}
		}
	}
	for _, got := range ids[1:] {
		if !slices.Equal(got, ids[0]) {
			t.Errorf("hosttest: the same provider answer gave call ids %v and %v; pass the provider's ids through", ids[0], got)
		}
	}
	// Only the request matters here: the answer calls a tool nobody offered.
	if _, req, _ := generate(nil); len(req.Tools) > 0 || req.Forced {
		t.Errorf("hosttest: the unbound model sent tools %v forced=%v, want none: an attempt without tools uses it", req.Tools, req.Forced)
	}
	p.FailNext()
	if _, _, err := generate(infos); err == nil {
		t.Errorf("hosttest: a failed provider request returned no error")
	}
}

func callIDs(t testing.TB, name string, resp *schema.Message) []string {
	t.Helper()
	if resp == nil || len(resp.ToolCalls) == 0 {
		t.Fatalf("hosttest: %s: the response has no native tool calls", name)
	}
	finish := ""
	if resp.ResponseMeta != nil {
		finish = resp.ResponseMeta.FinishReason
	}
	if finish != "tool_calls" && finish != "stop" && finish != "" {
		t.Errorf("hosttest: %s: finish_reason %q", name, finish)
	}
	var ids []string
	for _, c := range resp.ToolCalls {
		var args map[string]any
		switch {
		case c.Type != "" && c.Type != "function":
			t.Errorf("hosttest: %s: call %q has type %q", name, c.ID, c.Type)
		case strings.TrimSpace(c.ID) == "" || slices.Contains(ids, c.ID):
			t.Errorf("hosttest: %s: call id %q is blank or repeated", name, c.ID)
		case strings.TrimSpace(c.Function.Name) == "":
			t.Errorf("hosttest: %s: call %q has no name", name, c.ID)
		case json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil:
			t.Errorf("hosttest: %s: call %q arguments %q are not a JSON object", name, c.ID, c.Function.Arguments)
		}
		ids = append(ids, c.ID)
	}
	return ids
}

// reparsed returns call with its arguments decoded again, as a re-claim
// reads them back from its journal.
func reparsed(t testing.TB, call loop.ToolCall) loop.ToolCall {
	t.Helper()
	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		t.Fatalf("hosttest: arguments of %s: %v", call.Name, err)
	}
	call.Arguments = nil
	if err := json.Unmarshal(raw, &call.Arguments); err != nil {
		t.Fatalf("hosttest: arguments of %s: %v", call.Name, err)
	}
	return call
}

func show(r loop.ToolResult) string {
	terminal := "none"
	if r.Terminal != nil {
		terminal = fmt.Sprintf("%s %q", r.Terminal.Kind, r.Terminal.Reply)
	}
	return fmt.Sprintf("{content %q, receipt %q, terminal %s}", r.Content, r.Receipt, terminal)
}
