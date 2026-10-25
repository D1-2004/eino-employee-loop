package eino_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func request() native.Request {
	return native.Request{Key: workflow.Key{Workspace: "local", Tenant: "owner-org", Employee: "employee", Scene: "scene-one", Lane: "foreground", Subject: "interactive"}, Receipt: "input-one", Messages: []*schema.Message{schema.UserMessage("start")}}
}
func store(t *testing.T, dir string) *sqlite.Store {
	t.Helper()
	s, e := sqlite.Open(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func config(s *sqlite.Store, f native.Factory) native.Config {
	return native.Config{Name: "employee", Version: "native-v1", Authorize: func(_ context.Context, r native.Request) error {
		if r.Key.Workspace != "local" || r.Key.Tenant != "owner-org" {
			return native.ErrDenied
		}
		return nil
	}, Factory: f, Stores: func(_ context.Context, r native.Request, v string) (adk.CheckPointStore, error) {
		scope, _ := json.Marshal(r.Key)
		return s.Checkpoints(string(scope), v)
	}}
}

type localTool struct {
	name string
	fn   func(context.Context, string) (string, error)
}

func (t localTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: t.name, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
}
func (t localTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	return t.fn(ctx, args)
}
func checkpointRuntime(t *testing.T, db *sqlite.Store, m model.ToolCallingChatModel) *native.Runtime {
	factory := func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
		write := localTool{name: "write_receipt", fn: func(c context.Context, _ string) (string, error) {
			// Deliberately not deduplicated: this detects actual Eino tool replay.
			n := 0
			d, e := db.Get(c, "effects", "scope", "writes")
			if e == nil {
				if e = json.Unmarshal(d.Body, &n); e != nil {
					return "", e
				}
			} else if !errors.Is(e, sqlite.ErrNotFound) {
				return "", e
			}
			n++
			raw, _ := json.Marshal(n)
			_, e = db.Put(c, sqlite.Document{Namespace: "effects", Scope: "scope", ID: "writes", Revision: d.Revision, Body: raw})
			return "receipt-one", e
		}}
		ask := localTool{name: "ask_user", fn: func(c context.Context, args string) (string, error) {
			was, has, saved := tool.GetInterruptState[string](c)
			if !was {
				return "", tool.StatefulInterrupt(c, "question-one", args)
			}
			if !has {
				return "", errors.New("missing saved tool state")
			}
			target, hasAnswer, answer := tool.GetResumeContext[string](c)
			if !target || !hasAnswer {
				return "", tool.StatefulInterrupt(c, "question-one", saved)
			}
			if answer != "Q3" {
				return "", errors.New("unaccepted answer")
			}
			return "verified answer Q3", nil
		}}
		return &adk.ChatModelAgentConfig{Name: "employee", Model: m, MaxIterations: 8, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{write, ask}, ExecuteSequentially: true}}}, nil
	}
	r, e := native.New(config(db, factory))
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestNativeCheckpointRebuildResumesToolWithoutRepeatingEarlierEffect(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := store(t, dir)

	m := looptest.New(looptest.Tools(looptest.Call{ID: "write-one", Name: "write_receipt", Args: map[string]any{}}, looptest.Call{ID: "ask-one", Name: "ask_user", Args: map[string]any{}}))
	r := checkpointRuntime(t, s, m)
	req := request()
	out, e := r.Run(ctx, req)
	if e != nil || out.Completed || len(out.Interrupts) != 1 || m.Calls() != 1 {
		t.Fatal(out, e, m.Calls())
	}
	target := out.Interrupts[0].ID
	if _, e = r.Run(ctx, req); !errors.Is(e, native.ErrResumeRequired) {
		t.Fatal("interrupted run regenerated", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	// Reconstruct both SQLite and Eino Runner; no old Go runtime state survives.
	s = store(t, dir)
	defer s.Close()
	after := looptest.New(looptest.Text("verified Q3 continuation"))
	r = checkpointRuntime(t, s, after)
	foreign := req
	foreign.Key.Scene = "other-scene"
	if _, e = r.Resume(ctx, foreign, map[string]any{target: "Q3"}); e == nil {
		t.Fatal("checkpoint crossed scenes")
	}
	out, e = r.Resume(ctx, req, map[string]any{target: "Q3"})
	if e != nil || !out.Completed || len(out.Interrupts) != 0 || out.Output.Content != "verified Q3 continuation" || after.Calls() != 1 {
		t.Fatal(out, e, after.Calls())
	}
	d, e := s.Get(ctx, "effects", "scope", "writes")
	if e != nil || string(d.Body) != "1" {
		t.Fatal("Eino repeated a completed tool", string(d.Body), e)
	}
	found := false
	for _, ev := range out.Events {
		if ev.Message != nil && ev.Message.Role == schema.Tool && ev.Message.ToolName == "ask_user" {
			found = true
		}
	}
	if !found {
		t.Fatal("resumed tool result lost from standard event stream", out.Events)
	}
}

type pluginKey struct{}
type traceKey struct{}
type handler struct {
	adk.BaseChatModelAgentMiddleware
	calls *atomic.Int32
}

func (h *handler) BeforeModelRewriteState(c context.Context, s *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	h.calls.Add(1)
	return context.WithValue(c, pluginKey{}, true), s, nil
}

type markedModel struct {
	inner         *looptest.Model
	plugin, trace *atomic.Bool
}

func (m *markedModel) WithTools(t []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	_, e := m.inner.WithTools(t)
	return m, e
}
func (m *markedModel) Generate(c context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	v, _ := c.Value(pluginKey{}).(bool)
	m.plugin.Store(v)
	v, _ = c.Value(traceKey{}).(bool)
	m.trace.Store(v)
	return m.inner.Generate(c, in, opts...)
}
func (m *markedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not streamed")
}
func TestNativeStandardHandlersCallbacksAndAgentToolEvents(t *testing.T) {
	s := store(t, t.TempDir())
	defer s.Close()
	var plugins, traces atomic.Int32
	var pluginSeen, traceSeen atomic.Bool
	parent := &markedModel{inner: looptest.New(looptest.Tools(looptest.Call{ID: "sub-one", Name: "research", Args: map[string]any{"request": "verify local context"}}), looptest.Text("done")), plugin: &pluginSeen, trace: &traceSeen}
	f := func(c context.Context, _ native.Request) (*adk.ChatModelAgentConfig, error) {
		child, e := adk.NewChatModelAgent(c, &adk.ChatModelAgentConfig{Name: "research", Description: "Read-only verification subagent", Model: looptest.New(looptest.Text("local fact verified")), MaxIterations: 2})
		if e != nil {
			return nil, e
		}
		return &adk.ChatModelAgentConfig{Name: "employee", Model: parent, MaxIterations: 4, Handlers: []adk.ChatModelAgentMiddleware{&handler{calls: &plugins}}, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{adk.NewAgentTool(c, child)}, ExecuteSequentially: true}, EmitInternalEvents: true}}, nil
	}
	cfg := config(s, f)
	cfg.Callbacks = []callbacks.Handler{callbacks.NewHandlerBuilder().OnStartFn(func(c context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		traces.Add(1)
		return context.WithValue(c, traceKey{}, true)
	}).Build()}
	r, e := native.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := r.Run(context.Background(), request())
	if e != nil || !out.Completed || out.Output.Content != "done" || plugins.Load() != 2 || traces.Load() == 0 || !pluginSeen.Load() || !traceSeen.Load() {
		t.Fatal(out, e, plugins.Load(), traces.Load(), pluginSeen.Load(), traceSeen.Load())
	}
	childEvent := false
	for _, ev := range out.Events {
		if ev.Agent == "research" {
			childEvent = true
		}
	}
	if !childEvent {
		t.Fatal("AgentTool events were swallowed", out.Events)
	}
}

