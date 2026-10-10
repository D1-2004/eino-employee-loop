package schema_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/kernel"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/scene"
	"github.com/D1-2004/eino-employee-loop/schema"
	"github.com/D1-2004/eino-employee-loop/store/memstore"
	"github.com/D1-2004/eino-employee-loop/taskmgr"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
	"github.com/D1-2004/eino-employee-loop/taskmgr/memtask"
	"github.com/D1-2004/eino-employee-loop/toolhost"
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

var bg = context.Background()

// goTypes pairs every top-level schema with the Go type it describes.
var goTypes = map[string]any{
	"event.schema.json":             contract.Event{},
	"event-ack.schema.json":         contract.EventAck{},
	"delivery.schema.json":          contract.Delivery{},
	"delivery-ack.schema.json":      httpapi.DeliveryAck{},
	"delivery-status.schema.json":   httpapi.DeliveryStatus{},
	"dispatch-request.schema.json":  contract.DispatchRequest{},
	"task-control.schema.json":      contract.TaskControl{},
	"task-receipt.schema.json":      contract.TaskReceipt{},
	"task-view.schema.json":         contract.TaskView{},
	"task-list.schema.json":         httpapi.TaskList{},
	"execution-payload.schema.json": taskmgr.ExecutionPayload{},
	"tool-spec.schema.json":         contract.ToolSpec{},
	"type-spec.schema.json":         contract.TypeSpec{},
	"error.schema.json":             httpapi.ErrorBody{},
}

// nestedTypes pairs schema pointers inside files with Go types.
var nestedTypes = []struct {
	file, pointer string
	typ           any
}{
	{"defs.schema.json", "/$defs/SceneRef", contract.SceneRef{}},
	{"defs.schema.json", "/$defs/Actor", contract.Actor{}},
	{"defs.schema.json", "/$defs/SessionRef", contract.SessionRef{}},
	{"type-spec.schema.json", "/properties/coalesce", contract.Coalesce{}},
}

type field struct {
	name      string
	omitempty bool
}

func jsonFields(typ any) []field {
	t := reflect.TypeOf(typ)
	var out []field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, field{name: name, omitempty: strings.Contains(opts, "omitempty")})
	}
	return out
}

func pointer(t *testing.T, doc map[string]any, ptr string) map[string]any {
	t.Helper()
	node := doc
	for _, part := range strings.Split(strings.Trim(ptr, "/"), "/") {
		next, ok := node[part].(map[string]any)
		if !ok {
			t.Fatalf("pointer %s not found", ptr)
		}
		node = next
	}
	return node
}

// checkTags compares a schema's properties with the Go JSON tags: the same
// names, and only fields that are always encoded may be required.
func checkTags(t *testing.T, where string, node map[string]any, typ any) {
	t.Helper()
	props, _ := node["properties"].(map[string]any)
	var schemaNames, goNames []string
	for k := range props {
		schemaNames = append(schemaNames, k)
	}
	always := map[string]bool{}
	for _, f := range jsonFields(typ) {
		goNames = append(goNames, f.name)
		always[f.name] = !f.omitempty
	}
	sort.Strings(schemaNames)
	sort.Strings(goNames)
	if strings.Join(schemaNames, ",") != strings.Join(goNames, ",") {
		t.Errorf("%s: properties %v, Go JSON fields %v", where, schemaNames, goNames)
	}
	req, _ := node["required"].([]any)
	for _, r := range req {
		if !always[r.(string)] {
			t.Errorf("%s: %q is required but the Go field is omitempty or missing", where, r)
		}
	}
}

