package entry

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/host"
	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
)

func (s *standalone) authorizeHost(ctx context.Context, c host.Call) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("entry: closed")
	}
	if err := s.authorize(ctx, c.Scope); err != nil {
		return err
	}
	// The default local Host has no platform binding issuer. A remote adapter
	// authenticates and interprets its own opaque binding, never model arguments.
	if len(c.Binding) > 0 && string(c.Binding) != "null" {
		return work.ErrScope
	}
	if c.TaskID != "" {
		task, err := s.work.Get(ctx, c.Scope, c.TaskID)
		if err != nil {
			return err
		}
		if c.RunID != "" {
			found := false
			for _, run := range task.Runs {
				if run.ID == c.RunID {
					found = true
					break
				}
			}
			if !found {
				return work.ErrScope
			}
		}
		if c.SourceReceipt != "" {
			found := false
			for _, source := range task.Sources {
				if source.Receipt == c.SourceReceipt {
					found = true
					break
				}
			}
			if !found {
				return work.ErrScope
			}
		}
	}
	return nil
}
func (s *standalone) localServices() (host.Services, error) {
	return host.NewLocal(host.LocalOptions{Authorize: s.authorizeHost, Config: func(ctx context.Context, c host.Call) (host.ConfigSnapshot, error) {
		p := s.opts.Profile
		if c.TaskID != "" {
			t, err := s.work.Get(ctx, c.Scope, c.TaskID)
			if err != nil {
				return host.ConfigSnapshot{}, err
			}
			if err = json.Unmarshal(t.Definition.Configuration, &p); err != nil {
				return host.ConfigSnapshot{}, err
			}
		}
		return host.ConfigSnapshot{ID: p.ID, Version: p.Revision, Value: encode(p)}, nil
	}, Sources: sourceReader{s.context}, Tasks: taskReader{s.work}, Artifacts: s.files, Overrides: s.opts.Services})
}
func (s *standalone) HostServices() host.Services { return s.services }

// HostServices exposes capability clients. The default clients recheck the Entry
// lifetime and authorization on every use; injected clients retain that contract.
func (e *Entry) HostServices(ctx context.Context) (host.Services, error) {
	_, done, err := e.operation(ctx)
	if err != nil {
		return host.Services{}, err
	}
	defer done()
	backend, ok := e.backend.(interface{ HostServices() host.Services })
	if !ok {
		return host.Services{}, ErrUnsupported
	}
	return e.serviceClients(backend.HostServices()), nil
}

type frozenHostTools struct {
	Call  host.Call
	Tools []host.Tool
}

func (s *standalone) hostFunctions(ctx context.Context, scope work.Scope, receipt string, frame contextbuilder.Frame, before func(context.Context) error) ([]*function, error) {
	c := host.Call{Scope: scope}
	if frame.Current != nil {
		c.SourceReceipt = frame.Current.ID
	} else if len(frame.Sources) > 0 {
		c.SourceReceipt = frame.Sources[0].ID
	}
	if len(frame.Tasks) > 0 {
		task := frame.Tasks[0]
		c.TaskID = task.ID
		if run := task.CurrentRun(); run != nil {
			c.RunID = run.ID
		}
	}
	if err := s.authorizeHost(ctx, c); err != nil {
		return nil, err
	}
	if err := before(ctx); err != nil {
		return nil, err
	}
	definitions, err := s.services.Tools.DiscoverTools(ctx, c)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.ID == "" || seen[definition.ID] || definition.Effect || !json.Valid(definition.Schema) {
			return nil, host.ErrUnsupported
		}
		seen[definition.ID] = true
	}
	frozen := frozenHostTools{Call: c, Tools: definitions}
	key, _ := scope.Key()
	err = s.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "host-tool-catalog", key, receipt)
		if e == nil {
			if string(d.Body) != string(encode(frozen)) {
				return errors.New("entry: frozen Host tool catalog changed")
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "host-tool-catalog", Scope: key, ID: receipt, Body: encode(frozen)})
		return e
	})
	if err != nil {
		return nil, err
	}
	functions := make([]*function, 0, len(definitions))
	for _, definition := range definitions {
		var params map[string]any
		if err = json.Unmarshal(definition.Schema, &params); err != nil || params["type"] != "object" {
			return nil, host.ErrInvalid
		}
		definition := definition
		functions = append(functions, &function{spec: loop.Tool{Name: definition.ID, Description: definition.Description, Schema: params}, before: before, run: func(ctx context.Context, id string, args map[string]any) (any, error) {
			if err := s.authorizeHost(ctx, c); err != nil {
				return nil, err
			}
			result, err := s.services.Tools.CallTool(ctx, c, host.ToolCall{ID: definition.ID, CallID: id, Arguments: encode(args)})
			if err != nil {
				return nil, err
			}
			if err = before(ctx); err != nil {
				return nil, err
			}
			if !json.Valid(result.Content) {
				return nil, host.ErrInvalid
			}
			return result.Content, nil
		}})
	}
	return functions, nil
}
