package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/D1-2004/eino-employee-loop/workflow"
	"github.com/D1-2004/eino-employee-loop/workflow/filestore"
	"github.com/cloudwego/eino/schema"
)

type provider struct {
	model             *looptest.Model
	deny              bool
	commitFailure     bool
	commits, prepares int
	histories         [][]*schema.Message
}

func (p *provider) Authorize(context.Context, workflow.Request) error {
	if p.deny {
		return errors.New("revoked")
	}
	return nil
}
func (p *provider) Prepare(_ context.Context, r workflow.Request, s workflow.Snapshot) (workflow.Prepared, error) {
	p.prepares++
	history := s.History()
	p.histories = append(p.histories, history)
	messages := append([]*schema.Message{schema.SystemMessage("Use the frozen context.")}, history...)
	messages = append(messages, r.Messages...)
	return workflow.Prepared{Model: p.model, Config: loop.Config{}, Messages: messages}, nil
}
func (p *provider) Commit(_ context.Context, r workflow.Request, _ loop.Outcome) (string, error) {
	p.commits++
	if p.commitFailure {
		return "", errors.New("unknown commit outcome")
	}
	return "local-business/" + r.Receipt, nil
}

func input(receipt, text string) workflow.Request {
	return workflow.Request{Key: workflow.Key{Workspace: "w", Tenant: "t", Employee: "e", Scene: "s", Lane: "foreground", Subject: "interactive"}, Receipt: receipt, Kind: "foreground", Version: "candidate-1", Messages: []*schema.Message{schema.UserMessage(text)}}
}
func runtime(t *testing.T, p *provider) *workflow.Runtime {
	t.Helper()
	registry := &workflow.Registry{}
	if err := registry.Register("foreground", "candidate-1", p); err != nil {
		t.Fatal(err)
	}
	return &workflow.Runtime{Registry: registry, Store: filestore.Store{Dir: t.TempDir()}, Gate: func(context.Context, workflow.Request) error { return nil }}
}

func TestTypedMultiTurnRestartReplayAndIsolation(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("remember alpha"), looptest.Text("alpha continued"), looptest.Text("other scene"))}
	r := runtime(t, p)
	first := input("a", "save alpha")
	if _, err := r.Run(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the assembly against the same disk; history is not process state.
	r = &workflow.Runtime{Registry: r.Registry, Store: r.Store, Gate: r.Gate}
	if _, err := r.Run(context.Background(), input("b", "continue")); err != nil {
		t.Fatal(err)
	}
	if len(p.histories[1]) != 2 || p.histories[1][0].Content != "save alpha" || p.histories[1][1].Content != "remember alpha" {
		t.Fatalf("history duplicated or lost: %+v", p.histories[1])
	}
	out, err := r.Run(context.Background(), first)
	if err != nil || !out.Replay || p.model.Calls() != 2 || p.commits != 2 {
		t.Fatal(out, err, p.model.Calls(), p.commits)
	}
	other := input("c", "what do you remember?")
	other.Key.Scene = "different"
	if _, err = r.Run(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if len(p.histories[2]) != 0 {
		t.Fatal("cross-scene history leak", p.histories[2])
	}
	other.Key.Tenant = "another"
	p.model = looptest.New(looptest.Text("another tenant"))
	if _, err = r.Run(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if len(p.histories[3]) != 0 {
		t.Fatal("cross-tenant history leak")
	}
}

func TestCommitRecoveryDoesNotRegenerateOrOvertake(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("validated answer")), commitFailure: true}
	r := runtime(t, p)
	request := input("a", "finish")
	out, err := r.Run(context.Background(), request)
	if err == nil || out.Phase != workflow.Generated {
		t.Fatal(out, err)
	}
	if _, err = r.Run(context.Background(), input("b", "next")); !errors.Is(err, workflow.ErrBusy) {
		t.Fatal("overtook unknown commit", err)
	}
	p.deny = true
	if _, err = r.Run(context.Background(), request); !errors.Is(err, workflow.ErrDenied) {
		t.Fatal("revoked commit resumed", err)
	}
	p.deny = false
	p.commitFailure = false
	out, err = r.Run(context.Background(), request)
	if err != nil || !out.Replay || out.Phase != workflow.Completed || p.model.Calls() != 1 || p.prepares != 1 {
		t.Fatal(out, err, p.model.Calls(), p.prepares)
	}
}

func TestScopeReceiptVersionAndGateFailClosed(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("answer"))}
	r := runtime(t, p)
	request := input("a", "first")
	if _, err := r.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"input", "version", "gate", "revoked"} {
		t.Run(tc, func(t *testing.T) {
			changed := input("a", "first")
			oldGate := r.Gate
			defer func() { r.Gate = oldGate; p.deny = false }()
			want := workflow.ErrConflict
			switch tc {
			case "input":
				changed.Messages[0].Content = "different"
			case "version":
				changed.Version = "unknown"
				want = workflow.ErrVersion
			case "gate":
				r.Gate = nil
				want = workflow.ErrDenied
			case "revoked":
				p.deny = true
				want = workflow.ErrDenied
			}
			if _, err := r.Run(context.Background(), changed); !errors.Is(err, want) {
				t.Fatal(err, want)
			}
		})
	}
	if p.model.Calls() != 1 || p.commits != 1 {
		t.Fatal("replayed side effects")
	}
}

func TestLeaseSerializesSessionsAndChecksRevision(t *testing.T) {
	store := filestore.Store{Dir: t.TempDir()}
	k := input("a", "a").Key
	l, err := store.Acquire(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err = store.Acquire(context.Background(), k); !errors.Is(err, workflow.ErrBusy) {
		t.Fatal(err)
	}
	s, err := l.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Key = k
	if err = l.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(context.Background(), s); !errors.Is(err, workflow.ErrConflict) {
		t.Fatal("stale save allowed", err)
	}
}

func TestForeignStoredRecordCannotBecomeAnotherScenesContext(t *testing.T) {
	p := &provider{model: looptest.New(looptest.Text("private"))}
	r := runtime(t, p)
	request := input("a", "first")
	if _, err := r.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	l, err := r.Store.Acquire(context.Background(), request.Key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := l.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Records[0].Request.Key.Scene = "foreign-scene"
	if err = l.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err = l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Run(context.Background(), input("b", "next")); !errors.Is(err, workflow.ErrConflict) {
		t.Fatal("foreign record reached Provider", err)
	}
	if p.prepares != 1 || p.model.Calls() != 1 {
		t.Fatal("private context was exposed")
	}
}

func TestCancellationCheckpointsPartialOutcomeAndNeverCommits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &provider{model: looptest.New(looptest.Func(func([]*schema.Message) looptest.Step { cancel(); return looptest.Text("late answer") }))}
	r := runtime(t, p)
	request := input("a", "cancel")
	out, err := r.Run(ctx, request)
	if !errors.Is(err, context.Canceled) || out.Phase != workflow.Failed || p.commits != 0 {
		t.Fatal(out, err, p.commits)
	}
	out, err = r.Run(context.Background(), request)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "canceled") || !out.Replay || p.model.Calls() != 1 {
		t.Fatal(out, err, p.model.Calls())
	}
	l, err := r.Store.Acquire(context.Background(), request.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	s, err := l.Load(context.Background())
	if err != nil || len(s.Records) != 1 || s.Records[0].Phase != workflow.Failed {
		t.Fatal(s, err)
	}
}