type blockingModel struct{ started, exited chan struct{} }

func (m *blockingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *blockingModel) Generate(c context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	close(m.started)
	if m.exited != nil {
		defer close(m.exited)
	}
	<-c.Done()
	return nil, c.Err()
}
func (m *blockingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not streamed")
}
func TestNativeCancellationCheckpointsInsteadOfAbortingPersistence(t *testing.T) {
	s := store(t, t.TempDir())
	defer s.Close()
	m := &blockingModel{started: make(chan struct{}), exited: make(chan struct{})}
	cfg := config(s, func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
		return &adk.ChatModelAgentConfig{Name: "employee", Model: m, MaxIterations: 2}, nil
	})
	r, e := native.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		out native.Result
		err error
	}
	done := make(chan answer, 1)
	go func() { out, err := r.Run(ctx, request()); done <- answer{out, err} }()
	<-m.started
	cancel()
	var a answer
	select {
	case a = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("native cancellation did not finish")
	}
	select {
	case <-m.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("Generate kept running after its checkpoint was saved")
	}
	if !a.out.Cancelled || a.out.Completed || !errors.Is(a.err, context.Canceled) {
		t.Fatal(a)
	}
	scope, _ := json.Marshal(request().Key)
	cp, _ := s.Checkpoints(string(scope), "native-v1")
	_, exists, e := cp.Get(context.Background(), request().Receipt)
	if e != nil || !exists {
		t.Fatal("cancellation did not persist native checkpoint", exists, e)
	}
}

