package toolhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/toolhost"
)

var t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

var (
	alice = contract.Actor{ID: "alice", Kind: contract.ActorHuman, DisplayName: "Alice"}
	bob   = contract.Actor{ID: "bob", Kind: contract.ActorHuman, DisplayName: "Bob"}
)

func event(id string, actor contract.Actor, text string) contract.Event {
	return contract.Event{ID: id, Source: "test", Type: "message.addressed", Class: contract.ClassBusiness, Mode: contract.ModeTriggered,
		Scene: contract.SceneRef{ID: "s1", Kind: contract.SceneGroup}, Actor: actor, OccurredAt: t0, Text: text}
}

func window(id string, lane contract.Lane, kind contract.SessionKind, evs ...contract.Event) contract.Window {
	w := contract.Window{ID: id, Employee: "e1", Session: contract.SessionRef{Scene: "s1", Kind: kind}, Lane: lane,
		Lease: &contract.Lease{WindowID: id, Token: "tok", Generation: 1}}
	for i, e := range evs {
		w.Items = append(w.Items, contract.WindowItem{ReceiptID: id + "-r" + string(rune('0'+i)), Event: e})
	}
	return w
}

func fg(id string, evs ...contract.Event) contract.Window {
	return window(id, contract.LaneForeground, contract.SessionInteractive, evs...)
}

type fixture struct {
	tasks   *fakeTasks
	mem     *memstore.Memory
	conv    *memstore.Conversation
	journal *kt.Journal
	host    *toolhost.Host
}

func newFixture(memOpts ...memstore.MemoryOption) *fixture {
	f := &fixture{tasks: newFakeTasks(), mem: memstore.NewMemory(memOpts...), conv: memstore.NewConversation(), journal: kt.NewJournal()}
	f.host = toolhost.New(toolhost.Ports{Tasks: f.tasks, Memory: f.mem, Conversation: f.conv, Journal: f.journal})
	return f
}

// journaled binds a window through the wake journal, as KernelRunner does,
// so read refs are recorded where task controls look for them.
func (f *fixture) journaled(win contract.Window) contract.Host {
	return kernel.NewWakeJournal(f.journal, *win.Lease).Host(f.host.ForWindow(win, "t1"))
}

func exec(t *testing.T, h contract.Host, win contract.Window, id, name string, args map[string]any) (contract.ToolResult, map[string]any, error) {
	t.Helper()
	res, err := h.Execute(context.Background(), contract.Identity{Tenant: "t1", Employee: "e1", WindowID: win.ID, Session: win.Session},
		contract.ToolCall{NativeID: id, Name: name, Arguments: args})
	var fields map[string]any
	if err == nil {
		if jerr := json.Unmarshal([]byte(res.Content), &fields); jerr != nil {
			t.Fatalf("%s content is not JSON: %q", name, res.Content)
		}
	}
	return res, fields, err
}

func wantRefusal(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, contract.ErrToolRefused) || toolhost.RefusalCode(err) != code {
		t.Fatalf("want refusal %q, got %v", code, err)
	}
}

