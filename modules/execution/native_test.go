package execution_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/execution"
	"github.com/cloudwego/eino/components/model"
)

type executionContext struct{ *submissionContext }

func (*executionContext) Source(context.Context, contract.Scope, string) (contextbuilder.Source, contextbuilder.Ref, error) {
	panic("no wait source in fixture")
}

// The default execution worker must work directly with the Eino kernel and
// Host ports, without an Entry-owned prompt or tool callback.
func TestNativeExecutesAndRecoversVerifiedArtifactsWithoutEntry(t *testing.T) {
	ctx := context.Background()
	bridge, task, frame, _ := bridgeFixture(t)
	md, err := markdown.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer md.Close()
	if _, err = bridge.Tasks.Submission(ctx, task.Scope, task.ID, task.CurrentRun().ID, "execution", false); err != nil {
		t.Fatal(err)
	}
	profile := contract.Profile{ID: "profile", Revision: "one", ModelName: "script", MaxModelCalls: 8}
	frozen, _ := json.Marshal(profile)
	script := looptest.New(
		looptest.Tools(looptest.Call{ID: "write", Name: "write_file", Args: map[string]any{"path": "report.md", "content": "verified report"}}),
		looptest.Tools(looptest.Call{ID: "read", Name: "read_file", Args: map[string]any{"path": "report.md"}}),
		looptest.Tools(looptest.Call{ID: "finish", Name: "finish_run", Args: map[string]any{"summary": "report verified", "state": "succeeded"}}),
	)
	runner := &kernel.Runner{State: bridge.State, Owner: "process", Authorize: bridge.Authorize, Models: func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error) { return script, nil }}
	record := execution.Record{ID: "execution", TaskID: task.ID, RunID: task.CurrentRun().ID, SubmitKey: task.CurrentRun().SubmitKey, Request: execution.Request{Task: task, Frame: frame.frame, Configuration: frozen}}
	worker := &execution.Native{State: bridge.State, Artifacts: md, Tasks: bridge.Tasks, Context: &executionContext{frame}, Runner: runner}
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
	worker = &execution.Native{State: bridge.State, Artifacts: md, Tasks: bridge.Tasks, Context: &executionContext{frame}, Runner: runner}
	replay, err := worker.Execute(ctx, record)
	if err != nil || !replay.Exited || replay.Output != result.Output || len(replay.Artifacts) != 1 || script.Calls() != 3 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, script.Calls(), err)
	}
}