func TestNativeCrossProcessCheckpointResume(t *testing.T) {
	if dir := os.Getenv("EEL_NATIVE_CHECKPOINT_CHILD"); dir != "" {
		s := store(t, dir)
		defer s.Close()
		r := checkpointRuntime(t, s, looptest.New(looptest.Tools(looptest.Call{ID: "write-one", Name: "write_receipt", Args: map[string]any{}}, looptest.Call{ID: "ask-one", Name: "ask_user", Args: map[string]any{}})))
		result, err := r.Run(context.Background(), request())
		if err != nil || len(result.Interrupts) != 1 {
			t.Fatal(result, err)
		}
		raw, _ := json.Marshal(result.Interrupts[0].ID)
		if err = os.WriteFile(filepath.Join(dir, "interrupt.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeCrossProcessCheckpointResume$")
	cmd.Env = append(os.Environ(), "EEL_NATIVE_CHECKPOINT_CHILD="+dir)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("native interrupt child failed", err, string(raw))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "interrupt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var target string
	if err = json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	s := store(t, dir)
	defer s.Close()
	model := looptest.New(looptest.Text("Q3 recovered in a different process"))
	r := checkpointRuntime(t, s, model)
	result, err := r.Resume(ctx, request(), map[string]any{target: "Q3"})
	if err != nil || !result.Completed || result.Output.Content != "Q3 recovered in a different process" || model.Calls() != 1 {
		t.Fatal(result, err, model.Calls())
	}
	d, err := s.Get(ctx, "effects", "scope", "writes")
	if err != nil || string(d.Body) != "1" {
		t.Fatal("completed child tool repeated", string(d.Body), err)
	}
}

func TestNativeCancellationStopsToolAndRecoveryDoesNotRepeatCompletedEffect(t *testing.T) {
	db := store(t, t.TempDir())
	defer db.Close()
	var writes atomic.Int32
	var recovering atomic.Bool
	started, exited := make(chan struct{}), make(chan struct{})
	factory := func(m model.ToolCallingChatModel) native.Factory {
		return func(context.Context, native.Request) (*adk.ChatModelAgentConfig, error) {
			write := localTool{name: "write_once", fn: func(context.Context, string) (string, error) { writes.Add(1); return "receipt-one", nil }}
			wait := localTool{name: "wait", fn: func(ctx context.Context, _ string) (string, error) {
				if recovering.Load() {
					return "resumed", nil
				}
				close(started)
				defer close(exited)
				<-ctx.Done()
				return "", ctx.Err()
			}}
			return &adk.ChatModelAgentConfig{Name: "employee", Model: m, MaxIterations: 5, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{write, wait}, ExecuteSequentially: true}}}, nil
		}
	}
	first := looptest.New(looptest.Tools(looptest.Call{ID: "write-1", Name: "write_once", Args: map[string]any{}}), looptest.Tools(looptest.Call{ID: "wait-1", Name: "wait", Args: map[string]any{}}))
	rt, err := native.New(config(db, factory(first)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); _, runErr = rt.Run(ctx, request()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("native cancellation did not finish")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("tool retained its uncancelled context")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatal(runErr)
	}
	recovering.Store(true)
	rt, err = native.New(config(db, factory(looptest.New(looptest.Text("done")))))
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Recover(context.Background(), request())
	if err != nil || !out.Completed || writes.Load() != 1 {
		t.Fatalf("out=%+v err=%v writes=%d", out, err, writes.Load())
	}
}
