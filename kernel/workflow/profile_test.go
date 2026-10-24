package workflow_test

import (
	"context"
	"errors"
	"testing"

	filestore "github.com/D1-2004/eino-employee-loop/adapters/sessionfile"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	"github.com/D1-2004/eino-employee-loop/kernel/workflow"
	"github.com/cloudwego/eino/schema"
)

func profileFixture() workflow.Profile {
	return workflow.Profile{Version: "workflow-profile/1", Name: "local", Revision: "r1", Agents: []workflow.AgentBinding{{Name: "one", Provider: "foreground", ProviderVersion: "candidate-1", Lane: "foreground", Session: "interactive"}, {Name: "two", Provider: "foreground", ProviderVersion: "candidate-1", Lane: "foreground", Session: "interactive"}}}
}

func TestProfileRevisionAndBindingKeepSessionHistorySeparate(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("alpha"), looptest.Text("beta"), looptest.Text("alpha continued"), looptest.Text("fresh revision"))}
	registry := &workflow.Registry{}
	if err := registry.Register("foreground", "candidate-1", p); err != nil {
		t.Fatal(err)
	}
	store := filestore.Store{Dir: t.TempDir()}
	gate := func(context.Context, workflow.Request) error { return nil }
	profile := profileFixture()
	a, err := workflow.NewAssembly(profile, registry, store, gate)
	if err != nil {
		t.Fatal(err)
	}
	key := input("unused", "unused").Key
	key.Subject = ""
	for _, call := range []struct{ agent, receipt, text string }{{"one", "a", "alpha"}, {"two", "b", "beta"}, {"one", "c", "continue"}} {
		if _, err = a.Run(context.Background(), call.agent, key, call.receipt, []*schema.Message{schema.UserMessage(call.text)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.histories[0]) != 0 || len(p.histories[1]) != 0 || len(p.histories[2]) != 2 {
		t.Fatal("bindings shared or lost history", p.histories)
	}
	profile.Revision = "r2"
	b, err := workflow.NewAssembly(profile, registry, store, gate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Run(context.Background(), "one", key, "d", []*schema.Message{schema.UserMessage("new revision")}, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.histories[3]) != 0 {
		t.Fatal("profile revision reused old history")
	}
	key.ProfileRevision = "r1"
	if _, err = b.Run(context.Background(), "one", key, "e", []*schema.Message{schema.UserMessage("old revision")}, nil); !errors.Is(err, workflow.ErrConflict) {
		t.Fatal(err)
	}
}

func TestProfileRejectsAmbiguousConfigurationAndUnknownPlugins(t *testing.T) {
	for _, raw := range []string{
		`{"version":"workflow-profile/1","name":"one","name":"two","revision":"r1","agents":[]}`,
		`{"version":"workflow-profile/1","name":"one","revision":"r1","agents":[],"exec":"shell"}`,
	} {
		if _, err := workflow.ParseProfile([]byte(raw)); err == nil {
			t.Fatal("ambiguous/unregistered configuration accepted", raw)
		}
	}
	profile := profileFixture()
	registry := &workflow.Registry{}
	if _, err := workflow.NewAssembly(profile, registry, filestore.Store{Dir: t.TempDir()}, func(context.Context, workflow.Request) error { return nil }); !errors.Is(err, workflow.ErrVersion) {
		t.Fatal(err)
	}
}

func TestProfileTaskSubjectComesOnlyFromHostAdmission(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("accepted"))}
	registry := &workflow.Registry{}
	if err := registry.Register("foreground", "candidate-1", p); err != nil {
		t.Fatal(err)
	}
	profile := profileFixture()
	profile.Agents = profile.Agents[:1]
	profile.Agents[0].Session = "task"
	a, err := workflow.NewAssembly(profile, registry, filestore.Store{Dir: t.TempDir()}, func(context.Context, workflow.Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	key := input("x", "x").Key
	key.Subject = ""
	if _, err = a.Run(context.Background(), "one", key, "r", []*schema.Message{schema.UserMessage("continue task-secret")}, nil); !errors.Is(err, workflow.ErrDenied) {
		t.Fatal(err)
	}
	if p.model.Calls() != 0 {
		t.Fatal("message text supplied session authority")
	}
	key.Subject = "host-admitted-task"
	if _, err = a.Run(context.Background(), "one", key, "r", []*schema.Message{schema.UserMessage("continue")}, nil); err != nil {
		t.Fatal(err)
	}
}
