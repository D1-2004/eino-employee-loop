package owner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fixture struct {
	db      *sqlite.Store
	md      *markdown.Store
	builder *contextbuilder.Builder
	scope   work.Scope
	dir     string
}

func openFixture(t *testing.T, dir string) *fixture {
	t.Helper()
	ctx := context.Background()
	db, e := sqlite.Open(ctx, filepath.Join(dir, "state"))
	if e != nil {
		t.Fatal(e)
	}
	md, e := markdown.Open(filepath.Join(dir, "workspace"))
	if e != nil {
		t.Fatal(e)
	}
	manager, _ := work.New(db)
	scope := work.Scope{Workspace: "local", Tenant: "org", Employee: "employee", Scene: "scene", Requester: "owner"}
	builder, e := contextbuilder.New(db, md, manager, func(_ context.Context, s work.Scope, _ contextbuilder.View) error {
		if s.Workspace != scope.Workspace || s.Tenant != scope.Tenant || s.Employee != scope.Employee || s.Requester != scope.Requester {
			return work.ErrScope
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{db, md, builder, scope, dir}
	return f
}
func (f *fixture) close(t *testing.T) {
	t.Helper()
	if e := f.md.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Close(); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) admit(t *testing.T, id, text string) {
	t.Helper()
	_, e := f.builder.Admit(context.Background(), f.scope, contextbuilder.Source{ID: id, Actor: "owner", Text: text, At: time.Now().UTC()})
	if e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) opts(factory func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error)) ownerloop.Options {
	return ownerloop.Options{DB: f.db, Files: f.md, Context: f.builder, Owner: "lease-incarnation-one", Version: "profile-v1", Configuration: json.RawMessage(`{"revision":"one"}`), Models: factory, Authorize: func(_ context.Context, s work.Scope) error {
		if s != f.scope {
			return work.ErrScope
		}
		return nil
	}}
}
func output(role, id, quote string) looptest.Step {
	return looptest.Tools(looptest.Call{ID: "native-" + role, Name: "commit_owner_update", Args: map[string]any{"body": role + ": owner requested concise responses", "citations": []any{map[string]any{"source_id": id, "quote": quote}}}})
}
func drain(t *testing.T, w *ownerloop.Worker, scope work.Scope) {
	t.Helper()
	for i := 0; i < 40; i++ {
		progress, e := w.Tick(context.Background(), scope)
		if e != nil {
			t.Fatal(e)
		}
		if !progress {
			return
		}
	}
	t.Fatal("worker did not settle")
}

func TestAllSixNativeWorkersPersistEvidenceReadbackAndDoNotRerunAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := openFixture(t, dir)
	f.admit(t, "source-one", "请记住：回复简洁，并保留依据。")
	calls := map[string]*looptest.Model{}
	opts := f.opts(func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		if string(r.Configuration) != `{"revision":"one"}` {
			t.Fatal("configuration was not frozen", string(r.Configuration))
		}
		m := looptest.New(output(r.Role, "source-one", "回复简洁"))
		calls[r.Role] = m
		return m, nil
	})
	w, e := ownerloop.New(opts)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Observe(ctx, f.scope, "source-one"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	docs, e := w.Documents(ctx, f.scope)
	if e != nil || len(docs) != 6 {
		t.Fatal(len(docs), e)
	}
	for _, d := range docs {
		md, e := f.md.Read(ctx, d.Path)
		if e != nil || md.SHA256 != d.SHA256 || !strings.Contains(md.Body, "回复简洁") || len(d.Evidence) != 1 {
			t.Fatal(d, md, e)
		}
		if calls[d.Role].Calls() != 1 {
			t.Fatal("not an independent bounded role", d.Role, calls[d.Role].Calls())
		}
	}
	f.close(t)
	f = openFixture(t, dir)
	defer f.close(t)
	opts = f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		t.Fatal("settled observation called a model after restart")
		return nil, nil
	})
	opts.Owner = "lease-incarnation-two"
	w, e = ownerloop.New(opts)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Observe(ctx, f.scope, "source-one"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	if e = f.builder.Revoke(ctx, f.scope, "source-one"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	docs, e = w.Documents(ctx, f.scope)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range docs {
		md, e := f.md.Read(ctx, d.Path)
		if e != nil || !d.Invalidated || strings.Contains(md.Body, "回复简洁") {
			t.Fatal("withdrawn evidence remained usable", d, e)
		}
	}
}
func TestUnquotedFabricationExhaustsOnlyItsOwnBudgetWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "one", "原始请求只允许写本地文件")
	m := looptest.New(output("memory", "one", "不存在的同意"), output("memory", "one", "不存在的同意"), output("memory", "one", "不存在的同意"))
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) { return m, nil })
	opts.Roles = []ownerloop.RoleConfig{{Name: "memory", MaxModelCalls: 3}}
	w, _ := ownerloop.New(opts)
	if e := w.Observe(ctx, f.scope, "one"); e != nil {
		t.Fatal(e)
	}
	if _, e := w.Tick(ctx, f.scope); e == nil {
		t.Fatal("fabricated quote was committed")
	}
	if m.Calls() != 3 {
		t.Fatal("native repair budget not bounded", m.Calls())
	}
	docs, e := w.Documents(ctx, f.scope)
	if e != nil || len(docs) != 0 {
		t.Fatal("invalid proposal published", docs, e)
	}
	progress, e := w.Tick(ctx, f.scope)
	if e != nil || progress || m.Calls() != 3 {
		t.Fatal("failed loop spun in idle ticks", progress, e, m.Calls())
	}
}
func TestCommittedProjectionRepairsAfterConflictWithoutRepeatingModel(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "one", "请简洁回复")
	path := "owners/" + contextbuilder.OwnerKey(f.scope) + "/memory.md"
	original, e := f.md.Write(ctx, path, "", "original local context\n")
	if e != nil {
		t.Fatal(e)
	}
	var edit markdown.Document
	m := looptest.New(looptest.Func(func([]*schema.Message) looptest.Step {
		var e error
		edit, e = f.md.Write(ctx, path, original.SHA256, "human changed this file\n")
		if e != nil {
			t.Fatal(e)
		}
		return output("memory", "one", "简洁回复")
	}))
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) { return m, nil })
	opts.Roles = []ownerloop.RoleConfig{{"memory", 3}}
	w, _ := ownerloop.New(opts)
	if e = w.Observe(ctx, f.scope, "one"); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Tick(ctx, f.scope); !errors.Is(e, markdown.ErrConflict) {
		t.Fatal("overwrote a concurrent human edit", e)
	}
	docs, e := w.Documents(ctx, f.scope)
	if e != nil || len(docs) != 1 {
		t.Fatal("durable proposal lost", docs, e)
	}
	if _, e = f.md.Write(ctx, path, edit.SHA256, original.Body); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	actual, e := f.md.Read(ctx, path)
	if e != nil || actual.SHA256 != docs[0].SHA256 || m.Calls() != 1 {
		t.Fatal("repair repeated cognitive work", actual, m.Calls(), e)
	}
}
func TestEarlierFactsReloadOriginalSourcesOutsideTheTwentyMessageWindow(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "old-fact", "我偏好先讲结论。")
	round := 0
	opts := f.opts(func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		round++
		return looptest.New(looptest.Func(func(messages []*schema.Message) looptest.Step {
			if round == 1 {
				return output(r.Role, "old-fact", "先讲结论")
			}
			var input struct {
				Sources []struct {
					CitationID string
					Source     contextbuilder.Source
				}
			}
			if e := json.Unmarshal([]byte(messages[1].Content), &input); e != nil {
				t.Fatal(e)
			}
			oldID := ""
			for _, s := range input.Sources {
				if s.Source.ID == "old-fact" && s.Source.Text == "我偏好先讲结论。" {
					oldID = s.CitationID
				}
			}
			if oldID == "" {
				t.Fatal("earlier derived memory replaced original evidence")
			}
			return output(r.Role, oldID, "先讲结论")
		})), nil
	})
	opts.Roles = []ownerloop.RoleConfig{{"memory", 3}}
	w, _ := ownerloop.New(opts)
	if e := w.Observe(ctx, f.scope, "old-fact"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	for i := 0; i < 24; i++ {
		f.admit(t, fmt.Sprintf("new-%02d", i), fmt.Sprintf("新原始消息 %d", i))
	}
	if e := w.Observe(ctx, f.scope, "new-23"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	docs, e := w.Documents(ctx, f.scope)
	if e != nil || len(docs) != 1 || len(docs[0].Evidence) != 1 || docs[0].Evidence[0].Ref.ID != "old-fact" {
		t.Fatal(docs, e)
	}
}
func TestPendingObservationUsesFrozenConfigurationAcrossWorkerRecreation(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "one", "只在本地处理")
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		t.Fatal("not started")
		return nil, nil
	})
	opts.Roles = []ownerloop.RoleConfig{{"memory", 2}}
	w, _ := ownerloop.New(opts)
	if e := w.Observe(ctx, f.scope, "one"); e != nil {
		t.Fatal(e)
	}
	opts.Version = "profile-v2"
	opts.Configuration = json.RawMessage(`{"revision":"two"}`)
	opts.Models = func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		if r.Version != "profile-v1" || string(r.Configuration) != `{"revision":"one"}` {
			t.Fatal("pending work rebound current profile", r)
		}
		return looptest.New(output(r.Role, "one", "本地处理")), nil
	}
	w, _ = ownerloop.New(opts)
	drain(t, w, f.scope)
}

