package loop_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop"
	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
)

// Every rejection the loop repairs matches its sentinel and keeps the text
// it had in v0.2, which Hosts have persisted.
func TestRejectionsMatchSentinelsAndKeepTheirText(t *testing.T) {
	read := func(args map[string]any) lt.Call { return lt.Call{ID: "c1", Name: "read_task", Args: args} }
	badArgs := lt.Tools(read(map[string]any{"task_ref": "t1"}))
	badArgs.Message.ToolCalls[0].Function.Arguments = "[]"
	terminal := func(c *loop.Config) { c.RequireTerminalTool = true }
	cases := []struct {
		name     string
		step     lt.Step
		host     loop.Host
		mutate   func(*loop.Config)
		sentinel error
		text     string
	}{
		{"no message", lt.Step{}, nil, nil, loop.ErrIncompleteResponse, "employee loop: model returned no message"},
		{"cut off", lt.Truncated("cut"), nil, nil, loop.ErrIncompleteResponse, `employee loop: incomplete model response: finish_reason="length"`},
		{"empty reply", lt.Text("  "), nil, nil, loop.ErrIncompleteResponse, "employee loop: a plain reply needs text"},
		{"no call id", lt.Tools(lt.Call{Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), nil, nil, loop.ErrInvalidToolCall, "employee loop: invalid or duplicate native tool call identity"},
		{"arguments not an object", badArgs, nil, nil, loop.ErrInvalidArguments, `employee loop: invalid arguments for "read_task"`},
		{"arguments break the schema", lt.Tools(read(map[string]any{})), nil, nil, loop.ErrInvalidArguments, `employee loop: invalid params for read_task: arguments: missing required "task_ref"`},
		{"unknown tool", lt.Tools(lt.Call{ID: "c1", Name: "nope", Args: map[string]any{}}), nil, nil, loop.ErrUnknownTool, `employee loop: unknown tool "nope"`},
		{"not offered", lt.Tools(read(map[string]any{"task_ref": "t1"})), nil, func(c *loop.Config) {
			c.TurnTools = func(int, []loop.ToolOutcome) []string { return []string{"reply"} }
		}, loop.ErrUnknownTool, `employee loop: unknown tool "read_task"`},
		{"exclusive", lt.Tools(lt.Call{ID: "c1", Name: "stop_task", Args: map[string]any{"task_ref": "t1"}}, lt.Call{ID: "c2", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), nil, nil,
			loop.ErrExclusiveTool, "employee loop: stop_task must be the only call of its batch"},
		{"terminal conflict", lt.Tools(lt.Call{ID: "c1", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "x"}}, lt.Call{ID: "c2", Name: "stay_quiet", Args: map[string]any{}}), nil, nil,
			loop.ErrTerminalConflict, "employee loop: conflicting terminal tool results: reply and quiet in one batch"},
		{"terminal required", lt.Text("private"), nil, terminal, loop.ErrTerminalRequired, "employee loop: a native terminal tool call is required"},
		{"final text too long", lt.Text("toolong"), &finalText{}, func(c *loop.Config) { c.RequireTerminalTool, c.AcceptFinalText, c.FinalTextLimit = true, true, 3 },
			loop.ErrFinalTextRejected, "employee loop: final text exceeds 3 bytes"},
	}
	for _, c := range cases {
		host := c.host
		if host == nil {
			host = &fakeHost{}
		}
		var failures []error
		_, err := run(t, lt.New(c.step), host, func(cfg *loop.Config) {
			cfg.Budget = loop.Budget{MaxModelResponses: 1, MaxConsecutiveErrors: 1, MaxPhysicalAttempts: 1}
			cfg.OnModelFailure = func(err error) { failures = append(failures, err) }
			if c.mutate != nil {
				c.mutate(cfg)
			}
		})
		if len(failures) != 1 || failures[0].Error() != c.text || !errors.Is(failures[0], c.sentinel) {
			t.Errorf("%s: repaired %v, want %q matching %v", c.name, failures, c.text, c.sentinel)
			continue
		}
		// The budget error that ends the wake carries the last rejection.
		if !errors.Is(err, loop.ErrModelBudget) || !errors.Is(err, c.sentinel) || !strings.HasSuffix(err.Error(), "\n"+c.text) {
			t.Errorf("%s: run error %v", c.name, err)
		}
	}
}

type undecidedFinalText struct{ fakeHost }

func (undecidedFinalText) AcceptFinalText(context.Context, loop.Identity, string) (loop.ToolResult, error) {
	return loop.ToolResult{Receipt: "sent-1"}, nil
}

func TestWakeEndingFailuresMatchSentinels(t *testing.T) {
	_, err := run(t, lt.New(lt.Text("hi")), &undecidedFinalText{}, func(c *loop.Config) { c.RequireTerminalTool, c.AcceptFinalText = true, true })
	if !errors.Is(err, loop.ErrNoTerminalDecision) || err.Error() != "employee loop: final text produced no terminal decision" {
		t.Errorf("final text without a decision: %v", err)
	}

	down := errors.New("queue down")
	host := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
		return loop.ToolResult{}, down
	})
	call := lt.Call{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "reply": "on it"}}
	_, err = run(t, lt.New(lt.Tools(call)), host, nil)
	if !errors.Is(err, loop.ErrToolFailed) || !errors.Is(err, down) || err.Error() != "employee loop: tool dispatch_task failed: queue down" {
		t.Errorf("failed effect: %v", err)
	}

	noReceipt := &fakeHost{noReceipt: true}
	_, err = run(t, lt.New(lt.Tools(call)), noReceipt, nil)
	if !errors.Is(err, loop.ErrToolFailed) || !errors.Is(err, loop.ErrMissingReceipt) || errors.Is(err, loop.ErrToolRefused) {
		t.Errorf("effect without a receipt: %v", err)
	}
}

