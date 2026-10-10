package scene

import (
	"context"

	"github.com/D1-2004/eino-employee-loop/contract"
)

var _ contract.EventSink = (*Hub)(nil)

// Publish makes a Hub the loop's ingress (contract.EventSink): it admits the
// event and answers with its receipt. Replays of the same (source, event id)
// return the original receipt with Duplicate set; reusing an id with other
// content returns contract.ErrConflict.
func (h *Hub) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	a, err := h.Admit(ctx, e)
	if err != nil {
		return contract.EventAck{}, err
	}
	ack := contract.EventAck{ReceiptID: a.Receipt.ID, Duplicate: a.Duplicate, Lane: a.Receipt.Lane}
	if a.Window != nil {
		ack.WindowID = a.Window.ID
	}
	return ack, nil
}
