// Package stdin is a local Event Gateway for one scene: every input line is
// published as a message.addressed event from one person, and deliveries
// are printed. It lets the Employee Loop run interactively with no
// provider. It imports contract and i18n only.
package stdin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Provider is the SceneRef.Provider of the local scene.
const Provider = "stdin"

// DefaultSource is the event source namespace.
const DefaultSource = "local.stdin"

// Quit commands end Run.
var QuitCommands = []string{"/quit", "/exit"}

// Config configures a Gateway. Scene.ID and Actor.ID are required.
type Config struct {
	In  io.Reader
	Out io.Writer
	// Scene is the local scene; Kind defaults to dm, Provider to "stdin".
	Scene contract.SceneRef
	// Actor is the person typing; Kind is forced to human.
	Actor contract.Actor
	// Employee addresses the events (contract.Event.Employee); empty leaves
	// them addressed to whichever loop receives them.
	Employee string
	// Source defaults to DefaultSource.
	Source string
	// Locale is the content and message locale.
	Locale   string
	Messages *i18n.Bundle
	// Prompt prints a prompt before each line.
	Prompt bool
	Now    func() time.Time
}

// Gateway reads lines into events and prints deliveries.
type Gateway struct {
	cfg    Config
	prefix string
	mu     sync.Mutex
	in     int64
	out    int64
}

var _ contract.Deliverer = (*Gateway)(nil)

// New returns a Gateway. Event ids are "<run prefix>-<n>", where the run
// prefix is the start time, so restarts never reuse an id.
func New(cfg Config) *Gateway {
	if cfg.Messages == nil {
		cfg.Messages = Messages()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Source == "" {
		cfg.Source = DefaultSource
	}
	if cfg.Scene.Kind == "" {
		cfg.Scene.Kind = contract.SceneDirect
	}
	if cfg.Scene.Provider == "" {
		cfg.Scene.Provider = Provider
	}
	cfg.Actor.Kind = contract.ActorHuman
	return &Gateway{cfg: cfg, prefix: fmt.Sprintf("%s-%d", Provider, cfg.Now().UnixNano())}
}

// Scene returns the local scene.
func (g *Gateway) Scene() contract.SceneRef { return g.cfg.Scene }

func (g *Gateway) text(key string, args ...string) string {
	return g.cfg.Messages.Text(g.cfg.Locale, key, args...)
}

func (g *Gateway) print(s string, newline bool) {
	if g.cfg.Out == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if newline {
		fmt.Fprintln(g.cfg.Out, s)
	} else {
		fmt.Fprint(g.cfg.Out, s)
	}
}

// Event builds the event of one input line.
func (g *Gateway) Event(line string) contract.Event {
	g.mu.Lock()
	g.in++
	n := g.in
	g.mu.Unlock()
	return contract.Event{
		ID: fmt.Sprintf("%s-%d", g.prefix, n), Source: g.cfg.Source, Employee: g.cfg.Employee, Type: contract.TypeMessageAddressed,
		Scene: g.cfg.Scene, Actor: g.cfg.Actor, OccurredAt: g.cfg.Now().UTC(), Locale: g.cfg.Locale, Text: line,
	}
}

// Run publishes every non-empty line until EOF, a quit command or ctx
// ends. A publish failure is printed and reading continues.
func (g *Gateway) Run(ctx context.Context, sink contract.EventSink) error {
	if g.cfg.In == nil {
		return nil
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(g.cfg.In)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		if g.cfg.Prompt {
			g.print(g.text("gateway.stdin.prompt"), false)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, q := range QuitCommands {
				if line == q {
					return nil
				}
			}
			if _, err := sink.Publish(ctx, g.Event(line)); err != nil {
				g.print(g.text("gateway.stdin.publish_failed", "error", err.Error()), true)
			}
		}
	}
}

// Deliver implements contract.Deliverer by printing the delivery.
func (g *Gateway) Deliver(_ context.Context, d contract.Delivery) (string, error) {
	g.print(g.text("gateway.stdin.delivery", "employee", d.Employee, "text", d.Text), true)
	g.mu.Lock()
	g.out++
	n := g.out
	g.mu.Unlock()
	return fmt.Sprintf("%s-out-%d", g.prefix, n), nil
}
