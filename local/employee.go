package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/event"
	"github.com/D1-2004/eino-employee-loop/loop"
)

// Employee answers the messages of one channel with the loop.
type Employee struct {
	// ID names the employee in traces (Langfuse tag "agent-<ID>").
	ID string
	// Name is how the employee is called in its prompt and history.
	Name string
	// Instructions describe the employee's role; empty uses the locale's
	// default persona.
	Instructions string
	Locale       string
	Model        model.ToolCallingChatModel
	// Permanent, when set, marks model errors that end a wake at once
	// instead of being repaired (Script.Permanent for a scripted model).
	Permanent func(error) bool
	// ModelName is reported in traces.
	ModelName string
	Channel   Channel
	// Source is the event source of inbound messages, for example "dws".
	Source string
	// Tracer exports one Langfuse trace per wake; nil disables tracing.
	Tracer *Tracer
	// HistoryLimit bounds the remembered lines per conversation (default 20).
	HistoryLimit int
	// Log receives warnings and, at level Debug, the debug log of every
	// message and wake (see debug.go).
	Log *slog.Logger
	// LogBodies adds message texts, model requests and responses, tool
	// arguments and replies to the debug log.
	LogBodies bool

	catalog *event.Catalog
	mu      sync.Mutex
	convs   map[string]*conversation
}

// conversation is one scene: its history and the queue of its foreground
// session, so wakes of one conversation never overlap.
type conversation struct {
	mu    sync.Mutex
	lines []string
	queue chan Message
}

// Run listens on the channel until ctx ends.
func (e *Employee) Run(ctx context.Context) error {
	if e.Model == nil || e.Channel == nil {
		return errors.New("local: an employee needs a model and a channel")
	}
	return e.Channel.Listen(ctx, func(m Message) { e.enqueue(ctx, m) })
}

func (e *Employee) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// conversation returns the scene of m, creating it on first use.
func (e *Employee) conversation(m Message) (*conversation, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.catalog == nil {
		e.catalog = event.Standard()
	}
	if e.convs == nil {
		e.convs = map[string]*conversation{}
	}
	c := e.convs[m.ConversationID]
	if c != nil {
		return c, false
	}
	c = &conversation{queue: make(chan Message, 64)}
	e.convs[m.ConversationID] = c
	return c, true
}

// Answer handles one message synchronously in its conversation.
func (e *Employee) Answer(ctx context.Context, m Message) (loop.Outcome, error) {
	c, _ := e.conversation(m)
	return e.handle(ctx, c, m)
}

func (e *Employee) enqueue(ctx context.Context, m Message) {
	c, created := e.conversation(m)
	if created {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case next := <-c.queue:
					out, err := e.handle(ctx, c, next)
					if err != nil {
						e.log().Warn("local.wake_failed", "conversation", next.ConversationID, "message", next.MessageID, "err", err)
					}
					if o, ok := e.Channel.(handledObserver); ok {
						o.handled(next, out, err)
					}
				}
			}
		}()
	}
	c.queue <- m
}

// handledObserver is a channel told the outcome of every message it
// delivered (Console), not only the replies it sends.
type handledObserver interface {
	handled(Message, loop.Outcome, error)
}

