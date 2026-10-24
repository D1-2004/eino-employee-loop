package eino_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	native "github.com/D1-2004/eino-employee-loop/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/modules/entry"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRegisteredExtensionsComposeNativeHooksAndAgentToolWithTerminal(t *testing.T) {
	db := store(t, t.TempDir())
	defer db.Close()
	var middlewareCalls, callbackCalls atomic.Int32
	var pluginSeen, traceSeen atomic.Bool
	parent := &markedModel{inner: looptest.New(looptest.Tools(looptest.Call{ID: "research-one", Name: "research", Args: map[string]any{"request": "inspect private input"}}), looptest.Tools(looptest.Call{ID: "finish-one", Name: "finish", Args: map[string]any{}})), plugin: &pluginSeen, trace: &traceSeen}
	cfg := config(db, func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
		return &adk.ChatModelAgentConfig{Name: "employee", Model: parent, MaxIterations: 3, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{localTool{name: "finish", fn: func(context.Context, string) (string, error) { return "host terminal", nil }}}}, ReturnDirectly: map[string]bool{"finish": true}}}, nil
	})
	registry := entry.NewRegistry()
	ref := entry.PluginRef{Name: "researcher", Version: "v1"}
	if e := registry.Register(ref, func(ctx context.Context, req native.Request) (native.Extension, error) {
		if req.Key.Scene != "scene-one" {
			return native.Extension{}, errors.New("wrong scope")
		}
		child, e := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "research", Description: "Host read-only research factory", Model: looptest.New(looptest.Text("scoped fact")), MaxIterations: 1})
		if e != nil {
			return native.Extension{}, e
		}
		return native.Extension{ReadOnlyAgents: []adk.Agent{child}, Handlers: []adk.ChatModelAgentMiddleware{&handler{calls: &middlewareCalls}}, Callbacks: []callbacks.Handler{callbacks.NewHandlerBuilder().OnStartFn(func(c context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			callbackCalls.Add(1)
			return context.WithValue(c, traceKey{}, true)
		}).Build()}}, nil
	}); e != nil {
		t.Fatal(e)
	}
	var ready atomic.Bool
	cfg.ToolsReady = func(_ context.Context, _ native.Request, infos []*schema.ToolInfo) error {
		if len(infos) != 2 || infos[0].Name != "finish" || infos[1].Name != "research" {
			return errors.New("tools were not composed")
		}
		ready.Store(true)
		return nil
	}
	cfg, e := registry.Resolve([]entry.PluginRef{ref}, cfg)
	if e != nil {
		t.Fatal(e)
	}
	r, e := native.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := r.Run(context.Background(), request())
	if e != nil || !out.Completed || out.Output.Content != "host terminal" || !ready.Load() || middlewareCalls.Load() != 2 || callbackCalls.Load() == 0 || !pluginSeen.Load() || !traceSeen.Load() {
		t.Fatal(out, e, ready.Load(), middlewareCalls.Load(), callbackCalls.Load())
	}
	childEvent := false
	for _, ev := range out.Events {
		if ev.Agent == "research" {
			childEvent = true
		}
	}
	if !childEvent {
		t.Fatal("extension AgentTool lost native event", out.Events)
	}
}

type replaceTerminal struct {
	adk.BaseChatModelAgentMiddleware
}

func (*replaceTerminal) BeforeAgent(ctx context.Context, c *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	c.ReturnDirectly["finish"] = false
	return ctx, c, nil
}
func TestExtensionsRejectNamesAndCannotOverrideTerminalOrRevokedAdmission(t *testing.T) {
	for _, scenario := range []string{"duplicate", "terminal", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			db := store(t, t.TempDir())
			defer db.Close()
			var denied atomic.Bool
			var childCalls atomic.Int32
			parent := modelFuncExtension(func(context.Context, []*schema.Message) (*schema.Message, error) {
				if scenario == "revoked" {
					denied.Store(true)
				}
				return looptest.New(looptest.Tools(looptest.Call{ID: "child", Name: "research", Args: map[string]any{"request": "private"}})).Generate(context.Background(), nil)
			})
			cfg := config(db, func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
				return &adk.ChatModelAgentConfig{Name: "employee", Model: parent, MaxIterations: 2, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{&localTool{name: "finish", fn: func(context.Context, string) (string, error) { return "done", nil }}}}, ReturnDirectly: map[string]bool{"finish": true}}}, nil
			})
			baseAuthorize := cfg.Authorize
			cfg.Authorize = func(c context.Context, r native.Request) error {
				if denied.Load() {
					return native.ErrDenied
				}
				return baseAuthorize(c, r)
			}
			cfg.Extensions = []native.ExtensionFactory{func(c context.Context, _ native.Request) (native.Extension, error) {
				name := "research"
				if scenario == "duplicate" {
					name = "finish"
				}
				child, e := adk.NewChatModelAgent(c, &adk.ChatModelAgentConfig{Name: name, Description: "readonly", MaxIterations: 1, Model: modelFuncExtension(func(context.Context, []*schema.Message) (*schema.Message, error) {
					childCalls.Add(1)
					return schema.AssistantMessage("fact", nil), nil
				})})
				if e != nil {
					return native.Extension{}, e
				}
				ext := native.Extension{ReadOnlyAgents: []adk.Agent{child}}
				if scenario == "terminal" {
					ext.Handlers = []adk.ChatModelAgentMiddleware{&replaceTerminal{}}
				}
				return ext, nil
			}}
			r, e := native.New(cfg)
			if e != nil {
				t.Fatal(e)
			}
			_, e = r.Run(context.Background(), request())
			if e == nil {
				t.Fatal("extension violated Host boundary")
			}
			if scenario == "duplicate" && !strings.Contains(e.Error(), "duplicate") {
				t.Fatal(e)
			}
			if scenario == "terminal" && !strings.Contains(e.Error(), "terminal") {
				t.Fatal(e)
			}
			if childCalls.Load() != 0 {
				t.Fatal("child ran after rejection", childCalls.Load())
			}
		})
	}
}

type modelFuncExtension func(context.Context, []*schema.Message) (*schema.Message, error)

func (f modelFuncExtension) Generate(c context.Context, m []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return f(c, m)
}
func (f modelFuncExtension) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}
func (f modelFuncExtension) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not streamed")
}
func TestPluginRegistryRequiresExactUniqueRegistration(t *testing.T) {
	registry := entry.NewRegistry()
	ref := entry.PluginRef{Name: "readonly", Version: "sha-v1"}
	factory := func(context.Context, native.Request) (native.Extension, error) { return native.Extension{}, nil }
	if e := registry.Register(ref, factory); e != nil {
		t.Fatal(e)
	}
	if e := registry.Register(ref, factory); e == nil {
		t.Fatal("duplicate factory accepted")
	}
	base := native.Config{Name: "fixed", Version: "engine-v1"}
	if _, e := registry.Resolve([]entry.PluginRef{{Name: "readonly", Version: "wrong"}}, base); e == nil {
		t.Fatal("version mismatch accepted")
	}
	if _, e := registry.Resolve([]entry.PluginRef{ref, ref}, base); e == nil {
		t.Fatal("duplicate profile plugin accepted")
	}
	got, e := registry.Resolve([]entry.PluginRef{ref}, base)
	if e != nil || got.Name != base.Name || got.Version != base.Version || len(got.Extensions) != 1 || len(base.Extensions) != 0 {
		t.Fatal(got, e)
	}
}
