package state_test

import (
	"context"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"testing"
)

func TestContinueDuringPauseWaitsForActualExitAndKeepsIntent(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	task := dispatch(t, m, s)
	run := task.CurrentRun()
	if _, err := m.BeginSubmission(ctx, s, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submission(ctx, s, task.ID, run.ID, "real-execution", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Control(ctx, s, source("pause", "pause"), "pause", task.ID, "pause", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Control(ctx, s, source("continue", "continue original task"), "continue", task.ID, "continue", ""); err != nil {
		t.Fatal(err)
	}
	pending, _ := m.Get(ctx, s, task.ID)
	if pending.Pending == nil || pending.Pending.Kind != "continue" || len(pending.Runs) != 1 {
		t.Fatal(pending)
	}
	if _, err := m.RecordExecution(ctx, s, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "real-execution", State: work.RunStopped, ExitProven: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Get(ctx, s, task.ID)
	if after.State != work.Running || len(after.Runs) != 2 || after.Goal.Revision != task.Goal.Revision || after.Pending != nil {
		t.Fatal(after)
	}
}
func TestPauseContinueDoesNotBypassAnUnansweredQuestion(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	task := finish(t, m, s, dispatch(t, m, s))
	p := proposal(task, "wait")
	p.Waits = []work.Wait{{Kind: "human", Prompt: "Which quarter?"}}
	if _, err := m.ApplyReview(ctx, s, task.ID, "wait", p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Control(ctx, s, source("pause", "pause"), "pause", task.ID, "pause", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Control(ctx, s, source("continue", "continue"), "continue", task.ID, "continue", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Get(ctx, s, task.ID)
	if after.State != work.Waiting || after.Waits[0].Resolved || len(after.Runs) != 1 {
		t.Fatal(after)
	}
}
