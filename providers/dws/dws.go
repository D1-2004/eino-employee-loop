package dws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DWSConfig configures the DingTalk Workspace CLI poll/send provider.
// Secrets stay in dws profiles; this package only passes --profile.
type DWSConfig struct {
	// Profile is the dws --profile value (corpId:userId). Empty = current dws profile.
	Profile string
	// DWSBin is the dws binary path. Empty = "dws" on PATH, or $HOME/.local/bin/dws.
	DWSBin string
	// PollEvery is the catch-up interval. Default 8s.
	PollEvery time.Duration
	// Limit is +chat-messages --limit per poll. Default 20.
	Limit int
	// SeenPath persists seen message IDs. Empty = ~/.local/share/eino-employee-loop/seen-<cidhash>.json
	SeenPath string
	// SelfNames: skip inbound when sender display name matches (case-sensitive).
	// Also skip when MsgID was recorded as our own outbound.
	SelfNames []string
	// SelfIDs: skip inbound when senderId matches (openDingTalkId). Preferred over names.
	SelfIDs []string
	// Kind: "group" (default) uses --group; "dm" uses --open-dingtalk-id for the peer.
	// Note: single-chat openConversationId (cid…) still uses --group; dws rejects cid as --open-dingtalk-id.
	Kind string
	// CatchUp: when the seen store is empty (first subscribe / after wipe), still deliver
	// messages newer than now-CatchUp instead of seeding them away. Default 10m.
	// Fixes the case where the consumer was stuck/deadlocked while Hi already arrived.
	CatchUp time.Duration
}

// DWSProvider polls dws chat +chat-messages and sends via +messages-send.
type DWSProvider struct {
	cfg    DWSConfig
	mu     sync.Mutex
	seen   map[string]struct{}
	subs   map[string]chan Inbound
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

func NewDWSProvider(cfg DWSConfig) *DWSProvider {
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 8 * time.Second
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 20
	}
	if cfg.Kind == "" {
		cfg.Kind = "group"
	}
	if cfg.DWSBin == "" {
		cfg.DWSBin = resolveDWSBin()
	}
	if cfg.CatchUp <= 0 {
		cfg.CatchUp = 10 * time.Minute
	}
	return &DWSProvider{
		cfg:  cfg,
		seen: map[string]struct{}{},
		subs: map[string]chan Inbound{},
	}
}

func resolveDWSBin() string {
	if p, err := exec.LookPath("dws"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	cand := filepath.Join(home, ".local", "bin", "dws")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return "dws"
}

func (p *DWSProvider) seenPath(convID string) string {
	if p.cfg.SeenPath != "" {
		return p.cfg.SeenPath
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "eino-employee-loop")
	_ = os.MkdirAll(dir, 0o700)
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, convID)
	if len(safe) > 48 {
		safe = safe[:48]
	}
	return filepath.Join(dir, "seen-"+safe+".json")
}

func (p *DWSProvider) loadSeen(convID string) int {
	raw, err := os.ReadFile(p.seenPath(convID))
	if err != nil {
		return 0
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil {
		return 0
	}
	for _, id := range ids {
		p.seen[id] = struct{}{}
	}
	return len(ids)
}

func (p *DWSProvider) saveSeen(convID string) {
	ids := make([]string, 0, len(p.seen))
	for id := range p.seen {
		ids = append(ids, id)
	}
	if len(ids) > 800 {
		ids = ids[len(ids)-800:]
	}
	raw, _ := json.Marshal(ids)
	_ = os.WriteFile(p.seenPath(convID), raw, 0o600)
}

func (p *DWSProvider) dwsArgs(args ...string) []string {
	out := []string{}
	if p.cfg.Profile != "" {
		out = append(out, "--profile", p.cfg.Profile)
	}
	return append(out, args...)
}

func (p *DWSProvider) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.cfg.DWSBin, p.dwsArgs(args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("dws %v: %s", args, trunc(msg, 400))
	}
	return stdout.Bytes(), nil
}

