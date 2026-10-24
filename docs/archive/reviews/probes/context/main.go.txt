package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/D1-2004/eino-employee-loop/generation"
	"github.com/cloudwego/eino/callbacks"
)

type key struct{}

func main() {
	graphStarts := 0
	preparedMarker := false
	modelMarker := false
	h := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
		graphStarts++
		return context.WithValue(ctx, key{}, true)
	}).Build()
	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{Name: "probe-caller"}, h)
	p := generation.Providers[int, string, string]{
		Context: generation.ContextFunc[int](func(c context.Context, _ int) (int, error) { preparedMarker, _ = c.Value(key{}).(bool); return 1, nil }),
		Model: generation.ModelFunc[int, string](func(c context.Context, _ int) (string, error) {
			modelMarker, _ = c.Value(key{}).(bool)
			return "ok", nil
		}),
		Decision: generation.DecisionFunc[int, string, string](func(context.Context, int, string) (string, error) { return "ok", nil }),
		Journal:  generation.JournalFunc[int, string, string](func(context.Context, generation.Attempt[int, string, string]) error { return nil }),
	}
	result, err := generation.Run(ctx, generation.Config[int, string, string]{MaxAttempts: 1}, p)
	raw, _ := json.Marshal(map[string]any{"accepted": result.Accepted, "graph_callback_starts": graphStarts, "prepare_received_node_context": preparedMarker, "model_received_node_context": modelMarker, "error": fmt.Sprint(err)})
	fmt.Println(string(raw))
}
