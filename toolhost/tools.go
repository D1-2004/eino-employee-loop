package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// windowHost executes tools for one claimed window.
type windowHost struct {
	h      *Host
	win    contract.Window
	tenant string
}

// Execute implements contract.Host.
func (w *windowHost) Execute(ctx context.Context, id contract.Identity, call contract.ToolCall) (contract.ToolResult, error) {
	if id.WindowID != "" && id.WindowID != w.win.ID {
		return contract.ToolResult{}, refuse(CodeWindowMismatch)
	}
	fn, ok := implemented[call.Name]
	if !ok {
		return contract.ToolResult{}, refuse(CodeUnknownTool)
	}
	spec, ok := w.h.specs[call.Name]
	if !ok {
		return contract.ToolResult{}, refuse(CodeToolUnavailable)
	}
	if !spec.OfferedIn(w.win.Lane, w.win.Session.Kind) {
		return contract.ToolResult{}, refuse(CodeNotOffered)
	}
	return fn(w, ctx, call)
}

// ------------------------------------------------------------------ helpers

func str(call contract.ToolCall, key string) string {
	s, _ := call.Arguments[key].(string)
	return strings.TrimSpace(s)
}

func strList(call contract.ToolCall, key string) []string {
	raw, _ := call.Arguments[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// source returns the window event named by source_ref.
func (w *windowHost) source(call contract.ToolCall) (contract.Event, error) {
	ref := str(call, "source_ref")
	for _, it := range w.win.Items {
		if ref != "" && it.Event.ID == ref {
			return it.Event, nil
		}
	}
	return contract.Event{}, refuse(CodeUnknownSourceRef)
}

// requester returns the authenticated human behind a source event.
func requester(ev contract.Event) (contract.Actor, error) {
	if strings.TrimSpace(ev.Actor.ID) == "" || ev.Actor.Kind != contract.ActorHuman {
		return contract.Actor{}, refuse(CodeNoRequester)
	}
	return ev.Actor, nil
}

func (w *windowHost) sceneID() string { return w.win.Session.Scene }

// idemKey identifies a call across claims of its window: providers may
// reuse native ids in later model turns, so the turn is part of the key.
func (w *windowHost) idemKey(call contract.ToolCall) string {
	return w.win.ID + "/" + strconv.Itoa(call.Turn) + "/" + call.NativeID
}

func (w *windowHost) locale(ev contract.Event) string {
	if ev.Locale != "" {
		return ev.Locale
	}
	return w.h.locale
}

// taskErr maps Task Manager errors the model can correct to refusals.
// Everything else passes through: contract.ErrUnavailable and transport
// failures are infrastructure failures that the loop retries (an effect tool
// fails the wake and the window is retried; a read tool reports the error).
func taskErr(err error) error {
	switch {
	case errors.Is(err, contract.ErrInvalidRequest):
		return refuse(CodeInvalidRequest)
	case errors.Is(err, contract.ErrForbidden):
		return refuse(CodeForbidden)
	case errors.Is(err, contract.ErrStaleRevision):
		return refuse(CodeStaleReadRef)
	case errors.Is(err, contract.ErrTaskState):
		return refuse(CodeTaskState)
	case errors.Is(err, contract.ErrNotFound):
		return refuse(CodeTaskNotFound)
	}
	return err
}

// sceneTask reads a task and fences it to this employee and scene.
func (w *windowHost) sceneTask(ctx context.Context, ref string) (contract.TaskView, error) {
	if ref == "" {
		return contract.TaskView{}, refuse(CodeTaskNotFound)
	}
	view, err := w.h.ports.Tasks.Get(ctx, w.win.Employee, ref)
	if err != nil {
		return contract.TaskView{}, taskErr(err)
	}
	if view.Employee != w.win.Employee || view.Scene != w.sceneID() {
		return contract.TaskView{}, refuse(CodeTaskNotInScene)
	}
	return view, nil
}

func taskFields(v contract.TaskView) map[string]any {
	return map[string]any{
		"task_ref": v.TaskRef, "status": v.Status, "goal": v.Goal, "requester": v.Requester,
		"revision": v.Revision, "updated_at": v.UpdatedAt,
	}
}

// ReadRef renders the reference read_task returns for a task revision.
func ReadRef(taskRef string, revision int64) string {
	return taskRef + "@" + strconv.FormatInt(revision, 10)
}

func parseReadRef(ref string) (string, int64, bool) {
	i := strings.LastIndex(ref, "@")
	if i <= 0 || i == len(ref)-1 {
		return "", 0, false
	}
	rev, err := strconv.ParseInt(ref[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return ref[:i], rev, true
}

// ------------------------------------------------------------- terminals

func (w *windowHost) reply(_ context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	if _, err := w.source(call); err != nil {
		return contract.ToolResult{}, err
	}
	text := str(call, "reply")
	if text == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	return contract.ToolResult{
		Content:  content(map[string]any{"state": "accepted_for_delivery", "delivery_proven": false}),
		Terminal: &contract.Decision{Kind: contract.Reply, Reply: text},
	}, nil
}

func (w *windowHost) stayQuiet(_ context.Context, _ contract.ToolCall) (contract.ToolResult, error) {
	return contract.ToolResult{
		Content:  content(map[string]any{"state": "quiet"}),
		Terminal: &contract.Decision{Kind: contract.Quiet},
	}, nil
}

func (w *windowHost) askHuman(_ context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	question := str(call, "question")
	if question == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	options := strList(call, "options")
	lines := []string{question}
	for _, o := range options {
		lines = append(lines, w.h.messages.Text(w.locale(ev), "toolhost.ask.option", "option", o))
	}
	receipt := w.idemKey(call)
	return contract.ToolResult{
		Content:  content(map[string]any{"state": "waiting", "receipt": receipt, "options": len(options)}),
		Receipt:  receipt,
		Terminal: &contract.Decision{Kind: contract.Waiting, Reply: strings.Join(lines, "\n")},
	}, nil
}

// ----------------------------------------------------------------- tasks

func (w *windowHost) dispatchTask(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	actor, err := requester(ev)
	if err != nil {
		return contract.ToolResult{}, err
	}
	goal, prompt := str(call, "goal"), str(call, "prompt")
	if goal == "" || prompt == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	rcpt, err := w.h.ports.Tasks.Dispatch(ctx, contract.DispatchRequest{
		IdempotencyKey: w.idemKey(call), Employee: w.win.Employee, Tenant: w.tenant, Scene: ev.Scene,
		Requester: actor, SourceRef: ev.ID, Goal: goal, Prompt: prompt, Locale: ev.Locale,
	})
	if err != nil {
		return contract.ToolResult{}, taskErr(err)
	}
	return contract.ToolResult{
		Content: content(map[string]any{"task_ref": rcpt.TaskRef, "run_ref": rcpt.RunRef, "status": rcpt.Status,
			"created": rcpt.Created, "revision": rcpt.Revision, "result_proven": false}),
		Receipt:  rcpt.TaskRef,
		Terminal: &contract.Decision{Kind: contract.Dispatched, Reply: str(call, "reply")},
	}, nil
}

func (w *windowHost) findTasks(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	if _, err := w.source(call); err != nil {
		return contract.ToolResult{}, err
	}
	views, err := w.h.ports.Tasks.Find(ctx, contract.TaskQuery{
		Employee: w.win.Employee, Scene: w.sceneID(), Text: str(call, "query"), Limit: w.h.limits.FindTasks,
	})
	if err != nil {
		return contract.ToolResult{}, taskErr(err)
	}
	tasks := make([]map[string]any, 0, len(views))
	for _, v := range views {
		if v.Employee != w.win.Employee || v.Scene != w.sceneID() {
			continue
		}
		tasks = append(tasks, taskFields(v))
		if len(tasks) == w.h.limits.FindTasks {
			break
		}
	}
	return contract.ToolResult{Content: content(map[string]any{"tasks": tasks})}, nil
}

func (w *windowHost) readTask(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	if _, err := w.source(call); err != nil {
		return contract.ToolResult{}, err
	}
	view, err := w.sceneTask(ctx, str(call, "task_ref"))
	if err != nil {
		return contract.ToolResult{}, err
	}
	fields := taskFields(view)
	fields["read_ref"] = ReadRef(view.TaskRef, view.Revision)
	fields["report"] = view.Report
	fields["failure"] = view.Failure
	fields["source_ref"] = view.SourceRef
	return contract.ToolResult{Content: content(fields)}, nil
}

// readControl resolves a read_ref obtained earlier in this window into the
// task it names, refusing foreign requesters. It does not compare the
// revision: the control carries it as ExpectedRevision and the Task Manager
// checks it after its idempotency lookup, so a replayed control whose first
// attempt committed (and bumped the revision) is answered with its receipt
// instead of being refused as stale.
func (w *windowHost) readControl(ctx context.Context, call contract.ToolCall, actor contract.Actor) (contract.TaskView, int64, error) {
	ref, rev, ok := parseReadRef(str(call, "read_ref"))
	if !ok {
		return contract.TaskView{}, 0, refuse(CodeBadReadRef)
	}
	read, err := w.readInWindow(ctx, ReadRef(ref, rev))
	if err != nil {
		return contract.TaskView{}, 0, err
	}
	if !read {
		return contract.TaskView{}, 0, refuse(CodeReadRefNotInWindow)
	}
	view, err := w.sceneTask(ctx, ref)
	if err != nil {
		return contract.TaskView{}, 0, err
	}
	if view.Requester != actor.ID {
		return contract.TaskView{}, 0, refuse(CodeNotRequester)
	}
	return view, rev, nil
}

// readInWindow reports whether a successful read_task of this window, as
// recorded in its journal, returned readRef. The journal is durable, so the
// answer survives re-claims and process changes.
func (w *windowHost) readInWindow(ctx context.Context, readRef string) (bool, error) {
	if w.win.Lease == nil || w.h.ports.Journal == nil {
		return false, nil
	}
	recs, err := w.h.ports.Journal.Tools(ctx, *w.win.Lease)
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		if rec.Call.Name != "read_task" || rec.Error != "" {
			continue
		}
		var fields struct {
			ReadRef string `json:"read_ref"`
		}
		if json.Unmarshal([]byte(rec.Result.Content), &fields) == nil && fields.ReadRef == readRef {
			return true, nil
		}
	}
	return false, nil
}

func (w *windowHost) control(call contract.ToolCall, ev contract.Event, actor contract.Actor, taskRef string, revision int64, text string) contract.TaskControl {
	return contract.TaskControl{
		IdempotencyKey: w.idemKey(call), Employee: w.win.Employee, Scene: w.sceneID(), TaskRef: taskRef,
		Requester: actor, SourceRef: ev.ID, Text: text, ExpectedRevision: revision,
	}
}

func receiptFields(r contract.TaskReceipt) map[string]any {
	return map[string]any{"task_ref": r.TaskRef, "run_ref": r.RunRef, "status": r.Status, "revision": r.Revision}
}

// controlPrelude validates source, requester and read_ref of a control and
// returns the control to send, fenced to the read revision.
func (w *windowHost) controlPrelude(ctx context.Context, call contract.ToolCall, text string) (contract.TaskControl, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.TaskControl{}, err
	}
	actor, err := requester(ev)
	if err != nil {
		return contract.TaskControl{}, err
	}
	view, rev, err := w.readControl(ctx, call, actor)
	if err != nil {
		return contract.TaskControl{}, err
	}
	return w.control(call, ev, actor, view.TaskRef, rev, text), nil
}

func (w *windowHost) continueTask(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	prompt := str(call, "prompt")
	if prompt == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	c, err := w.controlPrelude(ctx, call, prompt)
	if err != nil {
		return contract.ToolResult{}, err
	}
	rcpt, err := w.h.ports.Tasks.Continue(ctx, c)
	if err != nil {
		return contract.ToolResult{}, taskErr(err)
	}
	return contract.ToolResult{
		Content:  content(receiptFields(rcpt)),
		Receipt:  rcpt.TaskRef,
		Terminal: &contract.Decision{Kind: contract.Dispatched, Reply: str(call, "reply")},
	}, nil
}

func (w *windowHost) stopTask(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	reply := str(call, "reply")
	if reply == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	c, err := w.controlPrelude(ctx, call, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	rcpt, err := w.h.ports.Tasks.Stop(ctx, c)
	if err != nil {
		return contract.ToolResult{}, taskErr(err)
	}
	return contract.ToolResult{
		Content:  content(receiptFields(rcpt)),
		Receipt:  rcpt.TaskRef,
		Terminal: &contract.Decision{Kind: contract.Reply, Reply: reply},
	}, nil
}

func (w *windowHost) steerTask(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	actor, err := requester(ev)
	if err != nil {
		return contract.ToolResult{}, err
	}
	correction := str(call, "correction")
	if correction == "" {
		return contract.ToolResult{}, refuse(CodeEmptyText)
	}
	ref := str(call, "task_ref")
	if ref == "" {
		views, err := w.h.ports.Tasks.Find(ctx, contract.TaskQuery{Employee: w.win.Employee, Scene: w.sceneID(), Requester: actor.ID})
		if err != nil {
			return contract.ToolResult{}, taskErr(err)
		}
		var running []string
		for _, v := range views {
			if v.Status == contract.TaskRunning && v.Requester == actor.ID && v.Scene == w.sceneID() && v.Employee == w.win.Employee {
				running = append(running, v.TaskRef)
			}
		}
		switch len(running) {
		case 0:
			return contract.ToolResult{}, refuse(CodeNoRunningTask)
		case 1:
			ref = running[0]
		default:
			return contract.ToolResult{}, refuse(CodeAmbiguousTask)
		}
	}
	view, err := w.sceneTask(ctx, ref)
	if err != nil {
		return contract.ToolResult{}, err
	}
	if view.Requester != actor.ID {
		return contract.ToolResult{}, refuse(CodeNotRequester)
	}
	// The task's state is checked by the Task Manager after its idempotency
	// lookup, not here: a replayed steer whose first attempt committed must
	// get its receipt even when the task has moved on since.
	rcpt, err := w.h.ports.Tasks.Steer(ctx, w.control(call, ev, actor, view.TaskRef, 0, correction))
	if errors.Is(err, contract.ErrTaskState) {
		return contract.ToolResult{}, refuse(CodeTaskNotRunning)
	}
	if err != nil {
		return contract.ToolResult{}, taskErr(err)
	}
	return contract.ToolResult{
		Content:  content(receiptFields(rcpt)),
		Receipt:  rcpt.TaskRef,
		Terminal: &contract.Decision{Kind: contract.Dispatched, Reply: str(call, "reply")},
	}, nil
}

// ---------------------------------------------------------------- memory

// memoryKinds are the kinds a model may capture; "digest" is Host-only.
var memoryKinds = map[string]bool{"fact": true, "decision": true, "preference": true, "pattern": true, "pitfall": true}

// bag returns (subject, audience) for a scope on behalf of a requester.
func bag(scope string, actor contract.Actor) (string, string) {
	if scope == "me" {
		return actor.ID, "me"
	}
	return "", "scene"
}

func (w *windowHost) memoryCapture(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	actor, err := requester(ev)
	if err != nil {
		return contract.ToolResult{}, err
	}
	kind, topic, quote := str(call, "type"), str(call, "subject"), str(call, "quote")
	if !memoryKinds[kind] {
		return contract.ToolResult{}, refuse(CodeBadMemoryKind)
	}
	if quote == "" || !strings.Contains(ev.Text, quote) {
		return contract.ToolResult{}, refuse(CodeBadQuote)
	}
	subject, audience := bag(str(call, "audience"), actor)
	fact := &contract.Fact{
		Tenant: w.tenant, Scene: w.sceneID(), Subject: subject, Audience: audience, Kind: kind,
		Text:  w.h.messages.Text(w.locale(ev), "toolhost.memory.text", "subject", topic, "quote", quote),
		Quote: quote, Speaker: actor.ID, SpeakerRole: "user", EvidenceScope: contract.ScopeUserSaid,
		SourceRef: ev.ID, SaidMs: ev.OccurredAt.UnixMilli(),
	}
	mem := w.h.ports.Memory
	compacted := 0
	cur, created, err := mem.Put(ctx, fact, ev.Text)
	if errors.Is(err, contract.ErrQuota) {
		if compacted, _, err = mem.Compact(ctx, w.tenant, w.sceneID(), subject, audience); err != nil {
			return contract.ToolResult{}, err
		}
		cur, created, err = mem.Put(ctx, fact, ev.Text)
	}
	switch {
	case errors.Is(err, contract.ErrBadQuote):
		return contract.ToolResult{}, refuse(CodeBadQuote)
	case errors.Is(err, contract.ErrQuota):
		return contract.ToolResult{}, refuse(CodeMemoryQuota)
	case err != nil:
		return contract.ToolResult{}, err
	}
	return contract.ToolResult{
		Content: content(map[string]any{"record_ref": cur.ID, "audience": audience, "deduplicated": !created, "compacted": compacted}),
		Receipt: cur.ID,
	}, nil
}

func (w *windowHost) memoryLookup(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	scope := str(call, "scope")
	var actor contract.Actor
	if scope == "me" {
		if actor, err = requester(ev); err != nil {
			return contract.ToolResult{}, err
		}
	}
	subject, audience := bag(scope, actor)
	facts, err := w.h.ports.Memory.Search(ctx, w.tenant, w.sceneID(), subject, audience, str(call, "query"), w.h.limits.MemoryRecords)
	if err != nil {
		return contract.ToolResult{}, err
	}
	records := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		if f.Scene != w.sceneID() || f.Audience != audience || f.Subject != subject {
			continue
		}
		records = append(records, map[string]any{
			"record_ref": f.ID, "kind": f.Kind, "text": f.Text, "speaker": f.Speaker,
			"speaker_role": f.SpeakerRole, "evidence_scope": f.EvidenceScope, "said_ms": f.SaidMs,
		})
	}
	return contract.ToolResult{Content: content(map[string]any{"scope": audience, "records": records})}, nil
}

func (w *windowHost) memoryForget(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	ev, err := w.source(call)
	if err != nil {
		return contract.ToolResult{}, err
	}
	actor, err := requester(ev)
	if err != nil {
		return contract.ToolResult{}, err
	}
	ref := str(call, "record_ref")
	fact, err := w.h.ports.Memory.GetFact(ctx, w.tenant, ref)
	if errors.Is(err, contract.ErrNotFound) {
		return contract.ToolResult{}, refuse(CodeUnknownRecord)
	}
	if err != nil {
		return contract.ToolResult{}, err
	}
	visible := fact.Scene == w.sceneID() && (fact.Audience == "scene" || (fact.Audience == "me" && fact.Subject == actor.ID))
	if !visible {
		return contract.ToolResult{}, refuse(CodeUnknownRecord)
	}
	n, err := w.h.ports.Memory.Archive(ctx, w.tenant, []string{fact.ID}, "forget:"+actor.ID)
	if err != nil {
		return contract.ToolResult{}, err
	}
	return contract.ToolResult{Content: content(map[string]any{"record_ref": fact.ID, "archived": n}), Receipt: fact.ID}, nil
}

// ---------------------------------------------------------- conversation

func (w *windowHost) readContext(ctx context.Context, call contract.ToolCall) (contract.ToolResult, error) {
	if _, err := w.source(call); err != nil {
		return contract.ToolResult{}, err
	}
	if str(call, "section") != "older_conversation" {
		return contract.ToolResult{}, refuse(CodeSectionUnavailable)
	}
	conv, limit := w.h.ports.Conversation, w.h.limits.Conversation
	inWindow := map[string]bool{}
	for _, it := range w.win.Items {
		inWindow[it.Event.ID] = true
	}
	// Find where this window starts in the log: the first message recorded
	// for one of its events. Without one, everything already logged is older.
	recent, err := conv.Recent(ctx, w.tenant, w.sceneID(), 0, limit+len(w.win.Items))
	if err != nil {
		return contract.ToolResult{}, err
	}
	var before int64
	for _, m := range recent {
		if inWindow[m.SourceRef] {
			before = m.Seq
			break
		}
	}
	msgs := recent
	if before > 0 {
		if msgs, err = conv.Recent(ctx, w.tenant, w.sceneID(), before, limit); err != nil {
			return contract.ToolResult{}, err
		}
	} else if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	lines := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if inWindow[m.SourceRef] {
			continue
		}
		lines = append(lines, map[string]any{"seq": m.Seq, "role": m.Role, "speaker": m.Speaker, "kind": m.Kind, "text": m.Text, "at_ms": m.AtMs})
	}
	return contract.ToolResult{Content: content(map[string]any{"section": "older_conversation", "messages": lines})}, nil
}
