package task

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/memory"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type runFunc func(context.Context, kernel.Invocation) (native.Result, error)

func (f runFunc) Run(c context.Context, i kernel.Invocation) (native.Result, error) { return f(c, i) }

func reviewFixture(t *testing.T) (*Worker, work.Task) {
	t.Helper()
	ctx := context.Background()
	db := memory.New()
	files := memory.NewArtifacts()
	t.Cleanup(func() { _ = files.Close(); _ = db.Close() })
	manager, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	profile := contract.Profile{ID: "employee", Revision: "v1", Workspace: "w", Tenant: "t", Employee: "e", Owner: "owner", Scenes: []string{"dm"}, MaxModelCalls: 4, MaxRuns: 4}
	scope := profile.Scope("dm")
	authorize := func(context.Context, work.Scope) error { return nil }
	builder, err := contextbuilder.New(db, files, manager, func(context.Context, work.Scope, contextbuilder.View) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	source := work.Source{Receipt: "source", Actor: "owner", Text: "Write a report"}
	if _, err = builder.Admit(ctx, scope, contextbuilder.Source{ID: source.Receipt, Actor: source.Actor, Text: source.Text}); err != nil {
		t.Fatal(err)
	}
	receipt, err := manager.Dispatch(ctx, scope, source, "dispatch", work.Definition{Goal: "Verified report", Instruction: source.Text, MaxRuns: 4, Configuration: encode(profile)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Get(ctx, scope, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run := task.CurrentRun()
	if _, err = manager.BeginSubmission(ctx, scope, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Submission(ctx, scope, task.ID, run.ID, "exec", false); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.RecordExecution(ctx, scope, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "exec", State: work.RunSucceeded, ExitProven: true, Output: "verified report"}); err != nil {
		t.Fatal(err)
	}
	task, err = manager.Get(ctx, scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{opts: Options{Profile: profile}, db: db, files: files, work: manager, context: builder, authorize: authorize}
	return worker, task
}

// A package move must preserve durable Review failure/retry behavior. This uses
// real transactional state and Context; only the model boundary is replaced.
func TestReviewHoldSurvivesWorkerReassemblyAndScopedRetry(t *testing.T) {
	worker, task := reviewFixture(t)
	ctx := context.Background()
	calls := 0
	worker.runner = runFunc(func(ctx context.Context, call kernel.Invocation) (native.Result, error) {
		calls++
		if calls == 1 {
			return native.Result{}, errors.New("provider unavailable")
		}
		if call.Role != "review" || len(call.Tools) != 1 || call.Tools[0].Spec.Name != "decide_review" || !call.Terminal["decide_review"] {
			t.Fatal("Review was not owned by M2", call.Role)
		}
		if err := call.Check(ctx); err != nil {
			return native.Result{}, err
		}
		_, err := call.Tools[0].Invoke(ctx, "native-review", map[string]any{"kind": "complete", "summary": "verified report"})
		return native.Result{}, err
	})
	if progress, err := worker.Step(ctx); !progress || err == nil {
		t.Fatal(progress, err)
	}
	if progress, err := worker.Step(ctx); progress || err != nil || calls != 1 {
		t.Fatal("hold retried implicitly", progress, err, calls)
	}
	failures, err := worker.Failures(ctx, task.Scope)
	if err != nil || len(failures) != 1 || failures[0].ID != task.ID {
		t.Fatal(failures, err)
	}
	recovered := &Worker{opts: worker.opts, db: worker.db, files: worker.files, work: worker.work, context: worker.context, authorize: worker.authorize, runner: worker.runner}
	foreign := task.Scope
	foreign.Scene = "group"
	if found, err := recovered.Retry(ctx, foreign, task.ID); found || err != nil {
		t.Fatal("foreign scope retried task", found, err)
	}
	if found, err := recovered.Retry(ctx, task.Scope, task.ID); !found || err != nil {
		t.Fatal(found, err)
	}
	if progress, err := recovered.Step(ctx); !progress || err != nil {
		t.Fatal(progress, err)
	}
	actual, err := worker.work.Get(ctx, task.Scope, task.ID)
	if err != nil || actual.State != work.Completed || len(actual.Runs) != 1 {
		t.Fatal(actual, err)
	}
	failures, err = recovered.Failures(ctx, task.Scope)
	if err != nil || len(failures) != 0 {
		t.Fatal(failures, err)
	}
}

func TestCustomReviewerCannotChooseAnotherSnapshotOrIgnoreRevocation(t *testing.T) {
	for _, mode := range []string{"foreign-run", "revoked-source"} {
		t.Run(mode, func(t *testing.T) {
			worker, task := reviewFixture(t)
			ctx := context.Background()
			worker.opts.ReviewFactory = func(context.Context, contract.Profile) (Reviewer, error) {
				return ReviewFunc(func(ctx context.Context, req ReviewRequest) (work.Review, error) {
					proposal := work.Review{TaskRevision: task.Revision, GoalRevision: task.Goal.Revision, RunID: task.CurrentRun().ID, Kind: "complete", Summary: "untrusted proposal"}
					if mode == "foreign-run" {
						proposal.RunID = "other-run"
					} else {
						if err := worker.context.(*contextbuilder.Builder).Revoke(ctx, task.Scope, "source"); err != nil {
							t.Fatal(err)
						}
					}
					return proposal, nil
				}), nil
			}
			if err := worker.Review(ctx, task); err == nil {
				t.Fatal("untrusted reviewer bypassed Host validation")
			}
			actual, err := worker.work.Get(ctx, task.Scope, task.ID)
			if err != nil || actual.State == work.Completed || !actual.NeedsReview {
				t.Fatal(actual, err)
			}
		})
	}
}
