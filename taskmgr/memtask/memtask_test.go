package memtask_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/taskmgr"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
	"github.com/D1-2004/eino-employee-loop/taskmgr/memtask"
)

// recSink records published events; fail decides per attempt whether to
// fail it.
type recSink struct {
	mu       sync.Mutex
	events   []contract.Event
	attempts int
	fail     func(attempt int, e contract.Event) error
}

func (s *recSink) Publish(_ context.Context, e contract.Event) (contract.EventAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.fail != nil {
		if err := s.fail(s.attempts, e); err != nil {
			return contract.EventAck{}, err
		}
	}
	s.events = append(s.events, e)
	return contract.EventAck{ReceiptID: fmt.Sprintf("r%d", len(s.events))}, nil
}

func (s *recSink) snapshot() []contract.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contract.Event(nil), s.events...)
}

// kinds lists "<kind>@<run suffix>" for events of one task.
func (s *recSink) kinds(task string) []string {
	var out []string
	for _, e := range s.snapshot() {
		if e.Ref(contract.RefTask) != task {
			continue
		}
		parts := strings.Split(e.ID, "/")
		run := parts[1][strings.LastIndex(parts[1], "."):]
		out = append(out, parts[2]+"@"+run)
	}
	return out
}

func (s *recSink) find(id string) (contract.Event, bool) {
	for _, e := range s.snapshot() {
		if e.ID == id {
			return e, true
		}
	}
	return contract.Event{}, false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func newManager(t *testing.T, exec taskmgr.Executor, sink contract.EventSink, opts ...memtask.Option) *memtask.Manager {
	t.Helper()
	opts = append([]memtask.Option{memtask.WithBackoff(time.Millisecond, 5*time.Millisecond)}, opts...)
	m := memtask.New(exec, sink, opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func dispatchReq(key string) contract.DispatchRequest {
	return contract.DispatchRequest{
		IdempotencyKey: key, Employee: "e1", Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup},
		Requester: contract.Actor{ID: "alice", Kind: contract.ActorHuman}, SourceRef: "msg-1",
		Goal: "weekly summary", Prompt: "summarize the week", Locale: "en",
	}
}

func control(ref, key string) contract.TaskControl {
	return contract.TaskControl{IdempotencyKey: key, Employee: "e1", Scene: "s1", TaskRef: ref,
		Requester: contract.Actor{ID: "alice", Kind: contract.ActorHuman}}
}

func status(t *testing.T, m *memtask.Manager, ref string) func() bool {
	return func() bool {
		v, err := m.Get(context.Background(), "e1", ref)
		return err == nil && !contract.TaskActive(v.Status)
	}
}

func payload(t *testing.T, e contract.Event) taskmgr.ExecutionPayload {
	t.Helper()
	var p taskmgr.ExecutionPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDispatchPublishesLifecycleAndResult(t *testing.T) {
	sink := &recSink{}
	m := newManager(t, executors.EchoExecutor{}, sink)
	rc, err := m.Dispatch(context.Background(), dispatchReq("k1"))
	if err != nil || !rc.Created || rc.Status != contract.TaskQueued || rc.RunRef != rc.TaskRef+".r1" {
		t.Fatalf("receipt %+v err %v", rc, err)
	}
	waitFor(t, "task done", status(t, m, rc.TaskRef))
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted@.r1", "started@.r1", "progress-1@.r1", "result@.r1", "terminal@.r1"}
	if got := sink.kinds(rc.TaskRef); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	res, _ := sink.find(rc.TaskRef + "/" + rc.RunRef + "/result")
	wantText := executors.Messages().Text("en", "taskmgr.echo.report", "goal", "weekly summary", "prompt", "summarize the week")
	if res.Type != taskmgr.EventResult || res.Text != wantText || res.Source != taskmgr.Source ||
		res.Actor.Kind != contract.ActorSystem || res.Ref(contract.RefRun) != rc.RunRef || res.Scene.ID != "s1" {
		t.Fatalf("bad result event %+v", res)
	}
	term, _ := sink.find(rc.TaskRef + "/" + rc.RunRef + "/terminal")
	if p := payload(t, term); p.TaskStatus != contract.TaskDone || p.RunStatus != taskmgr.RunDone || p.Reason != taskmgr.ReasonCompleted {
		t.Fatalf("terminal payload %+v", p)
	}
	v, _ := m.Get(context.Background(), "e1", rc.TaskRef)
	if v.Status != contract.TaskDone || v.Report != wantText || v.Revision != 3 || v.Requester != "alice" {
		t.Fatalf("view %+v", v)
	}
	if _, err := m.Get(context.Background(), "e2", rc.TaskRef); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("other employee must not see the task: %v", err)
	}
}

func TestDispatchIsIdempotent(t *testing.T) {
	sink := &recSink{}
	m := newManager(t, executors.EchoExecutor{}, sink)
	a, err := m.Dispatch(context.Background(), dispatchReq("k1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Dispatch(context.Background(), dispatchReq("k1"))
	if err != nil || b.Created || b.TaskRef != a.TaskRef {
		t.Fatalf("replay %+v err %v", b, err)
	}
	other := dispatchReq("k1")
	other.Goal = "something else"
	if _, err := m.Dispatch(context.Background(), other); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("reused key must conflict: %v", err)
	}
	waitFor(t, "task done", status(t, m, a.TaskRef))
	_ = m.Flush(context.Background())
	n := 0
	for _, k := range sink.kinds(a.TaskRef) {
		if strings.HasPrefix(k, "accepted") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("one dispatch must be accepted once, got %d", n)
	}
	bad := dispatchReq("")
	if _, err := m.Dispatch(context.Background(), bad); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("missing key: %v", err)
	}
}

// blocker blocks every run until its context ends, unless done reports true
// for the job.
type blocker struct {
	mu      sync.Mutex
	jobs    []taskmgr.Job
	stopped []string
	done    func(taskmgr.Job) bool
}

func (b *blocker) exec() executors.FuncExecutor {
	return func(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
		b.mu.Lock()
		b.jobs = append(b.jobs, job)
		b.mu.Unlock()
		if b.done != nil && b.done(job) {
			return "report:" + strings.Join(job.Corrections, ","), nil
		}
		<-ctx.Done()
		b.mu.Lock()
		b.stopped = append(b.stopped, job.RunRef)
		b.mu.Unlock()
		if err := r.Progress(context.Background(), "late"); !errors.Is(err, contract.ErrTaskState) {
			return "", fmt.Errorf("late progress must be rejected, got %v", err)
		}
		return "", ctx.Err()
	}
}

func (b *blocker) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.jobs) }

