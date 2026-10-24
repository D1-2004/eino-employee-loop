package ownerloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	native "github.com/D1-2004/eino-employee-loop/agent"
	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/D1-2004/eino-employee-loop/ownerloop"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestCustomOwnerRoleFreezesPolicyAndRuntimeConfiguration(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "one", "本地处理")
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		t.Fatal("not yet started")
		return nil, nil
	})
	opts.Roles = []ownerloop.RoleConfig{{Name: "business", MaxModelCalls: 2}}
	opts.RolePolicies = map[string]string{"business": "FROZEN_BUSINESS_POLICY"}
	w, err := ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, f.scope, "one"); err != nil {
		t.Fatal(err)
	}
	opts.Version = "profile-v2"
	opts.Configuration = json.RawMessage("{\"revision\":\"two\"}")
	opts.RolePolicies = map[string]string{"business": "NEW_POLICY_MUST_NOT_APPEAR"}
	opts.Models = func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		if r.Version != "profile-v1" {
			t.Fatal(r)
		}
		return looptest.New(looptest.Func(func(messages []*schema.Message) looptest.Step {
			if !strings.Contains(messages[0].Content, "FROZEN_BUSINESS_POLICY") || strings.Contains(messages[0].Content, "NEW_POLICY_MUST_NOT_APPEAR") {
				t.Fatal("policy drift", messages[0].Content)
			}
			return output("business", "one", "本地处理")
		})), nil
	}
	configured, traced := false, false
	opts.ConfigureRuntime = func(_ context.Context, r ownerloop.Request, cfg native.Config) (native.Config, error) {
		if r.Version != "profile-v1" || string(r.Configuration) != "{\"revision\":\"one\"}" {
			t.Fatal("configuration drift", r)
		}
		configured = true
		cfg.Extensions = append(cfg.Extensions, func(_ context.Context, r native.Request) (native.Extension, error) {
			if r.Key.Lane != "owner_business" || r.Key.ProfileRevision != "profile-v1" {
				t.Fatal(r.Key)
			}
			return native.Extension{Callbacks: []callbacks.Handler{callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
				traced = true
				return ctx
			}).Build()}}, nil
		})
		return cfg, nil
	}
	w, err = ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w, f.scope)
	if !configured || !traced {
		t.Fatal("runtime configurator did not reach native runner", configured, traced)
	}
}

func multiscopeBuilder(t *testing.T, f *fixture, policy contextbuilder.AudiencePolicy) *contextbuilder.Builder {
	t.Helper()
	manager, err := work.New(f.db)
	if err != nil {
		t.Fatal(err)
	}
	b, err := contextbuilder.New(f.db, f.md, manager, func(_ context.Context, s work.Scope, _ contextbuilder.View) error {
		if contextbuilder.OwnerKey(s) != contextbuilder.OwnerKey(f.scope) {
			return work.ErrScope
		}
		return nil
	}, contextbuilder.WithAudiencePolicy(policy))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOwnerAudienceDenialPreservesPriorDocumentAndDoesNotBlockOtherJobs(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.builder = multiscopeBuilder(t, f, nil)
	f.admit(t, "private", "PRIVATE_OWNER_FACT")
	privateModel := looptest.New(output("memory", "private", "PRIVATE_OWNER_FACT"))
	groupModel := looptest.New(output("memory", "group", "public"))
	opts := f.opts(func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		if r.Scope.Scene == "group" {
			return groupModel, nil
		}
		return privateModel, nil
	})
	opts.Authorize = func(_ context.Context, s work.Scope) error {
		if contextbuilder.OwnerKey(s) != contextbuilder.OwnerKey(f.scope) {
			return work.ErrScope
		}
		return nil
	}
	opts.Roles = []ownerloop.RoleConfig{{Name: "memory", MaxModelCalls: 2}}
	w, err := ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, f.scope, "private"); err != nil {
		t.Fatal(err)
	}
	drain(t, w, f.scope)
	before, err := w.Documents(ctx, f.scope)
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	group := f.scope
	group.Scene = "group"
	if _, err = f.builder.Admit(ctx, group, contextbuilder.Source{ID: "group", Actor: group.Requester, Text: "public"}); err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, group, "group"); err != nil {
		t.Fatal(err)
	}
	drain(t, w, group)
	if groupModel.Calls() != 0 {
		t.Fatal("private source entered group model")
	}
	after, err := w.Documents(ctx, f.scope)
	if err != nil || len(after) != 1 || after[0].SHA256 != before[0].SHA256 {
		t.Fatal("prior document overwritten", after, err)
	}
	rows, err := f.db.List(ctx, "owner-jobs", contextbuilder.OwnerKey(f.scope))
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, row := range rows {
		var job struct{ State, Error string }
		if err = json.Unmarshal(row.Body, &job); err != nil {
			t.Fatal(err)
		}
		if job.State == "failed" && strings.Contains(job.Error, "prior document preserved") {
			failed = true
		}
	}
	if !failed {
		t.Fatal("missing durable diagnostic")
	}
	// A denied group observation must not prevent later same-scene maintenance.
	f.admit(t, "next", "PRIVATE_OWNER_FACT")
	opts.Models = func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		return looptest.New(output("memory", "next", "PRIVATE_OWNER_FACT")), nil
	}
	w, err = ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, f.scope, "next"); err != nil {
		t.Fatal(err)
	}
	drain(t, w, f.scope)
}

func TestFrozenOwnerSourceAudienceRecheckedAfterPolicyChange(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.builder = multiscopeBuilder(t, f, func(context.Context, work.Scope, work.Scope, contextbuilder.View) error { return nil })
	f.admit(t, "private", "PRIVATE_OWNER_FACT")
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		return looptest.New(output("memory", "private", "PRIVATE_OWNER_FACT")), nil
	})
	opts.Authorize = func(_ context.Context, s work.Scope) error {
		if contextbuilder.OwnerKey(s) != contextbuilder.OwnerKey(f.scope) {
			return work.ErrScope
		}
		return nil
	}
	opts.Roles = []ownerloop.RoleConfig{{Name: "memory", MaxModelCalls: 2}}
	w, err := ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, f.scope, "private"); err != nil {
		t.Fatal(err)
	}
	drain(t, w, f.scope)
	group := f.scope
	group.Scene = "group"
	if _, err = f.builder.Admit(ctx, group, contextbuilder.Source{ID: "group", Actor: group.Requester, Text: "public"}); err != nil {
		t.Fatal(err)
	}
	cancelled := looptest.New(looptest.Step{Err: context.Canceled})
	opts.Models = func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) { return cancelled, nil }
	w, err = ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Observe(ctx, group, "group"); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Tick(ctx, group); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	strict := multiscopeBuilder(t, f, nil)
	opts.Context = strict
	never := looptest.New(output("memory", "group", "public"))
	opts.Models = func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) { return never, nil }
	w, err = ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Tick(ctx, group); !errors.Is(err, contextbuilder.ErrAudience) {
		t.Fatal("frozen audience was not checked", err)
	}
	if never.Calls() != 0 {
		t.Fatal("frozen private evidence reached model after policy revocation")
	}
}
