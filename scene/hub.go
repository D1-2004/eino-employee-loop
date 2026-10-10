// Package scene drives an employee from events: every event is admitted,
// routed to its scene, and processed by the scene's foreground session or
// one of its queued background sessions.
//
//	event ──► Catalog.Route ──► EventLog.Admit ──► host lane: Cancel + apply facts
//	                                           └─► SessionQueue.Enqueue (coalesce, preempt)
//	                                           ──► EventLog.MarkApplied
//	worker ──► SessionQueue.Claim (foreground first) ──► Runner (kernel wake)
//	       ──► Complete + deliveries (one fenced commit) ──► Outbox ──► channel adapter
//
// A scene has exactly one interactive (foreground) session, serialized and
// always served before the scene's background work. Background sessions
// (observation, task follow-up, routine, lifecycle) never take the
// interactive turn; preemptible background windows are cancelled when a
// person addresses the employee, and revoked when policy changes.
package scene

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Runner executes one claimed window and returns its outcome. The default
// implementation is KernelRunner.
type Runner interface {
	RunWindow(ctx context.Context, win contract.Window) (contract.Outcome, error)
}

// HostApplier applies host-lane events (facts, configuration revisions). It
// must be idempotent per receipt: when the admission did not finish (an
// error, a crash), a republished duplicate runs it again.
type HostApplier func(ctx context.Context, adm contract.Admission, receipt contract.EventReceipt) error

// Hub wires the ports of one employee. All fields except OnHostEvent and
// Now are required.
type Hub struct {
	Employee string
	Catalog  *contract.Catalog
	Log      contract.EventLog
	Queue    contract.SessionQueue
	Outbox   contract.Outbox
	Runner   Runner
	// OnHostEvent handles host-lane events; nil records them only.
	OnHostEvent HostApplier
	// RetryDelay is the backoff after a retryable failure.
	RetryDelay time.Duration
	// MaxAttempts holds a window after this many failed claims.
	MaxAttempts int
	Now         func() time.Time
}

