package entry

import (
	"context"
	"encoding/json"
	"errors"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
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
	if s.opts.BuildServices == nil {
		return host.Services{}, errors.New("entry: service assembly required; use app/local for defaults")
	}
	return s.opts.BuildServices(host.Bindings{Authorize: s.authorizeHost, Config: func(ctx context.Context, c host.Call) (host.ConfigSnapshot, error) {
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

func (s *standalone) hostFunctions(ctx context.Context, scope work.Scope, receipt string, frame contextbuilder.Frame, before func(context.Context) error) ([]*kernel.Function, error) {
	c := host.Call{Scope: scope}
	if frame.Current != nil {
		c.SourceReceipt = frame.Current.ID
	} else if len(frame.Sources) > 0 {
		c.SourceReceipt = frame.Sources[0].ID
	}
	if len(frame.Tasks) > 0 {
		t := frame.Tasks[0]
		c.TaskID = t.ID
		if r := t.CurrentRun(); r != nil {
			c.RunID = r.ID
		}
	}
	if s.opts.PrepareTools == nil {
		return nil, errors.New("entry: native Host tool adapter is not assembled")
	}
	return s.opts.PrepareTools(ctx, host.NativeToolBindings{State: s.db, Authorize: s.authorizeHost}, s.services.Tools, c, receipt, before)
}
