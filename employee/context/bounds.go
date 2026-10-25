package context

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// ErrBudget means mandatory original input cannot be represented within the
// configured frame budget. Callers must not send the returned diagnostic frame.
var ErrBudget = errors.New("context: mandatory input exceeds frame byte budget")

// Budget bounds the serialized model frame, not provider instructions or later
// tool results. Source records and task summary fields are never clipped.
type Budget struct {
	MaxBytes            int
	MaxSources          int
	MaxTasks            int
	MaxTaskSummaryBytes int
}

func DefaultBudget() Budget {
	return Budget{MaxBytes: 128 * 1024, MaxSources: 20, MaxTasks: 12, MaxTaskSummaryBytes: 1024}
}

// WithBudget overrides positive limits; zero fields retain safe defaults.
func WithBudget(limits Budget) Option {
	return func(b *Builder) {
		if limits.MaxBytes > 0 {
			b.budget.MaxBytes = limits.MaxBytes
		}
		if limits.MaxSources > 0 {
			b.budget.MaxSources = limits.MaxSources
		}
		if limits.MaxTasks > 0 {
			b.budget.MaxTasks = limits.MaxTasks
		}
		if limits.MaxTaskSummaryBytes > 0 {
			b.budget.MaxTaskSummaryBytes = limits.MaxTaskSummaryBytes
		}
	}
}

func (b *Builder) fits(f Frame) bool {
	raw, err := json.Marshal(f)
	return err == nil && len(raw) <= b.budget.MaxBytes
}

// Four fixed omission diagnostics fit within this reserved space.
const omissionReserveBytes = 512

