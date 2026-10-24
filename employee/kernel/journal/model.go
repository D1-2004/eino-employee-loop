// Package journal reserves physical calls before model I/O, and saves their
// responses before Eino can execute tools. Receipt replay reuses exact responses.
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var ErrInFlight = errors.New("journal: model attempt still active")
var ErrBudget = errors.New("journal: physical model budget exhausted")

type book struct{ Calls int }
type call struct {
	Owner, State, Error string
	ModelIdentity       string `json:",omitempty"`
	Response            *schema.Message
}
type Model struct {
	DB                          storage.State
	Scope, Receipt, Owner, Name string
	Limit                       int
	Delegate                    model.ToolCallingChatModel
	Before                      func(context.Context) error
	tools                       []*schema.ToolInfo
}

func (m *Model) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	delegate, err := m.Delegate.WithTools(tools)
	if err != nil {
		return nil, err
	}
	cp := *m
	cp.Delegate = delegate
	cp.tools = append([]*schema.ToolInfo(nil), tools...)
	return &cp, nil
}
func (m *Model) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	name := m.Name
	if identified, ok := m.Delegate.(interface{ ModelIdentity() string }); ok {
		name = identified.ModelIdentity()
	}
	if m.DB == nil || m.Delegate == nil || m.Scope == "" || m.Receipt == "" || m.Owner == "" || m.Name == "" || m.Limit < 1 {
		return nil, errors.New("journal: complete scoped model binding required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.Before != nil {
		if err := m.Before(ctx); err != nil {
			return nil, err
		}
	}
	// Eino's generated correlation metadata is not part of an OpenAI request. Native
	// tool IDs, content, reasoning, modalities and tool schemas remain frozen.
	copies := make([]*schema.Message, len(messages))
	for i, s := range messages {
		if s == nil {
			return nil, errors.New("journal: nil message")
		}
		cp := *s
		cp.ResponseMeta = nil
		if s.Extra != nil {
			cp.Extra = make(map[string]any, len(s.Extra))
			for k, v := range s.Extra {
				if k != "_eino_msg_id" {
					cp.Extra[k] = v
				}
			}
			if len(cp.Extra) == 0 {
				cp.Extra = nil
			}
		}
		copies[i] = &cp
	}
	options := model.GetCommonOptions(&model.Options{Tools: m.tools}, opts...)
	schemas, err := toolSchemas(options.Tools)
	if err != nil {
		return nil, err
	}
	deferred, err := toolSchemas(options.DeferredTools)
	if err != nil {
		return nil, err
	}
	var search []any
	if options.ToolSearchTool != nil {
		search, err = toolSchemas([]*schema.ToolInfo{options.ToolSearchTool})
		if err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(map[string]any{
		"model": name, "model_override": options.Model, "messages": copies,
		"tools": schemas, "deferred_tools": deferred, "tool_search": search,
		"tool_choice": options.ToolChoice, "agentic_tool_choice": options.AgenticToolChoice,
		"allowed_tools": options.AllowedToolNames, "temperature": options.Temperature,
		"max_tokens": options.MaxTokens, "top_p": options.TopP, "stop": options.Stop,
	})
	if err != nil {
		return nil, err
	}
	id := contract.Digest(raw)
	scopeParts, _ := json.Marshal([]string{m.Scope, m.Receipt})
	scope := string(scopeParts)
	var cached *schema.Message
	var reservation storage.Document
	err = m.DB.Transact(ctx, func(tx storage.Tx) error {
		prior, e := tx.Get(ctx, "model-calls", scope, id)
		var old call
		if e == nil {
			if e = json.Unmarshal(prior.Body, &old); e != nil {
				return e
			}
			if old.State == "complete" {
				cached = old.Response
				return nil
			}
			if old.State == "pending" && old.Owner == m.Owner {
				return ErrInFlight
			}
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		counter, e := tx.Get(ctx, "model-budget", scope, "physical")
		var budget book
		if e == nil {
			if e = json.Unmarshal(counter.Body, &budget); e != nil {
				return e
			}
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		if budget.Calls >= m.Limit {
			return ErrBudget
		}
		budget.Calls++
		counter.Namespace = "model-budget"
		counter.Scope = scope
		counter.ID = "physical"
		counter.Body, _ = json.Marshal(budget)
		if _, e = tx.Put(ctx, counter); e != nil {
			return e
		}
		reservation = storage.Document{Namespace: "model-calls", Scope: scope, ID: id, Revision: prior.Revision}
		reservation.Body, _ = json.Marshal(call{Owner: m.Owner, State: "pending", ModelIdentity: name})
		rev, e := tx.Put(ctx, reservation)
		reservation.Revision = rev
		return e
	})
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return cached, nil
	}
	response, callErr := m.Delegate.Generate(ctx, messages, opts...)
	state := call{Owner: m.Owner, State: "complete", Response: response, ModelIdentity: name}
	if callErr != nil {
		state.State = "failed"
		state.Error = callErr.Error()
	}
	if response == nil && callErr == nil {
		callErr = errors.New("journal: model returned nil")
		state.State = "failed"
		state.Error = callErr.Error()
	}
	reservation.Body, err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err = m.DB.Put(settle, reservation); err != nil {
		return nil, fmt.Errorf("journal: save response before tools: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return response, callErr
}
func (m *Model) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("journal: durable streaming requires complete-response journal; use Generate")
}

func toolSchemas(tools []*schema.ToolInfo) ([]any, error) {
	schemas := make([]any, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, errors.New("journal: nil tool schema")
		}
		var js any
		if t.ParamsOneOf != nil {
			s, err := t.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, err
			}
			js = s
		}
		schemas = append(schemas, map[string]any{"name": t.Name, "description": t.Desc, "schema": js})
	}
	return schemas, nil
}
