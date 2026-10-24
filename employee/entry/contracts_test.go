package entry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

func TestBusinessDirectoryHasOneProfileOwnerAcrossIDs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opts := Options{Directory: dir, Profile: testProfile(), Models: models(nil)}
	first, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	other := opts
	other.Profile.ID = "different-profile"
	timeout, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if second, err := openDefault(timeout, other); err == nil {
		second.Close()
		t.Fatal("a different profile took over the live business scope")
	}
	first.Close()
	if second, err := openDefault(ctx, other); err == nil {
		second.Close()
		t.Fatal("business directory silently rebound to different profile")
	}
	again, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
func TestDeliveryReceiptCannotReexecuteExpressionAndPackaging(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "verified"})), "review": looptest.New(reviewStep(t)), "package": looptest.New(call("publish_package", "package", map[string]any{"summary": "Only the verified local result"})), "expression": looptest.New(call("publish_result", "text", map[string]any{"text": "Result ready in the local delivery package"}))}
	p := testProfile()
	p.DeliveryLoops = []string{"expression", "package"}
	e, err := openDefault(ctx, Options{Directory: dir, Profile: p, Models: models(ms)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("first", "Compute a result")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 50); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.Tasks(ctx, "dm")
	if len(tasks) != 1 || len(tasks[0].Deliveries) != 1 {
		t.Fatal(tasks)
	}
	s := e.backend.(*standalone)
	if _, err = s.work.Control(ctx, tasks[0].Scope, work.Source{Receipt: "noop", Actor: "owner", Text: "stop"}, "noop-stop", tasks[0].ID, "stop", ""); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if ms["package"].Calls() != 1 || ms["expression"].Calls() != 1 {
		t.Fatal("delivery facts repeated cognitive loops", ms["package"].Calls(), ms["expression"].Calls())
	}
	deliveries, _ := e.Deliveries(ctx, "dm")
	if len(deliveries) != 1 || deliveries[0].State != "applied" {
		t.Fatal(deliveries)
	}
	packages, err := filepath.Glob(filepath.Join(dir, "workspace", "packages", "*", "*.md"))
	if err != nil || len(packages) != 1 {
		t.Fatal(packages, err)
	}
}
func TestResolvedHistoricalQuestionIsNotSent(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("ask_input", "ask", map[string]any{"prompt": "Which quarter?"}))})
	defer e.Close()
	_, err := e.Submit(ctx, testInput("start", "Ask which quarter"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = e.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := e.Tasks(ctx, "dm")
	task := tasks[0]
	if task.State != work.Waiting {
		t.Fatal(task)
	}
	s := e.backend.(*standalone)
	answer := work.Source{Receipt: "answer", Actor: "owner", Text: "Q3"}
	if _, err = s.context.Admit(ctx, task.Scope, contextbuilder.Source{ID: answer.Receipt, Actor: answer.Actor, Text: answer.Text}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.work.Answer(ctx, task.Scope, answer, "answer", task.ID, task.Waits[0].ID, task.Goal.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.interaction.Step(ctx); err != nil {
		t.Fatal(err)
	}
	records, _ := e.Deliveries(ctx, "dm")
	if len(records) != 1 || records[0].State != "cancelled" || records[0].Receipt != nil {
		t.Fatal(records)
	}
}
func TestDerivedOwnerViewChecksOtherSceneEvidenceBeforeAndAfterReconcile(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.Scenes = []string{"a", "b"}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(nil), AudiencePolicy: func(context.Context, work.Scope, work.Scope, contextbuilder.View) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	a, b := p.Scope("a"), p.Scope("b")
	if _, err = s.context.Admit(ctx, a, contextbuilder.Source{ID: "fact", Actor: "owner", Text: "Private fact from scene a"}); err != nil {
		t.Fatal(err)
	}
	_, ref, err := s.context.Source(ctx, a, "fact")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.context.Admit(ctx, b, contextbuilder.Source{ID: "current", Actor: "owner", Text: "Use my owner context"}); err != nil {
		t.Fatal(err)
	}
	path := "owners/" + contextbuilder.OwnerKey(a) + "/memory.md"
	md, err := s.files.Write(ctx, path, "", "# Remembered\nPrivate fact from scene a")
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"Path": path, "SHA256": md.SHA256, "OwnerEpoch": 0, "Evidence": []any{map[string]any{"Scope": a, "Ref": ref, "Quote": "Private fact from scene a"}}}
	if _, err = s.db.Put(ctx, sqlite.Document{Namespace: "owner-documents", Scope: contextbuilder.OwnerKey(a), ID: "memory", Body: encode(meta)}); err != nil {
		t.Fatal(err)
	}
	before, err := s.context.Build(ctx, b, contextbuilder.Front, "current", "")
	if err != nil || len(before.OwnerFiles) != 1 {
		t.Fatal(before, err)
	}
	if err = s.context.Revoke(ctx, a, "fact"); err != nil {
		t.Fatal(err)
	}
	if err = s.context.Revalidate(ctx, before); !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatal("cross-scene derived view survived source revocation", err)
	}
	after, err := s.context.Build(ctx, b, contextbuilder.Front, "current", "")
	if err != nil || len(after.OwnerFiles) != 0 {
		t.Fatal("read dirty projection before asynchronous reconciliation", after, err)
	}
}
func TestRepeatedNativeFileIDCannotChangeAnEarlierEffect(t *testing.T) {
	ctx := context.Background()
	original := "# Original"
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("write_file", "same-id", map[string]any{"path": "result.md", "content": original}), call("write_file", "same-id", map[string]any{"path": "result.md", "content": "# Changed", "expected_sha256": contextbuilder.Digest([]byte(original))})), "review": looptest.New(call("decide_review", "review", map[string]any{"kind": "fail", "summary": "Repeated native effect ID changed arguments"}))}
	e := openTest(t, t.TempDir(), ms)
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("file-request", "Write a result")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.Tasks(ctx, "dm")
	task := tasks[0]
	if task.State != work.Failed || !strings.Contains(task.CurrentRun().Failure, "conflict") {
		t.Fatal(task)
	}
	s := e.backend.(*standalone)
	doc, err := s.files.Read(ctx, "tasks/"+contextbuilder.OwnerKey(task.Scope)+"/"+task.ID+"/result.md")
	if err != nil || doc.Body != original {
		t.Fatal("effect was modified before detecting ID conflict", doc, err)
	}
	// The accepted raw model response stays an immutable native call record.
	key, _ := task.Scope.Key()
	docs, err := s.db.List(ctx, "execution-file-effects", key)
	if err != nil || len(docs) != 1 {
		t.Fatal(docs, err)
	}
	var hash string
	if json.Unmarshal(docs[0].Body, &hash) != nil || hash == "" {
		t.Fatal(docs)
	}
}

