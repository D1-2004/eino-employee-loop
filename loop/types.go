package loop

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Errors returned by Run. Budget errors are joined with ErrModelBudget.
var (
	ErrModelBudget            = errors.New("employee loop: model budget exhausted")
	ErrConsecutiveModelErrors = errors.New("employee loop: consecutive model errors exhausted")
	ErrModelResponseBudget    = errors.New("employee loop: valid model response budget exhausted")
	ErrPhysicalModelBudget    = errors.New("employee loop: provider attempt budget exhausted")
	ErrMissingReceipt         = errors.New("employee loop: effect has no host receipt")
	ErrTerminalConflict       = errors.New("employee loop: conflicting terminal tool results")
	// ErrToolRefused marks a Host refusal made before any effect (for example
	// an argument that names no known source). The model may correct the call
	// within the budget; it is not a batch failure.
	ErrToolRefused = errors.New("employee loop: tool call refused before any effect")
)

// Identity is supplied by the Host from trusted admission data and passed
// back to every Host call. The loop never reads or changes it.
type Identity struct {
	Tenant   string
	Employee string
	Scene    string
	Receipt  string
}

// Wake is the frozen input of one wake: the identity and the messages the
// Host assembled (system prompt first, then context and the current window).
type Wake struct {
	Identity Identity
	Messages []*schema.Message
}

// Disposition is how a wake ended.
type Disposition string

const (
	Reply      Disposition = "reply"
	Quiet      Disposition = "quiet"
	Dispatched Disposition = "dispatched"
	Waiting    Disposition = "waiting"
)

// Decision is a Host-accepted terminal disposition, or a plain text reply
// when the wake does not require a terminal tool. Reply may accompany
// Dispatched and Waiting.
type Decision struct {
	Kind  Disposition
	Reply string
}

// Tool is a capability supplied by the Host.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the arguments, sent to the model and used
	// for validation before any call runs.
	Schema map[string]any
	// Effect tools change the world; a successful call must return a Host
	// receipt.
	Effect bool
	// Terminal declares the disposition the tool always ends with, so a
	// batch mixing dispositions is rejected before any effect.
	Terminal Disposition
	// Exclusive tools must be the only call of their batch.
	Exclusive bool
	// Timeout bounds one call; zero leaves it to the wake context.
	Timeout time.Duration
}

// ToolCall is one native call of a model response.
type ToolCall struct {
	// Origin marks calls the loop made on the Host's behalf (FinalTextOrigin).
	Origin           string         `json:"origin,omitempty"`
	NativeToolCallID string         `json:"native_tool_call_id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
}

// ToolResult is the Host's answer to a call. Content is what the model sees.
type ToolResult struct {
	Content  string
	Receipt  string
	Terminal *Decision
}

// Host owns reads, permissions, durable state and effects. Execute honours
// ctx and deduplicates effects by identity plus NativeToolCallID; a
// committed effect returns its receipt even when ctx was cancelled after the
// commit. The loop never invents a receipt.
type Host interface {
	Execute(context.Context, Identity, ToolCall) (ToolResult, error)
}

// HostFunc adapts a function to Host.
type HostFunc func(context.Context, Identity, ToolCall) (ToolResult, error)

// Execute implements Host.
func (f HostFunc) Execute(ctx context.Context, id Identity, call ToolCall) (ToolResult, error) {
	return f(ctx, id, call)
}

// FinalTextHost accepts a first plain answer as the public reply of a wake
// that otherwise requires a terminal tool (Config.AcceptFinalText).
type FinalTextHost interface {
	AcceptFinalText(context.Context, Identity, string) (ToolResult, error)
}

// FinalTextOrigin marks the tool outcome of an accepted final text.
const FinalTextOrigin = "host_final_text"

// Receipt is the durable proof of one effect.
type Receipt struct {
	NativeToolCallID string
	ToolName         string
	ID               string
}

// ToolOutcome is one Host result in call order, including committed effects
// returned with an error. It does not claim the batch completed.
type ToolOutcome struct {
	Origin           string
	NativeToolCallID string
	ToolName         string
	Result           ToolResult
	Error            string
}

// Budget bounds one wake. Successful tool progress resets the consecutive
// error count.
type Budget struct {
	MaxModelResponses    int
	MaxConsecutiveErrors int
	MaxPhysicalAttempts  int
}

// DefaultBudget allows three of each.
func DefaultBudget() Budget {
	return Budget{MaxModelResponses: 3, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 3}
}

// BudgetUsage counts accepted responses and the current error streak.
type BudgetUsage struct {
	ModelResponses    int
	ConsecutiveErrors int
}

// Usage sums the provider-reported tokens of the wake.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Outcome is the result of one wake. Callers check Run's error as well:
// ToolOutcomes may hold committed effects of a failed wake.
type Outcome struct {
	Decision
	// ModelCalls counts provider requests (physical attempts).
	ModelCalls   int
	Receipts     []Receipt
	ToolOutcomes []ToolOutcome
	BudgetUsage  BudgetUsage
	Usage        Usage
	// Transcript is the accepted conversation: the wake's messages, accepted
	// responses and their tool results. Rejected output is never kept.
	Transcript []*schema.Message
}

// ModelCall describes one provider request for observers.
type ModelCall struct {
	Attempt  int
	Request  []*schema.Message
	Response *schema.Message
	Err      error
	Elapsed  time.Duration
}

// Config configures a loop. Everything Host-specific is injected here.
type Config struct {
	Tools  []Tool
	Budget Budget
	// RequireTerminalTool makes plain assistant text private: only a
	// Host-accepted terminal decision ends the wake (see AcceptFinalText).
	RequireTerminalTool bool
	// RequireNativeToolCall asks the provider for a tool call without
	// choosing which one (tool_choice "required").
	RequireNativeToolCall bool
	// AcceptFinalText lets a FinalTextHost accept a first plain answer of up
	// to FinalTextLimit bytes (default 8000) when RequireTerminalTool is set.
	AcceptFinalText bool
	FinalTextLimit  int
	// TurnTools narrows the tools offered to the next model call (call
	// counts from 1) given the outcomes so far; nil offers every tool. Calls
	// to a tool that was not offered are rejected like unknown tools.
	TurnTools func(call int, outcomes []ToolOutcome) []string
	// Locale selects the built-in repair texts (en, zh-CN).
	Locale string
	// RepairText replaces the built-in instruction appended after a rejected
	// response or provider error.
	RepairText func(err error) string
	// ValidateBatch and ValidateText add Host rules to the built-in batch
	// validation (unknown tools, exclusivity, terminal conflicts, argument
	// schemas) and to public text (plain replies and "reply" arguments).
	// ValidateBatch must not modify the calls it receives.
	ValidateBatch func([]ToolCall) error
	ValidateText  func(string) error
	// Permanent classifies provider errors that end the wake at once.
	Permanent func(error) bool
	// Hooks observe the wake; they never change it and must not block.
	OnModelCall     func(context.Context, ModelCall)
	OnToolCall      func(context.Context, ToolCall, ToolResult, error, time.Duration)
	OnBatchRejected func([]ToolCall, error)
	OnModelFailure  func(error)
}
