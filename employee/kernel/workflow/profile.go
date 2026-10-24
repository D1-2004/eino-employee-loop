package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/D1-2004/eino-employee-loop/employee/internal/jsoncheck"
	"github.com/cloudwego/eino/schema"
)

// Profile is immutable operator configuration. Changing a binding requires a new
// revision, which gets a separate session namespace rather than moving history.
type Profile struct {
	Version  string         `json:"version"`
	Name     string         `json:"name"`
	Revision string         `json:"revision"`
	Agents   []AgentBinding `json:"agents"`
}
type AgentBinding struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	ProviderVersion string `json:"provider_version"`
	Lane            string `json:"lane"`
	// Session is interactive, task, routine or isolated. Task/routine subjects
	// must be supplied by trusted Host admission, never inferred from messages.
	Session string `json:"session"`
}

func ParseProfile(raw []byte) (Profile, error) {
	var p Profile
	if len(raw) > 1<<20 {
		return p, fmt.Errorf("workflow: profile exceeds bound")
	}
	if err := jsoncheck.Unique(raw, 32); err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	return p, validateProfile(p)
}
func validateProfile(p Profile) error {
	if p.Version != "workflow-profile/1" || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Revision) == "" || len(p.Agents) == 0 || len(p.Agents) > 64 {
		return ErrVersion
	}
	seen := map[string]bool{}
	for _, a := range p.Agents {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Provider) == "" || strings.TrimSpace(a.ProviderVersion) == "" || strings.TrimSpace(a.Lane) == "" || seen[a.Name] {
			return ErrVersion
		}
		seen[a.Name] = true
		switch a.Session {
		case "interactive", "task", "routine", "isolated":
		default:
			return ErrVersion
		}
	}
	return nil
}

// Assembly resolves only code-registered Provider/version pairs. A profile
// cannot load executables, URLs, credentials or model-authored plugins.
type Assembly struct {
	profile Profile
	agents  map[string]AgentBinding
	runtime Runtime
}

func NewAssembly(p Profile, registry *Registry, store Store, gate Gate) (*Assembly, error) {
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	if store == nil || gate == nil {
		return nil, ErrDenied
	}
	a := &Assembly{profile: p, agents: map[string]AgentBinding{}, runtime: Runtime{Registry: registry, Store: store, Gate: gate}}
	for _, binding := range p.Agents {
		if _, err := registry.lookup(binding.Provider, binding.ProviderVersion); err != nil {
			return nil, err
		}
		a.agents[binding.Name] = binding
	}
	// Caller-owned configuration slices must not mutate a running assembly.
	a.profile.Agents = append([]AgentBinding(nil), p.Agents...)
	return a, nil
}

func (a *Assembly) Run(ctx context.Context, agent string, key Key, receipt string, messages []*schema.Message, data json.RawMessage) (Result, error) {
	binding, ok := a.agents[agent]
	if !ok {
		return Result{}, ErrVersion
	}
	if key.Profile != "" && key.Profile != a.profile.Name || key.ProfileRevision != "" && key.ProfileRevision != a.profile.Revision {
		return Result{}, ErrConflict
	}
	key.Profile, key.ProfileRevision = a.profile.Name, a.profile.Revision
	if key.ProfileBinding != "" && key.ProfileBinding != binding.Name {
		return Result{}, ErrConflict
	}
	key.ProfileBinding = binding.Name
	if key.Lane != "" && key.Lane != binding.Lane {
		return Result{}, ErrConflict
	}
	key.Lane = binding.Lane
	switch binding.Session {
	case "interactive":
		if key.Subject != "" && key.Subject != "interactive" {
			return Result{}, ErrConflict
		}
		key.Subject = "interactive"
	case "isolated":
		if key.Subject != "" && key.Subject != receipt {
			return Result{}, ErrConflict
		}
		key.Subject = receipt
	case "task", "routine":
		if strings.TrimSpace(key.Subject) == "" {
			return Result{}, ErrDenied
		}
	}
	return a.runtime.Run(ctx, Request{Key: key, Receipt: receipt, Kind: binding.Provider, Version: binding.ProviderVersion, Messages: messages, Data: data})
}
