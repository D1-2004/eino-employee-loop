package scene_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)} }

// scriptRunner returns a fixed outcome per session kind and records order.
type scriptRunner struct {
	order  []string
	during func(win contract.Window)
	reply  map[contract.SessionKind]string
}

func (r *scriptRunner) RunWindow(_ context.Context, win contract.Window) (contract.Outcome, error) {
	r.order = append(r.order, win.Session.String())
	if r.during != nil {
		r.during(win)
	}
	if text := r.reply[win.Session.Kind]; text != "" {
		return contract.Outcome{Decision: contract.Decision{Kind: contract.Reply, Reply: text}}, nil
	}
	return contract.Outcome{Decision: contract.Decision{Kind: contract.Quiet}}, nil
}

func newHub(c *clock, r scene.Runner) (*scene.Hub, *memstore.Store) {
	st := memstore.New()
	return &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Runner: r, Now: c.now}, st
}

var seq int

func ev(c *clock, typ, scene string, actor contract.ActorKind, refs map[string]string, text string) contract.Event {
	seq++
	return contract.Event{ID: fmt.Sprintf("%s-%d", typ, seq), Source: "test", Type: typ, Scene: contract.SceneRef{ID: scene, Kind: contract.SceneGroup},
		Actor: contract.Actor{ID: "alice", Kind: actor, DisplayName: "Alice"}, OccurredAt: c.now(), Refs: refs, Text: text}
}

func mustAdmit(t *testing.T, h *scene.Hub, e contract.Event) scene.Admitted {
	t.Helper()
	a, err := h.Admit(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAdmitChecksTheEmployeeAddress(t *testing.T) {
	c := newClock()
	h, _ := newHub(c, &scriptRunner{})
	e := ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hello")
	e.Employee = "e2"
	if _, err := h.Admit(context.Background(), e); !errors.Is(err, contract.ErrUnknownEmployee) {
		t.Fatalf("an event addressed to another employee is rejected: %v", err)
	}
	unaddressed := ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hello")
	first := mustAdmit(t, h, unaddressed)
	addressed := unaddressed
	addressed.Employee = "e1"
	again := mustAdmit(t, h, addressed)
	if !again.Duplicate || again.Receipt.ID != first.Receipt.ID {
		t.Fatalf("an empty address means this employee, so both forms are one event: %+v", again)
	}
	if first.Window.Items[0].Event.Employee != "e1" {
		t.Fatalf("the admitted event carries its address: %+v", first.Window.Items[0].Event)
	}
}

func TestAddressedMessagePreemptsObservationAndRunsFirst(t *testing.T) {
	c := newClock()
	r := &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "hi Alice"}}
	h, st := newHub(c, r)
	obs := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "anyone know the release date?"))
	follow := mustAdmit(t, h, ev(c, "execution.result", "s1", contract.ActorSystem, map[string]string{contract.RefTask: "t1"}, "report ready"))
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@employee hello"))

	w, _ := st.Window(context.Background(), "e1", obs.Window.ID)
	if w.State != contract.WindowCancelled || w.Reason != contract.CancelForegroundAdmission {
		t.Fatalf("observation must be preempted, got %s/%s", w.State, w.Reason)
	}
	if w, _ := st.Window(context.Background(), "e1", follow.Window.ID); w.State == contract.WindowCancelled {
		t.Fatal("task follow-up is not preemptible")
	}
	c.advance(2 * time.Second)
	n, err := h.Drain(context.Background(), contract.ClaimRequest{Worker: "w1"})
	if err != nil || n != 2 {
		t.Fatalf("drain n=%d err=%v", n, err)
	}
	if r.order[0] != "s1/interactive" || r.order[1] != "s1/follow_up/t1" {
		t.Fatalf("foreground first, got %v", r.order)
	}
	if d := st.Deliveries(); len(d) != 1 || d[0].Text != "hi Alice" || d[0].ReplyTo == "" {
		t.Fatalf("one foreground reply with a reply target, got %+v", d)
	}
}

func TestCoalescingAndSessionsPerTask(t *testing.T) {
	c := newClock()
	h, _ := newHub(c, &scriptRunner{})
	a := mustAdmit(t, h, ev(c, "execution.progress", "s1", contract.ActorSystem, map[string]string{contract.RefTask: "t1"}, "10%"))
	c.advance(100 * time.Millisecond)
	b := mustAdmit(t, h, ev(c, "execution.progress", "s1", contract.ActorSystem, map[string]string{contract.RefTask: "t1"}, "50%"))
	other := mustAdmit(t, h, ev(c, "execution.progress", "s1", contract.ActorSystem, map[string]string{contract.RefTask: "t2"}, "5%"))
	if a.Window.ID != b.Window.ID || len(b.Window.Items) != 2 {
		t.Fatalf("progress of one task coalesces: %+v", b.Window)
	}
	if other.Window.ID == a.Window.ID {
		t.Fatal("each task has its own follow-up session")
	}
}

