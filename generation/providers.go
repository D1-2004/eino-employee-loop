// Package generation runs bounded structured-inference workflows on Eino.
// It does not grant authorization or apply the model's proposed actions.
package generation

import "context"

type ContextProvider[A any] interface {
	Prepare(context.Context, int) (A, error)
}
type ModelProvider[A, R any] interface {
	Generate(context.Context, A) (R, error)
}
type DecisionProvider[A, R, D any] interface {
	Validate(context.Context, A, R) (D, error)
}
type JournalProvider[A, R, D any] interface {
	Save(context.Context, Attempt[A, R, D]) error
}

type ContextFunc[A any] func(context.Context, int) (A, error)

func (f ContextFunc[A]) Prepare(c context.Context, n int) (A, error) { return f(c, n) }

type ModelFunc[A, R any] func(context.Context, A) (R, error)

func (f ModelFunc[A, R]) Generate(c context.Context, a A) (R, error) { return f(c, a) }

type DecisionFunc[A, R, D any] func(context.Context, A, R) (D, error)

func (f DecisionFunc[A, R, D]) Validate(c context.Context, a A, r R) (D, error) { return f(c, a, r) }

type JournalFunc[A, R, D any] func(context.Context, Attempt[A, R, D]) error

func (f JournalFunc[A, R, D]) Save(c context.Context, a Attempt[A, R, D]) error { return f(c, a) }

type Providers[A, R, D any] struct {
	Context  ContextProvider[A]
	Model    ModelProvider[A, R]
	Decision DecisionProvider[A, R, D]
	Journal  JournalProvider[A, R, D]
}

type Attempt[A, R, D any] struct {
	Number          int
	Input           A
	Response        R
	Decision        D
	PrepareError    error
	ModelError      error
	ValidationError error
}

// Error is the current stage error. Preparation and model errors prevent later
// stages from running, so a transport error is never relabeled invalid output.
func (a Attempt[A, R, D]) Error() error {
	if a.PrepareError != nil {
		return a.PrepareError
	}
	if a.ModelError != nil {
		return a.ModelError
	}
	return a.ValidationError
}

type Config[A, R, D any] struct {
	MaxAttempts int
	Retry       func(Attempt[A, R, D]) bool
	// Continue requests another round after successful validation/journaling.
	// The configured physical attempt ceiling still applies.
	Continue func(Attempt[A, R, D]) bool
}

// Result.Accepted certifies validation only, never a business effect or commit.
type Result[A, R, D any] struct {
	Accepted     bool
	LimitReached bool
	Attempts     int
	Last         Attempt[A, R, D]
}
