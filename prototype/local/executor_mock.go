// Package tools provides injectable JobExecutor backends for the employee loop.
package local

import (
	"context"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/prototype/core"
)

// MockExecutor returns a deterministic demo result (no real side effects).
type MockExecutor struct {
	// Prefix is prepended to the result text. Default: the "executor.mock.prefix" pack text.
	Prefix string
	// Locale and Messages select the result text pack (nil Messages = core.Messages()).
	Locale   string
	Messages *i18n.Bundle
}

func (m MockExecutor) Execute(ctx context.Context, task *core.Task) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	msgs := m.Messages
	if msgs == nil {
		msgs = core.Messages()
	}
	prefix := m.Prefix
	if prefix == "" {
		prefix = msgs.Text(m.Locale, "executor.mock.prefix")
	}
	goal := task.Goal
	rs := []rune(goal)
	if len(rs) > 50 {
		goal = string(rs[:50]) + "…"
	}
	return msgs.Text(m.Locale, "executor.mock.result", "prefix", prefix, "goal", goal), nil
}

var _ core.JobExecutor = MockExecutor{}
