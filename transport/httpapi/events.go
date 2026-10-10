package httpapi

import (
	"context"
	"net/http"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// NewEventHandler serves the loop's ingress: POST /v1/events answers 202
// with a contract.EventAck, and GET /healthz.
func NewEventHandler(sink contract.EventSink, opts ...Option) http.Handler {
	s := newServer(opts)
	s.mux.HandleFunc("POST /v1/events", func(w http.ResponseWriter, r *http.Request) {
		var e contract.Event
		if !s.decode(w, r, &e) {
			return
		}
		ack, err := sink.Publish(r.Context(), e)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, ack)
	})
	return s
}

// EventSinkClient publishes events to a remote loop.
type EventSinkClient struct{ c client }

var _ contract.EventSink = (*EventSinkClient)(nil)

// NewEventSinkClient returns a client for a server rooted at baseURL.
func NewEventSinkClient(baseURL string, opts ...Option) *EventSinkClient {
	return &EventSinkClient{c: newClient(baseURL, opts)}
}

// Publish implements contract.EventSink.
func (c *EventSinkClient) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	var ack contract.EventAck
	err := c.c.do(ctx, http.MethodPost, "/v1/events", e, &ack)
	return ack, err
}
