package hosttest_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/hosttest"
	lt "github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
)

// recorder collects the failures of a check instead of failing the test.
type recorder struct {
	testing.TB
	mu       sync.Mutex
	failures []string
}

func (r *recorder) Helper()                  {}
func (r *recorder) Context() context.Context { return context.Background() }
func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); runtime.Goexit() }
func (r *recorder) Fatal(args ...any)                 { r.Errorf("%s", fmt.Sprint(args...)); runtime.Goexit() }

// failures runs check and returns what it reported.
func failures(t *testing.T, check func(testing.TB)) string {
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		check(r)
	}()
	<-done
	return strings.Join(r.failures, "\n")
}

// outbox is a Host with two effect tools, send (ends the wake with a reply)
// and note (does not), and one read tool. Like a journal it keys results by
// turn and native call id, and it learns the turn from Config.OnModelCall
// (see config). The flags break one rule each.
type outbox struct {
	sent          []string
	journal       map[string]loop.ToolResult
	turn          int
	noTurn        bool // keys by native call id alone
	noDedupe      bool
	noReceipt     bool
	plainRefusal  bool
	noFinalDecide bool
	lateCtx       bool // reports ctx's error after a commit
}

// config adds the OnModelCall that tells the outbox the turn of each call.
func (o *outbox) config(cfg loop.Config) loop.Config {
	cfg.OnModelCall = func(_ context.Context, call loop.ModelCall) { o.turn = call.Attempt }
	return cfg
}

func (o *outbox) Execute(ctx context.Context, _ loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
	key := fmt.Sprintf("%d/%s", o.turn, call.NativeToolCallID)
	if o.noTurn {
		key = call.NativeToolCallID
	}
	if r, ok := o.journal[key]; ok && !o.noDedupe {
		return r, nil
	}
	text, _ := call.Arguments["text"].(string)
	switch {
	case call.Name == "read":
		return loop.ToolResult{Content: "record"}, nil
	case call.Name != "send" && call.Name != "note":
		return loop.ToolResult{}, errors.New("no such tool")
	case text == "":
		if o.plainRefusal {
			return loop.ToolResult{}, errors.New("empty text")
		}
		return loop.ToolResult{}, fmt.Errorf("%w: empty text", loop.ErrToolRefused)
	}
	r := o.commit(ctx, key, text, call.Name == "send")
	if o.lateCtx && ctx.Err() != nil {
		return loop.ToolResult{}, ctx.Err()
	}
	return r, nil
}

func (o *outbox) AcceptFinalText(ctx context.Context, id loop.Identity, text string) (loop.ToolResult, error) {
	if r, ok := o.journal["final/"+id.Receipt]; ok && !o.noDedupe {
		return r, nil
	}
	return o.commit(ctx, "final/"+id.Receipt, text, !o.noFinalDecide), nil
}

func (o *outbox) commit(ctx context.Context, key, text string, reply bool) loop.ToolResult {
	o.sent = append(o.sent, text)
	hosttest.Committed(ctx)
	r := loop.ToolResult{Content: fmt.Sprintf(`{"sent":%d}`, len(o.sent)), Receipt: fmt.Sprintf("msg-%d", len(o.sent))}
	if reply {
		r.Terminal = &loop.Decision{Kind: loop.Reply, Reply: text}
	}
	if o.noReceipt {
		r.Receipt = ""
	}
	if o.journal == nil {
		o.journal = map[string]loop.ToolResult{}
	}
	o.journal[key] = r
	return r
}

func target(o *outbox) hosttest.Target {
	return hosttest.Target{Host: o, Identity: loop.Identity{Employee: "e1", Scene: "s1", Receipt: "r1"}, Effects: func() int { return len(o.sent) }}
}

var send = loop.ToolCall{NativeToolCallID: "c1", Name: "send", Arguments: map[string]any{"text": "hi"}}