type dwsMsgList struct {
	Messages []dwsMsg `json:"messages"`
}
type dwsMsg struct {
	ConversationID string `json:"conversationId"`
	CreateTime     string `json:"createTime"`
	MessageID      string `json:"messageId"`
	Sender         string `json:"sender"`
	SenderID       string `json:"senderId"`
	Text           string `json:"text"`
}

func parseCreateTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UnixMilli()
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	}
	loc := time.FixedZone("CST", 8*3600)
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t.UnixMilli()
		}
		if t, err := time.Parse(l, s); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}

func (p *DWSProvider) isSelf(m dwsMsg) bool {
	for _, id := range p.cfg.SelfIDs {
		if id != "" && m.SenderID == id {
			return true
		}
	}
	for _, n := range p.cfg.SelfNames {
		if n != "" && m.Sender == n {
			return true
		}
	}
	return false
}

func (p *DWSProvider) Subscribe(ctx context.Context, convID string) (<-chan Inbound, error) {
	if strings.TrimSpace(convID) == "" {
		return nil, fmt.Errorf("im.DWSProvider: empty convID")
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("im.DWSProvider: closed")
	}
	if ch, ok := p.subs[convID]; ok {
		p.mu.Unlock()
		return ch, nil
	}
	hadSeen := p.loadSeen(convID)
	ch := make(chan Inbound, 32)
	p.subs[convID] = ch

	runCtx, cancel := context.WithCancel(ctx)
	prev := p.cancel
	p.cancel = func() {
		cancel()
		if prev != nil {
			prev()
		}
	}
	p.mu.Unlock() // MUST unlock before pollOnce — pollOnce takes the same mutex (non-reentrant).

	// Seed seen with current page so restart does not replay history.
	// When seen store was empty, still deliver recent messages (CatchUp window).
	seedOnly := true
	catchUpCutoff := int64(0)
	if hadSeen == 0 {
		seedOnly = false
		catchUpCutoff = time.Now().Add(-p.cfg.CatchUp).UnixMilli()
		fmt.Fprintf(os.Stderr, "dws: empty seen store — catch-up messages newer than %s\n",
			time.UnixMilli(catchUpCutoff).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"))
	}
	seeded, err := p.pollOnce(runCtx, convID, seedOnly, catchUpCutoff)
	if err != nil {
		p.mu.Lock()
		delete(p.subs, convID)
		p.mu.Unlock()
		close(ch)
		return nil, fmt.Errorf("dws subscribe seed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "dws: subscribe ok cid=%s kind=%s seeded_or_new=%d had_seen=%d\n",
		trunc(convID, 40), p.cfg.Kind, seeded, hadSeen)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(ch)
		t := time.NewTicker(p.cfg.PollEvery)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				n, err := p.pollOnce(runCtx, convID, false, 0)
				if err != nil {
					fmt.Fprintf(os.Stderr, "dws poll: %v\n", err)
				} else if n > 0 {
					fmt.Fprintf(os.Stderr, "dws poll: +%d new\n", n)
				}
			}
		}
	}()
	return ch, nil
}

