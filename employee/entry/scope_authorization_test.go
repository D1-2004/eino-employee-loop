package entry

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	host "github.com/D1-2004/eino-employee-loop/employee/ports/services"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

func TestCurrentScopeRevocationFencesNativeEffectAndFrozenReplay(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("host: owner access withdrawn")
	var revoked atomic.Bool
	var checks atomic.Int32
	chatModel := looptest.New(looptest.Func(func(in []*schema.Message) looptest.Step {
		frame := frameOf(t, in)
		revoked.Store(true) // The provider completes after the Host withdrew access.
		return call("dispatch_task", "must-not-dispatch", map[string]any{"goal": frame.Current.Text, "instruction": frame.Current.Text, "source_ref": frame.Current.ID})
	}))
	executorModel := looptest.New()
	opts := Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(map[string]*looptest.Model{"chat": chatModel, "executor": executorModel}), AuthorizeScope: func(_ context.Context, scope contract.Scope) error {
		checks.Add(1)
		if revoked.Load() {
			return denied
		}
		return nil
	}}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if checks.Load() != 0 {
		t.Fatal("Open performed an authority preflight", checks.Load())
	}
	if _, err = e.Submit(ctx, testInput("accepted", "produce the actual report")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.RunOnce(ctx); !errors.Is(err, denied) {
		t.Fatal("revoked model output crossed the effect boundary", err)
	}
	s := e.backend.(*standalone)
	tasks, err := s.work.List(ctx, opts.Profile.Scope("dm"))
	if err != nil || len(tasks) != 0 || executorModel.Calls() != 0 {
		t.Fatal("revoked native call dispatched work", tasks, err, executorModel.Calls())
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	// A reopened Entry receives the current callback, not a frozen callback or
	// the permission that admitted the old chat frame.
	var reopenedChecks atomic.Int32
	opts.AuthorizeScope = func(context.Context, contract.Scope) error { reopenedChecks.Add(1); return denied }
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if reopenedChecks.Load() != 0 {
		t.Fatal("Open evaluated the replacement Host hook")
	}
	if _, err = e.RunOnce(ctx); !errors.Is(err, denied) || reopenedChecks.Load() == 0 {
		t.Fatal("frozen work bypassed current authority", err, reopenedChecks.Load())
	}
	if chatModel.Calls() != 1 || executorModel.Calls() != 0 {
		t.Fatal("revoked recovery entered a model or executor", chatModel.Calls(), executorModel.Calls())
	}
}

func TestScopeHookNarrowsStaticProfileAndSeparatesOwnersInSameScene(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("host: first owner withdrawn")
	var firstRevoked atomic.Bool
	for _, owner := range []string{"owner-one", "owner-two"} {
		t.Run(owner, func(t *testing.T) {
			p := testProfile()
			p.Owner = owner
			provider := &hostClientProbe{}
			var hookCalls atomic.Int32
			e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(nil), Services: provider.services(), AuthorizeScope: func(_ context.Context, scope contract.Scope) error {
				hookCalls.Add(1)
				if scope.Requester == "owner-one" && firstRevoked.Load() {
					return denied
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			services, err := e.HostServices(ctx)
			if err != nil {
				t.Fatal(err)
			}
			firstRevoked.Store(true)
			input := testInput("owner-source", "only this owner's source")
			input.Actor = owner
			_, err = e.Submit(ctx, input)
			if owner == "owner-one" && !errors.Is(err, denied) || owner == "owner-two" && err != nil {
				t.Fatal("one owner's revocation affected the wrong owner", err)
			}
			firstRevoked.Store(false)
			before := hookCalls.Load()
			for _, field := range []string{"workspace", "tenant", "employee", "owner", "scene"} {
				scope := p.Scope("dm")
				switch field {
				case "workspace":
					scope.Workspace = "foreign"
				case "tenant":
					scope.Tenant = "foreign"
				case "employee":
					scope.Employee = "foreign"
				case "owner":
					scope.Requester = "foreign"
				case "scene":
					scope.Scene = "foreign"
				}
				if _, err = services.Config.GetConfig(ctx, host.Call{Scope: scope}); !errors.Is(err, work.ErrScope) {
					t.Fatal("allowing hook expanded static "+field+" boundary", err)
				}
			}
			if hookCalls.Load() != before || provider.calls.Load() != 0 {
				t.Fatal("foreign scope reached the allowing hook or provider", hookCalls.Load(), before, provider.calls.Load())
			}
		})
	}
}

func TestExportedInjectedProvidersRecheckScopeAndPreserveCommittedReceipt(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("host: current scope revoked during provider call")
	for _, effect := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "committed-tool"}[effect], func(t *testing.T) {
			var revoked atomic.Bool
			provider := &hostClientProbe{toolReceipt: "actual-committed-receipt", wait: func(context.Context) { revoked.Store(true) }}
			e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Services: provider.services(), AuthorizeScope: func(context.Context, contract.Scope) error {
				if revoked.Load() {
					return denied
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			services, err := e.HostServices(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Only the remote provider interprets this binding. A scope check
			// must not invoke the local Host's blanket opaque-binding rejection.
			call := host.Call{Scope: testProfile().Scope("dm"), Binding: json.RawMessage(`{"remote_grant":"opaque"}`)}
			if effect {
				result, err := services.Tools.CallTool(ctx, call, host.ToolCall{ID: "commit", CallID: "durable-call", Arguments: json.RawMessage(`{}`)})
				if !errors.Is(err, denied) || result.Receipt != "actual-committed-receipt" || len(result.Content) != 0 {
					t.Fatal("withdrawal hid an actual commit or exposed revoked bytes", result, err)
				}
			} else {
				result, err := services.Resources.ReadResource(ctx, call, host.ResourceRef{Path: "private-resource"})
				if !errors.Is(err, denied) || len(result.Content) != 0 {
					t.Fatal("revoked resource bytes escaped provider wrapper", result, err)
				}
			}
			if provider.calls.Load() != 1 {
				t.Fatal("scope wrapper rejected provider-owned binding", provider.calls.Load())
			}
			if _, err = services.Config.GetConfig(ctx, call); !errors.Is(err, denied) || provider.calls.Load() != 1 {
				t.Fatal("already withdrawn scope reached injected provider", err, provider.calls.Load())
			}
		})
	}
}

func TestCompleteBackendCannotSilentlyIgnoreScopeHook(t *testing.T) {
	p := testProfile()
	p.Backend = "complete-remote-host"
	called := false
	e, err := openDefault(context.Background(), Options{Directory: t.TempDir(), Profile: p, AuthorizeScope: func(context.Context, contract.Scope) error { return nil }, Backends: map[string]func(context.Context, Options) (Backend, error){p.Backend: func(context.Context, Options) (Backend, error) {
		called = true
		return &partialOpenBackend{}, nil
	}}})
	if err == nil {
		e.Close()
		t.Fatal("complete backend ignored standalone scope authority option")
	}
	if called {
		t.Fatal("unsupported scope hook was rejected after backend factory side effects")
	}
}