func (b *blocker) stoppedRuns() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.stopped...)
}

func TestStopCancelsRunAndReportsCancelled(t *testing.T) {
	sink := &recSink{}
	b := &blocker{}
	m := newManager(t, b.exec(), sink)
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "run started", func() bool { return b.count() == 1 })

	st, err := m.Stop(context.Background(), control(rc.TaskRef, "stop-1"))
	if err != nil || st.Status != contract.TaskCancelled {
		t.Fatalf("stop %+v err %v", st, err)
	}
	waitFor(t, "executor cancelled", func() bool { return len(b.stoppedRuns()) == 1 })
	_ = m.Flush(context.Background())
	want := []string{"accepted@.r1", "started@.r1", "terminal@.r1"}
	if got := sink.kinds(rc.TaskRef); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	term, _ := sink.find(rc.TaskRef + "/" + rc.RunRef + "/terminal")
	if p := payload(t, term); p.TaskStatus != contract.TaskCancelled || p.RunStatus != taskmgr.RunCancelled || p.Reason != taskmgr.ReasonStopped {
		t.Fatalf("terminal payload %+v", p)
	}
	if again, err := m.Stop(context.Background(), control(rc.TaskRef, "stop-1")); err != nil || again != st {
		t.Fatalf("replayed stop %+v err %v", again, err)
	}
	if _, err := m.Stop(context.Background(), control(rc.TaskRef, "stop-2")); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("stopping a cancelled task: %v", err)
	}
}

