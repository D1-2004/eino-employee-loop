package dws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Sender sends text to a conversation and returns the provider message id
// (empty when unknown). *Client implements it.
type Sender interface {
	Send(ctx context.Context, conversationID, text string) (string, error)
}

// Gateway errors.
var (
	// ErrNoLocator marks a delivery whose scene has no conversation locator.
	ErrNoLocator = errors.New("dws: delivery_without_locator")
	// ErrForeignScene marks a delivery for another provider's scene.
	ErrForeignScene = errors.New("dws: foreign_scene")
)

// Deliverer sends the loop's deliveries through dws (contract.Deliverer).
// The conversation is the delivery scene's Locator. It remembers the
// provider ids of the deliveries it sent (bounded, in memory) and answers
// status queries from that record (contract.DeliveryReconciler).
type Deliverer struct {
	Sender Sender
	// Own records sent message ids for the Normalizer; may be nil.
	Own *RecentIDs
	// Remember bounds the delivery ids kept for status queries (default
	// 1024).
	Remember int

	mu    sync.Mutex
	sent  map[string]string // delivery id -> provider id
	order []string
}

var (
	_ contract.Deliverer          = (*Deliverer)(nil)
	_ contract.DeliveryReconciler = (*Deliverer)(nil)
)

// Deliver implements contract.Deliverer. It never retries. A delivery that
// cannot be addressed is a definite rejection (contract.ErrDeliveryRejected);
// a send error is ambiguous.
func (d *Deliverer) Deliver(ctx context.Context, del contract.Delivery) (string, error) {
	if del.Scene.Provider != "" && del.Scene.Provider != Provider {
		return "", fmt.Errorf("%w: %w: %s", contract.ErrDeliveryRejected, ErrForeignScene, del.Scene.Provider)
	}
	if del.Scene.Locator == "" {
		return "", fmt.Errorf("%w: %w", contract.ErrDeliveryRejected, ErrNoLocator)
	}
	id, err := d.Sender.Send(ctx, del.Scene.Locator, del.Text)
	if err != nil {
		if errors.Is(err, ErrEmptyConversation) {
			err = fmt.Errorf("%w: %w", contract.ErrDeliveryRejected, err)
		}
		return "", err
	}
	d.Own.Add(id)
	d.remember(del.ID, id)
	return id, nil
}

// DeliveryStatus implements contract.DeliveryReconciler: a delivery this
// Deliverer sent is delivered; anything else is unknown (it may have been
// sent before a restart). It never sends.
func (d *Deliverer) DeliveryStatus(_ context.Context, del contract.Delivery) (contract.DeliveryState, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.sent[del.ID]; ok {
		return contract.DeliveryDelivered, id, nil
	}
	return contract.DeliveryUnknown, "", nil
}

func (d *Deliverer) remember(deliveryID, providerID string) {
	if deliveryID == "" {
		return
	}
	limit := d.Remember
	if limit <= 0 {
		limit = 1024
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sent == nil {
		d.sent = map[string]string{}
	}
	if _, ok := d.sent[deliveryID]; !ok {
		d.order = append(d.order, deliveryID)
	}
	d.sent[deliveryID] = providerID
	for len(d.order) > limit {
		delete(d.sent, d.order[0])
		d.order = d.order[1:]
	}
}

// Source yields provider messages of a conversation. *Client implements it.
type Source interface {
	Subscribe(ctx context.Context, conversationID string) (<-chan Message, error)
}

// ReadMarker marks handled messages read. *Client implements it.
type ReadMarker interface {
	MarkRead(ctx context.Context, conversationID, messageID string) error
}

// Listener pumps a conversation's messages through a Normalizer into the
// loop's EventSink.
type Listener struct {
	Source     Source
	Normalizer *Normalizer
	Sink       contract.EventSink
	// Marker, when set, marks published messages read.
	Marker ReadMarker
	// Attempts bounds publishes of one event on transient errors (default 3).
	Attempts int
	// Backoff is the wait between attempts (default 500ms, doubling).
	Backoff time.Duration
	Logger  *slog.Logger
}

// Run subscribes and publishes until the subscription closes or ctx ends.
func (l *Listener) Run(ctx context.Context, conversationID string) error {
	ch, err := l.Source.Subscribe(ctx, conversationID)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-ch:
			if !ok {
				return ctx.Err()
			}
			l.handle(ctx, m)
		}
	}
}

func (l *Listener) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

// handle normalizes and publishes one message.
func (l *Listener) handle(ctx context.Context, m Message) {
	e, ok, err := l.Normalizer.Normalize(ctx, m)
	if err != nil {
		l.logger().Warn("dws.normalize.failed", "message", m.MessageID, "err", err)
		return
	}
	if !ok {
		return
	}
	ack, err := l.publish(ctx, e)
	if err != nil {
		l.logger().Warn("dws.publish.failed", "event", e.ID, "type", e.Type, "err", err)
		return
	}
	l.logger().Debug("dws.published", "event", e.ID, "type", e.Type, "lane", ack.Lane, "duplicate", ack.Duplicate)
	if l.Marker != nil && m.Kind != MessageReaction && !ack.Duplicate {
		if err := l.Marker.MarkRead(ctx, m.ConversationID, m.MessageID); err != nil {
			l.logger().Debug("dws.mark_read.failed", "message", m.MessageID, "err", err)
		}
	}
}

// publish retries transient failures. Rejections the loop will repeat
// (invalid event, unknown type or scene, conflicting replay) are permanent.
func (l *Listener) publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	attempts := l.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	wait := l.Backoff
	if wait <= 0 {
		wait = 500 * time.Millisecond
	}
	var err error
	for i := 0; i < attempts; i++ {
		var ack contract.EventAck
		if ack, err = l.Sink.Publish(ctx, e); err == nil {
			return ack, nil
		}
		if Permanent(err) || i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return contract.EventAck{}, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	return contract.EventAck{}, err
}

// Permanent reports a publish rejection that retrying cannot fix.
func Permanent(err error) bool {
	for _, p := range []error{contract.ErrInvalidEvent, contract.ErrUnknownType, contract.ErrUnknownScene, contract.ErrConflict} {
		if errors.Is(err, p) {
			return true
		}
	}
	return false
}
