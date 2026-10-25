package entry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	native "github.com/D1-2004/eino-employee-loop/employee/kernel/eino"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func ownerExtensionFixture(t *testing.T, childName string, steps ...looptest.Step) (Profile, *Registry, map[string]*looptest.Model, *looptest.Model) {
	t.Helper()
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	p.Plugins = []PluginRef{{Name: "owner-research", Version: "1"}}
	childModel := looptest.New(looptest.Text("Host-scoped research; original sources remain authoritative"))
	registry := NewRegistry()
	if err := registry.Register(p.Plugins[0], func(ctx context.Context, req native.Request) (native.Extension, error) {
		if req.Key.Lane != "owner_memory" {
			return native.Extension{}, nil
		}
		if req.Key.Workspace != p.Workspace || req.Key.Tenant != p.Tenant || req.Key.Employee != p.Employee || req.Key.Subject != p.Owner || req.Key.Scene != "dm" {
			return native.Extension{}, errors.New("child factory received another scope")
		}
		child, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: childName, Description: "Read-only Host-scoped research", Model: childModel, MaxIterations: 1})
		return native.Extension{ReadOnlyAgents: []adk.Agent{child}}, err
	}); err != nil {
		t.Fatal(err)
	}
	ms := pluginModels()
	ms["owner_memory"] = looptest.New(steps...)
	return p, registry, ms, childModel
}

func ownerCommit() looptest.Step {
	return call("commit_owner_update", "owner-commit", map[string]any{"body": "Owner prefers concise responses", "citations": []any{map[string]any{"source_id": "one", "quote": "concise"}}})
}

func TestOwnerAgentAsToolThenCommitReopensWithoutRepeating(t *testing.T) {
	ctx := context.Background()
	p, registry, ms, child := ownerExtensionFixture(t, "research", call("research", "research-one", map[string]any{"request": "Inspect the admitted concise preference"}), ownerCommit())
	dir := t.TempDir()
	opts := Options{Directory: dir, Profile: p, Models: models(ms), Plugins: registry}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	in := testInput("one", "Be concise")
	if _, err = e.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	before, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(before) != 1 || child.Calls() != 1 || ms["owner_memory"].Calls() != 2 {
		t.Fatal(before, err, child.Calls(), ms["owner_memory"].Calls())
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if admitted, err := e.Submit(ctx, in); err != nil || !admitted.Replay {
		t.Fatal(admitted, err)
	}
	if err = e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	after, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || !reflect.DeepEqual(before, after) || child.Calls() != 1 || ms["owner_memory"].Calls() != 2 {
		t.Fatal("reopen repeated child or commit", before, after, err, child.Calls(), ms["owner_memory"].Calls())
	}
}

func TestOwnerExtensionRejectsUnregisteredAndInvalidCallsBeforeInvocation(t *testing.T) {
	for _, scenario := range []string{"unknown", "invalid-arguments", "duplicate-id", "mixed-terminal", "invalid-citations", "collision"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			bad := call("research", "research-one", map[string]any{"request": "Inspect admitted evidence"})
			childName := "research"
			switch scenario {
			case "unknown":
				bad = call("unknown", "unknown-one", map[string]any{"request": "Inspect admitted evidence"})
			case "invalid-arguments":
				bad = call("research", "research-one", map[string]any{"request": 42})
			case "duplicate-id":
				bad = looptest.Tools(looptest.Call{ID: "same", Name: "research", Args: map[string]any{"request": "one"}}, looptest.Call{ID: "same", Name: "research", Args: map[string]any{"request": "two"}})
			case "mixed-terminal":
				bad.Message.ToolCalls = append(bad.Message.ToolCalls, ownerCommit().Message.ToolCalls...)
			case "invalid-citations":
				bad = call("commit_owner_update", "bad-commit", map[string]any{"body": "Invented preference", "citations": []any{map[string]any{"source_id": "one", "quote": "never admitted"}}})
			case "collision":
				childName = "commit_owner_update"
			}
			p, registry, ms, child := ownerExtensionFixture(t, childName, bad, ownerCommit())
			e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(ms), Plugins: registry})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if _, err = e.Submit(ctx, testInput("one", "Be concise")); err != nil {
				t.Fatal(err)
			}
			err = e.Drain(ctx, 30)
			if scenario == "collision" {
				if err == nil || !strings.Contains(err.Error(), "duplicate") || ms["owner_memory"].Calls() != 0 {
					t.Fatal("collision reached model", err, ms["owner_memory"].Calls())
				}
			} else if err != nil || ms["owner_memory"].Calls() != 2 {
				t.Fatal("invalid response was not corrected", err, ms["owner_memory"].Calls())
			}
			if child.Calls() != 0 {
				t.Fatal("rejected call invoked child", child.Calls())
			}
		})
	}
}

func TestOwnerAgentAsToolRechecksRevokedSource(t *testing.T) {
	ctx := context.Background()
	var e *Entry
	p, registry, ms, child := ownerExtensionFixture(t, "research", looptest.Func(func(_ []*schema.Message) looptest.Step {
		if err := e.backend.(*standalone).context.Revoke(ctx, testProfile().Scope("dm"), "one"); err != nil {
			return looptest.Fail(err)
		}
		return call("research", "research-one", map[string]any{"request": "This original source was just revoked"})
	}))
	var err error
	e, err = openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(ms), Plugins: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("one", "Be concise")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 30); err == nil {
		t.Fatal("revoked source did not stop Owner")
	}
	if child.Calls() != 0 {
		t.Fatal("child ran after source revocation", child.Calls())
	}
}