func TestSteerRestartsWithAccumulatedCorrections(t *testing.T) {
	sink := &recSink{}
	b := &blocker{done: func(j taskmgr.Job) bool { return len(j.Corrections) == 2 }}
	m := newManager(t, b.exec(), sink)
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "run 1", func() bool { return b.count() == 1 })

	c1 := control(rc.TaskRef, "steer-1")
	c1.Text = "only the first week"
	s1, err := m.Steer(context.Background(), c1)
	if err != nil || s1.RunRef != rc.TaskRef+".r2" || s1.Revision <= rc.Revision {
		t.Fatalf("steer %+v err %v", s1, err)
	}
	waitFor(t, "run 2", func() bool { return b.count() == 2 })
	c2 := control(rc.TaskRef, "steer-2")
	c2.Text = "add a table"
	if _, err := m.Steer(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task done", status(t, m, rc.TaskRef))
	_ = m.Flush(context.Background())

	b.mu.Lock()
	last := b.jobs[len(b.jobs)-1]
	b.mu.Unlock()
	if !reflect.DeepEqual(last.Corrections, []string{"only the first week", "add a table"}) || last.RunRef != rc.TaskRef+".r3" {
		t.Fatalf("last job %+v", last)
	}
	if got := b.stoppedRuns(); !reflect.DeepEqual(got, []string{rc.TaskRef + ".r1", rc.TaskRef + ".r2"}) {
		t.Fatalf("superseded runs %v", got)
	}
	want := []string{
		"accepted@.r1", "started@.r1",
		"terminal@.r1", "steered@.r2", "accepted@.r2", "started@.r2",
		"terminal@.r2", "steered@.r3", "accepted@.r3", "started@.r3", "result@.r3", "terminal@.r3",
	}
	if got := sink.kinds(rc.TaskRef); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v\nwant %v", got, want)
	}
	old, _ := sink.find(rc.TaskRef + "/" + rc.TaskRef + ".r1/terminal")
	if p := payload(t, old); p.RunStatus != taskmgr.RunSuperseded || p.Reason != taskmgr.ReasonSteered || p.TaskStatus == contract.TaskCancelled {
		t.Fatalf("superseded terminal %+v", p)
	}
	steered, _ := sink.find(rc.TaskRef + "/" + rc.TaskRef + ".r2/steered")
	if steered.Text != "only the first week" {
		t.Fatalf("steered text %q", steered.Text)
	}
	v, _ := m.Get(context.Background(), "e1", rc.TaskRef)
	if v.Status != contract.TaskDone || v.Report != "report:only the first week,add a table" {
		t.Fatalf("view %+v", v)
	}
}

func TestContinueStartsNewRunOnDoneTask(t *testing.T) {
	sink := &recSink{}
	b := &blocker{}
	m := newManager(t, executors.FuncExecutor(func(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
		if job.Prompt == "block" {
			return b.exec()(ctx, job, r)
		}
		return "did:" + job.Prompt, nil
	}), sink)
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "task done", status(t, m, rc.TaskRef))

	c := control(rc.TaskRef, "cont-1")
	c.Text = "block"
	cr, err := m.Continue(context.Background(), c)
	if err != nil || cr.RunRef != rc.TaskRef+".r2" || cr.Status != contract.TaskQueued {
		t.Fatalf("continue %+v err %v", cr, err)
	}
	waitFor(t, "run 2 blocking", func() bool { return b.count() == 1 })
	if _, err := m.Continue(context.Background(), control(rc.TaskRef, "cont-2")); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("continue on a running task: %v", err)
	}
	if _, err := m.Stop(context.Background(), control(rc.TaskRef, "stop")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(context.Background(), control(rc.TaskRef, "cont-3")); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("continue on a cancelled task: %v", err)
	}

	rc2, _ := m.Dispatch(context.Background(), dispatchReq("k2"))
	waitFor(t, "task 2 done", status(t, m, rc2.TaskRef))
	c = control(rc2.TaskRef, "cont-4")
	c.Text = "now the next month"
	if _, err := m.Continue(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task 2 continued", func() bool {
		v, _ := m.Get(context.Background(), "e1", rc2.TaskRef)
		return v.Status == contract.TaskDone && v.Report == "did:now the next month"
	})
	_ = m.Flush(context.Background())
	acc, _ := sink.find(rc2.TaskRef + "/" + rc2.TaskRef + ".r2/accepted")
	if p := payload(t, acc); p.Reason != taskmgr.ReasonContinued {
		t.Fatalf("continued accepted %+v", p)
	}
}

