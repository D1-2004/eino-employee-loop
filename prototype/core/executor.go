package core

import "context"

// JobExecutor runs a background task after dispatch_task accepts it.
// Core only transitions Host registry state; the concrete work is injected.
type JobExecutor interface {
	Execute(ctx context.Context, task *Task) (result string, err error)
}