// Admitted reports what happened to one event.
type Admitted struct {
	Receipt   contract.EventReceipt
	Duplicate bool
	Window    *contract.Window
	Cancelled int
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Admit routes and persists one event, applies its effect and marks the
// receipt applied. Replaying an applied event returns the original receipt
// and does nothing. Replaying an event whose effect did not finish (an
// error or a crash after the receipt committed) re-applies the effect; every
// step is idempotent per receipt, so nothing is enqueued, cancelled or
// applied twice.
//
// An event addressed to another employee is contract.ErrUnknownEmployee; an
// empty address means this employee and is filled in before the event is
// fingerprinted, so both forms are the same event.
func (h *Hub) Admit(ctx context.Context, e contract.Event) (Admitted, error) {
	switch e.Employee {
	case "":
		e.Employee = h.Employee
	case h.Employee:
	default:
		return Admitted{}, fmt.Errorf("%w: %s is not served by the loop of %s", contract.ErrUnknownEmployee, e.Employee, h.Employee)
	}
	adm, err := h.Catalog.Route(e)
	if err != nil {
		return Admitted{}, err
	}
	receipt, created, err := h.Log.Admit(ctx, h.Employee, adm, e.Fingerprint())
	if err != nil {
		return Admitted{}, err
	}
	out := Admitted{Receipt: receipt, Duplicate: !created}
	if !created && receipt.Applied {
		return out, nil
	}
	if adm.Spec.Lane == contract.LaneHost {
		if len(adm.Cancel) > 0 {
			if out.Cancelled, err = h.Queue.Cancel(ctx, h.Employee, receipt.ID, adm.Cancel); err != nil {
				return Admitted{}, err
			}
		}
		if h.OnHostEvent != nil {
			if err := h.OnHostEvent(ctx, adm, receipt); err != nil {
				return Admitted{}, err
			}
		}
	} else {
		win, err := h.Queue.Enqueue(ctx, h.Employee, adm, receipt.ID, h.now())
		if err != nil {
			return Admitted{}, err
		}
		out.Window = &win
	}
	if err := h.Log.MarkApplied(ctx, h.Employee, receipt.ID); err != nil {
		return Admitted{}, err
	}
	out.Receipt.Applied = true
	return out, nil
}

// ErrIdle is returned by Step when nothing is claimable.
var ErrIdle = errors.New("scene: no claimable window")

// HoldError makes Step hold a window with an explicit reason instead of
// retrying it.
type HoldError struct{ Reason string }

func (e HoldError) Error() string { return "scene: hold: " + e.Reason }

// Step claims and runs one window. It returns ErrIdle when there is no work.
func (h *Hub) Step(ctx context.Context, req contract.ClaimRequest) (*contract.Window, error) {
	if req.Now.IsZero() {
		req.Now = h.now()
	}
	win, err := h.Queue.Claim(ctx, req)
	if err != nil {
		return nil, err
	}
	if win == nil {
		return nil, ErrIdle
	}
	lease := *win.Lease
	outcome, runErr := h.Runner.RunWindow(ctx, *win)
	if ctx.Err() != nil {
		// Shutdown is not a business failure: the lease expires and another
		// worker replays the journal.
		return win, ctx.Err()
	}
	var hold HoldError
	switch {
	case errors.As(runErr, &hold):
		return win, h.Queue.Hold(ctx, lease, hold.Reason)
	case runErr != nil:
		max := h.MaxAttempts
		if max <= 0 {
			max = 3
		}
		if win.Attempts >= max {
			return win, errors.Join(runErr, h.Queue.Hold(ctx, lease, "attempts_exhausted"))
		}
		delay := h.RetryDelay
		if delay <= 0 {
			delay = time.Second
		}
		return win, errors.Join(runErr, h.Queue.Retry(ctx, lease, runErr.Error(), h.now().Add(delay)))
	}
	// Every public text of a decision is delivered: a reply, a dispatch
	// acknowledgement or a question. Quiet never speaks. The delivery
	// commits with the completion, fenced by the lease: a window cancelled
	// while running (new human input, revoked policy) fails here and its
	// reply is dropped; a failed commit leaves the window running, so its
	// lease expires and the journal replays it.
	var deliveries []contract.Delivery
	if outcome.Decision.Kind != contract.Quiet && strings.TrimSpace(outcome.Decision.Reply) != "" {
		deliveries = append(deliveries, h.delivery(*win, outcome.Decision.Reply))
	}
	if err := h.Queue.Complete(ctx, lease, contract.WindowResult{Outcome: outcome}, deliveries); err != nil {
		switch {
		case errors.Is(err, contract.ErrLease):
			return win, nil
		case errors.Is(err, contract.ErrConflict):
			// Another reply already holds this window's delivery id; a replay
			// would conflict again.
			return win, errors.Join(err, h.Queue.Hold(ctx, lease, HoldDeliveryConflict))
		}
		return win, err
	}
	return win, nil
}

// HoldDeliveryConflict is the hold reason of a window whose completion
// conflicts with an existing delivery of the same id.
const HoldDeliveryConflict = "delivery_conflict"

func (h *Hub) delivery(win contract.Window, text string) contract.Delivery {
	var scene contract.SceneRef
	replyTo, locale := "", ""
	if n := len(win.Items); n > 0 {
		last := win.Items[n-1].Event
		scene, locale = last.Scene, last.Locale
		if win.Lane == contract.LaneForeground {
			replyTo = last.ID
		}
	}
	return contract.Delivery{
		ID: win.ID + "/reply", Employee: h.Employee, Scene: scene, Session: win.Session,
		WindowID: win.ID, ReplyTo: replyTo, Text: text, Locale: locale,
	}
}

// Drain runs Step until no window is claimable or ctx ends.
func (h *Hub) Drain(ctx context.Context, req contract.ClaimRequest) (int, error) {
	n := 0
	for {
		req.Now = time.Time{}
		_, err := h.Step(ctx, req)
		if errors.Is(err, ErrIdle) {
			return n, nil
		}
		n++
		if err != nil && ctx.Err() != nil {
			return n, err
		}
	}
}

// Sender delivers one message through a channel and returns the provider id.
type Sender func(ctx context.Context, d contract.Delivery) (providerID string, err error)

// Deliver sends pending deliveries. A delivery moves to unknown before the
// send, so a crash mid-send is reconciled later and never re-sent. A send
// error wrapping contract.ErrDeliveryRejected marks the delivery failed;
// any other error (transport, timeout, 5xx) leaves it unknown for
// Reconcile.
func (h *Hub) Deliver(ctx context.Context, send Sender, limit int) (int, error) {
	pending, err := h.Outbox.Pending(ctx, h.Employee, limit)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, d := range pending {
		ok, err := h.Outbox.Transition(ctx, h.Employee, d.ID, []contract.DeliveryState{contract.DeliveryPending}, contract.DeliveryUnknown, "")
		if err != nil {
			return sent, err
		}
		if !ok {
			continue
		}
		providerID, sendErr := send(ctx, d)
		if sendErr != nil && !errors.Is(sendErr, contract.ErrDeliveryRejected) {
			return sent, fmt.Errorf("scene: deliver %s: outcome unknown: %w", d.ID, sendErr)
		}
		next := contract.DeliveryDelivered
		if sendErr != nil {
			next, providerID = contract.DeliveryFailed, ""
		}
		if _, err := h.Outbox.Transition(ctx, h.Employee, d.ID, []contract.DeliveryState{contract.DeliveryUnknown}, next, providerID); err != nil {
			return sent, err
		}
		if sendErr != nil {
			return sent, fmt.Errorf("scene: deliver %s: %w", d.ID, sendErr)
		}
		sent++
	}
	return sent, nil
}

// Reconcile settles unknown deliveries by asking the channel; it never
// sends. Only deliveries unknown for at least minAge are queried (zero
// queries all), so an in-flight send is left alone. A delivery the channel
// cannot place stays unknown. It returns the number of deliveries settled.
func (h *Hub) Reconcile(ctx context.Context, q contract.DeliveryReconciler, minAge time.Duration, limit int) (int, error) {
	var before time.Time
	if minAge > 0 {
		before = h.now().Add(-minAge)
	}
	unknown, err := h.Outbox.Unknown(ctx, h.Employee, before, limit)
	if err != nil {
		return 0, err
	}
	settled := 0
	var errs []error
	for _, d := range unknown {
		state, providerID, err := q.DeliveryStatus(ctx, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("scene: reconcile %s: %w", d.ID, err))
			continue
		}
		switch state {
		case contract.DeliveryUnknown:
			continue
		case contract.DeliveryDelivered, contract.DeliveryAccepted:
		case contract.DeliveryFailed:
			providerID = ""
		default:
			errs = append(errs, fmt.Errorf("scene: reconcile %s: unexpected state %q", d.ID, state))
			continue
		}
		ok, err := h.Outbox.Transition(ctx, h.Employee, d.ID, []contract.DeliveryState{contract.DeliveryUnknown}, state, providerID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			settled++
		}
	}
	return settled, errors.Join(errs...)
}
