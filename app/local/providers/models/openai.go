package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

var OpenAIRef = Ref{Name: "openai-compatible", Version: "1"}

// OpenAIEnvironment reads endpoint credentials at actual use. Explicit model and
// effort selections always come from the frozen Profile, never LLM_MODEL.
func OpenAIEnvironment() *Registry {
	legacy := func(ctx context.Context, r contract.ModelRequest) (model.ToolCallingChatModel, error) {
		if os.Getenv("LLM_BASE_URL") == "" || os.Getenv("LLM_API_KEY") == "" {
			return nil, errors.New("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL, or use -script")
		}
		expected := os.Getenv("LLM_MODEL")
		if expected == "" || expected != r.ModelName {
			return nil, errors.New("models: LLM_MODEL must equal frozen profile ModelName for legacy selection")
		}
		return openAI(ctx, r)
	}
	registry := New(legacy)
	if err := registry.Register(OpenAIRef, []string{"", "low", "medium", "high"}, openAI); err != nil {
		panic(err)
	}
	return registry
}
func openAI(ctx context.Context, r contract.ModelRequest) (model.ToolCallingChatModel, error) {
	base, key := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY")
	if base == "" || key == "" {
		return nil, errors.New("models: set LLM_BASE_URL and LLM_API_KEY")
	}
	timeout := 2 * time.Minute
	if value := strings.TrimSpace(os.Getenv("LLM_REQUEST_TIMEOUT")); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return nil, fmt.Errorf("models: LLM_REQUEST_TIMEOUT must be a positive duration, got %q", value)
		}
	}
	return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: base, APIKey: key, Model: r.ModelName, ReasoningEffort: openaimodel.ReasoningEffortLevel(r.Binding.Effort), Timeout: timeout})
}
