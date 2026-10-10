package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// ErrJournal marks a journal read or write failure or a replay mismatch.
// It ends the wake at once: after it the kernel neither asks the model again
// nor executes further calls. A mismatch also matches contract.ErrConflict.
var ErrJournal = errors.New("kernel: journal")

func journalErr(err error) error { return fmt.Errorf("%w: %w", ErrJournal, err) }

// WakeJournal records one window's wake in a contract.Journal and replays it
// when the window is re-claimed. Wrap the wake's model with Model and its
// Host with Host; both share the journal state of the wake. Create one per
// claim (lease).
//
// Replay rules:
//   - Model turns replay in order and must have byte-identical request JSON;
//     a different request is ErrJournal + contract.ErrConflict, never a
//     silent re-ask.
//   - Tool results replay by (turn, native id) and must have the same tool
//     name and arguments. A recorded error replays with its exact text and
//     its refusal class.
//   - Successes, refusals and errors that carry a receipt are recorded at
//     once. Provider failures and other tool errors are transient: they are
//     recorded only when a later model turn is recorded (the model has seen
//     them, so a replay must show them again). A wake that ends on a
//     transient failure leaves it unrecorded, and the re-claim asks the
//     model or executes the call again under the same idempotency key.
//   - FrozenTranscript returns the first recorded request, so a re-claim
//     replays the wake input frozen at the first claim instead of re-reading
//     live context.
type WakeJournal struct {
	journal contract.Journal
	lease   contract.Lease

	mu           sync.Mutex
	loaded       bool
	turns        []contract.ModelTurn
	tools        map[toolKey]contract.ToolRecord
	next         int
	pendingTurns []contract.ModelTurn
	pendingTools []contract.ToolRecord
}

type toolKey struct {
	turn int
	id   string
}

// NewWakeJournal returns the journal of one claimed window.
func NewWakeJournal(j contract.Journal, lease contract.Lease) *WakeJournal {
	return &WakeJournal{journal: j, lease: lease, tools: map[toolKey]contract.ToolRecord{}}
}

func (w *WakeJournal) loadLocked(ctx context.Context) error {
	if w.loaded {
		return nil
	}
	turns, err := w.journal.ModelTurns(ctx, w.lease)
	if err != nil {
		return journalErr(err)
	}
	recs, err := w.journal.Tools(ctx, w.lease)
	if err != nil {
		return journalErr(err)
	}
	w.turns = turns
	for _, rec := range recs {
		w.tools[toolKey{rec.Turn, rec.NativeID}] = rec
	}
	w.loaded = true
	return nil
}

// FrozenTranscript returns the wake input recorded by the first claim (the
// request of model turn 0), or nil when no turn is recorded yet. Pass it as
// Wake.Transcript instead of rebuilding context from live state.
func (w *WakeJournal) FrozenTranscript(ctx context.Context) ([]*schema.Message, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadLocked(ctx); err != nil {
		return nil, err
	}
	if len(w.turns) == 0 {
		return nil, nil
	}
	var msgs []*schema.Message
	if err := json.Unmarshal(w.turns[0].Request, &msgs); err != nil {
		return nil, journalErr(err)
	}
	return msgs, nil
}

// Model wraps the wake's chat model.
func (w *WakeJournal) Model(m model.ToolCallingChatModel) model.ToolCallingChatModel {
	return &journalModel{w: w, inner: m}
}

// Host wraps the wake's Host.
func (w *WakeJournal) Host(h contract.Host) contract.Host { return journalHost{w: w, inner: h} }

// flushLocked records the transient failures the model has now seen.
func (w *WakeJournal) flushLocked(ctx context.Context) error {
	for len(w.pendingTurns) > 0 {
		turn := w.pendingTurns[0]
		if err := w.journal.SaveModelTurn(ctx, w.lease, turn); err != nil {
			return journalErr(err)
		}
		w.turns = append(w.turns, turn)
		w.pendingTurns = w.pendingTurns[1:]
	}
	for len(w.pendingTools) > 0 {
		rec := w.pendingTools[0]
		if err := w.journal.SaveTool(ctx, w.lease, rec); err != nil {
			return journalErr(err)
		}
		w.tools[toolKey{rec.Turn, rec.NativeID}] = rec
		w.pendingTools = w.pendingTools[1:]
	}
	return nil
}

