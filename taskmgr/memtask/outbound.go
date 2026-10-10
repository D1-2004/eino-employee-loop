package memtask

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// outbound is the ordered, in-memory retry queue between the manager and
// the loop's EventSink. One goroutine publishes the head event; a transient
// failure is retried with capped exponential backoff and blocks the events
// behind it, so the loop sees each task's events in order.
//
// Limits:
//   - The queue lives in process memory: events not yet published are lost
//     if the process exits. Close flushes it within the caller's deadline.
//   - The bound is soft. Callers that create new work (Dispatch, Steer,
//     Continue, Progress) wait for room first; events a run needs to finish
//     (started, result, terminal) are always accepted, so the queue can
//     exceed the limit by a few events per active run.
//   - A permanent rejection by the sink (invalid event, unknown type or
//     scene, idempotency conflict) cannot succeed on retry; the event is
//     handed to the reject handler and skipped.
type outbound struct {
	sink       contract.EventSink
	limit      int
	base, max  time.Duration
	timeout    time.Duration
	onRejected func(contract.Event, error)

	mu      sync.Mutex
	items   []contract.Event
	changed chan struct{} // closed and replaced whenever items shrink or grow
	stopped bool

	stop       chan struct{}
	stopCtx    context.Context
	cancelStop context.CancelFunc
	done       chan struct{}
}

func newOutbound(sink contract.EventSink, o options) *outbound {
	q := &outbound{
		sink: sink, limit: o.queueLimit, base: o.backoffBase, max: o.backoffMax,
		timeout: o.publishTimeout, onRejected: o.onRejected,
		changed: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	q.stopCtx, q.cancelStop = context.WithCancel(context.Background())
	go q.loop()
	return q
}

// signalLocked wakes every waiter. Callers hold q.mu.
func (q *outbound) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

// push appends events without waiting for room.
func (q *outbound) push(events ...contract.Event) {
	if len(events) == 0 {
		return
	}
	q.mu.Lock()
	q.items = append(q.items, events...)
	q.signalLocked()
	q.mu.Unlock()
}

// waitRoom blocks until the queue is below its limit, ctx ends or the
// publisher stopped.
func (q *outbound) waitRoom(ctx context.Context) error {
	for {
		q.mu.Lock()
		if q.stopped {
			q.mu.Unlock()
			return errStopped
		}
		if q.limit <= 0 || len(q.items) < q.limit {
			q.mu.Unlock()
			return nil
		}
		ch := q.changed
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// flush waits until every queued event was published or rejected.
func (q *outbound) flush(ctx context.Context) error {
	for {
		q.mu.Lock()
		n := len(q.items)
		ch := q.changed
		q.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (q *outbound) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// shutdown stops the publisher and waits for it to exit.
func (q *outbound) shutdown() {
	q.mu.Lock()
	if !q.stopped {
		q.stopped = true
		close(q.stop)
		q.cancelStop()
		q.signalLocked()
	}
	q.mu.Unlock()
	<-q.done
}

var errStopped = errors.New("memtask: publisher stopped")

func (q *outbound) loop() {
	defer close(q.done)
	delay := q.base
	for {
		q.mu.Lock()
		var head contract.Event
		has := len(q.items) > 0
		if has {
			head = q.items[0]
		}
		ch := q.changed
		q.mu.Unlock()
		if !has {
			select {
			case <-ch:
				continue
			case <-q.stop:
				return
			}
		}
		err := q.publish(head)
		if err == nil || permanent(err) {
			if err != nil && q.onRejected != nil {
				q.onRejected(head, err)
			}
			q.mu.Lock()
			q.items = q.items[1:]
			q.signalLocked()
			q.mu.Unlock()
			delay = q.base
			continue
		}
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-q.stop:
			t.Stop()
			return
		}
		if delay *= 2; delay > q.max {
			delay = q.max
		}
	}
}

func (q *outbound) publish(e contract.Event) error {
	ctx, cancel := context.WithTimeout(q.stopCtx, q.timeout)
	defer cancel()
	_, err := q.sink.Publish(ctx, e)
	return err
}

// permanent reports sink errors that no retry can fix.
func permanent(err error) bool {
	return errors.Is(err, contract.ErrInvalidEvent) || errors.Is(err, contract.ErrUnknownType) ||
		errors.Is(err, contract.ErrUnknownScene) || errors.Is(err, contract.ErrConflict) ||
		errors.Is(err, contract.ErrUnknownEmployee)
}