func TestSchemaFilesAreWellFormed(t *testing.T) {
	v := loadValidator(t)
	if len(v.docs) != len(goTypes)+1 {
		t.Fatalf("schemas %v; every schema except defs needs a goTypes entry", schema.JSONSchemas())
	}
	for name, doc := range v.docs {
		if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("%s: $schema %v", name, doc["$schema"])
		}
		if id, _ := doc["$id"].(string); !strings.HasSuffix(id, "/schema/"+name) {
			t.Errorf("%s: $id %q", name, id)
		}
		var rs []string
		refs(doc, &rs)
		for _, r := range rs {
			if _, _, err := v.resolve(name, r); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}

func TestSchemasMatchGoJSONTags(t *testing.T) {
	v := loadValidator(t)
	for name, typ := range goTypes {
		doc, ok := v.docs[name]
		if !ok {
			t.Errorf("missing schema %s", name)
			continue
		}
		checkTags(t, name, doc, typ)
	}
	for _, n := range nestedTypes {
		checkTags(t, n.file+"#"+n.pointer, pointer(t, v.docs[n.file], n.pointer), n.typ)
	}
}

func TestErrorCodesMatchTransport(t *testing.T) {
	v := loadValidator(t)
	enum := pointer(t, v.docs["error.schema.json"], "/properties/code")["enum"].([]any)
	var got, want []string
	for _, e := range enum {
		got = append(got, e.(string))
	}
	for _, c := range httpapi.ErrorCodes {
		want = append(want, c.Code)
		if p := v.Validate("error.schema.json", httpapi.ErrorBody{Code: c.Code, Message: c.Err.Error()}); len(p) > 0 {
			t.Errorf("%s: %v", c.Code, p)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("error codes %v, transport %v", got, want)
	}
}

// recorder wraps a Task Manager and keeps every request it forwarded.
type recorder struct {
	contract.TaskManager
	mu       sync.Mutex
	dispatch []contract.DispatchRequest
	controls []contract.TaskControl
	receipts []contract.TaskReceipt
}

func (r *recorder) Dispatch(ctx context.Context, req contract.DispatchRequest) (contract.TaskReceipt, error) {
	rc, err := r.TaskManager.Dispatch(ctx, req)
	r.mu.Lock()
	r.dispatch, r.receipts = append(r.dispatch, req), append(r.receipts, rc)
	r.mu.Unlock()
	return rc, err
}

func (r *recorder) Steer(ctx context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return r.control(ctx, c, r.TaskManager.Steer)
}

func (r *recorder) Stop(ctx context.Context, c contract.TaskControl) (contract.TaskReceipt, error) {
	return r.control(ctx, c, r.TaskManager.Stop)
}

func (r *recorder) control(ctx context.Context, c contract.TaskControl, f func(context.Context, contract.TaskControl) (contract.TaskReceipt, error)) (contract.TaskReceipt, error) {
	rc, err := f(ctx, c)
	r.mu.Lock()
	r.controls, r.receipts = append(r.controls, c), append(r.receipts, rc)
	r.mu.Unlock()
	return rc, err
}

// sink admits events into a hub and keeps events and acks.
type sink struct {
	hub    *scene.Hub
	mu     sync.Mutex
	events []contract.Event
	acks   []contract.EventAck
}

func (s *sink) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	ack, err := s.hub.Publish(ctx, e)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.events, s.acks = append(s.events, e), append(s.acks, ack)
	}
	return ack, err
}

type runnerFunc func(ctx context.Context, win contract.Window) (contract.Outcome, error)

