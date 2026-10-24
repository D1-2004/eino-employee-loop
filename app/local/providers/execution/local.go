// Package execution implements the local atomic Run provider.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	core "github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type Local struct {
	// InterruptRun cancels only an in-process invocation. It is not exit proof.
	InterruptRun func(string)
	DB           storage.State
	Owner        string
	Authorize    func(context.Context, work.Scope) error
	Run          func(context.Context, core.Record) (core.Result, error)
	mu           sync.Mutex
	active       map[string]bool
}

// Interrupt requests process-local cancellation. The caller must still reconcile
// the durable result through Poll or Cancel before recording an exit.
func (l *Local) Interrupt(runID string) {
	if l.InterruptRun != nil {
		l.InterruptRun(runID)
	}
}

func (l *Local) Lookup(ctx context.Context, scope work.Scope, key string) (core.Record, error) {
	if l.DB == nil || l.Authorize == nil || l.Owner == "" {
		return core.Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return core.Record{}, err
	}
	s, err := scope.Key()
	if err != nil {
		return core.Record{}, err
	}
	d, err := l.DB.Get(ctx, "executions", s, key)
	if err != nil {
		return core.Record{}, err
	}
	var r core.Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return r, err
	}
	if err := core.Validate(r, core.Target{Scope: scope, SubmitKey: key, TaskID: r.TaskID, RunID: r.RunID}); err != nil {
		return core.Record{}, err
	}
	return r, nil
}
func (l *Local) Submit(ctx context.Context, request core.Request) (core.Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return core.Record{}, errors.New("execution: scoped local binding required")
	}
	scope := request.Task.Scope
	if request.Frame.Scope != scope {
		return core.Record{}, work.ErrScope
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return core.Record{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return core.Record{}, err
	}
	run := request.Task.CurrentRun()
	if run == nil || run.State != work.RunPreparing || run.SubmitKey == "" {
		return core.Record{}, work.ErrState
	}
	r := core.Record{ID: "execution-" + contextbuilder.Digest([]byte(run.SubmitKey))[:24], SubmitKey: run.SubmitKey, TaskID: request.Task.ID, RunID: run.ID, State: "prepared", Request: request}
	if err = core.Validate(r, core.TargetFor(request.Task)); err != nil {
		return core.Record{}, err
	}
	err = l.DB.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "executions", key, run.SubmitKey)
		if e == nil {
			var old core.Record
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			if e = core.Validate(old, core.TargetFor(request.Task)); e != nil {
				return e
			}
			if old.NeverSubmitted {
				r = old
				return nil
			}
			a, _ := json.Marshal(old.Request)
			b, _ := json.Marshal(request)
			if string(a) != string(b) {
				return storage.ErrConflict
			}
			r = old
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw, _ := json.Marshal(r)
		_, e = tx.Put(ctx, storage.Document{Namespace: "executions", Scope: key, ID: run.SubmitKey, Body: raw})
		return e
	})
	return r, err
}
func (l *Local) Advance(ctx context.Context, scope work.Scope, key string, kernel func(context.Context, core.Record) (core.Result, error)) (core.Record, error) {
	if l.DB == nil || l.Authorize == nil || l.Owner == "" {
		return core.Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return core.Record{}, err
	}
	scopeKey, _ := scope.Key()
	id := scopeKey + "/" + key
	l.mu.Lock()
	if l.active == nil {
		l.active = map[string]bool{}
	}
	if l.active[id] {
		l.mu.Unlock()
		return core.Record{}, core.ErrBusy
	}
	l.active[id] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.active, id); l.mu.Unlock() }()
	d, err := l.DB.Get(ctx, "executions", scopeKey, key)
	if err != nil {
		return core.Record{}, err
	}
	var r core.Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return r, err
	}
	if err = core.Validate(r, core.Target{Scope: scope, SubmitKey: key, TaskID: r.TaskID, RunID: r.RunID}); err != nil {
		return core.Record{}, err
	}
	if r.State == "exited" {
		return r, nil
	}
	if kernel == nil {
		return core.Record{}, errors.New("execution: local Run function required")
	}
	r.State = "running"
	r.Owner = l.Owner
	d.Body, _ = json.Marshal(r)
	rev, err := l.DB.Put(ctx, d)
	if err != nil {
		return r, err
	}
	d.Revision = rev
	result, runErr := kernel(ctx, r)
	var incompleteErr error
	r.Result = result
	r.State = "exited"
	if len(result.Interrupts) > 0 {
		r.State = "interrupted"
		r.Result.Exited = false
	}
	if ctx.Err() != nil {
		r.State = "recovery"
		r.Result.Exited = false
		r.Result.Interrupts = nil
	} else if runErr != nil {
		r.State = "exited"
		r.Result.Exited = true
		r.Result.Interrupts = nil
		r.Result.Failure = runErr.Error()
	} else if len(result.Interrupts) == 0 && !result.Exited {
		r.State = "recovery"
		incompleteErr = core.ErrIncomplete
	}
	d.Body, _ = json.Marshal(r)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, saveErr := l.DB.Put(settle, d)
	if ctx.Err() != nil {
		return r, errors.Join(ctx.Err(), saveErr)
	}
	return r, errors.Join(incompleteErr, saveErr)
}

