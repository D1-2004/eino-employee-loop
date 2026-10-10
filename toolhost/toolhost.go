// Package toolhost implements the standard tools of the Employee Loop over
// the contract ports, so a loop runs with no platform behind it:
//
//	Host ──Specs()──► KernelRunner.Tools (catalog ∩ implemented ∩ configured ports)
//	     ──ForWindow(win)──► contract.Host bound to one claimed window
//
// Every call is fenced by the bound window: a source_ref must name an event
// of that window, the requester of a call is the authenticated actor of that
// event (never model output), and task controls need a read_ref obtained by
// read_task in the same window, as recorded in the window's journal. The
// package knows no vendor, channel or business rule; background work goes
// through contract.TaskManager, memory through contract.MemoryStore, and
// history through contract.Conversation.
//
// Refusals are errors wrapping contract.ErrToolRefused with a machine code
// (see Refusal); tool result Content is compact JSON of machine fields.
package toolhost

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Ports are the services behind the standard tools. Each is optional; a
// tool is offered only when every port it names is configured.
type Ports struct {
	Tasks        contract.TaskManager
	Memory       contract.MemoryStore
	Conversation contract.Conversation
	// Journal must be the journal the wake records through
	// (scene.KernelRunner.Journal). It is the durable authority for read
	// refs, so a re-claimed window, in any process, keeps the reads of its
	// earlier claims. Tools that name the "journal" port need it.
	Journal contract.Journal
}

// Refusal codes. They are machine identifiers returned to the model.
const (
	CodeUnknownTool        = "unknown_tool"
	CodeToolUnavailable    = "tool_unavailable"
	CodeNotOffered         = "tool_not_offered_in_session"
	CodeWindowMismatch     = "window_mismatch"
	CodeUnknownSourceRef   = "unknown_source_ref"
	CodeNoRequester        = "no_requester"
	CodeEmptyText          = "empty_text"
	CodeForbidden          = "forbidden"
	CodeTaskState          = "task_state"
	CodeInvalidRequest     = "invalid_request"
	CodeTaskNotFound       = "task_not_found"
	CodeTaskNotInScene     = "task_not_in_scene"
	CodeNotRequester       = "not_requester"
	CodeBadReadRef         = "bad_read_ref"
	CodeReadRefNotInWindow = "read_ref_not_in_window"
	CodeStaleReadRef       = "stale_read_ref"
	CodeNoRunningTask      = "no_running_task"
	CodeAmbiguousTask      = "ambiguous_task"
	CodeTaskNotRunning     = "task_not_running"
	CodeBadQuote           = "bad_quote"
	CodeMemoryQuota        = "memory_quota"
	CodeBadMemoryKind      = "bad_memory_kind"
	CodeUnknownRecord      = "unknown_record"
	CodeSectionUnavailable = "section_unavailable"
)

// Refusal is a Host refusal before any effect. errors.Is(r,
// contract.ErrToolRefused) holds, so the kernel lets the model correct the
// call within the remaining budget.
type Refusal struct{ Code string }

func (r *Refusal) Error() string { return contract.ErrToolRefused.Error() + ": " + r.Code }

// Is makes a Refusal match contract.ErrToolRefused.
func (r *Refusal) Is(target error) bool { return target == contract.ErrToolRefused }

func refuse(code string) error { return &Refusal{Code: code} }

// RefusalCode returns the machine code of a refusal, or "".
func RefusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// Option configures a Host.
type Option func(*Host)

// WithMessages sets the bundle used for stored text (memory records, ask
// options). Default: i18n.Default().
func WithMessages(b *i18n.Bundle) Option { return func(h *Host) { h.messages = b } }

// WithLocale sets the default locale; an event's own Locale wins.
func WithLocale(locale string) Option { return func(h *Host) { h.locale = locale } }

// WithCatalog replaces the standard tool catalog, for hosts that ship a
// customised catalog. The Host offers only the tools it implements (the
// standard ones); extension tools an embedding platform adds (see the
// examples/hostext profile) are executed by that platform's own
// contract.Host.
func WithCatalog(specs []contract.ToolSpec) Option {
	return func(h *Host) { h.catalog = append([]contract.ToolSpec(nil), specs...) }
}

