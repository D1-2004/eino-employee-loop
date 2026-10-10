package core

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/cloudwego/eino/schema"
)

// MinimalTools returns a small tool set for a Grok-Bot-style demo
// (ack → dispatch → result → reply), without the full 31-tool catalog.
// Descriptions come from the "runtime.tool.*" pack keys of locale (nil msgs =
// Messages()).
func MinimalTools(msgs *i18n.Bundle, locale string) ([]*schema.ToolInfo, map[string]bool) {
	tx := newTexts(msgs, locale)
	d := func(key string) string { return tx.T("runtime.tool." + key) }
	falseV := false
	obj := func(props map[string]any, req []any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": falseV, "properties": props, "required": req}
	}
	toolsAny := []any{
		fn("find_tasks", d("find_tasks"), obj(map[string]any{
			"query": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("read_task", d("read_task"), obj(map[string]any{
			"task_ref": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("dispatch_task", d("dispatch_task"), obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "goal": map[string]any{"type": "string"},
			"prompt": map[string]any{"type": "string"},
			"reply":  map[string]any{"type": "string", "description": d("dispatch_task.param.reply")},
		}, []any{"source_ref", "goal", "prompt", "reply"})),
		fn("reply", d("reply"), obj(map[string]any{
			"reply":      map[string]any{"type": "string", "description": d("reply.param.reply")},
			"source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "reply"})),
		fn("ask_human", d("ask_human"), obj(map[string]any{
			"summary": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "summary"})),
		fn("stay_quiet", d("stay_quiet"), map[string]any{"type": "object"}),
		fn("memory_capture", d("memory_capture"), obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"type": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "quote": map[string]any{"type": "string"},
		}, []any{"source_ref", "audience", "type", "subject", "quote"})),
		fn("memory_lookup", d("memory_lookup"), obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "scope": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"query": map[string]any{"type": "string"},
		}, []any{"source_ref", "scope", "query"})),
		fn("memory_forget", d("memory_forget"), obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "record_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "record_ref"})),
		fn("stop_task", d("stop_task"), obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "task_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "task_ref"})),
		fn("read_context", d("read_context"), obj(map[string]any{
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

// DemoCase builds CaseData for the interactive / scripted demo in locale
// (nil msgs = Messages()). System text resolution follows
// ResolveSystemInstruction.
func DemoCase(msgs *i18n.Bundle, locale, system string) CaseData {
	system = ResolveSystemInstruction(msgs, locale, system)
	tools, term := MinimalTools(msgs, locale)
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	wd := int((now.Weekday() + 6) % 7)
	clk := newTexts(msgs, locale).T("runtime.clock", "payload", fmt.Sprintf(`{"at": %q, "today": %q, "yesterday": %q, "week_start": %q, "week_end": %q}`,
		now.Format(time.RFC3339Nano), now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02"),
		now.AddDate(0, 0, -wd).Format("2006-01-02"), now.AddDate(0, 0, 6-wd).Format("2006-01-02")))
	return CaseData{System: system, Clock: clk, ClockAtMs: now.UnixMilli(), Tools: tools, Terminal: term}
}

// ResolveSystemInstruction picks instruction text: explicit > SYSTEM_INSTRUCTION /
// EINO_SYSTEM_INSTRUCTION env > the "instruction.default_colleague" pack text
// of locale (nil msgs = Messages()).
func ResolveSystemInstruction(msgs *i18n.Bundle, locale, explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	for _, k := range []string{"SYSTEM_INSTRUCTION", "EINO_SYSTEM_INSTRUCTION"} {
		if s := strings.TrimSpace(os.Getenv(k)); s != "" {
			return s
		}
	}
	return newTexts(msgs, locale).T("instruction.default_colleague")
}
