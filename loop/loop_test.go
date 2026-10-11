package loop_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/loop"
	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
)

var obj = map[string]any{"type": "object"}

func strArgs(required ...string) map[string]any {
	props := map[string]any{}
	for _, r := range required {
		props[r] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var tools = []loop.Tool{
	{Name: "reply", Terminal: loop.Reply, Schema: strArgs("source_ref", "reply")},
	{Name: "stay_quiet", Terminal: loop.Quiet, Schema: obj},
	{Name: "dispatch_task", Effect: true, Schema: strArgs("source_ref", "goal", "reply")},
	{Name: "read_task", Schema: strArgs("task_ref")},
	{Name: "stop_task", Effect: true, Exclusive: true, Schema: strArgs("task_ref")},
}

// fakeHost answers like a durable Host.
type fakeHost struct {
	calls     []loop.ToolCall
	refuse    map[string]bool
	noReceipt bool
}

func (h *fakeHost) Execute(_ context.Context, _ loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
	h.calls = append(h.calls, call)
	if h.refuse[call.Name] {
		return loop.ToolResult{}, fmt.Errorf("%w: unknown task", loop.ErrToolRefused)
	}
	switch call.Name {
	case "reply":
		return loop.ToolResult{Content: `{"state":"accepted"}`, Receipt: "out-1", Terminal: &loop.Decision{Kind: loop.Reply, Reply: call.Arguments["reply"].(string)}}, nil
	case "stay_quiet":
		return loop.ToolResult{Content: `{}`, Terminal: &loop.Decision{Kind: loop.Quiet}}, nil
	case "dispatch_task":
		reply, _ := call.Arguments["reply"].(string)
		if h.noReceipt {
			return loop.ToolResult{Content: `{}`, Terminal: &loop.Decision{Kind: loop.Dispatched, Reply: reply}}, nil
		}
		return loop.ToolResult{Content: `{"task":"t1"}`, Receipt: "task-1", Terminal: &loop.Decision{Kind: loop.Dispatched, Reply: reply}}, nil
	case "read_task":
		return loop.ToolResult{Content: `{"status":"running"}`}, nil
	}
	return loop.ToolResult{}, fmt.Errorf("unexpected tool %s", call.Name)
}

func wake() loop.Wake {
	return loop.Wake{Identity: loop.Identity{Employee: "e1", Scene: "s1"}, Messages: []*schema.Message{schema.SystemMessage("You are a helpful colleague."), schema.UserMessage("alice: hi")}}
}

func run(t *testing.T, m *lt.Model, h loop.Host, mutate func(*loop.Config)) (loop.Outcome, error) {
	t.Helper()
	cfg := loop.Config{Tools: tools}
	if mutate != nil {
		mutate(&cfg)
	}
	return loop.New(cfg, m, h).Run(context.Background(), wake())
}

func TestPlainReplyFinishes(t *testing.T) {
	out, err := run(t, lt.New(lt.Text("hello")), &fakeHost{}, nil)
	if err != nil || out.Kind != loop.Reply || out.Reply != "hello" || out.ModelCalls != 1 || out.BudgetUsage.ModelResponses != 1 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if n := len(out.Transcript); n != 3 || out.Transcript[n-1].Content != "hello" {
		t.Fatalf("transcript %v", out.Transcript)
	}
}

func TestDispatchNeedsReceipt(t *testing.T) {
	call := lt.Call{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "reply": "on it"}}
	out, err := run(t, lt.New(lt.Tools(call)), &fakeHost{}, nil)
	if err != nil || out.Kind != loop.Dispatched || out.Reply != "on it" || len(out.Receipts) != 1 || out.Receipts[0].ID != "task-1" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if _, err := run(t, lt.New(lt.Tools(call)), &fakeHost{noReceipt: true}, nil); !errors.Is(err, loop.ErrMissingReceipt) {
		t.Fatalf("an effect without a receipt is never done: %v", err)
	}
}

func TestRefusalLetsTheModelCorrect(t *testing.T) {
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "bad"}}),
		lt.Tools(lt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "I could not find it."}}),
	)
	out, err := run(t, m, &fakeHost{refuse: map[string]bool{"read_task": true}}, nil)
	if err != nil || out.Kind != loop.Reply || len(out.ToolOutcomes) != 2 || out.ToolOutcomes[0].Error == "" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var toolMsgs int
	for _, msg := range m.Requests[1] {
		if msg.Role == schema.Tool {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("the refusal is paired with its call, got %d tool messages", toolMsgs)
	}
}

