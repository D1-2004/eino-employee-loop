package employeeloop_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/employeeloop"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
)

var (
	bg = context.Background()
	t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func event(id, typ, text string) contract.Event {
	return contract.Event{ID: id, Source: "gw", Type: typ, Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup},
		Actor: contract.Actor{ID: "alice", Kind: contract.ActorHuman, DisplayName: "Alice"}, OccurredAt: t0, Text: text}
}

type printer struct{ sent []contract.Delivery }

func (p *printer) Deliver(_ context.Context, d contract.Delivery) (string, error) {
	p.sent = append(p.sent, d)
	return "p" + d.ID, nil
}

func TestNewRequiresEmployeeAndModel(t *testing.T) {
	if _, err := employeeloop.New(employeeloop.Config{Model: kt.New()}); !errors.Is(err, employeeloop.ErrConfig) {
		t.Fatalf("no employee: %v", err)
	}
	if _, err := employeeloop.New(employeeloop.Config{Employee: "e1"}); !errors.Is(err, employeeloop.ErrConfig) {
		t.Fatalf("no model: %v", err)
	}
	l, err := employeeloop.New(employeeloop.Config{Employee: "e1", Model: kt.New()})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range l.Tools.Specs() {
		if strings.HasSuffix(s.Name, "_task") {
			t.Fatalf("task tools without a task manager: %s", s.Name)
		}
	}
}

