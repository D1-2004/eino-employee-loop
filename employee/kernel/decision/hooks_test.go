package decision_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	lt "github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
)

func TestProgressDecidesWhatResetsTheErrorStreak(t *testing.T) {
	script := func() *lt.Model {
		return lt.New(
			lt.Truncated("cut"),
			lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}),
			lt.Tools(lt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
		)
	}
	budget := loop.Budget{MaxModelResponses: 5, MaxConsecutiveErrors: 2, MaxPhysicalAttempts: 5}
	out, err := run(t, script(), &fakeHost{}, func(c *loop.Config) { c.Budget = budget })
	if err != nil || out.Reply != "done" {
		t.Fatalf("nil counts every successful call: out=%+v err=%v", out, err)
	}

	var asked []string
	m := script()
	out, err = run(t, m, &fakeHost{}, func(c *loop.Config) {
		c.Budget = budget
		c.Progress = func(call loop.ToolCall) bool {
			asked = append(asked, call.NativeToolCallID)
			return call.Name != "read_task"
		}
	})
	if !errors.Is(err, loop.ErrModelBudget) || !errors.Is(err, loop.ErrConsecutiveModelErrors) || m.Calls() != 2 || out.BudgetUsage.ConsecutiveErrors != 2 {
		t.Fatalf("a successful call that is not progress keeps the streak: out=%+v err=%v", out, err)
	}
	if len(out.ToolOutcomes) != 1 || out.ToolOutcomes[0].Error != "" || !slices.Equal(asked, []string{"c1"}) {
		t.Fatalf("the read succeeded and was asked about once: %+v asked=%v", out.ToolOutcomes, asked)
	}
}

// readingFinalText executes calls like fakeHost and accepts final text.
type readingFinalText struct {
	fakeHost
	texts []string
}

func (h *readingFinalText) AcceptFinalText(_ context.Context, _ loop.Identity, text string) (loop.ToolResult, error) {
	h.texts = append(h.texts, text)
	return loop.ToolResult{Receipt: "final-1", Terminal: &loop.Decision{Kind: loop.Reply, Reply: text}}, nil
}

func TestFinalTextAllowedChoosesTheResponses(t *testing.T) {
	read := lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}})
	afterRead := func(call int, outcomes []loop.ToolOutcome) bool {
		return call >= 2 && len(outcomes) == 1 && outcomes[0].ToolName == "read_task" && outcomes[0].Error == ""
	}
	cases := []struct {
		name     string
		steps    []lt.Step
		allowed  func(int, []loop.ToolOutcome) bool
		asked    []string
		failures []error
		texts    []string
		kind     loop.Disposition
	}{
		{"call 2 after a read", []lt.Step{read, lt.Text("runs")}, afterRead, []string{"2:1"}, nil, []string{"runs"}, loop.Reply},
		{"checked like the first", []lt.Step{read, lt.Text("BAD"), lt.Text("toolong"), lt.Text("ok")}, afterRead,
			[]string{"2:1", "3:1", "4:1"}, []error{errBad, loop.ErrFinalTextRejected}, []string{"ok"}, loop.Reply},
		{"call 1 refused", []lt.Step{lt.Text("2"), lt.Tools(lt.Call{ID: "c1", Name: "stay_quiet", Args: map[string]any{}})},
			func(int, []loop.ToolOutcome) bool { return false }, []string{"1:0"}, []error{loop.ErrTerminalRequired}, nil, loop.Quiet},
	}
	for _, c := range cases {
		h := &readingFinalText{}
		var asked []string
		var failures []error
		var batches int
		out, err := run(t, lt.New(c.steps...), h, func(cfg *loop.Config) {
			cfg.Budget = loop.Budget{MaxModelResponses: 5, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 5}
			cfg.RequireTerminalTool, cfg.AcceptFinalText, cfg.FinalTextLimit = true, true, 4
			cfg.ValidateText = func(text string) error {
				if text == "BAD" {
					return errBad
				}
				return nil
			}
			cfg.FinalTextAllowed = func(call int, outcomes []loop.ToolOutcome) bool {
				asked = append(asked, fmt.Sprintf("%d:%d", call, len(outcomes)))
				return c.allowed(call, outcomes)
			}
			cfg.OnModelFailure = func(err error) { failures = append(failures, err) }
			cfg.OnBatchAccepted = func([]loop.ToolCall) { batches++ }
		})
		if err != nil || out.Kind != c.kind || !slices.Equal(h.texts, c.texts) || !slices.Equal(asked, c.asked) || len(failures) != len(c.failures) {
			t.Errorf("%s: out=%+v err=%v texts=%v asked=%v failures=%v", c.name, out, err, h.texts, asked, failures)
			continue
		}
		for i, want := range c.failures {
			if !errors.Is(failures[i], want) {
				t.Errorf("%s: failure %d is %v, want %v", c.name, i, failures[i], want)
			}
		}
		// Accepted final text is not a batch.
		if c.texts != nil && (batches != 1 || out.Reply != c.texts[0] || out.ToolOutcomes[len(out.ToolOutcomes)-1].Origin != loop.FinalTextOrigin || len(out.Receipts) != 1) {
			t.Errorf("%s: batches=%d out=%+v", c.name, batches, out)
		}
	}
}

