package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/event"
	"github.com/D1-2004/eino-employee-loop/loop"
)

// The debug log: at slog level Debug the employee logs, per message, the
// routed event (local.route) and, per wake, every model request
// (local.model), repair (local.repair), tool call (local.tool) and the
// decision (local.decision). Message texts, requests, responses, tool
// arguments and replies are added only with Employee.LogBodies.

func (e *Employee) debugging() bool {
	return e.log().Enabled(context.Background(), slog.LevelDebug)
}

func (e *Employee) debugRoute(m Message, adm event.Admission) {
	if !e.debugging() {
		return
	}
	attrs := []any{"conversation", m.ConversationID, "message", m.MessageID, "sender", m.SenderName,
		"event", adm.Event.Type, "class", adm.Spec.Class, "mode", adm.Spec.Mode, "lane", adm.Spec.Lane}
	if adm.Session != nil {
		attrs = append(attrs, "session", adm.Session.String())
	}
	if adm.Spec.Lane != event.Foreground {
		attrs = append(attrs, "wake", "none: kept as history")
	}
	if e.LogBodies {
		attrs = append(attrs, "text", m.Text)
	}
	e.log().Debug("local.route", attrs...)
}

func (e *Employee) debugModel(call loop.ModelCall, tools []loop.Tool) {
	if !e.debugging() {
		return
	}
	offered := make([]string, len(tools))
	for i, t := range tools {
		offered[i] = t.Name
	}
	attrs := []any{"attempt", call.Attempt, "messages", len(call.Request), "roles", roles(call.Request),
		"tools", strings.Join(offered, ","), "elapsed", call.Elapsed.Round(time.Millisecond)}
	if resp := call.Response; resp != nil {
		if meta := resp.ResponseMeta; meta != nil {
			attrs = append(attrs, "finish", meta.FinishReason)
			if u := meta.Usage; u != nil {
				attrs = append(attrs, "tokens", fmt.Sprintf("%d/%d", u.PromptTokens, u.CompletionTokens))
			}
		}
		var calls []string
		for _, c := range resp.ToolCalls {
			calls = append(calls, c.Function.Name)
		}
		if len(calls) > 0 {
			attrs = append(attrs, "tool_calls", strings.Join(calls, ","))
		}
		if resp.Content != "" {
			attrs = append(attrs, "text_chars", len([]rune(resp.Content)))
		}
	}
	if call.Err != nil {
		attrs = append(attrs, "err", call.Err)
	}
	if e.LogBodies {
		attrs = append(attrs, "request", jsonText(call.Request), "response", jsonText(call.Response))
	}
	e.log().Debug("local.model", attrs...)
}

func (e *Employee) debugRepair(err error) {
	if e.debugging() {
		e.log().Debug("local.repair", "err", err)
	}
}

func (e *Employee) debugTool(call loop.ToolCall, result loop.ToolResult, err error, elapsed time.Duration) {
	if !e.debugging() {
		return
	}
	keys := make([]string, 0, len(call.Arguments))
	for k := range call.Arguments {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	attrs := []any{"tool", call.Name}
	if call.NativeToolCallID != "" {
		attrs = append(attrs, "call", call.NativeToolCallID)
	}
	attrs = append(attrs, "args", strings.Join(keys, ","), "elapsed", elapsed.Round(time.Millisecond))
	if result.Receipt != "" {
		attrs = append(attrs, "receipt", result.Receipt)
	}
	if result.Terminal != nil {
		attrs = append(attrs, "terminal", result.Terminal.Kind)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	if e.LogBodies {
		attrs = append(attrs, "arguments", jsonText(call.Arguments), "result", result.Content)
	}
	e.log().Debug("local.tool", attrs...)
}

func (e *Employee) debugDecision(m Message, out loop.Outcome, err error) {
	if !e.debugging() {
		return
	}
	var tools []string
	for _, o := range out.ToolOutcomes {
		tools = append(tools, o.ToolName)
	}
	attrs := []any{"conversation", m.ConversationID, "message", m.MessageID, "decision", out.Kind,
		"tools", strings.Join(tools, ","), "receipts", len(out.Receipts), "model_calls", out.ModelCalls,
		"responses", out.BudgetUsage.ModelResponses, "tokens", fmt.Sprintf("%d/%d", out.Usage.PromptTokens, out.Usage.CompletionTokens)}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	if e.LogBodies {
		attrs = append(attrs, "reply", out.Reply)
	}
	e.log().Debug("local.decision", attrs...)
}

// roles counts the messages of a request by role, in order of appearance:
// "system:1,user:1,assistant:1,tool:1".
func roles(msgs []*schema.Message) string {
	var order []schema.RoleType
	count := map[schema.RoleType]int{}
	for _, m := range msgs {
		if count[m.Role] == 0 {
			order = append(order, m.Role)
		}
		count[m.Role]++
	}
	parts := make([]string, len(order))
	for i, r := range order {
		parts[i] = fmt.Sprintf("%s:%d", r, count[r])
	}
	return strings.Join(parts, ",")
}

func jsonText(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
