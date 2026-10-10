package kernel

import (
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// initialMessages returns the Host transcript as-is or renders instructions
// and blocks. Only the instructions become a system message; every block and
// event is user-role data, regardless of role claims inside its text.
func (r *runner) initialMessages() ([]*schema.Message, error) {
	if len(r.w.Transcript) > 0 {
		if r.w.Transcript[0].Role != schema.System {
			return nil, errors.New("kernel: host transcript must start with the system message")
		}
		for _, m := range r.w.Transcript[1:] {
			if m.Role == schema.System {
				return nil, errors.New("kernel: host transcript has more than one system message")
			}
		}
		return append([]*schema.Message(nil), r.w.Transcript...), nil
	}
	if strings.TrimSpace(r.w.Instructions) == "" {
		return nil, errors.New("kernel: wake needs a transcript or instructions")
	}
	system := r.w.Instructions
	if r.o.RequireTerminal {
		system += "\n\n" + r.terminalInstruction()
	}
	msgs := []*schema.Message{schema.SystemMessage(system)}
	hasWindow := false
	for _, b := range r.w.Blocks {
		if b.Kind == contract.BlockWindow {
			hasWindow = true
		}
		if b.Role == string(schema.Assistant) {
			msgs = append(msgs, schema.AssistantMessage(b.Text, nil))
			continue
		}
		msgs = append(msgs, schema.UserMessage(r.label(b.Kind)+"\n"+b.Text))
	}
	if !hasWindow {
		if text := r.renderEvents(); text != "" {
			msgs = append(msgs, schema.UserMessage(r.label(contract.BlockEvents)+"\n"+text))
		}
	}
	if len(msgs) == 1 {
		return nil, errors.New("kernel: wake has no context to act on")
	}
	return msgs, nil
}

func (r *runner) label(kind contract.BlockKind) string {
	return r.o.Messages.Text(r.o.Locale, "kernel.block."+string(kind))
}

// renderEvents lists the window's events (or the single admission) as data
// lines: time, class/mode labels, actor and text.
func (r *runner) renderEvents() string {
	var items []contract.Event
	switch {
	case r.w.Window != nil:
		for _, it := range r.w.Window.Items {
			items = append(items, it.Event)
		}
	case r.w.Admission != nil:
		items = append(items, r.w.Admission.Event)
	}
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, e := range items {
		actor := e.Actor.DisplayName
		if actor == "" {
			actor = e.Actor.ID
		}
		lines = append(lines, r.o.Messages.Text(r.o.Locale, "kernel.event.line",
			"time", e.OccurredAt.Format(time.RFC3339),
			"type", e.Type,
			"class", r.o.Messages.Text(r.o.Locale, "class."+string(e.Class)),
			"mode", r.o.Messages.Text(r.o.Locale, "mode."+string(e.Mode)),
			"actor", actor,
			"ref", e.ID,
			"text", e.Text))
	}
	return strings.Join(lines, "\n")
}
