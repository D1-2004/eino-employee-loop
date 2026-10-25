package chat

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/memory"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func fifoService(t *testing.T, dir string, received *[]string) (*Service, *sqlite.Store) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	profile := contract.Profile{ID: "p", Revision: "r1", Workspace: "w", Tenant: "t", Employee: "e", Owner: "human", Backend: "standalone", Scenes: []string{"scene"}}
	allow := func(context.Context, contract.Scope) error { return nil }
	builder, err := contextbuilder.New(db, memory.NewArtifacts(), tasks, func(context.Context, contract.Scope, contextbuilder.View) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{State: db, Profile: profile, Context: builder, Tasks: tasks, Scheduler: scheduleStub{}, Outbox: &interaction.Manager{}, Authorize: allow, CancelExecution: func(string) {}, EnqueueMessage: func(context.Context, contract.Profile, contract.Input, string, string, string, string) (any, error) {
		return nil, nil
	}, Runner: runFunc(func(_ context.Context, in kernel.Invocation) (native.Result, error) {
		*received = append(*received, in.Receipt)
		return native.Result{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return service, db
}

func TestDurableInboxFIFOAcrossReopenAndReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	received := []string{}
	svc, db := fifoService(t, dir, &received)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("m%d", i)
		input := contract.Input{ID: id, Scene: "scene", Actor: "human", Text: "message " + id, Origin: "fixture"}
		admission, err := svc.Submit(ctx, input)
		if err != nil || admission.Replay {
			t.Fatal(admission, err)
		}
		if i == 3 {
			a, err := svc.Submit(ctx, input)
			if err != nil || !a.Replay {
				t.Fatal(a, err)
			}
		}
	}
	events, err := db.Events(ctx, "entry-admissions", svc.profile.Key(), 0, 100)
	if err != nil || len(events) != 6 {
		t.Fatalf("replay allocated event: %d %v", len(events), err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{2, 4} {
		svc, db = fifoService(t, dir, &received)
		for i := 0; i < count; i++ {
			progress, err := svc.Step(ctx)
			if err != nil || !progress {
				t.Fatal(progress, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"m1", "m2", "m3", "m4", "m5", "m6"}; !reflect.DeepEqual(received, want) {
		t.Fatalf("got %v want %v", received, want)
	}
}

func TestLegacyJobsUseSourceAdmissionTimeBeforeNewJobs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	received := []string{}
	svc, db := fifoService(t, dir, &received)
	defer db.Close()
	start := time.Now().UTC().Add(-time.Hour)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("m%d", i)
		in := contract.Input{ID: id, Scene: "scene", Actor: "human", Text: id, Origin: "legacy"}
		if _, err := svc.context.Admit(ctx, svc.profile.Scope("scene"), contextbuilder.Source{ID: id, Actor: "human", Text: id, At: start.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
		key := contextbuilder.Digest([]byte(in.Scene + "/" + in.Actor + "/" + in.ID))
		if _, err := db.Put(ctx, storage.Document{Namespace: "entry-jobs", Scope: svc.profile.Key(), ID: key, Body: encode(Job{Input: in, Profile: svc.profile, State: "queued"})}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Submit(ctx, contract.Input{ID: "new", Scene: "scene", Actor: "human", Text: "new", Origin: "fixture"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		ok, err := svc.Step(ctx)
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if want := []string{"m1", "m2", "m3", "m4", "m5", "m6", "new"}; !reflect.DeepEqual(received, want) {
		t.Fatalf("got %v want %v", received, want)
	}
}