func TestControlsEnforceRequesterSceneAndState(t *testing.T) {
	m := newManager(t, executors.EchoExecutor{}, &recSink{})
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "task done", status(t, m, rc.TaskRef))

	other := control(rc.TaskRef, "x1")
	other.Requester.ID = "bob"
	if _, err := m.Stop(context.Background(), other); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("other requester: %v", err)
	}
	cross := control(rc.TaskRef, "x2")
	cross.Scene = "s2"
	if _, err := m.Continue(context.Background(), cross); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("other scene: %v", err)
	}
	foreign := control(rc.TaskRef, "x3")
	foreign.Employee = "e2"
	if _, err := m.Stop(context.Background(), foreign); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("other employee: %v", err)
	}
	steer := control(rc.TaskRef, "x4")
	steer.Text = "faster"
	if _, err := m.Steer(context.Background(), steer); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("steer a done task: %v", err)
	}
	if _, err := m.Steer(context.Background(), control(rc.TaskRef, "x5")); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("steer without text: %v", err)
	}
	ok := control(rc.TaskRef, "x6")
	if _, err := m.Continue(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	reused := control(rc.TaskRef, "x6")
	reused.Text = "different"
	if _, err := m.Continue(context.Background(), reused); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("reused control key: %v", err)
	}
}

func TestFindFiltersNewestFirst(t *testing.T) {
	m := newManager(t, executors.EchoExecutor{}, &recSink{})
	for i, goal := range []string{"Weekly summary", "budget", "weekly digest"} {
		req := dispatchReq(fmt.Sprintf("k%d", i))
		req.Goal = goal
		if i == 1 {
			req.Requester.ID = "bob"
		}
		if _, err := m.Dispatch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Find(context.Background(), contract.TaskQuery{Employee: "e1", Scene: "s1", Text: "WEEKLY"})
	if err != nil || len(got) != 2 || got[0].Goal != "weekly digest" || got[1].Goal != "Weekly summary" {
		t.Fatalf("find %+v err %v", got, err)
	}
	got, _ = m.Find(context.Background(), contract.TaskQuery{Employee: "e1", Scene: "s1", Requester: "bob"})
	if len(got) != 1 || got[0].Goal != "budget" {
		t.Fatalf("by requester %+v", got)
	}
	got, _ = m.Find(context.Background(), contract.TaskQuery{Employee: "e1", Scene: "s1", Limit: 1})
	if len(got) != 1 {
		t.Fatalf("limit %+v", got)
	}
	if _, err := m.Find(context.Background(), contract.TaskQuery{Employee: "e1"}); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("find without scene: %v", err)
	}
}

func TestPublishRetriesInOrderAndSkipsPermanentRejections(t *testing.T) {
	transient := errors.New("sink down")
	sink := &recSink{fail: func(attempt int, e contract.Event) error {
		if attempt <= 3 {
			return transient
		}
		if strings.HasSuffix(e.ID, "/progress-1") {
			return fmt.Errorf("%w: test", contract.ErrInvalidEvent)
		}
		return nil
	}}
	var mu sync.Mutex
	var rejected []string
	m := newManager(t, executors.EchoExecutor{}, sink, memtask.WithRejectHandler(func(e contract.Event, err error) {
		mu.Lock()
		defer mu.Unlock()
		rejected = append(rejected, e.ID+":"+fmt.Sprint(errors.Is(err, contract.ErrInvalidEvent)))
	}))
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "task done", status(t, m, rc.TaskRef))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Flush(ctx); err != nil || m.Pending() != 0 {
		t.Fatalf("flush err %v pending %d", err, m.Pending())
	}
	want := []string{"accepted@.r1", "started@.r1", "result@.r1", "terminal@.r1"}
	if got := sink.kinds(rc.TaskRef); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rejected) != 1 || rejected[0] != rc.TaskRef+"/"+rc.RunRef+"/progress-1:true" {
		t.Fatalf("rejected %v", rejected)
	}
}