// Limits bound list sizes returned to the model.
type Limits struct {
	FindTasks     int
	MemoryRecords int
	Conversation  int
}

// DefaultLimits are chat-scale defaults.
var DefaultLimits = Limits{FindTasks: 10, MemoryRecords: 6, Conversation: 40}

// WithLimits replaces DefaultLimits; zero fields keep the default.
func WithLimits(l Limits) Option {
	return func(h *Host) {
		if l.FindTasks > 0 {
			h.limits.FindTasks = l.FindTasks
		}
		if l.MemoryRecords > 0 {
			h.limits.MemoryRecords = l.MemoryRecords
		}
		if l.Conversation > 0 {
			h.limits.Conversation = l.Conversation
		}
	}
}

// Host is the standard tool executor. It is safe for concurrent use by
// several windows.
type Host struct {
	ports    Ports
	messages *i18n.Bundle
	locale   string
	limits   Limits
	catalog  []contract.ToolSpec
	specs    map[string]contract.ToolSpec // offered specs by name
}

// toolFunc executes one tool for a bound window.
type toolFunc func(w *windowHost, ctx context.Context, call contract.ToolCall) (contract.ToolResult, error)

// implemented maps tool names to their implementation.
var implemented = map[string]toolFunc{
	"reply":          (*windowHost).reply,
	"stay_quiet":     (*windowHost).stayQuiet,
	"ask_human":      (*windowHost).askHuman,
	"dispatch_task":  (*windowHost).dispatchTask,
	"find_tasks":     (*windowHost).findTasks,
	"read_task":      (*windowHost).readTask,
	"continue_task":  (*windowHost).continueTask,
	"steer_task":     (*windowHost).steerTask,
	"stop_task":      (*windowHost).stopTask,
	"memory_capture": (*windowHost).memoryCapture,
	"memory_lookup":  (*windowHost).memoryLookup,
	"memory_forget":  (*windowHost).memoryForget,
	"read_context":   (*windowHost).readContext,
}

// New returns a Host over the given ports. It panics if the embedded
// standard tool catalog is invalid (a build-time invariant, as with
// contract.StandardCatalog).
func New(ports Ports, opts ...Option) *Host {
	h := &Host{ports: ports, locale: i18n.Fallback, limits: DefaultLimits}
	for _, o := range opts {
		o(h)
	}
	if h.messages == nil {
		h.messages = i18n.Default()
	}
	if h.catalog == nil {
		specs, err := contract.StandardTools()
		if err != nil {
			panic(err)
		}
		h.catalog = specs
	}
	h.specs = map[string]contract.ToolSpec{}
	for _, s := range h.catalog {
		if _, ok := implemented[s.Name]; ok && h.portsReady(s.Ports) {
			h.specs[s.Name] = s
		}
	}
	return h
}

// portsReady reports whether every named port is served. The outbox belongs
// to the loop itself; the executor is owned by the Task Manager, so it is
// served by Ports.Tasks. Any other port (for example
// "config") is a platform capability this package does not provide.
func (h *Host) portsReady(names []string) bool {
	for _, p := range names {
		switch p {
		case "outbox":
		case "journal":
			if h.ports.Journal == nil {
				return false
			}
		case "tasks", "executor":
			if h.ports.Tasks == nil {
				return false
			}
		case "memory":
			if h.ports.Memory == nil {
				return false
			}
		case "conversation":
			if h.ports.Conversation == nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Specs returns the catalog specs this Host implements and whose ports are
// configured, in catalog order. Descriptions are i18n keys; KernelRunner
// resolves them for the wake's locale.
func (h *Host) Specs() []contract.ToolSpec {
	out := make([]contract.ToolSpec, 0, len(h.specs))
	for _, s := range h.catalog {
		if _, ok := h.specs[s.Name]; ok {
			out = append(out, s)
		}
	}
	return out
}

// ForWindow returns the executor bound to one claimed window and tenant.
func (h *Host) ForWindow(win contract.Window, tenant string) contract.Host {
	return &windowHost{h: h, win: win, tenant: tenant}
}

func content(fields map[string]any) string {
	raw, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
