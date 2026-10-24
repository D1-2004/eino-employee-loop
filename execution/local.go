// Package execution provides the atomic Run submit/lookup/advance boundary.
// It never creates another business Task or interprets Goal completion.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
)

var ErrBusy = errors.New("execution: active local invocation")
var ErrIncomplete = errors.New("execution: kernel returned without exit or interrupt proof")
var ErrUnknown = errors.New("execution: outcome unknown")
var ErrNotFound = storage.ErrNotFound

type Request struct {
	Task          work.Task
	Frame         contextbuilder.Frame
	Configuration json.RawMessage
}
type Result struct {
	Output, Failure string
	Artifacts       []work.Artifact
	Interrupts      []native.Interrupt
	Exited          bool
}
type Record struct {
	ID, SubmitKey, TaskID, RunID, State, Owner string
	NeverSubmitted                             bool
	Request                                    Request
	Result                                     Result
}
type Port interface {
	Submit(context.Context, Request) (Record, error)
	Lookup(context.Context, work.Scope, string) (Record, error)
	Poll(context.Context, work.Scope, string) (Record, error)
	Cancel(context.Context, Target) (Record, error)
	Close() error
}
type Local struct {
	DB        storage.State
	Owner     string
	Authorize func(context.Context, work.Scope) error
	Run       func(context.Context, Record) (Result, error)
	mu        sync.Mutex
	active    map[string]bool
}

func (l *Local) Lookup(ctx context.Context, scope work.Scope, key string) (Record, error) {
	if l.DB == nil || l.Authorize == nil || l.Owner == "" {
		return Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return Record{}, err
	}
	s, err := scope.Key()
	if err != nil {
		return Record{}, err
	}
	d, err := l.DB.Get(ctx, "executions", s, key)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return r, err
	}
	if err := Validate(r, Target{Scope: scope, SubmitKey: key, TaskID: r.TaskID, RunID: r.RunID}); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (l *Local) Submit(ctx context.Context, request Request) (Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return Record{}, errors.New("execution: scoped local binding required")
	}
	scope := request.Task.Scope
	if request.Frame.Scope != scope {
		return Record{}, work.ErrScope
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return Record{}, err
	}
	key, err := scope.Key()
	if err != nil {
		return Record{}, err
	}
	run := request.Task.CurrentRun()
	if run == nil || run.State != work.RunPreparing || run.SubmitKey == "" {
		return Record{}, work.ErrState
	}
	r := Record{ID: "execution-" + contextbuilder.Digest([]byte(run.SubmitKey))[:24], SubmitKey: run.SubmitKey, TaskID: request.Task.ID, RunID: run.ID, State: "prepared", Request: request}
	if err = Validate(r, TargetFor(request.Task)); err != nil {
		return Record{}, err
	}
	err = l.DB.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "executions", key, run.SubmitKey)
		if e == nil {
			var old Record
			if e = json.Unmarshal(d.Body, &old); e != nil {
				return e
			}
			if e = Validate(old, TargetFor(request.Task)); e != nil {
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
func (l *Local) Advance(ctx context.Context, scope work.Scope, key string, kernel func(context.Context, Record) (Result, error)) (Record, error) {
	if l.DB == nil || l.Authorize == nil || l.Owner == "" {
		return Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return Record{}, err
	}
	scopeKey, _ := scope.Key()
	id := scopeKey + "/" + key
	l.mu.Lock()
	if l.active == nil {
		l.active = map[string]bool{}
	}
	if l.active[id] {
		l.mu.Unlock()
		return Record{}, ErrBusy
	}
	l.active[id] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.active, id); l.mu.Unlock() }()
	d, err := l.DB.Get(ctx, "executions", scopeKey, key)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return r, err
	}
	if err = Validate(r, Target{Scope: scope, SubmitKey: key, TaskID: r.TaskID, RunID: r.RunID}); err != nil {
		return Record{}, err
	}
	if r.State == "exited" {
		return r, nil
	}
	if kernel == nil {
		return Record{}, errors.New("execution: local Run function required")
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
		incompleteErr = ErrIncomplete
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
func (l *Local) Stop(ctx context.Context, scope work.Scope, key string) (Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, scope); err != nil {
		return Record{}, err
	}
	scopeKey, err := scope.Key()
	if err != nil {
		return Record{}, err
	}
	id := scopeKey + "/" + key
	l.mu.Lock()
	if l.active == nil {
		l.active = map[string]bool{}
	}
	if l.active[id] {
		l.mu.Unlock()
		return Record{}, ErrBusy
	}
	l.active[id] = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.active, id); l.mu.Unlock() }()
	d, err := l.DB.Get(ctx, "executions", scopeKey, key)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = json.Unmarshal(d.Body, &r); err != nil {
		return Record{}, err
	}
	if r.Request.Task.Scope != scope {
		return Record{}, work.ErrScope
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
