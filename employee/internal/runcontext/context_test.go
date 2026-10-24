package runcontext

import (
	"context"
	"errors"
	"testing"
	"time"
)

type key string

func TestNodeValuesKeepOriginalCancellationDeadlineAndCause(t *testing.T) {
	cause := errors.New("revoked")
	base, deadlineCancel := context.WithTimeout(context.WithValue(context.Background(), key("scope"), "trusted"), time.Minute)
	defer deadlineCancel()
	original, cancel := context.WithCancelCause(base)
	defer cancel(nil)
	node := context.WithValue(context.WithoutCancel(original), key("span"), "node-span")
	op := Operation(original, node)
	if op.Value(key("span")) != "node-span" || op.Value(key("scope")) != "trusted" {
		t.Fatal("node values or trusted scope lost")
	}
	d, ok := op.Deadline()
	want, _ := original.Deadline()
	if !ok || d != want {
		t.Fatal("original deadline lost")
	}
	cancel(cause)
	if !errors.Is(op.Err(), context.Canceled) || !errors.Is(context.Cause(op), cause) {
		t.Fatal("original cause lost", op.Err(), context.Cause(op))
	}
}
