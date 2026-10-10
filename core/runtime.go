package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// CaseData: the frozen, UNMODIFIED parts of a real eval case that the runtime reuses.
type CaseData struct {
	System     string
	Clock      string // "Frozen clock: {...}" message text
	Supervisor string
	Tools      []*schema.ToolInfo
	ClockAtMs  int64
	Terminal   map[string]bool // decision tools that end a turn (effect||terminal in tool_meta, minus memory_*)
}

type Config struct {
	Tenant, Scene string
	Case          CaseData
	JobDelay      time.Duration
	// Models injects the decision and ack chat models (see providers/llm).
	Models      ModelProvider
	Registry    bool // registry + memory store injected and tool-backed (treatment); false = control
	Ack         bool
	NoThinkAck  bool
	DedupWindow time.Duration
	ConvTokens  int // token budgets of dynamic context
	MemTokens   int
	RegTokens   int

	// CheckPointStore + CheckpointID wire Eino TurnLoop resume (SQLite KV locally).
	// Item must remain gob-encodable. Empty CheckpointID defaults to tenant|scene.
	CheckPointStore adk.CheckPointStore
	CheckpointID    string

	// OnOutbound is called for every user-facing delivery (ack/reply/ask/…).
	// Used by IM providers (stdin / DWS) so Host outbox and channel stay in sync.
	OnOutbound func(ctx context.Context, kind, text string)

	// Executor runs background jobs after dispatch (required for treat mode).
	// Inject providers/tools.MockExecutor or an OpenCode-backed executor.
	Executor JobExecutor

	// Session is optional durable coding/agent session (e.g. local OpenCode).
	Session Session
}

type Item struct {
	Kind    string // user | task_result | repair
	Msg     Msg
	TaskID  string
	Text    string // repair: previous plain text
	Trigger string // repair: original trigger
}

type OutEv struct {
	TMs  float64 `json:"t_ms"`
	Kind string  `json:"kind"` // ack|reply|dispatch_reply|result_reply|ask|dedup_notice|stay_quiet|suppressed_ack_dup
	Text string  `json:"text"`
	Turn int     `json:"turn"`
}
type CallRec struct {
	Round int            `json:"round"`
	Name  string         `json:"name"`
	Args  map[string]any `json:"args"`
	TMs   float64        `json:"t_ms"`
	Out   string         `json:"result,omitempty"`
}
type Usage struct{ Prompt, Completion, CacheHit int }
type Turn struct {
	No              int            `json:"turn"`
	Trigger         string         `json:"trigger"`
	StartMs         float64        `json:"start_ms"`
	EndMs           float64        `json:"end_ms"`
	Canceled        bool           `json:"canceled"`
	Calls           []CallRec      `json:"calls"`
	Contents        []string       `json:"round_contents"`
	Usage           []Usage        `json:"usage"`
	WindowSeq       []int64        `json:"window_seq"`
	FirstTerminalMs float64        `json:"first_terminal_ms"`
	Sections        map[string]int `json:"ctx_tokens_est"`
	BuildUs         float64        `json:"ctx_build_us"`
	winText         string
	WinText         string `json:"win_text,omitempty"`
	Repair          bool   `json:"repair,omitempty"`
	AckText         string `json:"ack_text,omitempty"`
}

type Runtime struct {
	cfg          Config
	ts           TaskStore
	ms           MemoryStore
	kv           KV
	conv         Conversation
	stats        *Stats
	t0           time.Time
	loop         *adk.TurnLoop[Item, *schema.Message]
	agent        adk.TypedAgent[*schema.Message]
	mu           sync.Mutex
	out          []OutEv
	turns        []*Turn
	cur          *Turn
	ack          map[int64]string // user seq -> ack text
	answeredUpTo int64
	inflight     int32
	activeTurn   int32
	jobs         sync.WaitGroup
	jobCancel    map[string]context.CancelFunc
	ackWG        sync.WaitGroup
	counters     map[string]int
	labels       map[string]string // memory label (m1) -> id for last snapshot
	cmodel       model.ToolCallingChatModel
	ackModel     model.BaseChatModel
	ackUsage     []Usage
	vbase, vwall int64
}

