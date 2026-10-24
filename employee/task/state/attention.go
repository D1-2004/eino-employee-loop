package state

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
)

// Attention is an explicit local inbox item, never an authorization to perform
// the requested action. Task binding is optional, source provenance is not.
type Attention struct {
	ID, TaskID, Kind, Summary, State string
	GoalRevision                     uint64
	Source                           Source
	Due                              time.Time
}

// Observe stores an independently inspectable attention item. Its identity is
// stable across source replay; changing the proposal requires a new identity.
func (s *Scheduler) Observe(ctx context.Context, scope Scope, in Attention) (Attention, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return Attention{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Attention{}, err
	}
	if in.ID == "" || strings.TrimSpace(in.Summary) == "" || in.State != "" && in.State != "open" {
		return Attention{}, ErrState
	}
	switch in.Kind {
	case "request", "question", "commitment", "deadline", "feedback":
	default:
		return Attention{}, ErrState
	}
	in.State = "open"
	if err = validateSource(scope, in.Source, in.ID); err != nil {
		return Attention{}, err
	}
	if err = s.check(ctx, scope, in.Source); err != nil {
		return Attention{}, err
	}
	if in.TaskID != "" {
		t, e := s.manager.Get(ctx, scope, in.TaskID)
		if e != nil {
			return Attention{}, e
		}
		if t.Goal.Revision != in.GoalRevision {
			return Attention{}, ErrStale
		}
	} else if in.GoalRevision != 0 {
		return Attention{}, ErrState
	}
	err = s.db.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "work-attention", key, in.ID)
		if e == nil {
			var prior Attention
			if e = json.Unmarshal(d.Body, &prior); e != nil {
				return e
			}
			compare := prior
			compare.State = "open"
			if stableID("", compare) != stableID("", in) {
				return storage.ErrConflict
			}
			in = prior
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw, _ := json.Marshal(in)
		_, e = tx.Put(ctx, storage.Document{Namespace: "work-attention", Scope: key, ID: in.ID, Body: raw})
		return e
	})
	return in, err
}
func (s *Scheduler) Attention(ctx context.Context, scope Scope) ([]Attention, error) {
	if err := s.authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, err := scope.Key()
	if err != nil {
		return nil, err
	}
	docs, err := s.db.List(ctx, "work-attention", key)
	if err != nil {
		return nil, err
	}
	out := []Attention{}
	for _, d := range docs {
		var a Attention
		if err = json.Unmarshal(d.Body, &a); err != nil {
			return nil, err
		}
		if err = s.check(ctx, scope, a.Source); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ResolveAttention requires a new original owner source. Neither a model view
// nor an expired deadline may silently dismiss the owner's open attention.
func (s *Scheduler) ResolveAttention(ctx context.Context, scope Scope, id, state string, source Source) error {
	if err := s.authorize(ctx, scope); err != nil {
		return err
	}
	key, err := scope.Key()
	if err != nil {
		return err
	}
	if state != "resolved" && state != "dismissed" {
		return ErrState
	}
	if err = validateSource(scope, source, id); err != nil {
		return err
	}
	if err = s.check(ctx, scope, source); err != nil {
		return err
	}
	return s.db.Transact(ctx, func(tx storage.Tx) error {
		op := stableID("attention-", []any{id, source.Receipt})
		payload := []any{id, state, source}
		receipt, e := tx.Get(ctx, "work-attention-receipts", key, op)
		if e == nil {
			if string(receipt.Body) != string(mustJSON(payload)) {
				return storage.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		d, e := tx.Get(ctx, "work-attention", key, id)
		if e != nil {
			return e
		}
		var a Attention
		if e = json.Unmarshal(d.Body, &a); e != nil {
			return e
		}
		if a.State != "open" {
			return ErrState
		}
		a.State = state
		d.Body = mustJSON(a)
		if _, e = tx.Put(ctx, d); e != nil {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "work-attention-receipts", Scope: key, ID: op, Body: mustJSON(payload)})
		return e
	})
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
