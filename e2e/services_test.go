// Package e2e_test runs the three services apart, joined only by HTTP and
// contract types, with a scripted model: the DWS Event Gateway (normalizer
// in, deliverer out), the Employee Loop and the Task Manager.
package e2e_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/employeeloop"
	"github.com/D1-2004/eino-employee-loop/gateway/dws"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/taskmgr"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
	"github.com/D1-2004/eino-employee-loop/taskmgr/memtask"
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

const (
	token    = "shared-secret"
	employee = "e1"
	groupCID = "cidGroup"
	botID    = "bot"
)

// fakeIM stands in for the provider behind the gateway's dws Deliverer.
type fakeIM struct {
	mu   sync.Mutex
	sent []sent
}

type sent struct{ conversation, text string }

func (f *fakeIM) Send(_ context.Context, conv, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{conv, text})
	return "im-" + string(rune('a'+len(f.sent)-1)), nil
}

func (f *fakeIM) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

// ingress records what reaches the loop over HTTP. An event is recorded
// before it is admitted, so a wake it causes always finds it.
type ingress struct {
	mu     sync.Mutex
	next   contract.EventSink
	events []contract.Event
	acks   map[string]contract.EventAck
}

func (s *ingress) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	ack, err := s.next.Publish(ctx, e)
	if err == nil {
		s.mu.Lock()
		s.acks[e.Source+"|"+e.ID] = ack
		s.mu.Unlock()
	}
	return ack, err
}

// last returns the last event of a type and its ack, once admitted.
func (s *ingress) last(typ string) (contract.Event, contract.EventAck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		if e := s.events[i]; e.Type == typ {
			ack, ok := s.acks[e.Source+"|"+e.ID]
			return e, ack, ok
		}
	}
	return contract.Event{}, contract.EventAck{}, false
}

// chanSource feeds provider messages to the gateway's Listener.
type chanSource struct{ ch chan dws.Message }

func (c chanSource) Subscribe(context.Context, string) (<-chan dws.Message, error) { return c.ch, nil }

type env struct {
	loop    *employeeloop.Loop
	store   *memstore.Store
	model   *kt.Model
	im      *fakeIM
	in      *ingress
	feed    chan dws.Message
	tasks   *httpapi.TaskManagerClient
	scenes  dws.SceneResolver
	started time.Time
}

// start wires the three services over httptest servers. Each boundary
// requires the shared bearer token.
func start(t *testing.T, exec taskmgr.Executor, steps ...kt.Step) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	auth := httpapi.WithToken(token)
	e := &env{model: kt.New(steps...), im: &fakeIM{}, in: &ingress{acks: map[string]contract.EventAck{}}, feed: make(chan dws.Message, 8),
		scenes: dws.HashSceneResolver{Namespace: "e2e", Tenant: "t1"}, started: time.Now()}

	// Event Gateway: delivery API in front of the provider.
	own := dws.NewRecentIDs(0)
	gatewaySrv := httptest.NewServer(httpapi.NewDeliveryHandler(&dws.Deliverer{Sender: e.im, Own: own}, auth))
	// Employee Loop ingress (bound to the loop below, before any event).
	loopSrv := httptest.NewServer(httpapi.NewEventHandler(e.in, auth))
	// Task Manager: reports only by publishing to the loop's ingress.
	tm := memtask.New(exec, httpapi.NewEventSinkClient(loopSrv.URL, auth), memtask.WithBackoff(time.Millisecond, 10*time.Millisecond))
	tmSrv := httptest.NewServer(httpapi.NewTaskManagerHandler(tm, auth))
	e.tasks = httpapi.NewTaskManagerClient(tmSrv.URL, auth)

	e.store = memstore.New()
	loop, err := employeeloop.New(employeeloop.Config{Employee: employee, Tenant: "t1", Locale: "en", Model: e.model, Tasks: e.tasks,
		Ports: employeeloop.Ports{Log: e.store, Queue: e.store, Journal: e.store, Outbox: e.store}})
	if err != nil {
		t.Fatal(err)
	}
	e.loop, e.in.next = loop, loop
	worker := loop.Worker(httpapi.NewDelivererClient(gatewaySrv.URL, auth))
	worker.Idle = 5 * time.Millisecond
	worker.OnError = func(err error) { t.Errorf("worker: %v", err) }

	listener := &dws.Listener{
		Source: chanSource{e.feed}, Sink: httpapi.NewEventSinkClient(loopSrv.URL, auth), Backoff: time.Millisecond,
		Normalizer: &dws.Normalizer{Scenes: e.scenes, Self: dws.Identity{IDs: []string{botID}, Names: []string{"Helper"}}, Own: own, Locale: "en"},
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = worker.Run(ctx) }()
	go func() { defer wg.Done(); _ = listener.Run(ctx, groupCID) }()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer ccancel()
		_ = tm.Close(cctx)
		gatewaySrv.Close()
		loopSrv.Close()
		tmSrv.Close()
	})
	return e
}

