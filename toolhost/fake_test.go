package toolhost_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// fakeTasks is an in-test Task Manager. It enforces requester and scene
// fences like a real one, deduplicates by idempotency key and, when Sink is
// set, reports results only by publishing events to the loop.
type fakeTasks struct {
	mu         sync.Mutex
	seq        int
	tasks      map[string]*contract.TaskView
	order      []string
	byKey      map[string]contract.TaskReceipt
	dispatches []contract.DispatchRequest
	controls   []string // "op:task_ref:text"
	sceneRefs  map[string]contract.SceneRef
	Sink       contract.EventSink
	Now        func() time.Time
	// Fail, when set, is returned by the next Dispatch instead of a receipt.
	Fail error
	// LoseControl, when set, is returned by the next control after it
	// committed: the effect happened but its response was lost.
	LoseControl error
}

func newFakeTasks() *fakeTasks {
	return &fakeTasks{tasks: map[string]*contract.TaskView{}, byKey: map[string]contract.TaskReceipt{}, sceneRefs: map[string]contract.SceneRef{},
		Now: func() time.Time { return time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC) }}
}

func (f *fakeTasks) Dispatch(_ context.Context, req contract.DispatchRequest) (contract.TaskReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Fail; err != nil {
		f.Fail = nil
		return contract.TaskReceipt{}, err
	}
	if r, ok := f.byKey[req.IdempotencyKey]; ok {
		r.Created = false
		return r, nil
	}
	f.dispatches = append(f.dispatches, req)
	f.seq++
	ref := fmt.Sprintf("task-%d", f.seq)
	f.tasks[ref] = &contract.TaskView{TaskRef: ref, Employee: req.Employee, Scene: req.Scene.ID, Requester: req.Requester.ID,
		SourceRef: req.SourceRef, Goal: req.Goal, Status: contract.TaskRunning, Revision: 1, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.order = append(f.order, ref)
	f.sceneRefs[ref] = req.Scene
	r := contract.TaskReceipt{TaskRef: ref, RunRef: ref + "/run-1", Status: contract.TaskRunning, Created: true, Revision: 1}
	f.byKey[req.IdempotencyKey] = r
	return r, nil
}

// seed inserts a task directly (tests of read and control tools).
func (f *fakeTasks) seed(v contract.TaskView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := v
	f.tasks[v.TaskRef] = &cp
	f.order = append(f.order, v.TaskRef)
}

func (f *fakeTasks) Get(_ context.Context, employee, ref string) (contract.TaskView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[ref]
	if !ok || t.Employee != employee {
		return contract.TaskView{}, contract.ErrNotFound
	}
	return *t, nil
}

func (f *fakeTasks) Find(_ context.Context, q contract.TaskQuery) ([]contract.TaskView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []contract.TaskView
	for i := len(f.order) - 1; i >= 0; i-- {
		t := f.tasks[f.order[i]]
		if t.Employee != q.Employee || t.Scene != q.Scene || (q.Requester != "" && t.Requester != q.Requester) {
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

func (f *fakeTasks) control(op string, c contract.TaskControl, allowed func(status string) bool, next string) (contract.TaskReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A known key answers with its receipt before any precondition: the
	// task may have moved on since the control committed.
	if r, ok := f.byKey[c.IdempotencyKey]; ok {
		return r, nil
	}
	t, ok := f.tasks[c.TaskRef]
	if !ok || t.Employee != c.Employee {
		return contract.TaskReceipt{}, contract.ErrNotFound
	}
	if t.Scene != c.Scene || t.Requester != c.Requester.ID {
		return contract.TaskReceipt{}, contract.ErrForbidden
	}
	if c.ExpectedRevision != 0 && c.ExpectedRevision != t.Revision {
		return contract.TaskReceipt{}, contract.ErrStaleRevision
	}
	if !allowed(t.Status) {
		return contract.TaskReceipt{}, contract.ErrTaskState
	}
	t.Status = next
	t.Revision++
	f.controls = append(f.controls, op+":"+c.TaskRef+":"+c.Text)
	r := contract.TaskReceipt{TaskRef: t.TaskRef, Status: t.Status, Revision: t.Revision}
	f.byKey[c.IdempotencyKey] = r
	if err := f.LoseControl; err != nil {
		f.LoseControl = nil
		return contract.TaskReceipt{}, err
	}
	return r, nil
}

func (f *fakeTasks) Steer(_ context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return f.control("steer", c, func(s string) bool { return s == contract.TaskRunning }, contract.TaskRunning)
}

func (f *fakeTasks) Stop(_ context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return f.control("stop", c, contract.TaskActive, contract.TaskCancelled)
}

func (f *fakeTasks) Continue(_ context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return f.control("continue", c, func(s string) bool { return !contract.TaskActive(s) }, contract.TaskRunning)
}

// finish completes a task and reports it to the loop as an execution.result
// event: the only way the Task Manager talks back.
func (f *fakeTasks) finish(ctx context.Context, ref, report string) (contract.EventAck, error) {
	f.mu.Lock()
	t := f.tasks[ref]
	t.Status, t.Report = contract.TaskDone, report
	t.Revision++
	t.UpdatedAt = f.Now()
	scene := f.sceneRefs[ref]
	f.mu.Unlock()
	return f.Sink.Publish(ctx, contract.Event{
		ID: ref + "/result", Source: "task-manager", Type: "execution.result", Scene: scene,
		Actor:      contract.Actor{ID: "task-manager", Kind: contract.ActorSystem},
		OccurredAt: f.Now(), Refs: map[string]string{contract.RefTask: ref}, Text: report,
	})
}

var _ contract.TaskManager = (*fakeTasks)(nil)
