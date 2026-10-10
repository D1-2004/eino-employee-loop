package local

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/prototype/core"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// quietModel always stay_quiet — Host must still surface a user-visible ask.
type quietModel struct{ n atomic.Int32 }

func (m *quietModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.n.Add(1)
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       fmt.Sprintf("q-%d", m.n.Load()),
			Type:     "function",
			Function: schema.FunctionCall{Name: "stay_quiet", Arguments: `{}`},
		}},
	}, nil
}
func (m *quietModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}
func (m *quietModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type quietProvider struct{ m *quietModel }

func (p quietProvider) Decision(ctx context.Context) (model.ToolCallingChatModel, error) {
	return p.m, nil
}
func (p quietProvider) Ack(ctx context.Context) (model.BaseChatModel, error) { return p.m, nil }

func TestGreetingNeverReopensTasks(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "greet.db")
	stack, err := Open(ctx, StackOpts{DBPath: db, Executor: "mock", Scene: "greet"})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	// Seed prior weather/fishing work into registry + conversation.
	_, _, _ = stack.SQL.Create(ctx, &core.Task{
		ID: "tsk_weather", Tenant: "test", Scene: "greet", Goal: "查杭州天气并对比钓鱼",
		IdemKey: "w1", Status: core.StDone, Result: "周日适合钓鱼",
	}, time.Minute)
	_, _ = stack.Conv.Append(ctx, "test", "greet", core.Msg{
		Role: "assistant", Speaker: "employee", Text: "本周杭州钓鱼天气对比还在跑", Kind: "reply", AtMs: core.NowMs(),
	})
	var outs []string
	cfg := core.DefaultConfig("test", "greet", spinProvider{m: &spinModel{}}, core.DemoCase(nil, "zh-CN", ""))
	cfg.Locale = "zh-CN" // Chinese input; Host replies keep the original zh-CN text
	cfg.Ack = false
	cfg.Executor = stack.Executor
	cfg.OnOutbound = func(_ context.Context, kind, text string) { outs = append(outs, kind+":"+text) }
	cfg.CheckPointStore = stack.Checkpoint
	cfg.CheckpointID = "test|greet"
	rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()

	rt.UserMessage(ctx, "冬翔", "Hi", 0)
	if !rt.WaitIdle(false, 5*time.Second) {
		t.Fatal("timeout")
	}
	c := rt.Counters()
	if c["host_direct_reply"] < 1 {
		t.Fatalf("expected host_direct_reply, counters=%#v outs=%v", c, outs)
	}
	joined := strings.Join(outs, "\n")
	for _, bad := range []string{"天气", "钓鱼", "任务", "对比"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("greeting reopened %q: %s", bad, joined)
		}
	}
	if !strings.Contains(joined, "reply:") {
		t.Fatalf("no reply: %v", outs)
	}
}

func TestArithmeticHostDirect(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "arith.db")
	stack, err := Open(ctx, StackOpts{DBPath: db, Executor: "mock", Scene: "arith"})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	var outs []string
	cfg := core.DefaultConfig("test", "arith", spinProvider{m: &spinModel{}}, core.DemoCase(nil, "zh-CN", ""))
	cfg.Locale = "zh-CN" // Chinese input; Host replies keep the original zh-CN text
	cfg.Ack = false
	cfg.OnOutbound = func(_ context.Context, kind, text string) { outs = append(outs, text) }
	cfg.CheckPointStore = stack.Checkpoint
	cfg.CheckpointID = "test|arith"
	rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()
	rt.UserMessage(ctx, "冬翔", "1+1", 0)
	if !rt.WaitIdle(false, 5*time.Second) {
		t.Fatal("timeout")
	}
	if rt.Counters()["host_direct_reply"] < 1 {
		t.Fatalf("counters %#v outs %v", rt.Counters(), outs)
	}
	ok := false
	for _, o := range outs {
		if o == "2" {
			ok = true
		}
		if strings.Contains(o, "天气") || strings.Contains(o, "记忆") {
			t.Fatalf("arith leaked context: %v", outs)
		}
	}
	if !ok {
		t.Fatalf("want 2, got %v", outs)
	}
}

func TestStayQuietForcedAsk(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "quiet.db")
	stack, err := Open(ctx, StackOpts{DBPath: db, Executor: "mock", Scene: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	qm := &quietModel{}
	var outs []string
	cfg := core.DefaultConfig("test", "quiet", quietProvider{m: qm}, core.DemoCase(nil, "zh-CN", ""))
	cfg.Locale = "zh-CN" // Chinese input; Host replies keep the original zh-CN text
	cfg.Ack = false
	cfg.BudgetFallbackDispatch = true
	cfg.MaxModelRoundsPerTurn = 2
	cfg.OnOutbound = func(_ context.Context, kind, text string) { outs = append(outs, kind+":"+text) }
	cfg.CheckPointStore = stack.Checkpoint
	cfg.CheckpointID = "test|quiet"
	rt, err := core.NewRuntime(ctx, cfg, stack.SQL, stack.SQL, stack.KV, stack.Conv, core.NewStats())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()

	rt.UserMessage(ctx, "冬翔", "好吧 模型请求的事件", 0)
	if !rt.WaitIdle(false, 10*time.Second) {
		t.Fatal("timeout")
	}
	c := rt.Counters()
	if c["host_forced_visible"] < 1 && !hasAsk(outs) {
		t.Fatalf("expected forced visible ask; counters=%#v outs=%v modelCalls=%d", c, outs, qm.n.Load())
	}
	if !hasAsk(outs) {
		t.Fatalf("no user-visible ask: %v", outs)
	}
}

func hasAsk(outs []string) bool {
	for _, o := range outs {
		if strings.HasPrefix(o, "ask:") || strings.HasPrefix(o, "reply:") {
			return true
		}
	}
	return false
}
