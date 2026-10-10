// Package tools provides injectable JobExecutor backends for the employee loop.
package tools

import (
	"context"
	"fmt"

	"github.com/dingtalk-fde/eino-employee-loop/core"
)

// MockExecutor returns a deterministic demo result (no real side effects).
type MockExecutor struct {
	// Prefix is prepended to the result text. Default: "[模拟执行器]".
	Prefix string
}

func (m MockExecutor) Execute(ctx context.Context, task *core.Task) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prefix := m.Prefix
	if prefix == "" {
		prefix = "[模拟执行器]"
	}
	goal := task.Goal
	rs := []rune(goal)
	if len(rs) > 50 {
		goal = string(rs[:50]) + "…"
	}
	return fmt.Sprintf("%s 已完成：%s（演示结果，非真实业务数据）", prefix, goal), nil
}

var _ core.JobExecutor = MockExecutor{}
