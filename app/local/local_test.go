package local

import (
	"context"
	"encoding/json"
	"errors"
	localservices "github.com/D1-2004/eino-employee-loop/adapters/services/local"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/entry"
	"github.com/D1-2004/eino-employee-loop/modules/interaction"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	host "github.com/D1-2004/eino-employee-loop/ports/services"
	"github.com/cloudwego/eino/components/model"
)

func native(name, id string, args map[string]any) looptest.Step {
	return looptest.Tools(looptest.Call{Name: name, ID: id, Args: args})
}

type observedMessages struct {
	calls  atomic.Int32
	source contextbuilder.Source
}

func (p *observedMessages) GetMessages(_ context.Context, c host.Call, q host.MessageRequest) (host.Messages, error) {
	p.calls.Add(1)
	if c.SourceReceipt != p.source.ID || len(q.IDs) != 1 || q.IDs[0] != p.source.ID {
		return host.Messages{}, host.ErrInvalid
	}
	return host.Messages{Items: []contextbuilder.Source{p.source}, Coverage: host.MessageCoverage{Source: "trusted-override", Complete: true, MissingIDs: []string{}}}, nil
}
func TestDefaultLocalAssemblyRunsNativeHostToolSixOwnersAndRealFileDelivery(t *testing.T) {
	ctx := context.Background()
	identity := Identity{Workspace: "ws", Tenant: "tenant", Employee: "employee", Owner: "owner", Scenes: []string{"dm"}}
	profile := DefaultProfile(identity, "scripted")
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	original := "Create the actual report; use only the accepted source."
	source := contextbuilder.Source{ID: "source", Actor: identity.Owner, Text: original}
	messages := &observedMessages{source: source}
	ms := map[string]*looptest.Model{
		"chat":       looptest.New(native("dispatch_task", "dispatch", map[string]any{"source_ref": "source", "goal": original, "instruction": original})),
		"executor":   looptest.New(native(localservices.ReadSourceTool, "read-admitted", map[string]any{"message_ids": []string{"source"}}), native("write_file", "write", map[string]any{"path": "report.md", "content": "# Verified local result"}), native("finish_run", "exit", map[string]any{"state": "succeeded", "summary": "Actual report written"})),
		"review":     looptest.New(native("decide_review", "review", map[string]any{"kind": "complete", "summary": "Verified report content and original source"})),
		"expression": looptest.New(native("publish_result", "expression", map[string]any{"text": "Actual report verified"})),
		"package":    looptest.New(native("publish_package", "package", map[string]any{"summary": "Actual report and immutable hash included"})),
	}
	for _, role := range profile.OwnerLoops {
		ms["owner_"+role] = looptest.New(native("commit_owner_update", "owner-"+role, map[string]any{"body": "Derived from the accepted original", "citations": []map[string]any{{"source_id": "source", "quote": original}}}))
	}
	models := func(_ context.Context, r entry.ModelRequest) (model.ToolCallingChatModel, error) {
		m := ms[r.Role]
		if m == nil {
			return nil, errors.New("unexpected role " + r.Role)
		}
		return m, nil
	}
	opts := Options{Directory: t.TempDir(), Profile: profile, Models: models, Services: host.Services{Messages: messages}}
	app, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	in := entry.Input{ID: source.ID, Actor: source.Actor, Scene: "dm", Text: source.Text, Origin: "explicit-local-input"}
	if _, err = app.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = app.Drain(ctx, 80); err != nil {
		t.Fatal(err)
	}
	tasks, err := app.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Completed || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks, err)
	}
	if messages.calls.Load() != 1 {
		t.Fatal("native executor did not use the overridden message provider", messages.calls.Load())
	}
	if len(tasks[0].Deliveries) != 1 || tasks[0].Deliveries[0].Stage != string(interaction.StageLocalApplied) || tasks[0].Deliveries[0].Transport != "local-file" {
		t.Fatal(tasks[0].Deliveries)
	}
	docs, err := app.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 6 {
		t.Fatal(docs, err)
	}
	c := host.Call{Scope: profile.Scope("dm"), SourceReceipt: source.ID, TaskID: tasks[0].ID, RunID: tasks[0].CurrentRun().ID}
	cfg, err := app.Services.Config.GetConfig(ctx, c)
	if err != nil || cfg.ID != profile.ID || cfg.Version != profile.Revision {
		t.Fatal(cfg, err)
	}
	var frozen entry.Profile
	if err = json.Unmarshal(cfg.Value, &frozen); err != nil || !frozen.HostTools {
		t.Fatal(frozen, err)
	}
	files, err := app.Services.Files.ListFiles(ctx, c)
	if err != nil || !files.Complete || len(files.Items) != 1 {
		t.Fatal(files, err)
	}
	resource, err := app.Services.Resources.ReadResource(ctx, c, files.Items[0])
	if err != nil || string(resource.Content) != "# Verified local result" {
		t.Fatal(resource, err)
	}

	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = app.Services.Config.GetConfig(ctx, c); err == nil {
		t.Fatal("default service survived Entry shutdown")
	}
	app, err = Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	admission, err := app.Submit(ctx, in)
	if err != nil || !admission.Replay {
		t.Fatal(admission, err)
	}
	if err = app.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	deliveries, err := app.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].Receipt.Stage != interaction.StageLocalApplied {
		t.Fatal(deliveries, err)
	}
	if messages.calls.Load() != 1 || ms["executor"].Calls() != 3 {
		t.Fatal("reopen repeated work", messages.calls.Load(), ms["executor"].Calls())
	}
}
func TestDefaultProfileDoesNotInventIdentity(t *testing.T) {
	p := DefaultProfile(Identity{}, "scripted")
	if p.Validate() == nil {
		t.Fatal("missing identity accepted")
	}
	_, err := Open(context.Background(), Options{Profile: entry.Profile{Backend: "service"}})
	if err == nil || !strings.Contains(err.Error(), "complete service backends") {
		t.Fatal(err)
	}
}
