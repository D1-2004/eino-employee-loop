package toolhost_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/toolhost"
)

// A window read a task, then its worker died. The re-claim runs in a fresh
// toolhost instance (another process): the read is replayed from the
// journal without executing again, and the control that follows still
// finds the read_ref, because read authority comes from the journal.
func TestReclaimInAFreshToolhostKeepsReadRefs(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: t0}
	st := memstore.New()
	tasks := newFakeTasks()
	tasks.seed(contract.TaskView{TaskRef: "live", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskRunning, Revision: 5})
	newRunner := func(m *kt.Model) *scene.KernelRunner {
		host := toolhost.New(toolhost.Ports{Tasks: tasks, Journal: st})
		return &scene.KernelRunner{Model: m, Journal: st, Tools: host.Specs(), Persona: "You help.", Locale: "en", Tenant: "t1",
			Hosts: func(win contract.Window) contract.Host { return host.ForWindow(win, "t1") }}
	}
	hub := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: clk.now, Runner: newRunner(kt.New())}
	if _, err := hub.Admit(ctx, event("m1", alice, "stop the report")); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Second)
	win, err := st.Claim(ctx, contract.ClaimRequest{Now: clk.now(), LeaseFor: time.Second})
	if err != nil || win == nil {
		t.Fatalf("claim: %+v %v", win, err)
	}

	crash, cancel := context.WithCancel(ctx)
	first := kt.New(
		kt.Tools(kt.Call{ID: "r1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "live"}}),
		kt.Func(func([]*schema.Message) kt.Step { cancel(); return kt.Fail(context.Canceled) }),
	)
	if _, err := newRunner(first).RunWindow(crash, *win); !errors.Is(err, context.Canceled) {
		t.Fatalf("the worker dies after the read: %v", err)
	}

	clk.advance(2 * time.Second)
	again, err := st.Claim(ctx, contract.ClaimRequest{Now: clk.now()})
	if err != nil || again == nil || again.ID != win.ID {
		t.Fatalf("re-claim: %+v %v", again, err)
	}
	second := kt.New(kt.Tools(kt.Call{ID: "s1", Name: "stop_task", Args: map[string]any{"source_ref": "m1", "read_ref": "live@5", "reply": "Stopped."}}))
	out, err := newRunner(second).RunWindow(ctx, *again)
	if err != nil || out.Decision.Reply != "Stopped." || second.Calls() != 1 {
		t.Fatalf("stop after re-claim: %+v %v calls=%d", out, err, second.Calls())
	}
	if len(tasks.controls) != 1 || tasks.controls[0] != "stop:live:" {
		t.Fatalf("controls: %v", tasks.controls)
	}
}

// A control committed at the Task Manager but its response was lost, so the
// journal has no record and the re-claim runs the call again with the same
// idempotency key. The task's revision moved on with the commit; the Task
// Manager answers the known key with its receipt before it checks the
// read_ref revision, so the replay is not refused and runs no second
// continuation.
func TestReplayedControlAfterALostResponseIsNotAppliedTwice(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: t0}
	st := memstore.New()
	tasks := newFakeTasks()
	tasks.seed(contract.TaskView{TaskRef: "done", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskDone, Revision: 2})
	newRunner := func(m *kt.Model) *scene.KernelRunner {
		host := toolhost.New(toolhost.Ports{Tasks: tasks, Journal: st})
		return &scene.KernelRunner{Model: m, Journal: st, Tools: host.Specs(), Persona: "You help.", Locale: "en", Tenant: "t1",
			Hosts: func(win contract.Window) contract.Host { return host.ForWindow(win, "t1") }}
	}
	hub := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st, Now: clk.now, Runner: newRunner(kt.New())}
	if _, err := hub.Admit(ctx, event("m1", alice, "extend it to Q4")); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Second)
	win, err := st.Claim(ctx, contract.ClaimRequest{Now: clk.now(), LeaseFor: time.Second})
	if err != nil || win == nil {
		t.Fatalf("claim: %+v %v", win, err)
	}
	cont := kt.Call{ID: "c1", Name: "continue_task", Args: map[string]any{"source_ref": "m1", "read_ref": "done@2", "prompt": "add Q4", "reply": "Extending."}}
	first := kt.New(
		kt.Tools(kt.Call{ID: "r1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "done"}}),
		kt.Tools(cont),
	)
	tasks.LoseControl = errors.New("transport: connection reset")
	if _, err := newRunner(first).RunWindow(ctx, *win); err == nil {
		t.Fatal("the lost response fails the wake")
	}
	if len(tasks.controls) != 1 {
		t.Fatalf("the first claim committed once: %v", tasks.controls)
	}

	clk.advance(2 * time.Second)
	again, err := st.Claim(ctx, contract.ClaimRequest{Now: clk.now()})
	if err != nil || again == nil || again.ID != win.ID {
		t.Fatalf("re-claim: %+v %v", again, err)
	}
	second := kt.New(kt.Tools(cont))
	out, err := newRunner(second).RunWindow(ctx, *again)
	if err != nil || out.Decision.Kind != contract.Dispatched || out.Decision.Reply != "Extending." {
		t.Fatalf("replayed continue: %+v %v", out, err)
	}
	if len(tasks.controls) != 1 || tasks.controls[0] != "continue:done:add Q4" {
		t.Fatalf("controls: %v", tasks.controls)
	}
}
