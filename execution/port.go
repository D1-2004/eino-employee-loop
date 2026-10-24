package execution

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/work"
)

// Target binds provider facts to exactly one business Run. An empty ExecutionID
// means submission has not yet been acknowledged, not that it never happened.
type Target struct {
	Scope                                 work.Scope
	TaskID, RunID, SubmitKey, ExecutionID string
}

func TargetFor(t work.Task) Target {
	v := Target{Scope: t.Scope, TaskID: t.ID}
	if r := t.CurrentRun(); r != nil {
		v.RunID, v.SubmitKey, v.ExecutionID = r.ID, r.SubmitKey, r.ExecutionID
	}
	return v
}

// Validate rejects foreign, stale, malformed and contradictory provider facts.
// It does not turn a remote absence or cancellation request into terminal proof.
func Validate(r Record, target Target) error {
	if _, err := target.Scope.Key(); err != nil {
		return err
	}
	if target.TaskID == "" || target.RunID == "" || target.SubmitKey == "" || r.ID == "" ||
		r.TaskID != target.TaskID || r.RunID != target.RunID || r.SubmitKey != target.SubmitKey ||
		r.Request.Task.Scope != target.Scope || r.Request.Task.ID != target.TaskID {
		return work.ErrScope
	}
	run := r.Request.Task.CurrentRun()
	if run == nil || run.ID != target.RunID || run.SubmitKey != target.SubmitKey ||
		target.ExecutionID != "" && (r.ID != target.ExecutionID || r.NeverSubmitted) {
		return work.ErrStale
	}
	switch r.State {
	case "prepared", "running", "recovery":
		if r.Result.Exited || len(r.Result.Interrupts) != 0 || r.NeverSubmitted {
			return work.ErrState
		}
	case "interrupted":
		if r.Result.Exited || len(r.Result.Interrupts) == 0 || r.NeverSubmitted {
			return work.ErrState
		}
	case "exited":
		if !r.Result.Exited || len(r.Result.Interrupts) != 0 {
			return work.ErrState
		}
	default:
		return work.ErrState
	}
	if !r.NeverSubmitted && r.Request.Frame.Scope != target.Scope {
		return work.ErrScope
	}
	return nil
}

func ValidateForTask(r Record, t work.Task) error {
	if err := Validate(r, TargetFor(t)); err != nil {
		return err
	}
	if !r.NeverSubmitted && !bytes.Equal(r.Request.Configuration, t.Definition.Configuration) {
		return errors.New("execution: provider changed frozen configuration")
	}
	// The shared artifact store can contain other Tasks and Owner memory. A
	// provider fact must not turn those files into this Task's Review evidence.
	prefix := "tasks/" + contextbuilder.OwnerKey(t.Scope) + "/" + t.ID + "/"
	for _, artifact := range r.Result.Artifacts {
		digest, err := hex.DecodeString(artifact.SHA256)
		if !strings.HasPrefix(artifact.Path, prefix) || len(artifact.Path) <= len(prefix) ||
			path.Clean(artifact.Path) != artifact.Path || strings.Contains(artifact.Path, "\\") ||
			err != nil || len(digest) != 32 || artifact.Bytes < 0 {
			return errors.New("execution: invalid or foreign task artifact")
		}
	}
	return nil
}

// ValidateRequest checks the immutable input as well as the physical identity.
// A key-fencing tombstone deliberately contains no fabricated input snapshot.
func ValidateRequest(r Record, request Request) error {
	if err := Validate(r, TargetFor(request.Task)); err != nil {
		return err
	}
	if r.NeverSubmitted {
		return nil
	}
	a, err := json.Marshal(r.Request)
	if err != nil {
		return err
	}
	b, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("execution: provider changed frozen request")
	}
	return nil
}

func (l *Local) Poll(ctx context.Context, scope work.Scope, key string) (Record, error) {
	return l.Advance(ctx, scope, key, l.Run)
}

// Cancel fences a never-submitted key durably before returning its proof. For an
// existing invocation, the leadership lease plus inactive-in-process check proves
// that no local kernel remains. This proof is specific to Local, not remote absence.
func (l *Local) Cancel(ctx context.Context, target Target) (Record, error) {
	if l.DB == nil || l.Owner == "" || l.Authorize == nil {
		return Record{}, errors.New("execution: scoped local binding required")
	}
	if err := l.Authorize(ctx, target.Scope); err != nil {
		return Record{}, err
	}
	key, err := target.Scope.Key()
	if err != nil {
		return Record{}, err
	}
	if target.TaskID == "" || target.RunID == "" || target.SubmitKey == "" {
		return Record{}, work.ErrScope
	}
	id := key + "/" + target.SubmitKey
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
	var r Record
	err = l.DB.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "executions", key, target.SubmitKey)
		if errors.Is(e, storage.ErrNotFound) {
			if target.ExecutionID != "" {
				return ErrUnknown
			}
			r = Record{ID: "cancel-" + contextbuilder.Digest([]byte(id))[:24], TaskID: target.TaskID, RunID: target.RunID, SubmitKey: target.SubmitKey, State: "exited", Owner: l.Owner, NeverSubmitted: true, Result: Result{Exited: true, Failure: "local submission fenced before start"}, Request: Request{Task: work.Task{ID: target.TaskID, Scope: target.Scope, Runs: []work.Run{{ID: target.RunID, SubmitKey: target.SubmitKey}}}}}
			d = storage.Document{Namespace: "executions", Scope: key, ID: target.SubmitKey}
		} else if e != nil {
			return e
		} else {
			if e = json.Unmarshal(d.Body, &r); e != nil {
				return e
			}
			if e = Validate(r, target); e != nil {
				return e
			}
			if r.State == "exited" {
				return nil
			}
			r.State, r.Owner = "exited", l.Owner
			r.Result = Result{Exited: true, Failure: "local invocation stopped"}
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
