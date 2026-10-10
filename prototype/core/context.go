package core

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func vclock(ms int64) string {
	return time.UnixMilli(ms).In(time.FixedZone("CST", 8*3600)).Format("15:04:05")
}

func (r *Runtime) genInput(ctx context.Context, l *adk.TurnLoop[Item, *schema.Message], items []Item) (*adk.GenInputResult[Item, *schema.Message], error) {
	t0 := time.Now()
	cfg := r.cfg
	turn := &Turn{No: len(r.turns) + 1, StartMs: r.now(), Sections: map[string]int{}}
	hasUser, hasReaction := false, false
	var resultIDs []string
	repairText, repair := "", false
	for _, it := range items {
		switch it.Kind {
		case "user":
			hasUser = true
		case "reaction":
			hasReaction = true
			hasUser = true // reuse user window path; Trigger set below
		case "repair":
			repair, repairText = true, it.Text
			if it.Trigger == "user" || it.Trigger == "reaction" {
				hasUser = true
			}
			if it.Trigger == "reaction" {
				hasReaction = true
			}
			if it.TaskID != "" {
				resultIDs = append(resultIDs, it.TaskID)
			}
		default:
			resultIDs = append(resultIDs, it.TaskID)
		}
	}
	turn.Trigger = "task_result"
	if hasReaction {
		turn.Trigger = "reaction"
	} else if hasUser {
		turn.Trigger = "user"
	}
	turn.Repair = repair
	// ---- current window = user messages not yet answered by a completed decision turn
	recent, _ := r.conv.Recent(ctx, cfg.Tenant, cfg.Scene, 0, 60)
	var window []Msg
	firstWin := int64(1 << 60)
	r.mu.Lock()
	ans := r.answeredUpTo
	r.mu.Unlock()
	for _, m := range recent {
		if m.Role == "user" && m.Seq > ans {
			window = append(window, m)
			if m.Seq < firstWin {
				firstWin = m.Seq
			}
		}
	}
	if len(window) > 6 {
		window = window[len(window)-6:]
		firstWin = window[0].Seq
	}
	inWin := map[int64]bool{}
	for _, m := range window {
		inWin[m.Seq] = true
	}
	var nearby []Msg
	for _, m := range recent {
		if !inWin[m.Seq] {
			nearby = append(nearby, m)
		}
	}
	// assistant messages that occurred after firstWin are kept in nearby (they are replies/acks to the window)
	// ---- sections
	addendum := r.tx.T("runtime.addendum")
	msgs := []*schema.Message{schema.SystemMessage(cfg.Case.System), schema.SystemMessage(addendum)}
	msgs = append(msgs, schema.UserMessage(r.clockMsg()))
	if cfg.Case.Supervisor != "" {
		msgs = append(msgs, schema.UserMessage(cfg.Case.Supervisor))
	}
	speaker := ""
	if len(window) > 0 {
		speaker = window[len(window)-1].Speaker
	}
	var winText []string
	for _, m := range window {
		winText = append(winText, m.Text)
	}
	turn.winText = strings.Join(winText, "\n")
	turn.WinText = turn.winText
	for _, m := range window {
		turn.WindowSeq = append(turn.WindowSeq, m.Seq)
	}
	slim := r.tx.slimContextForFastPath(turn.winText)
	// memory / registry — omitted on greeting & trivial Q&A so old work cannot resurface
	if slim {
		memTxt := r.tx.T("runtime.memory_omitted")
		turn.Sections["memory"] = EstTokens(memTxt)
		msgs = append(msgs, schema.UserMessage(memTxt))
		turn.Sections["registry"] = 0
	} else {
		memTxt := r.memorySnapshot(ctx, speaker, turn.winText)
		turn.Sections["memory"] = EstTokens(memTxt)
		msgs = append(msgs, schema.UserMessage(memTxt))
		if cfg.Registry {
			regTxt := r.registrySnapshot(ctx, resultIDs)
			turn.Sections["registry"] = EstTokens(regTxt)
			msgs = append(msgs, schema.UserMessage(regTxt))
		}
	}
	// nearby conversation under token budget (newest kept).
	// Trivial Q&A / pure greetings: no history — prevents "1+1 → essay" / greeting reopen.
	budget := cfg.ConvTokens
	if slim {
		budget = 0
		nearby = nil
	}
	var lines []*schema.Message
	used, omitted := 0, 0
	for i := len(nearby) - 1; i >= 0; i-- {
		m := nearby[i]
		var line string
		if m.Role == "user" {
			line = fmt.Sprintf(`[History {"observed_at":%q,"speaker":%q}] %s`, vclock(m.AtMs), m.Speaker, m.Text)
		} else {
			line = fmt.Sprintf(`[History {"observed_at":%q,"evidence_scope":"%s"}] %s`, vclock(m.AtMs), r.tx.T("runtime.history.employee_scope"), m.Text)
		}
		tk := EstTokens(line)
		if used+tk > budget {
			omitted = i + 1
			break
		}
		used += tk
		var sm *schema.Message
		if m.Role == "user" {
			sm = schema.UserMessage(line)
		} else {
			sm = schema.AssistantMessage(line, nil)
		}
		lines = append([]*schema.Message{sm}, lines...)
	}
	if omitted > 0 {
		hdr := r.tx.T("runtime.history.omitted", "count", strconv.Itoa(omitted))
		lines = append([]*schema.Message{schema.UserMessage(hdr)}, lines...)
		used += EstTokens(hdr)
	}
	turn.Sections["conversation"] = used
	msgs = append(msgs, lines...)
	// current window / follow-ups
	srcRef := ""
	if len(window) > 0 {
		var segs, utt []string
		for _, m := range window {
			segs = append(segs, fmt.Sprintf(`{"source_ref":%q,"message":{"text":%q,"senderDisplayName":%q}}`, m.SourceRef, m.Text, m.Speaker))
			utt = append(utt, r.tx.T("runtime.window.utterance", "source_ref", m.SourceRef, "text", m.Text))
			srcRef = m.SourceRef
		}
		msgs = append(msgs, schema.UserMessage(r.tx.T("runtime.window", "segments", strings.Join(segs, ","), "utterances", strings.Join(utt, "\n"))))
	}
	for _, id := range resultIDs {
		if t, err := r.ts.Get(ctx, cfg.Tenant, id); err == nil {
			srcRef = t.SourceRef
			payload := fmt.Sprintf(`{"task_ref":%q,"state":%q,"result":%q,"source_ref":%q}`, t.ID, t.Status, truncRunes(t.Result, 300), t.SourceRef)
			msgs = append(msgs, schema.UserMessage(r.tx.T("runtime.task_result", "payload", payload, "source_ref", t.SourceRef)))
		}
	}
	if hasReaction {
		msgs = append(msgs, schema.UserMessage(r.tx.T("runtime.correction_wake")))
	}
	if hint := r.tx.hostRouteHint(turn.winText); hint != "" {
		msgs = append(msgs, schema.UserMessage(hint))
	}
	if repair {
		msgs = append(msgs, schema.UserMessage(r.tx.T("runtime.repair", "text", truncRunes(repairText, 600))))
	}
	_ = srcRef
	tot := 0
	for _, m := range msgs[2:] {
		tot += EstTokens(m.Content)
	}
	turn.Sections["dynamic_total_excl_system"] = tot
	turn.Sections["system+tools_fixed"] = EstTokens(cfg.Case.System) + EstTokens(addendum)
	turn.BuildUs = float64(time.Since(t0).Nanoseconds()) / 1000
	r.mu.Lock()
	r.cur = turn
	r.turns = append(r.turns, turn)
	r.mu.Unlock()
	atomic.StoreInt32(&r.activeTurn, 1)
	return &adk.GenInputResult[Item, *schema.Message]{Input: &adk.AgentInput{Messages: msgs}, Consumed: items}, nil
}

