// Package employeeloop assembles one Employee Loop service from the loop
// packages:
//
//	EventSink (Publish) ──► scene.Hub ──► KernelRunner (model + toolhost)
//	scene.Worker ──► Hub.Step (foreground first) ──► Outbox ──► Deliverer
//
// The loop owns admission, sessions, wakes, the outbox, memory and the
// conversation log. Its storage is the contract ports in Config.Ports; any
// port left nil is in memory (store/memstore), so the loop runs and tests
// locally with no database, and a deployment passes durable ports without
// copying the assembly. Everything outside is injected too: the chat model,
// the Task Manager (contract.TaskManager) and the channel egress
// (contract.Deliverer). The package never imports a gateway, a model
// provider or a Task Manager implementation, so the same assembly runs
// in-process, behind HTTP, or in tests with scripted models and fakes.
//
// The loop records people's business messages (on Publish, also for
// duplicates) and delivered replies in the conversation log. Recording is
// idempotent by source ref (contract.Conversation), so a retried publish
// repairs a missed append and never records a message twice.
package employeeloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/toolhost"
)

// ErrConfig marks an incomplete configuration.
var ErrConfig = errors.New("employeeloop: invalid_config")

// DefaultHistory is the number of conversation messages in a wake's
// history block.
const DefaultHistory = 20

// Conversation roles and kinds recorded by the loop.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	KindMessage   = "msg"
	KindReaction  = "reaction"
	KindReply     = "reply"
)

// Ports are the loop's storage. Each nil port defaults to memory: Log,
// Queue, Journal and Outbox share one memstore.Store; Memory is
// memstore.NewMemory and Conversation memstore.NewConversation.
type Ports struct {
	Log          contract.EventLog
	Queue        contract.SessionQueue
	Journal      contract.Journal
	Outbox       contract.Outbox
	Memory       contract.MemoryStore
	Conversation contract.Conversation
}

func (p Ports) withDefaults() Ports {
	var st *memstore.Store
	mem := func() *memstore.Store {
		if st == nil {
			st = memstore.New()
		}
		return st
	}
	if p.Log == nil {
		p.Log = mem()
	}
	if p.Queue == nil {
		p.Queue = mem()
	}
	if p.Journal == nil {
		p.Journal = mem()
	}
	if p.Outbox == nil {
		p.Outbox = mem()
	}
	if p.Memory == nil {
		p.Memory = memstore.NewMemory()
	}
	if p.Conversation == nil {
		p.Conversation = memstore.NewConversation()
	}
	return p
}

// Config configures a Loop. Employee and Model are required.
type Config struct {
	Employee string
	Tenant   string
	// Locale is the default language; an event's own locale wins.
	Locale string
	// Persona is the trusted employee instructions. Empty uses the
	// localized generic persona.
	Persona string
	Model   model.ToolCallingChatModel
	// Tasks is the Task Manager boundary; nil offers no task tools.
	Tasks    contract.TaskManager
	Messages *i18n.Bundle
	// Catalog defaults to contract.StandardCatalog().
	Catalog *contract.Catalog
	// Budgets per lane; missing lanes use contract.DefaultBudget.
	Budgets map[contract.Lane]contract.Budget
	// History bounds the history block; 0 uses DefaultHistory, < 0 disables.
	History int
	// Ports is the loop's storage; nil ports are in memory.
	Ports Ports
	Now   func() time.Time
}

// Loop is one Employee Loop.
type Loop struct {
	cfg Config
	// Ports are the resolved storage ports (Config.Ports with defaults).
	Ports Ports
	Tools *toolhost.Host
	Hub   *scene.Hub
}

var _ contract.EventSink = (*Loop)(nil)