// Stop records the exit of an inactive local invocation. The caller must hold
// this Entry's process leadership lease, which proves any previous process is
// gone; this proof must never be generalized to a remote executor.
func (l *Local) Stop(ctx context.Context, scope work.Scope, key string) (core.Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return core.Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return core.Record{}, err
	}
	scopeKey, err := scope.Key()
	if err != nil {
		return core.Record{}, err
	}
	id := scopeKey + "/" + key
	l.mu.Lock()
	if l.active == nil {
		l.active = map[string]bool{}
	}
	if l.active[id] {
		l.mu.Unlock()
		return core.Record{}, core.ErrBusy
	}
	l.active[id] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.active, id); l.mu.Unlock() }()
	d, err := l.DB.Get(ctx, "executions", scopeKey, key)
	if err != nil {
		return core.Record{}, err
	}
	var r core.Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return core.Record{}, err
	}
	if r.Request.Task.Scope != scope {
		return core.Record{}, work.ErrScope
	}
	if r.State == "exited" {
		return r, nil
	}
	r.State = "exited"
	r.Owner = l.Owner
	r.Result.Exited = true
	r.Result.Interrupts = nil
	r.Result.Failure = "local invocation stopped"
	d.Body, err = json.Marshal(r)
	if err != nil {
		return r, err
	}
	_, err = l.DB.Put(ctx, d)
	return r, err
}

func (l *Local) Poll(ctx context.Context, scope work.Scope, key string) (core.Record, error) {
	return l.Advance(ctx, scope, key, l.Run)
}

// Cancel fences a never-submitted key durably before returning its proof. For an
// existing invocation, the leadership lease plus inactive-in-process check proves
// that no local kernel remains. This proof is specific to Local, not remote absence.
func (l *Local) Cancel(ctx context.Context, target core.Target) (core.Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return core.Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, target.Scope); err != nil {
		return core.Record{}, err
	}
	key, err := target.Scope.Key()
	if err != nil {
		return core.Record{}, err
	}
	if target.TaskID == "" || target.RunID == "" || target.SubmitKey == "" {
		return core.Record{}, work.ErrScope
	}
	id := key + "/" + target.SubmitKey
	l.mu.Lock()
	if l.active == nil {
		l.active = map[string]bool{}
	}
	if l.active[id] {
		l.mu.Unlock()
		return core.Record{}, core.ErrBusy
	}
	l.active[id] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.active, id); l.mu.Unlock() }()
	var r core.Record
	err = l.DB.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "executions", key, target.SubmitKey)
		if errors.Is(e, storage.ErrNotFound) {
			if target.ExecutionID != "" {
				return core.ErrUnknown
			}
			r = core.Record{ID: "cancel-" + contextbuilder.Digest([]byte(id))[:24], TaskID: target.TaskID, RunID: target.RunID, SubmitKey: target.SubmitKey, State: "exited", Owner: l.Owner, NeverSubmitted: true, Result: core.Result{Exited: true, Failure: "local submission fenced before start"}, Request: core.Request{Task: work.Task{ID: target.TaskID, Scope: target.Scope, Runs: []work.Run{{ID: target.RunID, SubmitKey: target.SubmitKey}}}}}
			d = storage.Document{Namespace: "executions", Scope: key, ID: target.SubmitKey}
		} else if e != nil {
			return e
		} else {
			if e = json.Unmarshal(d.Body, &r); e != nil {
				return e
			}
			if e = core.Validate(r, target); e != nil {
				return e
			}
			if r.State == "exited" {
				return nil
			}
			r.State, r.Owner = "exited", l.Owner
			r.Result = core.Result{Exited: true, Failure: "local invocation stopped"}
		}
		d.Body, e = json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = tx.Put(ctx, d)
		return e
	})
	return r, err
}

// Entry stops and joins its workers before closing the provider. Local has no
// independent worker; the database lifetime belongs to Entry's storage factory.
func (l *Local) Close() error { return nil }
