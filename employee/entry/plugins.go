package entry

import (
	"errors"
	"fmt"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"strings"
	"sync"

	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
)

// PluginRef selects Host-registered Go code, never a dynamic command or URL.
// Versions are exact immutable identifiers; there is no latest-version fallback.
type PluginRef = contract.PluginRef

type Registry struct {
	mu        sync.RWMutex
	factories map[PluginRef]native.ExtensionFactory
}

func NewRegistry() *Registry { return &Registry{factories: map[PluginRef]native.ExtensionFactory{}} }
func validPlugin(ref PluginRef) bool {
	return strings.TrimSpace(ref.Name) != "" && strings.TrimSpace(ref.Version) != "" && ref.Name == strings.TrimSpace(ref.Name) && ref.Version == strings.TrimSpace(ref.Version)
}
func (r *Registry) Register(ref PluginRef, factory native.ExtensionFactory) error {
	if r == nil || !validPlugin(ref) || factory == nil {
		return errors.New("entry: exact plugin name/version and Host factory required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = map[PluginRef]native.ExtensionFactory{}
	}
	if _, exists := r.factories[ref]; exists {
		return fmt.Errorf("entry: plugin %s@%s already registered", ref.Name, ref.Version)
	}
	r.factories[ref] = factory
	return nil
}

// Resolve copies the base runtime config and appends selected extension factories.
// Admission, scoped stores, factory, terminal effects and base callbacks stay Host-owned.
func (r *Registry) Resolve(refs []PluginRef, base native.Config) (native.Config, error) {
	factories := append([]native.ExtensionFactory(nil), base.Extensions...)
	seen := map[string]bool{}
	if r != nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
	}
	for _, ref := range refs {
		if !validPlugin(ref) || seen[ref.Name] {
			return native.Config{}, errors.New("entry: exact unique plugin references required")
		}
		seen[ref.Name] = true
		if r == nil {
			return native.Config{}, fmt.Errorf("entry: plugin %s@%s is not registered", ref.Name, ref.Version)
		}
		factory, ok := r.factories[ref]
		if !ok {
			return native.Config{}, fmt.Errorf("entry: plugin %s@%s is not registered", ref.Name, ref.Version)
		}
		factories = append(factories, factory)
	}
	base.Extensions = factories
	return base, nil
}
