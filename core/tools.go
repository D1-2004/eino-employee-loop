package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

var readOnly = map[string]bool{"find_tasks": true, "read_task": true, "read_context": true, "read_collection": true, "memory_lookup": true, "scene_config_get": true, "read_task_history": true, "get_supervisor": true, "read_human_cards": true, "describe_capabilities": true, "prepare_background": true}

type rtTool struct {
	info *schema.ToolInfo
	r    *Runtime
}

func (t *rtTool) Info(ctx context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func rid(p string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return p + hex.EncodeToString(b)
}

var rerunRe = regexp.MustCompile(`重新|再(跑|查|建|来|执行)|重跑|重做|重试|again|rerun`)

func idemKey(goal string) string {
	return NormHash(regexp.MustCompile(`[\s\p{P}]+`).ReplaceAllString(strings.ToLower(goal), ""))
}

func (t *rtTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	r := t.r
	name := t.info.Name
	cfg := r.cfg
	budget := TimeoutFor(ToolSpec{Effect: !readOnly[name]})
	if cfg.ToolTimeouts != nil {
		if d, ok := cfg.ToolTimeouts[name]; ok && d > 0 {
			budget = d
		}
	}
	// Keep tool budgets inside the wake wall-clock so one hung tool cannot stall T03-style.
	if cfg.TurnTimeout > 0 {
		half := cfg.TurnTimeout / 2
		if half < 15*time.Second {
			half = 15 * time.Second
		}
		if budget > half {
			budget = half
		}
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, budget)
	defer cancel()
	var a map[string]any
	_ = json.Unmarshal([]byte(args), &a)
	str := func(k string) string { s, _ := a[k].(string); return s }
	r.mu.Lock()
	turn := r.cur
	r.mu.Unlock()
	winText, winSeq := "", []int64(nil)
	if turn != nil {
		winText, winSeq = turn.winText, turn.WindowSeq
	}
	unavailable := map[string]any{"mock": true, "lookup_state": "unavailable", "reason": "no registry/memory backing in this configuration (control)"}
	var out any
	switch name {
	case "find_tasks":
		if !cfg.Registry {
			out = unavailable
			break
		}
		ts, _ := r.ts.Find(ctx, cfg.Tenant, cfg.Scene, str("query"), 8)
		c := []map[string]any{}
		for _, x := range ts {
			c = append(c, map[string]any{"task_ref": x.ID, "goal": truncRunes(x.Goal, 120), "state_at_snapshot": x.Status, "age_s": (NowMs() - x.CreatedMs) / 1000, "receipt": x.Receipt, "source_ref": x.SourceRef})
		}
		out = map[string]any{"candidates": c, "registry": "host", "note": "Host-recorded tasks of this conversation; empty list means none matched"}
	case "read_task":
		if !cfg.Registry {
			out = unavailable
			break
		}
		x, err := r.ts.Get(ctx, cfg.Tenant, str("task_ref"))
		if err != nil {
			out = map[string]any{"lookup_state": "not_found", "task_ref": str("task_ref")}
			break
		}
		evs := ""
		if x.DoneMs > 0 {
			evs = fmt.Sprintf("finished %ds after dispatch", (x.DoneMs-x.CreatedMs)/1000)
		}
		out = map[string]any{"task_ref": x.ID, "execution_state": x.Status, "goal": x.Goal, "receipt": x.Receipt, "result": x.Result, "timing": evs, "age_s": (NowMs() - x.CreatedMs) / 1000, "registry": "host"}
	case "dispatch_task":
		out = r.dispatch(ctx, a, winSeq)
	case "stop_task":
		if !cfg.Registry {
			out = map[string]any{"mock": true, "state": "proposal_accepted_not_executed"}
			break
		}
		id := str("task_ref")
		ok, _ := r.ts.Transition(ctx, cfg.Tenant, id, []string{StQueued, StRunning}, StCancelled, "stopped on user request")
		r.mu.Lock()
		if c := r.jobCancel[id]; c != nil {
			c()
		}
		r.mu.Unlock()
		out = map[string]any{"stopped": ok, "task_ref": id}
		if ok {
			r.deliver("reply", "好，已停掉任务 "+id+"。")
		}
	case "memory_lookup":
		if !cfg.Registry {
			out = unavailable
			break
		}
		sub, aud := "", "scene"
		if str("scope") == "me" {
			aud = "me"
			sub = r.lastSpeaker()
		}
		fs, _ := r.ms.Search(ctx, cfg.Tenant, cfg.Scene, sub, aud, str("query"), 6)
		recs := []map[string]any{}
		for _, f := range fs {
			recs = append(recs, map[string]any{"record_ref": f.ID, "kind": f.Kind, "text": truncRunes(f.Text, 160), "speaker": f.Speaker, "speaker_role": f.SpeakerRole, "evidence_scope": f.EvidenceScope})
		}
		out = map[string]any{"records": recs, "scope": str("scope")}
	case "memory_capture":
		if !cfg.Registry {
			out = map[string]any{"mock": true, "state": "proposal_accepted_not_executed"}
			break
		}
		aud := str("audience")
		sub := r.lastSpeaker()
		f := &Fact{Tenant: cfg.Tenant, Scene: cfg.Scene, Subject: sub, Audience: aud, Kind: str("type"), Text: str("subject") + "：" + str("quote"), Quote: str("quote"),
			Speaker: sub, SpeakerRole: "user", EvidenceScope: ScopeUserSaid, SourceRef: str("source_ref"), SaidMs: r.vnow()}
		if f.Kind == "" {
			f.Kind = "fact"
		}
		cur, created, err := r.ms.Put(ctx, f, winText)
		switch {
		case errors.Is(err, ErrBadQuote):
			r.count("memory_bad_quote_rejected")
			out = map[string]any{"saved": false, "error": "quote must be a verbatim excerpt of the sender's current message"}
		case errors.Is(err, ErrQuota):
			n, did, _ := r.ms.Compact(ctx, cfg.Tenant, cfg.Scene, sub, aud)
			r.count("memory_compactions")
			cur, created, err = r.ms.Put(ctx, f, winText)
			out = map[string]any{"saved": err == nil, "compacted": n, "digest": did}
		case err != nil:
			out = map[string]any{"saved": false, "error": err.Error()}
		default:
			if created {
				r.count("memory_written")
			}
			out = map[string]any{"saved": true, "record_ref": cur.ID, "deduplicated": !created}
		}
	case "memory_forget":
		if !cfg.Registry {
			out = map[string]any{"mock": true, "state": "proposal_accepted_not_executed"}
			break
		}
		ref := str("record_ref")
		r.mu.Lock()
		if id, ok := r.labels[ref]; ok {
			ref = id
		}
		r.mu.Unlock()
		n, _ := r.ms.Archive(ctx, cfg.Tenant, []string{ref}, "user_forget")
		out = map[string]any{"archived": n, "note": "archived, not deleted; original kept for audit"}
	case "read_context":
		if str("section") != "older_conversation" {
			out = unavailable
			break
		}
		ms, _ := r.conv.Recent(ctx, cfg.Tenant, cfg.Scene, 0, 40)
		var ls []string
		for _, m := range ms {
			ls = append(ls, fmt.Sprintf("[%s %s] %s", vclock(m.AtMs), m.Speaker, truncRunes(m.Text, 120)))
		}
		out = map[string]any{"older_conversation": ls}
	case "reply":
		kind := "reply"
		if turn != nil && turn.Trigger == "task_result" {
			kind = "result_reply"
		}
		r.deliver(kind, str("reply"))
		r.markReported(ctx)
		out = map[string]any{"state": "accepted_for_delivery", "delivery_proven": false}
	case "ask_human":
		txt := str("summary")
		if ch, ok := a["choice"].(map[string]any); ok {
			if q, _ := ch["question"].(string); q != "" {
				txt += " " + q
			}
		}
		r.deliver("ask", txt)
		out = map[string]any{"state": "accepted_for_delivery"}
	case "stay_quiet":
		if r.ackFor(winSeq) != "" {
			r.count("ack_misfire_stay_quiet")
		}
		out = map[string]any{"state": "recorded"}
	case "first_feedback":
		out = map[string]any{"mock": true, "intent_only": true, "acceptance": false}
	default:
		if readOnly[name] {
			out = map[string]any{"mock": true, "lookup_state": "unavailable", "reason": "no recorded fixture in this prototype"}
		} else {
			out = map[string]any{"mock": true, "state": "proposal_accepted_not_executed", "business_execution": false}
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.count("tool_timeout")
		b, _ := json.Marshal(map[string]any{
			"error": "tool_timeout", "tool": name, "budget_ms": budget.Milliseconds(),
			"note": "Host cancelled this tool call; prefer reply/ask_human/dispatch_task next.",
		})
		return string(b), nil
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (r *Runtime) lastSpeaker() string {
	ms, _ := r.conv.Recent(context.Background(), r.cfg.Tenant, r.cfg.Scene, 0, 10)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role == "user" {
			return ms[i].Speaker
		}
	}
	return ""
}

func (r *Runtime) markReported(ctx context.Context) {
	ts, _ := r.ts.Find(ctx, r.cfg.Tenant, r.cfg.Scene, "", 40)
	for _, t := range ts {
		if t.Status == StDone {
			_ = r.kv.Set(ctx, "reported:"+t.ID, "1", 24*time.Hour)
		}
	}
}

// dispatch is the heart of the duplicate-dispatch protection.
func (r *Runtime) dispatch(ctx context.Context, a map[string]any, winSeq []int64) any {
	cfg := r.cfg
	str := func(k string) string { s, _ := a[k].(string); return s }
	goal := str("goal")
	if goal == "" {
		goal = str("prompt")
	}
	idem := idemKey(goal)
	id := rid("tsk_")
	r.count("dispatch_calls")
	r.mu.Lock()
	turn := r.cur
	r.mu.Unlock()
	winText := ""
	if turn != nil {
		winText = turn.winText
	}
	if cfg.Registry {
		rerun := rerunRe.MatchString(winText)
		// 1. KV fast path: in-flight claim of the idempotency key (Redis SET NX in the cloud)
		claimed, _ := r.kv.SetNX(ctx, "dedup:"+cfg.Scene+":"+idem, id, cfg.DedupWindow)
		var existing *Task
		if !claimed {
			if prev, ok, _ := r.kv.Get(ctx, "dedup:"+cfg.Scene+":"+idem); ok {
				if x, err := r.ts.Get(ctx, cfg.Tenant, prev); err == nil && (ActiveStatus(x.Status) || (x.Status == StDone && !rerun)) {
					existing = x
				}
			}
		}
		// 2. similarity check against active / recently finished registry entries (catches rephrasings)
		if existing == nil {
			ts, _ := r.ts.Find(ctx, cfg.Tenant, cfg.Scene, "", 12)
			for _, x := range ts {
				recentDone := x.Status == StDone && time.Since(time.UnixMilli(x.DoneMs)) < cfg.DedupWindow && !rerun
				if (ActiveStatus(x.Status) || recentDone) && (x.IdemKey == idem || jaccard(x.Goal, goal) >= 0.6) {
					existing = x
					break
				}
			}
		}
		if existing != nil {
			r.count("dispatch_deduplicated")
			msg := fmt.Sprintf("这个还在办：「%s」，不重复派。", truncRunes(existing.Goal, 40))
			if existing.Status == StDone {
				msg = fmt.Sprintf("这个刚做完：「%s」。%s", truncRunes(existing.Goal, 36), truncRunes(existing.Result, 80))
			}
			r.deliver("dedup_notice", msg)
			return map[string]any{"accepted": false, "deduplicated": true, "task_ref": existing.ID, "state": existing.Status, "note": "Host found an equivalent task; nothing new was created. The Host already told the user."}
		}
	} else {
		idem = id // control: no idempotency
	}
	src := ""
	if turn != nil && len(turn.WindowSeq) > 0 {
		ms, _ := r.conv.Recent(ctx, cfg.Tenant, cfg.Scene, 0, 30)
		for _, m := range ms {
			if m.Seq == turn.WindowSeq[len(turn.WindowSeq)-1] {
				src = m.SourceRef
			}
		}
	}
	t := &Task{ID: id, Tenant: cfg.Tenant, Scene: cfg.Scene, SourceRef: src, Requester: r.lastSpeaker(), Goal: goal, Prompt: str("prompt"), IdemKey: idem, Receipt: "rcpt_" + id[4:], Status: StQueued}
	cur, created, err := r.ts.Create(ctx, t, cfg.DedupWindow)
	if err != nil {
		return map[string]any{"accepted": false, "error": err.Error()}
	}
	if !created {
		r.count("dispatch_deduplicated")
		r.deliver("dedup_notice", fmt.Sprintf("这个已经在办了（%s），不重复派。", cur.Status))
		return map[string]any{"accepted": false, "deduplicated": true, "task_ref": cur.ID, "state": cur.Status}
	}
	r.count("tasks_created")
	_ = r.ts.AddEvent(ctx, cfg.Tenant, id, "queued", "")
	r.emitProcess(EvAccepted, id, t.Receipt, str("reply"))
	// the model's own specific confirmation is delivered even when a generic ack was sent
	r.deliver("dispatch_reply", str("reply"))
	r.startJob(cur)
	return map[string]any{"accepted": true, "task_ref": id, "state": "queued", "receipt": t.Receipt, "note": "async receipt: the result arrives later as a Host event, not in this turn"}
}

func (r *Runtime) startJob(t *Task) {
	exec := r.cfg.Executor
	if exec == nil {
		r.count("executor_missing")
		_, _ = r.ts.Transition(context.Background(), r.cfg.Tenant, t.ID, []string{StQueued, StRunning}, StFailed, "no JobExecutor configured")
		return
	}
	jctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.jobCancel[t.ID] = cancel
	r.mu.Unlock()
	r.jobs.Add(1)
	go func() {
		defer r.jobs.Done()
		defer func() { r.mu.Lock(); delete(r.jobCancel, t.ID); r.mu.Unlock() }()
		ctx := context.Background()
		_, _ = r.ts.Transition(ctx, r.cfg.Tenant, t.ID, []string{StQueued}, StRunning, "")
		// honour JobDelay as a Host-side pacing hint before calling the executor
		if r.cfg.JobDelay > 0 {
			select {
			case <-time.After(r.cfg.JobDelay):
			case <-jctx.Done():
				return
			}
		}
		jobTO := r.cfg.JobTimeout
		if jobTO <= 0 {
			jobTO = 90 * time.Second
		}
		execCtx, execCancel := context.WithTimeout(jctx, jobTO)
		res, err := exec.Execute(execCtx, t)
		execCancel()
		if jctx.Err() != nil {
			return
		}
		if err != nil && execCtx.Err() != nil {
			err = fmt.Errorf("job timeout after %s: %w", jobTO, err)
		}
		if err != nil {
			_, _ = r.ts.Transition(ctx, r.cfg.Tenant, t.ID, []string{StRunning}, StFailed, err.Error())
			_ = r.ts.AddEvent(ctx, r.cfg.Tenant, t.ID, "failed", err.Error())
			r.emitProcess(EvFailed, t.ID, t.Receipt, err.Error())
			r.pushResult(t.ID)
			return
		}
		if ok, _ := r.ts.Transition(ctx, r.cfg.Tenant, t.ID, []string{StRunning}, StDone, res); ok {
			_ = r.ts.AddEvent(ctx, r.cfg.Tenant, t.ID, "done", "")
			r.emitProcess(EvDone, t.ID, t.Receipt, res)
			r.pushResult(t.ID)
		}
	}()
}

// ---------------- events ----------------
func (r *Runtime) onEvents(ctx context.Context, tc *adk.TurnContext[Item, *schema.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
	r.mu.Lock()
	turn := r.cur
	r.mu.Unlock()
	round := 0
	exceededBudget := false
	byID := map[string]int{}
	for {
		ev, ok := events.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			var ce *adk.CancelError
			if errors.As(ev.Err, &ce) {
				turn.Canceled = true
			} else if errors.Is(ev.Err, adk.ErrExceedMaxIterations) {
				// Eino stops before the next model call when MaxIterations is hit.
				exceededBudget = true
				r.count("max_iterations")
				turn.Contents = append(turn.Contents, "ERR:"+ev.Err.Error())
			} else {
				r.count("agent_errors")
				turn.Contents = append(turn.Contents, "ERR:"+ev.Err.Error())
			}
			continue
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mo := ev.Output.MessageOutput
		var m *schema.Message
		var err error
		if mo.IsStreaming {
			var chunks []*schema.Message
			for {
				c, e := mo.MessageStream.Recv()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					break
				}
				chunks = append(chunks, c)
			}
			if len(chunks) > 0 {
				m, err = schema.ConcatMessages(chunks)
			}
		} else {
			m, err = mo.GetMessage()
		}
		if err != nil || m == nil {
			continue
		}
		if mo.Role == schema.Tool {
			if i, ok := byID[m.ToolCallID]; ok {
				turn.Calls[i].Out = truncRunes(m.Content, 300)
			}
			continue
		}
		round++
		turn.Contents = append(turn.Contents, m.Content)
		if m.ResponseMeta != nil && m.ResponseMeta.Usage != nil {
			u := m.ResponseMeta.Usage
			turn.Usage = append(turn.Usage, Usage{u.PromptTokens, u.CompletionTokens, u.PromptTokenDetails.CachedTokens})
		}
		for _, tcall := range m.ToolCalls {
			var args map[string]any
			_ = json.Unmarshal([]byte(tcall.Function.Arguments), &args)
			cr := &CallRec{Round: round, Name: tcall.Function.Name, Args: args, TMs: r.now()}
			turn.Calls = append(turn.Calls, *cr)
			byID[tcall.ID] = len(turn.Calls) - 1
			if r.cfg.Case.Terminal[tcall.Function.Name] && turn.FirstTerminalMs == 0 {
				turn.FirstTerminalMs = cr.TMs
			}
		}
	}
	turn.EndMs = r.now()
	if !turn.Canceled && (turn.Trigger == "user" && (hasTermEarly(turn, r) || turn.Repair)) {
		r.mu.Lock()
		for _, s := range turn.WindowSeq {
			if s > r.answeredUpTo {
				r.answeredUpTo = s
			}
		}
		r.mu.Unlock()
	}
	turn.AckText = r.ackFor(turn.WindowSeq)
	// protocol repair (same idea as the eval loop's protocol_repair): a turn that ends with plain text and no
	// decision tool delivers nothing; give the model ONE chance to call a decision tool.
	hasTerm := false
	for _, c := range turn.Calls {
		if r.cfg.Case.Terminal[c.Name] {
			hasTerm = true
		}
	}
	maxR := r.cfg.MaxModelRoundsPerTurn
	if maxR <= 0 {
		maxR = 4
	}
	budgetExhausted := round >= maxR || exceededBudget
	canFallback := r.cfg.BudgetFallbackDispatch && (turn.Trigger == "user" || turn.Trigger == "reaction")

	if !turn.Canceled && !hasTerm && !turn.Repair {
		prev := ""
		for _, c := range turn.Contents {
			if strings.TrimSpace(c) != "" {
				prev = c
			}
		}
		if canFallback && budgetExhausted {
			r.count("budget_exhausted")
			r.hostAutoDispatch(ctx, turn)
		} else if len(tc.Consumed) > 0 {
			var tid string
			for _, it := range tc.Consumed {
				if it.Kind == "task_result" || (it.Kind == "repair" && it.TaskID != "") {
					tid = it.TaskID
				}
			}
			r.count("protocol_repairs")
			atomic.AddInt32(&r.inflight, 1)
			if ok, _ := r.loop.Push(Item{Kind: "repair", Text: prev, TaskID: tid, Trigger: turn.Trigger}); !ok {
				atomic.AddInt32(&r.inflight, -1)
			}
		}
	} else if !turn.Canceled && !hasTerm && turn.Repair {
		r.count("protocol_failures")
		if canFallback {
			r.count("budget_exhausted")
			r.hostAutoDispatch(ctx, turn)
		} else {
			r.ensureUserVisible(turn, "这轮没谈拢，你再说具体一点我再办。")
		}
	}

	// stay_quiet on a real user ask delivers nothing — Host forces a clarifying ask (T03).
	if !turn.Canceled && (turn.Trigger == "user" || turn.Trigger == "reaction") && stayQuietOnly(turn, r) && !r.turnDelivered(turn.No) {
		r.count("host_forced_visible")
		r.deliver("ask", clarifyingAsk(turn.winText))
		r.markWindowAnswered(turn)
	}
	if turn.Canceled && (turn.Trigger == "user" || turn.Trigger == "reaction") && !r.turnDelivered(turn.No) {
		r.count("turn_timeout_reply")
		r.deliver("reply", "这轮超时了，你补一句具体要我办啥，我接着干。")
		r.markWindowAnswered(turn)
	}

	atomic.StoreInt32(&r.activeTurn, 0)
	atomic.AddInt32(&r.inflight, -int32(len(tc.Consumed)))
	return nil
}

func stayQuietOnly(turn *Turn, r *Runtime) bool {
	if turn == nil || len(turn.Calls) == 0 {
		return false
	}
	sawQuiet, sawOtherTerm := false, false
	for _, c := range turn.Calls {
		if !r.cfg.Case.Terminal[c.Name] {
			continue
		}
		if c.Name == "stay_quiet" {
			sawQuiet = true
		} else {
			sawOtherTerm = true
		}
	}
	return sawQuiet && !sawOtherTerm
}

func (r *Runtime) turnDelivered(turnNo int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.out {
		if e.Turn == turnNo && e.Kind != "user_in" && e.Kind != "reaction_in" && e.Kind != "ack_latency" {
			switch e.Kind {
			case "ack", "reply", "dispatch_reply", "result_reply", "ask", "dedup_notice":
				return true
			}
		}
	}
	return false
}

func (r *Runtime) ensureUserVisible(turn *Turn, text string) {
	if turn == nil || r.turnDelivered(turn.No) {
		return
	}
	r.deliver("reply", text)
	r.markWindowAnswered(turn)
}

func (r *Runtime) markWindowAnswered(turn *Turn) {
	if turn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range turn.WindowSeq {
		if s > r.answeredUpTo {
			r.answeredUpTo = s
		}
	}
}

func hasTermEarly(t *Turn, r *Runtime) bool {
	for _, c := range t.Calls {
		if r.cfg.Case.Terminal[c.Name] {
			return true
		}
	}
	return false
}


// hostAutoDispatch is Host policy when a wake burns its model-round budget
// without a terminal tool. Prefer dispatching actionable window text over stalling.
// Channel-specific signals (emoji, etc.) belong in adapters; Core only sees Trigger.
func (r *Runtime) hostAutoDispatch(ctx context.Context, turn *Turn) {
	goal := strings.TrimSpace(turn.winText)
	if goal == "" {
		r.deliver("reply", "这轮没谈拢，你再说具体一点我再办。")
		r.emitProcess(EvBudget, "", "", "empty_goal")
		return
	}
	if isGreeting(goal) || isTrivialQA(goal) || isStatusLookup(goal) {
		// Should have been a direct reply; do not invent a background job.
		r.deliver("reply", "这轮没谈拢，你再说具体一点我再办。")
		r.count("budget_fallback_skip")
		r.emitProcess(EvBudget, "", "", "skip_non_actionable")
		return
	}
	if isAmbiguousShort(goal) || isVagueFragment(goal) {
		r.deliver("ask", clarifyingAsk(goal))
		r.count("budget_fallback_ask")
		r.emitProcess(EvBudget, "", "", "ask_ambiguous")
		return
	}
	if turn.Trigger == "reaction" {
		// Correction wake without a clean terminal — ask once for the intended fix.
		r.deliver("reply", "收到纠正。补一句期望结果，我马上按你的意思重做。")
		r.count("budget_fallback_ask")
		r.emitProcess(EvBudget, "", "", "ask_correction")
		return
	}
	r.count("budget_fallback_dispatch")
	r.emitProcess(EvBudget, "", "", "auto_dispatch")
	_ = r.dispatch(ctx, map[string]any{
		"goal":   goal,
		"prompt": goal,
		"reply":  "这轮时间到了，我先按原话派出去办，有结果再回你。",
	}, turn.WindowSeq)
}

func (r *Runtime) emitProcess(kind, taskID, receipt, text string) {
	if r.cfg.Events == nil {
		return
	}
	fieldID := r.cfg.Field.ID
	if fieldID == "" {
		fieldID = r.cfg.Scene
	}
	_ = r.cfg.Events.Emit(context.Background(), ProcessEvent{
		Kind: kind, FieldID: fieldID, TaskID: taskID, Receipt: receipt, Text: text, AtMs: NowMs(),
	})
}
