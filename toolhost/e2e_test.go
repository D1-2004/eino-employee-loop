package toolhost_test

import (
	"context"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/toolhost"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// The three services in one process, joined only by contract types:
// the loop (Hub + KernelRunner + toolhost) dispatches through a Task
// Manager, and the Task Manager reports back by publishing an
// execution.result event to the loop's EventSink.
func TestLoopDispatchesAndReportsThroughTheTaskManager(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: t0}
	st := memstore.New()
	tasks := newFakeTasks()
	tasks.Now = clk.now
	host := toolhost.New(toolhost.Ports{Tasks: tasks, Memory: memstore.NewMemory(), Conversation: memstore.NewConversation(), Journal: st})
	model := kt.New(
		// Wake 1 (foreground): dispatch real work with an acknowledgement.
		kt.Tools(kt.Call{ID: "d1", Name: "dispatch_task", Args: map[string]any{
			"source_ref": "m1", "goal": "weekly report", "prompt": "build the weekly report", "reply": "On it."}}),
		// Wake 2 (task follow-up): read the verified state, then report.
		kt.Tools(kt.Call{ID: "r1", Name: "read_task", Args: map[string]any{"source_ref": "task-1/result", "task_ref": "task-1"}}),
		kt.Tools(kt.Call{ID: "r2", Name: "reply", Args: map[string]any{"source_ref": "task-1/result", "reply": "The weekly report is ready: 42 rows."}}),
	)
	hub := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: clk.now,
		Runner: &scene.KernelRunner{
			Model: model, Journal: st, Tools: host.Specs(), Persona: "You help the team.", Locale: "en", Tenant: "t1",
			Hosts: func(win contract.Window) contract.Host { return host.ForWindow(win, "t1") },
		}}
	tasks.Sink = hub

	if _, err := hub.Admit(ctx, event("m1", alice, "please build the weekly report")); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Second)
	first, err := hub.Step(ctx, contract.ClaimRequest{Worker: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.dispatches) != 1 {
		t.Fatalf("one dispatch, got %d", len(tasks.dispatches))
	}
	req := tasks.dispatches[0]
	if req.IdempotencyKey != first.ID+"/0/d1" || req.Requester != alice || req.SourceRef != "m1" || req.Tenant != "t1" {
		t.Fatalf("dispatch request: %+v", req)
	}
	if res, ok := st.Result(first.ID); !ok || res.Outcome.Decision.Kind != contract.Dispatched || res.Outcome.Decision.Reply != "On it." ||
		len(res.Outcome.Receipts) != 1 || res.Outcome.Receipts[0].ID != "task-1" {
		t.Fatalf("first wake outcome: %+v", res)
	}

	ack, err := tasks.finish(ctx, "task-1", "42 rows")
	if err != nil || ack.Lane != contract.LaneBackground || ack.WindowID == "" {
		t.Fatalf("result event admitted to a follow-up session: %+v %v", ack, err)
	}
	clk.advance(2 * time.Second)
	if n, err := hub.Drain(ctx, contract.ClaimRequest{Worker: "w1"}); err != nil || n != 1 {
		t.Fatalf("drain n=%d err=%v", n, err)
	}
	follow, err := st.Window(ctx, "e1", ack.WindowID)
	if err != nil || follow.Session.Kind != contract.SessionFollowUp || follow.Session.Key != "task-1" || follow.State != contract.WindowDone {
		t.Fatalf("follow-up window: %+v %v", follow, err)
	}
	if model.Calls() != 3 {
		t.Fatalf("model calls: %d", model.Calls())
	}

	// Exactly two deliveries: the dispatch acknowledgement and the report.
	var ackDelivery, report *contract.Delivery
	deliveries := st.Deliveries()
	for i := range deliveries {
		switch deliveries[i].WindowID {
		case first.ID:
			ackDelivery = &deliveries[i]
		case follow.ID:
			report = &deliveries[i]
		}
	}
	if len(deliveries) != 2 {
		t.Fatalf("want two deliveries, got %+v", deliveries)
	}
	if ackDelivery == nil || ackDelivery.Text != "On it." || ackDelivery.ReplyTo != "m1" || ackDelivery.Session.Kind != contract.SessionInteractive {
		t.Fatalf("acknowledgement: %+v", ackDelivery)
	}
	if report == nil || report.Text != "The weekly report is ready: 42 rows." || report.Session.Kind != contract.SessionFollowUp || report.Scene.ID != "s1" {
		t.Fatalf("result report: %+v in %+v", report, deliveries)
	}
}
