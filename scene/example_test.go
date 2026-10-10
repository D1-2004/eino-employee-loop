package scene_test

import (
	"context"
	"fmt"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
)

// A platform keeps its own Host, tools and context and lets the Hub own
// admission, sessions, fenced completion and the outbox. Hosts binds a Host
// to each claimed window; Context freezes the platform's data blocks.
func ExampleKernelRunner() {
	ctx := context.Background()
	st := memstore.New()
	reply := contract.ToolSpec{Name: "reply", Kind: contract.ToolTerminal, Terminal: contract.Reply,
		Description: "Reply and finish.", Lanes: []contract.Lane{contract.LaneForeground},
		Parameters: map[string]any{"type": "object", "required": []any{"reply"},
			"properties": map[string]any{"reply": map[string]any{"type": "string"}}}}
	platformHost := func(win contract.Window) contract.Host {
		return contract.HostFunc(func(_ context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
			text, _ := call.Arguments["reply"].(string)
			return contract.ToolResult{Content: "{}", Terminal: &contract.Decision{Kind: contract.Reply, Reply: text}}, nil
		})
	}
	hub := &scene.Hub{
		Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st,
		Runner: &scene.KernelRunner{
			Model:   kerneltest.New(kerneltest.Tools(kerneltest.Call{ID: "c1", Name: "reply", Args: map[string]any{"reply": "Hello Alice."}})),
			Hosts:   platformHost,
			Tools:   []contract.ToolSpec{reply},
			Journal: st,
			Persona: "You are the office assistant.",
			Context: func(context.Context, contract.Window) ([]contract.ContextBlock, error) {
				return []contract.ContextBlock{{Kind: contract.BlockMemory, Text: "Alice prefers short answers."}}, nil
			},
		},
	}
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ack, err := hub.Publish(ctx, contract.Event{
		ID: "m1", Source: "platform.im", Type: contract.TypeMessageAddressed, OccurredAt: now, Text: "hi",
		Scene: contract.SceneRef{ID: "scene-1", Kind: contract.SceneDirect},
		Actor: contract.Actor{ID: "alice", Kind: contract.ActorHuman, DisplayName: "Alice"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	// The addressed message waits for its coalescing window to close.
	if _, err := hub.Step(ctx, contract.ClaimRequest{Worker: "w1", Now: now.Add(time.Second)}); err != nil {
		fmt.Println(err)
		return
	}
	pending, _ := st.Pending(ctx, "e1", 10)
	fmt.Println(ack.Lane, "|", pending[0].Text, "|", pending[0].ReplyTo)
	// Output: foreground | Hello Alice. | m1
}
