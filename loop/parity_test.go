package loop_test

// The parity test replays seeded scenarios and compares a canonical log
// with testdata/parity.golden.gz, the log of v0.2.1 (the hand-written loop).
// Record it only in a v0.2.1 checkout with this file copied in:
//
//	go test ./loop -run TestParityWithV021 -update
//
// Never run -update on v0.3 or later: it would record that version's own
// behaviour as the baseline.
//
// The log holds the exact JSON of every provider request (messages with
// Extra, the bound tools and every model option), every Host and Config
// callback in order, and the full outcome. A Host that journals requests
// may resume a wake begun by another binary, so they must stay identical.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/D1-2004/eino-employee-loop/loop"
	lt "github.com/D1-2004/eino-employee-loop/loop/looptest"
)

var update = flag.Bool("update", false, "record testdata/parity.golden.gz")

const (
	parityScenarios = 3000
	parityGolden    = "testdata/parity.golden.gz"
)

func TestParityWithV021(t *testing.T) {
	var got bytes.Buffer
	for seed := int64(0); seed < parityScenarios; seed++ {
		got.WriteString(paritySc(seed))
	}
	if *update {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(got.Bytes())
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(parityGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parityGolden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d scenarios: %d bytes, %d compressed", parityScenarios, got.Len(), buf.Len())
		return
	}
	f, err := os.Open(parityGolden)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got.Bytes(), want) {
		return
	}
	g, w := bufio.NewScanner(&got), bufio.NewScanner(bytes.NewReader(want))
	g.Buffer(nil, 1<<24)
	w.Buffer(nil, 1<<24)
	scenario := ""
	for line := 1; ; line++ {
		gok, wok := g.Scan(), w.Scan()
		if strings.HasPrefix(w.Text(), "### ") {
			scenario = w.Text()
		}
		if gok != wok || g.Text() != w.Text() {
			t.Fatalf("parity log differs at line %d (%s):\n got: %.2000s\nwant: %.2000s", line, scenario, g.Text(), w.Text())
		}
		if !gok {
			t.Fatal("parity log differs")
		}
	}
}

// plog is the canonical log of one scenario. The scenario's ctx is
// cancelled right after the cancelAt-th event of Run.
type plog struct {
	buf      strings.Builder
	events   int
	cancelAt int
	cancel   context.CancelFunc
	// names labels message pointers by first sight, so the log shows which
	// messages are the same object.
	names map[*schema.Message]string
}

func (l *plog) add(format string, a ...any) {
	fmt.Fprintf(&l.buf, format+"\n", a...)
	l.events++
	if l.cancel != nil && l.events == l.cancelAt {
		l.buf.WriteString("CANCEL\n")
		l.cancel()
	}
}

func (l *plog) label(m *schema.Message) string {
	if m == nil {
		return "nil"
	}
	if name, ok := l.names[m]; ok {
		return name
	}
	name := fmt.Sprintf("m%d", len(l.names))
	l.names[m] = name
	return name
}

func (l *plog) labels(msgs []*schema.Message) string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = l.label(m)
	}
	return strings.Join(out, ",")
}

func js(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "!" + err.Error()
	}
	return string(raw)
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q", err.Error())
}

func ctxText(ctx context.Context) string {
	_, deadline := ctx.Deadline()
	return fmt.Sprintf("ctx=%s deadline=%v", errText(ctx.Err()), deadline)
}

// parityModel records every request and replays scripted steps.
type parityModel struct {
	log      *plog
	steps    *[]lt.Step
	tools    []*schema.ToolInfo
	ctxAware bool
	sent     *string
}

func (m *parityModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.log.add("WITHTOOLS %d", len(tools))
	cp := *m
	cp.tools = tools
	return &cp, nil
}

