package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/providers/llm"
)

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestFromLookupDefaultsAndAliases(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want llm.Config
	}{
		{"deepseek default", map[string]string{"LLM_API_KEY": "k"},
			llm.Config{Provider: "deepseek", APIKey: "k", Model: llm.DefaultDeepSeekModel, AckModel: llm.DefaultDeepSeekModel}},
		{"deepseek alias", map[string]string{"DEEPSEEK_API_KEY": "d", "DEEPSEEK_BASE_URL": "http://ds"},
			llm.Config{Provider: "deepseek", APIKey: "d", BaseURL: "http://ds", Model: llm.DefaultDeepSeekModel, AckModel: llm.DefaultDeepSeekModel}},
		{"dashscope alias picks provider", map[string]string{"DASHSCOPE_API_KEY": "q"},
			llm.Config{Provider: "dashscope", APIKey: "q", BaseURL: llm.DashScopeBaseURL, Model: llm.DefaultDashScopeModel, AckModel: llm.DefaultDashScopeModel}},
		{"explicit openai", map[string]string{"LLM_PROVIDER": "OpenAI", "OPENAI_API_KEY": "o", "LLM_MODEL": "m", "LLM_ACK_MODEL": "a"},
			llm.Config{Provider: "openai", APIKey: "o", Model: "m", AckModel: "a"}},
		{"explicit key wins over alias", map[string]string{"LLM_PROVIDER": "dashscope", "LLM_API_KEY": "x", "DASHSCOPE_API_KEY": "q", "LLM_BASE_URL": "http://b"},
			llm.Config{Provider: "dashscope", APIKey: "x", BaseURL: "http://b", Model: llm.DefaultDashScopeModel, AckModel: llm.DefaultDashScopeModel}},
	}
	for _, c := range cases {
		got, err := llm.FromLookup(lookup(c.env))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		c.want.Timeout = llm.DefaultTimeout
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestFromLookupErrors(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want error
	}{
		"no key":           {map[string]string{}, llm.ErrMissingAPIKey},
		"openai needs one": {map[string]string{"LLM_PROVIDER": "openai", "LLM_API_KEY": "k"}, llm.ErrMissingModel},
		"unknown provider": {map[string]string{"LLM_PROVIDER": "nope", "LLM_API_KEY": "k"}, llm.ErrUnknownProvider},
	}
	for name, c := range cases {
		if _, err := llm.FromLookup(lookup(c.env)); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", name, err, c.want)
		}
	}
}

func TestNewChatModelBuildsOffline(t *testing.T) {
	for _, p := range []string{"deepseek", "dashscope", "openai"} {
		cfg := llm.Config{Provider: p, APIKey: "k", Model: "m", BaseURL: "http://127.0.0.1:1"}
		if m, err := llm.NewChatModel(context.Background(), cfg); err != nil || m == nil {
			t.Fatalf("%s decision model: %v", p, err)
		}
		if m, err := llm.NewAckModel(context.Background(), cfg); err != nil || m == nil {
			t.Fatalf("%s ack model: %v", p, err)
		}
	}
	if _, err := llm.NewChatModel(context.Background(), llm.Config{}); !errors.Is(err, llm.ErrMissingAPIKey) {
		t.Fatalf("empty config: %v", err)
	}
}
