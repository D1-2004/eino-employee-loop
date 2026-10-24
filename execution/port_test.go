package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/execution"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
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
	l = &execution.Local{DB: db, Owner: "next-leader", Authorize: l.Authorize, Run: func(context.Context, execution.Record) (execution.Result, error) {
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
