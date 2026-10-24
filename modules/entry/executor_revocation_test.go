package entry

import (
	"context"
	"errors"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/execution"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	"github.com/D1-2004/eino-employee-loop/ports/storage"
)

// A crash after journaling the request but before entering the unknown state
// leaves a Preparing Run. Recovery must recheck the saved frame before any
// external provider receives its previously authorized content.
func TestRecoveredPreparingRequestRevalidatesBeforeExternalSubmit(t *testing.T) {
	for _, revoke := range []string{"source", "owner-epoch"} {
		t.Run(revoke, func(t *testing.T) {
			ctx := context.Background()
			provider := &independentExecutor{}
			registry := NewModuleRegistry()
			ref := ModuleRef{Name: "revocation-probe", Version: "1"}
			if err := registry.RegisterExecutor(ref, func(context.Context, ExecutorDependencies) (execution.Port, error) {
				return provider, nil
			}); err != nil {
				t.Fatal(err)
			}
			profile := testProfile()
			profile.Modules.Executor = ref
			opts := Options{Directory: t.TempDir(), Profile: profile, Modules: registry, Models: models(nil)}
			e, err := openDefault(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			s := e.backend.(*standalone)
			scope := profile.Scope("dm")
			source := work.Source{Receipt: "original-source", Actor: profile.Owner, Text: "private accepted requirement"}
			if _, err = s.context.Admit(ctx, scope, contextbuilder.Source{ID: source.Receipt, Actor: source.Actor, Text: source.Text}); err != nil {
				t.Fatal(err)
			}
			receipt, err := s.work.Dispatch(ctx, scope, source, "dispatch", work.Definition{Goal: source.Text, Instruction: source.Text, MaxRuns: 2, Configuration: encode(profile)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.work.BeginSubmission(ctx, scope, receipt.TaskID, receipt.RunID); err != nil {
				t.Fatal(err)
			}
			task, err := s.work.Get(ctx, scope, receipt.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := s.context.Build(ctx, scope, contextbuilder.Execution, "", task.ID)
			if err != nil {
				t.Fatal(err)
			}
			key, _ := scope.Key()
			request := execution.Request{Task: task, Frame: frame, Configuration: task.Definition.Configuration}
			if _, err = s.db.Put(ctx, storage.Document{Namespace: "execution-requests", Scope: key, ID: task.CurrentRun().SubmitKey, Body: encode(request)}); err != nil {
				t.Fatal(err)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e, err = openDefault(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			s = e.backend.(*standalone)
			if revoke == "source" {
				err = s.context.Revoke(ctx, scope, source.Receipt)
			} else {
				err = s.context.ResetOwner(ctx, scope)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.RunOnce(ctx); !errors.Is(err, contextbuilder.ErrRevoked) {
				t.Fatalf("saved unauthorized request was not rejected: %v", err)
			}
			if provider.submits != 0 {
				t.Fatalf("revoked content reached provider: %d submissions", provider.submits)
			}
			after, err := s.work.Get(ctx, scope, task.ID)
			if err != nil || after.CurrentRun().State != work.RunPreparing {
				t.Fatalf("revoked request changed physical submission state: %+v, %v", after.CurrentRun(), err)
			}
		})
	}
}

type partialOpenBackend struct {
	Backend
	closes int
}

func (b *partialOpenBackend) Close() error { b.closes++; return nil }

func TestBackendFactoryPartialFailureClosesReturnedBackend(t *testing.T) {
	cause := errors.New("backend failed after allocating resources")
	b := &partialOpenBackend{}
	p := testProfile()
	p.Backend = "partial-backend"
	e, err := openDefault(context.Background(), Options{Directory: t.TempDir(), Profile: p, Backends: map[string]func(context.Context, Options) (Backend, error){
		p.Backend: func(context.Context, Options) (Backend, error) { return b, cause },
	}})
	if e != nil || !errors.Is(err, cause) || b.closes != 1 {
		t.Fatalf("partial backend was not cleaned up: entry=%v error=%v closes=%d", e, err, b.closes)
	}
}

func TestBackendFactoryTypedNilIsRejectedWithoutClosingNil(t *testing.T) {
	for _, withError := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-error", true: "partial-error"}[withError], func(t *testing.T) {
			p := testProfile()
			p.Backend = "typed-nil"
			cause := errors.New("factory rejected initialization")
			_, err := openDefault(context.Background(), Options{Directory: t.TempDir(), Profile: p, Backends: map[string]func(context.Context, Options) (Backend, error){p.Backend: func(context.Context, Options) (Backend, error) {
				var b *partialOpenBackend
				if withError {
					return b, cause
				}
				return b, nil
			}}})
			if err == nil || withError && !errors.Is(err, cause) {
				t.Fatalf("typed nil backend accepted or lost original cause: %v", err)
			}
		})
	}
}
