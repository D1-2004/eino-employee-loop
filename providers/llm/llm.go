// Package llm builds Eino chat models for DeepSeek-native and
// OpenAI-compatible endpoints (for example Alibaba Cloud Bailian /
// DashScope compatible mode) from configuration or the environment.
//
// It is a provider package: assemblies (commands) call it and hand the
// resulting model.ToolCallingChatModel to the loop. Loop packages never
// import it.
//
// Environment (FromEnv):
//
//	LLM_PROVIDER   deepseek | dashscope | openai (default deepseek)
//	LLM_MODEL      model name (defaults per provider; required for openai)
//	LLM_ACK_MODEL  optional low-latency model; defaults to LLM_MODEL
//	LLM_BASE_URL   endpoint override
//	LLM_API_KEY    API key
//
// Aliases fill unset values: DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL,
// DASHSCOPE_API_KEY / DASHSCOPE_BASE_URL and OPENAI_API_KEY /
// OPENAI_BASE_URL apply to their provider; with LLM_PROVIDER unset, the
// first provider whose alias key is present is chosen.
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// Providers.
const (
	ProviderDeepSeek  = "deepseek"
	ProviderDashScope = "dashscope"
	ProviderOpenAI    = "openai"
)

// DashScopeBaseURL is the DashScope OpenAI-compatible endpoint.
const DashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// Default models per provider.
const (
	DefaultDeepSeekModel  = "deepseek-flash"
	DefaultDashScopeModel = "deepseek-v4.1-flash"
)

// DefaultTimeout bounds one provider request unless Config.Timeout is set.
const DefaultTimeout = 120 * time.Second

// AckMaxTokens bounds the output of the acknowledgement model.
const AckMaxTokens = 80

// Configuration errors. They carry machine codes, not prose.
var (
	ErrMissingAPIKey   = errors.New("llm: missing_api_key")
	ErrMissingModel    = errors.New("llm: missing_model")
	ErrUnknownProvider = errors.New("llm: unknown_provider")
)

// Config selects a provider endpoint and models.
type Config struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	// AckModel is an optional low-latency model; defaults to Model.
	AckModel string
	// Timeout bounds one request; zero uses DefaultTimeout.
	Timeout time.Duration
}

// aliases lists, per provider, the env names that fill an unset key and
// base URL.
var aliases = []struct{ provider, key, baseURL string }{
	{ProviderDeepSeek, "DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL"},
	{ProviderDashScope, "DASHSCOPE_API_KEY", "DASHSCOPE_BASE_URL"},
	{ProviderOpenAI, "OPENAI_API_KEY", "OPENAI_BASE_URL"},
}

// FromEnv reads the configuration from the process environment.
func FromEnv() (Config, error) { return FromLookup(os.LookupEnv) }

// FromLookup reads the configuration through lookup (os.LookupEnv in
// production, a map in tests) and validates it.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	get := func(k string) string {
		v, _ := lookup(k)
		return strings.TrimSpace(v)
	}
	c := Config{
		Provider: strings.ToLower(get("LLM_PROVIDER")), BaseURL: get("LLM_BASE_URL"),
		APIKey: get("LLM_API_KEY"), Model: get("LLM_MODEL"), AckModel: get("LLM_ACK_MODEL"),
	}
	if c.Provider == "" {
		c.Provider = ProviderDeepSeek
		if c.APIKey == "" {
			for _, a := range aliases {
				if get(a.key) != "" {
					c.Provider = a.provider
					break
				}
			}
		}
	}
	for _, a := range aliases {
		if a.provider != c.Provider {
			continue
		}
		if c.APIKey == "" {
			c.APIKey = get(a.key)
		}
		if c.BaseURL == "" {
			c.BaseURL = get(a.baseURL)
		}
	}
	return c.Normalize()
}

// Normalize applies provider defaults and validates the configuration.
func (c Config) Normalize() (Config, error) {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = ProviderDeepSeek
	}
	switch c.Provider {
	case ProviderDashScope:
		if c.BaseURL == "" {
			c.BaseURL = DashScopeBaseURL
		}
		if c.Model == "" {
			c.Model = DefaultDashScopeModel
		}
	case ProviderOpenAI:
		if c.Model == "" {
			return c, fmt.Errorf("%w: %s", ErrMissingModel, c.Provider)
		}
	case ProviderDeepSeek:
		if c.Model == "" {
			c.Model = DefaultDeepSeekModel
		}
	default:
		return c, fmt.Errorf("%w: %s", ErrUnknownProvider, c.Provider)
	}
	if c.APIKey == "" {
		return c, fmt.Errorf("%w: %s", ErrMissingAPIKey, c.Provider)
	}
	if c.AckModel == "" {
		c.AckModel = c.Model
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return c, nil
}

// NewChatModel returns the tool-calling decision model of cfg.
func NewChatModel(ctx context.Context, cfg Config) (model.ToolCallingChatModel, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return build(ctx, cfg, cfg.Model, true)
}

// NewAckModel returns a short-output model without thinking, for quick
// acknowledgements.
func NewAckModel(ctx context.Context, cfg Config) (model.ToolCallingChatModel, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return build(ctx, cfg, cfg.AckModel, false)
}

func build(ctx context.Context, c Config, name string, think bool) (model.ToolCallingChatModel, error) {
	if c.Provider == ProviderDeepSeek {
		mc := &deepseek.ChatModelConfig{APIKey: c.APIKey, Model: name, BaseURL: c.BaseURL, Timeout: c.Timeout}
		if !think {
			mc.ThinkingConfig = &deepseek.ThinkingConfig{Type: "disabled"}
			mc.MaxTokens = AckMaxTokens
		}
		return deepseek.NewChatModel(ctx, mc)
	}
	oc := &openai.ChatModelConfig{APIKey: c.APIKey, BaseURL: c.BaseURL, Model: name, Timeout: c.Timeout}
	if !think {
		n := AckMaxTokens
		oc.MaxCompletionTokens = &n
		if c.Provider == ProviderDashScope {
			// DashScope compatible-mode switch for hybrid thinking models.
			oc.ExtraFields = map[string]any{"enable_thinking": false}
		}
	}
	return openai.NewChatModel(ctx, oc)
}
