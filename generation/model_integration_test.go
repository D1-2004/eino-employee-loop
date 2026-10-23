//go:build workflowintegration

package generation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/generation"
	"github.com/D1-2004/eino-employee-loop/loop"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// A consumer exports its real assembled contract with synthetic local evidence.
// Its business validator must separately consume the generated native decision.
func TestOwnModelConsumerGenerationFixture(t *testing.T) {
	if os.Getenv("EEL_RUN_WORKFLOW_MODEL_CHECK") != "1" {
		t.Skip("explicit own-model opt-in required")
	}
	input, output := os.Getenv("EEL_GENERATION_FIXTURE"), os.Getenv("EEL_GENERATION_RESULT")
	if input == "" || output == "" {
		t.Skip("consumer fixture and result paths required")
	}
	var fixture struct {
		Messages     []*schema.Message
		Tools        []loop.Tool
		ExpectedTool string
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL"), Timeout: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := loop.ToolInfos(fixture.Tools)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := m.WithTools(infos)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]loop.Tool{}
	for _, tool := range fixture.Tools {
		tools[tool.Name] = tool
	}
	physical, journaled := 0, 0
	lastError := ""
	p := generation.Providers[[]*schema.Message, *schema.Message, string]{
		Context: generation.ContextFunc[[]*schema.Message](func(context.Context, int) ([]*schema.Message, error) {
			messages := append([]*schema.Message(nil), fixture.Messages...)
			if lastError != "" {
				messages = append(messages, schema.UserMessage("The Host rejected the previous response: "+lastError+". Follow the frozen native decision contract."))
			}
			return messages, nil
		}),
		Model: generation.ModelFunc[[]*schema.Message, *schema.Message](func(c context.Context, messages []*schema.Message) (*schema.Message, error) {
			physical++
			return bound.Generate(c, messages, model.WithToolChoice(schema.ToolChoiceForced))
		}),
		Decision: generation.DecisionFunc[[]*schema.Message, *schema.Message, string](func(_ context.Context, _ []*schema.Message, m *schema.Message) (string, error) {
			if m == nil || m.ResponseMeta == nil || m.ResponseMeta.FinishReason != "tool_calls" || len(m.ToolCalls) != 1 {
				return "", fmt.Errorf("one complete native decision is required")
			}
			call := m.ToolCalls[0]
			tool, known := tools[call.Function.Name]
			if !known || strings.TrimSpace(call.ID) == "" {
				return "", fmt.Errorf("unknown or uncorrelated native decision")
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
				return "", fmt.Errorf("decision arguments must be an object")
			}
			if problems := loop.ValidateArguments(tool.Schema, args); len(problems) > 0 {
				return "", fmt.Errorf("invalid native arguments: %s", strings.Join(problems, "; "))
			}
			return call.Function.Name, nil
		}),
		Journal: generation.JournalFunc[[]*schema.Message, *schema.Message, string](func(_ context.Context, a generation.Attempt[[]*schema.Message, *schema.Message, string]) error {
			if a.Error() != nil {
				lastError = a.Error().Error()
			}
			raw, err := json.Marshal(a.Response)
			if err != nil {
				return err
			}
			if err = os.WriteFile(fmt.Sprintf("%s.attempt-%d", output, a.Number), raw, 0600); err != nil {
				return err
			}
			journaled++
			if a.Error() == nil {
				return os.WriteFile(output, raw, 0600)
			}
			return nil
		}),
	}
	out, err := generation.Run(ctx, generation.Config[[]*schema.Message, *schema.Message, string]{MaxAttempts: 3, Retry: func(a generation.Attempt[[]*schema.Message, *schema.Message, string]) bool {
		return a.PrepareError == nil
	}}, p)
	if err != nil || !out.Accepted {
		t.Fatal(out.Accepted, err)
	}
	if out.Last.Decision != fixture.ExpectedTool {
		t.Fatalf("business outcome differs: got %s want %s; original result retained at %s", out.Last.Decision, fixture.ExpectedTool, output)
	}
	t.Logf("model=%s native_decision=%s physical_calls=%d journaled=%d; consumer must verify its business parser and apply/readback separately", os.Getenv("LLM_MODEL"), out.Last.Decision, physical, journaled)
}