func TestCloseWaitsThenCancelsAtDeadline(t *testing.T) {
	sink := &recSink{}
	m := memtask.New(executors.EchoExecutor{}, sink)
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sink.kinds(rc.TaskRef); len(got) != 5 || got[4] != "terminal@.r1" {
		t.Fatalf("graceful close must finish and publish the run: %v", got)
	}
	if _, err := m.Dispatch(context.Background(), dispatchReq("k2")); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatalf("dispatch after close: %v", err)
	}

	b := &blocker{}
	m2 := memtask.New(b.exec(), sink)
	rc2, _ := m2.Dispatch(context.Background(), dispatchReq("k3"))
	waitFor(t, "run started", func() bool { return b.count() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m2.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close at deadline: %v", err)
	}
	v, _ := m2.Get(context.Background(), "e1", rc2.TaskRef)
	if v.Status != contract.TaskCancelled {
		t.Fatalf("shutdown must cancel the run: %+v", v)
	}
}

// The events a Task Manager publishes must be admitted by the loop: host
// facts on the host lane, results on the task's follow-up session.
func TestEventsAreAdmittedByTheLoop(t *testing.T) {
	st := memstore.New()
	hub := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st}
	var mu sync.Mutex
	lanes := map[string]contract.EventAck{}
	sink := sinkFunc(func(ctx context.Context, e contract.Event) (contract.EventAck, error) {
		ack, err := hub.Publish(ctx, e)
		if err == nil {
			mu.Lock()
			lanes[e.Type] = ack
			mu.Unlock()
		}
		return ack, err
	})
	var rejected []error
	m := newManager(t, executors.EchoExecutor{}, sink, memtask.WithRejectHandler(func(_ contract.Event, err error) { rejected = append(rejected, err) }))
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "task done", status(t, m, rc.TaskRef))
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rejected) > 0 {
		t.Fatalf("loop rejected events: %v", rejected)
	}
	mu.Lock()
	defer mu.Unlock()
	for typ, lane := range map[string]contract.Lane{
		taskmgr.EventAccepted: contract.LaneHost, taskmgr.EventStarted: contract.LaneHost,
		taskmgr.EventProgress: contract.LaneBackground, taskmgr.EventResult: contract.LaneBackground,
		taskmgr.EventTerminal: contract.LaneHost,
	} {
		if ack, ok := lanes[typ]; !ok || ack.Lane != lane {
			t.Errorf("%s: ack %+v", typ, ack)
		}
	}
	if lanes[taskmgr.EventResult].WindowID == "" {
		t.Fatal("result must open a follow-up window")
	}
	w, err := st.Window(context.Background(), "e1", lanes[taskmgr.EventResult].WindowID)
	if err != nil || w.Session.Kind != contract.SessionFollowUp || w.Session.Key != rc.TaskRef {
		t.Fatalf("window %+v err %v", w, err)
	}
}

type sinkFunc func(context.Context, contract.Event) (contract.EventAck, error)

func (f sinkFunc) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	return f(ctx, e)
}

