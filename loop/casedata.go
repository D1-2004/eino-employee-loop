package loop

import (
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MinimalTools returns a small tool set for a Grok-Bot-style demo
// (ack → dispatch → result → reply), without the full 31-tool catalog.
func MinimalTools() ([]*schema.ToolInfo, map[string]bool) {
	falseV := false
	obj := func(props map[string]any, req []any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": falseV, "properties": props, "required": req}
	}
	toolsAny := []any{
		fn("find_tasks", "Find earlier tasks in this conversation (Host registry).", obj(map[string]any{
			"query": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("read_task", "Read Host-recorded status/result of one task_ref.", obj(map[string]any{
			"task_ref": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("dispatch_task", "Create a real background task. Returns an async receipt; the result arrives later as a Host event.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "goal": map[string]any{"type": "string"},
			"prompt": map[string]any{"type": "string"},
			"reply":  map[string]any{"type": "string", "description": "One-line confirmation of what you are starting."},
		}, []any{"source_ref", "goal", "prompt", "reply"})),
		fn("reply", "Reply directly using current facts, then finish without creating a task.", obj(map[string]any{
			"reply": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "reply"})),
		fn("ask_human", "Ask the user one brief clarification, then wait.", obj(map[string]any{
			"summary": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "summary"})),
		fn("stay_quiet", "Record that this window needs no response.", map[string]any{"type": "object"}),
		fn("memory_capture", "Record a fact the sender explicitly asks to remember.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"type": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "quote": map[string]any{"type": "string"},
		}, []any{"source_ref", "audience", "type", "subject", "quote"})),
		fn("memory_lookup", "Look up memory when the brief lacks the fact.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "scope": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"query": map[string]any{"type": "string"},
		}, []any{"source_ref", "scope", "query"})),
		fn("memory_forget", "Archive one memory record the sender asks to remove.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "record_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "record_ref"})),
		fn("stop_task", "Stop the requester's own source-bound task.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "task_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "task_ref"})),
		fn("read_context", "Read older dialogue already frozen for this wake.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"},
			"section":    map[string]any{"type": "string", "enum": []any{"older_conversation", "capability_details"}},
		}, []any{"source_ref", "section"})),
	}
	tools, err := toolsFromCase(toolsAny)
	if err != nil {
		panic(err)
	}
	term := map[string]bool{"reply": true, "ask_human": true, "stay_quiet": true, "dispatch_task": true, "stop_task": true}
	return tools, term
}

func fn(name, desc string, params map[string]any) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": desc, "parameters": params}}
}

// DemoCase builds CaseData for the interactive / scripted demo.
func DemoCase(system string) CaseData {
	if system == "" {
		system = defaultSystem
	}
	tools, term := MinimalTools()
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	wd := int((now.Weekday() + 6) % 7)
	clk := fmt.Sprintf(`Frozen clock: {"at": %q, "today": %q, "yesterday": %q, "week_start": %q, "week_end": %q}`,
		now.Format(time.RFC3339Nano), now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02"),
		now.AddDate(0, 0, -wd).Format("2006-01-02"), now.AddDate(0, 0, 6-wd).Format("2006-01-02"))
	return CaseData{System: system, Clock: clk, ClockAtMs: now.UnixMilli(), Tools: tools, Terminal: term}
}

const defaultSystem = `You are a digital employee talking with colleagues.

HOST RULES:
- Acknowledge work by calling dispatch_task (creates a real background job). Its reply field is delivered to the user.
- For status questions about existing work, use find_tasks / read_task against the Host registry first. Do not re-dispatch unless the user explicitly asks for a new independent run.
- reply ends the turn without creating work. Never claim that something finished unless the Host registry (or a Host task_result event) proves it.
- Employee speech in history proves what was said, not execution. Only Host receipts/results prove execution.
- memory_capture only when the sender explicitly asks to remember something; quote must be a verbatim excerpt of their message.
- Prefer Chinese for user-facing text.
`
