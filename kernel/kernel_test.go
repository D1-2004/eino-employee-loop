package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/kernel"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
)

// fakeHost records calls and answers like a durable Host.
type fakeHost struct {
	calls  []contract.ToolCall
	refuse map[string]bool
	noRcpt map[string]bool
}

func (h *fakeHost) Execute(_ context.Context, _ contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	h.calls = append(h.calls, call)
	if h.refuse[call.Name] {
		return contract.ToolResult{}, fmt.Errorf("%w: unknown source", contract.ErrToolRefused)
	}
	switch call.Name {
	case "reply":
		return contract.ToolResult{Content: `{"state":"accepted"}`, Terminal: &contract.Decision{Kind: contract.Reply, Reply: call.Arguments["reply"].(string)}}, nil
	case "stay_quiet":
		return contract.ToolResult{Content: `{}`, Terminal: &contract.Decision{Kind: contract.Quiet}}, nil
	case "dispatch_task":
		if h.noRcpt["dispatch_task"] {
			return contract.ToolResult{Content: `{}`, Terminal: &contract.Decision{Kind: contract.Dispatched}}, nil
		}
		reply, _ := call.Arguments["reply"].(string)
		return contract.ToolResult{Content: `{"receipt":"r1"}`, Receipt: "task-1", Terminal: &contract.Decision{Kind: contract.Dispatched, Reply: reply}}, nil
	case "read_task":
		return contract.ToolResult{Content: `{"status":"running"}`}, nil
	}
	return contract.ToolResult{}, fmt.Errorf("unexpected tool %s", call.Name)
}

func standardTools(t *testing.T, lane contract.Lane, session contract.SessionKind) []contract.ToolSpec {
	t.Helper()
	tools, err := contract.StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	return i18n.Default().ToolDescriptions("en", contract.ToolsFor(tools, lane, session))
}

func wake() kernel.Wake {
	return kernel.Wake{
		Identity:     contract.Identity{Employee: "e1", Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup}},
		Instructions: "You are a helpful colleague.",
		Blocks:       []contract.ContextBlock{{Kind: contract.BlockWindow, Text: "alice: hi"}},
	}
}

func run(t *testing.T, m *kt.Model, h contract.Host, mutate func(*kernel.Options)) (kernel.Result, error) {
	t.Helper()
	o := kernel.Options{Model: m, Host: h, Tools: standardTools(t, contract.LaneForeground, contract.SessionInteractive), Locale: "en"}
	if mutate != nil {
		mutate(&o)
	}
	return kernel.Run(context.Background(), o, wake())
}

func TestPlainReplyFinishes(t *testing.T) {
	res, err := run(t, kt.New(kt.Text("hello")), &fakeHost{}, nil)
	if err != nil || res.Decision.Kind != contract.Reply || res.Decision.Reply != "hello" || res.Usage.Attempts != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestResultCarriesTheTranscript(t *testing.T) {
	m := kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "t1"}}),
		kt.Text("done"),
	)
	res, err := run(t, m, &fakeHost{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var roles []schema.RoleType
	for _, msg := range res.Transcript {
		roles = append(roles, msg.Role)
	}
	if len(roles) < 4 || roles[len(roles)-1] != schema.Assistant || roles[len(roles)-2] != schema.Tool {
		t.Fatalf("transcript must end with the tool result and the final answer, got %v", roles)
	}
}

func TestDispatchNeedsReceipt(t *testing.T) {
	call := kt.Call{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p", "reply": "on it"}}
	res, err := run(t, kt.New(kt.Tools(call)), &fakeHost{}, nil)
	if err != nil || res.Decision.Kind != contract.Dispatched || res.Decision.Reply != "on it" || len(res.Receipts) != 1 || res.Receipts[0].ID != "task-1" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	_, err = run(t, kt.New(kt.Tools(call)), &fakeHost{noRcpt: map[string]bool{"dispatch_task": true}}, nil)
	if !errors.Is(err, contract.ErrMissingReceipt) {
		t.Fatalf("an effect without a receipt is never done: %v", err)
	}
}

func TestRefusalLetsModelCorrect(t *testing.T) {
	h := &fakeHost{refuse: map[string]bool{"read_task": true}}
	m := kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "bad", "task_ref": "t1"}}),
		kt.Tools(kt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "I could not find it."}}),
	)
	res, err := run(t, m, h, nil)
	if err != nil || res.Decision.Kind != contract.Reply || len(res.ToolOutcomes) != 2 || res.ToolOutcomes[0].Error == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// The refusal is paired with its call in the transcript.
	var toolMsgs int
	for _, msg := range m.Requests[1] {
		if msg.Role == schema.Tool {
			toolMsgs++
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("expected the refusal as a tool message, got %d", toolMsgs)
	}
}