func (b *Builder) bound(f Frame) (Frame, error) {
	sources, refs := f.Sources, f.Refs
	owners, projections := f.OwnerFiles, f.OwnerProjections
	tasks, err := work.ModelTasks(f.Tasks)
	if err != nil {
		return f, err
	}
	f.Sources = nil
	f.Refs = nil
	f.OwnerFiles = nil
	f.OwnerProjections = nil
	f.OwnerEvidence = nil
	if f.Current != nil {
		for _, ref := range refs {
			if ref.ID == f.Current.ID {
				f.Refs = append(f.Refs, ref)
				break
			}
		}
	}
	// Execution constraints and their original evidence are mandatory. Avoid
	// duplicating evidence inside Task.Sources as well as Frame.Sources.
	if f.View == Execution {
		f.Tasks = append([]work.Task(nil), tasks...)
		for i := range f.Tasks {
			f.Tasks[i].Sources = nil
		}
		for _, source := range sources {
			if f.Current == nil || source.ID != f.Current.ID {
				f.Sources = append(f.Sources, source)
			}
		}
		for _, ref := range refs {
			if f.Current == nil || ref.ID != f.Current.ID {
				f.Refs = append(f.Refs, ref)
			}
		}
		if !b.fits(f) {
			f.Gaps = append(f.Gaps, "mandatory execution task or original sources exceed frame budget; no instruction was shortened")
			return f, ErrBudget
		}
		return f, nil
	}
	f.Tasks = nil
	if !b.fits(f) {
		f.Gaps = append(f.Gaps, "current original source exceeds frame budget; no instruction was shortened")
		return f, ErrBudget
	}
	// Reserve room for bounded omission diagnostics before admitting optional data.
	optionalFits := func(candidate Frame) bool {
		raw, err := json.Marshal(candidate)
		return err == nil && len(raw)+omissionReserveBytes <= b.budget.MaxBytes
	}
	if f.View == Front {
		sort.SliceStable(tasks, func(i, j int) bool {
			if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
				return tasks[i].ID < tasks[j].ID
			}
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		})
		omittedTasks, omittedFields := false, false
		for _, task := range tasks {
			if len(f.Tasks) >= b.budget.MaxTasks {
				omittedTasks = true
				continue
			}
			summary := work.Task{ID: task.ID, Scope: task.Scope, Revision: task.Revision, State: task.State, Goal: task.Goal, NeedsReview: task.NeedsReview, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
			if len(summary.Goal.Summary) > b.budget.MaxTaskSummaryBytes {
				summary.Goal.Summary = ""
				omittedFields = true
			}
			if len(task.Definition.Goal) <= b.budget.MaxTaskSummaryBytes {
				summary.Definition.Goal = task.Definition.Goal
			} else {
				omittedFields = true
			}
			if run := task.CurrentRun(); run != nil {
				summary.Runs = []work.Run{{ID: run.ID, ExecutionID: run.ExecutionID, GoalRevision: run.GoalRevision, State: run.State, ExitProven: run.ExitProven}}
			}
			for _, wait := range task.Waits {
				if wait.Resolved {
					continue
				}
				if len(summary.Waits) == 4 {
					omittedFields = true
					break
				}
				wait.Answer = ""
				if len(wait.Prompt) > b.budget.MaxTaskSummaryBytes {
					wait.Prompt = ""
					omittedFields = true
				}
				if len(wait.Condition) > b.budget.MaxTaskSummaryBytes {
					wait.Condition = ""
					omittedFields = true
				}
				summary.Waits = append(summary.Waits, wait)
			}
			candidate := f
			candidate.Tasks = append(append([]work.Task(nil), f.Tasks...), summary)
			if !optionalFits(candidate) {
				omittedTasks = true
				continue
			}
			f = candidate
		}
		if omittedTasks {
			f.Gaps = append(f.Gaps, "task summaries omitted by context budget; find_tasks/read_task expose authorized full records")
		}
		if omittedFields {
			f.Gaps = append(f.Gaps, "oversized task summary fields omitted whole; read_task retains original constraints")
		}
	}
	omittedHistory := false
	// The input is chronological; prefer newest whole sources, then restore order.
	for i := len(sources) - 1; i >= 0; i-- {
		source := sources[i]
		if f.Current != nil && source.ID == f.Current.ID {
			continue
		}
		if len(f.Sources) >= b.budget.MaxSources {
			omittedHistory = true
			continue
		}
		candidate := f
		candidate.Sources = append(append([]Source(nil), f.Sources...), source)
		for _, ref := range refs {
			if ref.ID == source.ID {
				candidate.Refs = append(append([]Ref(nil), f.Refs...), ref)
				break
			}
		}
		if !optionalFits(candidate) {
			omittedHistory = true
			continue
		}
		f = candidate
	}
	sort.SliceStable(f.Sources, func(i, j int) bool {
		if f.Sources[i].At.Equal(f.Sources[j].At) {
			return f.Sources[i].ID < f.Sources[j].ID
		}
		return f.Sources[i].At.Before(f.Sources[j].At)
	})
	if omittedHistory {
		f.Gaps = append(f.Gaps, "history omitted as whole records by context budget")
	}
	omittedOwners := false
	for _, owner := range owners {
		candidate := f
		candidate.OwnerFiles = append(append([]storage.File(nil), f.OwnerFiles...), owner)
		candidate.OwnerProjections = append([]OwnerProjection(nil), f.OwnerProjections...)
		candidate.OwnerEvidence = append([]ScopedRef(nil), f.OwnerEvidence...)
		for _, p := range projections {
			if p.Path == owner.Path && p.SHA256 == owner.SHA256 {
				candidate.OwnerProjections = append(candidate.OwnerProjections, p)
				candidate.OwnerEvidence = append(candidate.OwnerEvidence, p.Evidence...)
			}
		}
		if !optionalFits(candidate) {
			omittedOwners = true
			continue
		}
		f = candidate
	}
	if omittedOwners {
		f.Gaps = append(f.Gaps, "owner projections omitted as whole records by context budget")
	}
	if !b.fits(f) {
		return f, ErrBudget
	}
	return f, nil
}
