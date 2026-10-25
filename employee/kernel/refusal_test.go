package kernel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
)

func TestRecoverableBatchDoesNotFinishOrExecuteFollowingEffect(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := looptest.New(
		looptest.Tools(looptest.Call{Name: "reject", ID: "bad", Args: map[string]any{}}, looptest.Call{Name: "finish", ID: "premature", Args: map[string]any{}}),
		looptest.Tools(looptest.Call{Name: "finish", ID: "corrected", Args: map[string]any{}}),
	)
	effects := 0
	runner := &kernel.Runner{State: db, Owner: "process", Authorize: func(context.Context, contract.Scope) error { return nil }, Models: func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error) { return m, nil }}
	p := contract.Profile{ID: "test", Revision: "1", ModelName: "scripted", MaxModelCalls: 6}
	scope := contract.Scope{Workspace: "w", Tenant: "t", Employee: "e", Scene: "dm", Requester: "o"}
	tools := []*kernel.Function{
		{Spec: kernel.Spec("reject", "Reject without effect", map[string]any{}), Invoke: func(context.Context, string, map[string]any) (any, error) {
			return nil, kernel.Recoverable(errors.New("choose a valid target"))
		}},
		{Spec: kernel.Spec("finish", "Accepted terminal effect", map[string]any{}), Invoke: func(context.Context, string, map[string]any) (any, error) {
			effects++
			return map[string]bool{"done": true}, nil
		}},
	}
	out, err := runner.Run(ctx, kernel.Invocation{Profile: p, Scope: scope, Role: "test", Receipt: "batch", System: "Use tools", Context: map[string]any{}, Tools: tools, Terminal: map[string]bool{"finish": true}})
	if err != nil || !out.Completed || m.Calls() != 2 || effects != 1 {
		t.Fatalf("refusal bypassed: err=%v complete=%v models=%d effects=%d", err, out.Completed, m.Calls(), effects)
	}
}
func TestUnmarkedPermissionFailureIsNotModelCorrection(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := looptest.New(looptest.Tools(looptest.Call{Name: "finish", ID: "one", Args: map[string]any{}}))
	effects := 0
	runner := &kernel.Runner{State: db, Owner: "process", Authorize: func(context.Context, contract.Scope) error { return nil }, Models: func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error) { return m, nil }}
	f := &kernel.Function{Spec: kernel.Spec("finish", "Do work", map[string]any{}), Before: func(context.Context) error { return errors.New("current source permission denied") }, Invoke: func(context.Context, string, map[string]any) (any, error) { effects++; return "done", nil }}
	out, err := runner.Run(ctx, kernel.Invocation{Profile: contract.Profile{ID: "test", Revision: "1", ModelName: "scripted", MaxModelCalls: 4}, Scope: contract.Scope{Workspace: "w", Tenant: "t", Employee: "e", Scene: "dm", Requester: "o"}, Role: "test", Receipt: "denied", System: "Use tools", Context: map[string]any{}, Tools: []*kernel.Function{f}, Terminal: map[string]bool{"finish": true}})
	if err == nil || out.Completed || effects != 0 || m.Calls() != 1 {
		t.Fatal("permission error treated as correction", err, out.Completed, effects, m.Calls())
	}
}