func TestHostChecks(t *testing.T) {
	for _, c := range []struct {
		name  string
		host  *outbox
		check func(hosttest.Target, testing.TB)
		want  string // "" passes
	}{
		{"effect", &outbox{}, func(tg hosttest.Target, t testing.TB) { tg.Effect(t, send) }, ""},
		{"effect replayed twice", &outbox{noDedupe: true}, func(tg hosttest.Target, t testing.TB) { tg.Effect(t, send) }, "the replay"},
		{"effect without receipt", &outbox{noReceipt: true}, func(tg hosttest.Target, t testing.TB) { tg.Effect(t, send) }, "returns a receipt"},
		{"effect lost to a late cancel", &outbox{lateCtx: true}, func(tg hosttest.Target, t testing.TB) { tg.Effect(t, send) }, "cancelled right after the commit"},
		{"read is no effect", &outbox{}, func(tg hosttest.Target, t testing.TB) {
			tg.Effect(t, loop.ToolCall{NativeToolCallID: "c1", Name: "read", Arguments: map[string]any{}})
		}, "no effect was committed"},
		{"refusal", &outbox{}, func(tg hosttest.Target, t testing.TB) {
			tg.Refused(t, loop.ToolCall{NativeToolCallID: "c1", Name: "send", Arguments: map[string]any{"text": ""}})
		}, ""},
		{"plain error is no refusal", &outbox{plainRefusal: true}, func(tg hosttest.Target, t testing.TB) {
			tg.Refused(t, loop.ToolCall{NativeToolCallID: "c1", Name: "send", Arguments: map[string]any{"text": ""}})
		}, "loop.ErrToolRefused"},
		{"an effect is no refusal", &outbox{}, func(tg hosttest.Target, t testing.TB) { tg.Refused(t, send) }, "committed 1 effects"},
		{"final text", &outbox{}, func(tg hosttest.Target, t testing.TB) { tg.FinalText(t, "hello") }, ""},
		{"final text sent twice", &outbox{noDedupe: true}, func(tg hosttest.Target, t testing.TB) { tg.FinalText(t, "hello") }, "the replay"},
		{"final text without decision", &outbox{noFinalDecide: true}, func(tg hosttest.Target, t testing.TB) { tg.FinalText(t, "hello") }, "terminal decision"},
	} {
		if got := failures(t, func(tb testing.TB) { c.check(target(c.host), tb) }); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: failures %q, want %q", c.name, got, c.want)
		}
	}
}

var (
	wakeTools = []loop.Tool{
		{Name: "send", Effect: true, Terminal: loop.Reply, PublicArgs: []string{"text"}, Schema: map[string]any{"type": "object", "required": []any{"text"}}},
		{Name: "note", Effect: true, Schema: map[string]any{"type": "object", "required": []any{"text"}}},
		{Name: "read", Schema: map[string]any{"type": "object"}},
	}
	wakeMessages = []*schema.Message{schema.SystemMessage("You are a test employee."), schema.UserMessage("hi")}
	sendHi       = lt.Tools(lt.Call{ID: "c2", Name: "send", Args: map[string]any{"text": "hi"}})
	saidHi       = loop.Decision{Kind: loop.Reply, Reply: "hi"}
)

// wake runs Wake against o with cfg and returns what it reported.
func wake(t *testing.T, o *outbox, cfg loop.Config, steps []lt.Step, want loop.Decision) string {
	return failures(t, func(tb testing.TB) { target(o).Wake(tb, o.config(cfg), wakeMessages, steps, want) })
}

func TestWakeCheck(t *testing.T) {
	cfg := loop.Config{Tools: wakeTools}
	steps := []lt.Step{lt.Tools(lt.Call{ID: "c1", Name: "read", Args: map[string]any{}}), sendHi}
	if got := wake(t, &outbox{}, cfg, steps, saidHi); got != "" {
		t.Errorf("a journaled Host fails: %s", got)
	}
	if got := wake(t, &outbox{noDedupe: true}, cfg, steps, saidHi); !strings.Contains(got, "committed 1 more effects") {
		t.Errorf("a Host that sends again on a re-claim: %q", got)
	}
	// A TurnTools that depends on state outside the wake breaks a re-claim.
	var calls int
	drifting := loop.Config{Tools: wakeTools, TurnTools: func(int, []loop.ToolOutcome) []string {
		calls++
		if calls > 2 {
			return []string{"send"}
		}
		return []string{"read", "send"}
	}}
	if got := wake(t, &outbox{}, drifting, steps, saidHi); !strings.Contains(got, "other provider requests") {
		t.Errorf("non-deterministic Config: %q", got)
	}
	if got := wake(t, &outbox{}, cfg, steps, loop.Decision{Kind: loop.Quiet}); !strings.Contains(got, "want {Kind:quiet") {
		t.Errorf("a wrong decision: %q", got)
	}
	// Two responses reuse the native id c1: a Host that keys effects by call
	// id alone swallows the second note.
	reused := []lt.Step{
		lt.Tools(lt.Call{ID: "c1", Name: "note", Args: map[string]any{"text": "a"}}),
		lt.Tools(lt.Call{ID: "c1", Name: "note", Args: map[string]any{"text": "b"}}),
		sendHi,
	}
	if got := wake(t, &outbox{}, cfg, reused, saidHi); got != "" {
		t.Errorf("a Host keyed by turn fails: %s", got)
	}
	if got := wake(t, &outbox{noTurn: true}, cfg, reused, saidHi); !strings.Contains(got, "3 effect calls succeeded but 2 effects were committed") {
		t.Errorf("a Host keyed by call id alone: %q", got)
	}
}