func (f runnerFunc) RunWindow(ctx context.Context, win contract.Window) (contract.Outcome, error) {
	return f(ctx, win)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Sample instances come from the real implementations: the Hub admits and
// delivers, the standard toolhost builds Task Manager requests, and memtask
// answers and publishes execution events.
func TestRealInstancesValidate(t *testing.T) {
	v := loadValidator(t)
	valid := func(file string, instance any) {
		t.Helper()
		if p := v.Validate(file, instance); len(p) > 0 {
			raw, _ := json.Marshal(instance)
			t.Errorf("%s: %v\n%s", file, p, raw)
		}
	}

	st := memstore.New()
	hub := &scene.Hub{Employee: "e1", Catalog: contract.StandardCatalog(), Log: st, Queue: st, Outbox: st,
		Runner: runnerFunc(func(context.Context, contract.Window) (contract.Outcome, error) {
			return contract.Outcome{Decision: contract.Decision{Kind: contract.Reply, Reply: "On it."}}, nil
		})}
	in := &sink{hub: hub}
	block := executors.FuncExecutor(func(ctx context.Context, job taskmgr.Job, r taskmgr.Reporter) (string, error) {
		if err := r.Progress(ctx, "half way"); err != nil {
			return "", err
		}
		if job.Prompt == "block" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "done: " + job.Prompt, nil
	})
	tm := memtask.New(block, in, memtask.WithBackoff(time.Millisecond, 5*time.Millisecond))
	t.Cleanup(func() { _ = tm.Close(bg) })
	rec := &recorder{TaskManager: tm}

	human := contract.Event{
		ID: "m1", Source: "dingtalk.im", Type: contract.TypeMessageAddressed,
		Scene:      contract.SceneRef{ID: "s1", Kind: contract.SceneGroup, Tenant: "t1", Provider: "dingtalk", Locator: "cid-1"},
		Actor:      contract.Actor{ID: "alice", Kind: contract.ActorHuman, DisplayName: "Alice"},
		OccurredAt: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC), Locale: "en", Text: "Please summarize the week",
		Refs: map[string]string{contract.RefReplyTo: "m0"},
	}
	if _, err := in.Publish(bg, human); err != nil {
		t.Fatal(err)
	}

	// The loop wakes, decides and posts a delivery.
	if _, err := hub.Step(bg, contract.ClaimRequest{Worker: "w", Now: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	pending, err := st.Pending(bg, "e1", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %v err %v", pending, err)
	}
	valid("delivery.schema.json", pending[0])
	valid("delivery-ack.schema.json", httpapi.DeliveryAck{ProviderID: "pm-1"})
	valid("delivery-status.schema.json", httpapi.DeliveryStatus{State: contract.DeliveryDelivered, ProviderID: "pm-1"})
	valid("delivery-status.schema.json", httpapi.DeliveryStatus{State: contract.DeliveryUnknown})

	// The standard tools build Task Manager requests from the window.
	win := contract.Window{ID: "win-1", Employee: "e1", Lane: contract.LaneForeground,
		Session: contract.SessionRef{Scene: "s1", Kind: contract.SessionInteractive},
		Items:   []contract.WindowItem{{ReceiptID: "r1", Event: human}},
		Lease:   &contract.Lease{WindowID: "win-1", Token: "tok", Generation: 1}}
	journal := kt.NewJournal()
	host := kernel.NewWakeJournal(journal, *win.Lease).Host(toolhost.New(toolhost.Ports{Tasks: rec, Journal: journal}).ForWindow(win, "t1"))
	call := func(id, name string, args map[string]any) contract.ToolResult {
		t.Helper()
		res, err := host.Execute(bg, contract.Identity{WindowID: win.ID}, contract.ToolCall{NativeID: id, Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}
	call("c1", "dispatch_task", map[string]any{"source_ref": "m1", "goal": "Weekly summary", "prompt": "summarize",
		"reply": "On it."})
	call("c2", "dispatch_task", map[string]any{"source_ref": "m1", "goal": "Long job", "prompt": "block"})
	running := rec.receipts[1].TaskRef
	waitFor(t, "running", func() bool {
		view, err := tm.Get(bg, "e1", running)
		return err == nil && view.Status == contract.TaskRunning
	})
	waitFor(t, "first task done", func() bool {
		view, err := tm.Get(bg, "e1", rec.receipts[0].TaskRef)
		return err == nil && view.Status == contract.TaskDone
	})
	call("c3", "steer_task", map[string]any{"source_ref": "m1", "task_ref": running, "correction": "be brief"})
	// The superseding run moves the revision once more when it starts.
	steered := rec.receipts[2].Revision
	waitFor(t, "steered run started", func() bool {
		view, err := tm.Get(bg, "e1", running)
		return err == nil && view.Revision > steered
	})
	read := call("c4", "read_task", map[string]any{"source_ref": "m1", "task_ref": running})
	var fields map[string]any
	if err := json.Unmarshal([]byte(read.Content), &fields); err != nil {
		t.Fatal(err)
	}
	call("c5", "stop_task", map[string]any{"source_ref": "m1", "read_ref": fields["read_ref"], "reply": "Stopped."})
	if err := tm.Flush(bg); err != nil {
		t.Fatal(err)
	}

	for _, req := range rec.dispatch {
		valid("dispatch-request.schema.json", req)
	}
	for _, c := range rec.controls {
		valid("task-control.schema.json", c)
	}
	for _, rc := range rec.receipts {
		valid("task-receipt.schema.json", rc)
	}
	for _, rc := range rec.receipts {
		view, err := tm.Get(bg, "e1", rc.TaskRef)
		if err != nil {
			t.Fatal(err)
		}
		valid("task-view.schema.json", view)
	}
	views, err := tm.Find(bg, contract.TaskQuery{Employee: "e1", Scene: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	valid("task-list.schema.json", httpapi.TaskList{Tasks: views})

	// Every published event, its payload and its ack.
	in.mu.Lock()
	events, acks := append([]contract.Event(nil), in.events...), append([]contract.EventAck(nil), in.acks...)
	in.mu.Unlock()
	types := map[string]bool{}
	for i, e := range events {
		types[e.Type] = true
		valid("event.schema.json", e)
		valid("event-ack.schema.json", acks[i])
		if e.PayloadSchema == taskmgr.PayloadSchema {
			valid("execution-payload.schema.json", e.Payload)
		}
	}
	for _, typ := range []string{taskmgr.EventAccepted, taskmgr.EventStarted, taskmgr.EventProgress, taskmgr.EventSteered, taskmgr.EventResult, taskmgr.EventTerminal} {
		if !types[typ] {
			t.Errorf("no %s event was sampled", typ)
		}
	}

	// Catalog entries.
	tools, err := contract.StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tools {
		valid("tool-spec.schema.json", s)
	}
	valid("tool-spec.schema.json", contract.ToolSpec{Name: "custom", Kind: contract.ToolRead, Description: "d",
		Parameters: map[string]any{"type": "object"}, Timeout: 5 * time.Second})
	for _, s := range contract.StandardCatalog().Types() {
		valid("type-spec.schema.json", s)
	}
}

func TestInvalidInstancesAreRejected(t *testing.T) {
	v := loadValidator(t)
	cases := []struct {
		file, json, want string
	}{
		{"event.schema.json", `{"id":"m1","source":"s","occurred_at":"2026-10-11T09:00:00Z"}`, `missing required "type"`},
		{"event.schema.json", `{"id":"m1","source":"s","type":"t","occurred_at":"yesterday"}`, "not a date-time"},
		{"event.schema.json", `{"id":"m1","source":"s","type":"t","occurred_at":"2026-10-11T09:00:00Z","colour":"red"}`, `unknown property "colour"`},
		{"event.schema.json", `{"id":"m1","source":"s","type":"t","occurred_at":"2026-10-11T09:00:00Z","actor":{"id":"a","kind":"robot"}}`, "not in enum"},
		{"event.schema.json", `{"id":"m1","source":"s","type":"t","occurred_at":"2026-10-11T09:00:00Z","class":"gossip"}`, "anyOf"},
		{"delivery.schema.json", `{"id":"w/reply","employee":"e","scene":{},"session":{"scene":"s","kind":"interactive"},"window_id":"w","text":"hi","state":"lost"}`, "not in enum"},
		{"dispatch-request.schema.json", `{"idempotency_key":"k","employee":"e","scene":{"id":"s"},"requester":{"id":"a"},"goal":"","prompt":""}`, "anyOf"},
		{"dispatch-request.schema.json", `{"idempotency_key":"k","employee":"e","scene":{},"requester":{"id":"a"},"goal":"g"}`, `missing required "id"`},
		{"task-receipt.schema.json", `{"task_ref":"t","status":"done","created":true,"revision":1.5}`, "expected integer"},
		{"error.schema.json", `{"code":"teapot","message":"x"}`, "not in enum"},
		{"tool-spec.schema.json", `{"name":"t","kind":"read","parameters":{}}`, "anyOf"},
	}
	for _, tc := range cases {
		p := v.Validate(tc.file, []byte(tc.json))
		if !strings.Contains(strings.Join(p, "\n"), tc.want) {
			t.Errorf("%s %s: want %q, got %v", tc.file, tc.json, tc.want, p)
		}
	}
}
