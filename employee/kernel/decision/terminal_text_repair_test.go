package decision

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestTerminalTextRepairKeepsBothConversations(t *testing.T) {
	for _, reminder := range []string{"use the native reply tool", ""} {
		name := "reminder"
		if reminder == "" {
			name = "empty_custom_reminder"
		}
		t.Run(name, func(t *testing.T) {
			prior := []*schema.Message{schema.UserMessage("current question")}
			r := &run{cfg: Config{RequireTerminalTool: true, RepairText: func(int, []ToolOutcome, error) string { return reminder }}, msgs: prior}
			resp := &schema.Message{Role: schema.Assistant, Content: "  complete answer\n", ReasoningContent: "synthetic private reasoning", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}
			rc := &adk.RetryContext{InputMessages: detached(prior), OutputMessage: resp}
			decision := r.text(rc, resp)
			want := 2
			if reminder != "" {
				want++
			}
			if !decision.Retry || !decision.PersistModifiedInputMessages || len(decision.ModifiedInputMessages) != want || len(r.msgs) != want || sameConversation(decision.ModifiedInputMessages, r.msgs) != nil {
				t.Fatal("retained text and repair diverged between SDK and ADK")
			}
			got := decision.ModifiedInputMessages[1]
			if got == resp || !reflect.DeepEqual(got, resp) || r.msgs[1] != resp {
				t.Fatal("full response fields or ADK message detachment lost")
			}
			got.Content = "ADK-owned copy"
			if resp.Content != "  complete answer\n" {
				t.Fatal("ADK changed the original response")
			}
			if r.out.BudgetUsage.ModelResponses != 0 || r.out.BudgetUsage.ConsecutiveErrors != 1 || !errors.Is(r.lastErr, ErrTerminalRequired) || r.done {
				t.Fatal("private text counted as accepted progress or terminal delivery")
			}
		})
	}
}

func TestTerminalTextRepairDoesNotResetErrorBudgetOrDuplicateText(t *testing.T) {
	prior := []*schema.Message{schema.UserMessage("current question")}
	r := &run{cfg: Config{RequireTerminalTool: true, Budget: Budget{MaxModelResponses: 8, MaxConsecutiveErrors: 2, MaxPhysicalAttempts: 11}, RepairText: func(int, []ToolOutcome, error) string { return "repair" }}, msgs: prior}
	in := detached(prior)
	for i := 0; i < 2; i++ {
		if err := r.budgetErr(); err != nil {
			t.Fatal("premature budget exhaustion")
		}
		r.out.ModelCalls++
		resp := &schema.Message{Role: schema.Assistant, Content: "complete private answer"}
		rc := &adk.RetryContext{InputMessages: in, OutputMessage: resp}
		decision := r.text(rc, resp)
		in = decision.ModifiedInputMessages
		if len(in) != len(prior)+2*(i+1) || sameConversation(in, r.msgs) != nil {
			t.Fatal("a text response or reminder was lost or duplicated")
		}
	}
	if !errors.Is(r.budgetErr(), ErrConsecutiveModelErrors) || r.out.ModelCalls != 2 || r.out.BudgetUsage.ModelResponses != 0 {
		t.Fatal("retaining private text changed finite error-budget termination")
	}
}