func TestInvalidOutputIsRepairedWithinTheBudget(t *testing.T) {
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"reply": "missing source"}}),
		lt.Truncated("cut"),
		lt.Fail(errors.New("provider 500")),
	)
	var failures []error
	out, err := run(t, m, &fakeHost{}, func(c *loop.Config) { c.OnModelFailure = func(e error) { failures = append(failures, e) } })
	if !errors.Is(err, loop.ErrModelBudget) || !errors.Is(err, loop.ErrConsecutiveModelErrors) {
		t.Fatalf("budget exhaustion, got %v", err)
	}
	if out.ModelCalls != 3 || m.Calls() != 3 || len(failures) != 3 {
		t.Fatalf("three attempts and failures: %+v %d", out, len(failures))
	}
	last := m.Requests[1][len(m.Requests[1])-1]
	if last.Role != schema.User || !strings.Contains(last.Content, "not executed") {
		t.Fatalf("repair instruction expected, got %q", last.Content)
	}
	for _, msg := range m.Requests[2] {
		if msg.Role == schema.Assistant {
			t.Fatal("invalid output never enters the transcript")
		}
	}
}

func TestPermanentErrorStopsAtOnce(t *testing.T) {
	rejected := errors.New("rejected parameter")
	_, err := run(t, lt.New(lt.Fail(rejected), lt.Text("never")), &fakeHost{}, func(c *loop.Config) {
		c.Permanent = func(err error) bool { return errors.Is(err, rejected) }
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("permanent error: %v", err)
	}
}

func TestTerminalRequiredForcedChoiceAndLocale(t *testing.T) {
	m := lt.New(lt.Text("private thought"), lt.Tools(lt.Call{ID: "c1", Name: "stay_quiet", Args: map[string]any{}}))
	out, err := run(t, m, &fakeHost{}, func(c *loop.Config) {
		c.RequireTerminalTool, c.RequireNativeToolCall, c.Locale = true, true, "zh-CN"
	})
	if err != nil || out.Kind != loop.Quiet {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if got := m.Options[0].ToolChoice; got == nil || *got != schema.ToolChoiceForced {
		t.Fatalf("tool choice forced, got %v", got)
	}
	repair := m.Requests[1][len(m.Requests[1])-1].Content
	if !strings.Contains(repair, "终态") || !strings.Contains(repair, "reply, stay_quiet") {
		t.Fatalf("repair rendered in the wake locale with terminal tools: %q", repair)
	}
}

func TestRepairTextIsInjectable(t *testing.T) {
	m := lt.New(lt.Truncated("cut"), lt.Text("ok"))
	_, err := run(t, m, &fakeHost{}, func(c *loop.Config) { c.RepairText = func(error) string { return "HOST REPAIR" } })
	if err != nil || m.Requests[1][len(m.Requests[1])-1].Content != "HOST REPAIR" {
		t.Fatalf("host repair text: %v %q", err, m.Requests[1][len(m.Requests[1])-1].Content)
	}
}

type finalText struct {
	got string
	err error
}

func (f *finalText) Execute(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
	return loop.ToolResult{}, errors.New("no tools")
}

func (f *finalText) AcceptFinalText(_ context.Context, _ loop.Identity, text string) (loop.ToolResult, error) {
	f.got = text
	if f.err != nil {
		return loop.ToolResult{}, f.err
	}
	return loop.ToolResult{Receipt: "final-1", Terminal: &loop.Decision{Kind: loop.Reply, Reply: text}}, nil
}

func TestFinalTextHostAcceptsTheFirstPlainAnswer(t *testing.T) {
	ft := &finalText{}
	out, err := run(t, lt.New(lt.Text("2")), ft, func(c *loop.Config) { c.RequireTerminalTool, c.AcceptFinalText = true, true })
	if err != nil || out.Reply != "2" || ft.got != "2" || out.ToolOutcomes[0].Origin != loop.FinalTextOrigin {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	ft = &finalText{err: errors.New("journal down")}
	if _, err := run(t, lt.New(lt.Text("2"), lt.Text("3")), ft, func(c *loop.Config) { c.RequireTerminalTool, c.AcceptFinalText = true, true }); err == nil || !strings.Contains(err.Error(), "journal down") {
		t.Fatalf("a Host failure accepting final text ends the wake: %v", err)
	}
}

func TestConflictingTerminalsAreRejectedBeforeEffects(t *testing.T) {
	h := &fakeHost{}
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "x"}}, lt.Call{ID: "c2", Name: "stay_quiet", Args: map[string]any{}}),
		lt.Tools(lt.Call{ID: "c3", Name: "stay_quiet", Args: map[string]any{}}),
	)
	var rejected []error
	out, err := run(t, m, h, func(c *loop.Config) {
		c.OnBatchRejected = func(_ []loop.ToolCall, e error) { rejected = append(rejected, e) }
	})
	if err != nil || out.Kind != loop.Quiet || len(h.calls) != 1 || len(rejected) != 1 || !errors.Is(rejected[0], loop.ErrTerminalConflict) {
		t.Fatalf("out=%+v err=%v calls=%d rejected=%v", out, err, len(h.calls), rejected)
	}
}

