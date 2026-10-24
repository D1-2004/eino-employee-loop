package work

import (
	"context"
	"errors"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
)

// AbortLocalSubmission is deliberately specific to the SQLite local executor.
// Absence in the same database proves no local kernel was ever started. It is
// not a generic unknown/remote timeout shortcut. The caller holds local leadership.
func (m *Manager) AbortLocalSubmission(ctx context.Context, scope Scope, taskID, runID string) (Receipt, error) {
	op := stableID("local-never-submitted-", []any{scope, taskID, runID})
	return m.applyChecked(ctx, scope, op, runID, taskID, false, func(tx *sqlite.Tx, t *Task) error {
		if t.CurrentRun() == nil {
			return ErrState
		}
		key, _ := scope.Key()
		_, e := tx.Get(ctx, "executions", key, t.CurrentRun().SubmitKey)
		if !errors.Is(e, sqlite.ErrNotFound) {
			if e == nil {
				return ErrState
			}
			return e
		}
		run := t.CurrentRun()
		if run == nil || run.ID != runID || run.ExecutionID != "" || run.State != RunCancelling {
			return ErrState
		}
		run.State = RunStopped
		run.ExitProven = true
		t.Runs[len(t.Runs)-1] = *run
		if t.Pending != nil && t.State != Stopped && (t.State != Paused || t.Pending.Kind == "continue") {
			if len(t.Runs) >= t.Definition.MaxRuns {
				t.State, t.Goal.State = Paused, Paused
				return nil
			}
			instruction := t.Pending.Instruction
			t.Definition.Instruction = instruction
			if t.Pending.Kind == "steer" {
				t.Goal.Revision++
			}
			t.Pending = nil
			return nextRun(t, instruction)
		}
		return nil
	})
}
