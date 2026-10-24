package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ExtensionFactory is trusted Host code selected by a registered exact version.
// It never receives the mutable base config or storage/admission binding.
type ExtensionFactory func(context.Context, Request) (Extension, error)

// Extension composes standard Eino handlers, callbacks and AgentAsTool. ReadOnlyAgents
// must be assembled by the Host from read-only capabilities; this declaration
// does not sandbox arbitrary Go code or grant child agents business authority.
type Extension struct {
	Handlers       []adk.ChatModelAgentMiddleware
	ReadOnlyAgents []adk.Agent
	Callbacks      []callbacks.Handler
}

func (r *Runtime) extend(ctx context.Context, req Request, base *adk.ChatModelAgentConfig) (*adk.ChatModelAgentConfig, []callbacks.Handler, error) {
	cfg := *base
	cfg.Handlers = append([]adk.ChatModelAgentMiddleware(nil), base.Handlers...)
	cfg.ToolsConfig.Tools = append([]tool.BaseTool(nil), base.ToolsConfig.Tools...)
	terminal := make(map[string]bool, len(base.ToolsConfig.ReturnDirectly))
	for name, value := range base.ToolsConfig.ReturnDirectly {
		terminal[name] = value
	}
	cfg.ToolsConfig.ReturnDirectly = terminal
	handlers := append([]callbacks.Handler(nil), r.config.Callbacks...)
	for _, factory := range r.config.Extensions {
		extension, err := factory(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		for _, h := range extension.Handlers {
			if h == nil {
				return nil, nil, errors.New("agent: nil extension middleware")
			}
		}
		cfg.Handlers = append(cfg.Handlers, extension.Handlers...)
		handlers = append(handlers, extension.Callbacks...)
		for _, child := range extension.ReadOnlyAgents {
			if child == nil {
				return nil, nil, errors.New("agent: nil read-only AgentAsTool")
			}
			raw := adk.NewAgentTool(ctx, child)
			info, err := raw.Info(ctx)
			if err != nil {
				return nil, nil, err
			}
			invokable, ok := raw.(tool.InvokableTool)
			if !ok {
				return nil, nil, errors.New("agent: native AgentAsTool must be invokable")
			}
			cfg.ToolsConfig.Tools = append(cfg.ToolsConfig.Tools, &authorizedAgentTool{info: info, inner: invokable, before: func(c context.Context) error { return r.config.Authorize(c, req) }})
			cfg.ToolsConfig.EmitInternalEvents = true
		}
	}
	if len(r.config.Extensions) > 0 {
		for i, t := range cfg.ToolsConfig.Tools {
			if t != nil && !reflect.ValueOf(t).Comparable() {
				var err error
				cfg.ToolsConfig.Tools[i], err = stableTool(t)
				if err != nil {
					return nil, nil, err
				}
			}
		}
	}
	infos := make([]*schema.ToolInfo, 0, len(cfg.ToolsConfig.Tools))
	seen := map[string]bool{}
	for _, t := range cfg.ToolsConfig.Tools {
		if t == nil {
			return nil, nil, errors.New("agent: nil offered tool")
		}

		info, err := t.Info(ctx)
		if err != nil {
			return nil, nil, err
		}
		if info == nil || info.Name == "" || seen[info.Name] {
			return nil, nil, errors.New("agent: duplicate or empty tool name")
		}
		seen[info.Name] = true
		infos = append(infos, info)
	}
	for _, aliases := range cfg.ToolsConfig.ToolAliases {
		for _, alias := range aliases.NameAliases {
			if seen[alias] {
				return nil, nil, fmt.Errorf("agent: tool alias collides with offered tool %s", alias)
			}
			seen[alias] = true
		}
	}
	if r.config.ToolsReady != nil {
		if err := r.config.ToolsReady(ctx, req, infos); err != nil {
			return nil, nil, err
		}
	}
	if len(r.config.Extensions) > 0 {
		// Append a final standard middleware fence: extension hooks can rewrite
		// messages/context, while the Host's offered effects and terminal set stay fixed.
		expectedTerminal := make(map[string]bool, len(terminal))
		for k, v := range terminal {
			expectedTerminal[k] = v
		}
		cfg.Handlers = append(cfg.Handlers, &extensionFence{tools: append([]tool.BaseTool(nil), cfg.ToolsConfig.Tools...), terminal: expectedTerminal, before: func(c context.Context) error { return r.config.Authorize(c, req) }})
	}
	return &cfg, handlers, nil
}

type authorizedAgentTool struct {
	info   *schema.ToolInfo
	inner  tool.InvokableTool
	before func(context.Context) error
}

func (t *authorizedAgentTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }
func (t *authorizedAgentTool) InvokableRun(ctx context.Context, input string, opts ...tool.Option) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := t.before(ctx); err != nil {
		return "", errors.Join(ErrDenied, err)
	}
	return t.inner.InvokableRun(ctx, input, opts...)
}

