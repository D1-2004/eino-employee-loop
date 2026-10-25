package state_test

import (
	"context"
	"testing"

	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func TestReviewBudgetPauseRetainsEvidenceAndRecoveryInstruction(t *testing.T) {
	m, scope := fixture(t)
	ctx := context.Background()
	receipt, err := m.Dispatch(ctx, scope, source("budget-source", "write report"), "dispatch", work.Definition{Goal: "Report", Instruction: "Write report", MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := m.Get(ctx, scope, receipt.TaskID)
	run := task.CurrentRun()
	if _, err = m.BeginSubmission(ctx, scope, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Submission(ctx, scope, task.ID, run.ID, "exec", false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.RecordExecution(ctx, scope, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "exec", State: work.RunFailed, ExitProven: true, Failure: "real provider failure"}); err != nil {
		t.Fatal(err)
	}
	task, _ = m.Get(ctx, scope, task.ID)
	bad := proposal(task, "complete")
	if err = work.ValidateReview(task, bad); err == nil {
		t.Fatal("failed execution was allowed to complete")
	}
	p := proposal(task, "continue")
	p.NextInstruction = "Retry remaining work under the original constraints"
	if err = work.ValidateReview(task, p); err != nil {
		t.Fatal(err)
	}
	if task.State != work.Running || task.Pending != nil {
		t.Fatal("preview mutated original snapshot", task)
	}
	if _, err = m.ApplyReview(ctx, scope, task.ID, "review", p); err != nil {
		t.Fatal(err)
	}
	task, err = m.Get(ctx, scope, task.ID)
	if err != nil || task.State != work.Paused || task.NeedsReview || task.Pending == nil || task.Pending.Instruction != p.NextInstruction || !task.CurrentRun().ExitProven || task.CurrentRun().Failure != "real provider failure" || len(task.Runs) != 1 || task.Goal.Summary == "" {
		t.Fatal(task, err)
	}
}