func names(specs []contract.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

func TestSpecsFollowConfiguredPorts(t *testing.T) {
	none := names(toolhost.New(toolhost.Ports{}).Specs())
	if strings.Join(none, ",") != "stay_quiet" {
		t.Fatalf("no ports: %v", none)
	}
	tasksOnly := names(toolhost.New(toolhost.Ports{Tasks: newFakeTasks()}).Specs())
	if strings.Join(tasksOnly, ",") != "ask_human,find_tasks,read_task,stay_quiet" {
		t.Fatalf("tasks only: %v", tasksOnly)
	}
	withJournal := names(toolhost.New(toolhost.Ports{Tasks: newFakeTasks(), Journal: kt.NewJournal()}).Specs())
	if strings.Join(withJournal, ",") != "ask_human,continue_task,dispatch_task,find_tasks,read_task,stay_quiet,steer_task,stop_task" {
		t.Fatalf("tasks and journal: %v", withJournal)
	}
	all := names(newFixture().host.Specs())
	want := "ask_human,continue_task,dispatch_task,find_tasks,memory_capture,memory_forget,memory_lookup,read_context,read_task,reply,stay_quiet,steer_task,stop_task"
	if strings.Join(all, ",") != want {
		t.Fatalf("all ports: %v", all)
	}
	for _, s := range newFixture().host.Specs() {
		if s.DescriptionKey == "" {
			t.Fatalf("%s: catalog specs carry description keys", s.Name)
		}
	}
}

func TestFencesAndAvailability(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "hello"))
	h := f.host.ForWindow(win, "t1")
	_, _, err := exec(t, h, win, "c1", "reply", map[string]any{"source_ref": "nope", "reply": "hi"})
	wantRefusal(t, err, toolhost.CodeUnknownSourceRef)
	_, _, err = exec(t, h, fg("w2", event("m1", alice, "hello")), "c1", "stay_quiet", map[string]any{})
	wantRefusal(t, err, toolhost.CodeWindowMismatch)
	_, _, err = exec(t, h, win, "c1", "describe_capabilities", map[string]any{"source_ref": "m1", "reply": "x"})
	wantRefusal(t, err, toolhost.CodeUnknownTool)
	bare := toolhost.New(toolhost.Ports{Tasks: f.tasks}).ForWindow(win, "t1")
	_, _, err = exec(t, bare, win, "c1", "memory_lookup", map[string]any{"source_ref": "m1", "scope": "scene", "query": ""})
	wantRefusal(t, err, toolhost.CodeToolUnavailable)
	follow := window("w3", contract.LaneBackground, contract.SessionFollowUp, event("m1", alice, "x"))
	_, _, err = exec(t, f.host.ForWindow(follow, "t1"), follow, "c1", "dispatch_task", map[string]any{"source_ref": "m1", "goal": "g", "prompt": "p"})
	wantRefusal(t, err, toolhost.CodeNotOffered)
}

func TestReplyQuietAndAskHuman(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "which format?"))
	h := f.host.ForWindow(win, "t1")
	res, _, err := exec(t, h, win, "c1", "reply", map[string]any{"source_ref": "m1", "reply": " sure "})
	if err != nil || res.Terminal == nil || res.Terminal.Kind != contract.Reply || res.Terminal.Reply != "sure" {
		t.Fatalf("reply: %+v %v", res, err)
	}
	res, _, err = exec(t, h, win, "c2", "stay_quiet", map[string]any{"reason": "nothing new"})
	if err != nil || res.Terminal.Kind != contract.Quiet {
		t.Fatalf("quiet: %+v %v", res, err)
	}
	res, fields, err := exec(t, h, win, "c3", "ask_human", map[string]any{"source_ref": "m1", "question": "PDF or slides?", "options": []any{"PDF", "slides"}})
	if err != nil || res.Terminal.Kind != contract.Waiting || res.Receipt != "w1/0/c3" || fields["receipt"] != "w1/0/c3" {
		t.Fatalf("ask: %+v %v", res, err)
	}
	if !strings.HasPrefix(res.Terminal.Reply, "PDF or slides?") || !strings.Contains(res.Terminal.Reply, "- slides") {
		t.Fatalf("ask reply text: %q", res.Terminal.Reply)
	}
}