// pollOnce fetches one page. seedOnly marks everything seen without delivering.
// If catchUpCutoff > 0 and !seedOnly, messages older than cutoff are only seeded;
// newer ones are delivered (first-run catch-up).
func (p *DWSProvider) pollOnce(ctx context.Context, convID string, seedOnly bool, catchUpCutoff int64) (int, error) {
	args := []string{"chat", "+chat-messages", "--limit", fmt.Sprintf("%d", p.cfg.Limit), "-y", "-f", "json"}
	switch p.cfg.Kind {
	case "dm":
		args = append(args, "--open-dingtalk-id", convID)
	default:
		// openConversationId for both group and singleChat (1:1 still uses --group with cid…).
		args = append(args, "--group", convID)
	}
	raw, err := p.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	var list dwsMsgList
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, fmt.Errorf("parse chat-messages: %w (head=%q)", err, trunc(string(raw), 120))
	}
	// API returns newest first; process oldest-first among new ones.
	msgs := list.Messages
	nNew := 0
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := p.subs[convID]
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.MessageID == "" {
			continue
		}
		if _, ok := p.seen[m.MessageID]; ok {
			continue
		}
		at := parseCreateTime(m.CreateTime)
		// First-run catch-up: older than cutoff → seed only.
		if !seedOnly && catchUpCutoff > 0 && at < catchUpCutoff {
			p.seen[m.MessageID] = struct{}{}
			continue
		}
		p.seen[m.MessageID] = struct{}{}
		if seedOnly {
			continue
		}
		if p.isSelf(m) {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		in := Inbound{
			ConvID:  convID,
			MsgID:   m.MessageID,
			Speaker: m.Sender,
			Text:    text,
			AtMs:    at,
		}
		nNew++
		if ch != nil {
			select {
			case ch <- in:
			case <-ctx.Done():
				p.saveSeen(convID)
				return nNew, ctx.Err()
			default:
				fmt.Fprintf(os.Stderr, "dws: inbound channel full, drop %s\n", m.MessageID)
			}
		}
	}
	p.saveSeen(convID)
	return nNew, nil
}

// Send posts as the logged-in human profile ( --as user ), Markdown body, no AI badge.
func (p *DWSProvider) Send(ctx context.Context, convID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	uuid := fmt.Sprintf("eel-%d", time.Now().UnixNano())
	args := []string{
		"chat", "+messages-send",
		"--as", "user",
		"--markdown", text,
		"--ai-tag=false",
		"--uuid", uuid,
		"-y", "-f", "json",
	}
	switch p.cfg.Kind {
	case "dm":
		args = append(args, "--open-dingtalk-id", convID)
	default:
		args = append(args, "--group", convID)
	}
	_, err := p.run(ctx, args...)
	return err
}

func (p *DWSProvider) MarkRead(ctx context.Context, convID, msgID string) error {
	if strings.TrimSpace(convID) == "" || strings.TrimSpace(msgID) == "" {
		return nil
	}
	_, err := p.run(ctx, "chat", "+conversation-mark-read",
		"--conversation-id", convID,
		"--message-id", msgID,
		"-y", "-f", "json")
	return err
}

func (p *DWSProvider) ClearRedPoint(ctx context.Context, convID string) error {
	if strings.TrimSpace(convID) == "" {
		return nil
	}
	_, err := p.run(ctx, "chat", "+conversation-clear-red-point",
		"--conversation-id", convID,
		"-y", "-f", "json")
	return err
}

// FetchHistory returns recent messages oldest-first (excluding empty text).
func (p *DWSProvider) FetchHistory(ctx context.Context, convID string, limit int) ([]Inbound, error) {
	if limit <= 0 {
		limit = 40
	}
	args := []string{"chat", "+chat-messages", "--limit", fmt.Sprintf("%d", limit), "-y", "-f", "json"}
	switch p.cfg.Kind {
	case "dm":
		args = append(args, "--open-dingtalk-id", convID)
	default:
		args = append(args, "--group", convID)
	}
	raw, err := p.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var list dwsMsgList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse chat-messages: %w", err)
	}
	out := make([]Inbound, 0, len(list.Messages))
	// API newest-first → reverse to oldest-first
	for i := len(list.Messages) - 1; i >= 0; i-- {
		m := list.Messages[i]
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		out = append(out, Inbound{
			ConvID:  convID,
			MsgID:   m.MessageID,
			Speaker: m.Sender,
			Text:    text,
			AtMs:    parseCreateTime(m.CreateTime),
		})
	}
	return out, nil
}

func (p *DWSProvider) Close() error {
	p.mu.Lock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