func (r *Runtime) now() float64 { return float64(time.Since(r.t0).Microseconds()) / 1000 }
func (r *Runtime) vnow() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vwall == 0 {
		return r.cfg.Case.ClockAtMs + time.Since(r.t0).Milliseconds()
	}
	return r.vbase + (time.Now().UnixMilli() - r.vwall)
}
func (r *Runtime) setVirtual(atMs int64) {
	r.mu.Lock()
	r.vbase, r.vwall = atMs, time.Now().UnixMilli()
	r.mu.Unlock()
}
func (r *Runtime) count(k string) { r.mu.Lock(); r.counters[k]++; r.mu.Unlock() }

func NewRuntime(ctx context.Context, cfg Config, ts TaskStore, ms MemoryStore, kv KV, conv Conversation, st *Stats) (*Runtime, error) {
	r := &Runtime{cfg: cfg, ts: timedTasks{ts, st}, ms: timedMem{ms, st}, kv: timedKV{kv, st}, conv: conv, stats: st, t0: time.Now(),
		ack: map[int64]string{}, jobCancel: map[string]context.CancelFunc{}, counters: map[string]int{}, labels: map[string]string{}}
	if cfg.Models == nil {
		return nil, fmt.Errorf("core: Config.Models is required (see providers/llm)")
	}
	var err error
	if r.cmodel, err = cfg.Models.Decision(ctx); err != nil {
		return nil, err
	}
	if r.ackModel, err = cfg.Models.Ack(ctx); err != nil {
		return nil, err
	}
	var tools []tool.BaseTool
	for _, in := range cfg.Case.Tools {
		tools = append(tools, &rtTool{info: in, r: r})
	}
	ac := &adk.ChatModelAgentConfig{Name: "employee", Description: "employee decision loop", Model: r.cmodel, MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}}}
	ac.ToolsConfig.ReturnDirectly = cfg.Case.Terminal
	if r.agent, err = adk.NewChatModelAgent(ctx, ac); err != nil {
		return nil, err
	}
	cpID := cfg.CheckpointID
	if cpID == "" && cfg.CheckPointStore != nil {
		cpID = cfg.Tenant + "|" + cfg.Scene
	}
	r.loop = adk.NewTurnLoop(adk.TurnLoopConfig[Item, *schema.Message]{
		GenInput: r.genInput,
		GenResume: func(ctx context.Context, l *adk.TurnLoop[Item, *schema.Message], interrupted, unhandled, newItems []Item) (*adk.GenResumeResult[Item, *schema.Message], error) {
			return &adk.GenResumeResult[Item, *schema.Message]{
				Consumed:  interrupted,
				Remaining: append(append([]Item{}, unhandled...), newItems...),
			}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[Item, *schema.Message], c []Item) (adk.TypedAgent[*schema.Message], error) {
			return r.agent, nil
		},
		OnAgentEvents: r.onEvents,
		Store:         cfg.CheckPointStore,
		CheckpointID:  cpID,
	})
	r.loop.Run(ctx)
	return r, nil
}