// One Task Manager serves several loops: every event names its employee,
// and a contract.SinkResolver routes it to that employee's loop.
func TestOneManagerReportsToTheLoopOfEachEmployee(t *testing.T) {
	employees := []string{"e1", "e2"}
	stores := map[string]*memstore.Store{}
	var mu sync.Mutex
	got := map[string][]contract.Event{}
	loops := map[string]contract.EventSink{}
	for _, emp := range employees {
		st := memstore.New()
		stores[emp] = st
		hub := &scene.Hub{Employee: emp, Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st}
		loops[emp] = sinkFunc(func(ctx context.Context, e contract.Event) (contract.EventAck, error) {
			ack, err := hub.Publish(ctx, e)
			if err == nil {
				mu.Lock()
				got[emp] = append(got[emp], e)
				mu.Unlock()
			}
			return ack, err
		})
	}
	route := contract.SinkResolver(func(employee string) (contract.EventSink, error) {
		if s, ok := loops[employee]; ok {
			return s, nil
		}
		return nil, contract.ErrUnknownEmployee
	})
	var rejected []error
	m := newManager(t, executors.EchoExecutor{}, route, memtask.WithRejectHandler(func(_ contract.Event, err error) {
		mu.Lock()
		rejected = append(rejected, err)
		mu.Unlock()
	}))
	refs := map[string]string{}
	for _, emp := range employees {
		req := dispatchReq("k1")
		req.Employee = emp
		rc, err := m.Dispatch(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		refs[emp] = rc.TaskRef
		waitFor(t, "task done", func() bool {
			v, err := m.Get(context.Background(), emp, rc.TaskRef)
			return err == nil && !contract.TaskActive(v.Status)
		})
	}
	if err := m.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rejected) > 0 {
		t.Fatalf("rejected: %v", rejected)
	}
	for _, emp := range employees {
		if len(got[emp]) == 0 {
			t.Fatalf("%s received no events", emp)
		}
		for _, e := range got[emp] {
			if e.Employee != emp || e.Ref(contract.RefTask) != refs[emp] {
				t.Fatalf("%s received %s for %s (task %s)", emp, e.ID, e.Employee, e.Ref(contract.RefTask))
			}
		}
	}
	if _, err := route.Publish(context.Background(), contract.Event{ID: "x", Employee: "e9"}); !errors.Is(err, contract.ErrUnknownEmployee) {
		t.Fatalf("an unknown employee is a permanent rejection: %v", err)
	}
	if _, err := route.Publish(context.Background(), contract.Event{ID: "x"}); !errors.Is(err, contract.ErrInvalidEvent) {
		t.Fatalf("an event without an employee cannot be routed: %v", err)
	}
}

// A control fenced to a read revision applies only at that revision, and a
// replay of its key answers with the stored receipt even after the commit
// moved the task on: the key is checked before the revision and the state.
func TestControlKeyIsCheckedBeforeRevisionAndState(t *testing.T) {
	m := newManager(t, executors.EchoExecutor{}, &recSink{})
	rc, _ := m.Dispatch(context.Background(), dispatchReq("k1"))
	waitFor(t, "task done", status(t, m, rc.TaskRef))
	read, _ := m.Get(context.Background(), "e1", rc.TaskRef)

	stale := control(rc.TaskRef, "c0")
	stale.ExpectedRevision = read.Revision - 1
	if _, err := m.Continue(context.Background(), stale); !errors.Is(err, contract.ErrStaleRevision) {
		t.Fatalf("stale revision: %v", err)
	}
	c := control(rc.TaskRef, "c1")
	c.Text = "again"
	c.ExpectedRevision = read.Revision
	first, err := m.Continue(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "continued run done", status(t, m, rc.TaskRef))
	again, err := m.Continue(context.Background(), c)
	if err != nil || again != first {
		t.Fatalf("replay after the commit: %+v %v (first %+v)", again, err, first)
	}
	if v, _ := m.Get(context.Background(), "e1", rc.TaskRef); v.Status != contract.TaskDone || v.Revision <= read.Revision {
		t.Fatalf("view %+v", v)
	}
	other := control(rc.TaskRef, "c2")
	other.Text = "again"
	other.ExpectedRevision = read.Revision
	if _, err := m.Continue(context.Background(), other); !errors.Is(err, contract.ErrStaleRevision) {
		t.Fatalf("a new key at the old revision: %v", err)
	}
	if _, err := m.Stop(context.Background(), control(rc.TaskRef, "s1")); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("stop a done task: %v", err)
	}
}
