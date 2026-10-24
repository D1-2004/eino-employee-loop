package entry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func testProfile() Profile {
	return Profile{Version: "local-el-profile/1", ID: "local", Revision: "v1", Workspace: "ws", Tenant: "tenant", Employee: "employee", Owner: "owner", Name: "Employee", Backend: "standalone", ModelName: "deterministic-test", Scenes: []string{"dm"}, MaxModelCalls: 16, MaxRuns: 4}
}
func testInput(id, text string) Input {
	return Input{ID: id, Scene: "dm", Actor: "owner", Text: text, Origin: "explicit-replay"}
}
func frameOf(t *testing.T, in []*schema.Message) contextbuilder.Frame {
	t.Helper()
	var f contextbuilder.Frame
	if err := json.Unmarshal([]byte(in[1].Content), &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func call(name, id string, args map[string]any) looptest.Step {
	return looptest.Tools(looptest.Call{ID: id, Name: name, Args: args})
}
func dispatchStep(t *testing.T) looptest.Step {
	return looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		return call("dispatch_task", "dispatch", map[string]any{"goal": f.Current.Text, "instruction": f.Current.Text, "source_ref": f.Current.ID})
	})
}
func reviewStep(t *testing.T) looptest.Step {
	return looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		kind := "complete"
		if f.Tasks[0].CurrentRun().State == work.RunSuspended {
			kind = "resume"
		}
		return call("decide_review", "review", map[string]any{"kind": kind, "summary": "Verified the actual artifact and original instructions"})
	})
}
func models(ms map[string]*looptest.Model) Models {
	return func(_ context.Context, r ModelRequest) (model.ToolCallingChatModel, error) {
		m := ms[r.Role]
		if m == nil {
			return nil, errors.New("unexpected role: " + r.Role)
		}
		return m, nil
	}
}
func openTest(t *testing.T, dir string, ms map[string]*looptest.Model) *Entry {
	t.Helper()
	e, err := openDefault(context.Background(), Options{Directory: dir, Profile: testProfile(), Models: models(ms)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestLocalCompletePathAndReceiptReplayAfterReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("write_file", "file", map[string]any{"path": "result.md", "content": "# Actual result\nOriginal constraints preserved."}), call("read_file", "read", map[string]any{"path": "result.md"}), call("finish_run", "done", map[string]any{"state": "succeeded", "summary": "Written and read back result.md"})), "review": looptest.New(reviewStep(t))}
	e := openTest(t, dir, ms)
	in := testInput("source-one", "Write an actual Markdown result")
	a, err := e.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := e.Submit(ctx, in); err != nil || !b.Replay || b.ID != a.ID {
		t.Fatal(b, err)
	}
	bad := in
	bad.Text = "different"
	if _, err = e.Submit(ctx, bad); err == nil {
		t.Fatal("mutated receipt accepted")
	}
	bad = in
	bad.Actor = "stranger"
	if _, err = e.Submit(ctx, bad); err == nil {
		t.Fatal("unauthorized source accepted")
	}
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	task := tasks[0]
	r := task.CurrentRun()
	if task.State != work.Completed || r == nil || !r.ExitProven || len(r.Artifacts) != 1 || len(task.Runs) != 1 {
		t.Fatal(task)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workspace", filepath.FromSlash(r.Artifacts[0].Path)))
	if err != nil || !strings.Contains(string(raw), "Original constraints preserved") {
		t.Fatal(string(raw), err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" || deliveries[0].Receipt == nil {
		t.Fatal(deliveries, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e = openTest(t, dir, ms)
	defer e.Close()
	if _, err = e.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 10); err != nil {
		t.Fatal(err)
	}
	again, _ := e.Tasks(ctx, "dm")
	if len(again) != 1 || again[0].Revision != task.Revision || ms["chat"].Calls() != 1 || ms["executor"].Calls() != 3 || ms["review"].Calls() != 1 {
		t.Fatal(again)
	}
}
func TestNativeExecutionQuestionAnswerRebuildKeepsSameTaskAndRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	answer := looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		task := f.Tasks[0]
		return call("answer_question", "answer", map[string]any{"source_ref": f.Current.ID, "task_id": task.ID, "wait_id": task.Waits[0].ID, "goal_revision": task.Goal.Revision})
	})
	ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t), answer), "executor": looptest.New(call("write_file", "file", map[string]any{"path": "result.md", "content": "# Created before interruption"}), call("ask_input", "ask", map[string]any{"prompt": "Which quarter?"}), call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "Q3 answer received; artifact verified"})), "review": looptest.New(reviewStep(t), reviewStep(t))}
	e := openTest(t, dir, ms)
	if _, err := e.Submit(ctx, testInput("start", "Write a result; ask for the quarter")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		ts, _ := e.Tasks(ctx, "dm")
		t.Fatal(err, ts)
	}
	tasks, _ := e.Tasks(ctx, "dm")
	if len(tasks) != 1 || tasks[0].State != work.Waiting || tasks[0].CurrentRun().State != work.RunSuspended {
		t.Fatal(tasks)
	}
	id, runID := tasks[0].ID, tasks[0].CurrentRun().ID
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e = openTest(t, dir, ms)
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("answer-source", "Q3")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, _ = e.Tasks(ctx, "dm")
	if len(tasks) != 1 || tasks[0].ID != id || tasks[0].State != work.Completed || tasks[0].CurrentRun().ID != runID || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks)
	}
	if ms["executor"].Calls() != 3 || ms["review"].Calls() != 2 {
		t.Fatal("native continuation repeated earlier calls", ms["executor"].Calls(), ms["review"].Calls())
	}
}
func TestRecoverIntentBeforeLocalSubmissionAndStopWithoutInventedExecution(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "stop"}[stop], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			ms := map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": looptest.New(call("finish_run", "done", map[string]any{"state": "succeeded", "summary": "No file was requested"})), "review": looptest.New(reviewStep(t))}
			e := openTest(t, dir, ms)
			_, err := e.Submit(ctx, testInput("start", "Compute a response"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			s := e.backend.(*standalone)
			ts, _ := e.Tasks(ctx, "dm")
			task := ts[0]
			if _, err = s.work.BeginSubmission(ctx, task.Scope, task.ID, task.CurrentRun().ID); err != nil {
				t.Fatal(err)
			}
			if stop {
				if _, err = s.work.Control(ctx, task.Scope, work.Source{Actor: "owner", Receipt: "stop", Text: "stop"}, "stop", task.ID, "stop", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e = openTest(t, dir, ms)
			defer e.Close()
			if err = e.Drain(ctx, 30); err != nil {
				t.Fatal(err)
			}
			ts, _ = e.Tasks(ctx, "dm")
			if !ts[0].CurrentRun().ExitProven {
				t.Fatal(ts)
			}
			if stop && (ts[0].State != work.Stopped || ms["executor"].Calls() != 0 || ts[0].CurrentRun().ExecutionID != "") {
				t.Fatal("invented execution on never-submitted stop", ts)
			}
		})
	}
}

type blockingModel struct {
	entered chan struct{}
	once    sync.Once
}

func (m *blockingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *blockingModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.once.Do(func() { close(m.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *blockingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func TestCloseCancelsExplicitRunOnceBeforeClosingStorage(t *testing.T) {
	ctx := context.Background()
	m := &blockingModel{entered: make(chan struct{})}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: func(context.Context, ModelRequest) (model.ToolCallingChatModel, error) { return m, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(ctx, testInput("cancel", "Hello")); err != nil {
		t.Fatal(err)
	}
	ran := make(chan error, 1)
	go func() { _, err := e.RunOnce(ctx); ran <- err }()
	select {
	case <-m.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("model never started")
	}
	closed := make(chan error, 1)
	go func() { closed <- e.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel an explicit operation")
	}
	if err := <-ran; err == nil {
		t.Fatal("cancelled operation accepted")
	}
	if _, err = e.Tasks(ctx, "dm"); err == nil {
		t.Fatal("read after Close")
	}
}
