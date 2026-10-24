package einotools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/kernel"
	loop "github.com/D1-2004/eino-employee-loop/kernel/decision"
	host "github.com/D1-2004/eino-employee-loop/ports/services"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

func encode(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

type frozenHostTools struct {
	Call  host.Call
	Tools []host.Tool
}

// Build freezes the exact read-only catalog before model I/O and rechecks authority.
func Build(ctx context.Context, bindings host.NativeToolBindings, provider host.ToolProvider, c host.Call, receipt string, before func(context.Context) error) ([]*kernel.Function, error) {
	scope := c.Scope
	if err := bindings.Authorize(ctx, c); err != nil {
		return nil, err
	}
	if err := before(ctx); err != nil {
		return nil, err
	}
	definitions, err := provider.DiscoverTools(ctx, c)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.ID == "" || seen[definition.ID] || definition.Effect || !json.Valid(definition.Schema) {
			return nil, host.ErrUnsupported
		}
		seen[definition.ID] = true
	}
	frozen := frozenHostTools{Call: c, Tools: definitions}
	key, _ := scope.Key()
	err = bindings.State.Transact(ctx, func(tx storage.Tx) error {
		d, e := tx.Get(ctx, "host-tool-catalog", key, receipt)
		if e == nil {
			if string(d.Body) != string(encode(frozen)) {
				return errors.New("entry: frozen Host tool catalog changed")
			}
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		_, e = tx.Put(ctx, storage.Document{Namespace: "host-tool-catalog", Scope: key, ID: receipt, Body: encode(frozen)})
		return e
	})
	if err != nil {
		return nil, err
	}
	functions := make([]*kernel.Function, 0, len(definitions))
	for _, definition := range definitions {
		var params map[string]any
		if err = json.Unmarshal(definition.Schema, &params); err != nil || params["type"] != "object" {
			return nil, host.ErrInvalid
		}
		definition := definition
		functions = append(functions, &kernel.Function{Spec: loop.Tool{Name: definition.ID, Description: definition.Description, Schema: params}, Before: before, Invoke: func(ctx context.Context, id string, args map[string]any) (any, error) {
			if err := bindings.Authorize(ctx, c); err != nil {
				return nil, err
			}
			result, err := provider.CallTool(ctx, c, host.ToolCall{ID: definition.ID, CallID: id, Arguments: encode(args)})
			if err != nil {
				return nil, err
			}
			if err = before(ctx); err != nil {
				return nil, err
			}
			if !json.Valid(result.Content) {
				return nil, host.ErrInvalid
			}
			return result.Content, nil
		}})
	}
	return functions, nil
}
