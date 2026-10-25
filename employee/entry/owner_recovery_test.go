package entry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/components/model"
)

type recoveryOwnerCounts struct{ observations, ticks, retries, completed atomic.Int32 }
type recoveryOwnerItem struct {
	Scope           work.Scope
	State, Revision string
}

// Each version deliberately owns a separate queue, as an external replacement
// may. Builtin Owner happens to scan all versions in one shared namespace.
type recoveryOwner struct {
	deps   OwnerDependencies
	counts *recoveryOwnerCounts
}

func (o *recoveryOwner) namespace() string {
	return "recovery-owner-" + o.deps.Profile.Modules.Owner.Version
}
func (o *recoveryOwner) Observe(ctx context.Context, scope work.Scope, receipt string) error {
	o.counts.observations.Add(1)
	if err := o.deps.Authorize(ctx, scope); err != nil {
		return err
	}
	if _, _, err := o.deps.Context.Source(ctx, scope, receipt); err != nil {
		return err
	}
	key, _ := scope.Key()
	return o.deps.State.Transact(ctx, func(tx storage.Tx) error {
		_, err := tx.Get(ctx, o.namespace(), key, "owner-"+receipt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		_, err = tx.Put(ctx, storage.Document{Namespace: o.namespace(), Scope: key, ID: "owner-" + receipt, Body: encode(recoveryOwnerItem{Scope: scope, State: "failed", Revision: o.deps.Profile.Revision})})
		return err
	})
}
func (o *recoveryOwner) Tick(ctx context.Context, scope work.Scope) (bool, error) {
	o.counts.ticks.Add(1)
	if err := o.deps.Authorize(ctx, scope); err != nil {
		return false, err
	}
	key, _ := scope.Key()
	rows, err := o.deps.State.List(ctx, o.namespace(), key)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var item recoveryOwnerItem
		if err = json.Unmarshal(row.Body, &item); err != nil {
			return false, err
		}
		if item.State != "queued" {
			continue
		}
		item.State = "done"
		row.Body = encode(item)
		if _, err = o.deps.State.Put(ctx, row); err != nil {
			return false, err
		}
		o.counts.completed.Add(1)
		return true, nil
	}
	return false, nil
}
func (o *recoveryOwner) Documents(context.Context, work.Scope) ([]ownerloop.Document, error) {
	return nil, nil
}
func (o *recoveryOwner) Failures(ctx context.Context, scope work.Scope) ([]Failure, error) {
	if err := o.deps.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	key, _ := scope.Key()
	rows, err := o.deps.State.List(ctx, o.namespace(), key)
	if err != nil {
		return nil, err
	}
	out := []Failure{}
	for _, row := range rows {
		var item recoveryOwnerItem
		if err = json.Unmarshal(row.Body, &item); err != nil {
			return nil, err
		}
		if item.Scope == scope && item.State == "failed" {
			out = append(out, Failure{Kind: "owner", ID: row.ID, State: item.State, Error: "provider " + item.Revision + " failed"})
		}
	}
	return out, nil
}
func (o *recoveryOwner) Retry(ctx context.Context, scope work.Scope, id string) (bool, error) {
	if err := o.deps.Authorize(ctx, scope); err != nil {
		return false, err
	}
	key, _ := scope.Key()
	found := false
	err := o.deps.State.Transact(ctx, func(tx storage.Tx) error {
		row, err := tx.Get(ctx, o.namespace(), key, id)
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var item recoveryOwnerItem
		if err = json.Unmarshal(row.Body, &item); err != nil {
			return err
		}
		if item.Scope != scope {
			return nil
		}
		found = true
		if item.State != "failed" {
			return work.ErrState
		}
		item.State = "queued"
		row.Body = encode(item)
		_, err = tx.Put(ctx, row)
		return err
	})
	if found && err == nil {
		o.counts.retries.Add(1)
	}
	return found, err
}
func recoveryOwnerRegistry(t *testing.T, versions ...string) (*ModuleRegistry, map[string]*recoveryOwnerCounts) {
	t.Helper()
	r := NewModuleRegistry()
	counts := map[string]*recoveryOwnerCounts{}
	for _, version := range versions {
		c := &recoveryOwnerCounts{}
		counts[version] = c
		if err := r.RegisterOwner(ModuleRef{Name: "recovery-owner", Version: version}, func(_ context.Context, d OwnerDependencies) (OwnerRuntime, error) {
			return &recoveryOwner{deps: d, counts: c}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return r, counts
}
func frozenOwnerFixture(t *testing.T) (Options, map[string]*recoveryOwnerCounts) {
	t.Helper()
	ctx := context.Background()
	r, counts := recoveryOwnerRegistry(t, "1", "2")
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	p.Modules.Owner = ModuleRef{Name: "recovery-owner", Version: "1"}
	opts := Options{Directory: t.TempDir(), Profile: p, Modules: r, Models: func(context.Context, ModelRequest) (model.ToolCallingChatModel, error) {
		t.Error("recovery diagnostic executed a model")
		return nil, errors.New("model must not run")
	}}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(ctx, testInput("old-source", "preserve the accepted original")); err != nil {
		t.Fatal(err)
	}
	if err = e.backend.(*standalone).ownerWorker.Observe(ctx, p.Scope("dm"), "old-source"); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Profile.Revision = "v2"
	opts.Profile.Modules.Owner = ModuleRef{Name: "recovery-owner", Version: "2"}
	return opts, counts
}
func TestHistoricalOwnerRecoveryImmediatelyAfterReopen(t *testing.T) {
	for _, first := range []string{"failures", "retry"} {
		t.Run(first, func(t *testing.T) {
			ctx := context.Background()
			opts, counts := frozenOwnerFixture(t)
			e, err := openDefault(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			s := e.backend.(*standalone)
			if first == "failures" {
				failures, err := e.Failures(ctx, "dm")
				if err != nil || len(failures) != 1 || failures[0].ID != "owner-old-source" || failures[0].Error != "provider v1 failed" {
					t.Fatalf("historical failure=%+v err=%v", failures, err)
				}
			}
			if err = e.Retry(ctx, "owner-old-source"); err != nil {
				t.Fatal("historical retry after reopen", err)
			}
			for version, c := range counts {
				want := int32(0)
				if version == "1" {
					want = 1
				}
				if c.observations.Load() != want || c.ticks.Load() != 0 || c.completed.Load() != 0 {
					t.Fatalf("diagnostics/retry executed work for version %s: observations=%d ticks=%d completed=%d", version, c.observations.Load(), c.ticks.Load(), c.completed.Load())
				}
			}
			if counts["1"].retries.Load() != 1 || counts["2"].retries.Load() != 0 {
				t.Fatal("retry selected a new provider")
			}
			for i := 0; i < 3; i++ {
				if _, err = s.ownerOnce(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if counts["1"].completed.Load() != 1 || counts["2"].completed.Load() != 0 {
				t.Fatal("retry did not execute exactly once in frozen provider")
			}
			failures, err := e.Failures(ctx, "dm")
			if err != nil || len(failures) != 0 {
				t.Fatal("recovered failure remains", failures, err)
			}
		})
	}
}
func TestHistoricalOwnerRecoveryRequiresOldFactory(t *testing.T) {
	ctx := context.Background()
	opts, _ := frozenOwnerFixture(t)
	opts.Modules, _ = recoveryOwnerRegistry(t, "2")
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Failures(ctx, "dm"); err == nil || !strings.Contains(err.Error(), "owner module recovery-owner@1 is not registered") {
		t.Fatal("missing historical diagnostic factory fell back", err)
	}
	if err = e.Retry(ctx, "owner-old-source"); err == nil || !strings.Contains(err.Error(), "owner module recovery-owner@1 is not registered") {
		t.Fatal("missing historical retry factory fell back", err)
	}
}
func TestHistoricalOwnerFailureAfterWorkerRestoration(t *testing.T) {
	ctx := context.Background()
	opts, _ := frozenOwnerFixture(t)
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	if _, err = s.ownerOnce(ctx); err != nil {
		t.Fatal(err)
	}
	failures, err := e.Failures(ctx, "dm")
	if err != nil || len(s.ownerWorkers) != 2 || len(failures) != 1 || failures[0].Error != "provider v1 failed" {
		t.Fatalf("restored old provider omitted: workers=%d failures=%+v err=%v", len(s.ownerWorkers), failures, err)
	}
}
func TestBuiltinOwnerFailureAggregationDoesNotDuplicateSharedRows(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.OwnerLoops = []string{"memory"}
	opts := Options{Directory: t.TempDir(), Profile: p, Models: func(context.Context, ModelRequest) (model.ToolCallingChatModel, error) {
		t.Error("diagnostics ran a builtin model")
		return nil, errors.New("unexpected model")
	}}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if _, err = e.Submit(ctx, testInput("builtin-source", "a preserved preference")); err != nil {
		t.Fatal(err)
	}
	if err = s.ownerWorker.Observe(ctx, p.Scope("dm"), "builtin-source"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.List(ctx, "owner-jobs", contextbuilder.OwnerKey(p.Scope("dm")))
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(rows[0].Body, &raw); err != nil {
		t.Fatal(err)
	}
	raw["State"] = encode("failed")
	raw["Error"] = encode("provider unavailable")
	row := rows[0]
	row.Body = encode(raw)
	if _, err = s.db.Put(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Profile.Revision = "v2"
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s = e.backend.(*standalone)
	failures, err := e.Failures(ctx, "dm")
	if err != nil || len(s.ownerWorkers) != 2 || len(failures) != 1 || failures[0].ID != row.ID {
		t.Fatalf("shared rows duplicated: workers=%d failures=%+v err=%v", len(s.ownerWorkers), failures, err)
	}
}

type fixedDiagnosticOwner struct {
	OwnerRuntime
	failures []Failure
}

func (o fixedDiagnosticOwner) Failures(context.Context, work.Scope) ([]Failure, error) {
	return o.failures, nil
}
func TestOwnerFailureDedupRetainsDistinctStateAndError(t *testing.T) {
	ctx := context.Background()
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	a := Failure{Kind: "owner", ID: "opaque-same-id", State: "failed", Error: "one"}
	b := a
	b.Error = "two"
	c := a
	c.State = "held"
	s.ownerWorkers["diagnostic-1"] = fixedDiagnosticOwner{OwnerRuntime: s.ownerWorker, failures: []Failure{a, b, c}}
	s.ownerWorkers["diagnostic-2"] = fixedDiagnosticOwner{OwnerRuntime: s.ownerWorker, failures: []Failure{a, b, c}}
	failures, err := e.Failures(ctx, "dm")
	if err != nil || len(failures) != 3 {
		t.Fatal("distinct failures were dropped", failures, err)
	}
}
func TestConcurrentHistoricalOwnerDiagnosticsRetryAndTick(t *testing.T) {
	ctx := context.Background()
	opts, counts := frozenOwnerFixture(t)
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	var wg sync.WaitGroup
	for _, op := range []func() error{
		func() error { _, err := e.Failures(ctx, "dm"); return err },
		func() error {
			err := e.Retry(ctx, "owner-old-source")
			if errors.Is(err, work.ErrState) {
				return nil
			}
			return err
		},
		func() error { _, err := s.ownerOnce(ctx); return err },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := op(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// Finish an explicitly queued retry even when all concurrent ticks ran first.
	if _, err = s.ownerOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if counts["1"].retries.Load() != 1 || counts["1"].completed.Load() != 1 || counts["2"].completed.Load() != 0 {
		t.Fatal("concurrent recovery duplicated or changed provider")
	}
}
