package local

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/dingtalk-fde/eino-employee-loop/core"
)

// spinModel always calls a non-terminal tool so the agent burns MaxIterations.
type spinModel struct{ n atomic.Int32 }

func (m *spinModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	i := m.n.Add(1)
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   fmt.Sprintf("spin-%d", i),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "memory_lookup",
				Arguments: `{"source_ref":"src","scope":"me","query":"x"}`,
			},
		}},
	}, nil
}

func (m *spinModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *spinModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type spinProvider struct{ m *spinModel }

func (p spinProvider) Decision(ctx context.Context) (model.ToolCallingChatModel, error) { return p.m, nil }
func (p spinProvider) Ack(ctx context.Context) (model.BaseChatModel, error)             { return p.m, nil }

type sinkEvents struct{ kinds []string }

func (s *sinkEvents) Emit(_ context.Context, ev core.ProcessEvent) error {
	s.kinds = append(s.kinds, ev.Kind+":"+ev.Text)
	return nil
}

func TestBudgetFallbackAutoDispatch(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "bf.db")
	stack, err := Open(ctx, StackOpts{DBPath: db, Executor: "mock", Scene: "bf"})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	spin := &spinModel{}
	stack.Models = spinProvider{m: spin}
	sink := &sinkEvents{}
	var outs []string
	cfg := core.DefaultConfig("test", "bf", stack.Models, core.DemoCase(""))
	cfg.MaxModelRoundsPerTurn = 2
	cfg.BudgetFallbackDispatch = true
	cfg.Ack = false
	cfg.JobDelay = 10 * time.Millisecond
	cfg.Executor = stack.Executor
	cfg.Events = sink
	cfg.OnOutbound = func(_ context.Context, kind, text string) { outs = append(outs, kind+":"+text) }
	cfg.CheckPointStore = stack.Checkpoint
	cfg.CheckpointID = "test|bf"
	rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()

	rt.UserMessage(ctx, "冬翔", "帮我查一下杭州明天的天气并整理成简短摘要", 0)
	if !rt.WaitIdle(true, 15*time.Second) {
		t.Fatal("timeout waiting for idle")
	}

	c := rt.Counters()
	if c["budget_fallback_dispatch"] < 1 && c["budget_exhausted"] < 1 {
		t.Fatalf("expected budget fallback counters, got %#v outs=%v events=%v modelCalls=%d", c, outs, sink.kinds, spin.n.Load())
	}
	tasks, err := rt.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 1 {
		t.Fatalf("expected Host auto-dispatch to create a task; counters=%#v outs=%v events=%v", c, outs, sink.kinds)
	}
	foundBudget := false
	for _, k := range sink.kinds {
		if len(k) >= len(core.EvBudget) && k[:len(core.EvBudget)] == core.EvBudget {
			foundBudget = true
			break
		}
	}
	if !foundBudget {
		t.Fatalf("expected EvBudget process event, got %v", sink.kinds)
	}
}

func TestBudgetFallbackSkipsGreeting(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "bf2.db")
	stack, err := Open(ctx, StackOpts{DBPath: db, Executor: "mock", Scene: "bf2"})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	spin := &spinModel{}
	stack.Models = spinProvider{m: spin}
	sink := &sinkEvents{}
	cfg := core.DefaultConfig("test", "bf2", stack.Models, core.DemoCase(""))
	cfg.MaxModelRoundsPerTurn = 2
	cfg.BudgetFallbackDispatch = true
	cfg.Ack = false
	cfg.JobDelay = 10 * time.Millisecond
	cfg.Executor = stack.Executor
	cfg.Events = sink
	cfg.CheckPointStore = stack.Checkpoint
	cfg.CheckpointID = "test|bf2"
	rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()

	rt.UserMessage(ctx, "冬翔", "Hi", 0)
	if !rt.WaitIdle(false, 15*time.Second) {
		t.Fatal("timeout")
	}
	c := rt.Counters()
	if c["budget_fallback_dispatch"] != 0 {
		t.Fatalf("greeting must not auto-dispatch: %#v", c)
	}
	tasks, _ := rt.ListTasks(ctx)
	if len(tasks) != 0 {
		t.Fatalf("greeting invented tasks: %d", len(tasks))
	}
}
