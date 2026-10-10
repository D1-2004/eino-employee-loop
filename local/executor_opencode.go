package local

import (
	"context"
	"fmt"
	"strings"

	"github.com/dingtalk-fde/eino-employee-loop/core"
)

// OpenCodeExecutor runs a dispatched task inside a durable local OpenCode session
// (one session per Scene). Point the Session's WorkDir at a sandbox directory.
type OpenCodeExecutor struct {
	Session core.Session
	Scene   string
}

func (e OpenCodeExecutor) Execute(ctx context.Context, task *core.Task) (string, error) {
	sid, err := e.Session.Ensure(ctx, e.Scene, "employee-loop:"+e.Scene)
	if err != nil {
		return "", fmt.Errorf("opencode ensure: %w", err)
	}
	prompt := strings.TrimSpace(task.Prompt)
	if prompt == "" {
		prompt = task.Goal
	}
	out, err := e.Session.Run(ctx, sid, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

var _ core.JobExecutor = OpenCodeExecutor{}
