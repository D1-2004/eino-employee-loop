package decision_test

import (
	"context"
	"errors"
	"testing"

	loop "github.com/D1-2004/eino-employee-loop/kernel/decision"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	"github.com/cloudwego/eino/schema"
)

func TestPerCallValidationNeverExecutesUnknownOrInvalidTools(t *testing.T) {
	m := looptest.New(looptest.Tools(looptest.Call{ID: "unknown", Name: "unknown", Args: map[string]any{}}, looptest.Call{ID: "invalid", Name: "read", Args: map[string]any{}}, looptest.Call{ID: "valid", Name: "read", Args: map[string]any{"query": "report"}}), looptest.Text("done"))
	var executed []string
	cfg := loop.Config{PerCallValidation: true, Tools: []loop.Tool{{Name: "read", Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}}}}}
	host := loop.HostFunc(func(_ context.Context, _ loop.Identity, c loop.ToolCall) (loop.ToolResult, error) {
		executed = append(executed, c.NativeToolCallID)
		return loop.ToolResult{Content: "read"}, nil
	})
	out, err := loop.New(cfg, m, host).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("read")}})
	if err != nil || len(executed) != 1 || executed[0] != "valid" || len(out.ToolOutcomes) != 3 {
		t.Fatal(out, err, executed)
	}
	if out.ToolOutcomes[0].Error == "" || out.ToolOutcomes[1].Error == "" {
		t.Fatal("invalid calls lack paired errors", out)
	}
	if len(m.Requests[1]) != 5 {
		t.Fatal("native pairs lost", m.Requests[1])
	}
}

func TestHostBudgetCannotExceedPhysicalCeiling(t *testing.T) {
	m := looptest.New(looptest.Fail(errors.New("offline")), looptest.Fail(errors.New("offline")), looptest.Text("must never run"))
	cfg := loop.Config{Budget: loop.Budget{MaxModelResponses: 1, MaxConsecutiveErrors: 1, MaxPhysicalAttempts: 2}, BudgetError: func(int, loop.BudgetUsage) error { return nil }}
	out, err := loop.New(cfg, m, nil).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("hello")}})
	if !errors.Is(err, loop.ErrPhysicalModelBudget) || out.ModelCalls != 2 || m.Calls() != 2 {
		t.Fatal(out, err, m.Calls())
	}
}

func TestOutcomeFilterCannotRemoveCommittedEffects(t *testing.T) {
	m := looptest.New(looptest.Tools(looptest.Call{ID: "write", Name: "write", Args: map[string]any{}}))
	cfg := loop.Config{Tools: []loop.Tool{{Name: "write", Effect: true}}, RecordToolOutcome: func(loop.ToolCall, loop.ToolResult, error) bool { return false }}
	host := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
		return loop.ToolResult{Receipt: "write/committed"}, errors.New("post-commit failure")
	})
	out, err := loop.New(cfg, m, host).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("write")}})
	if err == nil || len(out.ToolOutcomes) != 1 || len(out.Receipts) != 1 {
		t.Fatal("committed evidence was filtered", out, err)
	}
}