func TestNativeCallbacksAndOwnerResetInvalidatePublishedProjection(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "one", "我要求先说结论")
	var traces atomic.Int64
	opts := f.opts(func(context.Context, ownerloop.Request) (model.ToolCallingChatModel, error) {
		return looptest.New(output("memory", "one", "先说结论")), nil
	})
	opts.Roles = []ownerloop.RoleConfig{{"memory", 3}}
	opts.Callbacks = []callbacks.Handler{callbacks.NewHandlerBuilder().OnStartFn(func(c context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		traces.Add(1)
		return c
	}).Build()}
	w, _ := ownerloop.New(opts)
	if e := w.Observe(ctx, f.scope, "one"); e != nil {
		t.Fatal(e)
	}
	drain(t, w, f.scope)
	if traces.Load() == 0 {
		t.Fatal("owner workers bypassed native Eino callbacks")
	}
	if e := f.builder.ResetOwner(ctx, f.scope); e != nil {
		t.Fatal(e)
	}
	if _, e := w.Documents(ctx, f.scope); !errors.Is(e, contextbuilder.ErrRevoked) {
		t.Fatal("reset owner doc stayed authoritative", e)
	}
	drain(t, w, f.scope)
	docs, e := w.Documents(ctx, f.scope)
	if e != nil || len(docs) != 1 || !docs[0].Invalidated {
		t.Fatal(docs, e)
	}
	md, e := f.md.Read(ctx, docs[0].Path)
	if e != nil || strings.Contains(md.Body, "先说结论") {
		t.Fatal("reset evidence stayed in readable projection", md, e)
	}
}
