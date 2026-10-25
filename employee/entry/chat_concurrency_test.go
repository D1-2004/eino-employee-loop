package entry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func awaitSceneTest(t *testing.T, entered <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal(message)
	}
}

func sceneBlockedModel(entered chan struct{}, calls *atomic.Int32) model.ToolCallingChatModel {
	var once sync.Once
	return recoveryModel(func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
}

func closeSceneTest(t *testing.T, e *Entry) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel and settle scene workers")
	}
}

func TestStartRunsSeparateSceneWorkersAndPreservesSameSceneExclusion(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.Scenes = []string{"dm", "other"}
	entered := make(chan struct{})
	var calls atomic.Int32
	blocked := sceneBlockedModel(entered, &calls)
	other := looptest.New(call("reply", "other-reply", map[string]any{"source_ref": "other-source", "text": "other scene progressed"}))
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: func(_ context.Context, r ModelRequest) (model.ToolCallingChatModel, error) {
		if r.Role != "chat" {
			return nil, errors.New("unexpected model role")
		}
		if r.Scope.Scene == "dm" {
			return blocked, nil
		}
		return other, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Submit(ctx, testInput("blocked-source", "Block this model")); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	awaitSceneTest(t, entered, "first scene never entered native Generate")
	// Admission must not acquire the scene execution lock.
	admitted := make(chan error, 1)
	go func() {
		_, err := e.Submit(ctx, testInput("second-source", "Wait behind the first message"))
		admitted <- err
	}()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit waited behind scene model")
	}
	if progress, err := e.backend.(*standalone).chat.StepScene(ctx, "dm"); progress || err != nil {
		t.Fatal("same scene ran concurrently", progress, err)
	}
	in := testInput("other-source", "Answer in the other scene")
	in.Scene = "other"
	if _, err := e.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	progressed := false
	for time.Now().Before(deadline) {
		deliveries, err := e.Deliveries(ctx, "other")
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) == 1 && deliveries[0].State == "applied" {
			progressed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !progressed {
		t.Fatal("Start's blocked scene prevented other scene's native model/delivery")
	}
	if calls.Load() != 1 || other.Calls() != 1 {
		t.Fatal("same scene duplicated or other scene failed", calls.Load(), other.Calls())
	}
	closeSceneTest(t, e)
}

func TestResumeInAnotherSceneDoesNotWaitForBlockedModel(t *testing.T) {
	ctx := context.Background()
	p := testProfile()
	p.Scenes = []string{"dm", "other"}
	entered := make(chan struct{})
	var calls atomic.Int32
	blocked := sceneBlockedModel(entered, &calls)
	other := looptest.New(call("ask_input", "ask", map[string]any{"source_ref": "question-source", "prompt": "Which quarter?"}), call("reply", "answered", map[string]any{"source_ref": "question-source", "text": "Q3 accepted"}))
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: p, Models: func(_ context.Context, r ModelRequest) (model.ToolCallingChatModel, error) {
		if r.Scope.Scene == "dm" {
			return blocked, nil
		}
		return other, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	in := testInput("question-source", "Ask for a quarter")
	in.Scene = "other"
	admission, err := e.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if progressed, err := e.backend.(*standalone).chat.StepScene(ctx, "other"); !progressed || err != nil {
		t.Fatal(progressed, err)
	}
	pending, err := e.Pending(ctx, "other")
	if err != nil || len(pending[admission.ID]) != 1 {
		t.Fatal(pending, err)
	}
	if _, err := e.Submit(ctx, testInput("blocked-source", "Block the other scene")); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	awaitSceneTest(t, entered, "first scene did not block")
	answer := testInput("answer-source", "Q3")
	answer.Scene = "other"
	resumed := make(chan error, 1)
	go func() { resumed <- e.Resume(ctx, admission.ID, answer, pending[admission.ID][0]) }()
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resume waited for another scene's model")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		remaining, err := e.Pending(ctx, "other")
		if err != nil {
			t.Fatal(err)
		}
		if len(remaining) == 0 && other.Calls() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if other.Calls() != 2 || calls.Load() != 1 {
		t.Fatal("resumed checkpoint did not progress independently", other.Calls(), calls.Load())
	}
	closeSceneTest(t, e)
}