func TestExplicitGreetingAndRoutineUseSeparateNativeLoopsAndReceipts(t *testing.T) {
	for _, kind := range []string{"greeting", "routine"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			ms := map[string]*looptest.Model{"chat": looptest.New(call("compose_message", "compose", map[string]any{"kind": kind, "instruction": "Prepare one local message", "source_ref": "source"})), kind: looptest.New(call("publish_result", "publish", map[string]any{"text": "An explicitly requested local " + kind}))}
			e := openTest(t, t.TempDir(), ms)
			defer e.Close()
			if _, err := e.Submit(ctx, testInput("source", "Prepare this one local "+kind)); err != nil {
				t.Fatal(err)
			}
			if err := e.Drain(ctx, 20); err != nil {
				t.Fatal(err)
			}
			ds, _ := e.Deliveries(ctx, "dm")
			if len(ds) != 1 || ds[0].State != "applied" || ds[0].Intent.Kind != kind {
				t.Fatal(ds)
			}
			if _, err := e.Submit(ctx, testInput("source", "Prepare this one local "+kind)); err != nil {
				t.Fatal(err)
			}
			if err := e.Drain(ctx, 20); err != nil {
				t.Fatal(err)
			}
			if ms[kind].Calls() != 1 || ms["chat"].Calls() != 1 {
				t.Fatal("repeated an explicit message loop")
			}
		})
	}
}

func TestExecutorCanReadCanonicalReceiptPathWithoutEscapingTask(t *testing.T) {
	ctx := context.Background()
	readCanonical := looptest.Func(func(in []*schema.Message) looptest.Step {
		var artifact work.Artifact
		for i := len(in) - 1; i >= 0; i-- {
			if in[i].Role == schema.Tool && in[i].ToolName == "write_file" {
				if err := json.Unmarshal([]byte(in[i].Content), &artifact); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		if artifact.Path == "" {
			t.Fatal("missing actual artifact receipt")
		}
		return call("read_file", "canonical-read", map[string]any{"path": artifact.Path})
	})
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("write_file", "write", map[string]any{"path": "report.md", "content": "# Result"}), readCanonical, call("finish_run", "done", map[string]any{"state": "succeeded", "summary": "Read canonical receipt path"})), "review": looptest.New(reviewStep(t))}
	e := openTest(t, t.TempDir(), ms)
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("canonical", "Write and read report.md")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	ts, _ := e.Tasks(ctx, "dm")
	if ts[0].State != work.Completed || len(ts[0].Runs) != 1 {
		t.Fatal(ts)
	}
}

func TestOwnerFailureHasExplicitRecoveryHandleAndPreservesPhysicalBudget(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	owner := looptest.New(looptest.Fail(errors.New("temporary provider failure")), call("commit_owner_update", "commit", map[string]any{"body": "The owner explicitly prefers concise Chinese.", "citations": []any{map[string]any{"source_id": "preference", "quote": "I prefer concise Chinese"}}}))
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(map[string]*looptest.Model{"chat": looptest.New(call("stay_quiet", "quiet", map[string]any{"source_ref": "preference"})), "owner_memory": owner})})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("preference", "I prefer concise Chinese")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 30); err == nil {
		t.Fatal("provider failure disappeared")
	}
	failures, err := e.Failures(ctx, "dm")
	if err != nil || len(failures) != 1 || failures[0].Kind != "owner" {
		t.Fatal(failures, err)
	}
	if err = e.Retry(ctx, failures[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	docs, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 1 || docs[0].Invalidated || owner.Calls() != 2 {
		t.Fatal(docs, err, owner.Calls())
	}
	// Retry reuses the original physical ledger, rather than starting with zero.
	s := e.backend.(*standalone)
	budgetScope := contextbuilder.OwnerKey(p.Scope("dm")) + "/owner/memory"
	raw, _ := json.Marshal([]string{budgetScope, failures[0].ID})
	budgets, err := s.db.List(ctx, "model-budget", string(raw))
	if err != nil || len(budgets) != 1 || !strings.Contains(string(budgets[0].Body), "2") {
		t.Fatal(budgets, err)
	}
}

func TestCallerCannotMutateOpenedProfilePermissions(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p.Scenes[0] = "foreign-scene"
	foreign := testInput("foreign", "not authorized")
	foreign.Scene = "foreign-scene"
	if _, err = e.Submit(ctx, foreign); err == nil {
		t.Fatal("caller mutated an opened permission snapshot")
	}
	if _, err = e.Submit(ctx, testInput("original", "still authorized")); err != nil {
		t.Fatal(err)
	}
}