func TestBackgroundSessionsRunConcurrentlyUnlessSerialized(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{})
	mustAdmit(t, h, ev(c, "routine.due", "s1", contract.ActorSystem, map[string]string{contract.RefRoutine: "daily"}, ""))
	mustAdmit(t, h, ev(c, "scene.bound", "s1", contract.ActorSystem, nil, ""))
	ctx := context.Background()
	w1, _ := st.Claim(ctx, contract.ClaimRequest{Now: c.now(), SerializeScene: true})
	w2, _ := st.Claim(ctx, contract.ClaimRequest{Now: c.now(), SerializeScene: true})
	if w1 == nil || w2 != nil {
		t.Fatalf("serialized scene runs one window at a time: %v %v", w1, w2)
	}
	w3, _ := st.Claim(ctx, contract.ClaimRequest{Now: c.now()})
	if w3 == nil || w3.Session.Kind == w1.Session.Kind {
		t.Fatalf("different background sessions may run concurrently: %v", w3)
	}
}

func TestReplyOfWindowCancelledWhileRunningIsDropped(t *testing.T) {
	c := newClock()
	r := &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionObservation: "the release is Friday"}}
	h, st := newHub(c, r)
	r.during = func(win contract.Window) {
		if win.Session.Kind == contract.SessionObservation {
			// A person addresses the employee while the observation runs.
			mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "@employee wait"))
		}
	}
	mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "release date?"))
	c.advance(31 * time.Second)
	if _, err := h.Step(context.Background(), contract.ClaimRequest{Worker: "w1"}); err != nil {
		t.Fatal(err)
	}
	if len(st.Deliveries()) != 0 {
		t.Fatalf("a preempted observation must not speak: %+v", st.Deliveries())
	}
}

func TestPolicyChangeRevokesObservation(t *testing.T) {
	c := newClock()
	h, st := newHub(c, &scriptRunner{})
	obs := mustAdmit(t, h, ev(c, "message.observed", "s1", contract.ActorHuman, nil, "x"))
	pol := mustAdmit(t, h, ev(c, "policy.changed", "s1", contract.ActorSystem, nil, ""))
	if pol.Window != nil || pol.Cancelled != 1 {
		t.Fatalf("host-lane event with one revocation: %+v", pol)
	}
	if w, _ := st.Window(context.Background(), "e1", obs.Window.ID); w.State != contract.WindowCancelled || w.Reason != contract.CancelRevoked {
		t.Fatalf("revoked: %+v", w)
	}
}

func TestDuplicateAndConflictingAdmission(t *testing.T) {
	c := newClock()
	h, _ := newHub(c, &scriptRunner{})
	e := ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hi")
	first := mustAdmit(t, h, e)
	again := mustAdmit(t, h, e)
	if !again.Duplicate || again.Receipt.ID != first.Receipt.ID || again.Window != nil {
		t.Fatalf("replay is idempotent: %+v", again)
	}
	e.Text = "changed"
	if _, err := h.Admit(context.Background(), e); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("same id with new content conflicts: %v", err)
	}
}

func TestDeliverNeverResendsUnknown(t *testing.T) {
	c := newClock()
	r := &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "ok"}}
	h, st := newHub(c, r)
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hi"))
	c.advance(2 * time.Second)
	if _, err := h.Drain(context.Background(), contract.ClaimRequest{}); err != nil {
		t.Fatal(err)
	}
	sends := 0
	_, err := h.Deliver(context.Background(), func(context.Context, contract.Delivery) (string, error) {
		sends++
		return "", errors.New("network")
	}, 10)
	if err == nil || sends != 1 {
		t.Fatalf("send attempted once: sends=%d err=%v", sends, err)
	}
	if n, _ := h.Deliver(context.Background(), func(context.Context, contract.Delivery) (string, error) { sends++; return "p1", nil }, 10); n != 0 || sends != 1 {
		t.Fatalf("unknown deliveries are not retried blindly: n=%d sends=%d", n, sends)
	}
	// A transport error is ambiguous: the message may have been sent.
	if d := st.Deliveries(); d[0].State != contract.DeliveryUnknown {
		t.Fatalf("state %s", d[0].State)
	}
}