func (w *WakeJournal) generate(ctx context.Context, inner model.ToolCallingChatModel, in []*schema.Message, opts []model.Option) (*schema.Message, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadLocked(ctx); err != nil {
		return nil, err
	}
	req, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	seq := w.next
	w.next++
	if seq < len(w.turns) {
		turn := w.turns[seq]
		if !bytes.Equal(turn.Request, req) {
			return nil, fmt.Errorf("%w: %w: model turn %d request changed", ErrJournal, contract.ErrConflict, seq)
		}
		if turn.Failure != "" {
			return nil, errors.New(turn.Failure)
		}
		var msg schema.Message
		if err := json.Unmarshal(turn.Response, &msg); err != nil {
			return nil, journalErr(err)
		}
		return &msg, nil
	}
	resp, callErr := inner.Generate(ctx, in, opts...)
	if callErr != nil {
		if ctx.Err() == nil {
			w.pendingTurns = append(w.pendingTurns, contract.ModelTurn{Seq: seq, Request: req, Failure: callErr.Error()})
		}
		return nil, callErr
	}
	turn := contract.ModelTurn{Seq: seq, Request: req}
	if turn.Response, err = json.Marshal(resp); err != nil {
		return nil, err
	}
	if err := w.flushLocked(context.WithoutCancel(ctx)); err != nil {
		return nil, err
	}
	if err := w.journal.SaveModelTurn(ctx, w.lease, turn); err != nil {
		return nil, journalErr(err)
	}
	w.turns = append(w.turns, turn)
	return resp, nil
}

func (w *WakeJournal) execute(ctx context.Context, host contract.Host, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	key := toolKey{call.Turn, call.NativeID}
	w.mu.Lock()
	err := w.loadLocked(ctx)
	rec, recorded := w.tools[key]
	w.mu.Unlock()
	if err != nil {
		return contract.ToolResult{}, err
	}
	if recorded {
		if rec.Call.Name != call.Name || !sameArguments(rec.Call.Arguments, call.Arguments) {
			return contract.ToolResult{}, fmt.Errorf("%w: %w: tool call %d/%s replayed as %s with other arguments",
				ErrJournal, contract.ErrConflict, call.Turn, call.NativeID, call.Name)
		}
		if rec.Error != "" {
			return rec.Result, recordedError{msg: rec.Error, refused: rec.Refused}
		}
		return rec.Result, nil
	}
	result, callErr := host.Execute(ctx, id, call)
	rec = contract.ToolRecord{Turn: call.Turn, NativeID: call.NativeID, Call: call, Result: result}
	if callErr != nil {
		rec.Error = callErr.Error()
		rec.Refused = errors.Is(callErr, contract.ErrToolRefused)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if callErr != nil && !rec.Refused && strings.TrimSpace(result.Receipt) == "" {
		w.pendingTools = append(w.pendingTools, rec)
		return result, callErr
	}
	if err := w.journal.SaveTool(context.WithoutCancel(ctx), w.lease, rec); err != nil {
		return result, errors.Join(callErr, journalErr(err))
	}
	w.tools[key] = rec
	return result, callErr
}

func sameArguments(a, b map[string]any) bool {
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ra, rb)
}

// recordedError replays a recorded tool error with its exact text and its
// refusal class.
type recordedError struct {
	msg     string
	refused bool
}

func (e recordedError) Error() string { return e.msg }

// Is makes a replayed refusal match contract.ErrToolRefused.
func (e recordedError) Is(target error) bool { return e.refused && target == contract.ErrToolRefused }

type journalModel struct {
	w     *WakeJournal
	inner model.ToolCallingChatModel
}

// WithTools implements model.ToolCallingChatModel; the bound model shares
// the wake's journal.
func (m *journalModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &journalModel{w: m.w, inner: inner}, nil
}

// Generate implements model.BaseChatModel.
func (m *journalModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.w.generate(ctx, m.inner, in, opts)
}

// Stream is not journaled; the kernel never streams.
func (m *journalModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("kernel: journaled model does not stream")
}

type journalHost struct {
	w     *WakeJournal
	inner contract.Host
}

// Execute implements contract.Host.
func (h journalHost) Execute(ctx context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	return h.w.execute(ctx, h.inner, id, call)
}
