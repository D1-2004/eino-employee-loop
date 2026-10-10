package local

import (
	"context"

	"github.com/cloudwego/eino/components/model"

	"github.com/D1-2004/eino-employee-loop/prototype/core"
	"github.com/D1-2004/eino-employee-loop/providers/llm"
)

// DashScopeBaseURL is kept for callers of the prototype; see providers/llm.
const DashScopeBaseURL = llm.DashScopeBaseURL

// Config is the provider configuration (see providers/llm).
type Config = llm.Config

// FromEnv reads the provider configuration from the environment (see
// providers/llm for variables and aliases).
func FromEnv() (Config, error) { return llm.FromEnv() }

// New returns a core.ModelProvider for cfg.
func New(cfg Config) core.ModelProvider { return provider{cfg} }

type provider struct{ cfg Config }

func (p provider) Decision(ctx context.Context) (model.ToolCallingChatModel, error) {
	return llm.NewChatModel(ctx, p.cfg)
}

func (p provider) Ack(ctx context.Context) (model.BaseChatModel, error) {
	return llm.NewAckModel(ctx, p.cfg)
}
