package scene_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
)

// An admission past the coalesce deadline of the open window, with no Claim
// in between, seals the old window instead of orphaning it: both windows
// run, in order, and background work of the scene is not blocked.
func TestEnqueuePastMaxWaitSealsTheOldWindow(t *testing.T) {
	c := newClock()
	r := &scriptRunner{}
	h, st := newHub(c, r)
	obs := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "x"))
	first := mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e one"))
	c.advance(1100 * time.Millisecond)
	second := mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e two"))
	if first.Window.ID == second.Window.ID {
		t.Fatal("past MaxWait a new window opens")
	}
	if w, _ := st.Window(context.Background(), "e1", first.Window.ID); w.State != contract.WindowReady {
		t.Fatalf("the old window is sealed, got %s", w.State)
	}
	// The observation was preempted by the second address; admit another one
	// that must still run once the foreground windows are done.
	if w, _ := st.Window(context.Background(), "e1", obs.Window.ID); w.State != contract.WindowCancelled {
		t.Fatalf("observation preempted, got %s", w.State)
	}
	later := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "y"))
	c.advance(time.Minute)
	n, err := h.Drain(context.Background(), contract.ClaimRequest{Worker: "w1"})
	if err != nil || n != 3 {
		t.Fatalf("drain n=%d err=%v order=%v", n, err, r.order)
	}
	for _, id := range []string{first.Window.ID, second.Window.ID, later.Window.ID} {
		if w, _ := st.Window(context.Background(), "e1", id); w.State != contract.WindowDone {
			t.Fatalf("window %s: %s", id, w.State)
		}
	}
	if r.order[0] != "s1/interactive" || r.order[1] != "s1/interactive" || r.order[2] != "s1/observation" {
		t.Fatalf("order %v", r.order)
	}
}

// A non-coalescing item in a session with an open window seals that window
// first, so the earlier items are processed first.
func TestNonCoalescingItemSealsTheOpenWindowFirst(t *testing.T) {
	c := newClock()
	r := &itemsRunner{}
	h, _ := newHub(c, r)
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e first"))
	c.advance(10 * time.Millisecond)
	mustAdmit(t, h, ev(c, "human.response", "s1", contract.ActorHuman, nil, "second"))
	if n, err := h.Drain(context.Background(), contract.ClaimRequest{Worker: "w1"}); err != nil || n != 2 {
		t.Fatalf("drain n=%d err=%v", n, err)
	}
	if len(r.texts) != 2 || r.texts[0] != "@e first" || r.texts[1] != "second" {
		t.Fatalf("order %v", r.texts)
	}
}

type itemsRunner struct{ texts []string }

func (r *itemsRunner) RunWindow(_ context.Context, win contract.Window) (contract.Outcome, error) {
	for _, it := range win.Items {
		r.texts = append(r.texts, it.Event.Text)
	}
	return contract.Outcome{Decision: contract.Decision{Kind: contract.Quiet}}, nil
}

// failOnce fails the wrapped step once.
type failOnce struct{ failed bool }

func (f *failOnce) fail() error {
	if f.failed {
		return nil
	}
	f.failed = true
	return errors.New("transient")
}

type flakyQueue struct {
	contract.SessionQueue
	enqueue *failOnce
}

func (q flakyQueue) Enqueue(ctx context.Context, employee string, adm contract.Admission, receiptID string, now time.Time) (contract.Window, error) {
	if err := q.enqueue.fail(); err != nil {
		return contract.Window{}, err
	}
	return q.SessionQueue.Enqueue(ctx, employee, adm, receiptID, now)
}

// A failed enqueue after the receipt committed is re-applied when the
// publisher retries; the retry is still reported as a duplicate.
func TestRetriedPublishAppliesAnUnappliedSessionAdmission(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{})
	h.Queue = flakyQueue{SessionQueue: st, enqueue: &failOnce{}}
	e := ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hi")
	if _, err := h.Publish(context.Background(), e); err == nil {
		t.Fatal("the first publish fails")
	}
	ack, err := h.Publish(context.Background(), e)
	if err != nil || !ack.Duplicate || ack.WindowID == "" {
		t.Fatalf("retry applies the admission: %+v %v", ack, err)
	}
	again, err := h.Publish(context.Background(), e)
	if err != nil || !again.Duplicate || again.WindowID != "" {
		t.Fatalf("an applied duplicate does nothing: %+v %v", again, err)
	}
	w, _ := st.Window(context.Background(), "e1", ack.WindowID)
	if len(w.Items) != 1 {
		t.Fatalf("enqueued once: %+v", w)
	}
	if r, _ := st.Receipt(context.Background(), "e1", ack.ReceiptID); !r.Applied {
		t.Fatalf("receipt applied: %+v", r)
	}
}

