package dws_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/gateway/dws"
)

type fakeSender struct {
	sent []string
	conv []string
	err  error
}

func (s *fakeSender) Send(_ context.Context, conv, text string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.conv, s.sent = append(s.conv, conv), append(s.sent, text)
	return "sent-" + text, nil
}

func TestDelivererSendsToLocatorAndRecordsOwnMessages(t *testing.T) {
	s := &fakeSender{}
	own := dws.NewRecentIDs(4)
	d := &dws.Deliverer{Sender: s, Own: own}
	del := contract.Delivery{ID: "w1/reply", Scene: contract.SceneRef{ID: "scn_1", Provider: dws.Provider, Locator: "cidG"}, Text: "hi"}
	id, err := d.Deliver(bg, del)
	if err != nil || id != "sent-hi" || s.conv[0] != "cidG" || !own.Has("sent-hi") {
		t.Fatalf("id=%q err=%v sender=%+v", id, err, s)
	}
	noLoc := del
	noLoc.Scene.Locator = ""
	if _, err := d.Deliver(bg, noLoc); !errors.Is(err, dws.ErrNoLocator) || !errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("no locator: %v", err)
	}
	foreign := del
	foreign.Scene.Provider = "slack"
	if _, err := d.Deliver(bg, foreign); !errors.Is(err, dws.ErrForeignScene) || !errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("foreign: %v", err)
	}
	s.err = errors.New("boom")
	other := del
	other.ID = "w2/reply"
	if _, err := d.Deliver(bg, other); err == nil || errors.Is(err, contract.ErrDeliveryRejected) {
		t.Fatalf("send errors surface as ambiguous: %v", err)
	}
	// Status queries answer from what was sent and never send.
	if st, id, err := d.DeliveryStatus(bg, del); err != nil || st != contract.DeliveryDelivered || id != "sent-hi" {
		t.Fatalf("sent delivery: %s %q %v", st, id, err)
	}
	if st, _, err := d.DeliveryStatus(bg, other); err != nil || st != contract.DeliveryUnknown || len(s.sent) != 1 {
		t.Fatalf("unsent delivery: %s %v sends=%d", st, err, len(s.sent))
	}
}

type chanSource struct{ ch chan dws.Message }

func (s chanSource) Subscribe(context.Context, string) (<-chan dws.Message, error) { return s.ch, nil }

type recSink struct {
	mu     sync.Mutex
	events []contract.Event
	fail   []error
}

func (s *recSink) Publish(_ context.Context, e contract.Event) (contract.EventAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fail) > 0 {
		err := s.fail[0]
		s.fail = s.fail[1:]
		return contract.EventAck{}, err
	}
	s.events = append(s.events, e)
	return contract.EventAck{ReceiptID: "r-" + e.ID, Lane: contract.LaneForeground}, nil
}

type recMarker struct{ marked []string }

func (m *recMarker) MarkRead(_ context.Context, _, id string) error {
	m.marked = append(m.marked, id)
	return nil
}

func TestListenerNormalizesPublishesRetriesAndMarksRead(t *testing.T) {
	n, _ := normalizer()
	src := chanSource{ch: make(chan dws.Message, 8)}
	sink := &recSink{fail: []error{errors.New("transient")}}
	marker := &recMarker{}
	l := &dws.Listener{Source: src, Normalizer: n, Sink: sink, Marker: marker, Backoff: time.Millisecond}

	mention := groupMsg("g1", "@Helper weekly report please")
	self := groupMsg("g2", "done")
	self.SenderID = "bot"
	invalid := groupMsg("g3", "x")
	invalid.SenderID = ""
	src.ch <- mention
	src.ch <- self
	src.ch <- invalid
	src.ch <- groupMsg("g4", "lunch?")
	close(src.ch)
	if err := l.Run(bg, "cidG"); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].Type != contract.TypeMessageAddressed || sink.events[1].Type != contract.TypeMessageObserved {
		t.Fatalf("events %+v", sink.events)
	}
	if len(marker.marked) != 2 || marker.marked[0] != "g1" {
		t.Fatalf("marked %v", marker.marked)
	}

	// Permanent rejections are not retried.
	src2 := chanSource{ch: make(chan dws.Message, 1)}
	sink2 := &recSink{fail: []error{contract.ErrUnknownScene, errors.New("must not be reached")}}
	l2 := &dws.Listener{Source: src2, Normalizer: n, Sink: sink2, Backoff: time.Millisecond}
	src2.ch <- groupMsg("g5", "hi")
	close(src2.ch)
	_ = l2.Run(bg, "cidG")
	if len(sink2.fail) != 1 || len(sink2.events) != 0 {
		t.Fatalf("permanent error retried: %+v", sink2)
	}
}
