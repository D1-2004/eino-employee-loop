package entry

import (
	"context"
	"strings"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

func TestNativeFileCASRefusalCanReadAndCorrectInSameRun(t *testing.T) {
	ctx := context.Background()
	var oldHash string
	executor := looptest.New(
		call("write_file", "bad-write", map[string]any{"path": "report.md", "content": "new verified content"}),
		looptest.Func(func(messages []*schema.Message) looptest.Step {
			last := messages[len(messages)-1]
			if last.Role != schema.Tool || !strings.Contains(last.Content, "tool_rejected") {
				return looptest.Fail(context.Canceled)
			}
			return call("read_file", "read-old", map[string]any{"path": "report.md"})
		}),
		looptest.Func(func(_ []*schema.Message) looptest.Step {
			return call("write_file", "corrected-write", map[string]any{"path": "report.md", "content": "new verified content", "expected_sha256": oldHash})
		}),
		call("finish_run", "done", map[string]any{"state": "succeeded", "summary": "Updated actual report with verified CAS"}),
	)
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{"chat": looptest.New(dispatchStep(t)), "executor": executor, "review": looptest.New(reviewStep(t))})
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("source", "Update the accepted report")); err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if _, err := s.chat.Step(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	path := "tasks/" + contextbuilder.OwnerKey(testProfile().Scope("dm")) + "/" + tasks[0].ID + "/report.md"
	old, err := s.files.Write(ctx, path, "", "original report")
	if err != nil {
		t.Fatal(err)
	}
	oldHash = old.SHA256
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err = e.Tasks(ctx, "dm")
	if err != nil || tasks[0].State != work.Completed || len(tasks[0].Runs) != 1 || executor.Calls() != 4 {
		t.Fatal(tasks, err, executor.Calls())
	}
	actual, err := s.files.Read(ctx, path)
	if err != nil || actual.Body != "new verified content" {
		t.Fatal(actual, err)
	}
}
