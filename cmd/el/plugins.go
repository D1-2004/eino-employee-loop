package main

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/entry"
	"github.com/cloudwego/eino/callbacks"
)

type compiledPlugin struct {
	Name, Version, Description string
}

// This catalog is compiled Host code, never an executable path or network URL.
var compiledPlugins = []compiledPlugin{{Name: "audit", Version: "v1", Description: "Metadata-only native lifecycle events to stderr (best effort)"}}

func pluginRegistry(out io.Writer) (*entry.Registry, error) {
	registry := entry.NewRegistry()
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	emit := func(role, event string) error {
		mu.Lock()
		defer mu.Unlock()
		return enc.Encode(struct {
			Type, Plugin, Version, Role, Event string
		}{"plugin_audit", "audit", "v1", role, event})
	}
	err := registry.Register(entry.PluginRef{Name: "audit", Version: "v1"}, func(_ context.Context, req native.Request) (native.Extension, error) {
		role := req.Key.Lane
		if err := emit(role, "runtime_configured"); err != nil {
			return native.Extension{}, err
		}
		handler := callbacks.NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
				_ = emit(role, "start")
				return ctx
			}).
			OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
				_ = emit(role, "end")
				return ctx
			}).
			OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
				_ = emit(role, "error")
				return ctx
			}).Build()
		return native.Extension{Callbacks: []callbacks.Handler{handler}}, nil
	})
	return registry, err
}
