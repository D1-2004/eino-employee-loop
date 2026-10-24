package journal_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/D1-2004/eino-employee-loop/adapters/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/kernel/journal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type modelFunc func(context.Context, []*schema.Message) (*schema.Message, error)

func (f modelFunc) Generate(c context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return f(c, in)
}
func (f modelFunc) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return f, nil }
func (f modelFunc) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not streaming")
}
func open(t *testing.T, dir string) *sqlite.Store {
	t.Helper()
	db, e := sqlite.Open(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	return db
}
func bind(db *sqlite.Store, delegate modelFunc) *journal.Model {
	return &journal.Model{DB: db, Scope: "owner/scene/chat", Receipt: "input-1", Owner: "process-1", Name: "frozen-model", Limit: 2, Delegate: delegate}
}

func TestResponseSurvivesCancellationAndDatabaseReopen(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		cancel()
		return schema.AssistantMessage("frozen answer", nil), nil
	})
	in := []*schema.Message{schema.UserMessage("question")}
	if _, e := m.Generate(ctx, in); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	db = open(t, dir)
	m = bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		return nil, errors.New("must replay")
	})
	m.Owner = "process-2"
	// Eino correlation metadata changes across Runner reconstruction, not the request.
	in[0].Extra = map[string]any{"_eino_msg_id": "reconstructed"}
	got, e := m.Generate(context.Background(), in)
	if e != nil || got.Content != "frozen answer" || calls.Load() != 1 {
		t.Fatal(got, e, calls.Load())
	}
	in[0].Content = "different model round"
	if _, e = m.Generate(context.Background(), in); e == nil {
		t.Fatal("delegate failure missing")
	}
	in[0].Content = "third physical call"
	if _, e = m.Generate(context.Background(), in); !errors.Is(e, journal.ErrBudget) {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal("budget leaked model I/O", calls.Load())
	}
}
func TestPendingAttemptCannotDuplicateAndReceiptsHaveSeparateBudgets(t *testing.T) {
	db := open(t, t.TempDir())
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		close(started)
		<-release
		return schema.AssistantMessage("ok", nil), nil
	})
	in := []*schema.Message{schema.UserMessage("same")}
	done := make(chan error, 1)
	go func() { _, e := m.Generate(context.Background(), in); done <- e }()
	<-started
	if _, e := m.Generate(context.Background(), in); !errors.Is(e, journal.ErrInFlight) {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	// A separate receipt has independent admission and budget.
	m2 := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		return schema.AssistantMessage("other", nil), nil
	})
	m2.Receipt = "input-2"
	if _, e := m2.Generate(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
func TestCancelledOrRevokedAdmissionDoesNotReserveModelBudget(t *testing.T) {
	db := open(t, t.TempDir())
	var calls atomic.Int32
	m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		return schema.AssistantMessage("ok", nil), nil
	})
	m.Limit = 1
	denied := errors.New("revoked source")
	m.Before = func(context.Context) error { return denied }
	if _, e := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("a")}); !errors.Is(e, denied) {
		t.Fatal(e)
	}
	m.Before = nil
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := m.Generate(c, []*schema.Message{schema.UserMessage("a")}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("a")}); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestFingerprintIncludesActualCommonOptionsAndToolSchemas(t *testing.T) {
	db := open(t, t.TempDir())
	var calls atomic.Int32
	m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		return schema.AssistantMessage("ok", nil), nil
	})
	m.Limit = 6
	in := []*schema.Message{schema.UserMessage("same")}
	for _, option := range []model.Option{model.WithStop([]string{"first"}), model.WithStop([]string{"second"}), model.WithModel("other-model")} {
		if _, e := m.Generate(context.Background(), in, option); e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("different physical requests replayed an unrelated cached response", calls.Load())
	}
	a, e := m.WithTools([]*schema.ToolInfo{{Name: "one", Desc: "first", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.WithTools([]*schema.ToolInfo{{Name: "two", Desc: "second", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Generate(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Generate(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 5 {
		t.Fatal("different native tools shared a cached response", calls.Load())
	}
}
func TestKilledProcessReconcilesPendingModelReservationWithinBudget(t *testing.T) {
	if dir := os.Getenv("EEL_JOURNAL_CRASH_DIR"); dir != "" {
		db := open(t, dir)
		m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
			if e := os.WriteFile(filepath.Join(dir, "attempt-started"), []byte("reserved"), 0600); e != nil {
				t.Fatal(e)
			}
			os.Exit(23)
			return nil, nil
		})
		_, _ = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("original request")})
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestKilledProcessReconcilesPendingModelReservationWithinBudget$")
	cmd.Env = append(os.Environ(), "EEL_JOURNAL_CRASH_DIR="+dir)
	out, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 23 {
		t.Fatal(string(out), e)
	}
	if _, e = os.Stat(filepath.Join(dir, "attempt-started")); e != nil {
		t.Fatal(e)
	}
	db := open(t, dir)
	var calls atomic.Int32
	m := bind(db, func(context.Context, []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		return schema.AssistantMessage("recovered response", nil), nil
	})
	m.Owner = "new-leader"
	in := []*schema.Message{schema.UserMessage("original request")}
	got, e := m.Generate(context.Background(), in)
	if e != nil || got.Content != "recovered response" {
		t.Fatal(got, e)
	}
	if _, e = m.Generate(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("new request")}); !errors.Is(e, journal.ErrBudget) {
		t.Fatal("crashed physical attempt disappeared from budget", e)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
