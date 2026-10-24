package work

// Scheduler records conditions and periodic occurrences against an existing
// Task. It never dispatches a replacement Task and never declares a Goal done.
import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
)

type Schedule struct {
	ID, TaskID, Condition string
	GoalRevision          uint64
	InitialGoalRevision   uint64
	Source                Source
	Due                   time.Time
	Every                 time.Duration
	Limit                 int
	Disabled              bool
	Emitted               int
	Since                 time.Time
}
type Watch = Schedule
type Signal struct {
	ID, Condition, Payload string
	Source                 Source
	At                     time.Time
}
type Occurrence struct {
	ID, ScheduleID, TaskID string
	GoalRevision           uint64
	Number                 int
	Due                    time.Time
	SignalID, Payload      string
	Acknowledged           bool
}
type Scheduler struct {
	db        *sqlite.Store
	manager   *Manager
	authorize func(context.Context, Scope) error
	check     func(context.Context, Scope, Source) error
}

func NewScheduler(db *sqlite.Store, manager *Manager, authorize func(context.Context, Scope) error, sourceCheck func(context.Context, Scope, Source) error) (*Scheduler, error) {
	if db == nil || manager == nil || authorize == nil || sourceCheck == nil {
		return nil, errors.New("work: scheduler requires stores and source/authorization gates")
	}
	return &Scheduler{db, manager, authorize, sourceCheck}, nil
}
func (s *Scheduler) Schedule(ctx context.Context, scope Scope, in Schedule) (Schedule, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return Schedule{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Schedule{}, err
	}
	if in.ID == "" || in.TaskID == "" || in.GoalRevision == 0 || (in.Condition == "" && in.Due.IsZero()) || (in.Condition != "" && !in.Due.IsZero()) || in.Every < 0 || in.Every > 0 && in.Due.IsZero() || in.Emitted != 0 || in.Disabled {
		return Schedule{}, ErrState
	}
	if in.InitialGoalRevision != 0 && in.InitialGoalRevision != in.GoalRevision {
		return Schedule{}, ErrState
	}
	if !in.Since.IsZero() {
		return Schedule{}, ErrState
	}
	in.InitialGoalRevision = in.GoalRevision
	if err = s.check(ctx, scope, in.Source); err != nil {
		return Schedule{}, err
	}
	if err = validateSource(scope, in.Source, in.ID); err != nil {
		return Schedule{}, err
	}
	if in.Limit == 0 {
		in.Limit = 1
	}
	if in.Limit < 1 || in.Limit > 10000 || in.Every == 0 && in.Limit != 1 {
		return Schedule{}, ErrBudget
	}
	if in.Limit > 1 && int64(in.Every) > math.MaxInt64/int64(in.Limit-1) {
		return Schedule{}, ErrBudget
	}
	err = s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "work-schedules", key, in.ID)
		if e == nil {
			var prior Schedule
			if e = json.Unmarshal(d.Body, &prior); e != nil {
				return e
			}
			compare := prior
			compare.Emitted = 0
			compare.Disabled = false
			compare.GoalRevision = compare.InitialGoalRevision
			compare.Since = time.Time{}
			if stableID("", compare) != stableID("", in) {
				return sqlite.ErrConflict
			}
			in = prior
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		td, e := tx.Get(ctx, "tasks", key, in.TaskID)
		if e != nil {
			return e
		}
		task, e := decode(td, scope)
		if e != nil {
			return e
		}
		if task.Goal.Revision != in.GoalRevision {
			return ErrStale
		}
		in.Since = time.Now().UTC()
		raw, _ := json.Marshal(in)
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "work-schedules", Scope: key, ID: in.ID, Body: raw})
		return e
	})
	return in, err
}
func (s *Scheduler) Signal(ctx context.Context, scope Scope, in Signal) error {
	if err := s.authorize(ctx, scope); err != nil {
		return err
	}
	key, err := scope.Key()
	if err != nil {
		return err
	}
	if in.ID == "" || strings.TrimSpace(in.Condition) == "" || in.Payload == "" {
		return ErrState
	}
	if err = s.check(ctx, scope, in.Source); err != nil {
		return err
	}
	if err = validateSource(scope, in.Source, in.ID); err != nil {
		return err
	}
	return s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "work-signals", key, in.ID)
		if e == nil {
			var old Signal
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			in.At = old.At
			if stableID("", old) != stableID("", in) {
				return sqlite.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		if in.At.IsZero() {
			in.At = time.Now().UTC()
		}
		raw, _ := json.Marshal(in)
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "work-signals", Scope: key, ID: in.ID, Body: raw})
		return e
	})
}
func (s *Scheduler) Tick(ctx context.Context, scope Scope, now time.Time) (bool, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return false, err
	}
	key, err := scope.Key()
	if err != nil {
		return false, err
	}
	if now.IsZero() {
		return false, ErrState
	}
	// Conditions wake the original Wait through the original Task's Review.
	signals, err := s.db.List(ctx, "work-signals", key)
	if err != nil {
		return false, err
	}
	tasks, err := s.manager.List(ctx, scope)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.State != Waiting {
			continue
		}
		for _, w := range task.Waits {
			if w.Resolved {
				continue
			}
			if w.Kind == "timer" && !w.Due.After(now) {
				_, err = s.manager.wake(ctx, scope, task.ID, w.ID, task.Goal.Revision, Signal{ID: stableID("timer-", []any{task.ID, w.ID, w.Due}), Payload: w.Due.UTC().Format(time.RFC3339Nano)}, now)
				return err == nil, err
			}
			if w.Kind == "event" {
				for _, d := range signals {
					var signal Signal
					if err = json.Unmarshal(d.Body, &signal); err != nil {
						return false, err
					}
					if signal.Condition != w.Condition || signal.At.After(now) || !w.Since.IsZero() && signal.At.Before(w.Since) {
						continue
					}
					if err = s.check(ctx, scope, signal.Source); err != nil {
						return false, err
					}
					_, err = s.manager.wake(ctx, scope, task.ID, w.ID, task.Goal.Revision, signal, now)
					return err == nil, err
				}
			}
		}
	}
	docs, err := s.db.List(ctx, "work-schedules", key)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		var schedule Schedule
		if err = json.Unmarshal(d.Body, &schedule); err != nil {
			return false, err
		}
		if err = s.check(ctx, scope, schedule.Source); err != nil {
			return false, err
		}
		t, err := s.manager.Get(ctx, scope, schedule.TaskID)
		if err != nil {
			return false, err
		}
		if t.Goal.Revision != schedule.GoalRevision {
			occurrences, e := s.Occurrences(ctx, scope)
			if e != nil {
				return false, e
			}
			for _, occurrence := range occurrences {
				if occurrence.ScheduleID != schedule.ID || occurrence.Acknowledged {
					continue
				}
				recovered, e := s.RecoverOccurrence(ctx, scope, occurrence.ID)
				if e != nil {
					return false, e
				}
				if recovered {
					return true, nil
				}
			}
			if schedule.Disabled {
				continue
			}
			schedule.Disabled = true
			d.Body, _ = json.Marshal(schedule)
			_, err = s.db.Put(ctx, d)
			return err == nil, err
		}
		if schedule.Disabled || schedule.Emitted >= schedule.Limit {
			continue
		}
		occ := Occurrence{ScheduleID: schedule.ID, TaskID: schedule.TaskID, GoalRevision: schedule.GoalRevision, Number: schedule.Emitted + 1}
		if schedule.Condition != "" {
			found := false
			for _, signalDoc := range signals {
				var signal Signal
				if err = json.Unmarshal(signalDoc.Body, &signal); err != nil {
					return false, err
				}
				if signal.Condition == schedule.Condition && !signal.At.Before(schedule.Since) && !signal.At.After(now) {
					if err = s.check(ctx, scope, signal.Source); err != nil {
						return false, err
					}
					occ.SignalID = signal.ID
					occ.Payload = signal.Payload
					occ.Due = signal.At
					found = true
					break
				}
			}
			if !found {
				continue
			}
		} else {
			occ.Due = schedule.Due.Add(time.Duration(schedule.Emitted) * schedule.Every)
			if occ.Due.After(now) {
				continue
			}
		}
		occ.ID = stableID("occurrence-", []any{scope, schedule.ID, occ.Number})
		schedule.Emitted++
		d.Body, _ = json.Marshal(schedule)
		raw, _ := json.Marshal(occ)
		err = s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
			if _, e := tx.Put(ctx, d); e != nil {
				return e
			}
			if _, e := tx.Put(ctx, sqlite.Document{Namespace: "work-occurrences", Scope: key, ID: occ.ID, Body: raw}); e != nil {
				return e
			}
			_, e := tx.Append(ctx, sqlite.Event{Namespace: "work-occurrence-events", Scope: key, ID: occ.ID, Kind: "occurrence.ready", Body: raw})
			return e
		})
		return err == nil, err
	}
	return false, nil
}
func (s *Scheduler) Occurrences(ctx context.Context, scope Scope) ([]Occurrence, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, err := scope.Key()
	if err != nil {
		return nil, err
	}
	docs, err := s.db.List(ctx, "work-occurrences", key)
	out := []Occurrence{}
	for _, d := range docs {
		var o Occurrence
		if e := json.Unmarshal(d.Body, &o); e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, err
}
func (s *Scheduler) Acknowledge(ctx context.Context, scope Scope, id string) error {
	if err := s.authorize(ctx, scope); err != nil {
		return err
	}
	key, err := scope.Key()
	if err != nil {
		return err
	}
	return s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		d, e := tx.Get(ctx, "work-occurrences", key, id)
		if e != nil {
			return e
		}
		var o Occurrence
		if e = json.Unmarshal(d.Body, &o); e != nil {
			return e
		}
		if o.Acknowledged {
			return nil
		}
		o.Acknowledged = true
		d.Body, _ = json.Marshal(o)
		_, e = tx.Put(ctx, d)
		return e
	})
}