// A failed host applier is re-run when the publisher retries.
func TestRetriedPublishReappliesAFailedHostEvent(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{})
	f := &failOnce{}
	applied := 0
	h.OnHostEvent = func(context.Context, contract.Admission, contract.EventReceipt) error {
		if err := f.fail(); err != nil {
			return err
		}
		applied++
		return nil
	}
	obs := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "x"))
	e := ev(c, "policy.changed", "s1", contract.ActorSystem, nil, "")
	if _, err := h.Publish(context.Background(), e); err == nil {
		t.Fatal("the first publish fails")
	}
	// An observation admitted between the attempts is not revoked by the
	// re-run: the revocation applies once per receipt.
	newer := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "y"))
	ack, err := h.Publish(context.Background(), e)
	if err != nil || !ack.Duplicate || applied != 1 {
		t.Fatalf("retry applies the host event: %+v %v applied=%d", ack, err, applied)
	}
	if _, err := h.Publish(context.Background(), e); err != nil || applied != 1 {
		t.Fatalf("an applied host event is not re-run: applied=%d err=%v", applied, err)
	}
	if w, _ := st.Window(context.Background(), "e1", obs.Window.ID); w.State != contract.WindowCancelled {
		t.Fatalf("revoked: %+v", w)
	}
	if w, _ := st.Window(context.Background(), "e1", newer.Window.ID); w.State == contract.WindowCancelled {
		t.Fatalf("a later observation is untouched: %+v", w)
	}
}

// failingOutbox rejects every direct Post.
type failingOutbox struct{ contract.Outbox }

func (failingOutbox) Post(context.Context, contract.Delivery) (contract.Delivery, bool, error) {
	return contract.Delivery{}, false, errors.New("outbox down")
}

// The reply is committed with the completion, not posted afterwards.
func TestStepCommitsTheReplyWithTheCompletion(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "hi"}})
	h.Outbox = failingOutbox{st}
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hello"))
	c.advance(2 * time.Second)
	win, err := h.Step(context.Background(), contract.ClaimRequest{Worker: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := st.Window(context.Background(), "e1", win.ID); w.State != contract.WindowDone {
		t.Fatalf("done: %s", w.State)
	}
	if d := st.Deliveries(); len(d) != 1 || d[0].Text != "hi" || d[0].State != contract.DeliveryPending {
		t.Fatalf("delivery committed with the completion: %+v", d)
	}
}

// A Step whose reply conflicts with an existing delivery holds the window
// instead of re-running it forever.
func TestStepHoldsAWindowWhoseReplyConflicts(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "hi"}})
	a := mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hello"))
	if _, _, err := st.Post(context.Background(), contract.Delivery{ID: a.Window.ID + "/reply", Employee: "e1", Text: "other"}); err != nil {
		t.Fatal(err)
	}
	c.advance(2 * time.Second)
	if _, err := h.Step(context.Background(), contract.ClaimRequest{Worker: "w"}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("conflict reported: %v", err)
	}
	if w, _ := st.Window(context.Background(), "e1", a.Window.ID); w.State != contract.WindowHeld || w.Reason != scene.HoldDeliveryConflict {
		t.Fatalf("held: %+v", w)
	}
}

// failingFirstRunner fails its first wake (a provider outage) and records
// the items of every wake.
type failingFirstRunner struct {
	itemsRunner
	failed bool
}

func (r *failingFirstRunner) RunWindow(ctx context.Context, win contract.Window) (contract.Outcome, error) {
	if !r.failed {
		r.failed = true
		return contract.Outcome{}, errors.New("provider outage")
	}
	return r.itemsRunner.RunWindow(ctx, win)
}

// A window that waits for its retry keeps its place: a later window of the
// same session is not claimed before it, so messages are answered in the
// order they arrived.
func TestRetryKeepsTheSessionInOrder(t *testing.T) {
	c := newClock()
	r := &failingFirstRunner{}
	h, _ := newHub(c, r)
	h.RetryDelay = 5 * time.Second
	ctx := context.Background()
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e first"))
	c.advance(2 * time.Second)
	if _, err := h.Step(ctx, contract.ClaimRequest{Worker: "w1"}); err == nil {
		t.Fatal("the first wake fails and is retried")
	}
	c.advance(500 * time.Millisecond)
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e second"))
	c.advance(time.Second)
	if win, err := h.Step(ctx, contract.ClaimRequest{Worker: "w1"}); !errors.Is(err, scene.ErrIdle) {
		t.Fatalf("the later window waits for the retry of the earlier one: %+v %v", win, err)
	}
	c.advance(10 * time.Second)
	if n, err := h.Drain(ctx, contract.ClaimRequest{Worker: "w1"}); err != nil || n != 2 {
		t.Fatalf("drain n=%d err=%v", n, err)
	}
	if len(r.texts) != 2 || r.texts[0] != "@e first" || r.texts[1] != "@e second" {
		t.Fatalf("order %v", r.texts)
	}
}

