package entry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/schema"
)

func compiledProfile() Profile {
	p := testProfile()
	p.Instructions = "LEGACY_GLOBAL_MUST_NOT_APPEAR"
	p.OwnerLoops = []string{"memory"}
	p.DeliveryLoops = []string{"expression", "package"}
	p.PromptBundle = &contract.PromptBundle{Contract: contract.NativePromptContract, SourceRevision: "synthetic-source/1", SourceManifestSHA256: contract.Digest([]byte("synthetic sources")), CompilerVersion: "synthetic-compiler/1", Roles: map[string]string{}}
	for _, role := range []string{"chat", "executor", "review", "task_attention", "greeting", "routine", "owner_memory", "expression", "package"} {
		p.PromptBundle.Roles[role] = "SYNTHETIC_FROZEN_" + role
	}
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	return p
}

// Runs actual native Chat/Executor/Review and independent Owner requests across
// SQLite reopen. The queued Task and Owner job must retain the accepted bundle.
func TestCompiledRolePromptsSurviveTaskAndOwnerReopen(t *testing.T) {
	ctx := context.Background()
	p := compiledProfile()
	// This unrelated role is valid but larger than the entire model Frame budget.
	// It must remain in the frozen configuration without entering another role's
	// user/tool messages or preventing execution/Review/delivery context builds.
	p.PromptBundle.Roles["greeting"] = strings.Repeat("SYNTHETIC_UNRELATED_GREETING ", 6000)
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	wanted := p.Snapshot()
	seen := map[string]bool{}
	check := func(role string, next looptest.Step) looptest.Step {
		return looptest.Func(func(in []*schema.Message) looptest.Step {
			if len(in) < 2 || in[0].Role != schema.System || !strings.Contains(in[0].Content, wanted.PromptBundle.Roles[role]) || strings.Contains(in[0].Content, "LEGACY_GLOBAL") || strings.Contains(in[0].Content, "CHANGED_CURRENT") {
				t.Fatalf("%s lost frozen role prompt", role)
			}
			for other, text := range wanted.PromptBundle.Roles {
				marker := strings.Split(text, " ")[0]
				if other != role && strings.Contains(in[0].Content, marker) {
					t.Fatalf("%s leaked %s instructions", role, other)
				}
				for _, message := range in[1:] {
					encoded, err := json.Marshal(message)
					if err != nil || strings.Contains(string(encoded), marker) {
						t.Fatalf("%s received %s instructions outside its system message", role, other)
					}
				}
			}
			seen[role] = true
			if next.Respond != nil {
				return next.Respond(in)
			}
			return next
		})
	}
	ms := map[string]*looptest.Model{
		"chat":         looptest.New(check("chat", dispatchStep(t))),
		"executor":     looptest.New(check("executor", call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "computed result"}))),
		"review":       looptest.New(check("review", reviewStep(t))),
		"owner_memory": looptest.New(check("owner_memory", call("commit_owner_update", "commit", map[string]any{"body": "Owner prefers concise", "citations": []any{map[string]any{"source_id": "one", "quote": "concise"}}}))),
		"expression":   looptest.New(check("expression", call("publish_result", "express", map[string]any{"text": "Verified computed result"}))),
		"package":      looptest.New(check("package", call("publish_package", "package", map[string]any{"summary": "Verified computed result package"}))),
	}
	dir := t.TempDir()
	e, err := openDefault(ctx, Options{Directory: dir, Profile: p, Models: models(ms)})
	if err != nil {
		t.Fatal(err)
	}
	// Entry must detach the caller's role map before admission.
	p.PromptBundle.Roles["chat"] = "CALLER_MUTATION"
	if _, err = e.Submit(ctx, testInput("one", "Be concise and compute result")); err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if _, err = s.chat.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.ownerWorker.Observe(ctx, wanted.Scope("dm"), "one"); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	p = wanted.Snapshot()
	p.Revision = "current-v2"
	for role := range p.PromptBundle.Roles {
		p.PromptBundle.Roles[role] = "CHANGED_CURRENT_" + role
	}
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	e, err = openDefault(ctx, Options{Directory: dir, Profile: p, Models: models(ms)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"chat", "executor", "review", "owner_memory", "expression", "package"} {
		if !seen[role] {
			t.Fatalf("no actual %s request", role)
		}
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || string(tasks[0].State) != "completed" || len(tasks[0].Runs) != 1 {
		t.Fatal("prompt composition changed business lifecycle", err)
	}
	if !strings.Contains(string(tasks[0].Definition.Configuration), "SYNTHETIC_UNRELATED_GREETING") {
		t.Fatal("model projection changed the SDK Task's accepted configuration")
	}
	docs, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 1 || docs[0].Version != wanted.Revision {
		t.Fatal("owner failed to retain original policy/version", err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" {
		t.Fatal("missing actual local Outbox receipt", err)
	}
}

func TestReadTaskToolProjectsBundleWithoutChangingSDKTask(t *testing.T) {
	ctx := context.Background()
	p := compiledProfile()
	p.OwnerLoops = nil
	p.DeliveryLoops = nil
	p.PromptBundle.Roles["greeting"] = strings.Repeat("PRIVATE_UNRELATED_ROLE ", 8000)
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	var taskID string
	chat := looptest.New(dispatchStep(t), looptest.Func(func(in []*schema.Message) looptest.Step {
		frame := frameOf(t, in)
		return call("read_task", "read", map[string]any{"task_id": taskID, "source_ref": frame.Current.ID})
	}), looptest.Func(func(in []*schema.Message) looptest.Step {
		found := false
		for _, message := range in {
			if message.Role != schema.Tool || message.ToolCallID != "read" {
				continue
			}
			var projected struct {
				ID         string
				Definition struct{ Configuration json.RawMessage }
			}
			if err := json.Unmarshal([]byte(message.Content), &projected); err != nil || projected.ID != taskID || !strings.Contains(string(projected.Definition.Configuration), `"redacted":true`) {
				t.Fatal("read_task did not return a provenance-only runtime configuration", err)
			}
			found = true
		}
		if !found {
			t.Fatal("no actual native read_task result")
		}
		for _, message := range in[1:] {
			raw, _ := json.Marshal(message)
			if strings.Contains(string(raw), "SYNTHETIC_FROZEN_") || strings.Contains(string(raw), "PRIVATE_UNRELATED_ROLE") {
				t.Fatal("private role instructions leaked into non-system model messages")
			}
		}
		return call("reply", "answer", map[string]any{"text": "Verified task state", "source_ref": "two"})
	}))
	ms := map[string]*looptest.Model{"chat": chat, "executor": looptest.New(call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "computed result"})), "review": looptest.New(reviewStep(t))}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(ms)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("one", "Compute the original result")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 {
		t.Fatal("missing original Task", err)
	}
	taskID = tasks[0].ID
	config := string(tasks[0].Definition.Configuration)
	if !strings.Contains(config, "PRIVATE_UNRELATED_ROLE") {
		t.Fatal("SDK Task did not retain complete configuration")
	}
	if _, err = e.Submit(ctx, testInput("two", "Read the current Task")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	after, err := e.Tasks(ctx, "dm")
	if err != nil || len(after) != 1 || string(after[0].Definition.Configuration) != config || chat.Calls() != 3 {
		t.Fatal("tool projection changed accepted configuration or native flow", err)
	}
}
