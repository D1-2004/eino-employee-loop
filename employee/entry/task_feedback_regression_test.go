package entry

import (
	"context"
	"encoding/json"
	chatmodule "github.com/D1-2004/eino-employee-loop/employee/chat"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
	"github.com/cloudwego/eino/schema"
)

func TestControlOptionalAckAndTerminalStatusUseExistingOutbox(t *testing.T) {
	for _, kind := range []string{"stop", "pause", "steer"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			chat := looptest.New(looptest.Func(func(in []*schema.Message) looptest.Step {
				f := frameOf(t, in)
				return call("dispatch_task", "dispatch", map[string]any{"goal": f.Current.Text, "instruction": f.Current.Text, "source_ref": f.Current.ID, "reply": "Accepted the original request"})
			}), looptest.Func(func(in []*schema.Message) looptest.Step {
				f := frameOf(t, in)
				return call(kind+"_task", "control", map[string]any{"task_id": f.Tasks[0].ID, "instruction": "Original correction text", "source_ref": f.Current.ID, "reply": "Acknowledged the current instruction"})
			}))
			e := openTest(t, t.TempDir(), map[string]*looptest.Model{"chat": chat})
			defer e.Close()
			s := e.backend.(*standalone)
			if _, err := e.Submit(ctx, testInput("initial", "Write a verified report")); err != nil {
				t.Fatal(err)
			}
			if progress, err := s.chat.Step(ctx); !progress || err != nil {
				t.Fatal(progress, err)
			}
			if _, err := e.Submit(ctx, testInput("correction", "Original correction text")); err != nil {
				t.Fatal(err)
			}
			if progress, err := s.chat.Step(ctx); !progress || err != nil {
				t.Fatal(progress, err)
			}
			tasks, err := e.Tasks(ctx, "dm")
			if err != nil || len(tasks) != 1 {
				t.Fatal(tasks, err)
			}
			task := tasks[0]
			if kind == "steer" {
				if len(task.Runs) != 2 || task.Definition.Instruction != "Original correction text" || !task.Runs[0].ExitProven {
					t.Fatal(task)
				}
			} else if kind == "stop" && task.State != work.Stopped || kind == "pause" && task.State != work.Paused {
				t.Fatal(task)
			}
			// Only the delivery worker executes. A control receipt alone must never claim delivery.
			before, err := e.Deliveries(ctx, "dm")
			if err != nil || len(before) != 2 {
				t.Fatal(before, err)
			}
			for _, r := range before {
				if r.State != "queued" || r.Receipt != nil {
					t.Fatal("ack invented transport evidence", r)
				}
			}
			for i := 0; i < 12; i++ {
				p, err := s.interaction.Step(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !p {
					break
				}
			}
			deliveries, err := e.Deliveries(ctx, "dm")
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if kind == "steer" {
				want = 2
			}
			if len(deliveries) != want {
				t.Fatal(deliveries)
			}
			status := 0
			for _, r := range deliveries {
				if r.State != "applied" || r.Receipt == nil {
					t.Fatal(r)
				}
				if r.Intent.Kind == "status" {
					status++
					if !strings.Contains(r.Intent.Body, string(task.State)) {
						t.Fatal(r)
					}
				}
			}
			if kind != "steer" && status != 1 {
				t.Fatal("missing terminal status", deliveries)
			}
			// Reconciliation and its delivery-fact task events cannot create status loops.
			for i := 0; i < 4; i++ {
				if _, err := s.interaction.Step(ctx); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := e.Deliveries(ctx, "dm")
			if len(after) != want {
				t.Fatal("duplicate delivery from replay", after)
			}
		})
	}
}

func TestFailedReviewCreatesVisibleFailureRatherThanSuccess(t *testing.T) {
	ctx := context.Background()
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{
		"chat":     looptest.New(dispatchStep(t)),
		"executor": looptest.New(call("finish_run", "failed", map[string]any{"state": "failed", "summary": "Source provider rejected the actual operation"})),
		"review":   looptest.New(call("decide_review", "fail", map[string]any{"kind": "fail", "summary": "Cannot finish: the provider rejected this operation"})),
	})
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("failed-source", "Run this operation")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Failed {
		t.Fatal(tasks, err)
	}
	records, err := e.Deliveries(ctx, "dm")
	if err != nil || len(records) != 1 || records[0].Intent.Kind != "status" || records[0].State != "applied" || !strings.Contains(records[0].Intent.Body, "failed") {
		t.Fatal(records, err)
	}
}