// ---------------- inbound ----------------
// UserMessage appends the message to the log (durable first), starts the ack path and pushes to the loop,
// preempting a running decision turn.
func (r *Runtime) UserMessage(ctx context.Context, speaker, text string, atMs int64) Msg {
	if atMs > 0 {
		r.setVirtual(atMs)
	}
	m, _ := r.conv.Append(ctx, r.cfg.Tenant, r.cfg.Scene, Msg{Role: "user", Speaker: speaker, Text: text, Kind: "msg", SourceRef: fmt.Sprintf("%s/msg%d", r.cfg.Scene, time.Now().UnixNano()%1e9), AtMs: r.vnow()})
	r.mu.Lock()
	r.out = append(r.out, OutEv{TMs: r.now(), Kind: "user_in", Text: text})
	r.mu.Unlock()
	if r.cfg.Ack {
		r.ackWG.Add(1)
		go func() { defer r.ackWG.Done(); r.doAck(ctx, m) }()
	}
	var opts []adk.PushOption[Item, *schema.Message]
	if atomic.LoadInt32(&r.activeTurn) == 1 {
		opts = append(opts, adk.WithPreempt[Item, *schema.Message](adk.AnySafePoint))
		r.count("preempt_pushes")
	}
	atomic.AddInt32(&r.inflight, 1)
	if ok, _ := r.loop.Push(Item{Kind: "user", Msg: m}, opts...); !ok {
		atomic.AddInt32(&r.inflight, -1)
	}
	return m
}

func (r *Runtime) pushResult(taskID string) {
	atomic.AddInt32(&r.inflight, 1)
	if ok, _ := r.loop.Push(Item{Kind: "task_result", TaskID: taskID}); !ok {
		atomic.AddInt32(&r.inflight, -1)
	}
}

func (r *Runtime) Idle(withJobs bool) bool {
	if atomic.LoadInt32(&r.inflight) != 0 || atomic.LoadInt32(&r.activeTurn) != 0 {
		return false
	}
	if withJobs {
		r.mu.Lock()
		n := len(r.jobCancel)
		r.mu.Unlock()
		return n == 0
	}
	return true
}

func (r *Runtime) WaitIdle(withJobs bool, max time.Duration) bool {
	end := time.Now().Add(max)
	stable := 0
	for time.Now().Before(end) {
		if r.Idle(withJobs) {
			stable++
			if stable >= 4 {
				return true
			}
		} else {
			stable = 0
		}
		time.Sleep(60 * time.Millisecond)
	}
	return false
}

func (r *Runtime) Stop() {
	r.loop.Stop()
	r.loop.Wait()
	r.ackWG.Wait()
}

// ---------------- deliver ----------------
func (r *Runtime) deliver(kind, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	turn := 0
	r.mu.Lock()
	if r.cur != nil {
		turn = r.cur.No
	}
	r.out = append(r.out, OutEv{TMs: r.now(), Kind: kind, Text: text, Turn: turn})
	r.mu.Unlock()
	_, _ = r.conv.Append(context.Background(), r.cfg.Tenant, r.cfg.Scene, Msg{Role: "assistant", Speaker: "employee", Text: text, Kind: kind, AtMs: r.vnow()})
	if r.cfg.OnOutbound != nil {
		r.cfg.OnOutbound(context.Background(), kind, text)
	}
}

// ---------------- ack path (thinking off, no tools) ----------------
const ackSystem = `你是钉钉同事的「秒回」通道。决策在另一条通道进行。结合最近几轮的发言人与顺序，用一句自然中文（不超过 24 字）让对方知道你在看。
硬性规则：
1. 只表达「在看/去办/去查」这类动作意图；绝不给结论、事实、数字、结果或承诺。
2. 绝不说已完成/已发送/已建好。
3. 问进度时说「我看下进度」一类即可。
4. 纯寒暄（Hi/你好/在吗/谢谢）、表情、不需要回应、或明显不是对你说的 → 只输出 <none>（主循环会自然回复）。
5. 不要机械重复「收到」；可带一点语境（对象/事项），但仍保持短。
只输出这句话本身，或 <none>。`

// isGreetingOrAckSkip detects pure greetings / thanks that should skip the stiff ack path.
func isGreetingOrAckSkip(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	t = strings.Trim(t, "！!。.~～、，,")
	if t == "" {
		return true
	}
	rs := []rune(t)
	if len(rs) <= 2 {
		hasWord := false
		for _, r := range rs {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x2e80 {
				hasWord = true
				break
			}
		}
		if !hasWord {
			return true
		}
	}
	switch t {
	case "hi", "hello", "hey", "yo", "sup",
		"你好", "您好", "嗨", "哈喽", "哈罗", "在吗", "在不在", "早上好", "中午好", "下午好", "晚上好", "晚安",
		"谢谢", "多谢", "感谢", "thx", "thanks", "thank you", "ok", "okay", "好的", "好", "嗯", "哦", "收到":
		return true
	}
	return false
}

