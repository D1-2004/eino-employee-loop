//go:build workflowintegration

package workflow_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/workflow"
	"github.com/D1-2004/eino-employee-loop/workflow/filestore"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// This Host writes real local artifacts. Task execution is a bounded local
// fixture, not Multica's database, Runtime, human answers or IM delivery.
type lifecycleState struct {
	TaskID, Status, Goal, Constraint, Answer, Result, Digest string
	Effects                                                  map[string]lifecycleEffect
}
type lifecycleEffect struct {
	Call   loop.ToolCall
	Result loop.ToolResult
}
type lifecycleProvider struct {
	model                   model.ToolCallingChatModel
	dir                     string
	calls, effects, commits int
	reads                   map[string]bool
	observationErr          error
}

func (p *lifecycleProvider) Authorize(_ context.Context, r workflow.Request) error {
	if r.Key.Workspace != "local-lifecycle" || r.Key.Tenant != "local-owner" || r.Key.Employee != "local-agent" {
		return workflow.ErrDenied
	}
	return nil
}
func (p *lifecycleProvider) load(scene string) (lifecycleState, error) {
	var s lifecycleState
	raw, err := os.ReadFile(filepath.Join(p.dir, scene+".json"))
	if errors.Is(err, os.ErrNotExist) {
		s.Effects = map[string]lifecycleEffect{}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}
func (p *lifecycleProvider) save(scene string, s lifecycleState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := filepath.Join(p.dir, scene+".json")
	if err = os.WriteFile(path+".next", raw, 0600); err != nil {
		return err
	}
	if err = os.Rename(path+".next", path); err != nil {
		return err
	}
	back, err := os.ReadFile(path)
	if err != nil || string(back) != string(raw) {
		return errors.New("state readback failed")
	}
	return nil
}
func localTool(name, description string, effect bool, required ...string) loop.Tool {
	props := map[string]any{}
	for _, k := range required {
		props[k] = map[string]any{"type": "string", "minLength": 1}
	}
	return loop.Tool{Name: name, Description: description, Effect: effect, Schema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}
}
func (p *lifecycleProvider) Prepare(_ context.Context, r workflow.Request, s workflow.Snapshot) (workflow.Prepared, error) {
	current, err := p.load(r.Key.Scene)
	if err != nil {
		return workflow.Prepared{}, err
	}
	// Host supplies only current authorized state, excluding old effect payloads.
	current.Effects = nil
	raw, _ := json.Marshal(current)
	messages := []*schema.Message{schema.SystemMessage("You operate a local task fixture. Follow the current user's intent with native tools. Read tasks before creating work or answering a task status/result question. Continue or steer the existing task instead of duplicating it. Waiting needs a real wait_task receipt. Claims of completion require the current Host result and digest. A missing task in this scene must never be inferred from another scene. For ordinary answers call reply. Do not claim that local tasks run on a cloud or send messages. Current authorized Host state: " + string(raw))}
	messages = append(messages, s.History()...)
	messages = append(messages, r.Messages...)
	tools := []loop.Tool{
		localTool("read_tasks", "Read the tasks and result evidence in this current scene.", false),
		localTool("dispatch_task", "Create a new independent local task, only when no existing task satisfies it.", true, "goal"),
		localTool("continue_task", "Resume the current task with the user's new input or answer.", true, "task_id", "text"),
		localTool("steer_task", "Change the requirement of the current task without creating new work.", true, "task_id", "text"),
		localTool("wait_task", "Persist waiting for a missing answer from this user.", true, "task_id", "text"),
		localTool("stop_task", "Cancel the current unfinished task when the user asks.", true, "task_id"),
		localTool("reply", "Answer from verified current Host evidence. Do not invent work or results.", false, "reply"),
	}
	return workflow.Prepared{Model: p.model, Host: loop.HostFunc(p.execute), Messages: messages, Config: loop.Config{Tools: tools, RequireTerminalTool: true, Budget: loop.Budget{MaxModelResponses: 6, MaxConsecutiveErrors: 2, MaxPhysicalAttempts: 6}, OnModelCall: p.observe}}, nil
}
func (p *lifecycleProvider) observe(_ context.Context, call loop.ModelCall) {
	p.calls++
	raw, err := json.Marshal(struct {
		Attempt  int
		Request  []*schema.Message
		Response *schema.Message
		Error    string
	}{call.Attempt, call.Request, call.Response, fmt.Sprint(call.Err)})
	if err == nil {
		err = os.WriteFile(filepath.Join(p.dir, fmt.Sprintf("model-%02d.json", p.calls)), raw, 0600)
	}
	if err != nil {
		p.observationErr = err
	}
}
func (p *lifecycleProvider) execute(_ context.Context, id loop.Identity, c loop.ToolCall) (loop.ToolResult, error) {
	s, err := p.load(id.Scene)
	if err != nil {
		return loop.ToolResult{}, err
	}
	if c.Name == "read_tasks" {
		if p.reads == nil {
			p.reads = map[string]bool{}
		}
		p.reads[id.Scene+"/"+id.Receipt] = true
		s.Effects = nil
		raw, _ := json.Marshal(s)
		return loop.ToolResult{Content: string(raw)}, nil
	}
	if c.Name == "reply" {
		return loop.ToolResult{Terminal: &loop.Decision{Kind: loop.Reply, Reply: c.Arguments["reply"].(string)}}, nil
	}
	key := id.Receipt + "/" + c.NativeToolCallID
	if prior, ok := s.Effects[key]; ok {
		a, _ := json.Marshal(prior.Call)
		b, _ := json.Marshal(c)
		if string(a) != string(b) {
			return loop.ToolResult{}, workflow.ErrConflict
		}
		return prior.Result, nil
	}
	get := func(k string) string { v, _ := c.Arguments[k].(string); return v }
	kind := loop.Dispatched
	if c.Name != "dispatch_task" && (s.TaskID == "" || get("task_id") != s.TaskID) {
		return loop.ToolResult{}, loop.ErrToolRefused
	}
	switch c.Name {
	case "dispatch_task":
		if !p.reads[id.Scene+"/"+id.Receipt] {
			return loop.ToolResult{}, errors.Join(loop.ErrToolRefused, errors.New("read_tasks required before dispatch"))
		}
		if s.TaskID != "" {
			return loop.ToolResult{}, loop.ErrToolRefused
		}
		s.TaskID = "task-" + id.Scene
		s.Status = "running"
		s.Goal = get("goal")
	case "continue_task":
		if s.Status == "stopped" || s.Status == "completed" {
			return loop.ToolResult{}, loop.ErrToolRefused
		}
		s.Answer = get("text")
		s.Status = "running"
	case "steer_task":
		if s.Status == "stopped" || s.Status == "completed" {
			return loop.ToolResult{}, loop.ErrToolRefused
		}
		s.Constraint = get("text")
	case "wait_task":
		s.Status = "waiting"
		s.Constraint = get("text")
		kind = loop.Waiting
	case "stop_task":
		if s.Status == "completed" {
			return loop.ToolResult{}, loop.ErrToolRefused
		}
		s.Status = "stopped"
	default:
		return loop.ToolResult{}, loop.ErrToolRefused
	}
	payload, _ := json.Marshal(map[string]string{"task_id": s.TaskID, "status": s.Status})
	result := loop.ToolResult{Receipt: "local-effect/" + id.Scene + "/" + key, Content: string(payload), Terminal: &loop.Decision{Kind: kind}}
	s.Effects[key] = lifecycleEffect{Call: c, Result: result}
	if err = p.save(id.Scene, s); err != nil {
		return loop.ToolResult{}, err
	}
	p.effects++
	return result, nil
}
func (p *lifecycleProvider) Commit(_ context.Context, r workflow.Request, out loop.Outcome) (string, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	path := filepath.Join(p.dir, "commit-"+r.Receipt+".json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		return "", err
	}
	back, err := os.ReadFile(path)
	if err != nil || string(back) != string(raw) {
		return "", errors.New("commit readback failed")
	}
	p.commits++
	return "local-commit/" + r.Receipt, nil
}
func TestOwnModelConfiguredTaskLifecycle(t *testing.T) {
	if os.Getenv("EEL_RUN_WORKFLOW_MODEL_CHECK") != "1" {
		t.Skip("explicit own-model opt-in required")
	}
	dir := os.Getenv("EEL_LIFECYCLE_DIR")
	if dir == "" {
		t.Skip("private local artifact directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if os.Getenv(k) == "" {
			t.Fatalf("missing %s", k)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL"), Timeout: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p := &lifecycleProvider{model: m, dir: dir}
	registry := &workflow.Registry{}
	if err = registry.Register("local-tasks", "candidate-1", p); err != nil {
		t.Fatal(err)
	}
	profile, err := workflow.ParseProfile([]byte(`{"version":"workflow-profile/1","name":"local-acceptance","revision":"r1","agents":[{"name":"assistant","provider":"local-tasks","provider_version":"candidate-1","lane":"foreground","session":"interactive"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := filestore.Store{Dir: filepath.Join(dir, "sessions")}
	assembly := func() *workflow.Assembly {
		a, e := workflow.NewAssembly(profile, registry, store, p.Authorize)
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	key := workflow.Key{Workspace: "local-lifecycle", Tenant: "local-owner", Employee: "local-agent", Scene: "scene-a"}
	sequence := 0
	run := func(receipt, text string) workflow.Result {
		t.Helper()
		before, e := p.load(key.Scene)
		if e != nil {
			t.Fatal(e)
		}
		beforeCounters := []int{p.calls, p.effects, p.commits}
		out, e := assembly().Run(ctx, "assistant", key, receipt, []*schema.Message{schema.UserMessage(text)}, nil)
		if e != nil {
			t.Fatalf("%s: %v phase=%s", receipt, e, out.Phase)
		}
		sequence++
		after, e := p.load(key.Scene)
		if e != nil {
			t.Fatal(e)
		}
		fullKey := key
		fullKey.Profile = profile.Name
		fullKey.ProfileRevision = profile.Revision
		fullKey.ProfileBinding = "assistant"
		fullKey.Lane = "foreground"
		fullKey.Subject = "interactive"
		event := map[string]any{"sequence": sequence, "time": time.Now().UTC().Format(time.RFC3339Nano), "key": fullKey, "receipt": receipt, "replay": out.Replay, "before": before, "after": after, "before_counters": beforeCounters, "after_counters": []int{p.calls, p.effects, p.commits}, "outcome": out.Outcome}
		raw, e := json.MarshalIndent(event, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("event-%02d.json", sequence)), raw, 0600); e != nil {
			t.Fatal(e)
		}
		if p.observationErr != nil {
			t.Fatal(p.observationErr)
		}
		return out
	}
	state := func() lifecycleState {
		t.Helper()
		s, e := p.load(key.Scene)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	run("create", "先查当前任务；如果没有，创建一项比较两家供应商报价的本地任务。")
	s := state()
	if s.TaskID == "" || s.Status != "running" || p.effects != 1 {
		t.Fatal("dispatch not persisted", s, p.effects)
	}
	run("continue", "按刚才完全相同的要求继续执行供应商报价比较，不增加或修改要求。")
	s = state()
	if s.Answer == "" || p.effects != 2 {
		t.Fatal("continuation duplicated/lost", s, p.effects)
	}
	run("steer", "改一下刚才那项任务的要求：比较范围只看企业版，别新建任务。")
	s = state()
	if !strings.Contains(s.Constraint, "企业") || p.effects != 3 {
		t.Fatal("steering not persisted", s, p.effects)
	}
	out := run("wait", "现在先等待我补充采购季度，不要继续，也别取消任务。")
	s = state()
	if s.Status != "waiting" || out.Outcome.Kind != loop.Waiting || p.effects != 4 {
		t.Fatal("wait has no receipt", out, s, p.effects)
	}
	run("answer", "采购季度是 Q3，请用这个答案继续刚才等待的任务。")
	s = state()
	if s.Status != "running" || !strings.Contains(strings.ToUpper(s.Answer), "Q3") || p.effects != 5 {
		t.Fatal("answer not bound to current task", s, p.effects)
	}
	// A real local executor artifact, independent of anything the model claims.
	content := []byte("LOCAL-RESULT-7F3: supplier A costs 120, supplier B costs 150; enterprise edition, Q3.")
	artifact := filepath.Join(dir, "supplier-result.txt")
	if err = os.WriteFile(artifact, content, 0600); err != nil {
		t.Fatal(err)
	}
	back, e := os.ReadFile(artifact)
	if e != nil || string(back) != string(content) {
		t.Fatal(e)
	}
	hash := sha256.Sum256(back)
	s.Result = string(back)
	s.Digest = hex.EncodeToString(hash[:])
	s.Status = "completed"
	if err = p.save(key.Scene, s); err != nil {
		t.Fatal(err)
	}
	out = run("result", "查一下刚才任务的实际结果，把其中两家供应商的金额告诉我。")
	if out.Outcome.Kind != loop.Reply || !strings.Contains(out.Outcome.Reply, "120") || !strings.Contains(out.Outcome.Reply, "150") || p.effects != 5 {
		t.Fatal("result not grounded in artifact", out, p.effects)
	}
	calls, commits := p.calls, p.commits
	out = run("answer", "采购季度是 Q3，请用这个答案继续刚才等待的任务。")
	if !out.Replay || p.calls != calls || p.commits != commits || p.effects != 5 {
		t.Fatal("replay repeated effects", out, p.calls, p.effects, p.commits)
	}
	key.Scene = "scene-b"
	out = run("isolation", "查一下我当前会话的任务和结果；如果没有就明确说没有。")
	if strings.Contains(out.Outcome.Reply, "120") || strings.Contains(out.Outcome.Reply, "150") || state().TaskID != "" || p.effects != 5 {
		t.Fatal("cross-scene data or effect leaked", out, state(), p.effects)
	}
	run("cancel-create", "创建一项本地任务，整理下一次采购的问题清单。")
	run("cancel", "取消当前这项任务，不要继续执行。")
	if state().Status != "stopped" || p.effects != 7 {
		t.Fatal("cancellation not persisted", state(), p.effects)
	}
	t.Logf("model=%s calls=%d real_file_effects=%d commits=%d; configured plugin, restart, read/dispatch/continue/steer/wait/answer/result/cancel/isolation/replay passed; local fixture only", os.Getenv("LLM_MODEL"), p.calls, p.effects, p.commits)
	manifest := map[string]any{"model": os.Getenv("LLM_MODEL"), "calls": p.calls, "effects": p.effects, "commits": p.commits, "result_sha256": s.Digest, "profile": profile, "scope": "local fixture; no Multica DB, Runtime or IM"}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err = os.WriteFile(filepath.Join(dir, "acceptance.json"), raw, 0600); err != nil {
		t.Fatal(fmt.Errorf("save acceptance: %w", err))
	}
}