func TestIllegalReviewCompleteIsCorrectedInSameNativeInvocation(t *testing.T) {
	ctx := context.Background()
	reviewer := looptest.New(
		call("decide_review", "invalid-complete", map[string]any{"kind": "complete", "summary": "Incorrectly claiming success"}),
		looptest.Func(func(in []*schema.Message) looptest.Step {
			found := false
			for _, m := range in {
				if m.Role == schema.Tool && strings.Contains(m.Content, "incompatible task state") {
					found = true
				}
			}
			if !found {
				t.Error("review rejection was not returned to the model", in)
			}
			return call("decide_review", "corrected-fail", map[string]any{"kind": "fail", "summary": "Actual execution failed; success is not established"})
		}),
	)
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{
		"chat":     looptest.New(dispatchStep(t)),
		"executor": looptest.New(call("finish_run", "failed", map[string]any{"state": "failed", "summary": "Actual provider failure"})),
		"review":   reviewer,
	})
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("review-correction", "Perform the operation")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Failed || tasks[0].NeedsReview {
		t.Fatal(tasks, err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].Intent.Kind != "status" {
		t.Fatal(deliveries, err)
	}
}

func TestReviewContinueAtBudgetBecomesVisiblePause(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.MaxRuns = 1
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: models(map[string]*looptest.Model{
		"chat":     looptest.New(dispatchStep(t)),
		"executor": looptest.New(call("finish_run", "partial", map[string]any{"state": "succeeded", "summary": "Only first part completed"})),
		"review":   looptest.New(call("decide_review", "continue", map[string]any{"kind": "continue", "summary": "More work is needed", "next_instruction": "Finish the remaining original request"})),
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, testInput("budget-visible", "Finish both parts")); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Paused || tasks[0].Pending == nil || tasks[0].NeedsReview || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks, err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "applied" || !strings.Contains(deliveries[0].Intent.Body, "run budget exhausted") {
		t.Fatal(deliveries, err)
	}
}

func TestSteerWaitingRejectsToModelWithoutChangingTask(t *testing.T) {
	ctx := context.Background()
	chat := looptest.New(dispatchStep(t), looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		return call("steer_task", "invalid-waiting-steer", map[string]any{"task_id": f.Tasks[0].ID, "source_ref": f.Current.ID, "instruction": f.Current.Text, "reply": "This must not be sent as accepted"})
	}), looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		found := false
		for _, m := range in {
			if m.Role == schema.Tool && strings.Contains(m.Content, "incompatible task state") {
				found = true
			}
		}
		if !found {
			t.Error("control rejection was not returned to the model")
		}
		return call("reply", "explain", map[string]any{"source_ref": f.Current.ID, "text": "The existing task is waiting for an answer; this correction was not applied."})
	}))
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{
		"chat":     chat,
		"executor": looptest.New(call("ask_input", "quarter", map[string]any{"prompt": "Which quarter?"})),
	})
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("waiting-task", "Write a report for the quarter")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	before, err := e.Tasks(ctx, "dm")
	if err != nil || len(before) != 1 || before[0].State != work.Waiting {
		t.Fatal(before, err)
	}
	if _, err := e.Submit(ctx, testInput("waiting-steer", "Change the instruction without answering")); err != nil {
		t.Fatal(err)
	}
	if err := e.Drain(ctx, 40); err != nil {
		t.Fatal(err)
	}
	after, err := e.Tasks(ctx, "dm")
	if err != nil || len(after) != 1 || after[0].State != work.Waiting || after[0].Goal.Revision != before[0].Goal.Revision || after[0].Definition.Instruction != before[0].Definition.Instruction || after[0].Pending != nil {
		t.Fatal(after, err)
	}
	deliveries, err := e.Deliveries(ctx, "dm")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range deliveries {
		if strings.Contains(record.Intent.Body, "must not be sent") {
			t.Fatal("rejected control acknowledged as accepted", record)
		}
		if record.Intent.Kind == "reply" && record.State == "applied" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing visible rejection explanation", deliveries)
	}
}

