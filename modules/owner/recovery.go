package owner

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/D1-2004/eino-employee-loop/contract"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

// Retry requeues one explicitly selected failed observation. It preserves its
// frozen configuration, evidence, model budget and unknown persisted fields.
// The owner key is shared across scenes, so an exact scope check is mandatory.
func (w *Worker) Retry(ctx context.Context, scope work.Scope, id string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.opts.Authorize(ctx, scope); err != nil {
		return false, err
	}
	d, err := w.opts.DB.Get(ctx, "owner-jobs", contextbuilder.OwnerKey(scope), id)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var job struct {
		Scope work.Scope
		State string
	}
	if err = json.Unmarshal(d.Body, &job); err != nil {
		return true, err
	}
	if job.Scope != scope {
		return false, nil
	}
	if job.State != "failed" {
		return true, work.ErrState
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(d.Body, &raw); err != nil {
		return true, err
	}
	raw["State"] = encode("queued")
	raw["Error"] = encode("")
	d.Body = encode(raw)
	_, err = w.opts.DB.Put(ctx, d)
	return true, err
}

// Failures exposes scoped recovery handles without leaking frozen model input.
func (w *Worker) Failures(ctx context.Context, scope work.Scope) ([]contract.Failure, error) {
	if err := w.opts.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	docs, err := w.opts.DB.List(ctx, "owner-jobs", contextbuilder.OwnerKey(scope))
	if err != nil {
		return nil, err
	}
	out := []contract.Failure{}
	for _, d := range docs {
		var job struct {
			Scope        work.Scope
			State, Error string
		}
		if err = json.Unmarshal(d.Body, &job); err != nil {
			return nil, err
		}
		if job.Scope == scope && job.State == "failed" {
			out = append(out, contract.Failure{Kind: "owner", ID: d.ID, State: job.State, Error: job.Error})
		}
	}
	return out, nil
}
