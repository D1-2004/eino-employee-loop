// Package hostext is an example host extension profile: event types and
// tool specs that an embedding platform adds for flows the Employee Loop
// does not implement (plans, collections, reminders, scene configuration).
//
// Nothing in the loop imports this package, produces these events or
// executes these tools. A platform that implements such a flow does what
// this package shows with its own data:
//
//   - register its event types next to the standard ones
//     (Catalog, or contract.Catalog.Register on its own catalog);
//   - offer its tool specs through toolhost.WithCatalog next to the
//     standard tools and execute them with its own contract.Host;
//   - ship the texts of its summary and description keys in its own i18n
//     pack and merge it into the loop's bundle (Messages, i18n.Extend).
//
// The profile lives in extensions.json and locales/; its keys are prefixed
// with "hostext." so they never collide with the loop's own packs.
package hostext

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// RefPlan is the event ref naming the plan a continue_plan call targets.
const RefPlan = "plan"

//go:embed extensions.json
var profileJSON []byte

//go:embed locales
var localesFS embed.FS

type profile struct {
	Version int                 `json:"version"`
	Types   []contract.TypeSpec `json:"types"`
	Tools   []contract.ToolSpec `json:"tools"`
}

func read() (profile, error) {
	var p profile
	dec := json.NewDecoder(strings.NewReader(string(profileJSON)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("hostext: extensions.json: %w", err)
	}
	return p, nil
}

// EventTypes decodes the profile's event types. They are validated and
// never collide with a standard type.
func EventTypes() ([]contract.TypeSpec, error) {
	p, err := read()
	if err != nil {
		return nil, err
	}
	std, err := contract.StandardEventTypes()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range std {
		seen[s.Type] = true
	}
	for _, s := range p.Types {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if seen[s.Type] {
			return nil, fmt.Errorf("hostext: duplicate event type %q", s.Type)
		}
		seen[s.Type] = true
	}
	return p.Types, nil
}

// Tools decodes the profile's tool specs. They are validated and never
// collide with a standard tool.
func Tools() ([]contract.ToolSpec, error) {
	p, err := read()
	if err != nil {
		return nil, err
	}
	std, err := contract.StandardTools()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range std {
		seen[s.Name] = true
	}
	for _, s := range p.Tools {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("hostext: duplicate tool %q", s.Name)
		}
		seen[s.Name] = true
	}
	return p.Tools, nil
}

// Catalog returns a new catalog with the standard event types and the
// profile's types.
func Catalog() (*contract.Catalog, error) {
	types, err := EventTypes()
	if err != nil {
		return nil, err
	}
	c := contract.StandardCatalog()
	for _, s := range types {
		if err := c.Register(s); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// LoadMessages merges the profile's pack into b.
func LoadMessages(b *i18n.Bundle) error { return b.LoadFS(localesFS, "locales") }

var (
	messagesOnce sync.Once
	messages     *i18n.Bundle
	messagesErr  error
)

// Messages returns the loop's default bundle plus the profile's pack,
// built once. Treat it as read-only.
func Messages() (*i18n.Bundle, error) {
	messagesOnce.Do(func() { messages, messagesErr = i18n.Extend(localesFS, "locales") })
	return messages, messagesErr
}