func TestInvalidOutputIsRepairedAndBudgetIsEnforced(t *testing.T) {
	m := kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "reply", Args: map[string]any{"reply": "missing source"}}),
		kt.Truncated("cut"),
		kt.Fail(errors.New("provider 500")),
	)
	res, err := run(t, m, &fakeHost{}, nil)
	if !errors.Is(err, contract.ErrBudget) || !errors.Is(err, kernel.ErrConsecutiveErrors) {
		t.Fatalf("expected budget exhaustion, got %v", err)
	}
	if res.Usage.Attempts != 3 || m.Calls() != 3 {
		t.Fatalf("three attempts, got %+v", res.Usage)
	}
	last := m.Requests[1][len(m.Requests[1])-1]
	if last.Role != schema.User || !strings.Contains(last.Content, i18n.Default().Text("en", "kernel.repair.invalid")) {
		t.Fatalf("repair instruction expected, got %q", last.Content)
	}
	for _, msg := range m.Requests[2] {
		if msg.Role == schema.Assistant {
			t.Fatal("invalid model output must never enter the transcript")
		}
	}
}

func TestPermanentErrorStopsImmediately(t *testing.T) {
	rejected := errors.New("rejected parameter")
	_, err := run(t, kt.New(kt.Fail(rejected), kt.Text("never")), &fakeHost{}, func(o *kernel.Options) {
		o.Permanent = func(err error) bool { return errors.Is(err, rejected) }
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("permanent error: %v", err)
	}
}

func TestRequireTerminalAndForcedChoice(t *testing.T) {
	m := kt.New(kt.Text("private thought"), kt.Tools(kt.Call{ID: "c1", Name: "stay_quiet", Args: map[string]any{}}))
	res, err := kernel.Run(context.Background(), kernel.Options{
		Model: m, Host: &fakeHost{}, Tools: standardTools(t, contract.LaneBackground, contract.SessionObservation),
		RequireTerminal: true, ForceToolCall: true, Locale: "zh-CN",
	}, wake())
	if err != nil || res.Decision.Kind != contract.Quiet {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := m.Options[0].ToolChoice; got == nil || *got != schema.ToolChoiceForced {
		t.Fatalf("tool choice forced, got %v", got)
	}
	sys := m.Requests[0][0].Content
	if !strings.Contains(sys, i18n.Default().Text("zh-CN", "kernel.terminal.required")) {
		t.Fatalf("terminal instruction rendered in the wake locale: %q", sys)
	}
	if !strings.Contains(m.Requests[0][1].Content, i18n.Default().Text("zh-CN", "kernel.block.window")) {
		t.Fatalf("labels rendered in the wake locale: %q", m.Requests[0][1].Content)
	}
}

type finalText struct{ got string }

func (f *finalText) AcceptFinalText(_ context.Context, _ contract.Identity, text string) (contract.ToolResult, error) {
	f.got = text
	return contract.ToolResult{Receipt: "final-1", Terminal: &contract.Decision{Kind: contract.Reply, Reply: text}}, nil
}

func TestFinalTextHostAcceptsFirstPlainAnswer(t *testing.T) {
	ft := &finalText{}
	res, err := run(t, kt.New(kt.Text("2")), &fakeHost{}, func(o *kernel.Options) { o.RequireTerminal, o.FinalText = true, ft })
	if err != nil || res.Decision.Reply != "2" || ft.got != "2" || res.ToolOutcomes[0].Origin != kernel.FinalTextOrigin {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestStaticTerminalConflictIsRejectedBeforeAnyEffect(t *testing.T) {
	for name, batch := range map[string][]kt.Call{
		"reply+quiet": {
			{ID: "c1", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "x"}},
			{ID: "c2", Name: "stay_quiet", Args: map[string]any{}},
		},
		"dispatch+reply": {
			{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p"}},
			{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "x"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := &fakeHost{}
			var rejected error
			m := kt.New(kt.Tools(batch...), kt.Tools(kt.Call{ID: "c3", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "fixed"}}))
			res, err := run(t, m, h, func(o *kernel.Options) { o.Observer = rejectRecorder{err: &rejected} })
			if err != nil || res.Decision.Reply != "fixed" || !errors.Is(rejected, contract.ErrTerminalConflict) {
				t.Fatalf("repairable rejection: %+v %v rejected=%v", res, err, rejected)
			}
			if len(h.calls) != 1 || h.calls[0].Name != "reply" || len(res.Receipts) != 0 {
				t.Fatalf("no call of the conflicting batch ran: %+v", h.calls)
			}
		})
	}
}

// rejectRecorder keeps the last rejected batch error.
type rejectRecorder struct{ err *error }

func (rejectRecorder) ModelCall(context.Context, int, *schema.Message, error, time.Duration) {}
func (rejectRecorder) ToolCall(context.Context, contract.ToolCall, contract.ToolResult, error, time.Duration) {
}
func (r rejectRecorder) BatchRejected(_ context.Context, _ []contract.ToolCall, err error) {
	*r.err = err
}
func (rejectRecorder) ResponseFailed(context.Context, error) {}

// runtimeTerminalHost returns a terminal decision from a read tool, which
// only aggregate can catch.
type runtimeTerminalHost struct{ fakeHost }

func (h *runtimeTerminalHost) Execute(ctx context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	if call.Name == "read_task" {
		return contract.ToolResult{Content: `{}`, Terminal: &contract.Decision{Kind: contract.Quiet}}, nil
	}
	return h.fakeHost.Execute(ctx, id, call)
}

func TestRuntimeTerminalConflictFails(t *testing.T) {
	m := kt.New(kt.Tools(
		kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "t1"}},
		kt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "x"}},
	))
	if _, err := run(t, m, &runtimeTerminalHost{}, nil); !errors.Is(err, contract.ErrTerminalConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestExclusiveToolAndValidator(t *testing.T) {
	m := kt.New(
		kt.Tools(
			kt.Call{ID: "c1", Name: "stop_task", Args: map[string]any{"source_ref": "m1", "read_ref": "r1", "reply": "stopping"}},
			kt.Call{ID: "c2", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "t1"}},
		),
		kt.Text("ok"),
	)
	res, err := run(t, m, &fakeHost{}, func(o *kernel.Options) {
		o.TextGuard = func(text string) error {
			if strings.Contains(text, "<tool") {
				return errors.New("tool markup in public text")
			}
			return nil
		}
	})
	if err != nil || res.Decision.Reply != "ok" || len(res.ToolOutcomes) != 0 {
		t.Fatalf("exclusive batch must be rejected before any effect: res=%+v err=%v", res, err)
	}
}

func TestToolTimeoutUsesSpec(t *testing.T) {
	slow := contract.HostFunc(func(ctx context.Context, _ contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
		<-ctx.Done()
		return contract.ToolResult{}, ctx.Err()
	})
	tools := standardTools(t, contract.LaneForeground, contract.SessionInteractive)
	for i := range tools {
		tools[i].Timeout = 10 * time.Millisecond
	}
	m := kt.New(kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "t1"}}), kt.Text("timed out"))
	res, err := kernel.Run(context.Background(), kernel.Options{Model: m, Host: slow, Tools: tools}, wake())
	if err != nil || res.Decision.Reply != "timed out" || !strings.Contains(res.ToolOutcomes[0].Error, "deadline") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
