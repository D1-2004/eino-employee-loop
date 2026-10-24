// Package task owns Task progression, evidence Review and timing decisions.
// State aggregates live in state so read-only Context does not depend on this
// worker or its model policies. Entry only assembles and invokes these services.
package task

import (
	"context"
	"encoding/json"
	"errors"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"sync"
	"time"
)

type Context interface {
	Build(context.Context, work.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error)
	Source(context.Context, work.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error)
	Revalidate(context.Context, contextbuilder.Frame) error
	Admit(context.Context, work.Scope, contextbuilder.Source) (contextbuilder.Source, error)
}

type Scheduler interface {
	Schedule(context.Context, work.Scope, work.Schedule) (work.Schedule, error)
	Signal(context.Context, work.Scope, work.Signal) error
	Tick(context.Context, work.Scope, time.Time) (bool, error)
	Occurrences(context.Context, work.Scope) ([]work.Occurrence, error)
	RecoverOccurrence(context.Context, work.Scope, string) (bool, error)
	Acknowledge(context.Context, work.Scope, string) error
	Schedules(context.Context, work.Scope) ([]work.Schedule, error)
	AcknowledgeForGoal(context.Context, work.Scope, string, uint64, uint64) error
}

type ReviewRequest struct {
	Task    work.Task
	Frame   contextbuilder.Frame
	Profile contract.Profile
	Receipt string
}
type Reviewer interface {
	Propose(context.Context, ReviewRequest) (work.Review, error)
}
type ReviewFunc func(context.Context, ReviewRequest) (work.Review, error)

func (f ReviewFunc) Propose(ctx context.Context, r ReviewRequest) (work.Review, error) {
	return f(ctx, r)
}

type Options struct {
	State         storage.State
	Artifacts     storage.Artifacts
	Profile       contract.Profile
	Tasks         *work.Manager
	Context       Context
	Executor      execution.Port
	Bridge        *execution.Bridge
	Runner        kernel.Agent
	Authorize     func(context.Context, work.Scope) error
	Scheduler     Scheduler
	Now           func() time.Time
	ReviewFactory func(context.Context, contract.Profile) (Reviewer, error)
}
type Worker struct {
	opts           Options
	db             storage.State
	files          storage.Artifacts
	work           *work.Manager
	context        Context
	executor       execution.Port
	bridge         *execution.Bridge
	runner         kernel.Agent
	authorize      func(context.Context, work.Scope) error
	scheduler      Scheduler
	bgMu, timingMu sync.Mutex
}

func New(opts Options) (*Worker, error) {
	if opts.State == nil || opts.Artifacts == nil || opts.Tasks == nil || opts.Context == nil || opts.Executor == nil || opts.Bridge == nil || opts.Runner == nil || opts.Authorize == nil || opts.Scheduler == nil {
		return nil, errors.New("task: state, artifacts, task manager, context, executor, bridge, runner, authorizer and scheduler required")
	}
	return &Worker{opts: opts, db: opts.State, files: opts.Artifacts, work: opts.Tasks, context: opts.Context, executor: opts.Executor, bridge: opts.Bridge, runner: opts.Runner, authorize: opts.Authorize, scheduler: opts.Scheduler}, nil
}
func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func settlement(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
}
func (s *Worker) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now().UTC()
}
