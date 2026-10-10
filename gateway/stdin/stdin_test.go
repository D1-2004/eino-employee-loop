package stdin_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/gateway/stdin"
)

type sink struct {
	events []contract.Event
	fail   error
}

func (s *sink) Publish(_ context.Context, e contract.Event) (contract.EventAck, error) {
	if s.fail != nil {
		return contract.EventAck{}, s.fail
	}
	s.events = append(s.events, e)
	return contract.EventAck{ReceiptID: "r", Lane: contract.LaneForeground}, nil
}

func TestLinesBecomeAddressedEventsUntilQuit(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	g := stdin.New(stdin.Config{
		In: strings.NewReader("hello\n\n  build the report  \n/quit\nignored\n"), Out: &out,
		Scene: contract.SceneRef{ID: "local"}, Actor: contract.Actor{ID: "me", DisplayName: "Me"},
		Locale: "en", Prompt: true, Now: func() time.Time { return at },
	})
	s := &sink{}
	if err := g.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(s.events) != 2 || s.events[1].Text != "build the report" || s.events[0].ID == s.events[1].ID {
		t.Fatalf("events %+v", s.events)
	}
	cat := contract.StandardCatalog()
	for _, e := range s.events {
		if e.Type != contract.TypeMessageAddressed || e.Actor.Kind != contract.ActorHuman || e.Scene.Kind != contract.SceneDirect ||
			e.Scene.Provider != stdin.Provider || !e.OccurredAt.Equal(at) || e.Locale != "en" {
			t.Fatalf("event %+v", e)
		}
		if _, err := cat.Route(e); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(out.String(), "> ") {
		t.Fatalf("prompt: %q", out.String())
	}
}

func TestPublishFailureIsPrintedAndDeliveriesArePrinted(t *testing.T) {
	var out bytes.Buffer
	g := stdin.New(stdin.Config{In: strings.NewReader("hi\n"), Out: &out, Scene: contract.SceneRef{ID: "local"},
		Actor: contract.Actor{ID: "me"}, Locale: "en"})
	if err := g.Run(context.Background(), &sink{fail: errors.New("loop down")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "loop down") {
		t.Fatalf("failure not shown: %q", out.String())
	}
	out.Reset()
	id1, err := g.Deliver(context.Background(), contract.Delivery{Employee: "e1", Text: "On it."})
	id2, _ := g.Deliver(context.Background(), contract.Delivery{Employee: "e1", Text: "Done."})
	if err != nil || id1 == "" || id1 == id2 {
		t.Fatalf("ids %q %q err %v", id1, id2, err)
	}
	if out.String() != "[e1] On it.\n[e1] Done.\n" {
		t.Fatalf("printed %q", out.String())
	}
}

func TestRunStopsWithContext(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	g := stdin.New(stdin.Config{In: r, Scene: contract.SceneRef{ID: "local"}, Actor: contract.Actor{ID: "me"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx, &sink{}) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignores ctx")
	}
}
