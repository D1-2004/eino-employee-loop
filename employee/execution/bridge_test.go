package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type submissionContext struct {
	frame    contextbuilder.Frame
	checks   int
	revokeAt int
}

func (c *submissionContext) Build(context.Context, contract.Scope, contextbuilder.View, string, string) (contextbuilder.Frame, error) {
	return c.frame, nil
}
func (c *submissionContext) Revalidate(context.Context, contextbuilder.Frame) error {
	c.checks++
	if c.revokeAt > 0 && c.checks >= c.revokeAt {
		return contextbuilder.ErrRevoked
	}
	return nil
}

type submissionPort struct {
	submit func(context.Context, execution.Request) (execution.Record, error)
}

func (p *submissionPort) Submit(c context.Context, r execution.Request) (execution.Record, error) {
	return p.submit(c, r)
}
func (*submissionPort) Lookup(context.Context, work.Scope, string) (execution.Record, error) {
	return execution.Record{}, execution.ErrNotFound
}
func (*submissionPort) Poll(context.Context, work.Scope, string) (execution.Record, error) {
	return execution.Record{}, execution.ErrUnknown
}
func (*submissionPort) Cancel(context.Context, execution.Target) (execution.Record, error) {
	return execution.Record{}, execution.ErrUnknown
}
func (*submissionPort) Close() error { return nil }

func bridgeFixture(t *testing.T) (*execution.Bridge, work.Task, *submissionContext, *submissionPort) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tasks, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	scope := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "scene", Requester: "owner"}
	receipt, err := tasks.Dispatch(ctx, scope, work.Source{Receipt: "source", Actor: "owner", Text: "write"}, "dispatch", work.Definition{Goal: "write report", Instruction: "write and verify report", Configuration: []byte(`{"version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(ctx, scope, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.BeginSubmission(ctx, scope, task.ID, task.CurrentRun().ID); err != nil {
		t.Fatal(err)
	}
	task, err = tasks.Get(ctx, scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	frame := &submissionContext{frame: contextbuilder.Frame{Scope: scope, View: contextbuilder.Execution, Refs: []contextbuilder.Ref{{ID: "source", SHA256: "frozen"}}}}
	port := &submissionPort{}
	return &execution.Bridge{State: db, Port: port, Context: frame, Tasks: tasks, Authorize: func(context.Context, contract.Scope) error { return nil }}, task, frame, port
}

func TestBridgeCommitsUnknownBeforeProviderAndRejectsChangedFrozenRequest(t *testing.T) {
	bridge, task, _, port := bridgeFixture(t)
	ctx := context.Background()
	port.submit = func(c context.Context, request execution.Request) (execution.Record, error) {
		current, err := bridge.Tasks.Get(c, task.Scope, task.ID)
		if err != nil || current.CurrentRun().State != work.RunUnknown {
			t.Fatalf("submission preceded durable unknown: %+v, %v", current, err)
		}
		// The provider keeps the same physical identity but rewrites frozen input.
		request.Frame.Refs[0].SHA256 = "rewritten"
		return execution.Record{ID: "execution", SubmitKey: task.CurrentRun().SubmitKey, TaskID: task.ID, RunID: task.CurrentRun().ID, State: "prepared", Request: request}, nil
	}
	if worked, err := bridge.Submit(ctx, task); !worked || err == nil {
		t.Fatalf("accepted changed input: %v %v", worked, err)
	}
	current, err := bridge.Tasks.Get(ctx, task.Scope, task.ID)
	if err != nil || current.CurrentRun().State != work.RunUnknown {
		t.Fatal(current, err)
	}
}

func TestBridgeRechecksRevocationAfterCommittingUnknownBeforeProvider(t *testing.T) {
	bridge, task, frame, port := bridgeFixture(t)
	frame.revokeAt = 2
	called := false
	port.submit = func(context.Context, execution.Request) (execution.Record, error) {
		called = true
		return execution.Record{}, nil
	}
	_, err := bridge.Submit(context.Background(), task)
	if !errors.Is(err, contextbuilder.ErrRevoked) || called {
		t.Fatal("revoked frozen input crossed provider boundary", called, err)
	}
	current, err := bridge.Tasks.Get(context.Background(), task.Scope, task.ID)
	if err != nil || current.CurrentRun().State != work.RunUnknown {
		t.Fatal(current, err)
	}
}
