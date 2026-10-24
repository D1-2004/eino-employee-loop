package execution

import (
	"context"
	"encoding/json"
	"errors"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// SubmissionContext provides frozen execution input and current read authority.
type SubmissionContext interface {
	Build(context.Context, contract.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error)
	Revalidate(context.Context, contextbuilder.Frame) error
}

// Bridge owns the durable submission boundary between business Task state and a
// replaceable executor. An uncertain submission is reconciled by Lookup, never
// by issuing a second submission.
type Bridge struct {
	State     storage.State
	Port      Port
	Context   SubmissionContext
	Authorize func(context.Context, contract.Scope) error
	Tasks     *work.Manager
}

func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

// Submit freezes the exact request before external I/O. The unknown
// state is committed before Submit, so a crash cannot cause a new submission
// merely because a remote provider's Lookup currently reports NotFound.
func (b *Bridge) Submit(ctx context.Context, task work.Task) (bool, error) {
	if b.State == nil || b.Port == nil || b.Context == nil || b.Authorize == nil || b.Tasks == nil {
		return false, errors.New("execution: complete submission binding required")
	}
	run := task.CurrentRun()
	if run == nil {
		return false, work.ErrState
	}
	scopeKey, err := task.Scope.Key()
	if err != nil {
		return false, err
	}
	var request Request
	d, err := b.State.Get(ctx, "execution-requests", scopeKey, run.SubmitKey)
	if errors.Is(err, storage.ErrNotFound) {
		frame, e := b.Context.Build(ctx, task.Scope, contextbuilder.Execution, "", task.ID)
		if e != nil {
			return true, e
		}
		request = Request{Task: task, Frame: frame, Configuration: task.Definition.Configuration}
		if _, e = b.State.Put(ctx, storage.Document{Namespace: "execution-requests", Scope: scopeKey, ID: run.SubmitKey, Body: encode(request)}); e != nil {
			return true, e
		}
	} else if err != nil {
		return true, err
	} else if err = json.Unmarshal(d.Body, &request); err != nil {
		return true, err
	}
	if request.Task.ID != task.ID || request.Task.Scope != task.Scope || request.Task.CurrentRun() == nil || request.Task.CurrentRun().ID != run.ID || request.Task.CurrentRun().SubmitKey != run.SubmitKey || string(request.Configuration) != string(task.Definition.Configuration) {
		return true, work.ErrScope
	}
	if err = b.Authorize(ctx, task.Scope); err != nil {
		return true, err
	}
	if err = b.Context.Revalidate(ctx, request.Frame); err != nil {
		return true, err
	}
	if _, err = b.Tasks.Submission(ctx, task.Scope, task.ID, run.ID, "", true); err != nil {
		return true, err
	}
	// Recheck immediately before crossing a replaceable provider boundary. A
	// recovered frozen request is evidence, never continuing read authority.
	if err = b.Authorize(ctx, task.Scope); err != nil {
		return true, err
	}
	if err = b.Context.Revalidate(ctx, request.Frame); err != nil {
		return true, err
	}
	record, err := b.Port.Submit(ctx, request)
	if err != nil {
		return true, err
	}
	if err = b.ValidateRecord(ctx, record, task); err != nil {
		return true, err
	}
	if record.NeverSubmitted {
		return true, ErrUnknown
	}
	_, err = b.Tasks.Submission(ctx, task.Scope, task.ID, run.ID, record.ID, false)
	return true, err
}

func (b *Bridge) ValidateRecord(ctx context.Context, record Record, task work.Task) error {
	if err := ValidateForTask(record, task); err != nil {
		return err
	}
	if record.NeverSubmitted {
		return nil
	}
	key, err := task.Scope.Key()
	if err != nil {
		return err
	}
	d, err := b.State.Get(ctx, "execution-requests", key, task.CurrentRun().SubmitKey)
	// Older Local records predate this submission request journal. Their own persisted
	// request remains the migration baseline, with scope/config/identity checked.
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var frozen Request
	if err = json.Unmarshal(d.Body, &frozen); err != nil {
		return err
	}
	return ValidateRequest(record, frozen)
}
