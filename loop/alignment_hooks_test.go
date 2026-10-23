package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/cloudwego/eino/schema"
)

func TestResponseTransformPreservesRawObservationAndValidatesDerivedBatch(t *testing.T) {
	raw := looptest.Tools(looptest.Call{ID: "work", Name: "dispatch_task", Args: map[string]any{"goal": "report", "extra": "display"}})
	model := looptest.New(raw)
	var observed, executed string
	cfg := loop.Config{Tools: []loop.Tool{{Name: "dispatch_task", Effect: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"goal": map[string]any{"type": "string"}}, "required": []any{"goal"}, "additionalProperties": false}}},
		OnModelCall: func(_ context.Context, c loop.ModelCall) { observed = c.Response.ToolCalls[0].Function.Arguments },
		TransformResponse: func(n int, o []loop.ToolOutcome, m *schema.Message) (*schema.Message, error) {
			cp := *m
			cp.ToolCalls = append([]schema.ToolCall(nil), m.ToolCalls...)
			var args map[string]any
			if err := json.Unmarshal([]byte(cp.ToolCalls[0].Function.Arguments), &args); err != nil {
				return nil, err
			}
			delete(args, "extra")
			raw, _ := json.Marshal(args)
			cp.ToolCalls[0].Function.Arguments = string(raw)
			return &cp, nil
		},
	}
	host := loop.HostFunc(func(_ context.Context, _ loop.Identity, c loop.ToolCall) (loop.ToolResult, error) {
		executed = c.Arguments["goal"].(string)
		if _, ok := c.Arguments["extra"]; ok {
			t.Error("unvalidated extra reached Host")
		}
		return loop.ToolResult{Receipt: "task/report", Terminal: &loop.Decision{Kind: loop.Dispatched}}, nil
	})
	out, err := loop.New(cfg, model, host).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("report")}})
	if err != nil || out.Kind != loop.Dispatched || observed != raw.Message.ToolCalls[0].Function.Arguments || executed != "report" {
		t.Fatal(out, err, observed, executed)
	}
	var accepted map[string]any
	if err = json.Unmarshal([]byte(out.Transcript[1].ToolCalls[0].Function.Arguments), &accepted); err != nil {
		t.Fatal(err)
	}
	if _, ok := accepted["extra"]; ok {
		t.Fatal("transcript did not retain derived arguments")
	}
}

func TestPerCallGuardPairsSkippedReadWithoutExecutingIt(t *testing.T) {
	m := looptest.New(looptest.Tools(looptest.Call{ID: "feedback", Name: "first_feedback", Args: map[string]any{}}, looptest.Call{ID: "read", Name: "read_context", Args: map[string]any{}}), looptest.Text("done"))
	var executed []string
	cfg := loop.Config{Tools: []loop.Tool{{Name: "first_feedback", Effect: true}, {Name: "read_context"}}, BeforeTool: func(_ int, o []loop.ToolOutcome, c loop.ToolCall) error {
		if c.Name == "read_context" && len(o) > 0 && o[len(o)-1].Error != "" {
			return errors.Join(loop.ErrToolRefused, errors.New("feedback rejected; read skipped"))
		}
		return nil
	}}
	host := loop.HostFunc(func(_ context.Context, _ loop.Identity, c loop.ToolCall) (loop.ToolResult, error) {
		executed = append(executed, c.Name)
		return loop.ToolResult{}, loop.ErrToolRefused
	})
	out, err := loop.New(cfg, m, host).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("read")}})
	if err != nil || len(executed) != 1 || len(out.ToolOutcomes) != 2 || out.ToolOutcomes[1].ToolName != "read_context" || out.ToolOutcomes[1].Error == "" {
		t.Fatal(out, err, executed)
	}
	if len(m.Requests[1]) != 4 || m.Requests[1][3].ToolCallID != "read" {
		t.Fatal("skipped call lost its native result pair", m.Requests[1])
	}
}

func TestStandaloneRepairKeepsPreviousStreakAndPhysicalBudget(t *testing.T) {
	m := looptest.New(looptest.Fail(errors.New("retry me")), looptest.Tools(looptest.Call{ID: "declare", Name: "declaration", Args: map[string]any{}}), looptest.Text("done"))
	cfg := loop.Config{Budget: loop.Budget{MaxModelResponses: 3, MaxConsecutiveErrors: 2, MaxPhysicalAttempts: 3}, Tools: []loop.Tool{{Name: "declaration"}}, Progress: func(loop.ToolCall) bool { return false }, CountBatch: func(_ int, cs []loop.ToolCall, o []loop.ToolOutcome, _ bool) bool {
		return len(cs) != 1 || cs[0].Name != "declaration" || o[len(o)-1].Error != ""
	}}
	out, err := loop.New(cfg, m, loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
		return loop.ToolResult{Content: "accepted"}, nil
	})).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("repair")}})
	if err != nil || out.ModelCalls != 3 || out.BudgetUsage.ModelResponses != 2 {
		t.Fatal(out, err)
	}
}

func TestCancelledTerminalToolRetainsItsCommittedReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := looptest.New(looptest.Tools(looptest.Call{ID: "dispatch", Name: "dispatch_task", Args: map[string]any{}}))
	cfg := loop.Config{CheckContextAfterTool: true, Tools: []loop.Tool{{Name: "dispatch_task", Effect: true}}}
	host := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
		cancel()
		return loop.ToolResult{Receipt: "task/committed", Terminal: &loop.Decision{Kind: loop.Dispatched}}, nil
	})
	out, err := loop.New(cfg, m, host).Run(ctx, loop.Wake{Messages: []*schema.Message{schema.UserMessage("dispatch")}})
	if !errors.Is(err, context.Canceled) || out.Kind != "" || len(out.Receipts) != 1 || out.Receipts[0].ID != "task/committed" {
		t.Fatal(out, err)
	}
}

func TestHostToolOfferingStillRequiresPolicyAndRegisteredTools(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		enabled, guard, reject bool
		tool                   string
		wantCalls              int
	}{
		{"default refuses hidden", false, true, false, "read_task", 0},
		{"Host approves registered hidden", true, true, false, "read_task", 1},
		{"Host refuses registered hidden", true, true, true, "read_task", 0},
		{"unknown stays forbidden", true, true, false, "unknown", 0},
		{"missing Host policy", true, false, false, "read_task", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := looptest.New(looptest.Tools(looptest.Call{ID: "read", Name: tc.tool, Args: map[string]any{"task_ref": "t1"}}), looptest.Text("done"))
			h := &fakeHost{}
			_, err := run(t, m, h, func(c *loop.Config) {
				c.HostToolOffering = tc.enabled
				c.TurnTools = func(int, []loop.ToolOutcome) []string { return []string{"reply"} }
				if tc.guard {
					c.ValidateBatch = func(int, []loop.ToolOutcome, []loop.ToolCall) error {
						if tc.reject {
							return loop.ErrToolRefused
						}
						return nil
					}
				}
			})
			if len(h.calls) != tc.wantCalls {
				t.Fatal(err, h.calls)
			}
			if tc.enabled && !tc.guard && (err == nil || m.Calls() != 0) {
				t.Fatal("missing policy reached model", err, m.Calls())
			}
		})
	}
}