// New assembles a Loop.
func New(cfg Config) (*Loop, error) {
	if strings.TrimSpace(cfg.Employee) == "" {
		return nil, fmt.Errorf("%w: employee", ErrConfig)
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("%w: model", ErrConfig)
	}
	if cfg.Messages == nil {
		cfg.Messages = i18n.Default()
	}
	if cfg.Catalog == nil {
		cfg.Catalog = contract.StandardCatalog()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.History == 0 {
		cfg.History = DefaultHistory
	}
	if strings.TrimSpace(cfg.Persona) == "" {
		cfg.Persona = cfg.Messages.Text(cfg.Locale, "employeeloop.persona.default")
	}
	p := cfg.Ports.withDefaults()
	l := &Loop{cfg: cfg, Ports: p}
	l.Tools = toolhost.New(toolhost.Ports{Tasks: cfg.Tasks, Memory: p.Memory, Conversation: p.Conversation, Journal: p.Journal},
		toolhost.WithMessages(cfg.Messages), toolhost.WithLocale(cfg.Locale))
	runner := &scene.KernelRunner{
		Model: cfg.Model, Journal: p.Journal, Tools: l.Tools.Specs(), Persona: cfg.Persona,
		Messages: cfg.Messages, Locale: cfg.Locale, Tenant: cfg.Tenant, Budgets: cfg.Budgets,
		Hosts:   func(win contract.Window) contract.Host { return l.Tools.ForWindow(win, cfg.Tenant) },
		Context: l.history,
	}
	l.Hub = &scene.Hub{
		Employee: cfg.Employee, Catalog: cfg.Catalog, Log: p.Log, Queue: p.Queue, Outbox: p.Outbox,
		Runner: runner, Now: cfg.Now,
	}
	return l, nil
}

// Employee returns the employee id.
func (l *Loop) Employee() string { return l.cfg.Employee }

// Publish implements contract.EventSink: it admits the event and records
// people's business messages in the scene's conversation log. Recording
// runs for duplicates too and is idempotent by source ref, so republishing
// an event whose recording failed repairs the log.
func (l *Loop) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	ack, err := l.Hub.Publish(ctx, e)
	if err != nil {
		return ack, err
	}
	if spec, ok := l.cfg.Catalog.Lookup(e.Type); ok && spec.Class == contract.ClassBusiness &&
		e.Actor.Kind == contract.ActorHuman && strings.TrimSpace(e.Text) != "" && e.Scene.ID != "" {
		kind := KindMessage
		if e.Type == contract.TypeReactionAdded {
			kind = KindReaction
		}
		speaker := e.Actor.DisplayName
		if speaker == "" {
			speaker = e.Actor.ID
		}
		if _, err := l.Ports.Conversation.Append(ctx, l.cfg.Tenant, e.Scene.ID, contract.Msg{
			Role: RoleUser, Speaker: speaker, Text: e.Text, Kind: kind, SourceRef: e.ID, AtMs: e.OccurredAt.UnixMilli(),
		}); err != nil {
			return ack, err
		}
	}
	return ack, nil
}

// Worker returns a worker that sends deliveries through d (nil leaves them
// in the outbox) and records every delivered reply in the conversation log.
func (l *Loop) Worker(d contract.Deliverer) *scene.Worker {
	w := &scene.Worker{Hub: l.Hub, ID: l.cfg.Employee}
	if d != nil {
		rec := recordingDeliverer{l: l, next: d}
		w.Deliverer = rec
		if q, ok := d.(contract.DeliveryReconciler); ok {
			w.Deliverer = reconcilingDeliverer{recordingDeliverer: rec, q: q}
		}
	}
	return w
}

// recordingDeliverer logs the employee's sent replies.
type recordingDeliverer struct {
	l    *Loop
	next contract.Deliverer
}

func (r recordingDeliverer) Deliver(ctx context.Context, d contract.Delivery) (string, error) {
	id, err := r.next.Deliver(ctx, d)
	if err != nil {
		return id, err
	}
	r.record(ctx, d)
	return id, nil
}

func (r recordingDeliverer) record(ctx context.Context, d contract.Delivery) {
	if d.Scene.ID != "" {
		_, _ = r.l.Ports.Conversation.Append(ctx, r.l.cfg.Tenant, d.Scene.ID, contract.Msg{
			Role: RoleAssistant, Speaker: r.l.cfg.Employee, Text: d.Text, Kind: KindReply,
			SourceRef: d.ID, AtMs: r.l.cfg.Now().UnixMilli(),
		})
	}
}

// reconcilingDeliverer forwards status queries and logs replies that a
// query finds were sent after all.
type reconcilingDeliverer struct {
	recordingDeliverer
	q contract.DeliveryReconciler
}

func (r reconcilingDeliverer) DeliveryStatus(ctx context.Context, d contract.Delivery) (contract.DeliveryState, string, error) {
	state, id, err := r.q.DeliveryStatus(ctx, d)
	if err == nil && (state == contract.DeliveryDelivered || state == contract.DeliveryAccepted) {
		r.record(ctx, d)
	}
	return state, id, err
}

// history freezes the scene's recent conversation, excluding the events of
// the window itself, as one data block.
func (l *Loop) history(ctx context.Context, win contract.Window) ([]contract.ContextBlock, error) {
	if l.cfg.History < 0 || len(win.Items) == 0 {
		return nil, nil
	}
	inWindow := map[string]bool{}
	for _, it := range win.Items {
		inWindow[it.Event.ID] = true
	}
	msgs, err := l.Ports.Conversation.Recent(ctx, l.cfg.Tenant, win.Session.Scene, 0, l.cfg.History+len(win.Items))
	if err != nil {
		return nil, err
	}
	locale := l.cfg.Locale
	if last := win.Items[len(win.Items)-1].Event; last.Locale != "" {
		locale = last.Locale
	}
	var lines []string
	for _, m := range msgs {
		if inWindow[m.SourceRef] {
			continue
		}
		lines = append(lines, l.cfg.Messages.Text(locale, "employeeloop.history.line", "speaker", m.Speaker, "text", m.Text))
	}
	if len(lines) > l.cfg.History {
		lines = lines[len(lines)-l.cfg.History:]
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return []contract.ContextBlock{{Kind: contract.BlockHistory, Text: strings.Join(lines, "\n")}}, nil
}
