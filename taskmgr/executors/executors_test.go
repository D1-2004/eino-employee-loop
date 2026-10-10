package executors_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/taskmgr"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
)

type recReporter struct{ texts []string }

func (r *recReporter) Progress(_ context.Context, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

func TestEchoIsDeterministicAndLocalized(t *testing.T) {
	b := executors.Messages()
	job := taskmgr.Job{Goal: "weekly summary", Prompt: "summarize", Locale: "zh-Hans"}
	r := &recReporter{}
	got, err := executors.EchoExecutor{}.Execute(context.Background(), job, r)
	if err != nil {
		t.Fatal(err)
	}
	if want := b.Text("zh-CN", "taskmgr.echo.report", "goal", "weekly summary", "prompt", "summarize"); got != want {
		t.Fatalf("report %q, want %q", got, want)
	}
	if len(r.texts) != 1 || r.texts[0] != b.Text("zh-CN", "taskmgr.echo.progress", "goal", "weekly summary") {
		t.Fatalf("progress %v", r.texts)
	}
	if got == b.Text("en", "taskmgr.echo.report", "goal", "weekly summary", "prompt", "summarize") {
		t.Fatal("zh-CN must not fall back to en")
	}

	job.Locale, job.Corrections = "en", []string{"a", "b"}
	got, _ = executors.EchoExecutor{}.Execute(context.Background(), job, &recReporter{})
	sep := b.Text("en", "taskmgr.echo.separator")
	if want := b.Text("en", "taskmgr.echo.report_corrected", "goal", "weekly summary", "prompt", "summarize", "corrections", "a"+sep+"b"); got != want {
		t.Fatalf("corrected %q, want %q", got, want)
	}
	if strings.Contains(got, "{") {
		t.Fatalf("unresolved placeholder in %q", got)
	}
}

func TestEchoHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	_, err := executors.EchoExecutor{Delay: time.Minute}.Execute(ctx, taskmgr.Job{}, &recReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestFuncExecutor(t *testing.T) {
	var e taskmgr.Executor = executors.FuncExecutor(func(_ context.Context, job taskmgr.Job, _ taskmgr.Reporter) (string, error) {
		return job.Goal, nil
	})
	if got, err := e.Execute(context.Background(), taskmgr.Job{Goal: "g"}, &recReporter{}); err != nil || got != "g" {
		t.Fatalf("got %q err %v", got, err)
	}
}