func TestReusedIDCheck(t *testing.T) {
	cfg := loop.Config{Tools: wakeTools}
	note := lt.Call{ID: "c1", Name: "note", Args: map[string]any{"text": "a"}}
	reused := func(o *outbox, call lt.Call) string {
		return failures(t, func(tb testing.TB) { target(o).ReusedID(tb, o.config(cfg), wakeMessages, call, sendHi, saidHi) })
	}
	if got := reused(&outbox{}, note); got != "" {
		t.Errorf("a Host keyed by turn fails: %s", got)
	}
	if got := reused(&outbox{noTurn: true}, note); !strings.Contains(got, "2 effects were committed") || !strings.Contains(got, "want two distinct ones") {
		t.Errorf("a Host keyed by call id alone: %q", got)
	}
	if got := reused(&outbox{}, lt.Call{ID: "c1", Name: "send", Args: map[string]any{"text": "a"}}); !strings.Contains(got, "does not end the wake") {
		t.Errorf("a terminal tool: %q", got)
	}
}

// provider is an in-process fake provider; adapter is a model adapter over
// it that breaks the rule its broken field names, if any.
type provider struct {
	mu       sync.Mutex
	requests []hosttest.Request
	fail     bool
}

func (p *provider) serve(req hosttest.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	ok := !p.fail
	p.fail = false
	return ok
}

func (p *provider) sent() []hosttest.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hosttest.Request(nil), p.requests...)
}

func (p *provider) failNext() { p.mu.Lock(); p.fail = true; p.mu.Unlock() }

// adapter binds tools on a copy of itself, so the unbound model keeps none.
type adapter struct {
	p      *provider
	broken string
	tools  []string
	first  *[]string // the first binding, shared by the copies
	minted *int      // ids minted so far, shared by the copies
}

func (a *adapter) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	switch a.broken {
	case "sticky tools":
		if *a.first == nil {
			*a.first = names
		}
		names = *a.first
	case "binds on itself":
		a.tools = names
		return a, nil
	}
	b := *a
	b.tools = names
	return &b, nil
}

func (a *adapter) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	o := model.GetCommonOptions(&model.Options{}, opts...)
	req := hosttest.Request{Tools: a.tools, Forced: o.ToolChoice != nil && *o.ToolChoice == schema.ToolChoiceForced && a.broken != "ignores force"}
	if !a.p.serve(req) && (a.broken != "retries" || !a.p.serve(req)) {
		return nil, errors.New("provider 500")
	}
	id := "call_1"
	if a.broken == "mints ids" {
		*a.minted++
		id = fmt.Sprintf("call_%d", *a.minted)
	}
	msg := lt.Tools(lt.Call{ID: id, Name: "hosttest_read", Args: map[string]any{"ref": "r1"}}).Message
	if a.broken != "drops usage" {
		msg.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}
	}
	return msg, nil
}

func (a *adapter) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("no stream")
}

func TestModelCheck(t *testing.T) {
	for _, c := range []struct {
		broken string
		want   string
	}{
		{"", ""},
		{"retries", "sent 2 provider requests"},
		{"ignores force", "forced=false, want [hosttest_read hosttest_reply] forced=true"},
		{"sticky tools", "narrowed"},
		{"binds on itself", "the unbound model sent tools [hosttest_read]"},
		{"mints ids", "pass the provider's ids through"},
		{"drops usage", "want the provider's {PromptTokens:5 CompletionTokens:2}"},
	} {
		p := &provider{}
		a := &adapter{p: p, broken: c.broken, first: new([]string), minted: new(int)}
		got := failures(t, func(tb testing.TB) {
			hosttest.Model(tb, a, hosttest.Provider{Requests: p.sent, FailNext: p.failNext, Usage: &loop.Usage{PromptTokens: 5, CompletionTokens: 2}})
		})
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%q: failures %q, want %q", c.broken, got, c.want)
		}
	}
}
