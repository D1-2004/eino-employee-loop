package contract

import (
	"context"
	"errors"
	"time"
)

// Storage errors shared by every port implementation.
var (
	ErrNotFound = errors.New("contract: not found")
	// ErrConflict marks a replay of an existing identity with different content.
	ErrConflict = errors.New("contract: idempotency conflict")
	// ErrLease marks a stale or foreign lease.
	ErrLease = errors.New("contract: lease lost")
	ErrQuota = errors.New("contract: quota exceeded")
	// ErrBadQuote marks a memory quote that is not verbatim speaker text.
	ErrBadQuote = errors.New("contract: quote is not a verbatim substring of the speaker's message")
)

// ---------------------------------------------------------------------------
// Event log: one durable receipt per (employee, source, event id).

// EventReceipt records an admitted event and its routing decision.
type EventReceipt struct {
	ID          string      `json:"id"`
	Employee    string      `json:"employee"`
	Source      string      `json:"source"`
	EventID     string      `json:"event_id"`
	Type        string      `json:"type"`
	Class       Class       `json:"class"`
	Mode        Mode        `json:"mode"`
	Lane        Lane        `json:"lane"`
	Session     *SessionRef `json:"session,omitempty"`
	Fingerprint string      `json:"fingerprint"`
	AdmittedAt  time.Time   `json:"admitted_at"`
	// Applied is set once the admission's effect committed: the enqueue
	// (session lanes) or the cancellations and the host applier (host lane).
	Applied bool `json:"applied,omitempty"`
}

// EventLog persists receipts. Admit is idempotent: the same (employee,
// source, event id) with the same fingerprint returns the original receipt
// and created=false; a different fingerprint returns ErrConflict.
//
// A receipt and its effect are not one transaction: the admitter commits
// the receipt, applies the effect (each step idempotent per receipt) and
// then calls MarkApplied. A duplicate of a receipt that is not Applied
// re-runs the effect, so a crash or a failed step between the two never
// drops the event. MarkApplied is idempotent.
type EventLog interface {
	Admit(ctx context.Context, employee string, adm Admission, fingerprint string) (EventReceipt, bool, error)
	MarkApplied(ctx context.Context, employee, receiptID string) error
	Receipt(ctx context.Context, employee, receiptID string) (EventReceipt, error)
}

// ---------------------------------------------------------------------------
// Session queue: per-scene foreground session and queued background sessions.

// WindowState is the lifecycle of a window.
type WindowState string

const (
	// WindowOpen still accepts coalesced items.
	WindowOpen WindowState = "open"
	// WindowReady is sealed and waiting to be claimed.
	WindowReady   WindowState = "ready"
	WindowRunning WindowState = "running"
	WindowDone    WindowState = "done"
	// WindowHeld stopped with an explicit reason and will not be retried.
	WindowHeld WindowState = "held"
	// WindowCancelled was preempted or revoked before submission.
	WindowCancelled WindowState = "cancelled"
)

// WindowItem is one admitted event inside a window.
type WindowItem struct {
	ReceiptID string `json:"receipt_id"`
	Event     Event  `json:"event"`
}

// Lease proves ownership of a running window. Every write is fenced by
// (window id, token, generation).
type Lease struct {
	WindowID   string    `json:"window_id"`
	Token      string    `json:"token"`
	Generation int64     `json:"generation"`
	Until      time.Time `json:"until"`
}

