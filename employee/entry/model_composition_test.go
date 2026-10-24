package entry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
)

// Exercises admission, persisted Task execution, Review and persisted Owner work
// across reopen. Current-profile changes must not change admitted model effort.
func TestLoopModelsRemainFrozenAcrossReopen(t *testing.T) {
	ctx := context.Background()
	profile := testProfile()
	profile.OwnerLoops = []string{"memory"}
	original := map[string]contract.ModelBinding{}
	for role, effort := range map[string]string{"chat": "low", "executor": "high", "review": "medium", "owner_memory": "low"} {
		original[role] = contract.ModelBinding{Provider: "test-provider", Version: "1", Model: "model-" + role, Effort: effort}
	}
	profile.ModelBindings = original
	seen := map[string]contract.ModelBinding{}
	ms := map[string]*looptest.Model{
		"chat":         looptest.New(dispatchStep(t)),
		"executor":     looptest.New(call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "verified local execution"})),
		"review":       looptest.New(reviewStep(t)),
		"owner_memory": looptest.New(call("commit_owner_update", "commit", map[string]any{"body": "Owner prefers concise", "citations": []any{map[string]any{"source_id": "one", "quote": "concise"}}})),
	}
	factory := func(_ context.Context, r ModelRequest) (model.ToolCallingChatModel, error) {
		seen[r.Role] = r.Binding
		if r.Binding != original[r.Role] || r.ModelName != original[r.Role].Model {
			t.Errorf("%s used current rather than admitted binding: %+v", r.Role, r.Binding)
		}
		return ms[r.Role], nil
	}
	dir := t.TempDir()
	e, err := openDefault(ctx, Options{Directory: dir, Profile: profile, Models: factory})
	if err != nil {
		t.Fatal(err)
	}
	// Entry must own the caller's map even before an input is admitted.
	profile.ModelBindings = map[string]contract.ModelBinding{}
	for role, b := range original {
		profile.ModelBindings[role] = b
	}
	originalChat := original["chat"]
	original["chat"] = contract.ModelBinding{Provider: "mutated", Version: "9", Model: "mutated", Effort: "high"}
	if e.profile.ModelBindings["chat"] != originalChat {
		t.Fatal("Entry retained caller map")
	}
	original["chat"] = originalChat
	if _, err = e.Submit(ctx, testInput("one", "Be concise and compute result")); err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if _, err = s.chat.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.ownerWorker.Observe(ctx, profile.Scope("dm"), "one"); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	profile.Revision = "v2"
	for role := range profile.ModelBindings {
		profile.ModelBindings[role] = contract.ModelBinding{Provider: "other-provider", Version: "2", Model: "new-model", Effort: "high"}
	}
	e, err = openDefault(ctx, Options{Directory: dir, Profile: profile, Models: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	for role, b := range original {
		if seen[role] != b {
			t.Errorf("missing frozen %s: %+v", role, seen)
		}
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || string(tasks[0].State) != "completed" {
		t.Fatal(tasks, err)
	}
	docs, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 1 {
		t.Fatal(docs, err)
	}
	s = e.backend.(*standalone)
	journalScope, _ := json.Marshal([]string{contextbuilder.OwnerKey(profile.Scope("dm")) + "/owner/memory", docs[0].JobID})
	calls, err := s.db.List(ctx, "model-calls", string(journalScope))
	if err != nil {
		t.Fatal(err)
	}
	foundOwner := false
	for _, doc := range calls {
		var identity struct{ ModelIdentity string }
		if err = json.Unmarshal(doc.Body, &identity); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(identity.ModelIdentity, "model-owner_memory") {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Fatal("Owner journal omitted resolved model identity")
	}
}