func (m *parityModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	*m.sent = js(in)
	m.log.add("GEN msgs=[%s] %s\n  messages=%s\n  tools=%s\n  options=%s", m.log.labels(in), ctxText(ctx), *m.sent, js(m.tools), js(model.GetCommonOptions(&model.Options{}, opts...)))
	if m.ctxAware && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(*m.steps) == 0 {
		return nil, lt.ErrExhausted
	}
	s := (*m.steps)[0]
	*m.steps = (*m.steps)[1:]
	return s.Message, s.Err
}

func (m *parityModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("parity: no stream")
}

var parityTools = []loop.Tool{
	{Name: "reply", Description: "Reply in the conversation.", Terminal: loop.Reply, Schema: strArgs("source_ref", "reply")},
	{Name: "stay_quiet", Description: "保持安静。", Terminal: loop.Quiet, Schema: obj},
	{Name: "dispatch_task", Description: "Dispatch a task.", Effect: true, Terminal: loop.Dispatched, Schema: strArgs("source_ref", "goal", "reply")},
	{Name: "wait", Terminal: loop.Waiting, Schema: obj},
	{Name: "read_task", Description: "Read a task.", Schema: map[string]any{
		"type": "object", "required": []any{"task_ref"}, "additionalProperties": false,
		"properties": map[string]any{
			"task_ref": map[string]any{"type": "string", "minLength": 1, "maxLength": 12},
			"fields":   map[string]any{"type": "array", "maxItems": 2, "items": map[string]any{"type": "string", "enum": []any{"status", "owner"}}},
			"limit":    map[string]any{"type": "integer"},
		},
	}},
	{Name: "stop_task", Effect: true, Exclusive: true, Timeout: time.Hour, Schema: strArgs("task_ref")},
	{Name: "note", Effect: true, Schema: obj},
	{Name: "flex", Schema: obj},
	{Name: "ping"},
}

type parityHost struct{ log *plog }

func (h parityHost) Execute(ctx context.Context, id loop.Identity, call loop.ToolCall) (loop.ToolResult, error) {
	h.log.add("EXEC %s id=%s call=%s", ctxText(ctx), js(id), js(call))
	arg := func(k string) string { v, _ := call.Arguments[k].(string); return v }
	switch call.Name {
	case "reply":
		if strings.Contains(arg("reply"), "noreceipt") {
			return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Reply, Reply: arg("reply")}}, nil
		}
		return loop.ToolResult{Content: `{"ok":1}`, Receipt: "out-" + call.NativeToolCallID, Terminal: &loop.Decision{Kind: loop.Reply, Reply: arg("reply")}}, nil
	case "stay_quiet":
		return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Quiet}}, nil
	case "wait":
		return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Waiting, Reply: "稍等"}}, nil
	case "dispatch_task":
		switch arg("goal") {
		case "noreceipt":
			return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Dispatched, Reply: arg("reply")}}, nil
		case "commitfail":
			return loop.ToolResult{Receipt: "task-x"}, errors.New("commit then fail")
		case "refuse":
			return loop.ToolResult{}, fmt.Errorf("%w: bad source", loop.ErrToolRefused)
		}
		return loop.ToolResult{Content: `{"task":"t"}`, Receipt: " task-" + call.NativeToolCallID, Terminal: &loop.Decision{Kind: loop.Dispatched, Reply: arg("reply")}}, nil
	case "read_task":
		switch arg("task_ref") {
		case "refuse":
			return loop.ToolResult{Content: "nope"}, fmt.Errorf("%w: unknown", loop.ErrToolRefused)
		case "fail":
			return loop.ToolResult{}, errors.New("read failed")
		case "receiptfail":
			return loop.ToolResult{Receipt: "r-odd"}, errors.New("odd")
		}
		return loop.ToolResult{Content: `{"status":"running"}`}, nil
	case "stop_task":
		return loop.ToolResult{Content: "{}", Receipt: "stop-" + call.NativeToolCallID}, nil
	case "note":
		if call.Arguments["receipt"] == true {
			return loop.ToolResult{Content: "noted", Receipt: "note-" + call.NativeToolCallID}, nil
		}
		return loop.ToolResult{Content: "noted"}, nil
	case "flex":
		switch call.Arguments["kind"] {
		case "reply":
			return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Reply, Reply: " flex "}}, nil
		case "quiet":
			return loop.ToolResult{Content: "{}", Terminal: &loop.Decision{Kind: loop.Quiet}}, nil
		}
		return loop.ToolResult{Content: "flexed"}, nil
	case "ping":
		return loop.ToolResult{Content: "pong"}, nil
	}
	return loop.ToolResult{}, errors.New("unexpected tool")
}