func (r *Runtime) memorySnapshot(ctx context.Context, speaker, text string) string {
	cfg := r.cfg
	var b strings.Builder
	b.WriteString(r.tx.T("memory.header") + "\n")
	r.mu.Lock()
	r.labels = map[string]string{}
	r.mu.Unlock()
	n := 0
	line := func(f *Fact) string {
		n++
		lb := fmt.Sprintf("m%d", n)
		r.mu.Lock()
		r.labels[lb] = f.ID
		r.mu.Unlock()
		who := r.tx.T("memory.audience.me")
		if f.Audience == "scene" {
			who = r.tx.T("memory.audience.scene")
		}
		src := r.tx.T("memory.source.user", "speaker", f.Speaker)
		if f.SpeakerRole == "employee" {
			src = r.tx.T("memory.source.employee")
		} else if f.SpeakerRole == "host" {
			src = r.tx.T("memory.source.host")
		}
		return r.tx.T("memory.line", "label", lb, "kind", r.tx.memoryKind(f.Kind), "audience", who,
			"date", time.UnixMilli(f.SaidMs).Format("01-02"), "source", src, "text", truncRunes(f.Text, 120))
	}
	seen := map[string]bool{}
	var pinned, related []string
	budget := cfg.MemTokens
	used := 0
	add := func(dst *[]string, f *Fact) {
		if seen[f.ID] {
			return
		}
		l := line(f)
		if used+EstTokens(l) > budget {
			return
		}
		used += EstTokens(l)
		seen[f.ID] = true
		*dst = append(*dst, l)
	}
	// Pinned preferences/decisions: prefer current speaker's "me" bag, then any "me" in scene, then scene-shared.
	for _, aud := range []string{"me", "scene"} {
		sub := speaker
		if aud == "scene" {
			sub = ""
		}
		rec, _ := r.ms.Recent(ctx, cfg.Tenant, cfg.Scene, sub, aud, 30)
		for _, f := range rec {
			if f.Kind == "preference" || f.Kind == "decision" {
				add(&pinned, f)
			}
		}
	}
	// Fallback: if speaker-scoped "me" bag was empty, pull recent preferences for any subject in this scene.
	if len(pinned) == 0 {
		rec, _ := r.ms.Recent(ctx, cfg.Tenant, cfg.Scene, "*", "me", 30)
		for _, f := range rec {
			if f.Kind == "preference" || f.Kind == "decision" {
				add(&pinned, f)
			}
		}
	}
	if cfg.Registry && text != "" {
		for _, kw := range keywords(text, 3) {
			for _, aud := range []string{"me", "scene"} {
				sub := speaker
				if aud == "scene" {
					sub = ""
				}
				hits, _ := r.ms.Search(ctx, cfg.Tenant, cfg.Scene, sub, aud, kw, 3)
				for _, f := range hits {
					if f.Kind != "preference" && f.Kind != "decision" {
						add(&related, f)
					}
				}
			}
		}
	}
	if len(pinned) > 0 {
		b.WriteString(r.tx.T("memory.pinned_header") + "\n" + strings.Join(pinned, "\n") + "\n")
	}
	if len(related) > 0 {
		b.WriteString(r.tx.T("memory.related_header") + "\n" + strings.Join(related, "\n") + "\n")
	} else {
		b.WriteString(r.tx.T("memory.related_none") + "\n")
	}
	b.WriteString(r.tx.T("memory.footer"))
	return b.String()
}

