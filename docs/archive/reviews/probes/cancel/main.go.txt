package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/D1-2004/eino-employee-loop/generation"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	modelCalls, journalCalls, persisted := 0, 0, 0
	p := generation.Providers[int, string, string]{
		Context: generation.ContextFunc[int](func(context.Context, int) (int, error) { return 1, nil }),
		Model: generation.ModelFunc[int, string](func(context.Context, int) (string, error) {
			modelCalls++
			cancel()
			return "real-response-to-settle", nil
		}),
		Decision: generation.DecisionFunc[int, string, string](func(context.Context, int, string) (string, error) { return "accepted", nil }),
		Journal: generation.JournalFunc[int, string, string](func(c context.Context, a generation.Attempt[int, string, string]) error {
			journalCalls++
			if err := c.Err(); err != nil {
				return err
			}
			persisted++
			return nil
		}),
	}
	result, err := generation.Run(ctx, generation.Config[int, string, string]{MaxAttempts: 1}, p)
	raw, _ := json.Marshal(map[string]any{"model_calls": modelCalls, "journal_invocations": journalCalls, "persisted_attempts": persisted, "accepted": result.Accepted, "response": result.Last.Response, "error": fmt.Sprint(err)})
	fmt.Println(string(raw))
}