// The standard tool catalog is what the module implements end to end: a
// Host with every port configured offers all of it, and no platform tool
// its catalog lists.
func TestHostImplementsExactlyTheStandardTools(t *testing.T) {
	std, err := contract.StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, s := range std {
		want = append(want, s.Name)
	}
	for _, s := range newFixture().host.Specs() {
		got = append(got, s.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("offered %v, standard %v", got, want)
	}
	ext := []contract.ToolSpec{{Name: "platform_only", Kind: contract.ToolRead, DescriptionKey: "platform.tool",
		Parameters: map[string]any{"type": "object"}}}
	h := toolhost.New(toolhost.Ports{Tasks: newFakeTasks(), Journal: kt.NewJournal()}, toolhost.WithCatalog(append(std, ext...)))
	for _, s := range h.Specs() {
		for _, e := range ext {
			if s.Name == e.Name {
				t.Errorf("toolhost offers %s, which only a platform Host implements", s.Name)
			}
		}
	}
}

func TestTaskManagerErrorsAreClassified(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "build the weekly report"))
	h := f.host.ForWindow(win, "t1")
	args := map[string]any{"source_ref": "m1", "goal": "weekly report", "prompt": "build it"}
	f.tasks.Fail = fmt.Errorf("%w: goal too long", contract.ErrInvalidRequest)
	if _, _, err := exec(t, h, win, "c1", "dispatch_task", args); toolhost.RefusalCode(err) != toolhost.CodeInvalidRequest {
		t.Fatalf("an invalid request is a refusal the model can correct: %v", err)
	}
	f.tasks.Fail = fmt.Errorf("%w: shutting down", contract.ErrUnavailable)
	_, _, err := exec(t, h, win, "c2", "dispatch_task", args)
	if toolhost.RefusalCode(err) != "" || errors.Is(err, contract.ErrToolRefused) || !errors.Is(err, contract.ErrUnavailable) {
		t.Fatalf("an unavailable Task Manager is a retryable failure, not a refusal: %v", err)
	}
	if _, _, err := exec(t, h, win, "c3", "dispatch_task", args); err != nil {
		t.Fatalf("the next call reaches the Task Manager: %v", err)
	}
}

func TestDispatchIsIdempotentPerWindowCall(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "build the weekly report"))
	h := f.host.ForWindow(win, "t1")
	args := map[string]any{"source_ref": "m1", "goal": "weekly report", "prompt": "build it", "reply": "on it"}
	first, fields, err := exec(t, h, win, "c1", "dispatch_task", args)
	if err != nil || first.Receipt != "task-1" || first.Terminal.Kind != contract.Dispatched || first.Terminal.Reply != "on it" || fields["created"] != true {
		t.Fatalf("dispatch: %+v %v", first, err)
	}
	again, fields, err := exec(t, f.host.ForWindow(win, "t1"), win, "c1", "dispatch_task", args)
	if err != nil || again.Receipt != "task-1" || fields["created"] != false || len(f.tasks.dispatches) != 1 {
		t.Fatalf("replayed dispatch creates nothing: %+v %v n=%d", again, err, len(f.tasks.dispatches))
	}
	req := f.tasks.dispatches[0]
	if req.IdempotencyKey != "w1/0/c1" || req.Requester != alice || req.Tenant != "t1" || req.Scene.ID != "s1" || req.SourceRef != "m1" {
		t.Fatalf("request: %+v", req)
	}
	other, _, err := exec(t, h, win, "c2", "dispatch_task", args)
	if err != nil || other.Receipt != "task-2" {
		t.Fatalf("a new call id is new work: %+v %v", other, err)
	}
	_, _, err = exec(t, h, win, "c3", "dispatch_task", map[string]any{"source_ref": "m9", "goal": "g", "prompt": "p"})
	wantRefusal(t, err, toolhost.CodeUnknownSourceRef)
}

