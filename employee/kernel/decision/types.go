package decision

import (
	"context"
	"encoding/json"
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
	// ErrMissingReceipt: a call to an Effect tool, or one whose result
	// decides Dispatched, succeeded without a receipt. It ends the wake and
	// always comes wrapped in ErrToolFailed.
	ErrMissingReceipt   = errors.New("employee loop: effect has no host receipt")
	ErrTerminalConflict = errors.New("employee loop: conflicting terminal tool results")
	// ErrToolRefused marks a Host refusal made before any effect (for example
	// an argument that names no known source). The model may correct the call
	// within the budget; it is not a batch failure.
	ErrToolRefused = errors.New("employee loop: tool call refused before any effect")
)

// Rejections of a model response. The loop repairs each one within the
// budget, so a Host meets them in RepairText and OnModelFailure (and in
// OnBatchRejected when they reject a tool batch: ErrInvalidToolCall,
// ErrInvalidArguments, ErrUnknownTool, ErrExclusiveTool and a static
// ErrTerminalConflict), and joined into Run's budget error when the budget
// runs out. ErrTerminalConflict (above) is one too when it is found before
// any call runs. Classify them with errors.Is: their texts are the v0.2
// texts and carry details, not a stable API. Errors from the Host's own
// hooks (ValidateBatch, ValidateText, ValidateDecision, provider errors) are
// passed on unchanged.
var (
	// ErrIncompleteResponse: the provider returned no message, an empty plain
	// reply or a finish_reason the loop does not accept (for example
	// "length").
	ErrIncompleteResponse = errors.New("employee loop: incomplete model response")
	// ErrInvalidToolCall: a native call without an id or a name, of a type
	// other than "function", or with an id repeated in its response.
	ErrInvalidToolCall = errors.New("employee loop: invalid or duplicate native tool call identity")
	// ErrInvalidArguments: arguments that are not a JSON object or break the
	// tool's Schema (Config.ValidateArguments when set).
	ErrInvalidArguments = errors.New("employee loop: invalid arguments")
	// ErrUnknownTool: a tool the Config does not have, or one TurnTools did
	// not offer to this provider attempt.
	ErrUnknownTool = errors.New("employee loop: unknown tool")
	// ErrExclusiveTool: an Exclusive tool shares its batch.
	ErrExclusiveTool = errors.New("employee loop: exclusive tool shares its batch")
	// ErrTerminalRequired: plain text where RequireTerminalTool needs a native
	// terminal tool call and the text was not taken as final text.
	ErrTerminalRequired = errors.New("employee loop: a native terminal tool call is required")
	// ErrFinalTextRejected: a final text longer than FinalTextLimit.
	ErrFinalTextRejected = errors.New("employee loop: final text rejected")
)

