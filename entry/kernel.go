package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/internal/jsoncheck"
	"github.com/D1-2004/eino-employee-loop/journal"
	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/D1-2004/eino-employee-loop/workflow"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type function struct {
	spec   loop.Tool
	info   *schema.ToolInfo
	before func(context.Context) error
	run    func(context.Context, string, map[string]any) (any, error)
}

func (f *function) Info(context.Context) (*schema.ToolInfo, error) { return f.info, nil }
func (f *function) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	if f.before != nil {
		if err := f.before(ctx); err != nil {
			return "", err
		}
	}
	args, err := arguments(f.spec, raw)
	if err != nil {
		return "", err
	}
	id := compose.GetToolCallID(ctx)
	if id == "" {
		return "", errors.New("entry: native call identity required")
	}
	out, err := f.run(ctx, id, args)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(out)
	return string(data), err
}
func arguments(spec loop.Tool, raw string) (map[string]any, error) {
	if err := jsoncheck.Unique([]byte(raw), 32); err != nil {
		return nil, err
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, errors.New("entry: object arguments required")
	}
	if p := loop.ValidateArguments(spec.Schema, args); len(p) > 0 {
		return nil, fmt.Errorf("invalid %s: %s", spec.Name, strings.Join(p, "; "))
	}
	return args, nil
}
func str(args map[string]any, key string) string    { v, _ := args[key].(string); return v }
func number(args map[string]any, key string) uint64 { v, _ := args[key].(float64); return uint64(v) }
func spec(name, description string, properties map[string]any, required ...string) loop.Tool {
	return loop.Tool{Name: name, Description: description, Schema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}
func stringField() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
func (f *function) bind() error {
	infos, err := loop.ToolInfos([]loop.Tool{f.spec})
	if err == nil {
		f.info = infos[0]
	}
	return err
}
func (s *standalone) runAgent(ctx context.Context, profile Profile, scope work.Scope, role, receipt string, frame contextbuilder.Frame, system string, functions []*function, terminal map[string]bool, allowText bool, targets ...map[string]any) (native.Result, error) {
	specs := map[string]loop.Tool{}
	nativeTools := []tool.BaseTool{}
	for _, f := range functions {
		if err := f.bind(); err != nil {
			return native.Result{}, err
		}
		specs[f.spec.Name] = f.spec
		nativeTools = append(nativeTools, f)
	}
	before := func(c context.Context) error {
		if err := s.authorize(c, scope); err != nil {
			return err
		}
		return s.context.Revalidate(c, frame)
	}
	if profile.HostTools && role == "executor" {
		extras, e := s.hostFunctions(ctx, scope, receipt, frame, before)
		if e != nil {
			return native.Result{}, e
		}
		for _, f := range extras {
			if _, exists := specs[f.spec.Name]; exists {
				return native.Result{}, errors.New("entry: Host tool name collides with a business tool")
			}
			if e = f.bind(); e != nil {
				return native.Result{}, e
			}
			specs[f.spec.Name] = f.spec
			nativeTools = append(nativeTools, f)
		}
	}
	modelRequest := ModelRequest{Role: role, Receipt: receipt, Scope: scope, Profile: profile, ModelName: profile.ModelName}
	delegate, err := s.opts.Models(ctx, modelRequest)
	if err != nil {
		return native.Result{}, err
	}
	scopeKey, _ := scope.Key()
	durable := &journal.Model{DB: s.db, Scope: scopeKey + "/" + role, Receipt: receipt, Owner: s.owner, Name: profile.ModelName, Limit: profile.MaxModelCalls, Delegate: delegate, Before: before}
	validate := func(msg *schema.Message) error {
		if msg == nil || msg.ResponseMeta == nil {
			return errors.New("missing complete response metadata")
		}
		if len(msg.ToolCalls) == 0 {
			if allowText && msg.ResponseMeta.FinishReason == "stop" && strings.TrimSpace(msg.Content) != "" {
				return nil
			}
			return errors.New("finish with a native terminal tool; ordinary content is private")
		}
		if msg.ResponseMeta.FinishReason != "tool_calls" {
			return errors.New("tool calls require complete tool_calls finish reason")
		}
		seen := map[string]bool{}
		terminals := 0
		for _, call := range msg.ToolCalls {
			if call.ID == "" || call.Type != "function" || seen[call.ID] {
				return errors.New("invalid native call identity")
			}
			seen[call.ID] = true
			tool, known := specs[call.Function.Name]
			if !known {
				return errors.New("tool not offered")
			}
			if _, e := arguments(tool, call.Function.Arguments); e != nil {
				return e
			}
			if terminal[call.Function.Name] {
				terminals++
			}
		}
		if terminals > 1 {
			return errors.New("one terminal operation per batch")
		}
		return nil
	}
	factory := func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
		return &adk.ChatModelAgentConfig{Name: role, Model: durable, MaxIterations: profile.MaxModelCalls, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: nativeTools, ExecuteSequentially: true}, ReturnDirectly: terminal}, ModelRetryConfig: &adk.ModelRetryConfig{MaxRetries: 2, BackoffFunc: func(context.Context, int) time.Duration { return 0 }, ShouldRetry: func(_ context.Context, c *adk.RetryContext) *adk.RetryDecision {
			if c.Err != nil {
				return &adk.RetryDecision{RewriteError: c.Err}
			}
			if err := validate(c.OutputMessage); err != nil {
				return &adk.RetryDecision{Retry: true, ModifiedInputMessages: append(append([]*schema.Message(nil), c.InputMessages...), schema.UserMessage("Host validation: "+err.Error()+". Use the frozen native tool contract; no extra permissions.")), PersistModifiedInputMessages: true}
			}
			return &adk.RetryDecision{}
		}}}, nil
	}
	cfg := native.Config{Name: role, Version: "local-el-native/1", Authorize: func(c context.Context, _ native.Request) error { return before(c) }, Factory: factory, Callbacks: s.opts.Callbacks, Stores: func(_ context.Context, request native.Request, version string) (adk.CheckPointStore, error) {
		key, _ := json.Marshal(request.Key)
		return s.db.CheckpointStore(string(key), version)
	}}
	cfg.ToolsReady = func(_ context.Context, _ native.Request, infos []*schema.ToolInfo) error {
		for _, info := range infos {
			if _, ok := specs[info.Name]; ok {
				continue
			}
			js, e := info.ParamsOneOf.ToJSONSchema()
			if e != nil {
				return e
			}
			raw, e := json.Marshal(js)
			if e != nil {
				return e
			}
			var params map[string]any
			if e = json.Unmarshal(raw, &params); e != nil {
				return e
			}
			specs[info.Name] = loop.Tool{Name: info.Name, Description: info.Desc, Schema: params}
		}
		return nil
	}
	cfg, err = s.opts.Plugins.Resolve(profile.Plugins, cfg)
	if err != nil {
		return native.Result{}, err
	}
	r, err := native.New(cfg)
	if err != nil {
		return native.Result{}, err
	}
	messages := []*schema.Message{schema.SystemMessage(system + "\n" + profile.Instructions), schema.UserMessage(string(encode(frame)))}
	key := workflow.Key{Profile: profile.ID, ProfileRevision: profile.Revision, ProfileBinding: role, Workspace: scope.Workspace, Tenant: scope.Tenant, Employee: scope.Employee, Scene: scope.Scene, Lane: role, Subject: scope.Requester}
	req := native.Request{Key: key, Receipt: receipt, Messages: messages}
	if len(targets) > 0 && targets[0] != nil {
		return r.Resume(ctx, req, targets[0])
	}
	cp, e := s.db.CheckpointStore(string(encode(key)), "local-el-native/1")
	if e != nil {
		return native.Result{}, e
	}
	if _, exists, e := cp.Get(ctx, receipt); e != nil {
		return native.Result{}, e
	} else if exists {
		return r.Recover(ctx, req)
	}
	return r.Run(ctx, req)
}