func (r *Runtime) doAck(ctx context.Context, m Msg) {
	if isGreetingOrAckSkip(m.Text) {
		r.count("ack_skipped_greeting")
		return
	}
	var b strings.Builder
	b.WriteString("最近对话（发言人 + 顺序，供你措辞，不要复述整段）：\n")
	recent, _ := r.conv.Recent(ctx, r.cfg.Tenant, r.cfg.Scene, m.Seq, 8)
	for _, x := range recent {
		who := x.Speaker
		if who == "" {
			who = x.Role
		}
		fmt.Fprintf(&b, "- %s · %s: %s\n", vclock(x.AtMs), who, truncRunes(x.Text, 80))
	}
	if r.cfg.Registry {
		if ts, _ := r.ts.Find(ctx, r.cfg.Tenant, r.cfg.Scene, "", 4); len(ts) > 0 {
			b.WriteString("当前登记任务：\n")
			for _, t := range ts {
				if ActiveStatus(t.Status) {
					fmt.Fprintf(&b, "- 进行中：%s\n", truncRunes(t.Goal, 40))
				}
			}
		}
	}
	fmt.Fprintf(&b, "新消息（%s）：%s", m.Speaker, truncRunes(m.Text, 300))
	t0 := time.Now()
	resp, err := r.ackModel.Generate(ctx, []*schema.Message{schema.SystemMessage(ackSystem), schema.UserMessage(b.String())})
	if err != nil {
		r.count("ack_errors")
		return
	}
	txt := strings.TrimSpace(resp.Content)
	if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
		u := resp.ResponseMeta.Usage
		r.mu.Lock()
		r.ackUsage = append(r.ackUsage, Usage{u.PromptTokens, u.CompletionTokens, 0})
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.out = append(r.out, OutEv{TMs: r.now(), Kind: "ack_latency", Text: fmt.Sprintf("%dms", time.Since(t0).Milliseconds())})
	r.mu.Unlock()
	if txt == "" || strings.Contains(txt, "<none>") {
		r.count("ack_none")
		return
	}
	r.mu.Lock()
	r.ack[m.Seq] = txt
	r.mu.Unlock()
	r.deliver("ack", txt)
}

func (r *Runtime) ackFor(window []int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range window {
		if t, ok := r.ack[s]; ok {
			return t
		}
	}
	return ""
}

// ---------------- helpers ----------------
func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}

var cjk = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]`)

// EstTokens: rough DeepSeek-style estimate (CJK ≈0.7 tok/char, other ≈ 1 tok / 3.6 chars).
func EstTokens(s string) int {
	c := len(cjk.FindAllString(s, -1))
	o := len([]rune(s)) - c
	return int(math.Ceil(float64(c)*0.7 + float64(o)/3.6))
}

func bigrams(s string) map[string]bool {
	s = strings.ToLower(regexp.MustCompile(`[\s\p{P}]+`).ReplaceAllString(s, ""))
	rs := []rune(s)
	m := map[string]bool{}
	for i := 0; i+1 < len(rs); i++ {
		m[string(rs[i:i+2])] = true
	}
	return m
}
func jaccard(a, b string) float64 {
	x, y := bigrams(a), bigrams(b)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	in := 0
	for k := range x {
		if y[k] {
			in++
		}
	}
	return float64(in) / float64(len(x)+len(y)-in)
}

func (r *Runtime) Summary() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	cnt := map[string]int{}
	for k, v := range r.counters {
		cnt[k] = v
	}
	return map[string]any{"counters": cnt}
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

var _ = json.Marshal
