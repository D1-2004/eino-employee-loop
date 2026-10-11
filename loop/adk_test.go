package loop_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/loop"
	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
)

// The tests below pin what the loop relies on from Eino's ADK and the
// parity golden cannot see.

func TestRequestsAreTheTranscriptsOwnMessages(t *testing.T) {
	reply := lt.Tools(lt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}})
	m := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), lt.Truncated("cut"), reply)
	var observed [][]*schema.Message
	out, err := run(t, m, &fakeHost{}, func(c *loop.Config) {
		c.OnModelCall = func(_ context.Context, mc loop.ModelCall) { observed = append(observed, mc.Request) }
	})
	if err != nil || out.Reply != "done" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	wantLens := []int{2, 4, 5}
	for i, req := range m.Requests {
		if len(req) != wantLens[i] || len(observed[i]) != wantLens[i] {
			t.Fatalf("request %d has %d messages, want %d", i, len(req), wantLens[i])
		}
		for j, msg := range req {
			if msg != out.Transcript[j] || observed[i][j] != msg || msg.Extra != nil {
				t.Fatalf("request %d message %d is not the transcript's own message: %+v", i, j, msg)
			}
		}
		if m.Options[i].Tools != nil {
			t.Fatalf("request %d got ADK's tool option; tools are bound per attempt", i)
		}
	}
	if len(out.Transcript) != 7 || out.Transcript[5] != reply.Message {
		t.Fatalf("the transcript keeps the provider's own message: %v", out.Transcript)
	}
}

// Production budgets outlast ADK's defaults (20 iterations, 3 retries): the
// wake budgets, never ADK's limits, end a long wake.
func TestLongWakesOutlastADKLimits(t *testing.T) {
	cases := []struct {
		name        string
		budget      loop.Budget
		reads, cuts int
	}{
		{"Multica's budget", loop.Budget{MaxModelResponses: 8, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 11}, 7, 0},
		{"more batches than 20 iterations", loop.Budget{MaxModelResponses: 30, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 40}, 24, 0},
		{"a long error streak", loop.Budget{MaxModelResponses: 3, MaxConsecutiveErrors: 12, MaxPhysicalAttempts: 12}, 0, 9},
	}
	for _, c := range cases {
		var steps []lt.Step
		for i := 0; i < c.reads; i++ {
			steps = append(steps, lt.Tools(lt.Call{ID: fmt.Sprintf("c%d", i), Name: "read_task", Args: map[string]any{"task_ref": "t1"}}))
		}
		for i := 0; i < c.cuts; i++ {
			steps = append(steps, lt.Truncated("cut"))
		}
		steps = append(steps, lt.Tools(lt.Call{ID: "r", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}))
		out, err := run(t, lt.New(steps...), &fakeHost{}, func(cfg *loop.Config) { cfg.Budget = c.budget })
		if err != nil || out.Reply != "done" || out.ModelCalls != c.reads+c.cuts+1 {
			t.Errorf("%s: err=%v out=%+v", c.name, err, out)
		}
	}
}

func TestBatchRunsSequentiallyInCallOrder(t *testing.T) {
	var inFlight, overlap atomic.Int32
	var order []string
	host := loop.HostFunc(func(_ context.Context, _ loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
		if inFlight.Add(1) > 1 {
			overlap.Add(1)
		}
		defer inFlight.Add(-1)
		order = append(order, call.NativeToolCallID)
		if call.NativeToolCallID == "c1" {
			time.Sleep(20 * time.Millisecond)
		}
		if call.Name == "reply" {
			return loop.ToolResult{Content: "sent", Receipt: "out-1", Terminal: &loop.Decision{Kind: loop.Reply, Reply: "ok"}}, nil
		}
		return loop.ToolResult{Content: "read"}, nil
	})
	read := func(id string) lt.Call {
		return lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": id}}
	}
	m := lt.New(lt.Tools(read("c1"), read("c2"), read("c3")), lt.Tools(lt.Call{ID: "c4", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "ok"}}))
	out, err := run(t, m, host, nil)
	if err != nil || out.Reply != "ok" || overlap.Load() != 0 || len(order) != 4 || order[0] != "c1" || order[1] != "c2" || order[2] != "c3" {
		t.Fatalf("out=%+v err=%v order=%v overlap=%d", out, err, order, overlap.Load())
	}
	for i, o := range out.ToolOutcomes {
		if o.NativeToolCallID != order[i] {
			t.Fatalf("outcomes follow call order: %+v", out.ToolOutcomes)
		}
	}
}

type panickingFinalText struct{ fakeHost }

func (panickingFinalText) AcceptFinalText(context.Context, loop.Identity, string) (loop.ToolResult, error) {
	panic("final text bug")
}

func TestPanicsBecomeErrors(t *testing.T) {
	read := func() *lt.Model {
		return lt.New(lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}))
	}
	cases := map[string]func() error{
		"host bug": func() error {
			host := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) { panic("host bug") })
			_, err := loop.New(loop.Config{Tools: tools}, read(), host).Run(context.Background(), wake())
			return err
		},
		"final text bug": func() error {
			_, err := run(t, lt.New(lt.Text("2")), &panickingFinalText{}, func(c *loop.Config) { c.RequireTerminalTool, c.AcceptFinalText = true, true })
			return err
		},
		"model bug": func() error {
			_, err := run(t, lt.New(lt.Func(func([]*schema.Message) lt.Step { panic("model bug") })), &fakeHost{}, nil)
			return err
		},
		"validator bug": func() error {
			_, err := run(t, read(), &fakeHost{}, func(c *loop.Config) {
				c.ValidateBatch = func(int, []loop.ToolOutcome, []loop.ToolCall) error { panic("validator bug") }
			})
			return err
		},
	}
	for value, wake := range cases {
		if err := wake(); err == nil || !strings.Contains(err.Error(), "panic error: "+value) {
			t.Errorf("%s: err=%v", value, err)
		}
	}
}

var (
	componentCallbacks atomic.Int32
	countCallbacks     sync.Once
)

// The Host model and the Host tools report their own callbacks; ADK adds
// none around them.
func TestADKAddsNoModelOrToolCallbacks(t *testing.T) {
	countCallbacks.Do(func() {
		callbacks.AppendGlobalHandlers(callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			if info != nil && (info.Component == components.ComponentOfChatModel || info.Component == components.ComponentOfTool) {
				componentCallbacks.Add(1)
			}
			return ctx
		}).Build())
	})
	componentCallbacks.Store(0)
	m := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), lt.Text("done"))
	if _, err := run(t, m, &fakeHost{}, nil); err != nil {
		t.Fatal(err)
	}
	if n := componentCallbacks.Load(); n != 0 {
		t.Fatalf("ADK reported %d ChatModel/Tool callbacks", n)
	}
}

// Handlers carried by ctx see nothing of ADK's graph, so a wake that ends
// on a terminal tool is not reported as failed.
func TestCtxCallbackHandlersSeeNoADKGraph(t *testing.T) {
	var events atomic.Int32
	h := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			events.Add(1)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
			events.Add(1)
			return ctx
		}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Name: "caller"}, h)
	m := lt.New(lt.Tools(lt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"task_ref": "t1"}}), lt.Tools(lt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "ok"}}))
	out, err := loop.New(loop.Config{Tools: tools}, m, &fakeHost{}).Run(ctx, wake())
	if err != nil || out.Reply != "ok" || events.Load() != 0 {
		t.Fatalf("err=%v out=%+v callback events=%d", err, out, events.Load())
	}
}
