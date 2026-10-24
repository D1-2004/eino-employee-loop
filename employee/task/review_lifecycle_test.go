package task

import (
	"context"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type partiallyOpenedReviewer struct{ closes, proposals int }

func (r *partiallyOpenedReviewer) Close() error { r.closes++; return nil }
func (r *partiallyOpenedReviewer) Propose(context.Context, ReviewRequest) (work.Review, error) {
	r.proposals++
	return work.Review{}, errors.New("partially opened reviewer cannot propose")
}

func TestReviewFactoryPartialFailureClosesResourceWithoutProposing(t *testing.T) {
	worker, task := reviewFixture(t)
	partial := &partiallyOpenedReviewer{}
	failed := errors.New("reviewer transport initialization failed")
	worker.opts.ReviewFactory = func(context.Context, contract.Profile) (Reviewer, error) { return partial, failed }
	err := worker.Review(context.Background(), task)
	if !errors.Is(err, failed) {
		t.Fatalf("factory failure was lost: %v", err)
	}
	if partial.closes != 1 || partial.proposals != 0 {
		t.Fatalf("partial resource lifecycle: closes=%d proposals=%d", partial.closes, partial.proposals)
	}
	current, getErr := worker.work.Get(context.Background(), task.Scope, task.ID)
	if getErr != nil || !current.NeedsReview || current.Revision != task.Revision {
		t.Fatalf("failed factory changed business state: %+v %v", current, getErr)
	}
}
