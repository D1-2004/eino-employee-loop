package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
)

var lease = contract.Lease{WindowID: "w1", Token: "t", Generation: 1}

// claim runs one wake through a fresh WakeJournal, as a new claim of the
// same window would.
func claim(t *testing.T, j contract.Journal, m *kt.Model, h contract.Host, w kernel.Wake) (kernel.Result, error) {
	t.Helper()
	wj := kernel.NewWakeJournal(j, lease)
	frozen, err := wj.FrozenTranscript(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frozen != nil {
		w.Transcript = frozen
	}
	o := kernel.Options{Model: wj.Model(m), Host: wj.Host(h), Tools: standardTools(t, contract.LaneForeground, contract.SessionInteractive), Locale: "en"}
	return kernel.Run(context.Background(), o, w)
}

// flakyHost fails a tool with a transient (non-refusal, receipt-less)
// error a set number of times, then delegates.
type flakyHost struct {
	fakeHost
	failures map[string]int
}

func (h *flakyHost) Execute(ctx context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	if h.failures[call.Name] > 0 {
		h.failures[call.Name]--
		h.calls = append(h.calls, call)
		return contract.ToolResult{}, errors.New("task manager unavailable")
	}
	return h.fakeHost.Execute(ctx, id, call)
}

func TestJournalReplaysRefusalVerbatim(t *testing.T) {
	j := kt.NewJournal()
	h := &fakeHost{refuse: map[string]bool{"read_task": true}}
	first, err := claim(t, j, kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "bad", "task_ref": "t1"}}),
		kt.Tools(kt.Call{ID: "c2", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "Not found."}}),
	), h, wake())
	if err != nil || first.Decision.Reply != "Not found." {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	if len(j.Records("w1")) != 2 || !j.Records("w1")[0].Refused || j.Records("w1")[0].Error != first.ToolOutcomes[0].Error {
		t.Fatalf("the refusal is recorded with its flag and exact text: %+v", j.Records("w1"))
	}
	fresh := kt.New()
	again, err := claim(t, j, fresh, h, wake())
	if err != nil || again.Decision.Reply != "Not found." || fresh.Calls() != 0 || len(h.calls) != 2 {
		t.Fatalf("replay: %+v %v calls=%d host=%d", again, err, fresh.Calls(), len(h.calls))
	}
	if again.ToolOutcomes[0].Error != first.ToolOutcomes[0].Error {
		t.Fatalf("replayed text %q, original %q", again.ToolOutcomes[0].Error, first.ToolOutcomes[0].Error)
	}
	if strings.Count(again.ToolOutcomes[0].Error, contract.ErrToolRefused.Error()) != 1 {
		t.Fatalf("the refusal prefix is not doubled: %q", again.ToolOutcomes[0].Error)
	}
}

func TestJournalReplayedRefusalKeepsItsClass(t *testing.T) {
	j := kt.NewJournal()
	j.Seed("w1", contract.ToolRecord{NativeID: "c1", Call: contract.ToolCall{NativeID: "c1", Name: "stay_quiet", Arguments: map[string]any{}},
		Error: "refused: no", Refused: true})
	wj := kernel.NewWakeJournal(j, lease)
	_, err := wj.Host(&fakeHost{}).Execute(context.Background(), contract.Identity{}, contract.ToolCall{NativeID: "c1", Name: "stay_quiet", Arguments: map[string]any{}})
	if !errors.Is(err, contract.ErrToolRefused) || err.Error() != "refused: no" {
		t.Fatalf("replayed refusal: %v", err)
	}
}

