package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/cloudwego/eino/schema"
)

func TestResultProjectionRetainsCommittedEffectsWhenProjectionFails(t *testing.T) {
	projectionFailure := errors.New("cannot encode typed Host result")
	m := looptest.New(looptest.Tools(looptest.Call{ID: "work", Name: "dispatch", Args: map[string]any{}}))
	h := loop.HostFunc(func(context.Context, loop.Identity, loop.ToolCall) (loop.ToolResult, error) {
		return loop.ToolResult{Receipt: "task/committed", Terminal: &loop.Decision{Kind: loop.Dispatched}}, nil
	})
	cfg := loop.Config{Tools: []loop.Tool{{Name: "dispatch", Effect: true}}, ResultData: func(out loop.Outcome, err error) (json.RawMessage, error) {
		if err != nil || len(out.Receipts) != 1 {
			t.Error("projection did not receive the settled native result")
		}
		return json.RawMessage(`{"version":"test/1"}`), projectionFailure
	}}
	out, err := loop.New(cfg, m, h).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("dispatch")}})
	if !errors.Is(err, projectionFailure) || len(out.Receipts) != 1 || out.Receipts[0].ID != "task/committed" || len(out.HostData) == 0 {
		t.Fatal(out, err)
	}
}

func TestInvalidOrPanickingProjectionNeverBecomesSuccess(t *testing.T) {
	for _, scenario := range []string{"invalid-json", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := loop.Config{ResultData: func(loop.Outcome, error) (json.RawMessage, error) {
				if scenario == "panic" {
					panic("bad projection")
				}
				return json.RawMessage("not JSON"), nil
			}}
			out, err := loop.New(cfg, looptest.New(looptest.Text("answer")), nil).Run(context.Background(), loop.Wake{Messages: []*schema.Message{schema.UserMessage("hello")}})
			if err == nil || len(out.HostData) != 0 || out.Reply != "answer" {
				t.Fatal(out, err)
			}
		})
	}
}