func TestExclusiveAndHostValidation(t *testing.T) {
	h := &fakeHost{}
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "stop_task", Args: map[string]any{"task_ref": "t1"}}, lt.Call{ID: "c2", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}),
		lt.Tools(lt.Call{ID: "c3", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "<tool>x</tool>"}}),
		lt.Text("ok"),
	)
	out, err := run(t, m, h, func(c *loop.Config) {
		c.ValidateText = func(text string) error {
			if strings.Contains(text, "<tool") {
				return errors.New("tool markup in public text")
			}
			return nil
		}
	})
	if err != nil || out.Reply != "ok" || len(h.calls) != 0 {
		t.Fatalf("rejected batches run nothing: out=%+v err=%v calls=%d", out, err, len(h.calls))
	}
}

func TestToolTimeoutAndHooks(t *testing.T) {
	slow := loop.HostFunc(func(ctx context.Context, _ loop.Identity, _ loop.ToolCall) (loop.ToolResult, error) {
		<-ctx.Done()
		return loop.ToolResult{}, ctx.Err()
	})
	timed := append([]loop.Tool(nil), tools...)
	for i := range timed {
		timed[i].Timeout = 10 * time.Millisecond
	}
	var modelCalls, toolCalls int
	cfg := loop.Config{Tools: timed,
		OnModelCall: func(context.Context, loop.ModelCall) { modelCalls++ },
		OnToolCall:  func(context.Context, loop.ToolCall, loop.ToolResult, error, time.Duration) { toolCalls++ },
	}
	m := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), lt.Text("timed out"))
	out, err := loop.New(cfg, m, slow).Run(context.Background(), wake())
	if err != nil || out.Reply != "timed out" || !strings.Contains(out.ToolOutcomes[0].Error, "deadline") || modelCalls != 2 || toolCalls != 1 {
		t.Fatalf("out=%+v err=%v hooks=%d/%d", out, err, modelCalls, toolCalls)
	}
}

func TestTurnToolsNarrowEachCall(t *testing.T) {
	h := &fakeHost{}
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}),
		lt.Tools(lt.Call{ID: "c2", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}),
		lt.Tools(lt.Call{ID: "c3", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
	)
	var seen []int
	out, err := run(t, m, h, func(c *loop.Config) {
		c.TurnTools = func(call int, outcomes []loop.ToolOutcome) []string {
			seen = append(seen, len(outcomes))
			if call == 1 {
				return []string{"read_task", "reply"}
			}
			return []string{"reply"}
		}
	})
	if err != nil || out.Reply != "done" || len(h.calls) != 2 || h.calls[1].Name != "reply" {
		t.Fatalf("a call to a tool not offered is rejected before any effect: out=%+v err=%v calls=%v", out, err, h.calls)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "reply" || len(seen) != 3 || seen[1] != 1 {
		t.Fatalf("tools per call: last=%v seen=%v", m.Tools, seen)
	}
}

func TestTokenUsageIsSummed(t *testing.T) {
	step := lt.Text("hi")
	step.Message.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: 7, CompletionTokens: 2}
	out, err := run(t, lt.New(step), &fakeHost{}, nil)
	if err != nil || out.Usage != (loop.Usage{PromptTokens: 7, CompletionTokens: 2}) {
		t.Fatalf("usage %+v err=%v", out.Usage, err)
	}
}