// handle routes one message and, for the foreground, runs a wake. Run calls
// it per conversation in arrival order.
func (e *Employee) handle(ctx context.Context, c *conversation, m Message) (loop.Outcome, error) {
	kind := event.Group
	if m.Direct {
		kind = event.Direct
	}
	typ := "message.observed"
	if m.Addressed() {
		typ = "message.addressed"
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	adm, err := e.catalog.Route(event.Event{
		ID: m.MessageID, Source: e.source(), Employee: e.ID, Type: typ,
		Scene:      event.SceneRef{ID: m.ConversationID, Kind: kind, Locator: m.ConversationID},
		Actor:      event.Actor{ID: m.SenderID, Kind: event.Human, DisplayName: m.SenderName},
		OccurredAt: at, Text: m.Text,
	})
	if err != nil {
		return loop.Outcome{}, err
	}
	e.debugRoute(m, adm)
	speaker := m.SenderName
	if speaker == "" {
		speaker = m.SenderID
	}
	if adm.Spec.Lane != event.Foreground {
		// Background sessions are not run locally; the message stays
		// available as history for the next foreground wake.
		c.remember(speaker+": "+m.Text, e.historyLimit())
		return loop.Outcome{}, nil
	}
	return e.wake(ctx, c, m, adm, speaker)
}

func (e *Employee) wake(ctx context.Context, c *conversation, m Message, adm event.Admission, speaker string) (loop.Outcome, error) {
	c.mu.Lock()
	history := append([]string(nil), c.lines...)
	c.mu.Unlock()
	name := e.Name
	if name == "" {
		name = e.ID
	}
	instructions := e.Instructions
	if strings.TrimSpace(instructions) == "" {
		instructions = text(e.Locale, "persona.default")
	}
	current, _ := json.Marshal(map[string]any{"message_id": m.MessageID, "sender": speaker, "text": m.Text, "direct": m.Direct})
	var user strings.Builder
	if len(history) > 0 {
		user.WriteString(text(e.Locale, "block.history") + "\n" + strings.Join(history, "\n") + "\n\n")
	}
	user.WriteString(text(e.Locale, "block.current") + "\n" + string(current))
	messages := []*schema.Message{
		schema.SystemMessage(text(e.Locale, "system.rules", "name", name) + "\n\n" + instructions),
		schema.UserMessage(user.String()),
	}

	trace := e.Tracer.Start("employee_loop", m.ConversationID, e.ID, map[string]any{
		"agent_id": e.ID, "employee_loop_engine": "eino", "employee_job_kind": "message", "employee_source_message_id": m.MessageID,
		"employee_contract_event": adm.Event.Type, "employee_contract_class": string(adm.Spec.Class), "employee_contract_mode": string(adm.Spec.Mode),
		"employee_contract_lane": string(adm.Spec.Lane), "employee_contract_session": string(adm.Session.Kind),
	})
	host := &chatHost{employee: e, message: m, trace: trace}
	out, err := loop.New(e.config(trace), e.Model, host).Run(ctx, loop.Wake{Identity: loop.Identity{Employee: e.ID, Scene: m.ConversationID, Receipt: m.MessageID}, Messages: messages})

	e.debugDecision(m, out, err)
	limit := e.historyLimit()
	c.remember(speaker+": "+m.Text, limit)
	if out.Kind == loop.Reply && out.Reply != "" {
		c.remember(text(e.Locale, "history.self", "name", name)+": "+out.Reply, limit)
	}
	result := map[string]any{"employee_decision": string(out.Kind), "employee_model_calls": out.ModelCalls}
	if err != nil {
		result["employee_failure"] = err.Error()
	}
	if terr := trace.End(ctx, map[string]any{"decision": string(out.Kind), "reply": out.Reply}, result); terr != nil {
		e.log().Warn("local.trace_failed", "trace", trace.ID(), "err", terr)
	}
	return out, err
}

// config is the loop configuration of every wake; trace may be nil.
func (e *Employee) config(trace *Trace) loop.Config {
	tools := e.tools()
	return loop.Config{
		Tools:               tools,
		RequireTerminalTool: true,
		AcceptFinalText:     true,
		Locale:              e.Locale,
		Permanent:           e.Permanent,
		OnModelCall: func(_ context.Context, call loop.ModelCall) {
			trace.Generation("employee_model", e.ModelName, call)
			// Every request offers all tools: the config has no TurnTools.
			e.debugModel(call, tools)
		},
		OnToolCall: func(_ context.Context, call loop.ToolCall, result loop.ToolResult, err error, elapsed time.Duration) {
			trace.Tool(call, result, err, elapsed)
			e.debugTool(call, result, err, elapsed)
		},
		OnModelFailure: e.debugRepair,
	}
}

func (e *Employee) source() string {
	if e.Source != "" {
		return e.Source
	}
	return "local"
}

func (e *Employee) historyLimit() int {
	if e.HistoryLimit > 0 {
		return e.HistoryLimit
	}
	return 20
}

func (c *conversation) remember(line string, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
	if len(c.lines) > limit {
		c.lines = c.lines[len(c.lines)-limit:]
	}
}

func (e *Employee) tools() []loop.Tool {
	return []loop.Tool{
		// "text" is the public text a Config.ValidateText would check; the
		// local employee sets none, so declaring it changes nothing here.
		{Name: "reply", Description: text(e.Locale, "tool.reply"), Effect: true, Terminal: loop.Reply, PublicArgs: []string{"text"}, Schema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"text"},
			"properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 3000, "description": text(e.Locale, "tool.reply.text")}},
		}},
		{Name: "stay_quiet", Description: text(e.Locale, "tool.stay_quiet"), Terminal: loop.Quiet, Schema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"reason": map[string]any{"type": "string", "description": text(e.Locale, "tool.stay_quiet.reason")}},
		}},
	}
}

