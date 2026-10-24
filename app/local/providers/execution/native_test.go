package execution_test

import (
	"context"
	"encoding/json"
	"testing"

	localexec "github.com/D1-2004/eino-employee-loop/app/local/providers/execution"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/markdown"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/components/model"
)

// The default execution worker must work directly with the Eino kernel and
// Host ports, without an Entry-owned prompt or tool callback.
func TestNativeExecutesAndRecoversVerifiedArtifactsWithoutEntry(t *testing.T) {
	ctx := context.Background()
	local, _, _ := fixture(t)
	md, err := markdown.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer md.Close()
	profile := contract.Profile{ID: "profile", Revision: "one", ModelName: "script", MaxModelCalls: 8}
	frozen, _ := json.Marshal(profile)
	tasks, err := work.New(local.DB)
	if err != nil {
		t.Fatal(err)
	}
	scope := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "scene", Requester: "owner"}
	source := work.Source{Receipt: "source", Actor: "owner", Text: "write and verify report"}
	accepted, err := tasks.Dispatch(ctx, scope, source, "dispatch", work.Definition{Goal: "write report", Instruction: source.Text, Configuration: frozen})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(ctx, scope, accepted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.BeginSubmission(ctx, scope, task.ID, task.CurrentRun().ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.Submission(ctx, scope, task.ID, task.CurrentRun().ID, "execution", false); err != nil {
		t.Fatal(err)
	}
	builder, err := contextbuilder.New(local.DB, md, tasks, func(context.Context, work.Scope, contextbuilder.View) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = builder.Admit(ctx, scope, contextbuilder.Source{ID: source.Receipt, Actor: source.Actor, Text: source.Text}); err != nil {
		t.Fatal(err)
	}
	frame, err := builder.Build(ctx, scope, contextbuilder.Execution, "", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	script := looptest.New(
		looptest.Tools(looptest.Call{ID: "write", Name: "write_file", Args: map[string]any{"path": "report.md", "content": "verified report"}}),
		looptest.Tools(looptest.Call{ID: "read", Name: "read_file", Args: map[string]any{"path": "report.md"}}),
		looptest.Tools(looptest.Call{ID: "finish", Name: "finish_run", Args: map[string]any{"summary": "report verified", "state": "succeeded"}}),
	)
	runner := &kernel.Runner{State: local.DB, Owner: "process", Authorize: local.Authorize, Models: func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error) { return script, nil }}
	record := execution.Record{ID: "execution", TaskID: task.ID, RunID: task.CurrentRun().ID, SubmitKey: task.CurrentRun().SubmitKey, Request: execution.Request{Task: task, Frame: frame, Configuration: frozen}}
	worker := &localexec.Native{State: local.DB, Artifacts: md, Tasks: tasks, Context: builder, Runner: runner}
	result, err := worker.Execute(ctx, record)
	if err != nil || !result.Exited || result.Output != "report verified" || len(result.Artifacts) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	doc, err := md.Read(ctx, result.Artifacts[0].Path)
	if err != nil || doc.Body != "verified report" || doc.SHA256 != result.Artifacts[0].SHA256 {
		t.Fatal(doc, err)
	}
	// Reconstruct the execution worker: terminal recovery reads the accepted tool
	// result and persisted artifact evidence without calling the model again.
	worker = &localexec.Native{State: local.DB, Artifacts: md, Tasks: tasks, Context: builder, Runner: runner}
	replay, err := worker.Execute(ctx, record)
	if err != nil || !replay.Exited || replay.Output != result.Output || len(replay.Artifacts) != 1 || script.Calls() != 3 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, script.Calls(), err)
	}
}
