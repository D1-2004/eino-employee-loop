// Package kerneltest provides a scripted Eino chat model and an in-memory
// journal for tests of hosts and assemblies that embed the kernel.
package kerneltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Step is one scripted response: a message, an error, or a function of the
// request (Respond) for answers that depend on run-time data such as
// event ids.
type Step struct {
	Message *schema.Message
	Err     error
	Respond func(in []*schema.Message) Step
}

// Model replays steps in order and records every request.
type Model struct {
	mu       sync.Mutex
	steps    []Step
	Requests [][]*schema.Message
	Options  []*model.Options
	Tools    []*schema.ToolInfo
}

// New returns a model that answers with the given steps.
func New(steps ...Step) *Model { return &Model{steps: steps} }

// ErrExhausted is returned when the script has no more steps.
var ErrExhausted = errors.New("kerneltest: script exhausted")

// WithTools implements model.ToolCallingChatModel; the copy shares the script.
func (m *Model) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	m.Tools = tools
	m.mu.Unlock()
	return m, nil
}

// Generate implements model.BaseChatModel.
func (m *Model) Generate(_ context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Requests = append(m.Requests, append([]*schema.Message(nil), in...))
	m.Options = append(m.Options, model.GetCommonOptions(&model.Options{}, opts...))
	if len(m.steps) == 0 {
		return nil, ErrExhausted
	}
	s := m.steps[0]
	m.steps = m.steps[1:]
	if s.Respond != nil {
		s = s.Respond(in)
	}
	return s.Message, s.Err
}

// Func scripts a response computed from the request when it arrives.
func Func(f func(in []*schema.Message) Step) Step { return Step{Respond: f} }

// Stream implements model.BaseChatModel.
func (m *Model) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("kerneltest: streaming not scripted")
}

// Calls returns the number of Generate calls.
func (m *Model) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

// Text scripts a plain final answer.
func Text(content string) Step {
	return Step{Message: &schema.Message{Role: schema.Assistant, Content: content, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}
}

// Call is one scripted tool call.
type Call struct {
	ID   string
	Name string
	Args map[string]any
}

// Tools scripts a native tool call batch.
func Tools(calls ...Call) Step {
	msg := &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}
	for _, c := range calls {
		raw, err := json.Marshal(c.Args)
		if err != nil {
			panic(fmt.Sprintf("kerneltest: %v", err))
		}
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{ID: c.ID, Type: "function", Function: schema.FunctionCall{Name: c.Name, Arguments: string(raw)}})
	}
	return Step{Message: msg}
}

// Truncated scripts a response cut off by the token limit.
func Truncated(content string) Step {
	return Step{Message: &schema.Message{Role: schema.Assistant, Content: content, ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}}}
}

// Fail scripts a provider error.
func Fail(err error) Step { return Step{Err: err} }