type parityFinalHost struct{ parityHost }

func (h parityFinalHost) AcceptFinalText(ctx context.Context, id loop.Identity, text string) (loop.ToolResult, error) {
	h.log.add("FINAL %s id=%s text=%q", ctxText(ctx), js(id), text)
	switch {
	case strings.Contains(text, "journalfail"):
		return loop.ToolResult{Receipt: "journal-1"}, errors.New("journal down")
	case strings.Contains(text, "noterminal"):
		return loop.ToolResult{Receipt: "fr"}, nil
	}
	return loop.ToolResult{Content: "sent", Receipt: "final", Terminal: &loop.Decision{Kind: loop.Reply, Reply: text}}, nil
}

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.Intn(len(xs))] }

func parityCall(r *rand.Rand, id string) lt.Call {
	switch r.Intn(18) {
	case 0:
		return lt.Call{ID: id, Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": pick(r, "ok", "BAD", "noreceipt", "好的")}}
	case 1:
		return lt.Call{ID: id, Name: "reply", Args: map[string]any{"reply": "missing source"}}
	case 2:
		return lt.Call{ID: id, Name: "stay_quiet", Args: map[string]any{}}
	case 3:
		return lt.Call{ID: id, Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": pick(r, "g", "noreceipt", "commitfail", "refuse"), "reply": pick(r, "on it", "BAD")}}
	case 4:
		return lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": pick(r, "t1", "refuse", "fail", "veto", "receiptfail")}}
	case 5:
		return lt.Call{ID: id, Name: "read_task", Args: pick(r,
			map[string]any{"task_ref": 3},
			map[string]any{"task_ref": ""},
			map[string]any{"task_ref": "t1", "fields": []any{"status", "owner", "status"}},
			map[string]any{"task_ref": "t1", "fields": []any{"color"}},
			map[string]any{"task_ref": "t1", "limit": 1.5, "extra": true},
			map[string]any{"task_ref": "t1", "fields": []any{"owner"}, "limit": 2},
		)}
	case 6:
		return lt.Call{ID: id, Name: "stop_task", Args: map[string]any{"task_ref": "t1"}}
	case 7:
		return lt.Call{ID: id, Name: "flex", Args: map[string]any{"kind": pick(r, "reply", "quiet", "none")}}
	case 8:
		return lt.Call{ID: id, Name: "nope", Args: map[string]any{}}
	case 9:
		return lt.Call{ID: id, Name: "wait", Args: map[string]any{}}
	case 10:
		return lt.Call{ID: id, Name: "note", Args: map[string]any{"receipt": r.Intn(2) == 0}}
	case 11:
		return lt.Call{ID: id, Name: "ping", Args: map[string]any{}}
	case 12, 13:
		return lt.Call{ID: id, Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "ok"}}
	case 14:
		return lt.Call{ID: id, Name: "dispatch_task", Args: map[string]any{"source_ref": "m1", "goal": "g", "reply": "on it"}}
	}
	return lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": "t1"}}
}

func parityStep(r *rand.Rand, n int) lt.Step {
	withMeta := func(s lt.Step) lt.Step {
		if r.Intn(2) == 0 {
			s.Message.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: r.Intn(100), CompletionTokens: r.Intn(50), TotalTokens: r.Intn(150)}
		}
		if r.Intn(10) == 0 {
			s.Message.Extra = map[string]any{"provider_id": fmt.Sprintf("resp-%d", n)}
		}
		if r.Intn(12) == 0 {
			s.Message.ResponseMeta = nil
		}
		if r.Intn(10) == 0 {
			s.Message.Role = pick[schema.RoleType](r, "", "Assistant", schema.User, schema.Tool, schema.System)
		}
		return s
	}
	switch k := r.Intn(14); {
	case k < 3:
		return withMeta(lt.Text(pick(r, "hello", " hello ", "BAD text", "", "  ", "journalfail", "noterminal", "a longer answer of forty-two characters..", strings.Repeat("x", 9000), "<tool>x</tool>")))
	case k == 3:
		return withMeta(lt.Truncated("cut"))
	case k == 4:
		return lt.Fail(pick(r, errors.New("boom"), errors.New("perm denied"), errors.New("provider 500")))
	case k == 5:
		return lt.Step{}
	}
	var calls []lt.Call
	for i, c := 0, pick(r, 1, 1, 1, 2, 3); i < c; i++ {
		id := fmt.Sprintf("c%d_%d", n, i)
		switch r.Intn(14) {
		case 0:
			id = "dup"
		case 1:
			id = pick(r, "", " ")
		}
		calls = append(calls, parityCall(r, id))
	}
	s := lt.Tools(calls...)
	switch r.Intn(20) {
	case 0:
		s.Message.ToolCalls[0].Type = "custom"
	case 1:
		s.Message.ToolCalls[0].Type = ""
	case 2:
		s.Message.ToolCalls[0].Function.Arguments = pick(r, "{", "null", "[]", `{"task_ref":"t1"} `)
	}
	if r.Intn(8) == 0 {
		s.Message.ResponseMeta.FinishReason = pick(r, "stop", "", "length", "content_filter")
	}
	if r.Intn(8) == 0 {
		s.Message.Content = "thinking"
	}
	return withMeta(s)
}

