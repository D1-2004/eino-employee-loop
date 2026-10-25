package contract

import (
	"encoding/json"
	"errors"
	"strings"
)

// ModelBinding pins the application's model Provider independently for each loop.
// It contains no endpoint or secret; Version identifies the Provider factory API.
type ModelBinding struct{ Provider, Version, Model, Effort string }

func (b ModelBinding) Validate() error {
	for _, s := range []string{b.Provider, b.Version, b.Model} {
		if s == "" || s != strings.TrimSpace(s) {
			return errors.New("contract: exact model provider, version and model required")
		}
	}
	if b.Effort != strings.TrimSpace(b.Effort) {
		return errors.New("contract: invalid model effort")
	}
	return nil
}

// Identity preserves legacy journal keys for old profiles, while explicit
// selections include every model-affecting configuration field.
func (b ModelBinding) Identity() string {
	if b.Provider == "" && b.Version == "" && b.Effort == "" {
		return b.Model
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

func modelRole(role string) bool {
	// Extension-defined sub-loops may use their own exact role. Wildcards and
	// whitespace are invalid: an implicit wildcard could silently change a loop.
	return role != "" && role == strings.TrimSpace(role) && !strings.ContainsAny(role, " *\t\n\r")
}

// Snapshot owns all mutable profile collections; a caller can safely reuse its
// source buffers for the next revision after admission.
func (p Profile) Snapshot() Profile {
	if p.PromptBundle != nil {
		bundle := *p.PromptBundle
		bundle.Roles = make(map[string]string, len(p.PromptBundle.Roles))
		for role, text := range p.PromptBundle.Roles {
			bundle.Roles[role] = text
		}
		bundle.UnresolvedBindings = append([]string(nil), bundle.UnresolvedBindings...)
		p.PromptBundle = &bundle
	}
	p.Scenes = append([]string(nil), p.Scenes...)
	p.OwnerLoops = append([]string(nil), p.OwnerLoops...)
	p.DeliveryLoops = append([]string(nil), p.DeliveryLoops...)
	p.Plugins = append([]PluginRef(nil), p.Plugins...)
	if p.ModelBindings != nil {
		cp := make(map[string]ModelBinding, len(p.ModelBindings))
		for role, b := range p.ModelBindings {
			cp[role] = b
		}
		p.ModelBindings = cp
	}
	return p
}

// ResolveModelRequest uses only the frozen admission profile. ModelName mirrors
// the selected model for existing injected factories; Binding carries the exact
// Provider/version/effort when the role has opted into explicit composition.
func ResolveModelRequest(p Profile, role, receipt string, scope Scope) (ModelRequest, error) {
	p = p.Snapshot()
	b, explicit := p.ModelBindings[role]
	if explicit {
		if err := b.Validate(); err != nil {
			return ModelRequest{}, err
		}
	} else {
		b.Model = p.ModelName
	}
	if b.Model == "" {
		return ModelRequest{}, errors.New("contract: model name required")
	}
	return ModelRequest{Role: role, Receipt: receipt, Scope: scope, Profile: p, ModelName: b.Model, Binding: b}, nil
}
