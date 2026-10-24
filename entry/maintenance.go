package entry

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/ownerloop"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
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
	if s.ownerWorker == nil {
		return false, nil
	}
	// Reconcile admissions from the durable inbox: a crash between acceptance and
	// maintenance admission cannot silently lose a source-driven worker.
	docs, err := s.db.List(ctx, "entry-jobs", s.opts.Profile.Key())
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		var j job
		if err = json.Unmarshal(d.Body, &j); err != nil {
			return false, err
		}
		if len(j.Profile.OwnerLoops) == 0 {
			continue
		}
		worker, e := s.ownerFor(ctx, j.Profile)
		if e != nil {
			return false, e
		}
		if err = worker.Observe(ctx, j.Profile.Scope(j.Input.Scene), j.Input.ID); err != nil && !errors.Is(err, contextbuilder.ErrRevoked) {
			return false, err
		}
	}
	keys := make([]string, 0, len(s.ownerWorkers))
	for key := range s.ownerWorkers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		worker := s.ownerWorkers[key]
		for _, scene := range s.opts.Profile.Scenes {
			progress, err := worker.Tick(ctx, s.opts.Profile.Scope(scene))
			if err != nil || progress {
				return progress, err
			}
		}
	}

	return false, nil
}
func (s *standalone) timingOnce(ctx context.Context) (bool, error) {
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		changed, err := s.scheduler.Tick(ctx, scope, s.now())
		if changed || err != nil {
			return changed, err
		}
		occurrences, err := s.scheduler.Occurrences(ctx, scope)
		if err != nil {
			return false, err
		}
		for _, o := range occurrences {
			if o.Acknowledged {
				continue
			}
			task, err := s.work.Get(ctx, scope, o.TaskID)
			if err != nil {
				return false, err
			}
			if task.Goal.Revision != o.GoalRevision {
				recovered, e := s.scheduler.RecoverOccurrence(ctx, scope, o.ID)
				if e != nil || recovered {
					return recovered, e
				}
				return true, s.scheduler.Acknowledge(ctx, scope, o.ID)
			}
			if task.State == work.Stopped || task.State == work.Paused {
				return true, s.scheduler.Acknowledge(ctx, scope, o.ID)
			}
			if task.State == work.Waiting || task.NeedsReview || task.CurrentRun() == nil || !task.CurrentRun().ExitProven {
				continue
			}
			return true, s.attend(ctx, task, o)
		}
	}
	return false, nil
}
func (s *standalone) attend(ctx context.Context, task work.Task, o work.Occurrence) error {
	frame, err := s.context.Build(ctx, task.Scope, contextbuilder.Execution, "", task.ID)
	if err != nil {
		return err
	}
	var profile Profile
	if err = json.Unmarshal(task.Definition.Configuration, &profile); err != nil {
		return err
	}
	var proposal string
	fn := &function{spec: spec("decide_attention", "Handle this authorized periodic occurrence on the same Task. Continue only the accepted work; skip if no action is appropriate.", map[string]any{"kind": map[string]any{"type": "string", "enum": []any{"continue", "skip"}}, "reason": stringField()}, "kind", "reason"), before: func(c context.Context) error { return s.context.Revalidate(c, frame) }, run: func(_ context.Context, _ string, a map[string]any) (any, error) {
		proposal = str(a, "kind")
		return map[string]string{"attention_proposal": proposal}, nil
	}}
	frame.Gaps = append(frame.Gaps, "Authorized occurrence: "+string(encode(o)))
	out, err := s.runAgent(ctx, profile, task.Scope, "task_attention", o.ID, frame, "Evaluate the authorized occurrence against the existing Task and original sources. You may continue the SAME Task or skip this occurrence. Never create a replacement Task or invent a new goal. An active or waiting task is not duplicated.", []*function{fn}, map[string]bool{"decide_attention": true}, false)
	if err != nil {
		return err
	}
	if proposal == "" && out.Output != nil {
		var p struct {
			Kind string `json:"attention_proposal"`
		}
		_ = json.Unmarshal([]byte(out.Output.Content), &p)
		proposal = p.Kind
	}
	if proposal == "skip" {
		return s.scheduler.Acknowledge(ctx, task.Scope, o.ID)
	}
	if proposal != "continue" {
		return errors.New("attention: missing validated decision")
	}
	schedules, e := s.scheduler.Schedules(ctx, task.Scope)
	if e != nil {
		return e
	}
	var src work.Source
	for _, schedule := range schedules {
		if schedule.ID == o.ScheduleID {
			src = schedule.Source
			break
		}
	}
	if src.Receipt == "" {
		return work.ErrStale
	}
	receipt, err := s.work.Control(ctx, task.Scope, src, o.ID, task.ID, "continue", task.Definition.Instruction)
	if err != nil {
		return err
	}
	return s.scheduler.AcknowledgeForGoal(ctx, task.Scope, o.ID, o.GoalRevision, receipt.GoalRevision)
}
func (s *standalone) Schedule(ctx context.Context, scope work.Scope, in work.Schedule) (work.Schedule, error) {
	return s.scheduler.Schedule(ctx, scope, in)
}
func (s *standalone) Signal(ctx context.Context, in Input, condition string) error {
	scope := s.opts.Profile.Scope(in.Scene)
	if in.Origin == "" {
		return work.ErrScope
	}
	if _, err := s.context.Admit(ctx, scope, contextbuilder.Source{ID: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
		return err
	}
	return s.scheduler.Signal(ctx, scope, work.Signal{ID: in.ID, Condition: condition, Payload: in.Text, Source: work.Source{Receipt: in.ID, Actor: in.Actor, Text: in.Text}, At: s.now()})
}
func (s *standalone) OwnerDocuments(ctx context.Context, scope work.Scope) ([]ownerloop.Document, error) {
	if s.ownerWorker == nil {
		return nil, nil
	}
	return s.ownerWorker.Documents(ctx, scope)
}

func (s *standalone) cancelExecution(runID string) {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if cancel := s.executionCancels[runID]; cancel != nil {
		cancel()
	}
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
	if profile.Modules.Owner.effective() == builtinModule {
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
