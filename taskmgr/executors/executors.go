// Package executors provides small taskmgr.Executor implementations for
// tests, demos and adapters.
package executors

import (
	"context"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/taskmgr"
)

// FuncExecutor adapts a function to taskmgr.Executor.
type FuncExecutor func(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error)

// Execute implements taskmgr.Executor.
func (f FuncExecutor) Execute(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
	return f(ctx, job, r)
}

// EchoExecutor reports one progress step and returns a deterministic report
// built from the job's goal, prompt and corrections, localized through the
// executors pack for the job's locale.
type EchoExecutor struct {
	// Bundle resolves the messages; nil uses Messages().
	Bundle *i18n.Bundle
	// Delay waits between the progress step and the report; the wait ends
	// early with ctx.Err() when ctx is cancelled.
	Delay time.Duration
}

var _ taskmgr.Executor = EchoExecutor{}

// Execute implements taskmgr.Executor.
func (e EchoExecutor) Execute(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
	b := e.Bundle
	if b == nil {
		b = Messages()
	}
	if err := r.Progress(ctx, b.Text(job.Locale, "taskmgr.echo.progress", "goal", job.Goal)); err != nil {
		return "", err
	}
	if e.Delay > 0 {
		t := time.NewTimer(e.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	} else if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(job.Corrections) == 0 {
		return b.Text(job.Locale, "taskmgr.echo.report", "goal", job.Goal, "prompt", job.Prompt), nil
	}
	sep := b.Text(job.Locale, "taskmgr.echo.separator")
	return b.Text(job.Locale, "taskmgr.echo.report_corrected",
		"goal", job.Goal, "prompt", job.Prompt, "corrections", strings.Join(job.Corrections, sep)), nil
}