func (s *Scheduler) Schedules(ctx context.Context, scope Scope) ([]Schedule, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, err := scope.Key()
	if err != nil {
		return nil, err
	}
	docs, err := s.db.List(ctx, "work-schedules", key)
	if err != nil {
		return nil, err
	}
	out := []Schedule{}
	for _, d := range docs {
		var schedule Schedule
		if err = json.Unmarshal(d.Body, &schedule); err != nil {
			return nil, err
		}
		out = append(out, schedule)
	}
	return out, nil
}

// AcknowledgeForGoal proves that Control with call=occurrenceID continued this
// exact Task from the scheduled source. It then atomically acknowledges that
// occurrence and rebinds only its own schedule to the actual successor Goal.
// A different caller's Goal change cannot be claimed as this occurrence's work.
func (s *Scheduler) AcknowledgeForGoal(ctx context.Context, scope Scope, id string, expectedOldGoal, newGoalRevision uint64) error {
	if err := s.authorize(ctx, scope); err != nil {
		return err
	}
	key, err := scope.Key()
	if err != nil {
		return err
	}
	if id == "" || expectedOldGoal == 0 || newGoalRevision != expectedOldGoal+1 {
		return ErrStale
	}
	occDoc, err := s.db.Get(ctx, "work-occurrences", key, id)
	if err != nil {
		return err
	}
	var original Occurrence
	if err = json.Unmarshal(occDoc.Body, &original); err != nil {
		return err
	}
	scheduleDoc, err := s.db.Get(ctx, "work-schedules", key, original.ScheduleID)
	if err != nil {
		return err
	}
	var schedule Schedule
	if err = json.Unmarshal(scheduleDoc.Body, &schedule); err != nil {
		return err
	}
	if err = s.check(ctx, scope, schedule.Source); err != nil {
		return err
	}
	return s.db.Transaction(ctx, func(tx *sqlite.Tx) error {
		op := stableID("occurrence-ack-", []any{id, expectedOldGoal, newGoalRevision})
		prior, e := tx.Get(ctx, "work-occurrence-receipts", key, id)
		if e == nil {
			if string(prior.Body) != string(mustJSON(op)) {
				return sqlite.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sqlite.ErrNotFound) {
			return e
		}
		od, e := tx.Get(ctx, "work-occurrences", key, id)
		if e != nil {
			return e
		}
		var occurrence Occurrence
		if e = json.Unmarshal(od.Body, &occurrence); e != nil {
			return e
		}
		if occurrence.Acknowledged || occurrence.GoalRevision != expectedOldGoal || occurrence.ScheduleID != schedule.ID || occurrence.TaskID != schedule.TaskID {
			return ErrStale
		}
		sd, e := tx.Get(ctx, "work-schedules", key, schedule.ID)
		if e != nil {
			return e
		}
		var current Schedule
		if e = json.Unmarshal(sd.Body, &current); e != nil {
			return e
		}
		if current.GoalRevision != expectedOldGoal || current.Source != schedule.Source {
			return ErrStale
		}
		td, e := tx.Get(ctx, "tasks", key, occurrence.TaskID)
		if e != nil {
			return e
		}
		task, e := decode(td, scope)
		if e != nil {
			return e
		}
		if task.Goal.Revision != newGoalRevision || task.CurrentRun() == nil {
			return ErrStale
		}
		controlOp := stableID("control-", []any{scope, current.Source.Receipt, id})
		proof, e := tx.Get(ctx, "work-receipts", key, controlOp)
		if e != nil {
			return e
		}
		var acceptedControl accepted
		if e = json.Unmarshal(proof.Body, &acceptedControl); e != nil {
			return e
		}
		if acceptedControl.Receipt.TaskID != task.ID || acceptedControl.Receipt.GoalRevision != newGoalRevision {
			return ErrStale
		}
		provenRun := false
		for _, run := range task.Runs {
			if run.ID == acceptedControl.Receipt.RunID && run.GoalRevision == newGoalRevision {
				provenRun = true
				break
			}
		}
		if !provenRun {
			return ErrStale
		}
		occurrence.Acknowledged = true
		od.Body = mustJSON(occurrence)
		current.GoalRevision = newGoalRevision
		current.Disabled = false
		sd.Body = mustJSON(current)
		if _, e = tx.Put(ctx, od); e != nil {
			return e
		}
		if _, e = tx.Put(ctx, sd); e != nil {
			return e
		}
		_, e = tx.Put(ctx, sqlite.Document{Namespace: "work-occurrence-receipts", Scope: key, ID: id, Body: mustJSON(op)})
		return e
	})
}

// RecoverOccurrence settles the crash window between an accepted Control and
// its occurrence acknowledgment. Only this occurrence's own durable receipt
// can recover the binding; an unrelated Goal change returns false.
func (s *Scheduler) RecoverOccurrence(ctx context.Context, scope Scope, id string) (bool, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return false, err
	}
	key, err := scope.Key()
	if err != nil {
		return false, err
	}
	d, err := s.db.Get(ctx, "work-occurrences", key, id)
	if err != nil {
		return false, err
	}
	var occurrence Occurrence
	if err = json.Unmarshal(d.Body, &occurrence); err != nil {
		return false, err
	}
	if occurrence.Acknowledged {
		return true, nil
	}
	d, err = s.db.Get(ctx, "work-schedules", key, occurrence.ScheduleID)
	if err != nil {
		return false, err
	}
	var schedule Schedule
	if err = json.Unmarshal(d.Body, &schedule); err != nil {
		return false, err
	}
	if schedule.TaskID != occurrence.TaskID {
		return false, ErrStale
	}
	if err = s.check(ctx, scope, schedule.Source); err != nil {
		return false, err
	}
	controlOp := stableID("control-", []any{scope, schedule.Source.Receipt, occurrence.ID})
	proof, err := s.db.Get(ctx, "work-receipts", key, controlOp)
	if errors.Is(err, sqlite.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var control accepted
	if err = json.Unmarshal(proof.Body, &control); err != nil {
		return false, err
	}
	next := occurrence.GoalRevision + 1
	if control.Receipt.TaskID != occurrence.TaskID || control.Receipt.GoalRevision != next {
		return false, nil
	}
	task, err := s.manager.Get(ctx, scope, occurrence.TaskID)
	if err != nil {
		return false, err
	}
	if task.Goal.Revision != next {
		return false, nil
	}
	if err = s.AcknowledgeForGoal(ctx, scope, id, occurrence.GoalRevision, next); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) wake(ctx context.Context, scope Scope, taskID, waitID string, goalRevision uint64, signal Signal, now time.Time) (Receipt, error) {
	op := stableID("wake-", []any{scope, taskID, waitID, signal.ID})
	return m.apply(ctx, scope, op, []any{taskID, waitID, goalRevision, signal}, taskID, false, func(t *Task) error {
		if t.State != Waiting || t.Goal.Revision != goalRevision {
			return ErrStale
		}
		found := false
		for i := range t.Waits {
			w := &t.Waits[i]
			if w.ID != waitID {
				continue
			}
			if w.Resolved {
				return ErrState
			}
			if w.Kind == "event" {
				if signal.Condition != w.Condition || signal.Source.Receipt == "" {
					return ErrState
				}
				w.SourceReceipt = signal.Source.Receipt
				t.Sources = append(t.Sources, signal.Source)
			} else if w.Kind == "timer" {
				if w.Due.IsZero() || w.Due.After(now) {
					return ErrState
				}
				w.SourceReceipt = "timer/" + signal.ID
			} else {
				return ErrState
			}
			w.Answer = signal.Payload
			w.Resolved = true
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