// Failures that end the wake, besides the budget errors and a terminal
// conflict found after the calls ran.
var (
	// ErrNoTerminalDecision: AcceptFinalText returned no terminal decision.
	ErrNoTerminalDecision = errors.New("employee loop: final text produced no terminal decision")
	// ErrToolFailed: a call that may have committed an effect (an Effect tool,
	// or a result with a receipt) failed with an error other than a
	// refusal, or a call lacks a receipt it needs (then ErrMissingReceipt
	// matches too); the wake fails once its batch has run. It wraps the
	// Host's error.
	ErrToolFailed = errors.New("employee loop: tool failed")
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

// Disposition is how a wake ended. The set is closed: Hosts use only the
// four below, as in v0.2, although the loop does not check.
type Disposition string

const (
	Reply Disposition = "reply"
	Quiet Disposition = "quiet"
	// Dispatched is the only disposition with a rule of its own: a result
	// that decides it needs a receipt (the accepted work), even from a tool
	// that is not an Effect, or the wake fails with ErrMissingReceipt.
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
	// PublicArgs names the top-level string arguments that are public text,
	// checked with Config.ValidateText before any call of the batch runs.
	// Nil checks the argument "reply" (the v0.2 rule); an empty, non-nil
	// list checks none.
	PublicArgs []string
}

// publicArgs returns the arguments ValidateText checks.
func (t Tool) publicArgs() []string {
	if t.PublicArgs == nil {
		return []string{"reply"}
	}
	return t.PublicArgs
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
// ctx and deduplicates effects by identity, turn and NativeToolCallID: a
// native id is unique within one response only, and a call's turn is the
// ModelCall.Attempt of the response that carried it (docs/host.md §5). A
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

// FinalTextHost accepts a plain answer as the public reply of a wake that
// otherwise requires a terminal tool (Config.AcceptFinalText).
type FinalTextHost interface {
	AcceptFinalText(context.Context, Identity, string) (ToolResult, error)
}

// FinalTextOrigin marks the tool outcome of an accepted final text. The
// value is Multica's, kept from v0.2 for stored outcomes.
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

// Budget bounds one wake. Successful tool progress (Config.Progress) resets
// the consecutive error count.
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
	// HostData is an opaque, versioned Host projection. It grants no authority
	// and never replaces the native decision, receipts or tool outcomes.
	HostData json.RawMessage `json:"host_data,omitempty"`
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
//
// The per-call hooks (FinalTextAllowed, TurnTools, RepairText,
// ValidateBatch) receive the state they judge: call, the provider attempt
// concerned (from 1, its ModelCall.Attempt), and outcomes, the Host results
// so far in call order (what Outcome.ToolOutcomes holds at that moment;
// read-only).
type Config struct {
	Tools  []Tool
	Budget Budget
	// HostToolOffering delegates the acceptance of registered-but-unlisted
	// tools to ValidateBatch. Unknown tools remain forbidden. A non-nil batch
	// validator is mandatory; the default rejects every unoffered call.
	HostToolOffering bool
	// ResultData creates an optional JSON Host projection after the wake has
	// settled. It is pure and receives read-only outcome/error values. An
	// error fails the wake without discarding its known committed effects.
	ResultData func(Outcome, error) (json.RawMessage, error)
	// PerCallValidation defers argument and unknown/offered-name checks to
	// each call. Such failures become paired tool results; unknown or
	// not-offered tools are never executed. Static batch rules still apply.
	PerCallValidation bool
	// RecordToolOutcome may omit a pre-effect validation result from the
	// outcome journal. Receipts, successful calls and terminal results are
	// always retained. The transcript always contains the native result pair.
	RecordToolOutcome func(ToolCall, ToolResult, error) bool
	// BudgetError supplies an additional frozen Host budget rule. It may
	// replace semantic counter limits, but not MaxPhysicalAttempts. Nil uses
	// the default counter rules. Its error text and chain are preserved.
	BudgetError func(physicalCalls int, usage BudgetUsage) error
	// TransformResponse optionally derives the accepted native response from
	// the provider's original response. It runs after OnModelCall (which sees
	// the original), before ADK or the transcript receives the response. It
	// must return a detached response and keep correlation IDs of retained
	// calls. Every transformed batch still passes all normal validators.
	TransformResponse func(call int, outcomes []ToolOutcome, response *schema.Message) (*schema.Message, error)
	// BeforeTool adds a use-time check before each Host call. Its refusal is
	// paired with that native call and accounted like a Host error; return
	// ErrToolRefused for a proven rejection before any effect.
	BeforeTool func(call int, outcomes []ToolOutcome, tool ToolCall) error
	// CountBatch controls error-streak accounting after a completed batch.
	// False leaves the previous streak unchanged (for a standalone protocol
	// repair). Response and physical budgets are never exempted.
	CountBatch func(call int, calls []ToolCall, outcomes []ToolOutcome, progress bool) bool
	// CheckContextAfterTool preserves committed receipts but prevents a
	// cancelled final tool from turning a cancelled wake into success.
	CheckContextAfterTool bool
	// RawFinalTextLimit checks the untrimmed provider bytes when a Host's
	// frozen text limit was defined before whitespace normalization.
	RawFinalTextLimit bool
	// Progress reports whether a successful call is progress. A batch with
	// progress resets the consecutive error count; a batch without adds one
	// to it, as a batch whose calls all failed does. Nil counts every
	// successful call.
	Progress func(ToolCall) bool
	// RequireTerminalTool makes plain assistant text private: only a
	// Host-accepted terminal decision ends the wake (see AcceptFinalText).
	RequireTerminalTool bool
	// RequireNativeToolCall asks the provider for a tool call without
	// choosing which one (tool_choice "required").
	RequireNativeToolCall bool
	// AcceptFinalText lets a FinalTextHost accept a plain answer of up to
	// FinalTextLimit bytes (default 8000) when RequireTerminalTool is set.
	AcceptFinalText bool
	FinalTextLimit  int
	// FinalTextAllowed reports whether the plain answer of model call `call`
	// (from 1), given the outcomes so far, may go to AcceptFinalText. Nil
	// allows only the first call, before any tool ran. An answer allowed
	// here is checked and accepted as the first one is: FinalTextLimit,
	// ValidateText, then one AcceptFinalText, whose result ends the wake.
	FinalTextAllowed func(call int, outcomes []ToolOutcome) bool
	// TurnTools narrows the tools offered to the next model call (call
	// counts from 1) given the outcomes so far; nil offers every tool. Calls
	// to a tool that was not offered are rejected like unknown tools.
	TurnTools func(call int, outcomes []ToolOutcome) []string
	// Locale selects the built-in repair texts (en, zh-CN).
	Locale string
	// RepairText replaces the built-in instruction appended after the
	// rejected response or provider error of model call `call`.
	RepairText func(call int, outcomes []ToolOutcome, err error) string
	// ValidateBatch and ValidateText add Host rules to the built-in batch
	// validation (unknown tools, exclusivity, terminal conflicts, argument
	// schemas) and to public text (plain replies, final text and the
	// arguments each tool lists in PublicArgs). Their errors reach the hooks
	// and Run unchanged. ValidateBatch is the last check of a batch and a
	// pure one: it sees the call and outcomes TurnTools saw for the response,
	// records nothing (OnBatchAccepted does) and must not modify the calls.
	ValidateBatch func(call int, outcomes []ToolOutcome, calls []ToolCall) error
	ValidateText  func(string) error
	// ValidateDecision checks every terminal decision a Host result carries
	// without an error, from Execute and from AcceptFinalText. A refused
	// decision fails its call: the model sees {"error": ...}, the
	// ToolOutcome's Error holds that text and the call is no progress. Once
	// the batch has run, the wake fails with the error unchanged (not wrapped
	// in ErrToolFailed), joined in call order with the batch's other
	// failures, after a missing receipt of the same call. A refused
	// final-text decision ends the wake at once. Nil checks nothing (v0.2).
	ValidateDecision func(Decision) error
	// ValidateArguments replaces the built-in argument check of every call
	// (the package function ValidateArguments over Tool.Schema), so a Host
	// with its own argument rules rejects exactly the arguments it would
	// reject; nil keeps the built-in check. It runs where that check runs,
	// before ValidateBatch; its problems reject the batch as
	// ErrInvalidArguments. It must not modify the arguments.
	ValidateArguments func(Tool, map[string]any) []string
	// Permanent classifies provider errors that end the wake at once.
	Permanent func(error) bool
	// Hooks observe the wake; they never change it and must not block.
	OnModelCall     func(context.Context, ModelCall)
	OnToolCall      func(context.Context, ToolCall, ToolResult, error, time.Duration)
	OnBatchRejected func([]ToolCall, error)
	OnModelFailure  func(error)
	// OnBatchAccepted receives a native batch once, after it passed every
	// check (the loop's own rules and public text, then ValidateBatch) and
	// before the first Execute of its calls; the wake may still end first if
	// ctx is cancelled. A rejected batch goes to OnBatchRejected instead, and
	// accepted final text is not a batch. A Run that replays a model journal
	// calls it again for every batch it accepts, as the loop keeps nothing
	// between runs. It must not modify the calls, which Execute receives.
	OnBatchAccepted func([]ToolCall)
}