func TestFindAndReadTaskAreSceneFenced(t *testing.T) {
	f := newFixture()
	f.tasks.seed(contract.TaskView{TaskRef: "mine", Employee: "e1", Scene: "s1", Requester: "alice", Goal: "report", Status: contract.TaskDone, Revision: 3, Report: "42 rows"})
	f.tasks.seed(contract.TaskView{TaskRef: "elsewhere", Employee: "e1", Scene: "s2", Requester: "alice", Goal: "report", Status: contract.TaskDone, Revision: 1})
	win := fg("w1", event("m1", alice, "how did the report go?"))
	h := f.host.ForWindow(win, "t1")
	_, fields, err := exec(t, h, win, "c1", "find_tasks", map[string]any{"source_ref": "m1", "query": "report"})
	if err != nil {
		t.Fatal(err)
	}
	if tasks := fields["tasks"].([]any); len(tasks) != 1 || tasks[0].(map[string]any)["task_ref"] != "mine" {
		t.Fatalf("find: %v", fields)
	}
	_, fields, err = exec(t, h, win, "c2", "read_task", map[string]any{"source_ref": "m1", "task_ref": "mine"})
	if err != nil || fields["read_ref"] != "mine@3" || fields["report"] != "42 rows" {
		t.Fatalf("read: %v %v", fields, err)
	}
	_, _, err = exec(t, h, win, "c3", "read_task", map[string]any{"source_ref": "m1", "task_ref": "elsewhere"})
	wantRefusal(t, err, toolhost.CodeTaskNotInScene)
	_, _, err = exec(t, h, win, "c4", "read_task", map[string]any{"source_ref": "m1", "task_ref": "missing"})
	wantRefusal(t, err, toolhost.CodeTaskNotFound)
}

func TestContinueAndStopNeedAFreshReadFromTheSameWindow(t *testing.T) {
	f := newFixture()
	f.tasks.seed(contract.TaskView{TaskRef: "done", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskDone, Revision: 2})
	f.tasks.seed(contract.TaskView{TaskRef: "live", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskRunning, Revision: 5})
	other := fg("w0", event("m0", alice, "status?"))
	if _, _, err := exec(t, f.journaled(other), other, "c1", "read_task", map[string]any{"source_ref": "m0", "task_ref": "done"}); err != nil {
		t.Fatal(err)
	}
	win := fg("w1", event("m1", alice, "extend it to Q4"), event("m2", bob, "stop it"))
	h := f.journaled(win)
	cont := map[string]any{"source_ref": "m1", "read_ref": "done@2", "prompt": "add Q4", "reply": "extending"}
	_, _, err := exec(t, h, win, "c1", "continue_task", cont)
	wantRefusal(t, err, toolhost.CodeReadRefNotInWindow)
	_, _, err = exec(t, h, win, "c2", "continue_task", map[string]any{"source_ref": "m1", "read_ref": "garbage", "prompt": "x"})
	wantRefusal(t, err, toolhost.CodeBadReadRef)
	if _, _, err := exec(t, h, win, "c3", "read_task", map[string]any{"source_ref": "m1", "task_ref": "done"}); err != nil {
		t.Fatal(err)
	}
	res, _, err := exec(t, h, win, "c4", "continue_task", cont)
	if err != nil || res.Receipt != "done" || res.Terminal.Kind != contract.Dispatched || res.Terminal.Reply != "extending" {
		t.Fatalf("continue: %+v %v", res, err)
	}
	// The continuation bumped the revision: the earlier read is stale now.
	_, _, err = exec(t, h, win, "c5", "stop_task", map[string]any{"source_ref": "m1", "read_ref": "done@2", "reply": "stopped"})
	wantRefusal(t, err, toolhost.CodeStaleReadRef)

	if _, _, err := exec(t, h, win, "c6", "read_task", map[string]any{"source_ref": "m1", "task_ref": "live"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = exec(t, h, win, "c7", "stop_task", map[string]any{"source_ref": "m2", "read_ref": "live@5", "reply": "stopped"})
	wantRefusal(t, err, toolhost.CodeNotRequester)
	_, _, err = exec(t, h, win, "c8", "continue_task", map[string]any{"source_ref": "m1", "read_ref": "live@5", "prompt": "more"})
	wantRefusal(t, err, toolhost.CodeTaskState)
	res, _, err = exec(t, h, win, "c9", "stop_task", map[string]any{"source_ref": "m1", "read_ref": "live@5", "reply": "stopped"})
	if err != nil || res.Receipt != "live" || res.Terminal.Kind != contract.Reply || res.Terminal.Reply != "stopped" {
		t.Fatalf("stop: %+v %v", res, err)
	}
	if strings.Join(f.tasks.controls, "|") != "continue:done:add Q4|stop:live:" {
		t.Fatalf("controls: %v", f.tasks.controls)
	}
}

func TestSteerPicksTheRequestersOnlyRunningTask(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "use the EU numbers"), event("m2", bob, "use mine"))
	h := f.host.ForWindow(win, "t1")
	steer := map[string]any{"source_ref": "m1", "correction": "EU numbers only", "reply": "noted"}
	_, _, err := exec(t, h, win, "c1", "steer_task", steer)
	wantRefusal(t, err, toolhost.CodeNoRunningTask)
	f.tasks.seed(contract.TaskView{TaskRef: "a1", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskRunning, Revision: 1})
	f.tasks.seed(contract.TaskView{TaskRef: "b1", Employee: "e1", Scene: "s1", Requester: "bob", Status: contract.TaskRunning, Revision: 1})
	f.tasks.seed(contract.TaskView{TaskRef: "a0", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskDone, Revision: 1})
	res, _, err := exec(t, h, win, "c2", "steer_task", steer)
	if err != nil || res.Receipt != "a1" || res.Terminal.Kind != contract.Dispatched || res.Terminal.Reply != "noted" {
		t.Fatalf("steer: %+v %v", res, err)
	}
	_, _, err = exec(t, h, win, "c3", "steer_task", map[string]any{"source_ref": "m2", "task_ref": "a1", "correction": "x"})
	wantRefusal(t, err, toolhost.CodeNotRequester)
	_, _, err = exec(t, h, win, "c4", "steer_task", map[string]any{"source_ref": "m1", "task_ref": "a0", "correction": "x"})
	wantRefusal(t, err, toolhost.CodeTaskNotRunning)
	f.tasks.seed(contract.TaskView{TaskRef: "a2", Employee: "e1", Scene: "s1", Requester: "alice", Status: contract.TaskRunning, Revision: 1})
	_, _, err = exec(t, h, win, "c5", "steer_task", steer)
	wantRefusal(t, err, toolhost.CodeAmbiguousTask)
}