// splitRe splits on whitespace and ASCII/CJK punctuation (escaped so the source stays ASCII).
var splitRe = regexp.MustCompile(`[\s\x{FF0C}\x{3002}\x{FF01}\x{FF1F}\x{3001}\x{FF1B}\x{FF1A},.!?;:@\x{FF08}\x{FF09}()\x{300C}\x{300D}"\x{201C}\x{201D}]+`)

func keywords(text string, max int) []string {
	var out []string
	for _, p := range splitRe.Split(text, -1) {
		if len([]rune(p)) >= 2 {
			if len([]rune(p)) > 8 {
				p = string([]rune(p)[:8])
			}
			out = append(out, p)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}

func (r *Runtime) registrySnapshot(ctx context.Context, resultIDs []string) string {
	cfg := r.cfg
	ts, _ := r.ts.Find(ctx, cfg.Tenant, cfg.Scene, "", 40)
	var b strings.Builder
	b.WriteString(r.tx.T("runtime.registry.header") + "\n")
	if len(ts) == 0 {
		b.WriteString(r.tx.T("runtime.registry.empty"))
		return b.String()
	}
	used, shown := 0, 0
	now := NowMs()
	for _, t := range ts {
		rep := ""
		if t.Status == StDone {
			if _, ok, _ := r.kv.Get(ctx, "reported:"+t.ID); ok {
				rep = " reported_to_user=true"
			} else {
				rep = " reported_to_user=false"
			}
		}
		l := fmt.Sprintf("- task_ref=%s status=%s age=%ds goal=%q receipt=%s requester=%s%s", t.ID, t.Status, (now-t.CreatedMs)/1000, truncRunes(t.Goal, 70), t.Receipt, t.Requester, rep)
		if t.Result != "" {
			l += fmt.Sprintf(" result=%q", truncRunes(t.Result, 90))
		}
		if used+EstTokens(l) > cfg.RegTokens {
			break
		}
		used += EstTokens(l)
		shown++
		b.WriteString(l + "\n")
	}
	if shown < len(ts) {
		b.WriteString(r.tx.T("runtime.registry.more", "count", strconv.Itoa(len(ts)-shown)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Runtime) clockMsg() string {
	t := time.UnixMilli(r.vnow()).In(time.FixedZone("CST", 8*3600))
	wd := int(t.Weekday()+6) % 7
	d := func(x time.Time) string { return x.Format("2006-01-02") }
	return r.tx.T("runtime.clock", "payload", fmt.Sprintf(`{"at": %q, "today": %q, "yesterday": %q, "week_start": %q, "week_end": %q}`, t.Format("2006-01-02T15:04:05.000000-07:00"), d(t), d(t.AddDate(0, 0, -1)), d(t.AddDate(0, 0, -wd)), d(t.AddDate(0, 0, 6-wd))))
}
