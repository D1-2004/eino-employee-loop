package scene

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Worker keeps a Hub running: step loops claim and run windows, foreground
// lanes first, and a delivery loop sends the outbox through a Deliverer and,
// when the Deliverer is also a contract.DeliveryReconciler, settles unknown
// deliveries by status query (never by resending).
// It holds no state of its own, so several workers (in one process or many)
// may serve the same Hub ports.
type Worker struct {
	Hub *Hub
	// ID names the worker in leases. Default "worker".
	ID string
	// Deliverer sends deliveries; nil leaves them pending in the outbox.
	Deliverer contract.Deliverer
	// Concurrency is the number of step loops. Default 1.
	Concurrency int
	// LeaseFor is the lease of a claimed window. Zero uses the queue default.
	LeaseFor time.Duration
	// Idle is the wait when nothing is claimable or deliverable. Default 50ms.
	Idle time.Duration
	// DeliverBatch bounds deliveries sent or reconciled per pass. Default 16.
	DeliverBatch int
	// ReconcileAfter is how long a delivery stays unknown before the
	// Deliverer is asked about it. Default 30s.
	ReconcileAfter time.Duration
	// ReconcileEvery is the interval between reconcile passes. Default 30s.
	ReconcileEvery time.Duration
	// OnError receives step and delivery errors (machine errors, not text).
	OnError func(error)
}

func (w *Worker) id() string {
	if w.ID == "" {
		return "worker"
	}
	return w.ID
}

func (w *Worker) idle() time.Duration {
	if w.Idle <= 0 {
		return 50 * time.Millisecond
	}
	return w.Idle
}

func (w *Worker) report(err error) {
	if err != nil && w.OnError != nil {
		w.OnError(err)
	}
}

// StepOnce claims and runs one window, trying foreground lanes before any
// other. It returns ErrIdle when nothing is claimable.
func (w *Worker) StepOnce(ctx context.Context) (*contract.Window, error) {
	req := contract.ClaimRequest{Worker: w.id(), LeaseFor: w.LeaseFor, Lanes: []contract.Lane{contract.LaneForeground}}
	win, err := w.Hub.Step(ctx, req)
	if !errors.Is(err, ErrIdle) {
		return win, err
	}
	req.Lanes = nil
	return w.Hub.Step(ctx, req)
}

// DeliverOnce sends up to DeliverBatch pending deliveries.
func (w *Worker) DeliverOnce(ctx context.Context) (int, error) {
	if w.Deliverer == nil {
		return 0, nil
	}
	return w.Hub.Deliver(ctx, w.Deliverer.Deliver, w.batch())
}

func (w *Worker) batch() int {
	if w.DeliverBatch <= 0 {
		return 16
	}
	return w.DeliverBatch
}

// ReconcileOnce asks the Deliverer about up to DeliverBatch deliveries that
// have been unknown for ReconcileAfter. It does nothing when the Deliverer
// cannot answer status queries.
func (w *Worker) ReconcileOnce(ctx context.Context) (int, error) {
	q, ok := w.Deliverer.(contract.DeliveryReconciler)
	if !ok {
		return 0, nil
	}
	after := w.ReconcileAfter
	if after <= 0 {
		after = 30 * time.Second
	}
	return w.Hub.Reconcile(ctx, q, after, w.batch())
}

// RunSteps runs one step loop until ctx ends.
func (w *Worker) RunSteps(ctx context.Context) error {
	for ctx.Err() == nil {
		_, err := w.StepOnce(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if !errors.Is(err, ErrIdle) {
			w.report(err)
		}
		sleep(ctx, w.idle())
	}
	return ctx.Err()
}

// RunDeliveries runs the delivery loop until ctx ends.
func (w *Worker) RunDeliveries(ctx context.Context) error {
	every := w.ReconcileEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	var last time.Time
	for ctx.Err() == nil {
		if time.Since(last) >= every {
			last = time.Now()
			if _, err := w.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
				w.report(err)
			}
		}
		n, err := w.DeliverOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.report(err)
		}
		if err != nil || n == 0 {
			sleep(ctx, w.idle())
		}
	}
	return ctx.Err()
}

// Run runs the step loops and the delivery loop until ctx ends, then
// returns ctx.Err().
func (w *Worker) Run(ctx context.Context) error {
	n := w.Concurrency
	if n <= 0 {
		n = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.RunSteps(ctx) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = w.RunDeliveries(ctx) }()
	wg.Wait()
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