// PublicArgs replaces the "reply" rule per tool; unset keeps it.
func TestPublicArgsChooseTheTextValidateTextChecks(t *testing.T) {
	for _, c := range []struct {
		name       string
		publicArgs []string
		args       map[string]any
		rejected   bool
	}{
		{"unset checks reply", nil, map[string]any{"reply": "BAD"}, true},
		{"unset ignores text", nil, map[string]any{"text": "BAD"}, false},
		{"declared text", []string{"text"}, map[string]any{"text": "BAD"}, true},
		{"declared text ignores reply", []string{"text"}, map[string]any{"reply": "BAD"}, false},
		{"several", []string{"title", "text"}, map[string]any{"title": "ok", "text": "BAD"}, true},
		{"empty checks none", []string{}, map[string]any{"reply": "BAD"}, false},
		{"non-string values are left to the schema", []string{"text"}, map[string]any{"text": 7}, false},
	} {
		say := loop.Tool{Name: "say", Terminal: loop.Reply, Schema: obj, PublicArgs: c.publicArgs}
		var calls int
		host := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
			calls++
			return loop.ToolResult{Content: "sent", Receipt: "out-1", Terminal: &loop.Decision{Kind: loop.Reply, Reply: "ok"}}, nil
		})
		var rejected []error
		cfg := loop.Config{Tools: []loop.Tool{say},
			ValidateText: func(text string) error {
				if text == "BAD" {
					return errors.New("bad public text")
				}
				return nil
			},
			OnBatchRejected: func(_ []loop.ToolCall, err error) { rejected = append(rejected, err) },
		}
		m := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "say", Args: c.args}), lt.Tools(lt.Call{ID: "c2", Name: "say", Args: map[string]any{}}))
		out, err := loop.New(cfg, m, host).Run(context.Background(), wake())
		if err != nil || out.Kind != loop.Reply || calls != 1 {
			t.Errorf("%s: out=%+v err=%v calls=%d", c.name, out, err, calls)
		}
		if got := len(rejected) == 1 && rejected[0].Error() == "bad public text"; got != c.rejected {
			t.Errorf("%s: rejected %v, want rejected=%v", c.name, rejected, c.rejected)
		}
	}
}
