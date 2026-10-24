package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

type Manager struct{ store storage.State }

func New(s storage.State) (*Manager, error) {
	if s == nil {
		return nil, errors.New("work: store required")
	}
	return &Manager{store: s}, nil
}
func (m *Manager) Get(ctx context.Context, scope Scope, id string) (Task, error) {
	key, err := scope.Key()
	if err != nil {
		return Task{}, err
	}
	d, err := m.store.Get(ctx, "tasks", key, id)
	if err != nil {
		return Task{}, err
	}
	return decode(d, scope)
}
func decode(d storage.Document, scope Scope) (Task, error) {
	var t Task
	if err := json.Unmarshal(d.Body, &t); err != nil {
		return t, err
	}
	if t.Scope != scope || t.ID != d.ID || t.Revision != d.Revision {
		return Task{}, ErrScope
	}
	return t, nil
}
func (m *Manager) List(ctx context.Context, scope Scope) ([]Task, error) {
	key, err := scope.Key()
	if err != nil {
		return nil, err
	}
	docs, err := m.store.List(ctx, "tasks", key)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, d := range docs {
		t, err := decode(d, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

type accepted struct {
	Hash    string
	Receipt Receipt
}

func (m *Manager) apply(ctx context.Context, scope Scope, operation string, payload any, id string, create bool, fn func(*Task) error) (Receipt, error) {
	return m.applyChecked(ctx, scope, operation, payload, id, create, func(_ storage.Tx, t *Task) error { return fn(t) })
}
func (m *Manager) applyChecked(ctx context.Context, scope Scope, operation string, payload any, id string, create bool, fn func(storage.Tx, *Task) error) (Receipt, error) {
	key, err := scope.Key()
	if err != nil {
		return Receipt{}, err
	}
	digest := stableID("", payload)
	var result Receipt
	err = m.store.Transact(ctx, func(tx storage.Tx) error {
		prior, e := tx.Get(ctx, "work-receipts", key, operation)
		if e == nil {
			var a accepted
			if e = json.Unmarshal(prior.Body, &a); e != nil {
				return e
			}
			if a.Hash != digest {
				return storage.ErrConflict
			}
			result = a.Receipt
			return nil
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		d, e := tx.Get(ctx, "tasks", key, id)
		var task Task
		if errors.Is(e, storage.ErrNotFound) && create {
			task = Task{ID: id, Scope: scope, CreatedAt: time.Now().UTC()}
		} else if e != nil {
			return e
		} else {
			task, e = decode(d, scope)
			if e != nil {
				return e
			}
		}
		priorGoal, priorState := task.Goal, task.State
		if e = fn(tx, &task); e != nil {
			return e
		}
		task.Revision = d.Revision + 1
		task.UpdatedAt = time.Now().UTC()
		raw, e := json.Marshal(task)
		if e != nil {
			return e
		}
		if _, e = tx.Put(ctx, storage.Document{Namespace: "tasks", Scope: key, ID: id, Revision: d.Revision, Body: raw}); e != nil {
			return e
		}
		result = Receipt{ID: "work/" + operation, TaskID: id, TaskRevision: task.Revision, GoalRevision: task.Goal.Revision, State: task.State}
		if run := task.CurrentRun(); run != nil {
			result.RunID = run.ID
		}
		raw, e = json.Marshal(accepted{Hash: digest, Receipt: result})
		if e != nil {
			return e
		}
		if _, e = tx.Put(ctx, storage.Document{Namespace: "work-receipts", Scope: key, ID: operation, Body: raw}); e != nil {
			return e
		}
		event, _ := json.Marshal(map[string]any{"receipt": result, "task_state": task.State, "goal": task.Goal, "waits": task.Waits, "task": task})
		kind := "task.changed"
		if task.State == Completed && (priorState != Completed || priorGoal.Revision != task.Goal.Revision) {
			kind = "goal.completed"
		}
		_, e = tx.Append(ctx, storage.Event{Namespace: "work-events", Scope: key, ID: operation, Kind: kind, Body: event})
		return e
	})
	return result, err
}
func (m *Manager) Dispatch(ctx context.Context, scope Scope, source Source, call string, d Definition) (Receipt, error) {
	return m.DispatchWithSources(ctx, scope, source, call, d, nil)
}
func (m *Manager) DispatchWithSources(ctx context.Context, scope Scope, source Source, call string, d Definition, extra []Source) (Receipt, error) {
	for _, s := range extra {
		if err := validateSource(scope, s, call); err != nil {
			return Receipt{}, err
		}
	}

	if err := validateSource(scope, source, call); err != nil {
		return Receipt{}, err
	}
	if strings.TrimSpace(d.Goal) == "" || strings.TrimSpace(d.Instruction) == "" {
		return Receipt{}, errors.New("work: complete goal and instruction required")
	}
	if d.MaxRuns == 0 {
		d.MaxRuns = 12
	}
	if d.MaxRuns < 1 || d.MaxRuns > 512 {
		return Receipt{}, ErrBudget
	}
	op := stableID("dispatch-", []any{scope, source.Receipt, call})
	id := stableID("task-", []any{scope, source.Receipt, call})
	return m.apply(ctx, scope, op, []any{source, d, extra}, id, true, func(t *Task) error {
		t.Definition = d
		t.Sources = append(t.Sources, source)
		t.Sources = append(t.Sources, extra...)
		t.Goal = Goal{Revision: 1, State: Ready}
		return nextRun(t, d.Instruction)
	})
}

// Control never starts a second writer before the previous Run has exit proof.
func (m *Manager) Control(ctx context.Context, scope Scope, source Source, call, id, kind, instruction string) (Receipt, error) {
	if err := validateSource(scope, source, call); err != nil {
		return Receipt{}, err
	}
	if kind != "continue" && kind != "steer" && kind != "stop" && kind != "pause" {
		return Receipt{}, ErrState
	}
	op := stableID("control-", []any{scope, source.Receipt, call})
	return m.apply(ctx, scope, op, []any{source, id, kind, instruction}, id, false, func(t *Task) error {
		t.Sources = append(t.Sources, source)
		run := t.CurrentRun()
		if run == nil {
			return ErrState
		}
		if kind == "stop" || kind == "pause" {
			if t.State == Completed || t.State == Failed {
				return nil
			}
			state := Stopped
			if kind == "pause" {
				state = Paused
			}
			t.State = state
			t.Goal.State = state
			t.NeedsReview = false
			t.Pending = nil
			if run.State == RunQueued {
				t.Runs[len(t.Runs)-1].State = RunStopped
				t.Runs[len(t.Runs)-1].ExitProven = true
			} else if !run.ExitProven {
				t.Runs[len(t.Runs)-1].State = RunCancelling
			}
			return nil
		}
		if strings.TrimSpace(instruction) == "" {
			instruction = source.Text
		}
		if t.State == Stopped {
			return ErrState
		}
		if t.State == Waiting {
			// Recovery-only waits can be answered by a current explicit continuation;
			// ordinary missing inputs remain a real Question and need Answer.
			if kind != "continue" {
				return ErrState
			}
			open := 0
			for _, w := range t.Waits {
				if !w.Resolved {
					open++
					if w.Kind != "recovery" {
						return ErrState
					}
				}
			}
			if open != 1 {
				return ErrState
			}
			for i := range t.Waits {
				if !t.Waits[i].Resolved {
					t.Waits[i].Resolved = true
					t.Waits[i].Answer = source.Text
					t.Waits[i].SourceReceipt = source.Receipt
				}
			}
			t.State = Ready
			t.Goal.State = Ready
			t.NeedsReview = true
			return nil
		}
		if kind == "continue" && t.State == Paused {
			for _, w := range t.Waits {
				if !w.Resolved {
					t.State, t.Goal.State = Waiting, Waiting
					return nil
				}
			}
			if !run.ExitProven {
				t.Pending = &Pending{Kind: "continue", Instruction: instruction, Source: source}
				return nil
			}
		}
		if kind == "steer" {
			t.Pending = &Pending{Kind: kind, Instruction: instruction, Source: source}
			if !run.ExitProven && run.State != RunQueued {
				t.Runs[len(t.Runs)-1].State = RunCancelling
				return nil
			}
			if run.State == RunQueued {
				t.Runs[len(t.Runs)-1].State = RunStopped
				t.Runs[len(t.Runs)-1].ExitProven = true
			}
			t.Goal.Revision++
			t.Definition.Instruction = instruction
			t.Pending = nil
			return nextRun(t, instruction)
		}
		if t.NeedsReview {
			return nil
		}
		if !run.ExitProven {
			return nil
		} // An accepted current Run is continued, not duplicated.
		if t.State == Completed || t.State == Failed {
			t.Goal.Revision++
		}
		return nextRun(t, instruction)
	})
}

// BeginSubmission commits intent before external I/O. A concurrent stop then
// knows there may be a resource to look up/cancel and cannot assert never-started.
func (m *Manager) BeginSubmission(ctx context.Context, scope Scope, taskID, runID string) (Receipt, error) {
	op := stableID("submit-intent-", []any{scope, taskID, runID})
	return m.apply(ctx, scope, op, runID, taskID, false, func(t *Task) error {
		run := t.CurrentRun()
		if run == nil || run.ID != runID || run.GoalRevision != t.Goal.Revision {
			return ErrStale
		}
		if t.State != Running || run.State != RunQueued {
			return ErrState
		}
		t.Runs[len(t.Runs)-1].State = RunPreparing
		return nil
	})
}

func (m *Manager) Submission(ctx context.Context, scope Scope, taskID, runID, executionID string, unknown bool) (Receipt, error) {
	kind := "submitted"
	if unknown {
		kind = "unknown"
	}
	op := stableID("execution-", []any{scope, taskID, runID, kind, executionID})
	return m.apply(ctx, scope, op, []any{runID, executionID, unknown}, taskID, false, func(t *Task) error {
		run := t.CurrentRun()
		if run == nil || run.ID != runID || run.GoalRevision != t.Goal.Revision {
			return ErrStale
		}
		if run.State != RunPreparing && run.State != RunUnknown && run.State != RunCancelling {
			return ErrState
		}
		if !unknown && executionID == "" {
			return errors.New("work: execution receipt required")
		}
		if run.ExecutionID != "" && executionID != "" && run.ExecutionID != executionID {
			return ErrStale
		}
		if executionID != "" {
			t.Runs[len(t.Runs)-1].ExecutionID = executionID
		}
		if run.State != RunCancelling {
			t.Runs[len(t.Runs)-1].State = RunActive
			if unknown {
				t.Runs[len(t.Runs)-1].State = RunUnknown
			}
		}
		return nil
	})
}
func (m *Manager) RecordExecution(ctx context.Context, scope Scope, taskID, eventID string, fact ExecutionFact) (Receipt, error) {
	if eventID == "" || fact.ExecutionID == "" {
		return Receipt{}, errors.New("work: real execution identity required")
	}
	op := stableID("execution-fact-", []any{scope, taskID, eventID})
	return m.apply(ctx, scope, op, fact, taskID, false, func(t *Task) error {
		run := t.CurrentRun()
		if run == nil || run.ID != fact.RunID || run.GoalRevision != t.Goal.Revision || run.ExecutionID != "" && run.ExecutionID != fact.ExecutionID {
			return ErrStale
		}
		if fact.State != RunSucceeded && fact.State != RunFailed && fact.State != RunStopped && fact.State != RunActive {
			return ErrState
		}
		if fact.State == RunActive && fact.ExitProven {
			return ErrState
		}
		if run.State == RunQueued {
			return ErrState
		}
		if run.ExitProven {
			previous := ExecutionFact{RunID: run.ID, ExecutionID: run.ExecutionID, State: run.State, Output: run.Output, Failure: run.Failure, ExitProven: run.ExitProven, Artifacts: run.Artifacts}
			if stableID("", previous) != stableID("", fact) {
				return ErrStale
			}
			return nil
		}
		if fact.State != RunActive && !fact.ExitProven {
			return ErrUnknown
		}
		run.ExecutionID = fact.ExecutionID
		run.State = fact.State
		run.Output = fact.Output
		run.Failure = fact.Failure
		run.ExitProven = fact.ExitProven
		run.Artifacts = append([]Artifact(nil), fact.Artifacts...)
		t.Runs[len(t.Runs)-1] = *run
		if t.State == Stopped || t.State == Paused && (t.Pending == nil || t.Pending.Kind != "continue") {
			return nil
		}
		if t.Pending != nil {
			if !run.ExitProven {
				return nil
			}
			// Exit is authoritative even when there is no budget for the
			// requested successor. Never roll back that proof with nextRun.
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
		if run.ExitProven {
			t.NeedsReview = true
		}
		return nil
	})
}
func (m *Manager) ApplyReview(ctx context.Context, scope Scope, taskID, reviewID string, proposal Review) (Receipt, error) {
	if reviewID == "" {
		return Receipt{}, ErrStale
	}
	op := stableID("review-", []any{scope, taskID, reviewID})
	return m.apply(ctx, scope, op, proposal, taskID, false, func(t *Task) error {
		run := t.CurrentRun()
		if t.Revision != proposal.TaskRevision || t.Goal.Revision != proposal.GoalRevision || run == nil || run.ID != proposal.RunID || !t.NeedsReview || !run.ExitProven {
			return ErrStale
		}
		if t.State == Stopped || t.State == Paused {
			return ErrState
		}
		if t.Pending != nil {
			return ErrStale
		}
		switch proposal.Kind {
		case "resume":
			if run.State != RunSuspended {
				return ErrState
			}
			for _, w := range t.Waits {
				if !w.Resolved {
					return ErrState
				}
			}
			run.State = RunActive
			run.ExitProven = false
			t.Runs[len(t.Runs)-1] = *run
			t.State = Running
			t.Goal.State = Running
			t.NeedsReview = false
		case "complete":
			if run.State != RunSucceeded || strings.TrimSpace(proposal.Summary) == "" {
				return ErrState
			}
			t.State = Completed
			t.Goal.State = Completed
			t.Goal.Summary = proposal.Summary
			t.NeedsReview = false
		case "continue":
			if strings.TrimSpace(proposal.NextInstruction) == "" {
				return ErrState
			}
			return nextRun(t, proposal.NextInstruction)
		case "wait":
			if len(proposal.Waits) == 0 {
				return ErrState
			}
			t.Waits = nil
			for i, w := range proposal.Waits {
				if w.Resolved || w.Answer != "" || w.SourceReceipt != "" {
					return ErrState
				}
				switch w.Kind {
				case "human", "recovery":
					if strings.TrimSpace(w.Prompt) == "" {
						return ErrState
					}
				case "event":
					if strings.TrimSpace(w.Condition) == "" {
						return ErrState
					}
				case "timer":
					if w.Due.IsZero() {
						return ErrState
					}
				default:
					return ErrState
				}
				w.ID = fmt.Sprintf("%s-wait-%d-%d-%d", t.ID, t.Goal.Revision, t.Revision, i+1)
				w.Since = time.Now().UTC()
				t.Waits = append(t.Waits, w)
			}
			t.State = Waiting
			t.Goal.State = Waiting
			t.NeedsReview = false
		case "fail":
			if strings.TrimSpace(proposal.Summary) == "" {
				return ErrState
			}
			t.State = Failed
			t.Goal.State = Failed
			t.Goal.Summary = proposal.Summary
			t.NeedsReview = false
		default:
			return ErrState
		}
		return nil
	})
}
func (m *Manager) Answer(ctx context.Context, scope Scope, source Source, call, taskID, waitID string, goalRevision uint64) (Receipt, error) {
	if err := validateSource(scope, source, call); err != nil {
		return Receipt{}, err
	}
	op := stableID("answer-", []any{scope, source.Receipt, call})
	return m.apply(ctx, scope, op, []any{source, taskID, waitID, goalRevision}, taskID, false, func(t *Task) error {
		if t.State != Waiting || t.Goal.Revision != goalRevision {
			return ErrStale
		}
		t.Sources = append(t.Sources, source)
		found := false
		for i := range t.Waits {
			w := &t.Waits[i]
			if w.ID != waitID {
				continue
			}
			if w.Resolved || w.Kind != "human" && w.Kind != "recovery" {
				return ErrState
			}
			w.Resolved = true
			w.Answer = source.Text
			w.SourceReceipt = source.Receipt
			found = true
		}
		if !found {
			return ErrStale
		}
		for _, w := range t.Waits {
			if !w.Resolved {
				return nil
			}
		}
		t.State = Ready
		t.Goal.State = Ready
		t.NeedsReview = true
		return nil
	})
}

// Suspend records a native execution's durable continuation and inactive writer.
// Answers still enter the same business Review before the original Run resumes.
func (m *Manager) Suspend(ctx context.Context, scope Scope, taskID, runID, executionID string, waits []Wait) (Receipt, error) {
	op := stableID("suspend-", []any{scope, runID, waits})
	return m.apply(ctx, scope, op, waits, taskID, false, func(t *Task) error {
		run := t.CurrentRun()
		if run == nil || run.ID != runID || run.ExecutionID != executionID || run.State != RunActive || t.State != Running || len(waits) == 0 {
			return ErrStale
		}
		for _, w := range waits {
			if w.ID == "" || w.NativeTarget == "" || w.ExecutionID != executionID || w.Kind != "human" || w.Resolved || w.Prompt == "" {
				return ErrState
			}
		}
		run.State = RunSuspended
		run.ExitProven = true
		t.Runs[len(t.Runs)-1] = *run
		t.Waits = append([]Wait(nil), waits...)
		for i := range t.Waits {
			t.Waits[i].Since = time.Now().UTC()
		}
		t.State = Waiting
		t.Goal.State = Waiting
		t.NeedsReview = false
		return nil
	})
}
