package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Rule fixtures are stateless: they match the frozen request, never consume a
// process-local cursor. Reopening a partially checkpointed run therefore cannot
// accidentally shift all subsequent responses by the number of cache hits.
type ruleScript struct {
	Version   string      `json:"version"`
	ModelName string      `json:"modelName"`
	Rules     []modelRule `json:"rules"`
}
type modelRule struct {
	Role      string       `json:"role"`
	Receipt   string       `json:"receipt,omitempty"`
	AfterTool *string      `json:"afterTool,omitempty"`
	Contains  []string     `json:"contains,omitempty"`
	Text      *string      `json:"text,omitempty"`
	Error     string       `json:"error,omitempty"`
	ToolCalls []scriptCall `json:"tool_calls,omitempty"`
}
type scriptCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type ruleModel struct {
	script  *ruleScript
	request entry.ModelRequest
}

func loadRuleModels(path string) (entry.Models, error) {
	var s ruleScript
	if err := readJSON(path, &s); err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	if s.Version != "local-el-script/1" || s.ModelName == "" || len(s.Rules) == 0 {
		return nil, errors.New("script: version, modelName and rules required")
	}
	for i, r := range s.Rules {
		outputs := 0
		for _, ok := range []bool{r.Text != nil, r.Error != "", len(r.ToolCalls) > 0} {
			if ok {
				outputs++
			}
		}
		if r.Role == "" || outputs != 1 {
			return nil, fmt.Errorf("script rule %d: role and exactly one response required", i+1)
		}
		seen := map[string]bool{}
		for _, c := range r.ToolCalls {
			if c.ID == "" || c.Name == "" || c.Arguments == nil || seen[c.ID] {
				return nil, fmt.Errorf("script rule %d: unique native call ID, name and arguments required", i+1)
			}
			seen[c.ID] = true
		}
	}
	return func(_ context.Context, r entry.ModelRequest) (model.ToolCallingChatModel, error) {
		if s.ModelName != r.ModelName {
			return nil, errors.New("script modelName must equal frozen profile.modelName")
		}
		return &ruleModel{script: &s, request: r}, nil
	}, nil
}
func (m *ruleModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *ruleModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("script: streaming is not configured")
}
func (m *ruleModel) Generate(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(messages)
	lastTool := ""
	bindings := map[string]string{"${source}": m.request.Receipt}
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.Tool {
			lastTool = msg.ToolName
		}
		if msg != nil && msg.Role == schema.User {
			var frame struct {
				Current         *struct{ ID, Text string }
				CurrentSourceID string `json:"current_source_id"`
				Sources         []struct {
					Scope      contract.Scope
					CitationID string
					Source     struct{ ID, Text string }
				}
			}
			if json.Unmarshal([]byte(msg.Content), &frame) != nil {
				continue
			}
			if frame.Current != nil && frame.Current.ID != "" {
				bindings["${current.id}"] = frame.Current.ID
				bindings["${current.text}"] = frame.Current.Text
			} else if frame.CurrentSourceID != "" {
				// Owner's bounded payload keeps the original once in Sources and
				// selects it by ID. Bind its exact citation within the job's scope.
				for _, source := range frame.Sources {
					if source.Source.ID == frame.CurrentSourceID && source.Scope == m.request.Scope {
						bindings["${current.id}"] = source.CitationID
						bindings["${current.text}"] = source.Source.Text
						break
					}
				}
			}
		}
	}
	for _, r := range m.script.Rules {
		if r.Role != m.request.Role || r.Receipt != "" && r.Receipt != m.request.Receipt || r.AfterTool != nil && *r.AfterTool != lastTool {
			continue
		}
		match := true
		for _, part := range r.Contains {
			if !strings.Contains(string(raw), part) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		response := &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}
		if r.Text != nil {
			response.Content = *r.Text
			return response, nil
		}
		response.ResponseMeta.FinishReason = "tool_calls"
		for _, c := range r.ToolCalls {
			args := replaceBindings(c.Arguments, bindings)
			encoded, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			response.ToolCalls = append(response.ToolCalls, schema.ToolCall{ID: c.ID, Type: "function", Function: schema.FunctionCall{Name: c.Name, Arguments: string(encoded)}})
		}
		return response, nil
	}
	return nil, fmt.Errorf("script: no rule for role=%s afterTool=%s", m.request.Role, lastTool)
}
func replaceBindings(value any, bindings map[string]string) any {
	switch v := value.(type) {
	case string:
		if bound, exists := bindings[v]; exists {
			return bound
		}
		return v
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			out[k] = replaceBindings(x, bindings)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = replaceBindings(x, bindings)
		}
		return out
	default:
		return v
	}
}
