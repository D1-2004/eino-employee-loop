package scene_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/scene"
)

type decisionRunner struct{ d contract.Decision }

func (r decisionRunner) RunWindow(context.Context, contract.Window) (contract.Outcome, error) {
	return contract.Outcome{Decision: r.d}, nil
}

// Every decision with public text is delivered except Quiet.
func TestStepDeliversEveryPublicDecisionText(t *testing.T) {
	cases := []struct {
		d    contract.Decision
		want int
	}{
		{contract.Decision{Kind: contract.Reply, Reply: "hi"}, 1},
		{contract.Decision{Kind: contract.Dispatched, Reply: "On it."}, 1},
		{contract.Decision{Kind: contract.Waiting, Reply: "Which week?"}, 1},
		{contract.Decision{Kind: contract.Dispatched}, 0},
		{contract.Decision{Kind: contract.Waiting, Reply: "  "}, 0},
		{contract.Decision{Kind: contract.Quiet, Reply: "never said"}, 0},
	}
	for _, c := range cases {
		clk := newClock()
		h, st := newHub(clk, decisionRunner{c.d})
		mustAdmit(t, h, ev(clk, "message.addressed", "s1", contract.ActorHuman, nil, "hello"))
		clk.advance(2 * time.Second)
		if _, err := h.Step(context.Background(), contract.ClaimRequest{Worker: "w"}); err != nil {
			t.Fatal(err)
		}
		if got := len(st.Deliveries()); got != c.want {
			t.Errorf("%+v: %d deliveries, want %d", c.d, got, c.want)
		}
	}
}

type recDeliverer struct {
	mu   sync.Mutex
	sent []contract.Delivery
}

func (d *recDeliverer) Deliver(_ context.Context, del contract.Delivery) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, del)
	return "p-" + del.ID, nil
}

func (d *recDeliverer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sent)
}

func TestWorkerRunsForegroundFirstAndDelivers(t *testing.T) {
	clk := newClock()
	var mu sync.Mutex
	r := &scriptRunner{reply: map[contract.SessionKind]string{contract.SessionInteractive: "hi", contract.SessionFollowUp: "report"}}
	locked := scene.Runner(runnerFunc(func(ctx context.Context, w contract.Window) (contract.Outcome, error) {
		mu.Lock()
		defer mu.Unlock()
		return r.RunWindow(ctx, w)
	}))
	h, st := newHub(clk, locked)
	mustAdmit(t, h, ev(clk, "execution.result", "s2", contract.ActorSystem, map[string]string{contract.RefTask: "t1"}, "done"))
	mustAdmit(t, h, ev(clk, "message.addressed", "s1", contract.ActorHuman, nil, "hello"))
	clk.advance(2 * time.Second)

	d := &recDeliverer{}
	w := &scene.Worker{Hub: h, Deliverer: d, Idle: time.Millisecond}
	first, err := w.StepOnce(context.Background())
	if err != nil || first.Lane != contract.LaneForeground {
		t.Fatalf("first claim %+v err %v", first, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for d.count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("deliveries: %+v", st.Deliveries())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("run: %v", err)
	}
	for _, del := range st.Deliveries() {
		if del.State != contract.DeliveryDelivered || del.ProviderID != "p-"+del.ID {
			t.Fatalf("delivery state %+v", del)
		}
	}
}

type runnerFunc func(context.Context, contract.Window) (contract.Outcome, error)

func (f runnerFunc) RunWindow(ctx context.Context, w contract.Window) (contract.Outcome, error) {
	return f(ctx, w)
}