// chatHost executes the two tools of a wake. A reply is sent at most once
// per wake; the sent message id (or the source message id when the
// platform reports none) is its receipt. Executing the call that sent it
// again returns its recorded result, as a re-claimed wake expects (the
// loop itself never repeats a call within a wake).
type chatHost struct {
	employee *Employee
	message  Message
	trace    *Trace
	sent     *sentReply
}

// sentReply records the reply of a wake: the call that sent it (the final
// text origin for a final text), its text and its result.
type sentReply struct {
	call, text string
	result     loop.ToolResult
}

func (h *chatHost) Execute(ctx context.Context, _ loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
	switch call.Name {
	case "reply":
		reply, _ := call.Arguments["text"].(string)
		return h.send(ctx, call.NativeToolCallID, reply)
	case "stay_quiet":
		return loop.ToolResult{Content: `{"state":"quiet"}`, Terminal: &loop.Decision{Kind: loop.Quiet}}, nil
	}
	return loop.ToolResult{}, fmt.Errorf("%w: unknown tool %s", loop.ErrToolRefused, call.Name)
}

// AcceptFinalText sends a first plain answer as the reply. The loop reports
// no tool call for it, so it is logged here like one.
func (h *chatHost) AcceptFinalText(ctx context.Context, _ loop.Identity, text string) (loop.ToolResult, error) {
	started := time.Now()
	result, err := h.send(ctx, loop.FinalTextOrigin, text)
	call := loop.ToolCall{Origin: loop.FinalTextOrigin, Name: loop.FinalTextOrigin, Arguments: map[string]any{"text": text}}
	h.employee.debugTool(call, result, err, time.Since(started))
	return result, err
}

func (h *chatHost) send(ctx context.Context, callID, reply string) (loop.ToolResult, error) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return loop.ToolResult{}, fmt.Errorf("%w: empty reply", loop.ErrToolRefused)
	}
	if s := h.sent; s != nil {
		if s.call == callID && s.text == reply {
			return s.result, nil
		}
		return loop.ToolResult{}, fmt.Errorf("%w: this wake already replied", loop.ErrToolRefused)
	}
	id, err := h.employee.Channel.Send(ctx, h.message, reply)
	if err != nil {
		return loop.ToolResult{}, err
	}
	if id == "" {
		id = "reply-to:" + h.message.MessageID
	}
	content, _ := json.Marshal(map[string]string{"state": "sent", "message_id": id})
	result := loop.ToolResult{Content: string(content), Receipt: id, Terminal: &loop.Decision{Kind: loop.Reply, Reply: reply}}
	h.sent = &sentReply{call: callID, text: reply, result: result}
	return result, nil
}
