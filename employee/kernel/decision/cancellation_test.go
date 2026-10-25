package decision_test

import (
	"context"
	"errors"
	"testing"
	"time"

	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	lt "github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type cancellationModel struct{ started, exited chan struct{} }

func (m *cancellationModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *cancellationModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	close(m.started)
	defer close(m.exited)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *cancellationModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func waitCancellation(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cooperative I/O remained alive after cancellation")
	}
}
func TestCancellationStopsCompatibilityModelAndDoesNotExecuteTools(t *testing.T) {
	m := &cancellationModel{started: make(chan struct{}), exited: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var err error
	h := &fakeHost{}
	go func() { defer close(done); _, err = loop.New(loop.Config{Tools: tools}, m, h).Run(ctx, wake()) }()
	waitCancellation(t, m.started)
	cancel()
	waitCancellation(t, done)
	waitCancellation(t, m.exited)
	if !errors.Is(err, context.Canceled) || len(h.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, h.calls)
	}
}
func TestCancellationStopsCompatibilityHostWithoutRetryingEffects(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	host := loop.HostFunc(func(ctx context.Context, _ loop.Identity, _ loop.ToolCall) (loop.ToolResult, error) {
		calls++
		close(started)
		defer close(exited)
		<-ctx.Done()
		return loop.ToolResult{}, ctx.Err()
	})
	m := lt.New(lt.Tools(lt.Call{ID: "effect-one", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "reply": "on it"}}))
	done := make(chan struct{})
	var err error
	go func() { defer close(done); _, err = loop.New(loop.Config{Tools: tools}, m, host).Run(ctx, wake()) }()
	waitCancellation(t, started)
	cancel()
	waitCancellation(t, done)
	waitCancellation(t, exited)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
