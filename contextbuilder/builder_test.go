package contextbuilder_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	cb "github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
)

func fixture(t *testing.T) (*cb.Builder, *markdown.Store, *work.Manager, work.Scope) {
	t.Helper()
	db, e := sqlite.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	md, e := markdown.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { md.Close() })
	tasks, e := work.New(db)
	if e != nil {
		t.Fatal(e)
	}
	scope := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "scene", Requester: "owner"}
	b, e := cb.New(db, md, tasks, func(_ context.Context, s work.Scope, _ cb.View) error {
		if s.Workspace != scope.Workspace || s.Tenant != scope.Tenant || s.Employee != scope.Employee || s.Requester != scope.Requester {
			return work.ErrScope
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return b, md, tasks, scope
}
func TestFrozenSourceReplayWithdrawalAndIsolation(t *testing.T) {
	b, _, _, s := fixture(t)
	ctx := context.Background()
	original, e := b.Admit(ctx, s, cb.Source{ID: "input", Actor: s.Requester, Text: "private fact"})
	if e != nil {
		t.Fatal(e)
	}
	replay, e := b.Admit(ctx, s, cb.Source{ID: "input", Actor: s.Requester, Text: "private fact", At: time.Now().Add(time.Hour)})
	if e != nil || !replay.At.Equal(original.At) {
		t.Fatal(replay, e)
	}
	if _, e = b.Admit(ctx, s, cb.Source{ID: "input", Actor: s.Requester, Text: "changed fact"}); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal(e)
	}
	frame, e := b.Build(ctx, s, cb.Front, "input", "")
	if e != nil {
		t.Fatal(e)
	}
	other := s
	other.Scene = "other-scene"
	if _, _, e = b.Source(ctx, other, "input"); !errors.Is(e, sqlite.ErrNotFound) {
		t.Fatal(e)
	}
	other = s
	other.Requester = "intruder"
	if _, _, e = b.Source(ctx, other, "input"); !errors.Is(e, work.ErrScope) {
		t.Fatal(e)
	}
	if e = b.Revoke(ctx, s, "input"); e != nil {
		t.Fatal(e)
	}
	if e = b.Revalidate(ctx, frame); !errors.Is(e, cb.ErrRevoked) {
		t.Fatal(e)
	}
	if _, e = b.Admit(ctx, s, original); !errors.Is(e, cb.ErrRevoked) {
		t.Fatal("withdrawn receipt resurrected", e)
	}
}
func TestViewsFenceTaskSourcesAndKeepCurrentOutsideWindow(t *testing.T) {
	b, md, tasks, s := fixture(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 24; i++ {
		if _, e := b.Admit(ctx, s, cb.Source{ID: fmt.Sprintf("input-%02d", i), Actor: s.Requester, Text: fmt.Sprintf("source %d", i), At: base.Add(time.Duration(i) * time.Second)}); e != nil {
			t.Fatal(e)
		}
	}
	ownerPath := "owners/" + cb.OwnerKey(s) + "/memory.md"
	file, e := md.Write(ctx, ownerPath, "", "owner context")
	if e != nil {
		t.Fatal(e)
	}
	front, e := b.Build(ctx, s, cb.Front, "input-00", "")
	if e != nil {
		t.Fatal(e)
	}
	if len(front.Sources) != 21 || front.Current.ID != "input-00" || len(front.OwnerFiles) != 1 || len(front.Gaps) == 0 {
		t.Fatal(front)
	}
	receipt, e := tasks.Dispatch(ctx, s, work.Source{Receipt: "input-00", Actor: s.Requester, Text: "source 0"}, "native-call", work.Definition{Goal: "report", Instruction: "write", MaxRuns: 2})
	if e != nil {
		t.Fatal(e)
	}
	execution, e := b.Build(ctx, s, cb.Execution, "", receipt.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	if len(execution.Sources) != 1 || execution.Sources[0].ID != "input-00" || len(execution.OwnerFiles) != 0 || len(execution.Tasks) != 1 {
		t.Fatal("execution inherited unrelated window or owner context", execution)
	}
	if _, e = md.Write(ctx, ownerPath, file.SHA256, "new version"); e != nil {
		t.Fatal(e)
	}
	if e = b.Revalidate(ctx, front); e != nil {
		t.Fatal("ordinary owner update revoked frozen authorized context", e)
	}
	if e = b.Revalidate(ctx, execution); e != nil {
		t.Fatal("unrelated owner edit invalidated task sources", e)
	}
	if e = b.ResetOwner(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e = b.Revalidate(ctx, front); !errors.Is(e, cb.ErrRevoked) {
		t.Fatal("reset retained old front view", e)
	}
	if e = b.Revalidate(ctx, execution); !errors.Is(e, cb.ErrRevoked) {
		t.Fatal("reset retained old execution view", e)
	}
	fresh, e := b.Build(ctx, s, cb.Front, "input-00", "")
	if e != nil || fresh.OwnerEpoch != front.OwnerEpoch+1 {
		t.Fatal(fresh, e)
	}
}