// longSteps scripts the chains of production budgets, past ADK's default
// limits: 2-10 or 21-24 batches the Host runs without error, a share of
// them (none, some or all) replaced by truncated responses, then a reply.
func longSteps(r *rand.Rand) []lt.Step {
	n := 2 + r.Intn(9)
	if r.Intn(2) == 0 {
		n = 21 + r.Intn(4)
	}
	cut := pick(r, 0, 0, 3, 10)
	var steps []lt.Step
	for i := 0; i < n; i++ {
		if r.Intn(10) < cut {
			steps = append(steps, lt.Truncated("cut"))
			continue
		}
		var calls []lt.Call
		for j, c := 0, 1+r.Intn(2); j < c; j++ {
			id := fmt.Sprintf("c%d_%d", i, j)
			calls = append(calls, pick(r,
				lt.Call{ID: id, Name: "read_task", Args: map[string]any{"task_ref": "t1"}},
				lt.Call{ID: id, Name: "ping", Args: map[string]any{}},
				lt.Call{ID: id, Name: "note", Args: map[string]any{"receipt": true}},
			))
		}
		steps = append(steps, lt.Tools(calls...))
	}
	return append(steps, lt.Tools(lt.Call{ID: fmt.Sprintf("c%d_0", n), Name: "reply", Args: map[string]any{"source_ref": "m1", "reply": "ok"}}))
}

func parityWake(r *rand.Rand) loop.Wake {
	w := loop.Wake{
		Identity: loop.Identity{Tenant: "t1", Employee: "e1", Scene: "s1", Receipt: "rc1"},
		Messages: []*schema.Message{schema.SystemMessage("You are a helpful colleague."), schema.UserMessage("alice: hi")},
	}
	if r.Intn(3) == 0 {
		prior := schema.AssistantMessage("", []schema.ToolCall{{ID: "p1", Type: "function", Function: schema.FunctionCall{Name: "read_task", Arguments: `{"task_ref":"t0"}`}}})
		user := schema.UserMessage("bob: 进展如何？")
		user.Name = "bob"
		user.Extra = map[string]any{"source_ref": "m2"}
		w.Messages = append(w.Messages, prior, schema.ToolMessage(`{"status":"done"}`, "p1", schema.WithToolName("read_task")), user)
	}
	return w
}

