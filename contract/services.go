package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service boundaries.
//
// The Employee Loop is one of three services that may run in one process or
// apart:
//
//	Event Gateway ──Event──► Employee Loop ──DispatchRequest──► Task Manager
//	      ▲                       │  ▲                               │
//	      └──────Delivery─────────┘  └──────────Event (execution.*)──┘
//
//   - The Event Gateway listens to providers (IM, calendar, approvals, ...),
//     resolves scenes and publishes Events to the loop (EventSink). It also
//     sends the loop's Deliveries through the provider (Deliverer).
//   - The Employee Loop admits events, runs scene sessions and wakes, and
//     owns its event log, session queue, journal, outbox, memory and
//     conversation log.
//   - The Task Manager owns background work. The loop dispatches and
//     controls tasks through TaskManager; the Task Manager reports back only
//     by publishing execution and completion events to the loop. Every
//     request names its employee and every event it publishes carries that
//     Event.Employee, so one Task Manager serves many loops (SinkResolver).
//
// Every value crossing a boundary is a plain JSON-serializable type defined
// in this package; the JSON Schemas in schema/ describe the wire format.

// EventAck is the loop's answer to a published event.
type EventAck struct {
	ReceiptID string `json:"receipt_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
	WindowID  string `json:"window_id,omitempty"`
	Lane      Lane   `json:"lane"`
}

// EventSink is the loop's ingress. Publishing is idempotent per
// (source, event id); reusing an id with different content is ErrConflict.
type EventSink interface {
	Publish(ctx context.Context, e Event) (EventAck, error)
}

// ErrUnknownEmployee marks an event addressed to an employee the receiving
// sink does not serve. It is permanent: a retry cannot fix it.
var ErrUnknownEmployee = errors.New("contract: unknown employee")

// SinkResolver returns the ingress of one employee's loop. As an EventSink
// it routes every event by Event.Employee, so one producer (for example a
// Task Manager) can report to many loops, in one process or behind HTTP.
// An event without an employee is ErrInvalidEvent; a resolver that does
// not know the employee returns ErrUnknownEmployee.
type SinkResolver func(employee string) (EventSink, error)

// Publish implements EventSink by routing on Event.Employee.
func (r SinkResolver) Publish(ctx context.Context, e Event) (EventAck, error) {
	if strings.TrimSpace(e.Employee) == "" {
		return EventAck{}, fmt.Errorf("%w: employee is required to route an event", ErrInvalidEvent)
	}
	sink, err := r(e.Employee)
	if err != nil {
		return EventAck{}, err
	}
	if sink == nil {
		return EventAck{}, fmt.Errorf("%w: %s", ErrUnknownEmployee, e.Employee)
	}
	return sink.Publish(ctx, e)
}

// Deliverer is a channel's egress: it sends one delivery and returns the
// provider message id. It must not retry a send whose outcome is unknown.
//
// An error wrapping ErrDeliveryRejected is a definite rejection: the
// message was not sent and never will be, and the delivery becomes failed.
// Any other error (transport failure, timeout, 5xx) is ambiguous and leaves
// the delivery unknown for reconciliation.
type Deliverer interface {
	Deliver(ctx context.Context, d Delivery) (providerID string, err error)
}

// ErrDeliveryRejected marks a definite delivery rejection (see Deliverer).
var ErrDeliveryRejected = errors.New("contract: delivery rejected")

// DeliveryReconciler is implemented by channels that can look up a
// delivery whose send outcome is unknown. DeliveryStatus never sends. It
// answers DeliveryDelivered or DeliveryAccepted with the provider id when
// the message exists, DeliveryFailed when it definitely was not and will
// not be sent, and DeliveryUnknown when it cannot tell.
type DeliveryReconciler interface {
	DeliveryStatus(ctx context.Context, d Delivery) (state DeliveryState, providerID string, err error)
}

// DispatchRequest asks the Task Manager to create background work. The loop
// derives IdempotencyKey from the window, model turn and native call id, so
// a replayed wake never creates a second task.
type DispatchRequest struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Employee       string   `json:"employee"`
	Tenant         string   `json:"tenant,omitempty"`
	Scene          SceneRef `json:"scene"`
	Requester      Actor    `json:"requester"`
	SourceRef      string   `json:"source_ref"`
	Goal           string   `json:"goal"`
	Prompt         string   `json:"prompt"`
	Locale         string   `json:"locale,omitempty"`
}

// TaskControl targets an existing task on behalf of a requester.
type TaskControl struct {
	IdempotencyKey string `json:"idempotency_key"`
	Employee       string `json:"employee"`
	Scene          string `json:"scene"`
	TaskRef        string `json:"task_ref"`
	Requester      Actor  `json:"requester"`
	SourceRef      string `json:"source_ref"`
	// Text is the correction (steer) or the continuation prompt (continue).
	Text string `json:"text,omitempty"`
	// ExpectedRevision, when positive, is the task revision the requester
	// decided on (the read_ref of continue and stop). A task at another
	// revision rejects the control with ErrStaleRevision.
	ExpectedRevision int64 `json:"expected_revision,omitempty"`
}

// TaskReceipt proves the Task Manager accepted a request.
type TaskReceipt struct {
	TaskRef  string `json:"task_ref"`
	RunRef   string `json:"run_ref,omitempty"`
	Status   string `json:"status"`
	Created  bool   `json:"created"`
	Revision int64  `json:"revision"`
}

// TaskView is the Task Manager's current view of a task. Report is the
// executor's returned content: evidence, not a delivery proof.
type TaskView struct {
	TaskRef   string    `json:"task_ref"`
	Employee  string    `json:"employee"`
	Scene     string    `json:"scene"`
	Requester string    `json:"requester"`
	SourceRef string    `json:"source_ref"`
	Goal      string    `json:"goal"`
	Status    string    `json:"status"`
	Revision  int64     `json:"revision"`
	Report    string    `json:"report,omitempty"`
	Failure   string    `json:"failure,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskQuery lists tasks of one scene, newest first.
type TaskQuery struct {
	Employee  string `json:"employee"`
	Scene     string `json:"scene"`
	Requester string `json:"requester,omitempty"`
	Text      string `json:"text,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// TaskManager is the boundary to the service that owns background work.
// Every mutation is idempotent by IdempotencyKey. Implementations enforce
// that only the requester controls its own task in its own scene.
//
// A control whose idempotency key is already recorded answers with the
// stored receipt before any revision or state precondition is checked: the
// first attempt may have committed and moved the task on, and a replay
// with the same key must not be refused as stale. Revision and state checks
// and the effect happen atomically, so the Task Manager is the only place
// that decides whether a control applies.
type TaskManager interface {
	Dispatch(ctx context.Context, req DispatchRequest) (TaskReceipt, error)
	Get(ctx context.Context, employee, taskRef string) (TaskView, error)
	Find(ctx context.Context, q TaskQuery) ([]TaskView, error)
	Steer(ctx context.Context, c TaskControl) (TaskReceipt, error)
	Stop(ctx context.Context, c TaskControl) (TaskReceipt, error)
	Continue(ctx context.Context, c TaskControl) (TaskReceipt, error)
}

// Task Manager errors.
var (
	// ErrForbidden marks a control request from someone other than the
	// requester, or across scenes.
	ErrForbidden = errors.New("contract: not allowed")
	// ErrTaskState marks a control that the task's current state rejects.
	ErrTaskState = errors.New("contract: task state does not allow this")
	// ErrStaleRevision marks a control whose ExpectedRevision is not the
	// task's current revision. Nothing was applied; the requester reads the
	// task again and decides anew.
	ErrStaleRevision = errors.New("contract: task revision changed")
	// ErrInvalidRequest marks a request the Task Manager rejects as
	// malformed (a missing or out-of-range field). Retrying the same request
	// cannot succeed; the loop returns it to the model as a refusal.
	ErrInvalidRequest = errors.New("contract: invalid request")
	// ErrUnavailable marks a Task Manager that cannot take requests now
	// (closed, shutting down, overloaded). The same request may succeed
	// later; the loop retries the window.
	ErrUnavailable = errors.New("contract: unavailable")
)
