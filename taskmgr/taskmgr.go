// Package taskmgr defines the Task Manager side of the Employee Loop: the
// Executor port that performs background work and the wire values a Task
// Manager publishes back to the loop.
//
// A Task Manager implements contract.TaskManager. It owns every task it
// accepts and reports on it only by publishing execution and completion
// events to the loop's contract.EventSink:
//
//	Dispatch/Steer/Continue ──► execution.accepted (host lane, per run)
//	run starts              ──► execution.started  (host lane)
//	Reporter.Progress       ──► execution.progress (follow-up session)
//	Steer                   ──► execution.steered  (host lane)
//	run done or failed      ──► execution.result   (follow-up session)
//	every run end           ──► execution.terminal (host lane)
//
// Every event carries Employee (the task's employee, so one Task Manager can
// report to many loops through contract.SinkResolver), Refs "task" and
// "run", Source "task-manager", a system actor and an ExecutionPayload. Event ids are "<task>/<run>/<kind>", so a
// republished event is recognised by the loop as a duplicate.
//
// Task Manager errors are the contract sentinels (contract.ErrInvalidRequest,
// contract.ErrUnavailable, contract.ErrForbidden, contract.ErrTaskState,
// contract.ErrNotFound, contract.ErrConflict), so transports and the loop
// classify them without importing this package.
//
// This package imports only contract; implementations live in subpackages
// (memtask) and executors in executors.
package taskmgr

import "context"

// Source is the event source namespace of Task Manager events.
const Source = "task-manager"

// ActorID is the system actor id of Task Manager events.
const ActorID = "task-manager"

// PayloadSchema names the ExecutionPayload carried by every event.
const PayloadSchema = "taskmgr.execution/v1"

// Event types published by a Task Manager (see contract/catalog/events.json).
const (
	EventAccepted = "execution.accepted"
	EventStarted  = "execution.started"
	EventProgress = "execution.progress"
	EventSteered  = "execution.steered"
	EventResult   = "execution.result"
	EventTerminal = "execution.terminal"
)

// Run statuses reported in ExecutionPayload.RunStatus. A run is one attempt
// at a task; steering supersedes the current run with a new one.
const (
	RunAccepted   = "accepted"
	RunRunning    = "running"
	RunDone       = "done"
	RunFailed     = "failed"
	RunCancelled  = "cancelled"
	RunSuperseded = "superseded"
)

// Reasons reported in ExecutionPayload.Reason.
const (
	ReasonCompleted = "completed"
	ReasonFailed    = "failed"
	ReasonStopped   = "stopped"
	ReasonSteered   = "steered"
	ReasonContinued = "continued"
	ReasonShutdown  = "shutdown"
)

// ExecutionPayload is the machine-readable payload of every Task Manager
// event. TaskStatus uses the contract task statuses.
type ExecutionPayload struct {
	TaskStatus string `json:"task_status"`
	RunStatus  string `json:"run_status"`
	Reason     string `json:"reason,omitempty"`
	Revision   int64  `json:"revision"`
	// Seq numbers progress events within a run, starting at 1.
	Seq int `json:"seq,omitempty"`
}

// Job is one run of a task as an Executor sees it.
type Job struct {
	TaskRef  string
	RunRef   string
	Employee string
	Scene    string
	Goal     string
	Prompt   string
	Locale   string
	// Corrections are the requester's steering instructions, oldest first.
	Corrections []string
}

// Reporter publishes progress of the current run. It returns an error once
// the run was superseded, stopped or the manager closed; executors should
// then return.
type Reporter interface {
	Progress(ctx context.Context, text string) error
}

// Executor performs one run. It must return promptly when ctx is cancelled.
// The returned report becomes the task's result text; a non-nil error fails
// the run and its message becomes the failure text.
type Executor interface {
	Execute(ctx context.Context, job Job, r Reporter) (report string, err error)
}
