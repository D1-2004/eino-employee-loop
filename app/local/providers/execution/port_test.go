package execution_test

import (
	"context"
	"errors"
	"testing"

	localexec "github.com/D1-2004/eino-employee-loop/app/local/providers/execution"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func TestLocalNeverSubmittedFenceSurvivesReopenAndBlocksLateSubmission(t *testing.T) {
	l, request, dir := fixture(t)
	ctx := context.Background()
	target := execution.TargetFor(request.Task)
	proof, err := l.Cancel(ctx, target)
	if err != nil || !proof.NeverSubmitted || !proof.Result.Exited || proof.ID == "" {
		t.Fatal(proof, err)
	}
	if err = execution.Validate(proof, target); err != nil {
		t.Fatal(err)
	}
	if err = l.DB.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l = &localexec.Local{DB: db, Owner: "next-leader", Authorize: l.Authorize, Run: func(context.Context, execution.Record) (execution.Result, error) {
		t.Fatal("cancelled key invoked kernel")
		return execution.Result{}, nil
	}}
	late, err := l.Submit(ctx, request)
	if err != nil || late.ID != proof.ID || !late.NeverSubmitted {
		t.Fatal(late, err)
	}
	polled, err := l.Poll(ctx, target.Scope, target.SubmitKey)
	if err != nil || polled.ID != proof.ID || !polled.NeverSubmitted {
		t.Fatal(polled, err)
	}
	replayed, err := l.Cancel(ctx, target)
	if err != nil || replayed.ID != proof.ID {
		t.Fatal(replayed, err)
	}
	foreign := target
	foreign.RunID = "another-run"
	if _, err = l.Cancel(ctx, foreign); !errors.Is(err, work.ErrScope) {
		t.Fatal("foreign run consumed proof", err)
	}
}

func TestLocalCancellationDoesNotConvertKnownButMissingExecutionToExit(t *testing.T) {
	l, request, _ := fixture(t)
	target := execution.TargetFor(request.Task)
	target.ExecutionID = "provider-previously-accepted"
	if _, err := l.Cancel(context.Background(), target); !errors.Is(err, execution.ErrUnknown) {
		t.Fatal(err)
	}
	if _, err := l.Lookup(context.Background(), target.Scope, target.SubmitKey); !errors.Is(err, execution.ErrNotFound) {
		t.Fatal("invented exit tombstone for acknowledged execution", err)
	}
}

func TestLocalPollUsesConstructionTimeKernelAndCancelRequiresNoActiveInvocation(t *testing.T) {
	l, request, _ := fixture(t)
	ctx := context.Background()
	r, err := l.Submit(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	l.Run = func(context.Context, execution.Record) (execution.Result, error) {
		close(started)
		<-release
		return execution.Result{Exited: true, Output: "actual exit"}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := l.Poll(ctx, request.Task.Scope, r.SubmitKey); done <- err }()
	<-started
	if _, err := l.Cancel(ctx, execution.TargetFor(request.Task)); !errors.Is(err, execution.ErrBusy) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	exit, err := l.Cancel(ctx, execution.TargetFor(request.Task))
	if err != nil || exit.Result.Output != "actual exit" || exit.NeverSubmitted {
		t.Fatal(exit, err)
	}
}

func TestLocalInterruptDoesNotClaimDurableExit(t *testing.T) {
	local, request, _ := fixture(t)
	ctx := context.Background()
	accepted, err := local.Submit(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var interrupted string
	local.InterruptRun = func(runID string) { interrupted = runID }
	var interrupter execution.Interrupter = local
	interrupter.Interrupt(request.Task.CurrentRun().ID)
	if interrupted != request.Task.CurrentRun().ID {
		t.Fatal("interrupt lost Run identity", interrupted)
	}
	actual, err := local.Lookup(ctx, request.Task.Scope, request.Task.CurrentRun().SubmitKey)
	if err != nil || actual.State != accepted.State || actual.Result.Exited || actual.NeverSubmitted {
		t.Fatal("process cancellation request fabricated durable exit proof", actual, err)
	}
}
