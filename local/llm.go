// Package llm builds core.ModelProvider implementations for DeepSeek-native and
// OpenAI-compatible endpoints (e.g. Alibaba Cloud Bailian / DashScope).
package local

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/dingtalk-fde/eino-employee-loop/core"
)

const DashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

type Config struct {
	Provider string // deepseek | dashscope | openai
	BaseURL  string
	APIKey   string
	Model    string
	AckModel string // optional; defaults to Model
}

// FromEnv reads LLM_PROVIDER, LLM_BASE_URL, LLM_API_KEY, LLM_MODEL, LLM_ACK_MODEL.
// Falls back to DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL when LLM_PROVIDER is unset.
func FromEnv() (Config, error) {
	c := Config{Provider: strings.ToLower(os.Getenv("LLM_PROVIDER")), BaseURL: os.Getenv("LLM_BASE_URL"),
		APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL"), AckModel: os.Getenv("LLM_ACK_MODEL")}
	if c.Provider == "" {
		c.Provider = "deepseek"
		if c.APIKey == "" {
			c.APIKey = os.Getenv("DEEPSEEK_API_KEY")
		}
		if c.BaseURL == "" {
			c.BaseURL = os.Getenv("DEEPSEEK_BASE_URL")
		}
	}
	if c.APIKey == "" {
		return c, fmt.Errorf("LLM_API_KEY (or DEEPSEEK_API_KEY) is required")
	}
	switch c.Provider {
	case "dashscope":
		if c.BaseURL == "" {
			c.BaseURL = DashScopeBaseURL
		}
		if c.Model == "" {
			c.Model = "deepseek-v4.1-flash"
		}
	case "openai":
		if c.Model == "" {
			return c, fmt.Errorf("LLM_MODEL required for provider openai")
		}
	case "deepseek":
		if c.Model == "" {
			c.Model = "deepseek-flash"
		}
	default:
		return c, fmt.Errorf("unknown LLM_PROVIDER %q", c.Provider)
	}
	if c.AckModel == "" {
		c.AckModel = c.Model
	}
	return c, nil
}

// New returns a core.ModelProvider for cfg.
func New(cfg Config) core.ModelProvider { return provider{cfg} }

type provider struct{ cfg Config }

func (p provider) Decision(ctx context.Context) (model.ToolCallingChatModel, error) {
	return p.build(ctx, p.cfg.Model, true)
}

func (p provider) Ack(ctx context.Context) (model.BaseChatModel, error) {
	return p.build(ctx, p.cfg.AckModel, false)
}

func (p provider) build(ctx context.Context, name string, think bool) (model.ToolCallingChatModel, error) {
	c := p.cfg
	if c.Provider == "deepseek" {
		mc := &deepseek.ChatModelConfig{APIKey: c.APIKey, Model: name, BaseURL: c.BaseURL, Timeout: 120 * time.Second}
		if !think {
			mc.ThinkingConfig = &deepseek.ThinkingConfig{Type: "disabled"}
			mc.MaxTokens = 80
		}
		return deepseek.NewChatModel(ctx, mc)
	}
	oc := &openai.ChatModelConfig{APIKey: c.APIKey, BaseURL: c.BaseURL, Model: name, Timeout: 120 * time.Second}
	if !think {
		n := 80
		oc.MaxCompletionTokens = &n
		if c.Provider == "dashscope" {
			// DashScope compatible-mode switch for hybrid thinking models.
			oc.ExtraFields = map[string]any{"enable_thinking": false}
		}
	}
	return openai.NewChatModel(ctx, oc)
}
