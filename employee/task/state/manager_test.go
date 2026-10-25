package state_test

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func fixture(t *testing.T) (*work.Manager, work.Scope) {
	t.Helper()
	s, e := sqlite.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	m, e := work.New(s)
	if e != nil {
		t.Fatal(e)
	}
	return m, work.Scope{Workspace: "local", Tenant: "org", Employee: "employee", Scene: "scene", Requester: "owner"}
}
func source(id, text string) work.Source { return work.Source{Receipt: id, Actor: "owner", Text: text} }
func dispatch(t *testing.T, m *work.Manager, s work.Scope) work.Task {
	t.Helper()
	r, e := m.Dispatch(context.Background(), s, source("message-one", "write report"), "native-one", work.Definition{Goal: "Write a verified local report", Instruction: "Write report and verify the file", MaxRuns: 4})
	if e != nil {
		t.Fatal(e)
	}
	task, e := m.Get(context.Background(), s, r.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func finish(t *testing.T, m *work.Manager, s work.Scope, task work.Task) work.Task {
	t.Helper()
	ctx := context.Background()
	r := task.CurrentRun()
	if _, e := m.BeginSubmission(ctx, s, task.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Submission(ctx, s, task.ID, r.ID, "exec-"+r.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e := m.RecordExecution(ctx, s, task.ID, "terminal-"+r.ID, work.ExecutionFact{RunID: r.ID, ExecutionID: "exec-" + r.ID, State: work.RunSucceeded, ExitProven: true, Output: "verified run output"}); e != nil {
		t.Fatal(e)
	}
	updated, e := m.Get(ctx, s, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	return updated
}
func proposal(task work.Task, kind string) work.Review {
	return work.Review{TaskRevision: task.Revision, GoalRevision: task.Goal.Revision, RunID: task.CurrentRun().ID, Kind: kind, Summary: "verified goal output"}
}
func TestDispatchIsAtomicScopedAndReceiptBound(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	a := dispatch(t, m, s)
	receipt, e := m.Dispatch(ctx, s, source("message-one", "write report"), "native-one", a.Definition)
	if e != nil || receipt.TaskID != a.ID {
		t.Fatal(receipt, e)
	}
	tasks, e := m.List(ctx, s)
	if e != nil || len(tasks) != 1 || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks, e)
	}
	changed := a.Definition
	changed.Goal = "different goal"
	if _, e = m.Dispatch(ctx, s, source("message-one", "write report"), "native-one", changed); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal("same native receipt created different work", e)
	}
	foreign := s
	foreign.Requester = "other"
	if _, e = m.Get(ctx, foreign, a.ID); !errors.Is(e, sqlite.ErrNotFound) {
		t.Fatal("foreign requester read private work", e)
	}
	if _, e = m.Control(ctx, s, work.Source{Receipt: "forged", Actor: "other", Text: "stop"}, "call", a.ID, "stop", ""); !errors.Is(e, work.ErrScope) {
		t.Fatal("text forged authority", e)
	}
}
func TestExitDoesNotCompleteGoalAndAnswerReturnsToOriginalReview(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	a := finish(t, m, s, dispatch(t, m, s))
	if a.State != work.Running || a.Goal.State != work.Running || !a.NeedsReview {
		t.Fatal("Run exit claimed business completion", a)
	}
	p := proposal(a, "wait")
	p.Waits = []work.Wait{{Kind: "human", Prompt: "Which quarter?"}}
	if _, e := m.ApplyReview(ctx, s, a.ID, "review-wait", p); e != nil {
		t.Fatal(e)
	}
	a, e := m.Get(ctx, s, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	wait := a.Waits[0]
	if _, e = m.Control(ctx, s, source("continue", "continue"), "continue", a.ID, "continue", ""); !errors.Is(e, work.ErrState) {
		t.Fatal("ordinary input wait was bypassed", e)
	}
	if _, e = m.Answer(ctx, s, source("answer", "Q3"), "answer-call", a.ID, wait.ID, a.Goal.Revision+1); !errors.Is(e, work.ErrStale) {
		t.Fatal("answer crossed goal revisions", e)
	}
	r, e := m.Answer(ctx, s, source("answer", "Q3"), "answer-call", a.ID, wait.ID, a.Goal.Revision)
	if e != nil {
		t.Fatal(e)
	}
	again, e := m.Answer(ctx, s, source("answer", "Q3"), "answer-call", a.ID, wait.ID, a.Goal.Revision)
	if e != nil || again != r {
		t.Fatal("answer replay changed effects", again, e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if a.State != work.Ready || !a.NeedsReview || len(a.Runs) != 1 || a.Waits[0].Answer != "Q3" {
		t.Fatal("answer did not return to original Review", a)
	}
	p = proposal(a, "continue")
	p.NextInstruction = "Finish report for Q3"
	if _, e = m.ApplyReview(ctx, s, a.ID, "review-continue", p); e != nil {
		t.Fatal(e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Runs) != 2 || a.Goal.Revision != 1 || a.Runs[1].Instruction != "Finish report for Q3" {
		t.Fatal("remaining work created a replacement goal", a)
	}
	a = finish(t, m, s, a)
	if _, e = m.ApplyReview(ctx, s, a.ID, "review-complete", proposal(a, "complete")); e != nil {
		t.Fatal(e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil || a.State != work.Completed || a.Goal.State != work.Completed {
		t.Fatal(a, e)
	}
}
func TestUnknownAndSteeringWaitForExitProof(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	a := dispatch(t, m, s)
	r := a.CurrentRun()
	if _, e := m.BeginSubmission(ctx, s, a.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Submission(ctx, s, a.ID, r.ID, "", true); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Control(ctx, s, source("more", "continue original"), "continue", a.ID, "continue", ""); e != nil {
		t.Fatal(e)
	}
	a, e := m.Get(ctx, s, a.ID)
	if e != nil || len(a.Runs) != 1 || a.CurrentRun().State != work.RunUnknown {
		t.Fatal("unknown submit generated a second Run", a, e)
	}
	if _, e = m.Control(ctx, s, source("steer", "only enterprise edition"), "steer", a.ID, "steer", "Only enterprise edition"); e != nil {
		t.Fatal(e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil || a.Pending == nil || len(a.Runs) != 1 || a.Goal.Revision != 1 {
		t.Fatal("steer raced the original writer", a, e)
	}
	if _, e = m.RecordExecution(ctx, s, a.ID, "no-proof", work.ExecutionFact{RunID: r.ID, ExecutionID: "exec", State: work.RunFailed}); !errors.Is(e, work.ErrUnknown) {
		t.Fatal("accepted unproved exit", e)
	}
	if _, e = m.RecordExecution(ctx, s, a.ID, "exit", work.ExecutionFact{RunID: r.ID, ExecutionID: "exec", State: work.RunStopped, ExitProven: true}); e != nil {
		t.Fatal(e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil || len(a.Runs) != 2 || a.Goal.Revision != 2 || a.Pending != nil || a.CurrentRun().Instruction != "Only enterprise edition" {
		t.Fatal(a, e)
	}
	if _, e = m.RecordExecution(ctx, s, a.ID, "late-old", work.ExecutionFact{RunID: r.ID, ExecutionID: "exec", State: work.RunSucceeded, ExitProven: true}); !errors.Is(e, work.ErrStale) {
		t.Fatal("late old writer changed new goal", e)
	}
}
func TestStopDuringPreparingDoesNotInventExitOrAllowReview(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	a := dispatch(t, m, s)
	r := a.CurrentRun()
	if _, e := m.BeginSubmission(ctx, s, a.ID, r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Control(ctx, s, source("stop", "stop"), "stop", a.ID, "stop", ""); e != nil {
		t.Fatal(e)
	}
	a, e := m.Get(ctx, s, a.ID)
	if e != nil || a.CurrentRun().ExitProven || a.CurrentRun().State != work.RunCancelling {
		t.Fatal("stop invented never-started proof", a, e)
	}
	if _, e = m.Submission(ctx, s, a.ID, r.ID, "exec-late", false); e != nil {
		t.Fatal("lost resource returned after stop", e)
	}
	if _, e = m.RecordExecution(ctx, s, a.ID, "actual-exit", work.ExecutionFact{RunID: r.ID, ExecutionID: "exec-late", State: work.RunStopped, ExitProven: true}); e != nil {
		t.Fatal(e)
	}
	a, e = m.Get(ctx, s, a.ID)
	if e != nil || a.State != work.Stopped || a.NeedsReview {
		t.Fatal(a, e)
	}
	if _, e = m.ApplyReview(ctx, s, a.ID, "stale-complete", proposal(a, "complete")); e == nil {
		t.Fatal("Review completed a stopped task")
	}
}

func TestSteerWithoutSuccessorBudgetDoesNotCancelWriter(t *testing.T) {
	m, s := fixture(t)
	ctx := context.Background()
	receipt, err := m.Dispatch(ctx, s, source("one", "write report"), "dispatch", work.Definition{Goal: "Report", Instruction: "Write report", MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.Get(ctx, s, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run := task.CurrentRun()
	if _, err = m.BeginSubmission(ctx, s, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Submission(ctx, s, task.ID, run.ID, "exec", false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Control(ctx, s, source("steer", "new requirement"), "steer", task.ID, "steer", "new requirement"); !errors.Is(err, work.ErrBudget) {
		t.Fatalf("expected explicit budget rejection: %v", err)
	}
	current, err := m.Get(ctx, s, task.ID)
	if err != nil || current.CurrentRun().State != work.RunActive || current.Pending != nil || current.Definition.Instruction != "Write report" || len(current.Sources) != 1 {
		t.Fatal("rejected steer changed writer, accepted source or original instruction", current, err)
	}
	if _, err = m.RecordExecution(ctx, s, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "exec", State: work.RunStopped, ExitProven: true}); err != nil {
		t.Fatal(err)
	}
	task, err = m.Get(ctx, s, task.ID)
	if err != nil || !task.CurrentRun().ExitProven || len(task.Runs) != 1 || task.State != work.Running || task.Pending != nil || !task.NeedsReview {
		t.Fatal("budget hold discarded exit proof or source intent", task, err)
	}
}