func TestLoopRecordsConversationAndFeedsHistory(t *testing.T) {
	clk := &clock{t: t0}
	model := kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "reply", Args: map[string]any{"source_ref": "m2", "reply": "Friday."}}),
		kt.Tools(kt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m3", "reply": "You are welcome."}}),
	)
	l, err := employeeloop.New(employeeloop.Config{Employee: "e1", Tenant: "t1", Locale: "en", Model: model, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []contract.Event{
		event("m1", contract.TypeMessageObserved, "release next week?"),
		event("m2", contract.TypeMessageAddressed, "@bot which day?"),
	} {
		if _, err := l.Publish(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if ack, err := l.Publish(bg, event("m2", contract.TypeMessageAddressed, "@bot which day?")); err != nil || !ack.Duplicate {
		t.Fatalf("replay %+v %v", ack, err)
	}
	p := &printer{}
	w := l.Worker(p)
	clk.t = clk.t.Add(2 * time.Second)
	if _, err := w.StepOnce(bg); err != nil {
		t.Fatal(err)
	}
	if n, err := w.DeliverOnce(bg); err != nil || n != 1 || p.sent[0].Text != "Friday." {
		t.Fatalf("deliver n=%d err=%v sent=%+v", n, err, p.sent)
	}
	// The first wake sees the observed message as history, not its own event.
	first := joined(model.Requests[0])
	if !strings.Contains(first, "Alice: release next week?") || strings.Contains(first, "Alice: @bot which day?") {
		t.Fatalf("history of wake 1:\n%s", first)
	}

	if _, err := l.Publish(bg, event("m3", contract.TypeMessageAddressed, "thanks")); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Second)
	if _, err := w.StepOnce(bg); err != nil {
		t.Fatal(err)
	}
	second := joined(model.Requests[1])
	if !strings.Contains(second, "e1: Friday.") || !strings.Contains(second, "Alice: @bot which day?") {
		t.Fatalf("history of wake 2:\n%s", second)
	}
	log, _ := l.Ports.Conversation.Recent(bg, "t1", "s1", 0, 0)
	if len(log) != 4 || log[2].Role != employeeloop.RoleAssistant || log[2].SourceRef == "" {
		t.Fatalf("conversation %+v", log)
	}
}

func TestWorkerWithoutDelivererKeepsOutboxPending(t *testing.T) {
	model := kt.New(kt.Tools(kt.Call{ID: "c1", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "hi"}}))
	clk := &clock{t: t0}
	l, _ := employeeloop.New(employeeloop.Config{Employee: "e1", Model: model, Now: clk.now, History: -1})
	_, _ = l.Publish(bg, event("m1", contract.TypeMessageAddressed, "hello"))
	clk.t = clk.t.Add(2 * time.Second)
	w := l.Worker(nil)
	if _, err := w.StepOnce(bg); err != nil {
		t.Fatal(err)
	}
	if n, err := w.DeliverOnce(bg); err != nil || n != 0 {
		t.Fatalf("deliver %d %v", n, err)
	}
	if pending, _ := l.Ports.Outbox.Pending(bg, "e1", 10); len(pending) != 1 {
		t.Fatalf("pending %+v", pending)
	}
}

// A scene message admitted between the first claim and the re-claim must
// not change the replayed wake: the wake input is frozen at the first claim,
// so the model is not asked again and the recorded answer is delivered.
func TestReclaimIgnoresSceneMessagesAdmittedAfterTheFirstClaim(t *testing.T) {
	clk := &clock{t: t0}
	model := kt.New(kt.Text("ok"), kt.Text("fresh"))
	l, err := employeeloop.New(employeeloop.Config{Employee: "e1", Tenant: "t1", Locale: "en", Model: model, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Publish(bg, event("m1", contract.TypeMessageAddressed, "@bot status?")); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Second)
	win, err := l.Ports.Queue.Claim(bg, contract.ClaimRequest{Now: clk.now(), LeaseFor: time.Second})
	if err != nil || win == nil {
		t.Fatalf("claim %+v %v", win, err)
	}
	if out, err := l.Hub.Runner.RunWindow(bg, *win); err != nil || out.Decision.Reply != "ok" {
		t.Fatalf("first attempt: %+v %v", out, err)
	}
	// The worker dies before Complete; meanwhile the scene moves on.
	later := event("m2", contract.TypeMessageObserved, "unrelated chatter")
	later.OccurredAt = clk.now()
	if _, err := l.Publish(bg, later); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Second)
	again, err := l.Worker(nil).StepOnce(bg)
	if err != nil || again == nil || again.ID != win.ID {
		t.Fatalf("re-claim %+v %v", again, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("the model was asked again: %d calls", model.Calls())
	}
	pending, _ := l.Ports.Outbox.Pending(bg, "e1", 10)
	if len(pending) != 1 || pending[0].Text != "ok" {
		t.Fatalf("the recorded answer is delivered: %+v", pending)
	}
}

func joined(msgs []*schema.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n")
}

// flakyConversation fails the first Append, as a database might.
type flakyConversation struct {
	contract.Conversation
	failed bool
}

func (f *flakyConversation) Append(ctx context.Context, tenant, scene string, m contract.Msg) (contract.Msg, error) {
	if !f.failed {
		f.failed = true
		return contract.Msg{}, errors.New("conversation store unavailable")
	}
	return f.Conversation.Append(ctx, tenant, scene, m)
}

// The loop runs on injected ports, and a publish retried after a failed
// recording repairs the conversation log exactly once.
func TestRetriedPublishRecordsTheMessageOnce(t *testing.T) {
	st := memstore.New()
	conv := &flakyConversation{Conversation: memstore.NewConversation()}
	l, err := employeeloop.New(employeeloop.Config{Employee: "e1", Tenant: "t1", Model: kt.New(),
		Ports: employeeloop.Ports{Log: st, Queue: st, Journal: st, Outbox: st, Conversation: conv}})
	if err != nil {
		t.Fatal(err)
	}
	e := event("m1", contract.TypeMessageAddressed, "@bot hello")
	if _, err := l.Publish(bg, e); err == nil {
		t.Fatal("the failed recording is reported so the publisher retries")
	}
	ack, err := l.Publish(bg, e)
	if err != nil || !ack.Duplicate {
		t.Fatalf("retry: %+v %v", ack, err)
	}
	if _, err := l.Publish(bg, e); err != nil {
		t.Fatal(err)
	}
	log, _ := conv.Recent(bg, "t1", "s1", 0, 0)
	if len(log) != 1 || log[0].SourceRef != "m1" || log[0].Text != "@bot hello" {
		t.Fatalf("the message is recorded once: %+v", log)
	}
	if r, err := st.Receipt(bg, "e1", ack.ReceiptID); err != nil || r.EventID != "m1" {
		t.Fatalf("the injected event log holds the receipt: %+v %v", r, err)
	}
}