type testHub struct {
	h  *scene.Hub
	st *memstore.Store
}

func TestKernelRunnerReplaysJournalAfterReclaim(t *testing.T) {
	c := newClock()
	st := memstore.New()
	effects := 0
	host := contract.HostFunc(func(_ context.Context, _ contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
		if call.Name == "dispatch_task" {
			effects++
			return contract.ToolResult{Content: "{}", Receipt: "task-1", Terminal: &contract.Decision{Kind: contract.Dispatched, Reply: "on it"}}, nil
		}
		return contract.ToolResult{}, errors.New("unexpected")
	})
	tools, _ := contract.StandardTools()
	dispatch := kt.Tools(kt.Call{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p", "reply": "on it"}})
	h := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: c.now,
		Runner: &scene.KernelRunner{Model: kt.New(dispatch), Host: host, Journal: st, Tools: tools, Persona: "You help.", Locale: "en"}}
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "build the report"))
	c.advance(2 * time.Second)
	ctx := context.Background()
	win, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now(), LeaseFor: time.Second})
	if err != nil || win == nil {
		t.Fatal(err)
	}
	first := &scene.KernelRunner{Model: kt.New(dispatch), Host: host, Journal: st, Tools: tools, Persona: "You help.", Locale: "en"}
	out, err := first.RunWindow(ctx, *win)
	if err != nil || out.Decision.Kind != contract.Dispatched || effects != 1 {
		t.Fatalf("first run: %+v %v effects=%d", out, err, effects)
	}
	// The worker dies before Complete; the lease expires and another worker
	// re-claims the window. The model is not asked again and the effect is
	// not repeated.
	c.advance(2 * time.Second)
	again, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now()})
	if err != nil || again == nil || again.ID != win.ID || again.Lease.Generation <= win.Lease.Generation {
		t.Fatalf("re-claim: %+v %v", again, err)
	}
	fresh := kt.New()
	second := &scene.KernelRunner{Model: fresh, Host: host, Journal: st, Tools: tools, Persona: "You help.", Locale: "en"}
	out, err = second.RunWindow(ctx, *again)
	if err != nil || out.Decision.Kind != contract.Dispatched || effects != 1 || fresh.Calls() != 0 {
		t.Fatalf("replay: %+v %v effects=%d calls=%d", out, err, effects, fresh.Calls())
	}
	if err := st.Complete(ctx, *win.Lease, contract.WindowResult{}, nil); !errors.Is(err, contract.ErrLease) {
		t.Fatalf("the stale owner is fenced: %v", err)
	}
}

func TestJournalConflictHoldsTheWindow(t *testing.T) {
	c := newClock()
	st := memstore.New()
	tools, _ := contract.StandardTools()
	runner := func(m *kt.Model, locale string) *scene.KernelRunner {
		return &scene.KernelRunner{Model: m, Host: contract.HostFunc(func(context.Context, contract.Identity, contract.ToolCall) (contract.ToolResult, error) {
			return contract.ToolResult{}, errors.New("unexpected")
		}), Journal: st, Tools: tools, Persona: "You help.", Locale: locale}
	}
	h := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: c.now}
	mustAdmit(t, h, ev(c, "message.addressed", "s1", contract.ActorHuman, nil, "hi"))
	c.advance(2 * time.Second)
	ctx := context.Background()
	win, err := st.Claim(ctx, contract.ClaimRequest{Now: c.now(), LeaseFor: time.Second})
	if err != nil || win == nil {
		t.Fatal(err)
	}
	if _, err := runner(kt.New(kt.Truncated("cut"), kt.Text("ok")), "en").RunWindow(ctx, *win); err != nil {
		t.Fatal(err)
	}
	// The re-claim renders the repair instruction in another locale, so the
	// second recorded request no longer matches.
	c.advance(2 * time.Second)
	fresh := kt.New(kt.Text("fresh"))
	h.Runner = runner(fresh, "zh-CN")
	if _, err := h.Step(ctx, contract.ClaimRequest{}); err != nil {
		t.Fatal(err)
	}
	if fresh.Calls() != 0 {
		t.Fatalf("a mismatch never re-asks the model: %d calls", fresh.Calls())
	}
	if w, _ := st.Window(ctx, "e1", win.ID); w.State != contract.WindowHeld || w.Reason != scene.HoldJournalConflict {
		t.Fatalf("held with a reason: %+v", w)
	}
}
