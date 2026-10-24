// Package models contains reference application model Providers. Employee only
// depends on contract.Models and never imports this package.
package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/cloudwego/eino/components/model"
)

type Ref struct{ Name, Version string }
type Factory func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error)
type provider struct {
	factory Factory
	efforts map[string]bool
}
type Registry struct {
	mu        sync.RWMutex
	providers map[Ref]provider
	legacy    Factory
}

func New(legacy Factory) *Registry { return &Registry{providers: map[Ref]provider{}, legacy: legacy} }

// Register declares the exact Provider version and supported effort values.
// Include "" explicitly when the Provider permits its default reasoning level.
func (r *Registry) Register(ref Ref, efforts []string, f Factory) error {
	if !(contract.PluginRef{Name: ref.Name, Version: ref.Version}).Valid() || f == nil || len(efforts) == 0 {
		return errors.New("models: exact provider version, factory and effort declaration required")
	}
	allowed := map[string]bool{}
	for _, effort := range efforts {
		if effort != strings.TrimSpace(effort) {
			return errors.New("models: effort must be exact")
		}
		if allowed[effort] {
			return errors.New("models: duplicate effort")
		}
		allowed[effort] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = map[Ref]provider{}
	}
	if _, exists := r.providers[ref]; exists {
		return errors.New("models: duplicate provider version")
	}
	r.providers[ref] = provider{factory: f, efforts: allowed}
	return nil
}

func (r *Registry) Models(ctx context.Context, request contract.ModelRequest) (model.ToolCallingChatModel, error) {
	// Re-resolve rather than trusting a mutable host-built request Binding. All
	// model-consuming paths must agree with the immutable Profile and loop role.
	resolved, err := contract.ResolveModelRequest(request.Profile, request.Role, request.Receipt, request.Scope)
	if err != nil {
		return nil, err
	}
	if resolved.Binding.Provider == "" {
		if r.legacy == nil {
			return nil, errors.New("models: legacy model Provider not configured")
		}
		return r.legacy(ctx, resolved)
	}
	b := resolved.Binding
	ref := Ref{Name: b.Provider, Version: b.Version}
	r.mu.RLock()
	p, exists := r.providers[ref]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("models: provider %s@%s is not registered", ref.Name, ref.Version)
	}
	if !p.efforts[b.Effort] {
		return nil, fmt.Errorf("models: provider %s@%s does not support effort %q", ref.Name, ref.Version, b.Effort)
	}
	return p.factory(ctx, resolved)
}
