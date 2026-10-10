package core

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Default tool-call budgets (Host may override per tool via ToolSpec.Timeout).
const (
	DefaultToolTimeout     = 30 * time.Second
	DefaultEffectTimeout   = 120 * time.Second
	DefaultDispatchTimeout = 10 * time.Minute
)

// ToolSpec is Host-supplied capability metadata. Core never hardcodes vendors.
type ToolSpec struct {
	Info     *schema.ToolInfo
	Effect   bool          // irreversible / durable side effect → needs receipt
	Terminal bool          // ends the wake (reply / ask / stay_quiet / dispatch)
	Timeout  time.Duration // 0 = DefaultToolTimeout (or DefaultEffectTimeout if Effect)
}

// ToolProvider injects the tool catalog and executors. All tools enter Core through this port.
type ToolProvider interface {
	// Specs returns the tools visible to the model for this wake.
	Specs(ctx context.Context) ([]ToolSpec, error)
	// Invoker returns an InvokableTool for name, or false if unknown.
	Invoker(name string) (tool.InvokableTool, bool)
}

// TimeoutFor picks the budget for a tool call.
func TimeoutFor(spec ToolSpec) time.Duration {
	if spec.Timeout > 0 {
		return spec.Timeout
	}
	if spec.Effect {
		return DefaultEffectTimeout
	}
	return DefaultToolTimeout
}

// WithTimeout runs fn under a child context capped by d (no-op if d<=0).
func WithTimeout(ctx context.Context, d time.Duration, fn func(context.Context) error) error {
	if d <= 0 {
		return fn(ctx)
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return fn(cctx)
}
