package local

import (
	"bytes"
	"context"
	"encoding/json"
	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
	"io"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

const ReadSourceTool = "host_read_source"
const ReadArtifactTool = "host_read_artifact"

func (l *local) DiscoverTools(ctx context.Context, c host.Call) ([]host.Tool, error) {
	if err := l.check(ctx, c); err != nil {
		return nil, err
	}
	return []host.Tool{
		{ID: ReadSourceTool, Description: "Read exact admitted source IDs in the authorized scope; missing IDs are explicit.", Schema: json.RawMessage(`{"type":"object","properties":{"message_ids":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":100}},"required":["message_ids"],"additionalProperties":false}`)},
		{ID: ReadArtifactTool, Description: "Read an immutable artifact from the bound Task and Run using a previously listed resource reference.", Schema: json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"run_id":{"type":"string"},"path":{"type":"string"},"version":{"type":"string"},"sha256":{"type":"string"}},"required":["task_id","run_id","path","version","sha256"],"additionalProperties":false}`)},
	}, nil
}
func decodeArguments(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return host.ErrInvalid
	}
	return nil
}
func (l *local) CallTool(ctx context.Context, c host.Call, q host.ToolCall) (host.ToolResult, error) {
	if err := l.check(ctx, c); err != nil {
		return host.ToolResult{}, err
	}
	var value any
	switch q.ID {
	case ReadSourceTool:
		var request host.MessageRequest
		if decodeArguments(q.Arguments, &request) != nil {
			return host.ToolResult{}, host.ErrInvalid
		}
		v, err := l.services.Messages.GetMessages(ctx, c, request)
		if err != nil {
			return host.ToolResult{}, err
		}
		value = v
	case ReadArtifactTool:
		var ref host.ResourceRef
		if decodeArguments(q.Arguments, &ref) != nil {
			return host.ToolResult{}, host.ErrInvalid
		}
		v, err := l.services.Resources.ReadResource(ctx, c, ref)
		if err != nil {
			return host.ToolResult{}, err
		}
		// The default artifact store is textual; expose readable model context
		// instead of JSON's base64 encoding of a byte slice.
		value = struct {
			Ref  host.ResourceRef `json:"ref"`
			Text string           `json:"text"`
		}{v.Ref, string(v.Content)}
	default:
		return host.ToolResult{}, host.ErrUnsupported
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return host.ToolResult{}, err
	}
	if err = l.check(ctx, c); err != nil {
		return host.ToolResult{}, err
	}
	return host.ToolResult{Content: raw}, nil
}

type einoTool struct {
	info     *schema.ToolInfo
	provider host.ToolProvider
	call     host.Call
	id       string
}

func (t *einoTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }
func (t *einoTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if !json.Valid([]byte(args)) {
		return "", host.ErrInvalid
	}
	r, err := t.provider.CallTool(ctx, t.call, host.ToolCall{ID: t.id, Arguments: json.RawMessage(args)})
	if err != nil {
		return "", err
	}
	if !json.Valid(r.Content) {
		return "", host.ErrInvalid
	}
	return string(r.Content), nil
}

// EinoTools exposes the host's public read schemas as native Eino tools. host.Call
// identity and opaque binding stay outside model arguments. Effectful tools
// require a host journal supplying native CallID and are rejected here, rather
// than manufacturing an idempotency key from arguments.
func EinoTools(ctx context.Context, p host.ToolProvider, c host.Call) ([]tool.BaseTool, error) {
	if p == nil {
		return nil, host.ErrInvalid
	}
	definitions, err := p.DiscoverTools(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]tool.BaseTool, 0, len(definitions))
	seen := map[string]bool{}
	for _, d := range definitions {
		if d.ID == "" || seen[d.ID] || !json.Valid(d.Schema) {
			return nil, host.ErrInvalid
		}
		if d.Effect {
			return nil, host.ErrUnsupported
		}
		seen[d.ID] = true
		var js jsonschema.Schema
		if json.Unmarshal(d.Schema, &js) != nil || js.Type != "object" {
			return nil, host.ErrInvalid
		}
		bound := c
		bound.Binding = bytes.Clone(c.Binding)
		out = append(out, &einoTool{info: &schema.ToolInfo{Name: d.ID, Desc: d.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&js)}, provider: p, call: bound, id: d.ID})
	}
	return out, nil
}
