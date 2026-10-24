// Package contract contains shared identity, configuration and receipt values.
// It contains no business services or adapter implementations.
package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"strings"
)

var ErrScope = errors.New("work: scope or current source mismatch")

type Scope struct{ Workspace, Tenant, Employee, Scene, Requester string }

func (s Scope) Key() (string, error) {
	for _, v := range []string{s.Workspace, s.Tenant, s.Employee, s.Scene, s.Requester} {
		if strings.TrimSpace(v) == "" {
			return "", ErrScope
		}
	}
	raw, _ := json.Marshal(s)
	return string(raw), nil
}
func Digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

type PluginRef struct{ Name, Version string }

func (r PluginRef) Valid() bool {
	return strings.TrimSpace(r.Name) != "" && strings.TrimSpace(r.Version) != "" && r.Name == strings.TrimSpace(r.Name) && r.Version == strings.TrimSpace(r.Version)
}

type ModuleRef struct{ Name, Version string }

func (r ModuleRef) Effective() ModuleRef {
	if r == (ModuleRef{}) {
		return ModuleRef{Name: "builtin", Version: "1"}
	}
	return r
}
func (r ModuleRef) Valid() bool { return (PluginRef{r.Name, r.Version}).Valid() }

type ModuleSelection struct{ Storage, Context, Executor, Review, Scheduler, Owner ModuleRef }

func (m ModuleSelection) Core() ModuleSelection {
	m.Review = ModuleRef{}
	m.Owner = ModuleRef{}
	m.Storage = m.Storage.Effective()
	m.Context = m.Context.Effective()
	m.Executor = m.Executor.Effective()
	m.Scheduler = m.Scheduler.Effective()
	return m
}

type Profile struct {
	ModelBindings                                                                       map[string]ModelBinding `json:",omitempty"`
	HostTools                                                                           bool
	Modules                                                                             ModuleSelection
	Plugins                                                                             []PluginRef
	Version, ID, Revision, Workspace, Tenant, Employee, Owner, Name, Backend, ModelName string
	Scenes                                                                              []string
	Instructions                                                                        string
	OwnerLoops                                                                          []string
	DeliveryLoops                                                                       []string
	MaxModelCalls, MaxRuns                                                              int
}

func (p Profile) Scope(scene string) Scope {
	return Scope{Workspace: p.Workspace, Tenant: p.Tenant, Employee: p.Employee, Scene: scene, Requester: p.Owner}
}
func (p Profile) Key() string {
	raw, _ := json.Marshal([]string{p.Workspace, p.Tenant, p.Employee, p.Owner, p.ID})
	return Digest(raw)
}
func (p Profile) Validate() error {
	for role, binding := range p.ModelBindings {
		if !modelRole(role) {
			return errors.New("contract: invalid model role")
		}
		if err := binding.Validate(); err != nil {
			return err
		}
	}
	if p.Version != "local-el-profile/1" || p.ID == "" || p.Revision == "" || p.Workspace == "" || p.Tenant == "" || p.Employee == "" || p.Owner == "" || p.Backend == "" || p.ModelName == "" || len(p.Scenes) == 0 || p.MaxModelCalls < 1 || p.MaxModelCalls > 128 || p.MaxRuns < 1 || p.MaxRuns > 512 {
		return errors.New("entry: invalid immutable profile")
	}
	for _, group := range []struct {
		names   []string
		allowed map[string]bool
	}{{p.OwnerLoops, nil}, {p.DeliveryLoops, map[string]bool{"expression": true, "package": true}}} {
		seen := map[string]bool{}
		for _, name := range group.names {
			if name == "" || seen[name] || (group.allowed != nil && !group.allowed[name]) {
				return errors.New("entry: unknown or duplicate loop")
			}
			seen[name] = true
		}
	}
	pluginNames := map[string]bool{}
	for _, ref := range p.Plugins {
		if !ref.Valid() || pluginNames[ref.Name] {
			return errors.New("entry: exact unique plugins required")
		}
		pluginNames[ref.Name] = true
	}
	seen := map[string]bool{}
	for _, s := range p.Scenes {
		if s == "" || seen[s] {
			return errors.New("entry: explicit unique scenes required")
		}
		seen[s] = true
	}
	return nil
}

type Input struct {
	ID, Scene, Actor, Text, Origin string
	Native                         json.RawMessage
}
type Admission struct {
	ID, State, Backend, ProfileRevision string
	Replay                              bool
}
type ModelRequest struct {
	Binding                  ModelBinding
	Role, Receipt, ModelName string
	Scope                    Scope
	Profile                  Profile
}
type Models func(context.Context, ModelRequest) (model.ToolCallingChatModel, error)

// Question is the version-stable native interruption payload used by the Host.
type Question struct{ ID, Prompt string }

// Failure is a scoped recovery handle, without model input or credentials.
type Failure struct{ Kind, ID, State, Error string }
