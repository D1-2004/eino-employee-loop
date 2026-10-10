// Package httpapi carries the Employee Loop service boundaries as JSON over
// HTTP. Each boundary has a server handler wrapping the contract interface
// and a client implementing it, so the Event Gateway, the Employee Loop and
// the Task Manager can run in one process or apart without code changes:
//
//	Loop ingress    POST /v1/events                      contract.EventSink
//	Task Manager    POST /v1/tasks                       contract.TaskManager
//	                GET  /v1/tasks/{ref}?employee=
//	                GET  /v1/tasks?employee=&scene=&requester=&text=&limit=
//	                POST /v1/tasks/{ref}/steer|stop|continue
//	Channel egress  POST /v1/deliveries                  contract.Deliverer
//	                POST /v1/deliveries/status           contract.DeliveryReconciler
//	Health          GET  /healthz                        (no auth)
//
// Bodies are the plain contract types. Errors are {"code","message"}; each
// code maps to one sentinel error, so errors.Is works across the wire (see
// ErrorCodes). Handlers and clients optionally share a bearer token, and
// request bodies are bounded.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// DefaultMaxBody bounds request bodies unless WithMaxBody overrides it.
const DefaultMaxBody int64 = 1 << 20

// maxResponse bounds response bodies read by clients.
const maxResponse int64 = 16 << 20

// Option configures a handler or a client.
type Option func(*config)

type config struct {
	token   string
	maxBody int64
	client  *http.Client
}

// WithToken sets the shared bearer token. Handlers reject requests without
// it (401); clients send it.
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithMaxBody bounds request bodies accepted by handlers.
func WithMaxBody(n int64) Option { return func(c *config) { c.maxBody = n } }

// WithHTTPClient sets the client's transport (default http.DefaultClient).
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.client = hc } }

func newConfig(opts []Option) config {
	c := config{maxBody: DefaultMaxBody, client: http.DefaultClient}
	for _, o := range opts {
		o(&c)
	}
	if c.maxBody <= 0 {
		c.maxBody = DefaultMaxBody
	}
	if c.client == nil {
		c.client = http.DefaultClient
	}
	return c
}

// Transport errors without a contract counterpart.
var (
	ErrUnauthorized = errors.New("httpapi: unauthorized")
	ErrTooLarge     = errors.New("httpapi: request body too large")
	ErrBadRequest   = errors.New("httpapi: malformed request")
	// ErrRemote marks a server failure without a more specific code.
	ErrRemote = errors.New("httpapi: remote error")
)

// ErrorCode is one wire error code.
type ErrorCode struct {
	Code   string
	Status int
	Err    error
}

// ErrorCodes lists the wire codes in matching order: a server error maps to
// the first entry it wraps; a client maps a code back to Err.
var ErrorCodes = []ErrorCode{
	{"invalid_event", http.StatusBadRequest, contract.ErrInvalidEvent},
	{"unknown_type", http.StatusBadRequest, contract.ErrUnknownType},
	{"unknown_scene", http.StatusBadRequest, contract.ErrUnknownScene},
	{"unknown_employee", http.StatusNotFound, contract.ErrUnknownEmployee},
	{"invalid_request", http.StatusBadRequest, contract.ErrInvalidRequest},
	{"delivery_rejected", http.StatusUnprocessableEntity, contract.ErrDeliveryRejected},
	{"bad_request", http.StatusBadRequest, ErrBadRequest},
	{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
	{"forbidden", http.StatusForbidden, contract.ErrForbidden},
	{"not_found", http.StatusNotFound, contract.ErrNotFound},
	{"conflict", http.StatusConflict, contract.ErrConflict},
	{"task_state", http.StatusConflict, contract.ErrTaskState},
	{"stale_revision", http.StatusConflict, contract.ErrStaleRevision},
	{"lease", http.StatusConflict, contract.ErrLease},
	{"too_large", http.StatusRequestEntityTooLarge, ErrTooLarge},
	{"quota", http.StatusTooManyRequests, contract.ErrQuota},
	{"unavailable", http.StatusServiceUnavailable, contract.ErrUnavailable},
	{"internal", http.StatusInternalServerError, ErrRemote},
}

// ErrorBody is the wire form of an error.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RemoteError is a decoded server error. It unwraps to the sentinel of its
// code.
type RemoteError struct {
	Status  int
	Code    string
	Message string
	err     error
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("httpapi: %d %s: %s", e.Status, e.Code, e.Message)
}

// Unwrap returns the sentinel error of the code.
func (e *RemoteError) Unwrap() error { return e.err }

func codeFor(err error) ErrorCode {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return codeOf(ErrTooLarge)
	}
	for _, c := range ErrorCodes {
		if errors.Is(err, c.Err) {
			return c
		}
	}
	return codeOf(ErrRemote)
}

func codeOf(sentinel error) ErrorCode {
	for _, c := range ErrorCodes {
		if c.Err == sentinel {
			return c
		}
	}
	return ErrorCode{"internal", http.StatusInternalServerError, ErrRemote}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	c := codeFor(err)
	writeJSON(w, c.Status, ErrorBody{Code: c.Code, Message: err.Error()})
}

// server holds the shared handler plumbing.
type server struct {
	cfg config
	mux *http.ServeMux
}

func newServer(opts []Option) *server {
	s := &server{cfg: newConfig(opts), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.token != "" && r.URL.Path != "/healthz" && !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, ErrUnauthorized)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.cfg.token)) == 1
}

// decode reads a bounded JSON body into v.
func (s *server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, s.cfg.maxBody)
	if err := json.NewDecoder(body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, ErrTooLarge)
		} else {
			writeError(w, fmt.Errorf("%w: %v", ErrBadRequest, err))
		}
		return false
	}
	return true
}

// client holds the shared client plumbing.
type client struct {
	base string
	cfg  config
}

func newClient(baseURL string, opts []Option) client {
	return client{base: strings.TrimRight(baseURL, "/"), cfg: newConfig(opts)}
}

// do sends one request; in (may be nil) is encoded as JSON and a 2xx body is
// decoded into out (may be nil). Non-2xx answers become *RemoteError.
func (c client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.token)
	}
	resp, err := c.cfg.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrRemote, err)
	}
	return nil
}

func decodeError(status int, raw []byte) error {
	var b ErrorBody
	if err := json.Unmarshal(raw, &b); err != nil || b.Code == "" {
		b = ErrorBody{Code: "internal", Message: strings.TrimSpace(string(raw))}
	}
	sentinel := ErrRemote
	for _, c := range ErrorCodes {
		if c.Code == b.Code {
			sentinel = c.Err
			break
		}
	}
	return &RemoteError{Status: status, Code: b.Code, Message: b.Message, err: sentinel}
}