func TestOverBudgetChatIsPreservedAndDoesNotPoisonNextAdmission(t *testing.T) {
	ctx := context.Background()
	model := looptest.New(looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		if f.Current.ID != "small-followup" {
			t.Error("oversize request reached the model", f.Current.ID)
		}
		return call("reply", "small-reply", map[string]any{"source_ref": f.Current.ID, "text": "The following request can proceed"})
	}))
	e := openTest(t, t.TempDir(), map[string]*looptest.Model{"chat": model})
	defer e.Close()
	original := strings.Repeat("preserved original human text ", 6000)
	if _, err := e.Submit(ctx, testInput("oversize-original", original)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(ctx, testInput("small-followup", "A small followup")); err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	if progressed, err := s.chat.Step(ctx); !progressed || err == nil {
		t.Fatal("expected explicit oversized-input failure", progressed, err)
	}
	if progressed, err := s.chat.Step(ctx); !progressed || err != nil {
		t.Fatal("failed head blocked next request", progressed, err)
	}
	for i := 0; i < 8; i++ {
		p, err := s.interaction.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !p {
			break
		}
	}
	docs, err := s.db.List(ctx, "entry-jobs", testProfile().Key())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, doc := range docs {
		var j chatmodule.Job
		if err = json.Unmarshal(doc.Body, &j); err != nil {
			t.Fatal(err)
		}
		if j.Input.ID == "oversize-original" {
			found = true
			if j.State != "failed" || j.Error == "" || j.Input.Text != original {
				t.Fatal("failed original was changed", j.State, j.Error, len(j.Input.Text))
			}
		}
	}
	if !found {
		t.Fatal("missing failed job")
	}
	records, err := e.Deliveries(ctx, "dm")
	if err != nil || len(records) != 2 {
		t.Fatal(records, err)
	}
	for _, record := range records {
		if record.State != "applied" || record.Receipt == nil {
			t.Fatal(record)
		}
	}
	if model.Calls() != 1 {
		t.Fatal("mandatory overbudget input consumed physical model budget", model.Calls())
	}
}

func TestDispatchCommittedBeforeAckFailureDoesNotRetryWithNewCallID(t *testing.T) {
	for _, cause := range []error{storage.ErrNotFound, work.ErrState, work.ErrBudget, work.ErrStale} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := context.Background()
			dispatch := func(id string) looptest.Step {
				return looptest.Func(func(in []*schema.Message) looptest.Step {
					f := frameOf(t, in)
					return call("dispatch_task", id, map[string]any{"source_ref": f.Current.ID, "goal": f.Current.Text, "instruction": f.Current.Text, "reply": "Accepted the request"})
				})
			}
			model := looptest.New(dispatch("first-command"), dispatch("must-not-be-retried"))
			resolutions := 0
			e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(map[string]*looptest.Model{"chat": model}), ResolveDelivery: func(context.Context, interaction.Intent) (interaction.Intent, error) {
				resolutions++
				return interaction.Intent{}, cause
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if _, err = e.Submit(ctx, testInput("ack-resolution-failure", "Write a report")); err != nil {
				t.Fatal(err)
			}
			if progressed, err := e.backend.(*standalone).chat.Step(ctx); !progressed || err == nil {
				t.Fatal("ACK failure must hard-fail after command acceptance", progressed, err)
			}
			tasks, err := e.Tasks(ctx, "dm")
			if err != nil || len(tasks) != 1 || len(tasks[0].Runs) != 1 {
				t.Fatal("failed ACK caused duplicate Task/Run", tasks, err)
			}
			if model.Calls() != 1 || resolutions != 1 {
				t.Fatal("post-commit failure reached model correction", model.Calls(), resolutions)
			}
			records, err := e.Deliveries(ctx, "dm")
			if err != nil || len(records) != 0 {
				t.Fatal("unresolved ACK fabricated delivery", records, err)
			}
		})
	}
}

func TestControlCommittedBeforeAckFailureDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	model := looptest.New(dispatchStep(t), looptest.Func(func(in []*schema.Message) looptest.Step {
		f := frameOf(t, in)
		return call("pause_task", "accepted-pause", map[string]any{"source_ref": f.Current.ID, "task_id": f.Tasks[0].ID, "reply": "Paused"})
	}), looptest.Func(func(in []*schema.Message) looptest.Step {
		t.Error("post-control ACK failure reached model correction")
		return call("stay_quiet", "unexpected", map[string]any{"source_ref": "control-pause"})
	}))
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(map[string]*looptest.Model{"chat": model}), ResolveDelivery: func(context.Context, interaction.Intent) (interaction.Intent, error) {
		return interaction.Intent{}, storage.ErrNotFound
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	if _, err = e.Submit(ctx, testInput("control-start", "Write a report")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.chat.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(ctx, testInput("control-pause", "Pause this task")); err != nil {
		t.Fatal(err)
	}
	if progressed, err := s.chat.Step(ctx); !progressed || err == nil {
		t.Fatal(progressed, err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Paused || len(tasks[0].Runs) != 1 {
		t.Fatal(tasks, err)
	}
	if model.Calls() != 2 {
		t.Fatal("control repeated after its commit", model.Calls())
	}
}