func (e *env) say(id, text string, mention bool) {
	m := dws.Message{ConversationID: groupCID, MessageID: id, SenderID: "alice", SenderName: "Alice",
		Text: text, AtMs: time.Now().UnixMilli(), Kind: dws.MessageText}
	if mention {
		m.MentionIDs = []string{botID}
	}
	e.feed <- m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A person addresses the employee in a group; the loop dispatches over
// HTTP, the Task Manager reports the result over HTTP, a follow-up wake
// reports it, and both replies reach the gateway.
func TestAddressedRequestIsDispatchedAndReportedAcrossServices(t *testing.T) {
	exec := executors.FuncExecutor(func(_ context.Context, job taskmgr.Job, _ taskmgr.Reporter) (string, error) {
		return "42 rows for " + job.Goal, nil
	})
	var e *env
	e = start(t, exec,
		kt.Tools(kt.Call{ID: "d1", Name: "dispatch_task", Args: map[string]any{
			"source_ref": "msg-1", "goal": "weekly report", "prompt": "build the weekly report", "reply": "On it."}}),
		kt.Func(func([]*schema.Message) kt.Step {
			res, _, _ := e.in.last(taskmgr.EventResult)
			return kt.Tools(kt.Call{ID: "r1", Name: "reply", Args: map[string]any{
				"source_ref": res.ID, "reply": "The weekly report is ready: " + res.Text}})
		}),
	)
	e.say("msg-1", "@Helper please build the weekly report", true)

	waitFor(t, "two deliveries at the gateway", func() bool { return len(e.im.messages()) >= 2 })
	got := e.im.messages()
	if len(got) != 2 || got[0] != (sent{groupCID, "On it."}) || got[1] != (sent{groupCID, "The weekly report is ready: 42 rows for weekly report"}) {
		t.Fatalf("gateway received %+v", got)
	}

	addressed, _, ok := e.in.last(contract.TypeMessageAddressed)
	if !ok || addressed.Scene.ID == groupCID || addressed.Scene.Locator != groupCID || addressed.Actor.ID != "alice" {
		t.Fatalf("addressed event %+v", addressed)
	}
	res, resAck, ok := e.in.last(taskmgr.EventResult)
	if !ok || res.Source != taskmgr.Source || res.Scene.ID != addressed.Scene.ID || resAck.Lane != contract.LaneBackground {
		t.Fatalf("result event %+v ack %+v", res, resAck)
	}
	taskRef := res.Ref(contract.RefTask)
	view, err := e.tasks.Get(context.Background(), employee, taskRef)
	if err != nil || view.Status != contract.TaskDone || view.Requester != "alice" || view.SourceRef != "msg-1" || view.Scene != addressed.Scene.ID {
		t.Fatalf("task view %+v err %v", view, err)
	}
	follow, err := e.store.Window(context.Background(), employee, resAck.WindowID)
	if err != nil || follow.Session.Kind != contract.SessionFollowUp || follow.Session.Key != taskRef || follow.State != contract.WindowDone {
		t.Fatalf("follow-up window %+v err %v", follow, err)
	}
	// Exactly two deliveries: the dispatch acknowledgement of the
	// interactive window and the report of the follow-up window.
	deliveries := e.store.Deliveries()
	if len(deliveries) != 2 || deliveries[0].WindowID == deliveries[1].WindowID {
		t.Fatalf("deliveries %+v", deliveries)
	}
	for _, d := range deliveries {
		if d.State != contract.DeliveryDelivered || d.ProviderID == "" {
			t.Fatalf("delivery not confirmed: %+v", d)
		}
		if d.Text == "On it." && (d.ReplyTo != "msg-1" || d.Session.Kind != contract.SessionInteractive) {
			t.Fatalf("acknowledgement delivery %+v", d)
		}
	}
	if e.model.Calls() != 2 {
		t.Fatalf("model calls: %d", e.model.Calls())
	}
	// The conversation log holds the request and both delivered replies.
	log, _ := e.loop.Ports.Conversation.Recent(context.Background(), "t1", addressed.Scene.ID, 0, 10)
	if len(log) != 3 || log[0].Role != employeeloop.RoleUser || log[1].Text != "On it." || log[2].Role != employeeloop.RoleAssistant {
		t.Fatalf("conversation %+v", log)
	}
}

// An unaddressed group message is only observed; a direct address in the
// same scene preempts the observation window before it ever wakes, and the
// observed message is still visible to the foreground wake as history.
func TestObservedMessageIsPreemptedByDirectAddress(t *testing.T) {
	never := executors.FuncExecutor(func(context.Context, taskmgr.Job, taskmgr.Reporter) (string, error) {
		t.Error("no task expected")
		return "", nil
	})
	e := start(t, never,
		kt.Tools(kt.Call{ID: "a1", Name: "reply", Args: map[string]any{"source_ref": "msg-3", "reply": "The release is on Friday."}}),
	)
	e.say("msg-2", "does anyone know the release date?", false)
	var obsAck contract.EventAck
	waitFor(t, "observed admission", func() bool {
		var ok bool
		_, obsAck, ok = e.in.last(contract.TypeMessageObserved)
		return ok
	})
	if obsAck.Lane != contract.LaneBackground || obsAck.WindowID == "" {
		t.Fatalf("observed ack %+v", obsAck)
	}
	e.say("msg-3", "@Helper when is the release?", true)

	waitFor(t, "one delivery", func() bool { return len(e.im.messages()) >= 1 })
	if got := e.im.messages(); len(got) != 1 || got[0] != (sent{groupCID, "The release is on Friday."}) {
		t.Fatalf("gateway received %+v", got)
	}
	obs, err := e.store.Window(context.Background(), employee, obsAck.WindowID)
	if err != nil || obs.State != contract.WindowCancelled || obs.Reason != contract.CancelForegroundAdmission {
		t.Fatalf("observation window %+v err %v", obs, err)
	}
	if e.model.Calls() != 1 {
		t.Fatalf("only the foreground wakes, model calls: %d", e.model.Calls())
	}
	seen := false
	for _, m := range e.model.Requests[0] {
		if strings.Contains(m.Content, "does anyone know the release date?") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the observed message is history of the foreground wake")
	}
}