func TestJournalLeavesTransientToolFailuresForTheReclaim(t *testing.T) {
	j := kt.NewJournal()
	h := &flakyHost{failures: map[string]int{"dispatch_task": 1}}
	dispatch := kt.Tools(kt.Call{ID: "c1", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p", "reply": "On it."}})
	if _, err := claim(t, j, kt.New(dispatch), h, wake()); err == nil || !strings.Contains(err.Error(), "task manager unavailable") {
		t.Fatalf("a failed effect fails the wake: %v", err)
	}
	if len(j.Records("w1")) != 0 || len(j.Turns("w1")) != 1 {
		t.Fatalf("the transient failure is not recorded: %+v", j.Records("w1"))
	}
	fresh := kt.New()
	res, err := claim(t, j, fresh, h, wake())
	if err != nil || res.Decision.Kind != contract.Dispatched || fresh.Calls() != 0 || len(h.calls) != 2 {
		t.Fatalf("re-claim re-executes: %+v %v model=%d host=%d", res, err, fresh.Calls(), len(h.calls))
	}
	if a, b := h.calls[0], h.calls[1]; a.Turn != b.Turn || a.NativeID != b.NativeID {
		t.Fatalf("same call identity, so the same idempotency key: %+v %+v", a, b)
	}
}

func TestJournalRecordsTransientFailuresTheModelSaw(t *testing.T) {
	j := kt.NewJournal()
	h := &flakyHost{failures: map[string]int{"read_task": 1, "dispatch_task": 1}}
	_, err := claim(t, j, kt.New(
		kt.Fail(errors.New("provider 503")),
		kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "t1"}}),
		kt.Tools(kt.Call{ID: "c2", Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p", "reply": "On it."}}),
	), h, wake())
	if err == nil {
		t.Fatal("the failed dispatch fails the wake")
	}
	if len(j.Turns("w1")) != 3 || j.Turns("w1")[0].Failure == "" || len(j.Records("w1")) != 1 || j.Records("w1")[0].Call.Name != "read_task" {
		t.Fatalf("seen failures recorded, the trailing one not: turns=%d tools=%+v", len(j.Turns("w1")), j.Records("w1"))
	}
	fresh := kt.New()
	res, err := claim(t, j, fresh, h, wake())
	if err != nil || res.Decision.Kind != contract.Dispatched || fresh.Calls() != 0 {
		t.Fatalf("replay then re-execute: %+v %v model=%d", res, err, fresh.Calls())
	}
}

func TestJournalLeavesTrailingProviderFailuresForTheReclaim(t *testing.T) {
	j := kt.NewJournal()
	down := errors.New("provider 503")
	if _, err := claim(t, j, kt.New(kt.Fail(down), kt.Fail(down), kt.Fail(down)), &fakeHost{}, wake()); !errors.Is(err, contract.ErrBudget) {
		t.Fatalf("budget: %v", err)
	}
	if len(j.Turns("w1")) != 0 {
		t.Fatalf("trailing provider failures are not recorded: %d", len(j.Turns("w1")))
	}
	up := kt.New(kt.Text("hello"))
	if res, err := claim(t, j, up, &fakeHost{}, wake()); err != nil || res.Decision.Reply != "hello" || up.Calls() != 1 {
		t.Fatalf("re-claim asks again: %+v %v", res, err)
	}
}

func TestJournalKeysCallsByTurnAndNativeID(t *testing.T) {
	j := kt.NewJournal()
	h := &readHost{}
	m := kt.New(
		kt.Tools(kt.Call{ID: "call_0", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "A"}}),
		kt.Tools(kt.Call{ID: "call_0", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "B"}}),
		kt.Tools(kt.Call{ID: "call_0", Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "done"}}),
	)
	res, err := claim(t, j, m, h, wake())
	if err != nil || res.Decision.Reply != "done" || strings.Join(h.read, ",") != "A,B" {
		t.Fatalf("reused native ids execute per turn: %+v %v reads=%v", res, err, h.read)
	}
	if res.ToolOutcomes[1].Result.Content != `{"task_ref":"B"}` {
		t.Fatalf("turn 1 gets its own result: %+v", res.ToolOutcomes[1])
	}
	if res2, err := claim(t, j, kt.New(), h, wake()); err != nil || res2.Decision.Reply != "done" || len(h.read) != 2 {
		t.Fatalf("replay: %+v %v reads=%v", res2, err, h.read)
	}
}

type readHost struct{ read []string }

func (h *readHost) Execute(_ context.Context, _ contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	switch call.Name {
	case "read_task":
		ref := call.Arguments["task_ref"].(string)
		h.read = append(h.read, ref)
		return contract.ToolResult{Content: `{"task_ref":"` + ref + `"}`}, nil
	case "reply":
		return contract.ToolResult{Content: `{}`, Terminal: &contract.Decision{Kind: contract.Reply, Reply: call.Arguments["reply"].(string)}}, nil
	}
	return contract.ToolResult{}, fmt.Errorf("unexpected %s", call.Name)
}

func TestJournalMismatchStopsTheWake(t *testing.T) {
	j := kt.NewJournal()
	if _, err := claim(t, j, kt.New(kt.Text("ok")), &fakeHost{}, wake()); err != nil {
		t.Fatal(err)
	}
	changed := wake()
	changed.Instructions = "You are someone else."
	fresh := kt.New(kt.Text("fresh"))
	wj := kernel.NewWakeJournal(j, lease)
	o := kernel.Options{Model: wj.Model(fresh), Host: wj.Host(&fakeHost{}), Tools: standardTools(t, contract.LaneForeground, contract.SessionInteractive), Locale: "en"}
	_, err := kernel.Run(context.Background(), o, changed)
	if !errors.Is(err, kernel.ErrJournal) || !errors.Is(err, contract.ErrConflict) || fresh.Calls() != 0 {
		t.Fatalf("a changed request stops the wake: %v calls=%d", err, fresh.Calls())
	}

	// A recorded call replayed with other arguments is a conflict, too.
	j2 := kt.NewJournal()
	j2.Seed("w1", contract.ToolRecord{NativeID: "c1", Call: contract.ToolCall{NativeID: "c1", Name: "read_task",
		Arguments: map[string]any{"source_ref": "m1", "task_ref": "A"}}, Result: contract.ToolResult{Content: "{}"}})
	h := &readHost{}
	m := kt.New(
		kt.Tools(kt.Call{ID: "c1", Name: "read_task", Args: map[string]any{"source_ref": "m1", "task_ref": "B"}}),
		kt.Text("never"),
	)
	wj = kernel.NewWakeJournal(j2, lease)
	o = kernel.Options{Model: wj.Model(m), Host: wj.Host(h), Tools: o.Tools, Locale: "en"}
	_, err = kernel.Run(context.Background(), o, wake())
	if !errors.Is(err, kernel.ErrJournal) || !errors.Is(err, contract.ErrConflict) || len(h.read) != 0 || m.Calls() != 1 {
		t.Fatalf("argument mismatch stops the wake: %v reads=%v calls=%d", err, h.read, m.Calls())
	}
}
