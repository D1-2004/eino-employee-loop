package entry

import (
	"context"
	"encoding/json"
	"errors"
	localexec "github.com/D1-2004/eino-employee-loop/app/local/providers/execution"
	"sync/atomic"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recoveryModel func(context.Context, []*schema.Message) (*schema.Message, error)

func (f recoveryModel) Generate(c context.Context, m []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return f(c, m)
}
func (f recoveryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}
func (f recoveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not streamed")
}
func readyForReview(t *testing.T, s *standalone, id string) work.Task {
	t.Helper()
	ctx := context.Background()
	scope := s.opts.Profile.Scope("dm")
	source := work.Source{Receipt: id, Actor: scope.Requester, Text: "Verify local execution " + id}
	if _, e := s.context.Admit(ctx, scope, contextbuilder.Source{ID: id, Actor: source.Actor, Text: source.Text}); e != nil {
		t.Fatal(e)
	}
	receipt, e := s.work.Dispatch(ctx, scope, source, "dispatch", work.Definition{Configuration: encode(s.opts.Profile), Goal: source.Text, Instruction: source.Text, MaxRuns: 4})
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.work.Get(ctx, scope, receipt.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	run := task.CurrentRun()
	if _, e = s.work.BeginSubmission(ctx, scope, task.ID, run.ID); e != nil {
		t.Fatal(e)
	}
	task, e = s.work.Get(ctx, scope, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	frame, e := s.context.Build(ctx, scope, contextbuilder.Execution, "", task.ID)
	if e != nil {
		t.Fatal(e)
	}
	record, e := s.executor.Submit(ctx, execution.Request{Task: task, Frame: frame, Configuration: task.Definition.Configuration})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.work.Submission(ctx, scope, task.ID, run.ID, record.ID, false); e != nil {
		t.Fatal(e)
	}
	record, e = s.executor.(*localexec.Local).Advance(ctx, scope, run.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
		return execution.Result{Output: "Actual local invocation exited", Exited: true}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.work.RecordExecution(ctx, scope, task.ID, "fixture-exit", work.ExecutionFact{RunID: run.ID, ExecutionID: record.ID, State: work.RunSucceeded, Output: record.Result.Output, ExitProven: true}); e != nil {
		t.Fatal(e)
	}
	task, e = s.work.Get(ctx, scope, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func TestReviewHoldPersistsWithoutBlockingSecondTaskAndExplicitRetryPreservesBudget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var failedTask string
	var recovering atomic.Bool
	var calls atomic.Int32
	opts := Options{Directory: dir, Profile: testProfile(), Models: func(_ context.Context, r ModelRequest) (model.ToolCallingChatModel, error) {
		if r.Role != "review" {
			return nil, errors.New("unexpected role")
		}
		return recoveryModel(func(_ context.Context, in []*schema.Message) (*schema.Message, error) {
			calls.Add(1)
			frame := frameOf(t, in)
			if frame.Tasks[0].ID == failedTask && !recovering.Load() {
				return nil, errors.New("transient review provider failure")
			}
			step := call("decide_review", "review", map[string]any{"kind": "complete", "summary": "Verified local execution evidence"})
			return step.Message, nil
		}), nil
	}}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	readyForReview(t, s, "first-source")
	readyForReview(t, s, "second-source")
	tasks, err := s.work.List(ctx, testProfile().Scope("dm"))
	if err != nil {
		t.Fatal(err)
	}
	failedTask = tasks[0].ID
	if progress, err := s.tasksWorker.Step(ctx); !progress || err == nil {
		t.Fatal("first failure was not surfaced", progress, err)
	}
	if progress, err := s.tasksWorker.Step(ctx); !progress || err != nil {
		t.Fatal("held first task blocked unrelated review", progress, err)
	}
	second, err := s.work.Get(ctx, tasks[1].Scope, tasks[1].ID)
	if err != nil || second.State != work.Completed {
		t.Fatal(second, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s = e.backend.(*standalone)
	if progress, err := s.tasksWorker.Step(ctx); progress || err != nil || calls.Load() != 2 {
		t.Fatal("hold disappeared on restart", progress, err, calls.Load())
	}
	heldTask, err := s.work.Get(ctx, tasks[0].Scope, failedTask)
	if err != nil {
		t.Fatal(err)
	}
	scopeKey, _ := heldTask.Scope.Key()
	receipt := "review-" + contextbuilder.Digest(encode([]any{heldTask.ID, heldTask.Revision, heldTask.Goal.Revision, heldTask.CurrentRun().ID}))
	budgetScope := string(encode([]string{scopeKey + "/review", receipt}))
	before, err := s.db.Get(ctx, "model-budget", budgetScope, "physical")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, failedTask); err != nil {
		t.Fatal(err)
	}
	after, err := s.db.Get(ctx, "model-budget", budgetScope, "physical")
	if err != nil || string(after.Body) != string(before.Body) || after.Revision != before.Revision {
		t.Fatal("explicit retry reset physical budget", before, after, err)
	}
	recovering.Store(true)
	if progress, err := s.tasksWorker.Step(ctx); !progress || err != nil {
		t.Fatal(progress, err)
	}
	done, err := s.work.Get(ctx, heldTask.Scope, failedTask)
	if err != nil || done.State != work.Completed || calls.Load() != 3 {
		t.Fatal(done, err, calls.Load())
	}
}

type unknownDeliveryPort struct{ sends, lookups map[string]int }

func (p *unknownDeliveryPort) Send(_ context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends[in.ID]++
	if in.ID == "a-unknown" {
		return interaction.Receipt{}, interaction.ErrUnknown
	}
	return interaction.Receipt{ID: in.ID, ProviderMessageID: "verified/" + in.ID, BodySHA256: contextbuilder.Digest([]byte(in.Body))}, nil
}
func (p *unknownDeliveryPort) Lookup(_ context.Context, in interaction.Intent) (interaction.Lookup, error) {
	p.lookups[in.ID]++
	return interaction.Lookup{}, nil
}
func TestUnknownDeliveryDoesNotBlockReadyIntentInAnotherScene(t *testing.T) {
	ctx := context.Background()
	port := &unknownDeliveryPort{sends: map[string]int{}, lookups: map[string]int{}}
	profile := testProfile()
	profile.Scenes = []string{"dm", "other"}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: profile, Delivery: port, Models: models(map[string]*looptest.Model{})})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	for _, item := range []struct{ id, scene string }{{"a-unknown", "dm"}, {"z-ready", "other"}} {
		scope := profile.Scope(item.scene)
		if _, err = s.context.Admit(ctx, scope, contextbuilder.Source{ID: "source-" + item.id, Actor: profile.Owner, Text: "Original reply source"}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.delivery.Enqueue(ctx, interaction.Intent{ID: item.id, Scope: scope, SourceReceipt: "source-" + item.id, Kind: "reply", Body: "Actual reply"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.delivery.Deliver(ctx, profile.Scope("dm"), "a-unknown"); !errors.Is(err, interaction.ErrUnknown) {
		t.Fatal(err)
	}
	if progress, err := s.interaction.Step(ctx); !progress || err != nil {
		t.Fatal("unknown pinned a later scene's ready delivery", progress, err)
	}
	ready, err := s.delivery.List(ctx, profile.Scope("other"))
	if err != nil || len(ready) != 1 || ready[0].State != "applied" || port.sends["a-unknown"] != 1 || port.sends["z-ready"] != 1 {
		t.Fatal(ready, err, port)
	}
	if progress, err := s.interaction.Step(ctx); progress || !errors.Is(err, interaction.ErrUnknown) {
		t.Fatal("unresolved lookup obligation was silently lost", progress, err)
	}
	if port.sends["a-unknown"] != 1 || port.lookups["a-unknown"] != 2 {
		t.Fatal("unknown resubmitted", port)
	}
}
func TestExplicitRetryRequeuesFailedChatWithoutChangingPhysicalBudget(t *testing.T) {
	ctx := context.Background()
	m := looptest.New(looptest.Fail(errors.New("temporary model outage")), call("reply", "reply", map[string]any{"source_ref": "chat-source", "text": "Recovered answer"}))
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{"chat": m})
	defer e.Close()
	s := e.backend.(*standalone)
	a, err := e.Submit(ctx, testInput("chat-source", "Please answer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.chat.Step(ctx); err == nil {
		t.Fatal("failure missing")
	}
	d, err := s.db.Get(ctx, "entry-jobs", testProfile().Key(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var failed job
	if err = json.Unmarshal(d.Body, &failed); err != nil || failed.State != "failed" {
		t.Fatal(failed, err)
	}
	scope, _ := testProfile().Scope("dm").Key()
	budgetScope := string(encode([]string{scope + "/chat", "chat-source"}))
	before, err := s.db.Get(ctx, "model-budget", budgetScope, "physical")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.db.Get(ctx, "model-budget", budgetScope, "physical")
	if err != nil || string(after.Body) != string(before.Body) {
		t.Fatal("budget reset", before, after, err)
	}
	if progress, err := s.chat.Step(ctx); !progress || err != nil || m.Calls() != 2 {
		t.Fatal(progress, err, m.Calls())
	}
	if err = s.Retry(ctx, a.ID); !errors.Is(err, work.ErrState) {
		t.Fatal("replayed successful job", err)
	}
}
