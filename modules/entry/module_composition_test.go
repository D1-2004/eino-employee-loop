package entry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/adapters/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/execution"
	ownerloop "github.com/D1-2004/eino-employee-loop/modules/owner"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

type compositionContext struct {
	ContextRuntime
	builds *int
	closed func()
}

func (c *compositionContext) Build(ctx context.Context, scope work.Scope, view contextbuilder.View, source, task string) (contextbuilder.Frame, error) {
	*c.builds++
	return c.ContextRuntime.Build(ctx, scope, view, source, task)
}
func (c *compositionContext) Close() error {
	if c.closed != nil {
		c.closed()
	}
	return nil
}

type compositionScheduler struct {
	SchedulerRuntime
	ticks, schedules *int
	closed           func()
}

func (s *compositionScheduler) Tick(ctx context.Context, scope work.Scope, now time.Time) (bool, error) {
	*s.ticks++
	return s.SchedulerRuntime.Tick(ctx, scope, now)
}
func (s *compositionScheduler) Schedule(ctx context.Context, scope work.Scope, schedule work.Schedule) (work.Schedule, error) {
	*s.schedules++
	return s.SchedulerRuntime.Schedule(ctx, scope, schedule)
}
func (s *compositionScheduler) Close() error {
	if s.closed != nil {
		s.closed()
	}
	return nil
}

// This owner uses its own versioned durable queue, not the builtin Worker. It
// proves that restoration must drive the selected module, rather than relying
// on a builtin implementation that happens to scan every version's records.
type compositionOwner struct {
	d      OwnerDependencies
	ticks  *int
	ready  *bool
	closed func()
}

