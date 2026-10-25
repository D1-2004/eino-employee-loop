package kernel

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
)

type refusal struct{ cause error }

func (e *refusal) Error() string { return e.cause.Error() }
func (e *refusal) Unwrap() error { return e.cause }

// Recoverable marks a trusted module's proven no-effect business refusal. Do not
// wrap authorization, cancellation, I/O or unknown external-operation outcomes.
func Recoverable(err error) error {
	if err == nil {
		return nil
	}
	return &refusal{cause: err}
}
func isRefusal(err error) bool { var target *refusal; return errors.As(err, &target) }
func clearRefusedTerminal(ctx context.Context) error {
	return compose.ProcessState[*adk.State](ctx, func(_ context.Context, state *adk.State) error {
		state.ReturnDirectlyToolCallID = ""
		state.HasReturnDirectly = false
		return nil
	})
}

type toolBatch struct {
	mu      sync.Mutex
	refused bool
}

func (b *toolBatch) reset() { b.mu.Lock(); b.refused = false; b.mu.Unlock() }
func (b *toolBatch) wrap(f *Function) *Function {
	cp := *f
	cp.Invoke = func(ctx context.Context, id string, args map[string]any) (any, error) {
		b.mu.Lock()
		refused := b.refused
		b.mu.Unlock()
		if refused {
			return nil, Recoverable(errors.New("earlier tool in this batch was rejected; correct the request in a new model turn"))
		}
		out, err := f.Invoke(ctx, id, args)
		if isRefusal(err) {
			b.mu.Lock()
			b.refused = true
			b.mu.Unlock()
		}
		return out, err
	}
	return &cp
}
