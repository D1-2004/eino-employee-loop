package entry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/storage/memory"
	"github.com/D1-2004/eino-employee-loop/work"
)

// Exercise storage substitution through public Entry assembly. Memory is only
// asserted within this Entry lifetime; no restart durability is implied.
func TestStorageCompositionCompletesRealEntryPathAndReplaysInput(t *testing.T) {
	for _, adapter := range []string{"builtin", "memory"} {
		t.Run(adapter, func(t *testing.T) {
			ctx := context.Background()
			profile := testProfile()
			modules := NewModuleRegistry()
			var factories atomic.Int32
			if adapter == "memory" {
				profile.Modules.Storage = ModuleRef{Name: "memory", Version: "1"}
				if err := modules.RegisterStorage(profile.Modules.Storage, func(_ context.Context, d StorageDependencies) (storage.State, storage.Artifacts, error) {
					// A factory may retain or edit its configuration; this must not
					// alter admission authority in the owning Host.
					d.Profile.Scenes[0] = "factory-private-scene"
					factories.Add(1)
					return memory.New(), memory.NewArtifacts(), nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			ms := map[string]*looptest.Model{
				"chat": looptest.New(dispatchStep(t)),
				"executor": looptest.New(
					call("write_file", "write", map[string]any{"path": "result.md", "content": "# Verified storage adapter\nOriginal instruction retained."}),
					call("read_file", "read", map[string]any{"path": "result.md"}),
					call("finish_run", "finish", map[string]any{"state": "succeeded", "summary": "Wrote and read back result.md"}),
				),
				"review": looptest.New(reviewStep(t)),
			}
			e, err := Open(ctx, Options{Directory: t.TempDir(), Profile: profile, Modules: modules, Models: models(ms)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := e.Close(); err != nil {
					t.Error(err)
				}
			}()
			in := testInput("composition-input", "Write a verified Markdown report")
			accepted, err := e.Submit(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Drain(ctx, 40); err != nil {
				t.Fatal(err)
			}
			tasks, err := e.Tasks(ctx, "dm")
			if err != nil || len(tasks) != 1 {
				t.Fatal(tasks, err)
			}
			task := tasks[0]
			run := task.CurrentRun()
			if task.State != work.Completed || task.Goal.State != work.Completed || len(task.Runs) != 1 || run == nil || !run.ExitProven || run.State != work.RunSucceeded || len(run.Artifacts) != 1 {
				t.Fatal("Entry did not complete Task -> executor -> Review", task)
			}
			file, err := e.backend.(*standalone).files.Read(ctx, run.Artifacts[0].Path)
			if err != nil || !strings.Contains(file.Body, "Original instruction retained.") {
				t.Fatal("executor artifact missing from selected storage", file, err)
			}
			deliveries, err := e.Deliveries(ctx, "dm")
			if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" || deliveries[0].Receipt == nil {
				t.Fatal("delivery not applied", deliveries, err)
			}
			firstDelivery := deliveries[0]
			for range 2 {
				replay, err := e.Submit(ctx, in)
				if err != nil || !replay.Replay || replay.ID != accepted.ID {
					t.Fatal("input receipt replay changed", replay, err)
				}
				if err = e.Drain(ctx, 10); err != nil {
					t.Fatal(err)
				}
			}
			again, err := e.Tasks(ctx, "dm")
			if err != nil || len(again) != 1 || again[0].ID != task.ID || again[0].Revision != task.Revision || len(again[0].Runs) != 1 || again[0].CurrentRun().ID != run.ID {
				t.Fatal("input replay changed Task/Run", again, err)
			}
			deliveries, err = e.Deliveries(ctx, "dm")
			if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" || deliveries[0].Receipt == nil || !reflect.DeepEqual(deliveries[0].Receipt, firstDelivery.Receipt) {
				t.Fatal("input replay changed delivery", deliveries, err)
			}
			if ms["chat"].Calls() != 1 || ms["executor"].Calls() != 3 || ms["review"].Calls() != 1 {
				t.Fatal("input replay reran models", ms["chat"].Calls(), ms["executor"].Calls(), ms["review"].Calls())
			}
			if adapter == "memory" && factories.Load() != 1 {
				t.Fatal("storage factory not selected exactly once", factories.Load())
			}
		})
	}
}

type closeCountState struct {
	storage.State
	closes atomic.Int32
}

func (s *closeCountState) Close() error { s.closes.Add(1); return s.State.Close() }

type closeCountArtifacts struct {
	storage.Artifacts
	closes atomic.Int32
}

func (s *closeCountArtifacts) Close() error { s.closes.Add(1); return s.Artifacts.Close() }

func TestStorageFactoryFailureClosesEveryReturnedResource(t *testing.T) {
	factoryErr := errors.New("storage factory rejected configuration")
	for _, tc := range []struct {
		name         string
		state, files bool
		err          error
	}{
		{name: "error without resources", err: factoryErr},
		{name: "state plus error", state: true, err: factoryErr},
		{name: "artifacts plus error", files: true, err: factoryErr},
		{name: "both plus error", state: true, files: true, err: factoryErr},
		{name: "state without artifacts", state: true},
		{name: "artifacts without state", files: true},
		{name: "no resources"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &closeCountState{State: memory.New()}
			files := &closeCountArtifacts{Artifacts: memory.NewArtifacts()}
			t.Cleanup(func() {
				if !tc.state {
					state.State.Close()
				}
				if !tc.files {
					files.Artifacts.Close()
				}
			})
			profile := testProfile()
			profile.Modules.Storage = ModuleRef{Name: "partial", Version: "1"}
			registry := NewModuleRegistry()
			if err := registry.RegisterStorage(profile.Modules.Storage, func(context.Context, StorageDependencies) (storage.State, storage.Artifacts, error) {
				var db storage.State
				var artifacts storage.Artifacts
				if tc.state {
					db = state
				}
				if tc.files {
					artifacts = files
				}
				return db, artifacts, tc.err
			}); err != nil {
				t.Fatal(err)
			}
			e, err := Open(context.Background(), Options{Directory: t.TempDir(), Profile: profile, Modules: registry, Models: models(map[string]*looptest.Model{})})
			if err == nil || e != nil {
				if e != nil {
					e.Close()
				}
				t.Fatal("failed factory admitted Entry", e, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("factory error identity lost", err)
			}
			if tc.err == nil && !strings.Contains(err.Error(), "unavailable resources") {
				t.Fatal("partial resources not rejected explicitly", err)
			}
			wantState, wantFiles := int32(0), int32(0)
			if tc.state {
				wantState = 1
			}
			if tc.files {
				wantFiles = 1
			}
			if state.closes.Load() != wantState || files.closes.Load() != wantFiles {
				t.Fatal("factory cleanup ownership wrong", state.closes.Load(), files.closes.Load())
			}
		})
	}
}
