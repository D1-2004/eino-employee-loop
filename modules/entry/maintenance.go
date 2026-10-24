package entry

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/contract"
	native "github.com/D1-2004/eino-employee-loop/kernel/eino"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	ownerloop "github.com/D1-2004/eino-employee-loop/modules/owner"
	taskmodule "github.com/D1-2004/eino-employee-loop/modules/task"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"sort"
	"time"
)

func (s *standalone) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now().UTC()
}
func (s *standalone) ownerOnce(ctx context.Context) (bool, error) {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	admitted, err := s.chat.Admissions(ctx)
	if err != nil {
		return false, err
	}
	for _, admission := range admitted {
		p := admission.Profile
		if len(p.OwnerLoops) == 0 {
			continue
		}
		worker, e := s.ownerFor(ctx, p)
		if e != nil {
			return false, e
		}
		if e = worker.Observe(ctx, p.Scope(admission.Input.Scene), admission.Input.ID); e != nil && !errors.Is(e, contextbuilder.ErrRevoked) {
			return false, e
		}
	}
	keys := make([]string, 0, len(s.ownerWorkers))
	for key := range s.ownerWorkers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, scene := range s.opts.Profile.Scenes {
			progress, err := s.ownerWorkers[key].Tick(ctx, s.opts.Profile.Scope(scene))
			if progress || err != nil {
				return progress, err
			}
		}
	}
	return false, nil
}
func (s *standalone) Schedule(ctx context.Context, scope work.Scope, in work.Schedule) (work.Schedule, error) {
	return s.tasksWorker.Schedule(ctx, scope, in)
}
func (s *standalone) Signal(ctx context.Context, in Input, condition string) error {
	if in.Origin == "" {
		return work.ErrScope
	}
	return s.tasksWorker.Signal(ctx, in, condition)
}
func (s *standalone) OwnerDocuments(ctx context.Context, scope work.Scope) ([]ownerloop.Document, error) {
	return s.ownerWorker.Documents(ctx, scope)
}
func (s *standalone) reviewerFor(ctx context.Context, p contract.Profile) (taskmodule.Reviewer, error) {
	if err := s.opts.Modules.validate(p.Modules); err != nil {
		return nil, err
	}
	if p.Modules.Review.Effective() == builtinModule {
		return &taskmodule.NativeReviewer{Runner: s.runner, Context: s.context}, nil
	}
	s.opts.Modules.mu.RLock()
	factory := s.opts.Modules.reviews[p.Modules.Review]
	s.opts.Modules.mu.RUnlock()
	return factory(ctx, s.reviewDependencies(p))
}
func (s *standalone) defaultOwnerFor(profile Profile) (*ownerloop.Worker, error) {
	roles := []ownerloop.RoleConfig{}
	for _, name := range profile.OwnerLoops {
		roles = append(roles, ownerloop.RoleConfig{Name: name, MaxModelCalls: profile.MaxModelCalls})
	}
	if len(roles) == 0 {
		roles = ownerloop.DefaultRoles()
	} // Tick restores already admitted work; new admission is disabled above.
	d := s.ownerDependencies(profile)
	return ownerloop.New(ownerloop.Options{DB: d.State, Files: d.Artifacts, Context: d.Context, Owner: d.Owner, Version: d.Profile.Revision, Configuration: encode(d.Profile), Callbacks: d.Callbacks, Roles: roles, Authorize: d.Authorize, ConfigureRuntime: d.Plugins, Models: d.Models})
}

func (s *standalone) ownerDependencies(profile Profile) OwnerDependencies {
	// Capture only the factories needed by Owner. Every recovered observation
	// selects its own immutable Profile rather than the current admission profile.
	models, plugins := s.opts.Models, s.opts.Plugins
	frozenProfile := func(r ownerloop.Request) (Profile, error) {
		var frozen Profile
		if len(r.Configuration) == 0 {
			return frozen, errors.New("entry: owner observation missing frozen profile")
		}
		if err := json.Unmarshal(r.Configuration, &frozen); err != nil {
			return frozen, err
		}
		if frozen.Revision != r.Version {
			return frozen, errors.New("entry: owner observation profile revision mismatch")
		}
		return frozen, nil
	}
	return OwnerDependencies{State: s.db, Artifacts: s.files, Context: s.context, Owner: s.owner, Authorize: s.authorize, Profile: profileSnapshot(profile), Callbacks: append([]callbacks.Handler(nil), s.opts.Callbacks...), Plugins: func(_ context.Context, r ownerloop.Request, cfg native.Config) (native.Config, error) {
		frozen, err := frozenProfile(r)
		if err != nil {
			return native.Config{}, err
		}
		return plugins.Resolve(frozen.Plugins, cfg)
	}, Models: func(c context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		frozen, err := frozenProfile(r)
		if err != nil {
			return nil, err
		}
		return models(c, ModelRequest{Role: "owner_" + r.Role, Receipt: r.Receipt, Scope: r.Scope, Profile: frozen, ModelName: frozen.ModelName})
	}}
}

func (s *standalone) defaultScheduler() (SchedulerRuntime, error) {
	return work.NewScheduler(s.db, s.work, s.authorize, func(c context.Context, scope work.Scope, src work.Source) error {
		current, _, err := s.context.Source(c, scope, src.Receipt)
		if err != nil {
			return err
		}
		if current.Actor != src.Actor || current.Text != src.Text {
			return work.ErrScope
		}
		return nil
	})
}
func (s *standalone) ownerFor(ctx context.Context, profile Profile) (OwnerRuntime, error) {
	if err := s.opts.Modules.validate(profile.Modules); err != nil {
		return nil, err
	}
	id := string(encode(profile))
	if worker := s.ownerWorkers[id]; worker != nil {
		return worker, nil
	}
	var worker OwnerRuntime
	var err error
	if profile.Modules.Owner.Effective() == builtinModule {
		worker, err = s.defaultOwnerFor(profile)
	} else {
		s.opts.Modules.mu.RLock()
		factory := s.opts.Modules.owners[profile.Modules.Owner]
		s.opts.Modules.mu.RUnlock()
		worker, err = factory(ctx, s.ownerDependencies(profile))
	}
	if worker != nil {
		s.own(worker)
	}
	if err != nil {
		return nil, err
	}
	if nilModule(worker) {
		return nil, errors.New("entry: owner factory returned nil")
	}
	s.ownerWorkers[id] = worker
	return worker, nil
}
