package context_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	cb "github.com/D1-2004/eino-employee-loop/employee/context"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func TestOversizedCurrentRejectedAndCannotPoisonLaterHistory(t *testing.T) {
	b, _, _, scope, _ := fixture(t)
	ctx := context.Background()
	original := strings.Repeat("long constraint: keep this exactly\n", 20_000)
	if len(original) < 600_000 {
		t.Fatal("fixture too small")
	}
	if _, err := b.Admit(ctx, scope, cb.Source{ID: "huge", Actor: scope.Requester, Text: original}); err != nil {
		t.Fatal(err)
	}
	frame, err := b.Build(ctx, scope, cb.Front, "huge", "")
	if !errors.Is(err, cb.ErrBudget) || frame.Current == nil || frame.Current.Text != original || len(frame.Gaps) == 0 {
		t.Fatalf("oversized original was clipped or accepted: %v", err)
	}
	if _, err := b.Admit(ctx, scope, cb.Source{ID: "small", Actor: scope.Requester, Text: "NEW-CURRENT-UNIQUE"}); err != nil {
		t.Fatal(err)
	}
	frame, err = b.Build(ctx, scope, cb.Front, "small", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > cb.DefaultBudget().MaxBytes || strings.Contains(string(raw), "long constraint") || strings.Count(string(raw), "NEW-CURRENT-UNIQUE") != 1 {
		t.Fatalf("unbounded/duplicated frame: bytes=%d", len(raw))
	}
	if len(frame.Refs) != 1 || frame.Refs[0].ID != "small" {
		t.Fatalf("current was not frozen: %v", frame.Refs)
	}
	if err := b.Revoke(ctx, scope, "small"); err != nil {
		t.Fatal(err)
	}
	if err := b.Revalidate(ctx, frame); !errors.Is(err, cb.ErrRevoked) {
		t.Fatalf("lost revocation fencing: %v", err)
	}
	legacy := frame
	legacy.Current = &cb.Source{ID: "huge", Actor: scope.Requester, Text: original}
	if err := b.Revalidate(ctx, legacy); !errors.Is(err, cb.ErrBudget) {
		t.Fatalf("oversized legacy replay: %v", err)
	}
}

func TestContextBudgetCountsEscapedJSONAndBoundsTaskProjection(t *testing.T) {
	const maxBytes = 4096
	b, _, tasks, scope, _ := fixture(t, cb.WithBudget(cb.Budget{MaxBytes: maxBytes, MaxSources: 4, MaxTasks: 3, MaxTaskSummaryBytes: 32}))
	ctx := context.Background()
	if _, err := b.Admit(ctx, scope, cb.Source{ID: "current", Actor: scope.Requester, Text: "current-exact"}); err != nil {
		t.Fatal(err)
	}
	// JSON escaping can expand a source; the bound is on the actual wire frame.
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("s%d", i)
		text := strings.Repeat("\"\\\n", 100)
		if _, err := b.Admit(ctx, scope, cb.Source{ID: id, Actor: scope.Requester, Text: text}); err != nil {
			t.Fatal(err)
		}
		_, err := tasks.Dispatch(ctx, scope, work.Source{Receipt: id, Actor: scope.Requester, Text: text}, id, work.Definition{Goal: strings.Repeat("goal", 40), Instruction: strings.Repeat("private full instruction", 400), MaxRuns: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	frame, err := b.Build(ctx, scope, cb.Front, "current", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(frame)
	if len(raw) > maxBytes || len(frame.Sources) > 4 || len(frame.Tasks) > 3 {
		t.Fatalf("bounds violated: bytes=%d sources=%d tasks=%d", len(raw), len(frame.Sources), len(frame.Tasks))
	}
	if strings.Contains(string(raw), "private full instruction") || strings.Contains(string(raw), strings.Repeat("goal", 40)) {
		t.Fatal("raw task body or oversized field reached front model")
	}
	if len(frame.Tasks) == 0 || len(frame.Gaps) == 0 {
		t.Fatal("missing bounded task projections or omissions")
	}
	for _, task := range frame.Tasks {
		if task.ID == "" || task.Revision == 0 || len(task.Sources) != 0 {
			t.Fatalf("bad task summary: %+v", task)
		}
	}
}

func TestBudgetOmitsWholeOwnerProjectionAndRejectsExecutionConstraints(t *testing.T) {
	b, md, tasks, scope, db := fixture(t, cb.WithBudget(cb.Budget{MaxBytes: 8192}))
	ctx := context.Background()
	const original = "permitted quote and exact instruction"
	if _, err := b.Admit(ctx, scope, cb.Source{ID: "source", Actor: scope.Requester, Text: original}); err != nil {
		t.Fatal(err)
	}
	_, ref, err := b.Source(ctx, scope, "source")
	if err != nil {
		t.Fatal(err)
	}
	file, err := md.Write(ctx, "owners/"+cb.OwnerKey(scope)+"/memory.md", "", strings.Repeat("large projection", 2000))
	if err != nil {
		t.Fatal(err)
	}
	putOwnerMetadata(t, db, scope, file, []ownerEvidence{{Scope: scope, Ref: ref, Quote: "permitted quote"}})
	frame, err := b.Build(ctx, scope, cb.Front, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.OwnerFiles) != 0 || len(frame.OwnerProjections) != 0 || len(frame.OwnerEvidence) != 0 {
		t.Fatal("partial oversized projection retained")
	}
	if err := b.Revalidate(ctx, frame); err != nil {
		t.Fatal(err)
	}
	constraints := strings.Repeat("NEVER SHORTEN THIS CONSTRAINT. ", 500)
	receipt, err := tasks.Dispatch(ctx, scope, work.Source{Receipt: "source", Actor: scope.Requester, Text: original}, "dispatch", work.Definition{Goal: "report", Instruction: "write", Constraints: []string{constraints}, MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err = b.Build(ctx, scope, cb.Execution, "", receipt.TaskID)
	if !errors.Is(err, cb.ErrBudget) || len(frame.Tasks) != 1 || frame.Tasks[0].Definition.Constraints[0] != constraints {
		t.Fatal("execution silently shortened constraints", err)
	}
}
