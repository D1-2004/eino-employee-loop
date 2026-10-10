package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

var bg = context.Background()

func newHub() (*scene.Hub, *memstore.Store) {
	st := memstore.New()
	return &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st}, st
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func event(id, text string) contract.Event {
	return contract.Event{ID: id, Source: "gateway", Type: "message.addressed",
		Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup},
		Actor: contract.Actor{ID: "alice", Kind: contract.ActorHuman}, Text: text,
		OccurredAt: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
}

func TestEventIngressRoundTripAndErrors(t *testing.T) {
	hub, _ := newHub()
	c := httpapi.NewEventSinkClient(serve(t, httpapi.NewEventHandler(hub)) + "/")

	ack, err := c.Publish(bg, event("m1", "hello"))
	if err != nil || ack.ReceiptID == "" || ack.Lane != contract.LaneForeground || ack.WindowID == "" || ack.Duplicate {
		t.Fatalf("ack %+v err %v", ack, err)
	}
	again, err := c.Publish(bg, event("m1", "hello"))
	if err != nil || !again.Duplicate || again.ReceiptID != ack.ReceiptID {
		t.Fatalf("replay %+v err %v", again, err)
	}
	_, err = c.Publish(bg, event("m1", "changed"))
	var re *httpapi.RemoteError
	if !errors.Is(err, contract.ErrConflict) || !errors.As(err, &re) || re.Status != http.StatusConflict || re.Code != "conflict" {
		t.Fatalf("conflict: %v", err)
	}
	cases := map[string]struct {
		mutate func(*contract.Event)
		want   error
	}{
		"invalid":       {func(e *contract.Event) { e.OccurredAt = time.Time{} }, contract.ErrInvalidEvent},
		"unknown type":  {func(e *contract.Event) { e.Type = "nope" }, contract.ErrUnknownType},
		"unknown scene": {func(e *contract.Event) { e.Scene = contract.SceneRef{} }, contract.ErrUnknownScene},
	}
	for name, tc := range cases {
		e := event("bad-"+name, "x")
		tc.mutate(&e)
		_, err := c.Publish(bg, e)
		if !errors.Is(err, tc.want) || !errors.As(err, &re) || re.Status != http.StatusBadRequest {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBearerTokenHealthzAndBodyLimits(t *testing.T) {
	hub, _ := newHub()
	url := serve(t, httpapi.NewEventHandler(hub, httpapi.WithToken("s3cret"), httpapi.WithMaxBody(512)))

	if _, err := httpapi.NewEventSinkClient(url).Publish(bg, event("m1", "x")); !errors.Is(err, httpapi.ErrUnauthorized) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := httpapi.NewEventSinkClient(url, httpapi.WithToken("wrong")).Publish(bg, event("m1", "x")); !errors.Is(err, httpapi.ErrUnauthorized) {
		t.Fatalf("wrong token: %v", err)
	}
	c := httpapi.NewEventSinkClient(url, httpapi.WithToken("s3cret"))
	if _, err := c.Publish(bg, event("m1", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(bg, event("m2", strings.Repeat("x", 1024))); !errors.Is(err, httpapi.ErrTooLarge) {
		t.Fatalf("large body: %v", err)
	}
	resp, err := http.Get(url + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %v %v", resp, err)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, url+"/v1/events", strings.NewReader("{not json"))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body httpapi.ErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusBadRequest || body.Code != "bad_request" || body.Message == "" {
		t.Fatalf("malformed json: %d %+v", resp.StatusCode, body)
	}
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

// ackSink admits into a hub and records acks by event type.
type ackSink struct {
	hub  *scene.Hub
	mu   sync.Mutex
	acks map[string]contract.EventAck
}

func (s *ackSink) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	ack, err := s.hub.Publish(ctx, e)
	if err == nil {
		s.mu.Lock()
		s.acks[e.Type] = ack
		s.mu.Unlock()
	}
	return ack, err
}

func (s *ackSink) ack(typ string) (contract.EventAck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.acks[typ]
	return a, ok
}

// Three services apart: the Task Manager reports to the loop over HTTP and
// the loop controls the Task Manager over HTTP.
func TestTaskManagerOverHTTPReportsToLoopOverHTTP(t *testing.T) {
	hub, st := newHub()
	loop := &ackSink{hub: hub, acks: map[string]contract.EventAck{}}
	loopURL := serve(t, httpapi.NewEventHandler(loop, httpapi.WithToken("loop")))

	exec := executors.FuncExecutor(func(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
		if job.Prompt == "block" && len(job.Corrections) == 0 {
			<-ctx.Done()
			return "", ctx.Err()
		}
		if err := r.Progress(ctx, "half way"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s|%s", job.Prompt, strings.Join(job.Corrections, ",")), nil
	})
	tm := memtask.New(exec, httpapi.NewEventSinkClient(loopURL, httpapi.WithToken("loop")), memtask.WithBackoff(time.Millisecond, 5*time.Millisecond))
	t.Cleanup(func() { _ = tm.Close(bg) })
	tmURL := serve(t, httpapi.NewTaskManagerHandler(tm, httpapi.WithToken("tm")))
	c := httpapi.NewTaskManagerClient(tmURL, httpapi.WithToken("tm"))

	req := contract.DispatchRequest{IdempotencyKey: "k1", Employee: "e1", Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup},
		Requester: contract.Actor{ID: "alice", Kind: contract.ActorHuman}, Goal: "Weekly summary", Prompt: "summarize", Locale: "en"}
	rc, err := c.Dispatch(bg, req)
	if err != nil || !rc.Created {
		t.Fatalf("dispatch %+v err %v", rc, err)
	}
	if again, err := c.Dispatch(bg, req); err != nil || again.Created || again.TaskRef != rc.TaskRef {
		t.Fatalf("replay %+v err %v", again, err)
	}
	waitFor(t, "task done", func() bool {
		v, err := c.Get(bg, "e1", rc.TaskRef)
		return err == nil && v.Status == contract.TaskDone
	})
	v, _ := c.Get(bg, "e1", rc.TaskRef)
	if v.Report != "summarize|" || v.Goal != "Weekly summary" || v.CreatedAt.IsZero() {
		t.Fatalf("view %+v", v)
	}
	if err := tm.Flush(bg); err != nil {
		t.Fatal(err)
	}
	res, ok := loop.ack(taskmgr.EventResult)
	if !ok || res.Lane != contract.LaneBackground {
		t.Fatalf("result ack %+v", res)
	}
	w, err := st.Window(bg, "e1", res.WindowID)
	if err != nil || w.Session.Kind != contract.SessionFollowUp || w.Session.Key != rc.TaskRef {
		t.Fatalf("follow-up window %+v err %v", w, err)
	}

	// Find with filters.
	list, err := c.Find(bg, contract.TaskQuery{Employee: "e1", Scene: "s1", Requester: "alice", Text: "weekly", Limit: 5})
	if err != nil || len(list) != 1 || list[0].TaskRef != rc.TaskRef {
		t.Fatalf("find %+v err %v", list, err)
	}
	if list, err := c.Find(bg, contract.TaskQuery{Employee: "e1", Scene: "nowhere"}); err != nil || len(list) != 0 || list == nil {
		t.Fatalf("empty find %+v err %v", list, err)
	}

	// Error mapping across the wire.
	ctl := contract.TaskControl{IdempotencyKey: "c1", Employee: "e1", Scene: "s1", TaskRef: rc.TaskRef,
		Requester: contract.Actor{ID: "alice", Kind: contract.ActorHuman}, Text: "faster"}
	if _, err := c.Steer(bg, ctl); !errors.Is(err, contract.ErrTaskState) {
		t.Fatalf("steer done task: %v", err)
	}
	bob := ctl
	bob.IdempotencyKey, bob.Requester.ID = "c2", "bob"
	if _, err := c.Stop(bg, bob); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("stop by bob: %v", err)
	}
	if _, err := c.Get(bg, "e1", "task-missing"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if _, err := c.Dispatch(bg, contract.DispatchRequest{Employee: "e1"}); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("invalid dispatch: %v", err)
	}
	if _, err := c.Find(bg, contract.TaskQuery{Employee: "e1"}); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("find without scene: %v", err)
	}

	stale := ctl
	stale.IdempotencyKey, stale.Text, stale.ExpectedRevision = "c2s", "block", 999
	if _, err := c.Continue(bg, stale); !errors.Is(err, contract.ErrStaleRevision) {
		t.Fatalf("stale revision over the wire: %v", err)
	}

	// Continue into a blocking run, steer it, then stop a second task.
	cont := ctl
	cont.IdempotencyKey, cont.Text = "c3", "block"
	cr, err := c.Continue(bg, cont)
	if err != nil || cr.RunRef != rc.TaskRef+".r2" {
		t.Fatalf("continue %+v err %v", cr, err)
	}
	steer := ctl
	steer.IdempotencyKey, steer.Text = "c4", "fix"
	sr, err := c.Steer(bg, steer)
	if err != nil || sr.RunRef != rc.TaskRef+".r3" {
		t.Fatalf("steer %+v err %v", sr, err)
	}
	waitFor(t, "steered run done", func() bool {
		v, _ := c.Get(bg, "e1", rc.TaskRef)
		return v.Status == contract.TaskDone && v.Report == "block|fix"
	})

	req2 := req
	req2.IdempotencyKey, req2.Prompt = "k2", "block"
	rc2, _ := c.Dispatch(bg, req2)
	stop := ctl
	stop.IdempotencyKey, stop.TaskRef, stop.Text = "c5", rc2.TaskRef, ""
	sp, err := c.Stop(bg, stop)
	if err != nil || sp.Status != contract.TaskCancelled {
		t.Fatalf("stop %+v err %v", sp, err)
	}
}

func TestTaskHandlerRejectsMismatchedRefAndBadLimit(t *testing.T) {
	tm := memtask.New(executors.EchoExecutor{}, &ackSink{acks: map[string]contract.EventAck{}, hub: func() *scene.Hub { h, _ := newHub(); return h }()})
	t.Cleanup(func() { _ = tm.Close(bg) })
	url := serve(t, httpapi.NewTaskManagerHandler(tm))

	body := `{"idempotency_key":"k","employee":"e1","scene":"s1","task_ref":"task-a","requester":{"id":"alice","kind":"human"}}`
	resp, err := http.Post(url+"/v1/tasks/task-b/stop", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched ref: %d", resp.StatusCode)
	}
	resp, err = http.Get(url + "/v1/tasks?employee=e1&scene=s1&limit=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", resp.StatusCode)
	}
	if _, err := httpapi.NewTaskManagerClient(url).Stop(bg, contract.TaskControl{}); !errors.Is(err, contract.ErrInvalidRequest) {
		t.Fatalf("client without ref: %v", err)
	}
}

type fakeDeliverer struct {
	got contract.Delivery
	err error
}

func (f *fakeDeliverer) Deliver(_ context.Context, d contract.Delivery) (string, error) {
	f.got = d
	if f.err != nil {
		return "", f.err
	}
	return "pm-" + d.ID, nil
}

func TestDeliveryEgressRoundTripAndErrors(t *testing.T) {
	f := &fakeDeliverer{}
	url := serve(t, httpapi.NewDeliveryHandler(f, httpapi.WithToken("gw")))
	c := httpapi.NewDelivererClient(url, httpapi.WithToken("gw"))

	d := contract.Delivery{ID: "w1/reply", Employee: "e1", Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneDirect, Locator: "conv-9"},
		Session: contract.SessionRef{Scene: "s1", Kind: contract.SessionInteractive}, WindowID: "w1", ReplyTo: "m1", Text: "hi", Locale: "en"}
	id, err := c.Deliver(bg, d)
	if err != nil || id != "pm-w1/reply" || f.got.Scene.Locator != "conv-9" || f.got.Text != "hi" {
		t.Fatalf("deliver %q err %v got %+v", id, err, f.got)
	}
	f.err = fmt.Errorf("%w: rate limited", contract.ErrQuota)
	if _, err := c.Deliver(bg, d); !errors.Is(err, contract.ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
	f.err = errors.New("provider exploded")
	_, err = c.Deliver(bg, d)
	var re *httpapi.RemoteError
	if !errors.Is(err, httpapi.ErrRemote) || !errors.As(err, &re) || re.Status != http.StatusInternalServerError || !strings.Contains(re.Message, "exploded") {
		t.Fatalf("internal: %v", err)
	}
	if _, err := httpapi.NewDelivererClient(url).Deliver(bg, d); !errors.Is(err, httpapi.ErrUnauthorized) || !errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("no token: %v", err)
	}
}

// Only answers proving no send are definite rejections; a status query
// reaches the gateway's reconciler and never sends.
func TestDeliveryRejectionsAndStatus(t *testing.T) {
	f := &reconcilingDeliverer{}
	url := serve(t, httpapi.NewDeliveryHandler(f))
	c := httpapi.NewDelivererClient(url)
	d := contract.Delivery{ID: "w1/reply", Employee: "e1", Text: "hi"}

	f.err = fmt.Errorf("%w: no locator", contract.ErrDeliveryRejected)
	var re *httpapi.RemoteError
	if _, err := c.Deliver(bg, d); !errors.Is(err, contract.ErrDeliveryRejected) || !errors.As(err, &re) || re.Status != http.StatusUnprocessableEntity {
		t.Fatalf("rejected: %v", err)
	}
	f.err = errors.New("provider timeout")
	if _, err := c.Deliver(bg, d); err == nil || errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("a 5xx is ambiguous: %v", err)
	}
	f.err = fmt.Errorf("%w: slow down", contract.ErrQuota)
	if _, err := c.Deliver(bg, d); err == nil || errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("a quota answer is ambiguous unless the deliverer says otherwise: %v", err)
	}
	if _, err := httpapi.NewDelivererClient("http://127.0.0.1:1").Deliver(bg, d); err == nil || errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("a transport error is ambiguous: %v", err)
	}

	f.state, f.id = contract.DeliveryDelivered, "pm-7"
	state, id, err := c.DeliveryStatus(bg, d)
	if err != nil || state != contract.DeliveryDelivered || id != "pm-7" || f.queried != "w1/reply" {
		t.Fatalf("status %s %q %v", state, id, err)
	}
	plain := httpapi.NewDelivererClient(serve(t, httpapi.NewDeliveryHandler(&fakeDeliverer{})))
	if state, _, err := plain.DeliveryStatus(bg, d); err != nil || state != contract.DeliveryUnknown {
		t.Fatalf("a gateway without status answers unknown: %s %v", state, err)
	}
}

type reconcilingDeliverer struct {
	fakeDeliverer
	state   contract.DeliveryState
	id      string
	queried string
}

func (r *reconcilingDeliverer) DeliveryStatus(_ context.Context, d contract.Delivery) (contract.DeliveryState, string, error) {
	r.queried = d.ID
	return r.state, r.id, nil
}
