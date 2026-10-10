package im

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
	// Kind: "group" (default) uses --group; "dm" uses --open-dingtalk-id for the peer.
	Kind string
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

func (p *DWSProvider) loadSeen(convID string) {
	raw, err := os.ReadFile(p.seenPath(convID))
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil {
		return
	}
	for _, id := range ids {
		p.seen[id] = struct{}{}
	}
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
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("im.DWSProvider: closed")
	}
	if ch, ok := p.subs[convID]; ok {
		return ch, nil
	}
	p.loadSeen(convID)
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

	// Seed seen with current page so restart does not replay history.
	seeded, err := p.pollOnce(runCtx, convID, true)
	if err != nil {
		delete(p.subs, convID)
		close(ch)
		return nil, fmt.Errorf("dws subscribe seed: %w", err)
	}
	_ = seeded

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
				if _, err := p.pollOnce(runCtx, convID, false); err != nil {
					fmt.Fprintf(os.Stderr, "dws poll: %v\n", err)
				}
			}
		}
	}()
	return ch, nil
}

func (p *DWSProvider) pollOnce(ctx context.Context, convID string, seedOnly bool) (int, error) {
	args := []string{"chat", "+chat-messages", "--limit", fmt.Sprintf("%d", p.cfg.Limit), "-y", "-f", "json"}
	switch p.cfg.Kind {
	case "dm":
		args = append(args, "--open-dingtalk-id", convID)
	default:
		args = append(args, "--group", convID)
	}
	raw, err := p.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	var list dwsMsgList
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, fmt.Errorf("parse chat-messages: %w", err)
	}
	// API returns newest first; process oldest-first among new ones.
	msgs := list.Messages
	nNew := 0
	p.mu.Lock()
	ch := p.subs[convID]
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.MessageID == "" {
			continue
		}
		if _, ok := p.seen[m.MessageID]; ok {
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
			AtMs:    parseCreateTime(m.CreateTime),
		}
		nNew++
		if ch != nil {
			select {
			case ch <- in:
			case <-ctx.Done():
				p.saveSeen(convID)
				p.mu.Unlock()
				return nNew, ctx.Err()
			default:
				fmt.Fprintf(os.Stderr, "dws: inbound channel full, drop %s\n", m.MessageID)
			}
		}
	}
	p.saveSeen(convID)
	p.mu.Unlock()
	return nNew, nil
}

func (p *DWSProvider) Send(ctx context.Context, convID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	uuid := fmt.Sprintf("eel-%d", time.Now().UnixNano())
	args := []string{
		"chat", "+messages-send",
		"--as", "user",
		"--text", text,
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
