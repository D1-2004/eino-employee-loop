package entry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/execution"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/D1-2004/eino-employee-loop/work"
)

// independentExecutor deliberately neither embeds Local nor uses its database,
// kernel callback, private namespace or execution model. It represents a Host's
// independently managed service with its own idempotency and cancellation facts.
type independentExecutor struct {
	records                         map[string]execution.Record
	submits, polls, cancels, closes int
	submitErr, lookupErr, cancelErr error
	cancelExited                    bool
	change                          func(*execution.Record)
}

func (p *independentExecutor) Submit(_ context.Context, req execution.Request) (execution.Record, error) {
	p.submits++
	if p.records == nil {
		p.records = map[string]execution.Record{}
	}
	run := req.Task.CurrentRun()
	r, ok := p.records[run.SubmitKey]
	if !ok {
		// Keep a detached, frozen provider request as a real remote adapter must.
		var frozen execution.Request
		_ = json.Unmarshal(encode(req), &frozen)
		r = execution.Record{ID: "external/" + run.ID, TaskID: req.Task.ID, RunID: run.ID, SubmitKey: run.SubmitKey, State: "running", Request: frozen}
		p.records[run.SubmitKey] = r
	}
	if p.change != nil {
		p.change(&r)
	}
	return r, p.submitErr
}
func (p *independentExecutor) Lookup(_ context.Context, _ work.Scope, key string) (execution.Record, error) {
	if p.lookupErr != nil {
		return execution.Record{}, p.lookupErr
	}
	r, ok := p.records[key]
	if !ok {
		return execution.Record{}, execution.ErrNotFound
	}
	if p.change != nil {
		p.change(&r)
	}
	return r, nil
}
func (p *independentExecutor) Poll(ctx context.Context, scope work.Scope, key string) (execution.Record, error) {
	p.polls++
	r, err := p.Lookup(ctx, scope, key)
	if err != nil {
		return r, err
	}
	r.State, r.Result = "exited", execution.Result{Exited: true, Output: "Independent provider verified the result"}
	p.records[key] = r
	return r, nil
}
func (p *independentExecutor) Cancel(ctx context.Context, target execution.Target) (execution.Record, error) {
	p.cancels++
	if p.cancelErr != nil {
		return execution.Record{}, p.cancelErr
	}
	r, err := p.Lookup(ctx, target.Scope, target.SubmitKey)
	if err != nil {
		return r, err
	}
	if p.cancelExited {
		r.State, r.Result = "exited", execution.Result{Exited: true, Failure: "provider confirmed cancellation"}
		p.records[target.SubmitKey] = r
	}
	return r, nil
}
func (p *independentExecutor) Close() error { p.closes++; return nil }