// paritySc runs one seeded scenario and returns its log.
func paritySc(seed int64) string {
	r := rand.New(rand.NewSource(seed))
	l := &plog{names: map[*schema.Message]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAt := -1
	if r.Intn(3) == 0 {
		cancelAt = r.Intn(30)
	}
	w := parityWake(r)
	cfg := loop.Config{Tools: parityTools}
	if r.Intn(10) == 0 {
		// ADK runs a wake without tools on a different graph.
		cfg.Tools = nil
	}
	// A long wake has a production budget (Multica's is {8,3,11}).
	long := r.Intn(7) == 0
	switch {
	case long && r.Intn(3) == 0:
		cfg.Budget = loop.Budget{MaxModelResponses: 8, MaxConsecutiveErrors: 3, MaxPhysicalAttempts: 11}
	case long:
		responses := 1 + r.Intn(30)
		cfg.Budget = loop.Budget{MaxModelResponses: responses, MaxConsecutiveErrors: 3 + r.Intn(10), MaxPhysicalAttempts: responses + r.Intn(11)}
	case r.Intn(3) > 0:
		cfg.Budget = loop.Budget{MaxModelResponses: 1 + r.Intn(4), MaxConsecutiveErrors: 1 + r.Intn(4), MaxPhysicalAttempts: 1 + r.Intn(6)}
	}
	cfg.RequireTerminalTool = r.Intn(2) == 0
	cfg.AcceptFinalText = r.Intn(2) == 0
	cfg.RequireNativeToolCall = r.Intn(3) == 0
	cfg.FinalTextLimit = pick(r, 0, 0, 20)
	var steps []lt.Step
	if long && r.Intn(2) == 0 {
		steps = longSteps(r)
	} else {
		for i, n := 0, 1+r.Intn(8); i < n; i++ {
			steps = append(steps, parityStep(r, i))
		}
		if cfg.RequireTerminalTool && cfg.AcceptFinalText && r.Intn(2) == 0 {
			// Exercise the final-text path more often.
			steps[0] = lt.Text(pick(r, "hello", "BAD text", "journalfail", "noterminal", "a longer answer of forty-two characters..", strings.Repeat("x", 9000)))
		}
	}
	cfg.Locale = pick(r, "", "en", "zh-CN", "zh-Hans", "fr")
	repair := r.Intn(3)
	switch repair {
	case 1:
		cfg.RepairText = func(_ int, _ []loop.ToolOutcome, err error) string {
			l.add("REPAIR %s", errText(err))
			return "FIX: " + err.Error()
		}
	case 2:
		cfg.RepairText = func(_ int, _ []loop.ToolOutcome, err error) string {
			l.add("REPAIR %s", errText(err))
			return " "
		}
	}
	cfg.ValidateText = func(s string) error {
		l.add("VTEXT %q", s)
		if strings.Contains(s, "BAD") {
			return errors.New("bad text")
		}
		return nil
	}
	if r.Intn(2) == 0 {
		cfg.ValidateBatch = func(_ int, _ []loop.ToolOutcome, calls []loop.ToolCall) error {
			l.add("VBATCH %s", js(calls))
			for _, c := range calls {
				if c.Arguments["task_ref"] == "veto" {
					return errors.New("vetoed")
				}
			}
			return nil
		}
	}
	if r.Intn(2) == 0 {
		cfg.Permanent = func(err error) bool {
			l.add("PERMANENT %s", errText(err))
			return strings.Contains(err.Error(), "perm")
		}
	}
	if r.Intn(2) == 0 {
		cfg.TurnTools = func(call int, outcomes []loop.ToolOutcome) []string {
			l.add("TURN %d outcomes=%s", call, js(outcomes))
			switch {
			case call == 3:
				return nil
			case call%2 == 0:
				return []string{"reply", "read_task", "stay_quiet", "unknown"}
			}
			return []string{"reply", "stay_quiet", "dispatch_task", "wait", "read_task", "stop_task", "note", "flex", "ping"}
		}
	}
	sent := new(string)
	cfg.OnModelCall = func(ctx context.Context, mc loop.ModelCall) {
		req := js(mc.Request)
		if req == *sent {
			req = "as-sent"
		}
		l.add("HOOK model attempt=%d %s request=[%s] %s response=%s %s err=%s", mc.Attempt, ctxText(ctx), l.labels(mc.Request), req, l.label(mc.Response), js(mc.Response), errText(mc.Err))
	}
	cfg.OnToolCall = func(ctx context.Context, c loop.ToolCall, res loop.ToolResult, err error, _ time.Duration) {
		l.add("HOOK tool %s call=%s result=%s err=%s", ctxText(ctx), js(c), js(res), errText(err))
	}
	cfg.OnBatchRejected = func(calls []loop.ToolCall, err error) {
		l.add("HOOK rejected calls=%s err=%s", js(calls), errText(err))
	}
	cfg.OnModelFailure = func(err error) { l.add("HOOK failure err=%s", errText(err)) }
	var host loop.Host
	hostKind := pick(r, "host", "final", "final", "nil")
	switch hostKind {
	case "host":
		host = parityHost{log: l}
	case "final":
		host = parityFinalHost{parityHost{log: l}}
	}
	m := &parityModel{log: l, steps: &steps, ctxAware: r.Intn(2) == 0, sent: sent}
	l.add("### scenario %d tools=%d host=%s budget=%+v terminal=%v final=%v forced=%v limit=%d locale=%q repair=%d vbatch=%v permanent=%v turn=%v ctxaware=%v cancel=%v@%d",
		seed, len(cfg.Tools), hostKind, cfg.Budget, cfg.RequireTerminalTool, cfg.AcceptFinalText, cfg.RequireNativeToolCall, cfg.FinalTextLimit, cfg.Locale, repair,
		cfg.ValidateBatch != nil, cfg.Permanent != nil, cfg.TurnTools != nil, m.ctxAware, cancelAt >= 0, cancelAt)
	l.add("WAKE [%s] %s", l.labels(w.Messages), js(w.Messages))
	for i := range steps {
		if s := steps[i]; s.Message != nil {
			l.label(s.Message)
		}
	}
	// Events count from Run on; cancelAt 0 cancels before Run.
	if cancelAt == 0 {
		l.buf.WriteString("CANCEL\n")
		cancel()
	} else if cancelAt > 0 {
		l.events, l.cancel, l.cancelAt = 0, cancel, cancelAt
	}
	out, err := loop.New(cfg, m, host).Run(ctx, w)
	l.cancel = nil
	l.add("ERR %s", errText(err))
	for _, s := range []error{loop.ErrModelBudget, loop.ErrConsecutiveModelErrors, loop.ErrModelResponseBudget, loop.ErrPhysicalModelBudget, loop.ErrMissingReceipt, loop.ErrTerminalConflict, loop.ErrToolRefused, context.Canceled, lt.ErrExhausted} {
		if errors.Is(err, s) {
			l.add("IS %v", s)
		}
	}
	l.add("DECISION %s calls=%d budget=%s usage=%s", js(out.Decision), out.ModelCalls, js(out.BudgetUsage), js(out.Usage))
	l.add("RECEIPTS %s", js(out.Receipts))
	l.add("OUTCOMES %s", js(out.ToolOutcomes))
	l.add("TRANSCRIPT [%s] %s", l.labels(out.Transcript), js(out.Transcript))
	return l.buf.String()
}
