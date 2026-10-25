package state

import "context"

// RecordNeverSubmitted accepts a trusted provider's durable key-fencing proof.
// The provider must persist a tombstone preventing late Submit before returning
// this proof. An unknown lookup or a transient NotFound is never sufficient.
func (m *Manager) RecordNeverSubmitted(ctx context.Context, scope Scope, taskID, runID, proofID string) (Receipt, error) {
	if proofID == "" {
		return Receipt{}, ErrUnknown
	}
	op := stableID("never-submitted-", []any{scope, taskID, runID, proofID})
	return m.apply(ctx, scope, op, []any{runID, proofID}, taskID, false, func(t *Task) error {
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
				t.Goal.Summary = "run budget exhausted"
				t.NeedsReview = false
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