var errBad = errors.New("bad public text")

func TestValidateArgumentsReplacesTheSchemaCheck(t *testing.T) {
	var asked []string
	var rejected []error
	h := &fakeHost{}
	m := lt.New(
		// Breaks the built-in schema check (a number, an unknown property).
		lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": 1, "extra": true}}),
		lt.Tools(lt.Call{ID: "c2", Name: "read_task", Args: map[string]any{"task_ref": "blocked"}}),
		lt.Tools(lt.Call{ID: "c3", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
	)
	out, err := run(t, m, h, func(c *loop.Config) {
		c.Budget = loop.Budget{MaxModelResponses: 5, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 5}
		c.ValidateArguments = func(tool loop.Tool, args map[string]any) []string {
			asked = append(asked, tool.Name)
			if args["task_ref"] == "blocked" {
				return []string{"task_ref: blocked"}
			}
			return nil
		}
		c.OnBatchRejected = func(_ []loop.ToolCall, err error) { rejected = append(rejected, err) }
	})
	var ran []string
	for _, call := range h.calls {
		ran = append(ran, call.NativeToolCallID)
	}
	if err != nil || out.Reply != "done" || !slices.Equal(asked, []string{"read_task", "read_task", "reply"}) || !slices.Equal(ran, []string{"c1", "c3"}) {
		t.Fatalf("out=%+v err=%v asked=%v ran=%v", out, err, asked, ran)
	}
	if len(rejected) != 1 || !errors.Is(rejected[0], loop.ErrInvalidArguments) || rejected[0].Error() != "employee loop: invalid params for read_task: task_ref: blocked" {
		t.Fatalf("rejected=%v", rejected)
	}
}

// ValidateBatch runs after every built-in check of a batch and judges it with
// the call and the outcomes TurnTools saw for the same response; RepairText
// gets the call and the outcomes of the response it repairs.
func TestValidateBatchIsTheLastCheckWithTheStateTurnToolsSaw(t *testing.T) {
	var log []string
	turns := map[int][]loop.ToolOutcome{}
	ids := func(outcomes []loop.ToolOutcome) string {
		var s []string
		for _, o := range outcomes {
			s = append(s, o.NativeToolCallID)
		}
		return "[" + strings.Join(s, " ") + "]"
	}
	read := func(id, ref string) lt.Call {
		return lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": ref}}
	}
	errBlocked := errors.New("blocked")
	m := lt.New(
		lt.Tools(read("c1", "t1")),
		lt.Tools(lt.Call{ID: "c2", Name: "read_task", Args: map[string]any{}}),                                   // breaks the schema
		lt.Tools(lt.Call{ID: "c3", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "BAD"}}),     // public text
		lt.Tools(lt.Call{ID: "c4", Name: "stop_task", Args: map[string]any{"task_ref": "t1"}}, read("c5", "t1")), // exclusive
		lt.Tools(lt.Call{ID: "c6", Name: "nope", Args: map[string]any{}}),                                        // unknown
		lt.Tools(read("c7", "blocked")),
		lt.Truncated("cut"),
		lt.Tools(lt.Call{ID: "c8", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
	)
	out, err := run(t, m, &fakeHost{}, func(c *loop.Config) {
		c.Budget = loop.Budget{MaxModelResponses: 3, MaxConsecutiveErrors: 7, MaxPhysicalAttempts: 8}
		c.ValidateText = func(text string) error {
			if text == "BAD" {
				return errBad
			}
			return nil
		}
		c.TurnTools = func(call int, outcomes []loop.ToolOutcome) []string {
			turns[call] = outcomes
			log = append(log, fmt.Sprintf("turn %d %s", call, ids(outcomes)))
			return []string{"reply", "read_task", "stop_task"}
		}
		c.ValidateBatch = func(call int, outcomes []loop.ToolOutcome, calls []loop.ToolCall) error {
			if !reflect.DeepEqual(outcomes, turns[call]) {
				t.Errorf("call %d: ValidateBatch saw %+v, TurnTools %+v", call, outcomes, turns[call])
			}
			log = append(log, fmt.Sprintf("validate %d %s %s", call, ids(outcomes), calls[0].NativeToolCallID))
			if calls[0].Arguments["task_ref"] == "blocked" {
				return errBlocked
			}
			return nil
		}
		c.RepairText = func(call int, outcomes []loop.ToolOutcome, err error) string {
			if !reflect.DeepEqual(outcomes, turns[call]) {
				t.Errorf("call %d: RepairText saw %+v, TurnTools %+v", call, outcomes, turns[call])
			}
			var sentinel string
			for _, s := range []error{loop.ErrInvalidArguments, errBad, loop.ErrExclusiveTool, loop.ErrUnknownTool, errBlocked, loop.ErrIncompleteResponse} {
				if errors.Is(err, s) {
					sentinel = s.Error()
				}
			}
			log = append(log, fmt.Sprintf("repair %d %s %s", call, ids(outcomes), sentinel))
			return "fix it"
		}
	})
	want := []string{
		"turn 1 []", "validate 1 [] c1",
		"turn 2 [c1]", "repair 2 [c1] " + loop.ErrInvalidArguments.Error(),
		"turn 3 [c1]", "repair 3 [c1] " + errBad.Error(),
		"turn 4 [c1]", "repair 4 [c1] " + loop.ErrExclusiveTool.Error(),
		"turn 5 [c1]", "repair 5 [c1] " + loop.ErrUnknownTool.Error(),
		"turn 6 [c1]", "validate 6 [c1] c7", "repair 6 [c1] blocked",
		"turn 7 [c1]", "repair 7 [c1] " + loop.ErrIncompleteResponse.Error(),
		"turn 8 [c1]", "validate 8 [c1] c8",
	}
	if err != nil || out.Reply != "done" || !slices.Equal(log, want) {
		t.Fatalf("out=%+v err=%v\nlog  %q\nwant %q", out, err, log, want)
	}
}

func TestOnBatchAcceptedSeesOnlyAcceptedBatchesBeforeTheyRun(t *testing.T) {
	var log []string
	ids := func(calls []loop.ToolCall) string {
		var s []string
		for _, c := range calls {
			s = append(s, c.NativeToolCallID)
		}
		return strings.Join(s, " ")
	}
	fake := &fakeHost{}
	host := loop.HostFunc(func(ctx context.Context, id loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
		log = append(log, "execute "+call.NativeToolCallID)
		return fake.Execute(ctx, id, call)
	})
	read := func(id, ref string) lt.Call {
		return lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": ref}}
	}
	m := lt.New(
		lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{}}), // breaks the schema
		lt.Tools(read("c2", "blocked")),                                        // ValidateBatch rejects it
		lt.Tools(read("c3", "t1"), read("c4", "t2")),
		lt.Tools(lt.Call{ID: "c5", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
	)
	out, err := run(t, m, host, func(c *loop.Config) {
		c.Budget = loop.Budget{MaxModelResponses: 5, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 5}
		c.ValidateBatch = func(_ int, _ []loop.ToolOutcome, calls []loop.ToolCall) error {
			log = append(log, "validate "+ids(calls))
			if calls[0].Arguments["task_ref"] == "blocked" {
				return errors.New("blocked")
			}
			return nil
		}
		c.OnBatchRejected = func(calls []loop.ToolCall, _ error) { log = append(log, "rejected "+ids(calls)) }
		c.OnBatchAccepted = func(calls []loop.ToolCall) { log = append(log, "accepted "+ids(calls)) }
	})
	want := []string{
		"rejected c1",
		"validate c2", "rejected c2",
		"validate c3 c4", "accepted c3 c4", "execute c3", "execute c4",
		"validate c5", "accepted c5", "execute c5",
	}
	if err != nil || out.Reply != "done" || !slices.Equal(log, want) {
		t.Fatalf("out=%+v err=%v\nlog  %q\nwant %q", out, err, log, want)
	}
}

var errEmptyReply = errors.New("empty reply")

// rejectEmptyReply is a ValidateDecision that refuses a reply without text and
// records the decisions it is asked about.
func rejectEmptyReply(asked *[]loop.Decision) func(loop.Decision) error {
	return func(d loop.Decision) error {
		*asked = append(*asked, d)
		if d.Kind == loop.Reply && strings.TrimSpace(d.Reply) == "" {
			return errEmptyReply
		}
		return nil
	}
}

// A refused decision fails its call like a missing receipt, but the wake
// fails with the Host's error as it is, joined in call order.
func TestValidateDecisionFailsTheCallAndThenTheWake(t *testing.T) {
	decisionTools := []loop.Tool{
		{Name: "say", Schema: obj},                // decides without an effect
		{Name: "send", Effect: true, Schema: obj}, // decides with an effect
		{Name: "read", Schema: obj},
	}
	// The arguments script the Host: fail, receipt and the reply it decides.
	host := loop.HostFunc(func(_ context.Context, _ loop.Identity, c loop.ToolCall) (loop.ToolResult, error) {
		if msg, ok := c.Arguments["fail"].(string); ok {
			return loop.ToolResult{}, errors.New(msg)
		}
		result := loop.ToolResult{Content: "done " + c.NativeToolCallID}
		result.Receipt, _ = c.Arguments["receipt"].(string)
		if reply, ok := c.Arguments["reply"].(string); ok {
			result.Terminal = &loop.Decision{Kind: loop.Reply, Reply: reply}
		}
		return result, nil
	})
	tc := func(id, name string, args ...string) lt.Call {
		m := map[string]any{}
		for i := 0; i < len(args); i += 2 {
			m[args[i]] = args[i+1]
		}
		return lt.Call{ID: id, Name: name, Args: m}
	}
	missing := "employee loop: effect has no host receipt: send"
	cases := []struct {
		name     string
		batch    []lt.Call
		err      string   // Run's error text
		failed   bool     // it matches ErrToolFailed
		failures []string // ToolOutcome.Error in call order
		receipts int
		asked    int
	}{
		{"no effect", []lt.Call{tc("c1", "say", "reply", "")}, "empty reply", false, []string{"empty reply"}, 0, 1},
		{"effect", []lt.Call{tc("c1", "send", "reply", "", "receipt", "out-1")}, "empty reply", false, []string{"empty reply"}, 1, 1},
		{"missing receipt", []lt.Call{tc("c1", "send", "reply", "")},
			"employee loop: tool send failed: " + missing + "\nempty reply", true, []string{missing + "\nempty reply"}, 0, 1},
		{"after an effect failure", []lt.Call{tc("c1", "send", "fail", "queue down"), tc("c2", "say", "reply", "")},
			"employee loop: tool send failed: queue down\nempty reply", true, []string{"queue down", "empty reply"}, 0, 1},
		{"before an effect failure", []lt.Call{tc("c1", "say", "reply", ""), tc("c2", "send", "fail", "queue down")},
			"empty reply\nemployee loop: tool send failed: queue down", true, []string{"empty reply", "queue down"}, 0, 1},
		{"beside a decided call", []lt.Call{tc("c1", "say", "reply", "hi"), tc("c2", "say", "reply", "")}, "empty reply", false, []string{"", "empty reply"}, 0, 2},
		{"accepted", []lt.Call{tc("c1", "read"), tc("c2", "send", "reply", "hi", "receipt", "out-1")}, "", false, []string{"", ""}, 1, 1},
	}
	for _, c := range cases {
		var asked []loop.Decision
		var progress []string
		m := lt.New(lt.Tools(c.batch...), lt.Text("never asked"))
		out, err := loop.New(loop.Config{
			Tools:            decisionTools,
			ValidateDecision: rejectEmptyReply(&asked),
			Progress: func(call loop.ToolCall) bool {
				progress = append(progress, call.NativeToolCallID)
				return true
			},
		}, m, host).Run(context.Background(), wake())
		var errs []string
		for _, o := range out.ToolOutcomes {
			errs = append(errs, o.Error)
		}
		if got := fmt.Sprint(err); c.err == "" && err != nil || c.err != "" && got != c.err {
			t.Errorf("%s: err %q, want %q", c.name, got, c.err)
		}
		if c.err != "" && (!errors.Is(err, errEmptyReply) || errors.Is(err, loop.ErrToolFailed) != c.failed || out.Kind != "") {
			t.Errorf("%s: err=%v failed=%v out=%+v", c.name, err, errors.Is(err, loop.ErrToolFailed), out)
		}
		if !slices.Equal(errs, c.failures) || len(out.Receipts) != c.receipts || len(asked) != c.asked || m.Calls() != 1 {
			t.Errorf("%s: errors=%q receipts=%v asked=%v calls=%d", c.name, errs, out.Receipts, asked, m.Calls())
		}
		for _, msg := range out.Transcript[3:] {
			// The model sees the error in place of the result content.
			i := slices.IndexFunc(out.ToolOutcomes, func(o loop.ToolOutcome) bool { return o.NativeToolCallID == msg.ToolCallID })
			want := "done " + msg.ToolCallID
			if o := out.ToolOutcomes[i]; o.Error != "" {
				raw, _ := json.Marshal(map[string]string{"error": o.Error})
				want = string(raw)
			} else if !slices.Contains(progress, msg.ToolCallID) {
				t.Errorf("%s: %s succeeded but Progress was not asked", c.name, msg.ToolCallID)
			}
			if msg.Content != want {
				t.Errorf("%s: %s content %q, want %q", c.name, msg.ToolCallID, msg.Content, want)
			}
		}
		if c.err == "" && (out.Kind != loop.Reply || out.Reply != "hi") {
			t.Errorf("%s: out=%+v", c.name, out)
		}
		if c.name == "no effect" && (out.BudgetUsage.ConsecutiveErrors != 1 || len(progress) != 0) {
			t.Errorf("%s: a refused decision is no progress: usage=%+v progress=%v", c.name, out.BudgetUsage, progress)
		}
	}
}

// finalTextDecision accepts final text with a fixed decision.
type finalTextDecision struct {
	fakeHost
	decision loop.Decision
}

func (h *finalTextDecision) AcceptFinalText(context.Context, loop.Identity, string) (loop.ToolResult, error) {
	d := h.decision
	return loop.ToolResult{Receipt: "final-1", Terminal: &d}, nil
}

func TestValidateDecisionEndsAFinalTextWakeAtOnce(t *testing.T) {
	for _, decision := range []loop.Decision{{Kind: loop.Reply}, {Kind: loop.Reply, Reply: "hello"}} {
		var asked []loop.Decision
		m := lt.New(lt.Text("hello"), lt.Text("again"))
		out, err := run(t, m, &finalTextDecision{decision: decision}, func(c *loop.Config) {
			c.RequireTerminalTool, c.AcceptFinalText = true, true
			c.ValidateDecision = rejectEmptyReply(&asked)
		})
		if len(asked) != 1 || asked[0] != decision || m.Calls() != 1 || len(out.ToolOutcomes) != 1 || out.ToolOutcomes[0].Origin != loop.FinalTextOrigin || len(out.Receipts) != 1 {
			t.Errorf("%+v: asked=%v calls=%d out=%+v", decision, asked, m.Calls(), out)
			continue
		}
		if decision.Reply != "" {
			if err != nil || out.Decision != decision || out.ToolOutcomes[0].Error != "" {
				t.Errorf("accepted: out=%+v err=%v", out, err)
			}
			continue
		}
		// Refused: no repair, the committed receipt is kept, the answer is
		// not in the transcript.
		if err != errEmptyReply || out.Kind != "" || out.ToolOutcomes[0].Error != "empty reply" || len(out.Transcript) != 2 {
			t.Errorf("refused: out=%+v err=%v", out, err)
		}
	}
}
