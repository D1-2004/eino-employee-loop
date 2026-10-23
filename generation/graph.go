package generation

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/compose"
)

var ErrAttemptsExhausted = errors.New("generation: attempt budget exhausted")

type state[A, R, D any] struct {
	ctx       context.Context
	config    Config[A, R, D]
	providers Providers[A, R, D]
	result    Result[A, R, D]
	err       error
}

// Run drives all retries through Eino. Providers own durable state and their
// physical reservation ceiling, which must not reset across Run invocations.
func Run[A, R, D any](ctx context.Context, cfg Config[A, R, D], p Providers[A, R, D]) (Result[A, R, D], error) {
	var empty Result[A, R, D]
	if cfg.MaxAttempts <= 0 || cfg.MaxAttempts > 1024 {
		return empty, errors.New("generation: invalid attempt ceiling")
	}
	if p.Context == nil || p.Model == nil || p.Decision == nil || p.Journal == nil {
		return empty, errors.New("generation: all four Providers are required")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	g := compose.NewGraph[*state[A, R, D], *state[A, R, D]]()
	nodes := []struct {
		name string
		fn   func(context.Context, *state[A, R, D]) (*state[A, R, D], error)
	}{
		{"prepare", prepare[A, R, D]}, {"model", generate[A, R, D]}, {"validate", validate[A, R, D]}, {"journal", save[A, R, D]},
	}
	previous := compose.START
	for _, node := range nodes {
		if err := g.AddLambdaNode(node.name, compose.InvokableLambda(node.fn)); err != nil {
			return empty, err
		}
		if err := g.AddEdge(previous, node.name); err != nil {
			return empty, err
		}
		previous = node.name
	}
	branch := compose.NewGraphBranch(func(_ context.Context, s *state[A, R, D]) (string, error) {
		if s.err != nil || s.result.Accepted {
			return compose.END, nil
		}
		return "prepare", nil
	}, map[string]bool{"prepare": true, compose.END: true})
	if err := g.AddBranch("journal", branch); err != nil {
		return empty, err
	}
	graph, err := g.Compile(ctx, compose.WithMaxRunSteps(cfg.MaxAttempts*4+2))
	if err != nil {
		return empty, err
	}
	s := &state[A, R, D]{ctx: ctx, config: cfg, providers: p}
	_, err = graph.Invoke(context.WithoutCancel(ctx), s)
	return s.result, errors.Join(s.err, err)
}

func prepare[A, R, D any](_ context.Context, s *state[A, R, D]) (*state[A, R, D], error) {
	s.result.Attempts++
	s.result.Last = Attempt[A, R, D]{Number: s.result.Attempts}
	a := &s.result.Last
	if err := s.ctx.Err(); err != nil {
		a.PrepareError = err
		return s, nil
	}
	a.Input, a.PrepareError = s.providers.Context.Prepare(s.ctx, a.Number)
	return s, nil
}
func generate[A, R, D any](_ context.Context, s *state[A, R, D]) (*state[A, R, D], error) {
	a := &s.result.Last
	if a.PrepareError != nil {
		return s, nil
	}
	if err := s.ctx.Err(); err != nil {
		a.ModelError = err
		return s, nil
	}
	a.Response, a.ModelError = s.providers.Model.Generate(s.ctx, a.Input)
	return s, nil
}
func validate[A, R, D any](_ context.Context, s *state[A, R, D]) (*state[A, R, D], error) {
	a := &s.result.Last
	if a.PrepareError != nil || a.ModelError != nil {
		return s, nil
	}
	if err := s.ctx.Err(); err != nil {
		a.ModelError = err
		return s, nil
	}
	a.Decision, a.ValidationError = s.providers.Decision.Validate(s.ctx, a.Input, a.Response)
	return s, nil
}
func save[A, R, D any](_ context.Context, s *state[A, R, D]) (*state[A, R, D], error) {
	a := s.result.Last
	// Preparation errors carry no prepared input/reservation. The Context
	// Provider owns atomic cleanup if its own preparation fails partway.
	if a.PrepareError == nil {
		if err := s.providers.Journal.Save(s.ctx, a); err != nil {
			s.err = errors.Join(a.Error(), err)
			return s, nil
		}
	}
	if err := s.ctx.Err(); err != nil {
		s.err = errors.Join(a.Error(), err)
		return s, nil
	}
	if a.Error() == nil {
		s.result.Accepted = true
		return s, nil
	}
	if s.config.Retry == nil || !s.config.Retry(a) {
		s.err = a.Error()
		return s, nil
	}
	if s.result.Attempts >= s.config.MaxAttempts {
		s.err = fmt.Errorf("%w: %w", ErrAttemptsExhausted, a.Error())
	}
	return s, nil
}
