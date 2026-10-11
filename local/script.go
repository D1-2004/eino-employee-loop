package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
)

// Script is a scripted model loaded from a JSON file, for runs and tests
// without an LLM. The file is an array of responses, used in order, one per
// model request:
//
//	[
//	  {"tool_calls": [{"name": "reply", "arguments": {"text": "Hi!"}}]},
//	  {"text": "a plain answer"},
//	  {"error": "a provider error"}
//	]
//
// A request after the last response fails the wake with an error that says
// the script ran out; Permanent recognises it.
type Script struct {
	*lt.Model
	Path  string
	steps int
}

type scriptStep struct {
	Text      *string `json:"text"`
	Error     string  `json:"error"`
	ToolCalls []struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"tool_calls"`
}

// LoadScript reads a model script.
func LoadScript(path string) (*Script, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc []scriptStep
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("model script %s: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("model script %s: content after the response array", path)
	}
	steps := make([]lt.Step, 0, len(doc))
	for i, s := range doc {
		set := 0
		for _, ok := range []bool{s.Text != nil, s.Error != "", len(s.ToolCalls) > 0} {
			if ok {
				set++
			}
		}
		if set != 1 {
			return nil, fmt.Errorf("model script %s: response %d needs exactly one of text, tool_calls, error", path, i+1)
		}
		switch {
		case s.Text != nil:
			steps = append(steps, lt.Text(*s.Text))
		case s.Error != "":
			steps = append(steps, lt.Fail(errors.New(s.Error)))
		default:
			calls := make([]lt.Call, len(s.ToolCalls))
			for j, c := range s.ToolCalls {
				if c.Name == "" {
					return nil, fmt.Errorf("model script %s: response %d: a tool call needs a name", path, i+1)
				}
				if c.ID == "" {
					c.ID = fmt.Sprintf("call_%d_%d", i+1, j+1)
				}
				if c.Arguments == nil {
					c.Arguments = map[string]any{}
				}
				calls[j] = lt.Call{ID: c.ID, Name: c.Name, Args: c.Arguments}
			}
			steps = append(steps, lt.Tools(calls...))
		}
	}
	return &Script{Model: lt.New(steps...), Path: path, steps: len(steps)}, nil
}

// WithTools implements model.ToolCallingChatModel.
func (s *Script) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if _, err := s.Model.WithTools(tools); err != nil {
		return nil, err
	}
	return s, nil
}

// Generate answers with the next response of the script.
func (s *Script) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	msg, err := s.Model.Generate(ctx, in, opts...)
	if errors.Is(err, lt.ErrExhausted) {
		return nil, fmt.Errorf("model script %s ran out after %d responses: %w", s.Path, s.steps, err)
	}
	return msg, err
}

// Permanent reports a script that ran out, which asking again cannot fix
// (Employee.Permanent).
func (s *Script) Permanent(err error) bool {
	return errors.Is(err, lt.ErrExhausted)
}
