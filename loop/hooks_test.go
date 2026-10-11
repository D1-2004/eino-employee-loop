package loop_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop"
	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
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
		c.ValidateBatch = func(calls []loop.ToolCall) error {
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
