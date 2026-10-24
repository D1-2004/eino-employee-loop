package local_test

import (
	"context"
	"encoding/json"
	"errors"
	localservices "github.com/D1-2004/eino-employee-loop/adapters/services/local"
	host "github.com/D1-2004/eino-employee-loop/ports/services"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/adapters/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/cloudwego/eino/components/tool"
)

type fixture struct {
	services host.Services
	call     host.Call
	builder  *contextbuilder.Builder
	files    *markdown.Store
	artifact host.ResourceRef
	config   json.RawMessage
	tasks    *work.Manager
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files, err := markdown.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	tasks, err := work.New(db)
	if err != nil {
		t.Fatal(err)
	}
	scope := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "opaque-room", Requester: "owner"}
	b, err := contextbuilder.New(db, files, tasks, func(_ context.Context, s work.Scope, _ contextbuilder.View) error {
		if s != scope {
			return work.ErrScope
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Admit(ctx, scope, contextbuilder.Source{ID: "source", Actor: scope.Requester, Text: "write verified report"}); err != nil {
		t.Fatal(err)
	}
	r, err := tasks.Dispatch(ctx, scope, work.Source{Receipt: "source", Actor: scope.Requester, Text: "write verified report"}, "native-dispatch", work.Definition{Goal: "report", Instruction: "write report", MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(ctx, scope, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run := task.CurrentRun()
	f, err := files.Write(ctx, "tasks/"+contextbuilder.OwnerKey(scope)+"/"+task.ID+"/report.md", "", "verified report")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.BeginSubmission(ctx, scope, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.Submission(ctx, scope, task.ID, run.ID, "execution", false); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.RecordExecution(ctx, scope, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "execution", State: work.RunSucceeded, ExitProven: true, Artifacts: []work.Artifact{{Path: f.Path, SHA256: f.SHA256, Bytes: int64(len(f.Body))}}}); err != nil {
		t.Fatal(err)
	}
	config := json.RawMessage(`{"profile":"actual-local-profile"}`)
	s, err := localservices.NewLocal(host.Bindings{Authorize: func(_ context.Context, c host.Call) error {
		if c.Scope != scope || string(c.Binding) != `{"trusted":"binding"}` {
			return work.ErrScope
		}
		return nil
	}, Config: func(context.Context, host.Call) (host.ConfigSnapshot, error) {
		return host.ConfigSnapshot{ID: "profile", Version: "v1", Value: config}, nil
	}, Sources: b, Tasks: tasks, Artifacts: files})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{s, host.Call{Scope: scope, SourceReceipt: "source", TaskID: task.ID, RunID: run.ID, Binding: json.RawMessage(`{"trusted":"binding"}`)}, b, files, host.ResourceRef{TaskID: task.ID, RunID: run.ID, Path: f.Path, Version: f.SHA256, SHA256: f.SHA256}, config, tasks}
}
func TestLocalReadServicesUseRealFactsAndNativeEinoTools(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cfg, err := f.services.Config.GetConfig(ctx, f.call)
	if err != nil || !strings.Contains(string(cfg.Value), "actual-local-profile") {
		t.Fatal(cfg, err)
	}
	cfg.Value[0] = 'x'
	if !json.Valid(f.config) {
		t.Fatal("config alias escaped")
	}
	m, err := f.services.Messages.GetMessages(ctx, f.call, host.MessageRequest{IDs: []string{"source", "missing"}})
	if err != nil || len(m.Items) != 1 || len(m.Coverage.MissingIDs) != 1 || m.Coverage.Source != "admitted_local_sources" {
		t.Fatal(m, err)
	}
	listed, err := f.services.Files.ListFiles(ctx, f.call)
	if err != nil || len(listed.Items) != 1 || listed.Items[0] != f.artifact {
		t.Fatal(listed, err)
	}
	r, err := f.services.Resources.ReadResource(ctx, f.call, f.artifact)
	if err != nil || string(r.Content) != "verified report" {
		t.Fatal(r, err)
	}
	tools, err := localservices.EinoTools(ctx, f.services.Tools, f.call)
	if err != nil || len(tools) != 2 {
		t.Fatal(tools, err)
	}
	out, err := tools[0].(tool.InvokableTool).InvokableRun(ctx, `{"message_ids":["source"]}`)
	if err != nil || !strings.Contains(out, "write verified report") {
		t.Fatal(out, err)
	}
	args, _ := json.Marshal(f.artifact)
	out, err = tools[1].(tool.InvokableTool).InvokableRun(ctx, string(args))
	if err != nil || !strings.Contains(out, "verified report") {
		t.Fatal(out, err)
	}
	if _, err = tools[0].(tool.InvokableTool).InvokableRun(ctx, `{"message_ids":["source"],"injected":true}`); !errors.Is(err, host.ErrInvalid) {
		t.Fatal(err)
	}
}
func TestLocalRejectsCrossScopeTaskChangedFilesAndRevokedSources(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	foreign := f.call
	foreign.Scope.Scene = "other"
	if _, err := f.services.Files.ListFiles(ctx, foreign); !errors.Is(err, work.ErrScope) {
		t.Fatal(err)
	}
	foreign = f.call
	foreign.Binding = json.RawMessage(`{"trusted":"forged"}`)
	if _, err := f.services.Config.GetConfig(ctx, foreign); !errors.Is(err, work.ErrScope) {
		t.Fatal(err)
	}
	ref := f.artifact
	ref.TaskID = "foreign"
	if _, err := f.services.Resources.ReadResource(ctx, f.call, ref); !errors.Is(err, host.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := f.files.Write(ctx, f.artifact.Path, f.artifact.SHA256, "changed after execution"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.services.Resources.ReadResource(ctx, f.call, f.artifact); !errors.Is(err, host.ErrChanged) {
		t.Fatal(err)
	}
	if err := f.builder.Revoke(ctx, f.call.Scope, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.services.Config.GetConfig(ctx, f.call); !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatal(err)
	}
	if _, err := f.services.Tools.DiscoverTools(ctx, f.call); !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatal(err)
	}
}

type effectProvider struct{}

func (effectProvider) DiscoverTools(context.Context, host.Call) ([]host.Tool, error) {
	return []host.Tool{{ID: "write", Schema: json.RawMessage(`{"type":"object"}`), Effect: true}}, nil
}
func (effectProvider) CallTool(context.Context, host.Call, host.ToolCall) (host.ToolResult, error) {
	panic("effect must not be invoked")
}
func TestNativeReadAdapterRejectsEffectsWithoutHostCallJournal(t *testing.T) {
	if _, err := localservices.EinoTools(context.Background(), effectProvider{}, host.Call{}); !errors.Is(err, host.ErrUnsupported) {
		t.Fatal(err)
	}
}

type messageFunc func(context.Context, host.Call, host.MessageRequest) (host.Messages, error)

func (f messageFunc) GetMessages(ctx context.Context, c host.Call, q host.MessageRequest) (host.Messages, error) {
	return f(ctx, c, q)
}
func TestDefaultToolsUseOverriddenGroupAndCheckRevocationAfterRead(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	revoke := false
	p := messageFunc(func(ctx context.Context, c host.Call, q host.MessageRequest) (host.Messages, error) {
		if revoke {
			if err := f.builder.Revoke(ctx, c.Scope, c.SourceReceipt); err != nil {
				return host.Messages{}, err
			}
		}
		return host.Messages{Items: []contextbuilder.Source{{ID: "replacement", Text: "replacement provider"}}, Coverage: host.MessageCoverage{Source: "replacement", Complete: true, MissingIDs: []string{}}}, nil
	})
	s, err := localservices.NewLocal(host.Bindings{Authorize: func(_ context.Context, c host.Call) error {
		if c.Scope != f.call.Scope {
			return work.ErrScope
		}
		return nil
	}, Config: func(context.Context, host.Call) (host.ConfigSnapshot, error) {
		return host.ConfigSnapshot{ID: "profile", Version: "v1", Value: f.config}, nil
	}, Sources: f.builder, Tasks: f.tasks, Artifacts: f.files, Overrides: host.Services{Messages: p}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := localservices.EinoTools(ctx, s.Tools, f.call)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tools[0].(tool.InvokableTool).InvokableRun(ctx, `{"message_ids":["source"]}`)
	if err != nil || !strings.Contains(out, "replacement provider") {
		t.Fatal(out, err)
	}
	revoke = true
	if _, err = tools[0].(tool.InvokableTool).InvokableRun(ctx, `{"message_ids":["source"]}`); !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatal(err)
	}
}
