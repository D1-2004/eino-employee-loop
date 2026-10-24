package owner_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/components/model"
)

func TestOwnerRetryPreservesFrozenObservationAndScopesSharedOwnerKey(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "original", "请记住这个原话")
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		t.Fatal("recovery must not run model")
		return nil, nil
	})
	allowed := true
	opts.Authorize = func(_ context.Context, s work.Scope) error {
		if !allowed || s.Workspace != f.scope.Workspace || s.Tenant != f.scope.Tenant || s.Employee != f.scope.Employee || s.Requester != f.scope.Requester {
			return work.ErrScope
		}
		return nil
	}
	worker, err := ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Observe(ctx, f.scope, "original"); err != nil {
		t.Fatal(err)
	}
	rows, err := f.db.List(ctx, "owner-jobs", contextbuilder.OwnerKey(f.scope))
	if err != nil || len(rows) == 0 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	doc := rows[0]
	var original map[string]json.RawMessage
	if err = json.Unmarshal(doc.Body, &original); err != nil {
		t.Fatal(err)
	}
	original["State"] = json.RawMessage(`"failed"`)
	original["Error"] = json.RawMessage(`"transient failure"`)
	original["future_field"] = json.RawMessage(`{"frozen":"preserve"}`)
	doc.Body, _ = json.Marshal(original)
	if _, err = f.db.Put(ctx, doc); err != nil {
		t.Fatal(err)
	}
	failures, err := worker.Failures(ctx, f.scope)
	if err != nil || len(failures) != 1 || failures[0].ID != doc.ID {
		t.Fatalf("failures=%+v err=%v", failures, err)
	}
	other := f.scope
	other.Scene = "another-scene"
	if contextbuilder.OwnerKey(other) != contextbuilder.OwnerKey(f.scope) {
		t.Fatal("fixture must exercise shared owner key")
	}
	if found, err := worker.Retry(ctx, other, doc.ID); found || err != nil {
		t.Fatalf("cross-scene retry=%v %v", found, err)
	}
	if failures, err = worker.Failures(ctx, other); err != nil || len(failures) != 0 {
		t.Fatalf("cross-scene failures=%+v %v", failures, err)
	}
	if found, err := worker.Retry(ctx, f.scope, doc.ID); !found || err != nil {
		t.Fatalf("retry=%v %v", found, err)
	}
	saved, err := f.db.Get(ctx, doc.Namespace, doc.Scope, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	original["State"] = json.RawMessage(`"queued"`)
	original["Error"] = json.RawMessage(`""`)
	expected, _ := json.Marshal(original)
	if string(saved.Body) != string(expected) {
		t.Fatal("retry changed frozen or unknown persisted fields")
	}
	if found, err := worker.Retry(ctx, f.scope, doc.ID); !found || !errors.Is(err, work.ErrState) {
		t.Fatalf("queued retry=%v %v", found, err)
	}
	allowed = false
	if _, err = worker.Retry(ctx, f.scope, doc.ID); !errors.Is(err, work.ErrScope) {
		t.Fatalf("denied retry=%v", err)
	}
	if _, err = worker.Failures(ctx, f.scope); !errors.Is(err, work.ErrScope) {
		t.Fatalf("denied failures=%v", err)
	}
}
