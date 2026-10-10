package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// DeliveryAck is the answer to POST /v1/deliveries.
type DeliveryAck struct {
	ProviderID string `json:"provider_id"`
}

// DeliveryStatus is the answer to POST /v1/deliveries/status.
type DeliveryStatus struct {
	State      contract.DeliveryState `json:"state"`
	ProviderID string                 `json:"provider_id,omitempty"`
}

// NewDeliveryHandler serves a channel's egress: POST /v1/deliveries sends a
// contract.Delivery and answers 200 with a DeliveryAck; a definite rejection
// (contract.ErrDeliveryRejected) answers 422 delivery_rejected. POST
// /v1/deliveries/status looks a delivery up without sending and answers a
// DeliveryStatus; when d is not a contract.DeliveryReconciler the state is
// always unknown.
func NewDeliveryHandler(d contract.Deliverer, opts ...Option) http.Handler {
	s := newServer(opts)
	s.mux.HandleFunc("POST /v1/deliveries", func(w http.ResponseWriter, r *http.Request) {
		var in contract.Delivery
		if !s.decode(w, r, &in) {
			return
		}
		id, err := d.Deliver(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, DeliveryAck{ProviderID: id})
	})
	s.mux.HandleFunc("POST /v1/deliveries/status", func(w http.ResponseWriter, r *http.Request) {
		var in contract.Delivery
		if !s.decode(w, r, &in) {
			return
		}
		out := DeliveryStatus{State: contract.DeliveryUnknown}
		if q, ok := d.(contract.DeliveryReconciler); ok {
			state, id, err := q.DeliveryStatus(r.Context(), in)
			if err != nil {
				writeError(w, err)
				return
			}
			out = DeliveryStatus{State: state, ProviderID: id}
		}
		writeJSON(w, http.StatusOK, out)
	})
	return s
}

// DelivererClient sends deliveries through a remote gateway. It never
// retries. Answers the gateway gives before any send (delivery_rejected,
// bad_request, unauthorized, too_large) are definite rejections and wrap
// contract.ErrDeliveryRejected; any other failure (transport error,
// timeout, 5xx) leaves the outcome unknown, which the caller reconciles
// through DeliveryStatus (see contract.DeliveryUnknown).
type DelivererClient struct{ c client }

var (
	_ contract.Deliverer          = (*DelivererClient)(nil)
	_ contract.DeliveryReconciler = (*DelivererClient)(nil)
)

// NewDelivererClient returns a client for a server rooted at baseURL.
func NewDelivererClient(baseURL string, opts ...Option) *DelivererClient {
	return &DelivererClient{c: newClient(baseURL, opts)}
}

// Deliver implements contract.Deliverer.
func (c *DelivererClient) Deliver(ctx context.Context, d contract.Delivery) (string, error) {
	var ack DeliveryAck
	if err := c.c.do(ctx, http.MethodPost, "/v1/deliveries", d, &ack); err != nil {
		if rejectedBeforeSend(err) && !errors.Is(err, contract.ErrDeliveryRejected) {
			err = fmt.Errorf("%w: %w", contract.ErrDeliveryRejected, err)
		}
		return "", err
	}
	return ack.ProviderID, nil
}

// DeliveryStatus implements contract.DeliveryReconciler. It never sends.
func (c *DelivererClient) DeliveryStatus(ctx context.Context, d contract.Delivery) (contract.DeliveryState, string, error) {
	var out DeliveryStatus
	if err := c.c.do(ctx, http.MethodPost, "/v1/deliveries/status", d, &out); err != nil {
		return contract.DeliveryUnknown, "", err
	}
	return out.State, out.ProviderID, nil
}

// rejectedBeforeSend reports a remote answer that proves the gateway did not
// send: an explicit rejection, or a request refused before the Deliverer ran.
func rejectedBeforeSend(err error) bool {
	var re *RemoteError
	if !errors.As(err, &re) {
		return false
	}
	switch re.Code {
	case "delivery_rejected", "bad_request", "unauthorized", "too_large":
		return true
	}
	return false
}
