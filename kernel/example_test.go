package kernel_test

import (
	"context"
	"fmt"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	"github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
)

// A platform embeds the kernel by injecting its own tools, Host and frozen
// context into one wake. The Host executes every call and owns receipts;
// the kernel orders, validates and budgets the model's decisions.
func ExampleRun() {
	object := func(required []any, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
	}
	str := map[string]any{"type": "string"}
	tools := []contract.ToolSpec{
		{Name: "create_ticket", Kind: contract.ToolEffect, Description: "Create a ticket in the platform.",
			Parameters: object([]any{"title"}, map[string]any{"title": str})},
		{Name: "reply", Kind: contract.ToolTerminal, Terminal: contract.Reply, Description: "Reply and finish.",
			Parameters: object([]any{"reply"}, map[string]any{"reply": str})},
	}
	host := contract.HostFunc(func(_ context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
		switch call.Name {
		case "create_ticket":
			// A real Host checks id.Tenant and the requester, commits the
			// effect idempotently by (id.WindowID, call.NativeID) and
			// returns the durable receipt.
			return contract.ToolResult{Content: `{"ticket":"T-1"}`, Receipt: "T-1"}, nil
		case "reply":
			text, _ := call.Arguments["reply"].(string)
			return contract.ToolResult{Content: `{"state":"accepted_for_delivery"}`,
				Terminal: &contract.Decision{Kind: contract.Reply, Reply: text}}, nil
		}
		return contract.ToolResult{}, contract.ErrToolRefused
	})
	// kerneltest scripts the model, so the example runs offline.
	model := kerneltest.New(
		kerneltest.Tools(kerneltest.Call{ID: "c1", Name: "create_ticket", Args: map[string]any{"title": "Printer jam"}}),
		kerneltest.Tools(kerneltest.Call{ID: "c2", Name: "reply", Args: map[string]any{"reply": "Ticket T-1 is open."}}),
	)
	res, err := kernel.Run(context.Background(),
		kernel.Options{Model: model, Host: host, Tools: tools, RequireTerminal: true},
		kernel.Wake{
			Identity:     contract.Identity{Tenant: "t1", Employee: "e1", WindowID: "w1"},
			Instructions: "You are the office assistant.",
			Blocks:       []contract.ContextBlock{{Kind: contract.BlockEvents, Text: "Alice: the printer is jammed"}},
		})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Decision.Kind, "|", res.Decision.Reply, "|", res.Receipts[0].ID, "|", res.Usage.Responses)
	// Output: reply | Ticket T-1 is open. | T-1 | 2
}
