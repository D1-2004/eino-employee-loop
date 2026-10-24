// Package execution provides the atomic Run submit/lookup/advance boundary.
// It never creates another business Task or interprets Goal completion.
package execution

import (
	"context"
	"encoding/json"
	"errors"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
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

// TaskReader grants execution read access without permitting business decisions.
type TaskReader interface {
	Get(context.Context, contract.Scope, string) (work.Task, error)
}

// NativeContext resolves original human answers and rechecks frozen evidence.
type NativeContext interface {
	Source(context.Context, contract.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
	Revalidate(context.Context, contextbuilder.Frame) error
}

// Interrupter requests in-process cancellation without asserting provider exit.
// Port.Cancel remains the only boundary that can prove exit or non-submission.
type Interrupter interface{ Interrupt(runID string) }
