package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

func example(name string) string { return filepath.Join("..", "..", "evals", "cases", "local", name) }
func invoke(t *testing.T, dir, script string, args ...string) (string, error) {
	return invokeProfile(t, "profile.json", dir, script, args...)
}
func invokeProfile(t *testing.T, profile, dir, script string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	base := []string{"-profile", example(profile), "-dir", dir}
	if script != "" {
		base = append(base, "-script", example(script))
	}
	base = append(base, args...)
	err := run(context.Background(), base, strings.NewReader(""), &out, &errOut)
	return out.String(), err
}

type stateOutput struct {
	Type       string               `json:"type"`
	Tasks      []work.Task          `json:"tasks"`
	Pending    map[string][]string  `json:"pending"`
	Deliveries []interaction.Record `json:"deliveries"`
}

func readState(t *testing.T, raw string) stateOutput {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(raw))
	var result stateOutput
	for d.More() {
		var next stateOutput
		if err := d.Decode(&next); err != nil {
			t.Fatal(err)
		}
		if next.Type == "local_state" {
			result = next
		}
	}
	if result.Type == "" {
		t.Fatalf("no persisted state in output: %s", raw)
	}
	return result
}

func TestReplayCommandPersistsVerifiedArtifactAndReceiptAcrossReopen(t *testing.T) {
	for _, key := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	out, err := invoke(t, dir, "script.json", "replay", "-in", example("input.ndjson"))
	if err != nil {
		t.Fatalf("replay: %v\n%s", err, out)
	}
	state := readState(t, out)
	if len(state.Tasks) != 1 || state.Tasks[0].State != work.Completed || len(state.Tasks[0].Runs) != 1 {
		t.Fatalf("tasks=%+v", state.Tasks)
	}
	artifacts := state.Tasks[0].Runs[0].Artifacts
	if len(artifacts) != 1 {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workspace", filepath.FromSlash(artifacts[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SQLite 保存状态，Markdown 保存文件。") {
		t.Fatalf("actual artifact=%s", raw)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != artifacts[0].SHA256 {
		t.Fatal("persisted artifact SHA256 does not match actual file")
	}
	if len(state.Deliveries) != 1 || state.Deliveries[0].State != "applied" || state.Deliveries[0].Receipt == nil || !strings.HasPrefix(state.Deliveries[0].Receipt.ProviderMessageID, "local-file/") {
		t.Fatalf("deliveries=%+v", state.Deliveries)
	}
	// Reopen with no script or credentials: status must only read persisted facts.
	out, err = invoke(t, dir, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	after := readState(t, out)
	if len(after.Tasks) != 1 || after.Tasks[0].ID != state.Tasks[0].ID || after.Tasks[0].Runs[0].ID != state.Tasks[0].Runs[0].ID {
		t.Fatalf("reopen=%+v", after.Tasks)
	}
	out, err = invoke(t, dir, "script.json", "replay", "-in", example("input.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"Replay":true`) {
		t.Fatalf("missing replay receipt: %s", out)
	}
	after = readState(t, out)
	if len(after.Tasks) != 1 || len(after.Tasks[0].Runs) != 1 || len(after.Deliveries) != 1 {
		t.Fatalf("duplicate side effect=%+v", after)
	}
}

func TestAnswerCommandResumesExactPersistedChatTarget(t *testing.T) {
	dir := t.TempDir()
	out, err := invoke(t, dir, "ask-script.json", "submit", "-id", "question-1", "-text", "请创建报告")
	if err != nil {
		t.Fatalf("submit: %v\n%s", err, out)
	}
	state := readState(t, out)
	if len(state.Pending) != 1 {
		t.Fatalf("pending=%+v", state.Pending)
	}
	var job, target string
	for j, targets := range state.Pending {
		if len(targets) != 1 {
			t.Fatalf("targets=%v", targets)
		}
		job, target = j, targets[0]
	}
	if _, err = invoke(t, dir, "ask-script.json", "answer", "-job", job, "-target", "wrong-target", "-id", "bad-answer", "-text", "标题"); err == nil {
		t.Fatal("accepted wrong resume target")
	}
	out, err = invoke(t, dir, "ask-script.json", "answer", "-job", job, "-target", target, "-id", "answer-1", "-text", "本地验证报告")
	if err != nil {
		t.Fatalf("answer: %v\n%s", err, out)
	}
	state = readState(t, out)
	if len(state.Pending) != 0 || len(state.Tasks) != 0 || len(state.Deliveries) != 2 {
		t.Fatalf("state=%+v", state)
	}
	for _, d := range state.Deliveries {
		if d.State != "applied" || d.Receipt == nil {
			t.Fatalf("unsettled delivery=%+v", d)
		}
	}
}

func TestRuleModelIsStatelessAndBindsSource(t *testing.T) {
	factory, err := loadRuleModels(example("script.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := entry.ModelRequest{Role: "chat", Receipt: "original-source", ModelName: "scripted-local-demo"}
	for range 2 {
		m, err := factory(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("input")})
		if err != nil {
			t.Fatal(err)
		}
		if len(msg.ToolCalls) != 1 || !strings.Contains(msg.ToolCalls[0].Function.Arguments, "original-source") {
			t.Fatalf("message=%+v", msg)
		}
	}
	request.ModelName = "different-model"
	if _, err = factory(context.Background(), request); err == nil {
		t.Fatal("script silently replaced frozen model")
	}
	owners, err := loadRuleModels(example("allloops-script.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := owners(context.Background(), entry.ModelRequest{Role: "owner_memory", Receipt: "opaque-owner-job", ModelName: "scripted-local-demo"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage(`{"current":{"ID":"human-source","Text":"精确原文"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	var args struct {
		Citations []struct {
			SourceID string `json:"source_id"`
			Quote    string `json:"quote"`
		} `json:"citations"`
	}
	if err = json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Citations) != 1 || args.Citations[0].SourceID != "human-source" || args.Citations[0].Quote != "精确原文" {
		t.Fatalf("model invented citation=%+v", args)
	}
}

func TestServeReportsOwnedWorkerFailureAndWaitsForCancellation(t *testing.T) {
	for _, key := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	var profile entry.Profile
	if err := readJSON(example("profile.json"), &profile); err != nil {
		t.Fatal(err)
	}
	models, err := modelFactory("")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Open(context.Background(), Options{Directory: dir, Profile: profile, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(context.Background(), entry.Input{ID: "serve-message", Scene: profile.Scenes[0], Actor: profile.Owner, Text: "hello", Origin: "local-cli"}); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out, errOut bytes.Buffer
	err = run(ctx, []string{"-profile", example("profile.json"), "-dir", dir, "serve"}, strings.NewReader(""), &out, &errOut)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(out.String(), `"type":"worker_error"`) || !strings.Contains(out.String(), "LLM_BASE_URL") {
		t.Fatalf("serve must report job failure and remain alive until cancellation: %v; output=%s", err, out.String())
	}
}

func TestAllConfiguredLoopsAndSourceBoundScheduleCommands(t *testing.T) {
	dir := t.TempDir()
	out, err := invokeProfile(t, "allloops-profile.json", dir, "allloops-script.json", "replay", "-in", example("allloops-input.ndjson"))
	if err != nil {
		t.Fatalf("allloops: %v\n%s", err, out)
	}
	state := readState(t, out)
	if len(state.Tasks) != 1 || state.Tasks[0].State != work.Completed || len(state.Deliveries) != 1 {
		t.Fatalf("state=%+v", state)
	}
	if !strings.Contains(state.Deliveries[0].Intent.Body, "report.md 已创建并核验。") || !strings.Contains(state.Deliveries[0].Intent.Body, "packages/") {
		t.Fatalf("expression/package not used: %+v", state.Deliveries)
	}
	packages, err := filepath.Glob(filepath.Join(dir, "workspace", "packages", "*", "*.md"))
	if err != nil || len(packages) != 1 {
		t.Fatalf("packages=%v err=%v", packages, err)
	}
	pkg, err := os.ReadFile(packages[0])
	if err != nil || !strings.Contains(string(pkg), "report.md 的实际文件、SHA256 和原始任务信息") {
		t.Fatalf("package=%s err=%v", pkg, err)
	}
	out, err = invokeProfile(t, "allloops-profile.json", dir, "", "owner-status")
	if err != nil {
		t.Fatal(err)
	}
	var owners struct {
		Documents []ownerloop.Document `json:"documents"`
	}
	if err = json.Unmarshal([]byte(out), &owners); err != nil {
		t.Fatal(err)
	}
	if len(owners.Documents) != 6 {
		t.Fatalf("owner documents=%+v", owners.Documents)
	}
	for _, doc := range owners.Documents {
		if doc.Invalidated || len(doc.Evidence) != 1 || doc.Evidence[0].Ref.ID != "allloops-message-1" || !strings.Contains(doc.Evidence[0].Quote, "我偏好简洁中文") {
			t.Fatalf("invalid owner source=%+v", doc)
		}
		body, e := os.ReadFile(filepath.Join(dir, "workspace", filepath.FromSlash(doc.Path)))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != doc.SHA256 {
			t.Fatalf("owner projection=%+v err=%v", doc, e)
		}
	}
	var schedule scheduleDocument
	if err = readJSON(example("schedule.json"), &schedule); err != nil {
		t.Fatal(err)
	}
	schedule.TaskID = state.Tasks[0].ID
	writeSchedule := func() string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "schedule.json")
		body, e := json.Marshal(schedule)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	out, err = invokeProfile(t, "allloops-profile.json", dir, "allloops-script.json", "schedule", "-schedule", writeSchedule())
	if err != nil || !strings.Contains(out, `"type":"schedule_recorded"`) {
		t.Fatalf("schedule: %v\n%s", err, out)
	}
	schedule.ID = "forged-source"
	schedule.Source.Text = "model rewritten source"
	if _, err = invokeProfile(t, "allloops-profile.json", dir, "allloops-script.json", "schedule", "-schedule", writeSchedule()); err == nil {
		t.Fatal("schedule accepted a rewritten original source")
	}
	out, err = invokeProfile(t, "allloops-profile.json", dir, "allloops-script.json", "signal", "-condition", "report.updated", "-event", example("signal.json"))
	if err != nil || !strings.Contains(out, `"type":"signal_recorded"`) {
		t.Fatalf("signal: %v\n%s", err, out)
	}
	after := readState(t, out)
	if len(after.Tasks) != 1 || after.Tasks[0].ID != state.Tasks[0].ID || len(after.Tasks[0].Runs) != 1 {
		t.Fatalf("signal duplicated task/run=%+v", after.Tasks)
	}
}

func TestScheduleDocumentRequiresExplicitVersionAndBudget(t *testing.T) {
	valid := scheduleDocument{Version: "local-el-schedule/1", ID: "s", TaskID: "t", GoalRevision: 1, Source: work.Source{Receipt: "m", Actor: "a", Text: "original"}, Due: time.Now(), Every: "1h", Limit: 3}
	s, err := valid.schedule()
	if err != nil || s.Every != time.Hour || s.Limit != 3 {
		t.Fatalf("schedule=%+v err=%v", s, err)
	}
	for _, mutate := range []func(*scheduleDocument){func(d *scheduleDocument) { d.Version = "" }, func(d *scheduleDocument) { d.Every = "" }, func(d *scheduleDocument) { d.Limit = 0 }, func(d *scheduleDocument) { d.GoalRevision = 0 }} {
		d := valid
		mutate(&d)
		if _, err = d.schedule(); err == nil {
			t.Fatalf("accepted ambiguous schedule=%+v", d)
		}
	}
}
