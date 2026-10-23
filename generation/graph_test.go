package generation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/D1-2004/eino-employee-loop/generation"
)

func TestGraphReservesRevalidatesAndJournalsBeforeEveryRetry(t *testing.T) {
	var events []string
	p := generation.Providers[int, string, string]{
		Context: generation.ContextFunc[int](func(_ context.Context, n int) (int, error) { events = append(events, "reserve"); return n, nil }),
		Model: generation.ModelFunc[int, string](func(_ context.Context, n int) (string, error) {
			events = append(events, "model")
			if n == 1 {
				return "", errors.New("offline")
			}
			if n == 2 {
				return "invalid", nil
			}
			return "valid", nil
		}),
		Decision: generation.DecisionFunc[int, string, string](func(_ context.Context, _ int, r string) (string, error) {
			events = append(events, "validate")
			if r != "valid" {
				return "", errors.New("invalid")
			}
			return r, nil
		}),
		Journal: generation.JournalFunc[int, string, string](func(_ context.Context, a generation.Attempt[int, string, string]) error {
			events = append(events, "save")
			if a.Number == 1 && a.ModelError == nil {
				t.Error("transport failure lost")
			}
			return nil
		}),
	}
	out, err := generation.Run(context.Background(), generation.Config[int, string, string]{MaxAttempts: 3, Retry: func(generation.Attempt[int, string, string]) bool { return true }}, p)
	if err != nil || !out.Accepted || out.Attempts != 3 || out.Last.Decision != "valid" {
		t.Fatal(out, err)
	}
	want := []string{"reserve", "model", "save", "reserve", "model", "validate", "save", "reserve", "model", "validate", "save"}
	if !reflect.DeepEqual(events, want) {
		t.Fatal("provider ordering changed", events)
	}
}

func TestGraphNeverRetriesAfterJournalFailure(t *testing.T) {
	calls := 0
	storage := errors.New("journal failed")
	p := generation.Providers[int, string, string]{Context: generation.ContextFunc[int](func(context.Context, int) (int, error) { return 1, nil }), Model: generation.ModelFunc[int, string](func(context.Context, int) (string, error) { calls++; return "output", nil }), Decision: generation.DecisionFunc[int, string, string](func(_ context.Context, _ int, s string) (string, error) { return s, nil }), Journal: generation.JournalFunc[int, string, string](func(context.Context, generation.Attempt[int, string, string]) error { return storage })}
	out, err := generation.Run(context.Background(), generation.Config[int, string, string]{MaxAttempts: 3, Retry: func(generation.Attempt[int, string, string]) bool { return true }}, p)
	if !errors.Is(err, storage) || out.Accepted || calls != 1 {
		t.Fatal(out, err, calls)
	}
}

func TestGraphCancellationSettlesPreparedAttemptWithoutAnotherModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	models, saves := 0, 0
	p := generation.Providers[int, string, string]{Context: generation.ContextFunc[int](func(context.Context, int) (int, error) { return 1, nil }), Model: generation.ModelFunc[int, string](func(context.Context, int) (string, error) { models++; cancel(); return "late response", nil }), Decision: generation.DecisionFunc[int, string, string](func(context.Context, int, string) (string, error) {
		t.Error("cancelled result was accepted")
		return "", nil
	}), Journal: generation.JournalFunc[int, string, string](func(_ context.Context, a generation.Attempt[int, string, string]) error {
		saves++
		if !errors.Is(a.ModelError, context.Canceled) {
			t.Error("cancelled attempt lost its cause")
		}
		return nil
	})}
	out, err := generation.Run(ctx, generation.Config[int, string, string]{MaxAttempts: 3, Retry: func(generation.Attempt[int, string, string]) bool { return true }}, p)
	if !errors.Is(err, context.Canceled) || out.Accepted || models != 1 || saves != 1 {
		t.Fatal(out, err, models, saves)
	}
}