func TestMemoryCaptureLookupAndForget(t *testing.T) {
	f := newFixture()
	win := fg("w1", event("m1", alice, "remember: I prefer PDF reports"), event("m2", bob, "what does alice prefer?"))
	h := f.host.ForWindow(win, "t1")
	capture := map[string]any{"source_ref": "m1", "audience": "me", "type": "preference", "subject": "report format", "quote": "I prefer PDF reports"}
	_, _, err := exec(t, h, win, "c1", "memory_capture", map[string]any{"source_ref": "m1", "audience": "me", "type": "preference", "subject": "x", "quote": "I prefer slides"})
	wantRefusal(t, err, toolhost.CodeBadQuote)
	_, _, err = exec(t, h, win, "c2", "memory_capture", map[string]any{"source_ref": "m1", "audience": "me", "type": "digest", "subject": "x", "quote": "PDF"})
	wantRefusal(t, err, toolhost.CodeBadMemoryKind)
	res, fields, err := exec(t, h, win, "c3", "memory_capture", capture)
	if err != nil || res.Receipt == "" || fields["deduplicated"] != false {
		t.Fatalf("capture: %+v %v", res, err)
	}
	fact, _ := f.mem.GetFact(context.Background(), "t1", res.Receipt)
	if fact.Subject != "alice" || fact.Audience != "me" || fact.EvidenceScope != contract.ScopeUserSaid || fact.SourceRef != "m1" || fact.Speaker != "alice" {
		t.Fatalf("fact: %+v", fact)
	}
	if again, fields, _ := exec(t, h, win, "c4", "memory_capture", capture); again.Receipt != res.Receipt || fields["deduplicated"] != true {
		t.Fatalf("dedup: %+v", again)
	}
	lookup := func(source, scope string) []any {
		_, fields, err := exec(t, h, win, "l-"+source+scope, "memory_lookup", map[string]any{"source_ref": source, "scope": scope, "query": "pdf"})
		if err != nil {
			t.Fatal(err)
		}
		return fields["records"].([]any)
	}
	if got := lookup("m1", "me"); len(got) != 1 {
		t.Fatalf("alice sees her record: %v", got)
	}
	if got := lookup("m2", "me"); len(got) != 0 {
		t.Fatalf("bob must not read alice's bag: %v", got)
	}
	if got := lookup("m2", "scene"); len(got) != 0 {
		t.Fatalf("scene bag is separate: %v", got)
	}
	_, _, err = exec(t, h, win, "c5", "memory_forget", map[string]any{"source_ref": "m2", "record_ref": res.Receipt})
	wantRefusal(t, err, toolhost.CodeUnknownRecord)
	forget, fields, err := exec(t, h, win, "c6", "memory_forget", map[string]any{"source_ref": "m1", "record_ref": res.Receipt})
	if err != nil || forget.Receipt != res.Receipt || fields["archived"] != float64(1) {
		t.Fatalf("forget: %+v %v", forget, err)
	}
	if fact, _ := f.mem.GetFact(context.Background(), "t1", res.Receipt); fact.Status != memstore.FactArchived {
		t.Fatalf("forget archives, never deletes: %+v", fact)
	}
	if got := lookup("m1", "me"); len(got) != 0 {
		t.Fatalf("archived records are not looked up: %v", got)
	}
}