// While a foreground wake runs, an observation admitted after it began is
// not claimed: proactive work of the scene waits until the person who
// addressed the employee has been answered.
func TestRunningForegroundHoldsBackObservationOfItsScene(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{})
	ctx := context.Background()
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e when is the release?"))
	c.advance(2 * time.Second)
	fg, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now(), LeaseFor: 5 * time.Minute})
	if err != nil || fg == nil || fg.Lane != contract.LaneForeground {
		t.Fatalf("foreground claim: %+v %v", fg, err)
	}
	obs := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "release is soon I think"))
	other := mustAdmit(t, h, ev(c, "message.observed", "s2", contract.ActorHuman, nil, "unrelated"))
	c.advance(40 * time.Second)
	got, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now()})
	if err != nil || got == nil || got.ID != other.Window.ID {
		t.Fatalf("only the other scene's observation is claimable: %+v %v", got, err)
	}
	if got, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now()}); err != nil || got != nil {
		t.Fatalf("the observation waits for the running foreground window: %+v %v", got, err)
	}
	if err := st.Complete(ctx, *fg.Lease, contract.WindowResult{}, nil); err != nil {
		t.Fatal(err)
	}
	got, err = st.Claim(ctx, contract.ClaimRequest{Now: c.now()})
	if err != nil || got == nil || got.ID != obs.Window.ID {
		t.Fatalf("after the foreground window the observation runs: %+v %v", got, err)
	}
}

// The same rule through the Hub: a worker that claims while another runs
// the foreground wake gets nothing from that scene.
func TestHubDoesNotRunObservationBesideForeground(t *testing.T) {
	c := newClock()
	var during *contract.Window
	r := &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "Friday"}}
	h, st := newHub(c, r)
	ctx := context.Background()
	r.during = func(win contract.Window) {
		if win.Session.Kind != contract.SessionInteractive {
			return
		}
		mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "maybe Thursday?"))
		c.advance(40 * time.Second)
		during, _ = st.Claim(ctx, contract.ClaimRequest{Worker: "w2", Now: c.now()})
	}
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@e release date?"))
	c.advance(2 * time.Second)
	if _, err := h.Step(ctx, contract.ClaimRequest{Worker: "w1"}); err != nil {
		t.Fatal(err)
	}
	if during != nil {
		t.Fatalf("a second worker claimed %s beside the foreground wake", during.Session)
	}
	if n, err := h.Drain(ctx, contract.ClaimRequest{Worker: "w1"}); err != nil || n != 1 {
		t.Fatalf("the observation runs afterwards: n=%d err=%v", n, err)
	}
	if len(r.order) != 2 || r.order[1] != "s1/observation" {
		t.Fatalf("order %v", r.order)
	}
}

// A completion whose delivery conflicts commits nothing.
func TestCompleteIsAtomicWithItsDeliveries(t *testing.T) {
	c := newClock()
	st := memstore.New()
	h := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: c.now}
	a := mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hello"))
	c.advance(2 * time.Second)
	ctx := context.Background()
	win, _ := st.Claim(ctx, contract.ClaimRequest{Now: c.now()})
	if _, _, err := st.Post(ctx, contract.Delivery{ID: win.ID + "/reply", Employee: "e1", WindowID: win.ID, Text: "other"}); err != nil {
		t.Fatal(err)
	}
	d := contract.Delivery{ID: win.ID + "/reply", Employee: "e1", WindowID: win.ID, Text: "hi"}
	if err := st.Complete(ctx, *win.Lease, contract.WindowResult{}, []contract.Delivery{d}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if w, _ := st.Window(ctx, "e1", a.Window.ID); w.State != contract.WindowRunning {
		t.Fatalf("not completed: %s", w.State)
	}
	stale := *win.Lease
	stale.Token = "stale"
	if err := st.Complete(ctx, stale, contract.WindowResult{}, []contract.Delivery{{ID: "x", Employee: "e1", Text: "never"}}); !errors.Is(err, contract.ErrLease) {
		t.Fatalf("fenced: %v", err)
	}
	if len(st.Deliveries()) != 1 {
		t.Fatalf("a fenced completion posts nothing: %+v", st.Deliveries())
	}
}
