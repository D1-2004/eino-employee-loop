package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolKind classifies the side effect of a tool.
type ToolKind string

const (
	// ToolRead returns data and changes nothing.
	ToolRead ToolKind = "read"
	// ToolEffect commits a durable side effect and must return a receipt.
	ToolEffect ToolKind = "effect"
	// ToolTerminal ends the wake with a disposition (reply, quiet, ask).
	ToolTerminal ToolKind = "terminal"
)

// Disposition is how a wake ended.
type Disposition string

const (
	Reply      Disposition = "reply"
	Quiet      Disposition = "quiet"
	Dispatched Disposition = "dispatched"
	Waiting    Disposition = "waiting"
)

// ToolSpec is the Host-supplied contract of one model-visible tool. The
// loop never hardcodes a vendor tool: every tool enters through a ToolSpec
// and is executed by the Host.
type ToolSpec struct {
	Name string   `json:"name"`
	Kind ToolKind `json:"kind"`
	// Description is the model-facing text. Catalog specs leave it empty and
	// set DescriptionKey; hosts resolve it through i18n before a wake.
	Description    string `json:"description,omitempty"`
	DescriptionKey string `json:"description_key,omitempty"`
	// Parameters is a JSON Schema object.
	Parameters map[string]any `json:"parameters"`
	// Terminal is the fixed disposition of a terminal tool. Effect tools
	// may also return a terminal decision at run time (dispatch).
	Terminal Disposition `json:"terminal,omitempty"`
	// Exclusive tools must be the only call in a batch.
	Exclusive bool `json:"exclusive,omitempty"`
	// Lanes and Sessions restrict where the tool may be offered.
	Lanes    []Lane        `json:"lanes,omitempty"`
	Sessions []SessionKind `json:"sessions,omitempty"`
	// Ports names the storage ports that back the tool (documentation and
	// assembly checks): "tasks", "memory", "conversation", "outbox", ...
	Ports []string `json:"ports,omitempty"`
	// Timeout bounds one execution. Zero uses the kernel default for Kind.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Effect reports whether a successful call must carry a durable receipt.
func (t ToolSpec) Effect() bool { return t.Kind == ToolEffect }

// OfferedIn reports whether the tool may be offered in a lane/session.
func (t ToolSpec) OfferedIn(lane Lane, session SessionKind) bool {
	if len(t.Lanes) > 0 && !containsLane(t.Lanes, lane) {
		return false
	}
	if len(t.Sessions) > 0 && !containsSession(t.Sessions, session) {
		return false
	}
	return true
}

func containsLane(xs []Lane, x Lane) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsSession(xs []SessionKind, x SessionKind) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Validate checks the spec shape.
func (t ToolSpec) Validate() error {
	var problems []string
	if strings.TrimSpace(t.Name) == "" {
		problems = append(problems, "name is required")
	}
	switch t.Kind {
	case ToolRead, ToolEffect:
	case ToolTerminal:
		if t.Terminal == "" {
			problems = append(problems, "terminal tools declare their disposition")
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown kind %q", t.Kind))
	}
	if t.Terminal != "" && !validDisposition(t.Terminal) {
		problems = append(problems, fmt.Sprintf("unknown disposition %q", t.Terminal))
	}
	if t.Parameters == nil {
		problems = append(problems, "parameters schema is required")
	}
	if t.Description == "" && t.DescriptionKey == "" {
		problems = append(problems, "description or description_key is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("contract: tool %q: %s", t.Name, strings.Join(problems, "; "))
	}
	return nil
}

func validDisposition(d Disposition) bool {
	switch d {
	case Reply, Quiet, Dispatched, Waiting:
		return true
	}
	return false
}

// HostCallPrefix is reserved for Host-created journal keys. Provider tool
// call ids using it are rejected.
const HostCallPrefix = "host:"

// ToolCall is one native provider tool call. NativeID is the provider's
// correlation id. Providers may reuse native ids across model turns, so a
// call is identified within a wake by (Turn, NativeID); together with the
// window they form the Host's idempotency key.
type ToolCall struct {
	NativeID  string         `json:"native_id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// Turn is the zero-based model turn of the wake that produced the call.
	Turn int `json:"turn,omitempty"`
	// Origin marks calls the kernel synthesized (for example a final-text
	// adapter); empty for provider calls.
	Origin string `json:"origin,omitempty"`
}

// Decision is a terminal disposition, optionally with a public reply.
type Decision struct {
	Kind  Disposition `json:"kind,omitempty"`
	Reply string      `json:"reply,omitempty"`
}

// Validate checks a Host-returned terminal decision.
func (d Decision) Validate() error {
	switch d.Kind {
	case Reply:
		if strings.TrimSpace(d.Reply) == "" {
			return errors.New("contract: empty terminal reply")
		}
	case Quiet, Dispatched, Waiting:
	default:
		return fmt.Errorf("contract: invalid disposition %q", d.Kind)
	}
	return nil
}

// ToolResult is the Host's answer to one call. Content is returned to the
// model; Receipt proves a committed effect; Terminal ends the wake.
type ToolResult struct {
	Content  string    `json:"content"`
	Receipt  string    `json:"receipt,omitempty"`
	Terminal *Decision `json:"terminal,omitempty"`
}

// Receipt pairs a committed effect with the call that produced it.
type Receipt struct {
	NativeID string `json:"native_id"`
	Tool     string `json:"tool"`
	ID       string `json:"id"`
}

// ToolOutcome preserves every Host result in call order, including effects
// committed alongside an error.
type ToolOutcome struct {
	NativeID string     `json:"native_id"`
	Tool     string     `json:"tool"`
	Origin   string     `json:"origin,omitempty"`
	Result   ToolResult `json:"result"`
	Error    string     `json:"error,omitempty"`
}

// Budget bounds one wake. A model response that makes progress resets the
// consecutive-error count; attempts count every provider request.
type Budget struct {
	MaxResponses         int `json:"max_responses"`
	MaxConsecutiveErrors int `json:"max_consecutive_errors"`
	MaxAttempts          int `json:"max_attempts"`
}

// DefaultBudget allows three provider requests in total.
func DefaultBudget() Budget { return Budget{MaxResponses: 3, MaxConsecutiveErrors: 3, MaxAttempts: 3} }

// Validate checks budget limits.
func (b Budget) Validate() error {
	if b.MaxResponses <= 0 || b.MaxConsecutiveErrors <= 0 || b.MaxAttempts <= 0 {
		return errors.New("contract: budget limits must be positive")
	}
	return nil
}

// Usage counts what a wake consumed.
type Usage struct {
	Attempts          int `json:"attempts"`
	Responses         int `json:"responses"`
	ConsecutiveErrors int `json:"consecutive_errors"`
	PromptTokens      int `json:"prompt_tokens,omitempty"`
	CompletionTokens  int `json:"completion_tokens,omitempty"`
}

// Outcome is the result of one wake. It is not a delivery proof: replies
// are delivered by the Host's outbox.
type Outcome struct {
	Decision     Decision      `json:"decision"`
	Usage        Usage         `json:"usage"`
	Receipts     []Receipt     `json:"receipts,omitempty"`
	ToolOutcomes []ToolOutcome `json:"tool_outcomes,omitempty"`
}

// Errors shared by kernels and hosts.
var (
	// ErrToolRefused marks a Host refusal before any effect. The model may
	// correct the call within the remaining budget.
	ErrToolRefused = errors.New("contract: tool call refused before any effect")
	// ErrMissingReceipt marks an effect that returned no durable receipt.
	ErrMissingReceipt = errors.New("contract: effect has no host receipt")
	// ErrTerminalConflict marks a batch with incompatible dispositions.
	ErrTerminalConflict = errors.New("contract: conflicting terminal tool results")
	// ErrBudget marks an exhausted wake budget.
	ErrBudget = errors.New("contract: wake budget exhausted")
	// ErrNoDisposition marks a wake that ended without a terminal decision.
	ErrNoDisposition = errors.New("contract: wake ended without a terminal disposition")
)

// Identity is supplied by the admission Host and never decoded from model or
// user data. Hosts enforce tenant fences and permissions using it.
type Identity struct {
	Tenant    string     `json:"tenant"`
	Employee  string     `json:"employee"`
	Scene     SceneRef   `json:"scene"`
	Session   SessionRef `json:"session"`
	ReceiptID string     `json:"receipt_id,omitempty"`
	WindowID  string     `json:"window_id,omitempty"`
}

// Host owns reads, permissions, durable state and irreversible effects.
// Execute must honour ctx and deduplicate effects by identity plus call id.
// A committed effect returns its receipt even when ctx is cancelled right
// after the commit.
type Host interface {
	Execute(ctx context.Context, id Identity, call ToolCall) (ToolResult, error)
}

// HostFunc adapts a function to Host.
type HostFunc func(ctx context.Context, id Identity, call ToolCall) (ToolResult, error)

// Execute implements Host.
func (f HostFunc) Execute(ctx context.Context, id Identity, call ToolCall) (ToolResult, error) {
	return f(ctx, id, call)
}

// FinalTextHost optionally accepts a provider's plain final text as the
// wake's reply when the wake requires a terminal tool. It must validate the
// text through the same path as the reply tool.
type FinalTextHost interface {
	AcceptFinalText(ctx context.Context, id Identity, text string) (ToolResult, error)
}

// BatchValidator optionally validates a whole native batch before any call
// executes (exclusivity, schema, public-text rules). Returning an error
// rejects the batch without effects and costs one model response.
type BatchValidator interface {
	ValidateBatch(calls []ToolCall) error
}

// TextGuard optionally rejects public text (plain replies) before it is
// accepted, for example text that encodes tool calls.
type TextGuard func(text string) error
