package entry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
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
func (s *standalone) reviewHeld(ctx context.Context, task work.Task) (bool, error) {
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
func (s *standalone) holdReview(ctx context.Context, task work.Task, cause error) error {
	key, _ := task.Scope.Key()
	id := reviewHoldID(task)
	return s.db.Transact(ctx, func(tx storage.Tx) error {
		d, err := tx.Get(ctx, "review-holds", key, id)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		d.Body = encode(reviewHold{TaskID: task.ID, TaskRevision: task.Revision, State: "held", Error: cause.Error(), At: time.Now().UTC()})
		_, err = tx.Put(ctx, d)
		return err
	})
}

// Retry explicitly releases one current business Review hold, or requeues one
// failed durable Chat job. It preserves frozen inputs and physical model budgets.
// id is a Task ID or durable admission/job ID, not an ambiguous source message ID.
func (s *standalone) Retry(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("entry: retry requires Task or admission ID")
	}
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	// Chat jobs are scoped to this immutable profile identity. Never reinterpret a
	// source message ID across scenes as authorization to retry multiple jobs.
	doc, err := s.db.Get(ctx, "entry-jobs", s.opts.Profile.Key(), id)
	if err == nil {
		var j job
		if err = json.Unmarshal(doc.Body, &j); err != nil {
			return err
		}
		if err = s.authorize(ctx, j.Profile.Scope(j.Input.Scene)); err != nil {
			return err
		}
		if j.State != "failed" {
			return work.ErrState
		}
		j.State = "queued"
		j.Error = ""
		doc.Body = encode(j)
		_, err = s.db.Put(ctx, doc)
		return err
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		d, e := s.db.Get(ctx, "owner-jobs", contextbuilder.OwnerKey(scope), id)
		if e == nil {
			var j struct {
				Scope work.Scope
				State string
			}
			if e = json.Unmarshal(d.Body, &j); e != nil {
				return e
			}
			if j.Scope != scope {
				continue
			}
			if j.State != "failed" {
				return work.ErrState
			}
			var raw map[string]json.RawMessage
			if e = json.Unmarshal(d.Body, &raw); e != nil {
				return e
			}
			raw["State"] = encode("queued")
			raw["Error"] = encode("")
			d.Body = encode(raw)
			_, e = s.db.Put(ctx, d)
			return e
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
	}
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		if err = s.authorize(ctx, scope); err != nil {
			return err
		}
		task, e := s.work.Get(ctx, scope, id)
		if errors.Is(e, storage.ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		if !task.NeedsReview {
			return work.ErrState
		}
		key, _ := scope.Key()
		return s.db.Transact(ctx, func(tx storage.Tx) error {
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
	}
	return storage.ErrNotFound
}

type pendingDelivery struct {
	scope  work.Scope
	record interaction.Record
}

// advanceOutbox tries ready intents across all scenes before reconciling unknown
// intents. An uncertain transport fact stays durable without pinning unrelated work.
func (s *standalone) advanceOutbox(ctx context.Context) (bool, error) {
	ready, unknown := []pendingDelivery{}, []pendingDelivery{}
	var otherErr, unknownErr error
	for _, scene := range s.opts.Profile.Scenes {
		scope := s.opts.Profile.Scope(scene)
		records, err := s.delivery.List(ctx, scope)
		if err != nil {
			return false, err
		}
		for _, r := range records {
			switch r.State {
			case "cancelled", "failed":
				continue
			case "applied":
				if err = s.recordDelivery(ctx, scope, r); err != nil {
					otherErr = errors.Join(otherErr, err)
				}
			case "unknown", "submitting", "provider_accepted":
				unknown = append(unknown, pendingDelivery{scope, r})
			default:
				ready = append(ready, pendingDelivery{scope, r})
			}
		}
	}
	progress := false
	for _, item := range append(ready, unknown...) {
		if err := ctx.Err(); err != nil {
			return progress, errors.Join(otherErr, err)
		}
		r, err := s.delivery.Deliver(ctx, item.scope, item.record.Intent.ID)
		if err != nil {
			if errors.Is(err, interaction.ErrPending) || r.State == "provider_accepted" {
				unknownErr = errors.Join(unknownErr, interaction.ErrPending)
			} else if errors.Is(err, interaction.ErrUnknown) || r.State == "unknown" || r.State == "submitting" {
				unknownErr = errors.Join(unknownErr, interaction.ErrUnknown, err)
			} else {
				otherErr = errors.Join(otherErr, err)
			}
			continue
		}
		progress = true
		if err = s.recordDelivery(ctx, item.scope, r); err != nil {
			otherErr = errors.Join(otherErr, err)
		}
	}
	if otherErr != nil {
		return progress, otherErr
	}
	if progress {
		return true, nil
	}
	return false, unknownErr
}

// Failure is a scoped recovery handle; it contains no model input or secret.
type Failure struct{ Kind, ID, State, Error string }

func (s *standalone) Failures(ctx context.Context, scope work.Scope) ([]Failure, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	out := []Failure{}
	for _, group := range []struct{ namespace, key, kind string }{{"entry-jobs", s.opts.Profile.Key(), "chat"}, {"owner-jobs", contextbuilder.OwnerKey(scope), "owner"}} {
		docs, err := s.db.List(ctx, group.namespace, group.key)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			var j struct {
				Scope        work.Scope
				Input        Input
				State, Error string
			}
			if err = json.Unmarshal(d.Body, &j); err != nil {
				return nil, err
			}
			if group.kind == "chat" {
				if j.Input.Scene != scope.Scene {
					continue
				}
			} else if j.Scope != scope {
				continue
			}
			if j.State == "failed" {
				out = append(out, Failure{Kind: group.kind, ID: d.ID, State: j.State, Error: j.Error})
			}
		}
	}
	key, _ := scope.Key()
	docs, err := s.db.List(ctx, "review-holds", key)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		var h reviewHold
		if err = json.Unmarshal(d.Body, &h); err != nil {
			return nil, err
		}
		if h.State == "held" {
			task, e := s.work.Get(ctx, scope, h.TaskID)
			if e != nil {
				return nil, e
			}
			if task.Revision == h.TaskRevision {
				out = append(out, Failure{Kind: "review", ID: h.TaskID, State: h.State, Error: h.Error})
			}
		}
	}
	records, err := s.delivery.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.State == "failed" {
			out = append(out, Failure{Kind: "delivery", ID: record.Intent.ID, State: record.State, Error: record.LastError})
		}
	}
	return out, nil
}