func TestMemoryCaptureCompactsWhenTheBagIsFull(t *testing.T) {
	f := newFixture(memstore.WithLimits(contract.MemoryLimits{MaxFactBytes: 600, MaxActivePerBag: 3, MaxBytesPerBag: 1 << 20, CompactKeepNewer: 1}))
	win := fg("w1", event("m1", alice, "note one; note two; note three; note four"))
	h := f.host.ForWindow(win, "t1")
	for i, q := range []string{"note one", "note two", "note three", "note four"} {
		_, fields, err := exec(t, h, win, "c"+q, "memory_capture", map[string]any{"source_ref": "m1", "audience": "scene", "type": "fact", "subject": "notes", "quote": q})
		if err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
		if i == 3 && fields["compacted"] != float64(2) {
			t.Fatalf("the fourth capture compacts the two oldest records: %v", fields)
		}
	}
	n, _, _ := f.mem.CountActive(context.Background(), "t1", "s1", "", "scene")
	if n != 3 { // digest + kept newer + new record
		t.Fatalf("active after compaction: %d", n)
	}
}

func TestReadContextReturnsConversationBeforeTheWindow(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	for _, m := range []contract.Msg{
		{Role: "user", Speaker: "alice", Text: "old question", SourceRef: "m0"},
		{Role: "assistant", Speaker: "e1", Text: "old answer", Kind: "reply"},
		{Role: "user", Speaker: "alice", Text: "new question", SourceRef: "m1"},
	} {
		if _, err := f.conv.Append(ctx, "t1", "s1", m); err != nil {
			t.Fatal(err)
		}
	}
	win := fg("w1", event("m1", alice, "new question"))
	h := f.host.ForWindow(win, "t1")
	_, fields, err := exec(t, h, win, "c1", "read_context", map[string]any{"source_ref": "m1", "section": "older_conversation"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := fields["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["text"] != "old question" || msgs[1].(map[string]any)["text"] != "old answer" {
		t.Fatalf("older conversation: %v", msgs)
	}
	_, _, err = exec(t, h, win, "c2", "read_context", map[string]any{"source_ref": "m1", "section": "capability_details"})
	wantRefusal(t, err, toolhost.CodeSectionUnavailable)
}
