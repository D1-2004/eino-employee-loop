package chat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/memory"
	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	native "github.com/D1-2004/eino-employee-loop/kernel/eino"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/interaction"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

type runFunc func(context.Context, kernel.Invocation) (native.Result, error)

func (f runFunc) Run(ctx context.Context, in kernel.Invocation) (native.Result, error) {
	return f(ctx, in)
}

type scheduleStub struct{}

func (scheduleStub) Schedule(context.Context, contract.Scope, work.Schedule) (work.Schedule, error) {
	return work.Schedule{}, nil
}
func (scheduleStub) Observe(context.Context, contract.Scope, work.Attention) (work.Attention, error) {
	return work.Attention{}, nil
}
func (scheduleStub) Attention(context.Context, contract.Scope) ([]work.Attention, error) {
	return nil, nil
}
func (scheduleStub) ResolveAttention(context.Context, contract.Scope, string, string, work.Source) error {
	return nil
}

func TestLegacyFailedAdmissionRetriesFrozenFrameAndRechecksRevocation(t *testing.T) {
	ctx := context.Background()
	db := memory.New()
	defer db.Close()
	tasks, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	p := contract.Profile{ID: "profile", Revision: "new", Workspace: "workspace", Tenant: "tenant", Employee: "employee", Owner: "human", Backend: "standalone", Scenes: []string{"scene"}}
	scope := p.Scope("scene")
	authorize := func(_ context.Context, s contract.Scope) error {
		if s != scope {
			return work.ErrScope
		}
		return nil
	}
	builder, err := contextbuilder.New(db, memory.NewArtifacts(), tasks, func(c context.Context, s contract.Scope, _ contextbuilder.View) error { return authorize(c, s) })
	if err != nil {
		t.Fatal(err)
	}
	in := contract.Input{ID: "message", Scene: "scene", Actor: "human", Text: "original human instruction", Origin: "fixture"}
	if _, err = builder.Admit(ctx, scope, contextbuilder.Source{ID: in.ID, Actor: in.Actor, Text: in.Text}); err != nil {
		t.Fatal(err)
	}
	frame, err := builder.Build(ctx, scope, contextbuilder.Front, in.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	frozen := p
	frozen.Revision = "old"
	legacy := Job{Input: in, Profile: frozen, State: "failed", Error: "temporary model failure", Frame: &frame, Answers: []work.Source{{Receipt: "prior-answer", Actor: "human", Text: "frozen answer"}}}
	id := contextbuilder.Digest([]byte(in.Scene + "/" + in.Actor + "/" + in.ID))
	if _, err = db.Put(ctx, storage.Document{Namespace: "entry-jobs", Scope: p.Key(), ID: id, Body: encode(legacy)}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc, err := New(Options{State: db, Profile: p, Context: builder, Tasks: tasks, Scheduler: scheduleStub{}, Outbox: &interaction.Manager{}, Authorize: authorize, CancelExecution: func(string) {}, EnqueueMessage: func(context.Context, contract.Profile, contract.Input, string, string, string, string) (any, error) {
		return nil, nil
	}, Runner: runFunc(func(c context.Context, call kernel.Invocation) (native.Result, error) {
		calls++
		if call.Profile.Revision != "old" || call.Receipt != in.ID {
			t.Fatal("retry changed frozen identity")
		}
		return native.Result{}, call.Check(c)
	})})
	if err != nil {
		t.Fatal(err)
	}
	found, err := svc.Retry(ctx, id)
	if !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	doc, err := db.Get(ctx, "entry-jobs", p.Key(), id)
	if err != nil {
		t.Fatal(err)
	}
	var queued Job
	if err = json.Unmarshal(doc.Body, &queued); err != nil {
		t.Fatal(err)
	}
	legacy.State = "queued"
	legacy.Error = ""
	if string(encode(queued)) != string(encode(legacy)) {
		t.Fatal("retry changed frozen payload")
	}
	if err = builder.Revoke(ctx, scope, in.ID); err != nil {
		t.Fatal(err)
	}
	progress, err := svc.Step(ctx)
	if !progress || !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatalf("revoked retry: %v %v", progress, err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	failures, err := svc.Failures(ctx, scope)
	if err != nil || len(failures) != 1 || failures[0].ID != id {
		t.Fatalf("failures: %+v %v", failures, err)
	}
}
