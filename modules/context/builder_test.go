package context_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/adapters/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	cb "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/cloudwego/eino/schema"
)

func fixture(t *testing.T, options ...cb.Option) (*cb.Builder, *markdown.Store, *work.Manager, work.Scope, *sqlite.Store) {
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
	}, options...)
	if e != nil {
		t.Fatal(e)
	}
	return b, md, tasks, scope, db
}

type ownerEvidence struct {
	Scope work.Scope
	Ref   cb.Ref
	Quote string
}

func putOwnerMetadata(t *testing.T, db *sqlite.Store, owner work.Scope, file markdown.Document, evidence []ownerEvidence) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Path, SHA256 string
		Evidence     []ownerEvidence
	}{file.Path, file.SHA256, evidence})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Put(context.Background(), sqlite.Document{Namespace: "owner-documents", Scope: cb.OwnerKey(owner), ID: "memory", Body: raw})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOwnerAudienceFiltersEntireMixedDocumentBeforeEveryModelView(t *testing.T) {
	b, md, tasks, private, db := fixture(t)
	ctx := context.Background()
	group := private
	group.Scene = "opaque-42" // The Host, not a scene-name convention, owns audience semantics.
	const marker = "PRIVATE-OWNER-MARKER-42"
	for _, item := range []struct {
		scope    work.Scope
		id, text string
	}{{private, "private-source", marker}, {group, "group-source", "public task"}} {
		if _, err := b.Admit(ctx, item.scope, cb.Source{ID: item.id, Actor: item.scope.Requester, Text: item.text}); err != nil {
			t.Fatal(err)
		}
	}
	_, privateRef, err := b.Source(ctx, private, "private-source")
	if err != nil {
		t.Fatal(err)
	}
	_, groupRef, err := b.Source(ctx, group, "group-source")
	if err != nil {
		t.Fatal(err)
	}
	file, err := md.Write(ctx, "owners/"+cb.OwnerKey(private)+"/memory.md", "", marker+" plus public task")
	if err != nil {
		t.Fatal(err)
	}
	putOwnerMetadata(t, db, private, file, []ownerEvidence{{private, privateRef, marker}, {group, groupRef, "public task"}})
	receipt, err := tasks.Dispatch(ctx, group, work.Source{Receipt: "group-source", Actor: group.Requester, Text: "public task"}, "dispatch", work.Definition{Goal: "public task", Instruction: "report", MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []cb.View{cb.Front, cb.Execution, cb.Maintenance} {
		t.Run(string(view), func(t *testing.T) {
			frame, err := b.Build(ctx, group, view, "group-source", receipt.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if len(frame.OwnerFiles) != 0 || len(frame.OwnerEvidence) != 0 {
				t.Fatal("mixed document partially exposed", frame)
			}
			raw, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			model := looptest.New(looptest.Func(func(messages []*schema.Message) looptest.Step {
				for _, message := range messages {
					if strings.Contains(message.Content, marker) {
						t.Fatal("private source reached model")
					}
				}
				return looptest.Text("audience safe")
			}))
			if _, err = model.Generate(ctx, []*schema.Message{schema.UserMessage(string(raw))}); err != nil {
				t.Fatal(err)
			}
			// A frame frozen by the old implementation must be fenced on replay,
			// even though its source remains readable to the owner.
			frame.OwnerFiles = []markdown.Document{file}
			frame.OwnerEvidence = []cb.ScopedRef{{Scope: private, Ref: privateRef}}
			if err = b.Revalidate(ctx, frame); !errors.Is(err, cb.ErrAudience) {
				t.Fatal("legacy cross-scene replay accepted", err)
			}
		})
	}
}

func TestAudienceGrantAndWithdrawalFenceFrozenOwnerProjection(t *testing.T) {
	allowed := true
	b, md, _, source, db := fixture(t, cb.WithAudiencePolicy(func(_ context.Context, from, to work.Scope, _ cb.View) error {
		if from == to || allowed {
			return nil
		}
		return cb.ErrAudience
	}))
	ctx := context.Background()
	target := source
	target.Scene = "another-opaque-scene"
	if _, err := b.Admit(ctx, source, cb.Source{ID: "source", Actor: source.Requester, Text: "explicitly shared fact"}); err != nil {
		t.Fatal(err)
	}
	_, ref, err := b.Source(ctx, source, "source")
	if err != nil {
		t.Fatal(err)
	}
	file, err := md.Write(ctx, "owners/"+cb.OwnerKey(source)+"/memory.md", "", "shared fact")
	if err != nil {
		t.Fatal(err)
	}
	putOwnerMetadata(t, db, source, file, []ownerEvidence{{source, ref, "shared fact"}})
	frame, err := b.Build(ctx, target, cb.Front, "", "")
	if err != nil || len(frame.OwnerFiles) != 1 {
		t.Fatal(frame, err)
	}
	allowed = false
	if err = b.Revalidate(ctx, frame); !errors.Is(err, cb.ErrAudience) {
		t.Fatal("withdrawn audience grant retained on replay", err)
	}
	frame.OwnerEvidence = nil
	if err = b.Revalidate(ctx, frame); !errors.Is(err, cb.ErrAudience) {
		t.Fatal("projection replay trusted missing aggregate evidence", err)
	}
	fresh, err := b.Build(ctx, target, cb.Front, "", "")
	if err != nil || len(fresh.OwnerFiles) != 0 {
		t.Fatal(fresh, err)
	}
	same, err := b.Build(ctx, source, cb.Maintenance, "source", "")
	if err != nil || len(same.OwnerFiles) != 1 {
		t.Fatal("same scene denied", same, err)
	}
	other := source
	other.Tenant = "foreign-tenant"
	allowed = true
	if err = b.CheckAudience(ctx, other, target, cb.Front); !errors.Is(err, cb.ErrAudience) {
		t.Fatal("host grant bypassed tenant boundary", err)
	}
}

func TestOwnerMarkdownWithoutEvidenceMetadataFailsClosed(t *testing.T) {
	b, md, _, scope, _ := fixture(t)
	file, err := md.Write(context.Background(), "owners/"+cb.OwnerKey(scope)+"/memory.md", "", "unattributed private fact")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := b.Build(context.Background(), scope, cb.Front, "", "")
	if err != nil || len(frame.OwnerFiles) != 0 {
		t.Fatal(frame, err)
	}
	frame.OwnerFiles = []markdown.Document{file}
	if err = b.Revalidate(context.Background(), frame); !errors.Is(err, cb.ErrAudience) {
		t.Fatal("legacy unattributed file replayed", err)
	}
}
func TestFrozenSourceReplayWithdrawalAndIsolation(t *testing.T) {
	b, _, _, s, _ := fixture(t)
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
	b, md, tasks, s, db := fixture(t)
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
	_, ownerRef, e := b.Source(ctx, s, "input-00")
	if e != nil {
		t.Fatal(e)
	}
	putOwnerMetadata(t, db, s, file, []ownerEvidence{{Scope: s, Ref: ownerRef, Quote: "source 0"}})
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
