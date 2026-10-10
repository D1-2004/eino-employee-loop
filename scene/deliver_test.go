package scene_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/scene"
)

// flakyChannel times out after sending, then answers status queries.
type flakyChannel struct {
	sends   int
	queried int
}

func (f *flakyChannel) Deliver(context.Context, contract.Delivery) (string, error) {
	f.sends++
	return "", context.DeadlineExceeded
}

func (f *flakyChannel) DeliveryStatus(_ context.Context, d contract.Delivery) (contract.DeliveryState, string, error) {
	f.queried++
	return contract.DeliveryDelivered, "pm-" + d.ID, nil
}

// The worker reconciles through a Deliverer that answers status queries.
func TestWorkerReconcilesUnknownDeliveries(t *testing.T) {
	th := deliverable(t)
	th.h.Now = nil
	ch := &flakyChannel{}
	w := &scene.Worker{Hub: th.h, Deliverer: ch, ReconcileAfter: time.Nanosecond}
	ctx := context.Background()
	if _, err := w.DeliverOnce(ctx); err == nil {
		t.Fatal("timeout reported")
	}
	time.Sleep(time.Millisecond)
	if n, err := w.ReconcileOnce(ctx); err != nil || n != 1 || ch.sends != 1 || ch.queried != 1 {
		t.Fatalf("n=%d err=%v sends=%d queried=%d", n, err, ch.sends, ch.queried)
	}
	if d := th.st.Deliveries(); d[0].State != contract.DeliveryDelivered || d[0].ProviderID != "pm-"+d[0].ID {
		t.Fatalf("delivered: %+v", d[0])
	}
	plain := &scene.Worker{Hub: th.h, Deliverer: &recDeliverer{}}
	if n, err := plain.ReconcileOnce(ctx); n != 0 || err != nil {
		t.Fatalf("a deliverer without status queries skips reconciliation: %d %v", n, err)
	}
}

func deliverable(t *testing.T) *testHub {
	t.Helper()
	c := newClock()
	h, st := newHub(c, &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "ok"}})
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hi"))
	c.advance(2 * time.Second)
	if _, err := h.Drain(context.Background(), contract.ClaimRequest{}); err != nil {
		t.Fatal(err)
	}
	return &testHub{h, st}
}

// Only a definite rejection marks a delivery failed.
func TestDeliverMarksOnlyDefiniteRejectionsFailed(t *testing.T) {
	for _, c := range []struct {
		err  error
		want contract.DeliveryState
	}{
		{fmt.Errorf("%w: no locator", contract.ErrDeliveryRejected), contract.DeliveryFailed},
		{context.DeadlineExceeded, contract.DeliveryUnknown},
		{errors.New("503 unavailable"), contract.DeliveryUnknown},
	} {
		th := deliverable(t)
		if _, err := th.h.Deliver(context.Background(), func(context.Context, contract.Delivery) (string, error) { return "", c.err }, 10); err == nil {
			t.Fatalf("%v: error reported", c.err)
		}
		if d := th.st.Deliveries(); d[0].State != c.want {
			t.Errorf("%v: state %s, want %s", c.err, d[0].State, c.want)
		}
	}
}

type statusFunc func(ctx context.Context, d contract.Delivery) (contract.DeliveryState, string, error)

func (f statusFunc) DeliveryStatus(ctx context.Context, d contract.Delivery) (contract.DeliveryState, string, error) {
	return f(ctx, d)
}

// Reconcile resolves unknown deliveries by status query and never sends.
func TestReconcileResolvesUnknownWithoutResending(t *testing.T) {
	th := deliverable(t)
	ctx := context.Background()
	if _, err := th.h.Deliver(ctx, func(context.Context, contract.Delivery) (string, error) { return "", context.DeadlineExceeded }, 10); err == nil {
		t.Fatal("timeout reported")
	}
	asked := 0
	still := statusFunc(func(context.Context, contract.Delivery) (contract.DeliveryState, string, error) {
		asked++
		return contract.DeliveryUnknown, "", nil
	})
	if n, err := th.h.Reconcile(ctx, still, 0, 10); err != nil || n != 0 || asked != 1 {
		t.Fatalf("still unknown: n=%d err=%v asked=%d", n, err, asked)
	}
	// The outbox stamps updates with the wall clock; compare against it.
	th.h.Now = nil
	if n, err := th.h.Reconcile(ctx, still, time.Hour, 10); err != nil || n != 0 || asked != 1 {
		t.Fatalf("recent unknown deliveries wait: n=%d err=%v asked=%d", n, err, asked)
	}
	sent := statusFunc(func(context.Context, contract.Delivery) (contract.DeliveryState, string, error) {
		return contract.DeliveryDelivered, "pm-9", nil
	})
	if n, err := th.h.Reconcile(ctx, sent, 0, 10); err != nil || n != 1 {
		t.Fatalf("resolved: n=%d err=%v", n, err)
	}
	if d := th.st.Deliveries(); d[0].State != contract.DeliveryDelivered || d[0].ProviderID != "pm-9" {
		t.Fatalf("delivered: %+v", d[0])
	}
	if n, _ := th.h.Deliver(ctx, func(context.Context, contract.Delivery) (string, error) {
		t.Fatal("never resent")
		return "", nil
	}, 10); n != 0 {
		t.Fatal("nothing pending")
	}
}

// A status query settles a delivery only as delivered or failed; anything
// else leaves it unknown.
func TestReconcileRejectsOtherStates(t *testing.T) {
	th := deliverable(t)
	ctx := context.Background()
	_, _ = th.h.Deliver(ctx, func(context.Context, contract.Delivery) (string, error) { return "", errors.New("reset") }, 10)
	bad := statusFunc(func(context.Context, contract.Delivery) (contract.DeliveryState, string, error) {
		return contract.DeliveryPending, "", nil
	})
	if _, err := th.h.Reconcile(ctx, bad, 0, 10); err == nil {
		t.Fatal("pending is not a reconciled state")
	}
	if d := th.st.Deliveries(); d[0].State != contract.DeliveryUnknown {
		t.Fatalf("unchanged: %s", d[0].State)
	}
	notSent := statusFunc(func(context.Context, contract.Delivery) (contract.DeliveryState, string, error) {
		return contract.DeliveryFailed, "", nil
	})
	if n, err := th.h.Reconcile(ctx, notSent, 0, 10); err != nil || n != 1 {
		t.Fatalf("definitely not sent: n=%d err=%v", n, err)
	}
	if d := th.st.Deliveries(); d[0].State != contract.DeliveryFailed {
		t.Fatalf("failed: %s", d[0].State)
	}
}