type extensionFence struct {
	adk.BaseChatModelAgentMiddleware
	tools    []tool.BaseTool
	terminal map[string]bool
	before   func(context.Context) error
}

func (f *extensionFence) BeforeAgent(ctx context.Context, run *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if err := f.before(ctx); err != nil {
		return ctx, nil, errors.Join(ErrDenied, err)
	}
	if run == nil || !sameTools(run.Tools, f.tools) || !equalTerminal(run.ReturnDirectly, f.terminal) {
		return ctx, nil, errors.New("agent: middleware cannot replace offered tools or terminal contract")
	}
	return ctx, run, nil
}
func (f *extensionFence) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if err := f.before(ctx); err != nil {
		return ctx, nil, errors.Join(ErrDenied, err)
	}
	return ctx, state, nil
}
func equalTerminal(a, b map[string]bool) bool {
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return string(rawA) == string(rawB)
}

func sameTools(a, b []tool.BaseTool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		va, vb := reflect.ValueOf(a[i]), reflect.ValueOf(b[i])
		if !va.IsValid() || !vb.IsValid() || va.Type() != vb.Type() {
			return false
		}
		if va.Comparable() {
			if va.Interface() != vb.Interface() {
				return false
			}
			continue
		}
		// Bind value tools behind stable pointers before the middleware boundary.
		return false
	}
	return true
}

// Stable pointer shells preserve all four standard Eino invocation interfaces
// while making middleware tool identity independent of value-tool comparability.
type stableInvoke struct{ tool.InvokableTool }
type stableStream struct{ tool.StreamableTool }
type stableInvokeStream struct {
	tool.InvokableTool
	tool.StreamableTool
}

func (t *stableInvokeStream) Info(c context.Context) (*schema.ToolInfo, error) {
	return t.InvokableTool.Info(c)
}

type stableEnhancedInvoke struct{ tool.EnhancedInvokableTool }
type stableEnhancedStream struct{ tool.EnhancedStreamableTool }
type stableEnhancedBoth struct {
	tool.EnhancedInvokableTool
	tool.EnhancedStreamableTool
}

func (t *stableEnhancedBoth) Info(c context.Context) (*schema.ToolInfo, error) {
	return t.EnhancedInvokableTool.Info(c)
}
func stableTool(t tool.BaseTool) (tool.BaseTool, error) {
	if invoke, ok := t.(tool.EnhancedInvokableTool); ok {
		if stream, ok := t.(tool.EnhancedStreamableTool); ok {
			return &stableEnhancedBoth{invoke, stream}, nil
		}
		return &stableEnhancedInvoke{invoke}, nil
	}
	if stream, ok := t.(tool.EnhancedStreamableTool); ok {
		return &stableEnhancedStream{stream}, nil
	}
	if invoke, ok := t.(tool.InvokableTool); ok {
		if stream, ok := t.(tool.StreamableTool); ok {
			return &stableInvokeStream{invoke, stream}, nil
		}
		return &stableInvoke{invoke}, nil
	}
	if stream, ok := t.(tool.StreamableTool); ok {
		return &stableStream{stream}, nil
	}
	return nil, errors.New("agent: offered extension tool has no native invocation interface")
}