func (o *compositionOwner) namespace() string {
	return "composition-owner-" + o.d.Profile.Modules.Owner.Version
}
func (o *compositionOwner) Observe(ctx context.Context, scope work.Scope, receipt string) error {
	if _, _, err := o.d.Context.Source(ctx, scope, receipt); err != nil {
		return err
	}
	key, _ := scope.Key()
	return o.d.State.Transact(ctx, func(tx storage.Tx) error {
		_, err := tx.Get(ctx, o.namespace(), key, receipt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		_, err = tx.Put(ctx, storage.Document{Namespace: o.namespace(), Scope: key, ID: receipt, Body: encode(false)})
		return err
	})
}
func (o *compositionOwner) Tick(ctx context.Context, scope work.Scope) (bool, error) {
	*o.ticks++
	if o.ready != nil && !*o.ready {
		return false, nil
	}
	if err := o.d.Authorize(ctx, scope); err != nil {
		return false, err
	}
	key, _ := scope.Key()
	docs, err := o.d.State.List(ctx, o.namespace(), key)
	if err != nil {
		return false, err
	}
	for _, doc := range docs {
		var done bool
		if err = json.Unmarshal(doc.Body, &done); err != nil {
			return false, err
		}
		if done {
			continue
		}
		doc.Body = encode(true)
		_, err = o.d.State.Put(ctx, doc)
		return err == nil, err
	}
	return false, nil
}
func (o *compositionOwner) Documents(ctx context.Context, scope work.Scope) ([]ownerloop.Document, error) {
	if err := o.d.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, _ := scope.Key()
	docs, err := o.d.State.List(ctx, o.namespace(), key)
	if err != nil {
		return nil, err
	}
	out := []ownerloop.Document{}
	for _, doc := range docs {
		var done bool
		if err = json.Unmarshal(doc.Body, &done); err != nil {
			return nil, err
		}
		if done {
			out = append(out, ownerloop.Document{Role: "memory", Version: o.d.Profile.Revision, JobID: doc.ID, Body: "independent owner processed original source"})
		}
	}
	return out, nil
}
func (o *compositionOwner) Close() error {
	if o.closed != nil {
		o.closed()
	}
	return nil
}

func completeCompositionReview(_ context.Context, req ReviewRequest) (work.Review, error) {
	return work.Review{TaskRevision: req.Task.Revision, GoalRevision: req.Task.Goal.Revision, RunID: req.Task.CurrentRun().ID, Kind: "complete", Summary: "Independent reviewer checked the actual result"}, nil
}

func TestModuleFactoriesComposeRealTaskAndOwnerPaths(t *testing.T) {
	ctx := context.Background()
	registry := NewModuleRegistry()
	ref := ModuleRef{Name: "alternate", Version: "1"}
	builds, ticks, schedules, reviews, ownerTicks := 0, 0, 0, 0, 0
	if err := registry.RegisterContext(ref, func(_ context.Context, d ContextDependencies) (ContextRuntime, error) {
		if _, exposesManager := d.Tasks.(*work.Manager); exposesManager {
			t.Fatal("context factory received business write authority through task reader")
		}
		builder, err := contextbuilder.New(d.State, d.Artifacts, d.Tasks, func(ctx context.Context, s work.Scope, _ contextbuilder.View) error { return d.Authorize(ctx, s) }, contextbuilder.WithAudiencePolicy(d.Audience))
		return &compositionContext{ContextRuntime: builder, builds: &builds}, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterScheduler(ref, func(_ context.Context, d SchedulerDependencies) (SchedulerRuntime, error) {
		if _, exposesContextWrites := d.Context.(ContextRuntime); exposesContextWrites {
			t.Fatal("scheduler factory received mutable context instead of source reader")
		}
		scheduler, err := work.NewScheduler(d.State, d.Work, d.Authorize, func(ctx context.Context, s work.Scope, src work.Source) error {
			actual, _, err := d.Context.Source(ctx, s, src.Receipt)
			if err != nil {
				return err
			}
			if actual.Actor != src.Actor || actual.Text != src.Text {
				return work.ErrScope
			}
			return nil
		})
		return &compositionScheduler{SchedulerRuntime: scheduler, ticks: &ticks, schedules: &schedules}, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterReview(ref, func(_ context.Context, _ ReviewDependencies) (Reviewer, error) {
		return ReviewFunc(func(ctx context.Context, req ReviewRequest) (work.Review, error) {
			reviews++
			if len(req.Frame.Materials) != 1 || req.Frame.Materials[0].Body != "# Actual result" {
				return work.Review{}, errors.New("independent reviewer did not receive artifact readback")
			}
			return completeCompositionReview(ctx, req)
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterOwner(ref, func(_ context.Context, d OwnerDependencies) (OwnerRuntime, error) {
		return &compositionOwner{d: d, ticks: &ownerTicks}, nil
	}); err != nil {
		t.Fatal(err)
	}
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	p.Modules = ModuleSelection{Context: ref, Review: ref, Scheduler: ref, Owner: ref}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Modules: registry, Models: models(map[string]*looptest.Model{
		"chat":     looptest.New(dispatchStep(t)),
		"executor": looptest.New(call("write_file", "artifact", map[string]any{"path": "result.md", "content": "# Actual result"}), call("finish_run", "exit", map[string]any{"state": "succeeded", "summary": "Written result"})),
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("original", "Produce the actual result")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Completed || !tasks[0].CurrentRun().ExitProven {
		t.Fatal(tasks, err)
	}
	docs, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 1 || docs[0].JobID != "original" {
		t.Fatal(docs, err)
	}
	task := tasks[0]
	if _, err = e.Schedule(ctx, "dm", work.Schedule{ID: "authorized", TaskID: task.ID, GoalRevision: task.Goal.Revision, Due: time.Now().Add(time.Hour), Source: task.Sources[0]}); err != nil {
		t.Fatal(err)
	}
	if builds == 0 || ticks == 0 || schedules != 1 || reviews != 1 || ownerTicks == 0 {
		t.Fatal("selected module bypassed", builds, ticks, schedules, reviews, ownerTicks)
	}
}

func compositionTask(t *testing.T, s *standalone, p Profile, exited bool) work.Task {
	t.Helper()
	ctx := context.Background()
	scope := p.Scope("dm")
	source := work.Source{Receipt: "accepted", Actor: p.Owner, Text: "accepted goal"}
	if _, err := s.context.Admit(ctx, scope, contextbuilder.Source{ID: source.Receipt, Actor: source.Actor, Text: source.Text}); err != nil {
		t.Fatal(err)
	}
	r, err := s.work.Dispatch(ctx, scope, source, "dispatch", work.Definition{Goal: "accepted goal", Instruction: "produce result", MaxRuns: 3, Configuration: encode(p)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.work.BeginSubmission(ctx, scope, r.TaskID, r.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.work.Submission(ctx, scope, r.TaskID, r.RunID, "actual-provider-receipt", false); err != nil {
		t.Fatal(err)
	}
	if exited {
		if _, err = s.work.RecordExecution(ctx, scope, r.TaskID, "provider-exit", work.ExecutionFact{RunID: r.RunID, ExecutionID: "actual-provider-receipt", State: work.RunSucceeded, ExitProven: true, Output: "actual provider result"}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := s.work.Get(ctx, scope, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestIndependentReviewerCannotBypassSnapshotOrExitAuthority(t *testing.T) {
	for _, kind := range []string{"task-revision", "goal-revision", "run-id", "no-exit"} {
		t.Run(kind, func(t *testing.T) {
			registry := NewModuleRegistry()
			ref := ModuleRef{Name: "independent-review", Version: "1"}
			if err := registry.RegisterReview(ref, func(context.Context, ReviewDependencies) (Reviewer, error) {
				return ReviewFunc(func(ctx context.Context, req ReviewRequest) (work.Review, error) {
					proposal, err := completeCompositionReview(ctx, req)
					switch kind {
					case "task-revision":
						proposal.TaskRevision++
					case "goal-revision":
						proposal.GoalRevision++
					case "run-id":
						proposal.RunID = "another-run"
					}
					return proposal, err
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			p := testProfile()
			p.Modules.Review = ref
			e, err := openDefault(context.Background(), Options{Directory: t.TempDir(), Profile: p, Modules: registry, Models: models(nil)})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			s := e.backend.(*standalone)
			task := compositionTask(t, s, p, kind != "no-exit")
			if err = s.tasksWorker.Review(context.Background(), task); !errors.Is(err, work.ErrStale) {
				t.Fatal("review crossed authority boundary", err)
			}
			after, err := s.work.Get(context.Background(), task.Scope, task.ID)
			if err != nil || after.State == work.Completed || after.Revision != task.Revision {
				t.Fatal(after, err)
			}
		})
	}
}

func TestFrozenReviewAndOwnerUseOriginalRegisteredModuleAfterReopen(t *testing.T) {
	ctx := context.Background()
	registry := NewModuleRegistry()
	v1, v2 := ModuleRef{Name: "alternate", Version: "1"}, ModuleRef{Name: "alternate", Version: "2"}
	reviews := map[string]int{}
	ticks := map[string]*int{}
	ready := false
	for _, ref := range []ModuleRef{v1, v2} {
		if err := registry.RegisterReview(ref, func(_ context.Context, d ReviewDependencies) (Reviewer, error) {
			return ReviewFunc(func(ctx context.Context, r ReviewRequest) (work.Review, error) {
				reviews[d.Profile.Modules.Review.Version]++
				return completeCompositionReview(ctx, r)
			}), nil
		}); err != nil {
			t.Fatal(err)
		}
		ticks[ref.Version] = new(int)
		if err := registry.RegisterOwner(ref, func(_ context.Context, d OwnerDependencies) (OwnerRuntime, error) {
			return &compositionOwner{d: d, ticks: ticks[d.Profile.Modules.Owner.Version], ready: &ready}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	p.Modules.Review = v1
	p.Modules.Owner = v1
	opts := Options{Directory: t.TempDir(), Profile: p, Modules: registry, Models: models(nil)}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	task := compositionTask(t, s, p, true)
	if _, err = e.Submit(ctx, testInput("frozen-owner-source", "retain this original source")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ownerOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	p.Revision = "v2"
	p.Modules.Review = v2
	p.Modules.Owner = v2
	opts.Profile = p
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s = e.backend.(*standalone)
	if _, err = s.tasksWorker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if reviews["1"] != 1 || reviews["2"] != 0 {
		t.Fatal("old task selected current reviewer", reviews)
	}
	ready = true
	if _, err = s.ownerOnce(ctx); err != nil {
		t.Fatal(err)
	}
	key, _ := p.Scope("dm").Key()
	doc, err := s.db.Get(ctx, "composition-owner-1", key, "frozen-owner-source")
	if err != nil || string(doc.Body) != "true" {
		t.Fatal("old owner module queue was not resumed", string(doc.Body), err, *ticks["1"], *ticks["2"])
	}
	after, err := s.work.Get(ctx, task.Scope, task.ID)
	if err != nil || after.State != work.Completed {
		t.Fatal(after, err)
	}
}

func TestFrozenReviewRejectsMissingHistoricalModuleWithoutFallback(t *testing.T) {
	ctx := context.Background()
	v1, v2 := ModuleRef{Name: "independent-review", Version: "1"}, ModuleRef{Name: "independent-review", Version: "2"}
	registry := NewModuleRegistry()
	if err := registry.RegisterReview(v1, func(context.Context, ReviewDependencies) (Reviewer, error) {
		return ReviewFunc(completeCompositionReview), nil
	}); err != nil {
		t.Fatal(err)
	}
	p := testProfile()
	p.Modules.Review = v1
	opts := Options{Directory: t.TempDir(), Profile: p, Modules: registry, Models: models(nil)}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	task := compositionTask(t, e.backend.(*standalone), p, true)
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	fallbackCalls := 0
	opts.Modules = NewModuleRegistry()
	if err = opts.Modules.RegisterReview(v2, func(context.Context, ReviewDependencies) (Reviewer, error) {
		fallbackCalls++
		return ReviewFunc(completeCompositionReview), nil
	}); err != nil {
		t.Fatal(err)
	}
	opts.Profile.Revision = "v2"
	opts.Profile.Modules.Review = v2
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	if _, err = s.tasksWorker.Step(ctx); err == nil || !strings.Contains(err.Error(), "review module independent-review@1 is not registered") {
		t.Fatal("historical module was silently replaced", err)
	}
	after, err := s.work.Get(ctx, task.Scope, task.ID)
	if err != nil || !after.NeedsReview || after.State == work.Completed || fallbackCalls != 0 {
		t.Fatal(after, err, fallbackCalls)
	}
}

func TestUnknownModuleAndCoreRebindingFailBeforeFactorySideEffects(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"storage", "context", "executor", "review", "scheduler", "owner"} {
		t.Run(kind, func(t *testing.T) {
			p := testProfile()
			unknown := ModuleRef{Name: "absent", Version: "1"}
			switch kind {
			case "storage":
				p.Modules.Storage = unknown
			case "context":
				p.Modules.Context = unknown
			case "executor":
				p.Modules.Executor = unknown
			case "review":
				p.Modules.Review = unknown
			case "scheduler":
				p.Modules.Scheduler = unknown
			case "owner":
				p.Modules.Owner = unknown
			}
			called := false
			p.Backend = "trusted-host"
			e, err := openDefault(ctx, Options{Directory: filepath.Join(t.TempDir(), "not-yet-created"), Profile: p, Backends: map[string]func(context.Context, Options) (Backend, error){"trusted-host": func(context.Context, Options) (Backend, error) {
				called = true
				return nil, errors.New("should not run")
			}}})
			if err == nil {
				e.Close()
				t.Fatal("unknown module admitted")
			}
			if called || !strings.Contains(err.Error(), kind+" module") {
				t.Fatal(called, err)
			}
		})
	}
	registry := NewModuleRegistry()
	calls := 0
	ref := ModuleRef{Name: "new-context", Version: "1"}
	if err := registry.RegisterContext(ref, func(context.Context, ContextDependencies) (ContextRuntime, error) {
		calls++
		return nil, errors.New("must not construct changed core")
	}); err != nil {
		t.Fatal(err)
	}
	opts := Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Modules: registry}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Profile.Revision = "v2"
	opts.Profile.Modules.Context = ref
	if e, err = openDefault(ctx, opts); err == nil {
		e.Close()
		t.Fatal("core silently rebound")
	}
	if calls != 0 || !strings.Contains(err.Error(), "migration required") {
		t.Fatal(calls, err)
	}
}

type compositionBackend struct {
	Backend
	closes *int
}

func (b *compositionBackend) Close() error { *b.closes++; return nil }

type compositionDeclaredBackend struct {
	*compositionBackend
	caps []Capability
}

func (b *compositionDeclaredBackend) Capabilities() []Capability { return b.caps }

func TestBackendCapabilitiesRejectMissingOrFalseClaimsAndUnsupportedDiagnostics(t *testing.T) {
	for _, kind := range []string{"required-missing", "false-claim", "unknown-claim", "unsupported-diagnostics"} {
		t.Run(kind, func(t *testing.T) {
			closes := 0
			base := &compositionBackend{closes: &closes}
			var b Backend = base
			p := testProfile()
			p.Backend = "trusted-host"
			opts := Options{Directory: t.TempDir(), Profile: p, Backends: map[string]func(context.Context, Options) (Backend, error){"trusted-host": func(context.Context, Options) (Backend, error) { return b, nil }}}
			switch kind {
			case "required-missing":
				opts.RequiredCapabilities = []Capability{CapabilityDiagnostics}
			case "false-claim":
				b = &compositionDeclaredBackend{base, []Capability{CapabilityDiagnostics}}
			case "unknown-claim":
				b = &compositionDeclaredBackend{base, []Capability{"fictional"}}
			}
			e, err := openDefault(context.Background(), opts)
			if kind == "unsupported-diagnostics" {
				if err != nil {
					t.Fatal(err)
				}
				failures, err := e.Failures(context.Background(), "dm")
				if !errors.Is(err, ErrUnsupported) || failures != nil {
					t.Fatal("unsupported looked like no failures", failures, err)
				}
				e.Close()
			} else if err == nil {
				e.Close()
				t.Fatal("backend capability mismatch admitted")
			}
			if closes != 1 {
				t.Fatal("backend resources leaked or closed twice", closes)
			}
		})
	}
}

type compositionExecutor struct {
	execution.Port
	closed func()
}

func (e *compositionExecutor) Close() error { e.closed(); return nil }

type compositionState struct {
	storage.State
	closed func()
}

func (s *compositionState) Close() error { s.closed(); return s.State.Close() }

type compositionArtifacts struct {
	storage.Artifacts
	closed func()
}

func (a *compositionArtifacts) Close() error { a.closed(); return a.Artifacts.Close() }

func TestModuleClosersUnwindInReverseOnCloseAndOpenFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "failed-open"}[fail], func(t *testing.T) {
			registry := NewModuleRegistry()
			ref := ModuleRef{Name: "lifecycle", Version: "1"}
			closed := []string{}
			closeFn := func(name string) func() { return func() { closed = append(closed, name) } }
			registry.RegisterStorage(ref, func(ctx context.Context, opts StorageDependencies) (storage.State, storage.Artifacts, error) {
				db, err := sqlite.Open(ctx, filepath.Join(opts.Directory, "custom-state"))
				if err != nil {
					return nil, nil, err
				}
				md, err := markdown.Open(filepath.Join(opts.Directory, "custom-files"))
				if err != nil {
					db.Close()
					return nil, nil, err
				}
				return &compositionState{db, closeFn("state")}, &compositionArtifacts{md, closeFn("artifacts")}, nil
			})
			registry.RegisterContext(ref, func(_ context.Context, d ContextDependencies) (ContextRuntime, error) {
				builder, err := contextbuilder.New(d.State, d.Artifacts, d.Tasks, func(ctx context.Context, s work.Scope, _ contextbuilder.View) error { return d.Authorize(ctx, s) })
				return &compositionContext{ContextRuntime: builder, builds: new(int), closed: closeFn("context")}, err
			})
			registry.RegisterExecutor(ref, func(_ context.Context, d ExecutorDependencies) (execution.Port, error) {
				return &compositionExecutor{&execution.Local{DB: d.State, Owner: d.Owner, Authorize: d.Authorize, Run: d.Run}, closeFn("executor")}, nil
			})
			registry.RegisterScheduler(ref, func(_ context.Context, d SchedulerDependencies) (SchedulerRuntime, error) {
				scheduler, err := work.NewScheduler(d.State, d.Work, d.Authorize, func(context.Context, work.Scope, work.Source) error { return nil })
				return &compositionScheduler{SchedulerRuntime: scheduler, ticks: new(int), schedules: new(int), closed: closeFn("scheduler")}, err
			})
			registry.RegisterOwner(ref, func(_ context.Context, d OwnerDependencies) (OwnerRuntime, error) {
				owner := &compositionOwner{d: d, ticks: new(int), closed: closeFn("owner")}
				if fail {
					return owner, errors.New("incomplete owner initialization")
				}
				return owner, nil
			})
			p := testProfile()
			p.Modules = ModuleSelection{Storage: ref, Context: ref, Executor: ref, Scheduler: ref, Owner: ref}
			e, err := openDefault(context.Background(), Options{Directory: t.TempDir(), Profile: p, Models: models(nil), Modules: registry})
			if fail {
				if err == nil {
					e.Close()
					t.Fatal("failed module initialization admitted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Close(); err != nil {
					t.Fatal(err)
				}
				e.Close()
			}
			if want := []string{"owner", "scheduler", "executor", "context", "artifacts", "state"}; !reflect.DeepEqual(closed, want) {
				t.Fatal("resource unwind order", closed, want)
			}
		})
	}
}
