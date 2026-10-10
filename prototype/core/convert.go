package core

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// toolsFromCase converts OpenAI-style case tools into Eino ToolInfo with ParamsOneOf JSON Schema.
func toolsFromCase(toolsAny []any) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(toolsAny))
	for i, item := range toolsAny {
		t, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool[%d] not object", i)
		}
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			return nil, fmt.Errorf("tool[%d] missing function", i)
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		var oneOf *schema.ParamsOneOf
		if params != nil {
			raw, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			js := &jsonschema.Schema{}
			if err := json.Unmarshal(raw, js); err != nil {
				return nil, fmt.Errorf("tool %s schema: %w", name, err)
			}
			oneOf = schema.NewParamsOneOfByJSONSchema(js)
		}
		out = append(out, &schema.ToolInfo{
			Name:        name,
			Desc:        desc,
			ParamsOneOf: oneOf,
		})
	}
	return out, nil
}

func messagesFromAny(in []any) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(in))
	for i, item := range in {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("message[%d] not object", i)
		}
		msg, err := mapToMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func mapToMessage(m map[string]any) (*schema.Message, error) {
	role, _ := m["role"].(string)
	content, _ := m["content"].(string)
	msg := &schema.Message{Content: content}
	switch role {
	case "system":
		msg.Role = schema.System
	case "user":
		msg.Role = schema.User
	case "assistant":
		msg.Role = schema.Assistant
		if tcs, ok := m["tool_calls"].([]any); ok && len(tcs) > 0 {
			for _, raw := range tcs {
				tcMap, _ := raw.(map[string]any)
				fn, _ := tcMap["function"].(map[string]any)
				args := ""
				switch v := fn["arguments"].(type) {
				case string:
					args = v
				default:
					b, _ := json.Marshal(v)
					args = string(b)
				}
				msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
					ID:   str(tcMap["id"]),
					Type: strDefault(tcMap["type"], "function"),
					Function: schema.FunctionCall{
						Name:      str(fn["name"]),
						Arguments: args,
					},
				})
			}
		}
	case "tool":
		msg.Role = schema.Tool
		msg.ToolCallID = str(m["tool_call_id"])
	default:
		msg.Role = schema.User
	}
	return msg, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strDefault(v any, def string) string {
	s, _ := v.(string)
	if s == "" {
		return def
	}
	return s
}

func assistantToBridge(msg *schema.Message) map[string]any {
	out := map[string]any{
		"content": msg.Content,
	}
	if len(msg.ToolCalls) > 0 {
		tcs := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			tcs = append(tcs, map[string]any{
				"id":   tc.ID,
				"type": tc.Type,
				"function": map[string]any{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
			})
		}
		out["tool_calls"] = tcs
		out["finish_reason"] = "tool_calls"
	} else {
		out["finish_reason"] = "stop"
	}
	return out
}