func withIndependentExecutor(t *testing.T, p *independentExecutor, ms map[string]*looptest.Model) *Entry {
	t.Helper()
	ref := ModuleRef{Name: "independent-executor", Version: "1"}
	registry := NewModuleRegistry()
	if err := registry.RegisterExecutor(ref, func(context.Context, ExecutorDependencies) (execution.Port, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	profile.Modules.Executor = ref
	e, err := Open(context.Background(), Options{Directory: t.TempDir(), Profile: profile, Models: models(ms), Modules: registry})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestIndependentExecutorCompletesTaskReviewAndDeliveryThroughEntry(t *testing.T) {
	ctx := context.Background()
	p := &independentExecutor{}
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "review": looptest.New(reviewStep(t))}
	e := withIndependentExecutor(t, p, ms)
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("external-source", "Obtain a verified result")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Completed || len(tasks[0].Runs) != 1 || !tasks[0].CurrentRun().ExitProven || tasks[0].CurrentRun().ExecutionID != "external/"+tasks[0].CurrentRun().ID {
		t.Fatal(tasks, err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" || deliveries[0].Receipt == nil {
		t.Fatal(deliveries, err)
	}
	if p.submits != 1 || p.polls != 1 || ms["review"].Calls() != 1 {
		t.Fatal(p)
	}
	if err = e.Close(); err != nil || p.closes != 1 {
		t.Fatal(err, p.closes)
	}
}

func TestUnknownExecutorNotFoundAndUnprovenCancelCannotCreateSuccessor(t *testing.T) {
	ctx := context.Background()
	p := &independentExecutor{submitErr: execution.ErrUnknown, lookupErr: execution.ErrNotFound}
	e := withIndependentExecutor(t, p, map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))})
	defer e.Close()
	s := e.backend.(*standalone)
	if _, err := e.Submit(ctx, testInput("unknown-source", "Work then preserve uncertain submission")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.chatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backgroundOnce(ctx); !errors.Is(err, execution.ErrUnknown) {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.backgroundOnce(ctx); !errors.Is(err, execution.ErrNotFound) {
			t.Fatal(err)
		}
	}
	tasks, _ := e.Tasks(ctx, "dm")
	task := tasks[0]
	if p.submits != 1 || task.CurrentRun().State != work.RunUnknown || task.CurrentRun().ExitProven {
		t.Fatal(p, task)
	}
	if _, err := s.work.Control(ctx, task.Scope, work.Source{Receipt: "steer-unknown", Actor: task.Scope.Requester, Text: "Use a revised requirement"}, "steer", task.ID, "steer", "Revised requirement"); err != nil {
		t.Fatal(err)
	}
	for _, cancelErr := range []error{execution.ErrUnknown, execution.ErrNotFound, nil} {
		p.cancelErr = cancelErr
		if cancelErr == nil {
			p.lookupErr = nil
		}
		_, err := s.backgroundOnce(ctx)
		if cancelErr != nil && !errors.Is(err, cancelErr) || cancelErr == nil && err != nil {
			t.Fatal(err)
		}
		current, _ := e.Tasks(ctx, "dm")
		if len(current[0].Runs) != 1 || current[0].CurrentRun().ExitProven || current[0].Pending == nil || p.submits != 1 {
			t.Fatal("invented exit or second writer", current, p)
		}
	}
	p.cancelExited = true
	if progress, err := s.backgroundOnce(ctx); !progress || err != nil {
		t.Fatal(progress, err)
	}
	current, _ := e.Tasks(ctx, "dm")
	if len(current[0].Runs) != 2 || !current[0].Runs[0].ExitProven || current[0].CurrentRun().State != work.RunQueued || p.submits != 1 {
		t.Fatal(current, p)
	}
}

func TestExecutorForeignOrChangedSubmissionReceiptIsRejected(t *testing.T) {
	for name, change := range map[string]func(*execution.Record){
		"task":          func(r *execution.Record) { r.TaskID = "foreign" },
		"run":           func(r *execution.Record) { r.RunID = "foreign" },
		"key":           func(r *execution.Record) { r.SubmitKey = "foreign" },
		"scope":         func(r *execution.Record) { r.Request.Task.Scope.Scene = "foreign" },
		"configuration": func(r *execution.Record) { r.Request.Configuration = []byte(`{"changed":true}`) },
		"false-exit":    func(r *execution.Record) { r.Result.Exited = true },
		"foreign-artifact": func(r *execution.Record) {
			r.Result.Artifacts = []work.Artifact{{Path: "owner/private.md", SHA256: "0000000000000000000000000000000000000000000000000000000000000000", Bytes: 1}}
		},
		"changed-input": func(r *execution.Record) {
			r.Request.Frame.Gaps = append(r.Request.Frame.Gaps, "provider substituted input")
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			p := &independentExecutor{change: change}
			e := withIndependentExecutor(t, p, map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))})
			defer e.Close()
			s := e.backend.(*standalone)
			if _, err := e.Submit(ctx, testInput("foreign-source", "Do not accept foreign provider facts")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.chatOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.backgroundOnce(ctx); err == nil {
				t.Fatal("foreign receipt accepted")
			}
			tasks, _ := e.Tasks(ctx, "dm")
			if len(tasks[0].Runs) != 1 || tasks[0].CurrentRun().ExecutionID != "" || tasks[0].CurrentRun().ExitProven || tasks[0].CurrentRun().State != work.RunUnknown {
				t.Fatal(tasks)
			}
		})
	}
}

func TestExecutorCannotReplaceAcknowledgedExecutionIdentityDuringPoll(t *testing.T) {
	ctx := context.Background()
	p := &independentExecutor{}
	e := withIndependentExecutor(t, p, map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t))})
	defer e.Close()
	s := e.backend.(*standalone)
	if _, err := e.Submit(ctx, testInput("bound-execution", "Keep the acknowledged physical execution")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.chatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backgroundOnce(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.Tasks(ctx, "dm")
	executionID := tasks[0].CurrentRun().ExecutionID
	p.change = func(r *execution.Record) { r.ID = "foreign-execution" }
	if _, err := s.backgroundOnce(ctx); err == nil {
		t.Fatal("changed execution ID accepted")
	}
	tasks, _ = e.Tasks(ctx, "dm")
	if tasks[0].CurrentRun().ExecutionID != executionID || tasks[0].CurrentRun().ExitProven || tasks[0].NeedsReview || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks)
	}
}

func TestUnknownProviderRecoversByLookupAfterReopenWithFrozenConfiguration(t *testing.T) {
	ctx := context.Background()
	p := &independentExecutor{submitErr: execution.ErrUnknown, lookupErr: execution.ErrNotFound}
	ref := ModuleRef{Name: "persistent-executor", Version: "1"}
	registry := NewModuleRegistry()
	if err := registry.RegisterExecutor(ref, func(context.Context, ExecutorDependencies) (execution.Port, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	profile := testProfile()
	profile.Modules.Executor = ref
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "review": looptest.New(reviewStep(t))}
	opts := Options{Directory: t.TempDir(), Profile: profile, Models: models(ms), Modules: registry}
	e, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(ctx, testInput("recover-external", "Recover exactly the accepted task")); err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if _, err = s.chatOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.backgroundOnce(ctx); !errors.Is(err, execution.ErrUnknown) {
		t.Fatal(err)
	}
	before, _ := e.Tasks(ctx, "dm")
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	p.lookupErr = nil
	opts.Profile.Revision = "v2"
	e, err = Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Tasks(ctx, "dm")
	if len(after) != 1 || after[0].ID != before[0].ID || len(after[0].Runs) != 1 || after[0].CurrentRun().ID != before[0].CurrentRun().ID || after[0].State != work.Completed || p.submits != 1 {
		t.Fatal(after, p)
	}
	for _, record := range p.records {
		var frozen Profile
		if err = json.Unmarshal(record.Request.Configuration, &frozen); err != nil || frozen.Revision != "v1" {
			t.Fatal(frozen, err)
		}
	}
}
