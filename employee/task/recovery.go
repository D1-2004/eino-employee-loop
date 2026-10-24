package task

import (
	"context"
	"encoding/json"
	"errors"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"time"
)

type reviewHold struct {
	TaskID       string
	TaskRevision uint64
	State, Error string
	At           time.Time
}

func reviewHoldID(task work.Task) string {
	return contextbuilder.Digest(encode([]any{task.ID, task.Revision}))
}
func (s *Worker) reviewHeld(ctx context.Context, task work.Task) (bool, error) {
	if err := s.authorize(ctx, task.Scope); err != nil {
		return false, err
	}
	key, _ := task.Scope.Key()
	d, err := s.db.Get(ctx, "review-holds", key, reviewHoldID(task))
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var hold reviewHold
	if err = json.Unmarshal(d.Body, &hold); err != nil {
		return false, err
	}
	if hold.TaskID != task.ID || hold.TaskRevision != task.Revision {
		return false, work.ErrStale
	}
	return hold.State == "held", nil
}
func (s *Worker) holdReview(ctx context.Context, task work.Task, cause error) error {
	key, _ := task.Scope.Key()
	id := reviewHoldID(task)
	return s.db.Transact(ctx, func(tx storage.Tx) error {
		d, err := tx.Get(ctx, "review-holds", key, id)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		d.Body = encode(reviewHold{TaskID: task.ID, TaskRevision: task.Revision, State: "held", Error: cause.Error(), At: s.now()})
		_, err = tx.Put(ctx, d)
		return err
	})
}

// Retry releases only a current Task Review hold in the explicitly selected
// scope. Chat and Owner retry handles belong to their own modules.
func (s *Worker) Retry(ctx context.Context, scope work.Scope, id string) (bool, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return false, err
	}
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	task, err := s.work.Get(ctx, scope, id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !task.NeedsReview {
		return true, work.ErrState
	}
	key, _ := scope.Key()
	err = s.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "review-holds", key, reviewHoldID(task))
		if e != nil {
			return e
		}
		var hold reviewHold
		if e = json.Unmarshal(d.Body, &hold); e != nil {
			return e
		}
		if hold.State != "held" || hold.TaskID != id || hold.TaskRevision != task.Revision {
			return work.ErrState
		}
		hold.State = "released"
		d.Body = encode(hold)
		_, e = tx.Put(ctx, d)
		return e
	})
	return true, err
}
func (s *Worker) Failures(ctx context.Context, scope work.Scope) ([]contract.Failure, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, _ := scope.Key()
	docs, err := s.db.List(ctx, "review-holds", key)
	if err != nil {
		return nil, err
	}
	out := []contract.Failure{}
	for _, d := range docs {
		var h reviewHold
		if err = json.Unmarshal(d.Body, &h); err != nil {
			return nil, err
		}
		if h.State != "held" {
			continue
		}
		task, e := s.work.Get(ctx, scope, h.TaskID)
		if e != nil {
			return nil, e
		}
		if task.Revision == h.TaskRevision {
			out = append(out, contract.Failure{Kind: "review", ID: h.TaskID, State: h.State, Error: h.Error})
		}
	}
	return out, nil
}