// Window is the unit of work of a session: one model wake.
type Window struct {
	ID          string       `json:"id"`
	Employee    string       `json:"employee"`
	Session     SessionRef   `json:"session"`
	Lane        Lane         `json:"lane"`
	State       WindowState  `json:"state"`
	Preemptible bool         `json:"preemptible"`
	Items       []WindowItem `json:"items"`
	OpenedAt    time.Time    `json:"opened_at"`
	// DueAt is when an open window seals by itself (coalesce deadline).
	DueAt    time.Time `json:"due_at"`
	Attempts int       `json:"attempts"`
	Lease    *Lease    `json:"lease,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// ClaimRequest selects the next window. Every implementation enforces:
//
//   - Sessions are FIFO: only the oldest unfinished (open, ready or
//     running) window of a session may be claimed, so a window waiting for
//     its retry (ready with a future DueAt) keeps the later windows of its
//     session behind it, and at most one window per session runs at a
//     time. "Oldest" is creation order (opened_at, then a monotonic id).
//   - Foreground windows of a scene are claimed before its background
//     windows: a background window waits while its scene has an open or
//     ready foreground window.
//   - A preemptible window also waits while its scene has a running
//     foreground window, so an observation admitted during a foreground
//     wake never runs beside it.
//
// SerializeScene additionally allows only one running window per scene
// (stricter hosts).
type ClaimRequest struct {
	Worker         string        `json:"worker"`
	Lanes          []Lane        `json:"lanes,omitempty"`
	Sessions       []SessionKind `json:"sessions,omitempty"`
	LeaseFor       time.Duration `json:"lease_for"`
	SerializeScene bool          `json:"serialize_scene,omitempty"`
	Now            time.Time     `json:"now"`
}

// WindowResult is what a worker reports when it finishes a window.
type WindowResult struct {
	Outcome Outcome `json:"outcome"`
	Failure string  `json:"failure,omitempty"`
}

// SessionQueue owns window lifecycle.
//
// Enqueue applies adm.Cancel in the same transaction, so a foreground
// admission and the preemption of observation windows commit together. It
// is idempotent per receipt: a second Enqueue of a receipt that a window
// already holds returns that window and changes nothing. When the item
// cannot join the session's open window (no coalescing, MaxItems reached or
// past MaxWait), that window is sealed (ready, due no later than now) before
// a new one is opened, so no window is left open behind its successor.
//
// Claim seals every open window whose DueAt has passed before it selects,
// then applies the ordering rules of ClaimRequest.
//
// Complete records the result and inserts the window's deliveries into the
// outbox in one transaction fenced by the lease: either the window is done
// and its deliveries are pending, or nothing changed. A delivery whose ID
// exists with other content fails the whole call with ErrConflict.
//
// Cancel applies the cancellations of one host-lane receipt once: a
// repeated call with the same receipt cancels nothing more and returns the
// original count, so a re-run never revokes windows admitted after it.
type SessionQueue interface {
	Enqueue(ctx context.Context, employee string, adm Admission, receiptID string, now time.Time) (Window, error)
	Claim(ctx context.Context, req ClaimRequest) (*Window, error)
	Complete(ctx context.Context, lease Lease, result WindowResult, deliveries []Delivery) error
	Retry(ctx context.Context, lease Lease, reason string, notBefore time.Time) error
	Hold(ctx context.Context, lease Lease, reason string) error
	Cancel(ctx context.Context, employee, receiptID string, cancels []SessionCancel) (int, error)
	Window(ctx context.Context, employee, windowID string) (Window, error)
}

// ---------------------------------------------------------------------------
// Journal: exactly-once model requests and tool effects within a window.

// ModelTurn is one recorded provider exchange.
type ModelTurn struct {
	Seq      int    `json:"seq"`
	Request  []byte `json:"request"`
	Response []byte `json:"response,omitempty"`
	Failure  string `json:"failure,omitempty"`
}

// ToolRecord is one recorded tool execution, keyed by (Turn, NativeID).
// Error holds the exact error text the model saw; Refused records whether
// that error was a contract.ErrToolRefused refusal.
type ToolRecord struct {
	Turn     int        `json:"turn"`
	NativeID string     `json:"native_id"`
	Call     ToolCall   `json:"call"`
	Result   ToolResult `json:"result"`
	Error    string     `json:"error,omitempty"`
	Refused  bool       `json:"refused,omitempty"`
}

// Journal lets a re-claimed window replay committed model turns and tool
// effects instead of repeating them. Writes are fenced by the lease.
//
// Model turns are saved in Seq order (Seq == number of saved turns,
// otherwise ErrConflict). Tool records are unique per (Turn, NativeID)
// within a window; a second save of the same key is ErrConflict. Tools
// returns the window's records in save order.
type Journal interface {
	ModelTurns(ctx context.Context, lease Lease) ([]ModelTurn, error)
	SaveModelTurn(ctx context.Context, lease Lease, turn ModelTurn) error
	Tools(ctx context.Context, lease Lease) ([]ToolRecord, error)
	SaveTool(ctx context.Context, lease Lease, rec ToolRecord) error
}

// ---------------------------------------------------------------------------
// Outbox: user-visible deliveries.

// DeliveryState is the provider-facing lifecycle of a delivery.
type DeliveryState string

const (
	DeliveryPending DeliveryState = "pending"
	// DeliveryUnknown means a send was attempted and its result is unknown:
	// the attempt is in flight, crashed, or failed ambiguously (transport
	// error, timeout, 5xx). It is reconciled by status query
	// (DeliveryReconciler), never re-sent.
	DeliveryUnknown   DeliveryState = "unknown"
	DeliveryAccepted  DeliveryState = "provider_accepted"
	DeliveryDelivered DeliveryState = "delivered"
	// DeliveryFailed means the channel definitely did not send the message
	// (ErrDeliveryRejected, or a status query that says so).
	DeliveryFailed DeliveryState = "failed"
	// DeliverySuppressed was revoked before any send attempt.
	DeliverySuppressed DeliveryState = "suppressed"
)

// Delivery is one outbound message produced by a window.
type Delivery struct {
	// ID is idempotent per window and purpose, e.g. "<window>/reply".
	ID       string        `json:"id"`
	Employee string        `json:"employee"`
	Scene    SceneRef      `json:"scene"`
	Session  SessionRef    `json:"session"`
	WindowID string        `json:"window_id"`
	ReplyTo  string        `json:"reply_to,omitempty"`
	Text     string        `json:"text"`
	Locale   string        `json:"locale,omitempty"`
	State    DeliveryState `json:"state"`
	// ProviderID is the provider message id once known.
	ProviderID string    `json:"provider_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Outbox stores deliveries. Windows write theirs through
// SessionQueue.Complete; Post is for other producers and is idempotent by
// Delivery.ID. Transition is a compare-and-set; an unknown delivery is
// never re-sent. Pending lists pending deliveries oldest first. Unknown
// lists unknown deliveries last updated before before (zero: all), oldest
// update first, for reconciliation.
type Outbox interface {
	Post(ctx context.Context, d Delivery) (Delivery, bool, error)
	Transition(ctx context.Context, employee, id string, from []DeliveryState, to DeliveryState, providerID string) (bool, error)
	Pending(ctx context.Context, employee string, limit int) ([]Delivery, error)
	Unknown(ctx context.Context, employee string, before time.Time, limit int) ([]Delivery, error)
}

// ---------------------------------------------------------------------------
// Task statuses, memory and conversation ports.

// Task statuses reported in TaskView.Status, TaskReceipt.Status and the
// task_status of execution payloads.
const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskDone      = "done"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// TaskActive reports whether a task can still change.
func TaskActive(status string) bool { return status == TaskQueued || status == TaskRunning }

// Fact is one memory record with provenance.
type Fact struct {
	ID            string `json:"id"`
	Tenant        string `json:"-"`
	Scene         string `json:"scene"`
	Subject       string `json:"subject"`  // person for audience=me, "" for scene
	Audience      string `json:"audience"` // me | scene
	Kind          string `json:"kind"`     // fact | decision | preference | pattern | pitfall | digest
	Text          string `json:"text"`
	Quote         string `json:"quote"`
	Speaker       string `json:"speaker"`
	SpeakerRole   string `json:"speaker_role"` // user | employee | host
	EvidenceScope string `json:"evidence_scope"`
	SourceRef     string `json:"source_ref"`
	SaidMs        int64  `json:"said_ms"`
	CreatedMs     int64  `json:"created_ms"`
	Status        string `json:"status"` // active | archived
	ArchivedBy    string `json:"archived_by,omitempty"`
	TextHash      string `json:"-"`
}

// Evidence scopes are machine identifiers; hosts render them through i18n.
const (
	ScopeUserSaid     = "user_said"
	ScopeEmployeeSaid = "employee_said"
	ScopeHostDigest   = "host_digest"
)

// MemoryLimits bounds one memory bag (tenant, scene, subject, audience).
type MemoryLimits struct {
	MaxFactBytes     int `json:"max_fact_bytes"`
	MaxActivePerBag  int `json:"max_active_per_bag"`
	MaxBytesPerBag   int `json:"max_bytes_per_bag"`
	CompactKeepNewer int `json:"compact_keep_newer"`
}

// DefaultMemoryLimits are conservative defaults for chat-scale scenes.
var DefaultMemoryLimits = MemoryLimits{MaxFactBytes: 600, MaxActivePerBag: 120, MaxBytesPerBag: 24 * 1024, CompactKeepNewer: 60}

// MemoryStore persists facts. It never deletes or rewrites text: forget and
// compaction archive records.
type MemoryStore interface {
	// Put validates provenance (quote must be a substring of originalMsg when
	// originalMsg != ""), dedups identical text and enforces quotas.
	Put(ctx context.Context, f *Fact, originalMsg string) (cur *Fact, created bool, err error)
	Search(ctx context.Context, tenant, scene, subject, audience, query string, limit int) ([]*Fact, error)
	Recent(ctx context.Context, tenant, scene, subject, audience string, limit int) ([]*Fact, error)
	Archive(ctx context.Context, tenant string, ids []string, by string) (int, error)
	// Compact archives the oldest active facts of a bag and writes one digest
	// fact listing the originals verbatim; no model summarisation.
	Compact(ctx context.Context, tenant, scene, subject, audience string) (archived int, digestID string, err error)
	CountActive(ctx context.Context, tenant, scene, subject, audience string) (n int, bytes int, err error)
	GetFact(ctx context.Context, tenant, id string) (*Fact, error)
	Close() error
}

// Msg is one durable conversation log entry.
type Msg struct {
	Seq     int64  `json:"seq"`
	Role    string `json:"role"` // user | assistant | host
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	// Kind is a machine label of the entry. The loop records msg, reaction
	// and reply; Hosts may add their own kinds.
	Kind      string `json:"kind"`
	SourceRef string `json:"source_ref"`
	AtMs      int64  `json:"at_ms"`
}

// Conversation is the dialogue log of a scene. Append assigns the next
// Seq. It is idempotent per (tenant, scene, SourceRef, Kind) when SourceRef
// is not empty: a repeat returns the stored message and appends nothing, so
// a recorder can re-run after a failure or for a duplicate event. Recent
// returns up to n newest messages with Seq < beforeSeq (0 = no bound),
// oldest first.
type Conversation interface {
	Append(ctx context.Context, tenant, scene string, m Msg) (Msg, error)
	Recent(ctx context.Context, tenant, scene string, beforeSeq int64, n int) ([]Msg, error)
}
