package scene

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/kernel"
)

// ContextBuilder freezes the data blocks of a window (memory, history,
// resources). Blocks are data; they never grant authority.
type ContextBuilder func(ctx context.Context, win contract.Window) ([]contract.ContextBlock, error)

// KernelRunner runs a window as one kernel wake with injected model, Host
// and tools. Tools are filtered by the window's lane and session, and their
// descriptions and the session role are rendered in the wake's locale.
type KernelRunner struct {
	Model model.ToolCallingChatModel
	// Host executes tools for every window. Simple embedders set it; hosts
	// that bind per-window state (source refs, read refs) set Hosts instead.
	Host contract.Host
	// Hosts, when set, returns the Host bound to one claimed window and is
	// used instead of Host.
	Hosts func(win contract.Window) contract.Host
	// Journal, when set, makes re-claimed windows replay committed model
	// turns and tool effects instead of repeating them, and freezes the wake
	// input at the first claim: a re-claim reuses the recorded first request
	// and does not call Context again. A journal mismatch holds the window
	// with HoldJournalConflict.
	Journal contract.Journal
	Tools   []contract.ToolSpec
	// Persona is the Host's trusted employee instructions.
	Persona string
	Context ContextBuilder
	// Budgets per lane; missing lanes use contract.DefaultBudget.
	Budgets map[contract.Lane]contract.Budget
	// ForegroundRequiresTerminal makes plain text private in the foreground
	// too. Background sessions always require a terminal decision.
	ForegroundRequiresTerminal bool
	Messages                   *i18n.Bundle
	// Locale is the default language; an event's own Locale wins.
	Locale    string
	Tenant    string
	Validator contract.BatchValidator
	TextGuard contract.TextGuard
	FinalText contract.FinalTextHost
	Permanent func(error) bool
	Observer  kernel.Observer
}

// RunWindow implements Runner.
func (k *KernelRunner) RunWindow(ctx context.Context, win contract.Window) (contract.Outcome, error) {
	if len(win.Items) == 0 {
		return contract.Outcome{}, HoldError{Reason: "empty_window"}
	}
	if win.Lease == nil {
		return contract.Outcome{}, errors.New("scene: window has no lease")
	}
	messages := k.Messages
	if messages == nil {
		messages = i18n.Default()
	}
	last := win.Items[len(win.Items)-1].Event
	locale := k.Locale
	if last.Locale != "" {
		locale = last.Locale
	}
	tools := messages.ToolDescriptions(locale, contract.ToolsFor(k.Tools, win.Lane, win.Session.Kind))
	var journal *kernel.WakeJournal
	var frozen []*schema.Message
	if k.Journal != nil {
		journal = kernel.NewWakeJournal(k.Journal, *win.Lease)
		var err error
		if frozen, err = journal.FrozenTranscript(ctx); err != nil {
			return contract.Outcome{}, err
		}
	}
	var blocks []contract.ContextBlock
	if k.Context != nil && frozen == nil {
		var err error
		if blocks, err = k.Context(ctx, win); err != nil {
			return contract.Outcome{}, err
		}
	}
	parts := []string{}
	if p := strings.TrimSpace(k.Persona); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, messages.Text(locale, "scene.session."+string(win.Session.Kind)))
	budget, ok := k.Budgets[win.Lane]
	if !ok {
		budget = contract.DefaultBudget()
	}
	m, host := k.Model, k.Host
	if k.Hosts != nil {
		host = k.Hosts(win)
	}
	if host == nil {
		return contract.Outcome{}, errors.New("scene: no host for window")
	}
	if journal != nil {
		m, host = journal.Model(m), journal.Host(host)
	}
	id := contract.Identity{
		Tenant: k.Tenant, Employee: win.Employee, Scene: last.Scene, Session: win.Session,
		ReceiptID: win.Items[0].ReceiptID, WindowID: win.ID,
	}
	res, err := kernel.Run(ctx, kernel.Options{
		Model: m, Host: host, Tools: tools, Budget: budget,
		RequireTerminal: win.Lane != contract.LaneForeground || k.ForegroundRequiresTerminal,
		Validator:       k.Validator, TextGuard: k.TextGuard, FinalText: k.FinalText,
		Permanent: k.Permanent, Messages: messages, Locale: locale, Observer: k.Observer,
	}, kernel.Wake{Identity: id, Window: &win, Instructions: strings.Join(parts, "\n\n"), Blocks: blocks, Transcript: frozen})
	if errors.Is(err, kernel.ErrJournal) && errors.Is(err, contract.ErrConflict) {
		// Retrying cannot help: the recorded wake no longer matches.
		return res.Outcome, HoldError{Reason: HoldJournalConflict}
	}
	return res.Outcome, err
}

// HoldJournalConflict is the hold reason of a window whose re-claim no
// longer matches its journal.
const HoldJournalConflict = "journal_conflict"
