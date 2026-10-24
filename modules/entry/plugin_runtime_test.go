package entry

import (
	"context"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
	native "github.com/D1-2004/eino-employee-loop/kernel/eino"
	"github.com/cloudwego/eino/callbacks"
)

func pluginModels() map[string]*looptest.Model {
	return map[string]*looptest.Model{
		"chat": looptest.New(call("stay_quiet", "quiet", map[string]any{"source_ref": "one"})),
		"owner_memory": looptest.New(call("commit_owner_update", "commit", map[string]any{
			"body": "Owner prefers concise responses", "citations": []any{map[string]any{"source_id": "one", "quote": "concise"}},
		})),
	}
}

func registerCountingPlugin(t *testing.T, registry *Registry, version string, factories, callbacksSeen map[string]int) {
	t.Helper()
	if err := registry.Register(PluginRef{Name: "audit", Version: version}, func(_ context.Context, req native.Request) (native.Extension, error) {
		key := version + "/" + req.Key.Lane
		factories[key]++
		return native.Extension{Callbacks: []callbacks.Handler{callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			callbacksSeen[key]++
			return ctx
		}).Build()}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePluginsExecuteInChatAndOwnerNativeRuntimes(t *testing.T) {
	ctx := context.Background()
	profile := testProfile()
	profile.OwnerLoops = []string{"memory"}
	profile.Plugins = []PluginRef{{Name: "audit", Version: "v1"}}
	registry := NewRegistry()
	factories, traces := map[string]int{}, map[string]int{}
	registerCountingPlugin(t, registry, "v1", factories, traces)
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: profile, Models: models(pluginModels()), Plugins: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("one", "Be concise")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 30); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"chat", "owner_memory"} {
		key := "v1/" + role
		if factories[key] != 1 || traces[key] == 0 {
			t.Fatalf("%s: factories=%v callbacks=%v", role, factories, traces)
		}
	}
	docs, err := e.OwnerDocuments(ctx, "dm")
	if err != nil || len(docs) != 1 {
		t.Fatal(docs, err)
	}
}

func TestAdmittedOwnerUsesOldPluginVersionAfterEntryReopen(t *testing.T) {
	for _, keepOld := range []bool{true, false} {
		t.Run(map[bool]string{true: "registered", false: "removed"}[keepOld], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			profile := testProfile()
			profile.OwnerLoops = []string{"memory"}
			profile.Plugins = []PluginRef{{Name: "audit", Version: "v1"}}
			registry := NewRegistry()
			factories, traces := map[string]int{}, map[string]int{}
			registerCountingPlugin(t, registry, "v1", factories, traces)
			ms := pluginModels()
			e, err := openDefault(ctx, Options{Directory: dir, Profile: profile, Models: models(ms), Plugins: registry})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Submit(ctx, testInput("one", "Be concise")); err != nil {
				t.Fatal(err)
			}
			s := e.backend.(*standalone)
			if _, err = s.chat.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if err = s.ownerWorker.Observe(ctx, profile.Scope("dm"), "one"); err != nil {
				t.Fatal(err)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			profile.Revision = "v2"
			profile.Plugins = []PluginRef{{Name: "audit", Version: "v2"}}
			registry = NewRegistry()
			if keepOld {
				registerCountingPlugin(t, registry, "v1", factories, traces)
			}
			registerCountingPlugin(t, registry, "v2", factories, traces)
			e, err = openDefault(ctx, Options{Directory: dir, Profile: profile, Models: models(ms), Plugins: registry})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			err = e.Drain(ctx, 30)
			if !keepOld {
				if err == nil || !strings.Contains(err.Error(), "audit@v1 is not registered") {
					t.Fatal("missing old factory did not fail closed", err)
				}
				if ms["owner_memory"].Calls() != 0 {
					t.Fatal("model invoked without old factory")
				}
			} else if err != nil || factories["v1/owner_memory"] != 1 || traces["v1/owner_memory"] == 0 {
				t.Fatal(err, factories, traces)
			}
			if factories["v2/owner_memory"] != 0 {
				t.Fatal("old work drifted to current plugin", factories)
			}
		})
	}
}
