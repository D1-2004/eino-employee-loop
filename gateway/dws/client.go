// Package dws is the DingTalk Workspace CLI (dws) side of the Event
// Gateway. It has three layers, each usable alone:
//
//   - Client wraps the dws binary: subscribe to a conversation (DingTalk
//     Stream via `dws event +listen-im`, or polling), send, fetch history,
//     mark read and poll reactions. It speaks provider Messages only.
//   - Normalizer is a pure function from a provider Message to a
//     contract.Event: messages addressed to the employee become
//     message.addressed, other group messages message.observed, and a
//     negative reaction on the employee's message reaction.added. Scene ids
//     are minted by an injected SceneResolver, never the raw conversation id.
//   - Listener pumps a Client subscription through the Normalizer into a
//     contract.EventSink; Deliverer sends the loop's contract.Delivery values
//     through the Client (contract.Deliverer).
//
// Which reactions count as negative is gateway configuration
// (reactions.json, LexiconConfig), not localized text.
//
// The package imports contract only; it never imports loop, task-manager
// or prototype packages.
package dws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Conversation kinds of a Client.
const (
	KindGroup = "group"
	KindDM    = "dm"
)

// Transports of a Client.
const (
	TransportStream = "stream"
	TransportPoll   = "poll"
)

// Client errors.
var (
	ErrEmptyConversation = errors.New("dws: empty_conversation")
	ErrClosed            = errors.New("dws: closed")
	ErrCommand           = errors.New("dws: command_failed")
	ErrParse             = errors.New("dws: parse_failed")
)

// Config configures the dws CLI client. Secrets stay in dws profiles; this
// package only passes --profile.
type Config struct {
	// Profile is the dws --profile value (corpId:userId). Empty uses the
	// current dws profile.
	Profile string
	// Bin is the dws binary path. Empty uses "dws" on PATH, else
	// $HOME/.local/bin/dws.
	Bin string
	// PollEvery is the catch-up interval of the poll transport. Default 8s.
	PollEvery time.Duration
	// Limit is the +chat-messages page size per poll. Default 20.
	Limit int
	// SeenPath persists seen message ids. Empty uses
	// ~/.local/share/eino-employee-loop/seen-<conversation>.json.
	SeenPath string
	// SelfNames and SelfIDs drop the employee's own messages and reactions
	// at the source (ids preferred over display names).
	SelfNames []string
	SelfIDs   []string
	// Kind is KindGroup (default, --group) or KindDM (--open-dingtalk-id of
	// the peer). A single-chat openConversationId still uses KindGroup: dws
	// rejects a conversation id as --open-dingtalk-id.
	Kind string
	// CatchUp delivers messages newer than now-CatchUp when the seen store
	// is empty (first subscribe) instead of only seeding them. Default 10m.
	CatchUp time.Duration
	// Transport is TransportStream (default) or TransportPoll (tests and
	// emergencies only).
	Transport string
	// AllowPollFallback switches a repeatedly failing stream to polling.
	// Default false: stream failures only restart with backoff.
	AllowPollFallback bool
	// StreamRestartMin is the minimum wait before restarting a stream.
	// Default 30s; cooldown hints from dws win when larger.
	StreamRestartMin time.Duration
	// Logger receives operational logs (machine message keys). Default
	// slog.Default().
	Logger *slog.Logger
	// Negative classifies polled reactions; nil uses
	// DefaultLexicon().Negative.
	Negative func(name, text string) bool
}

// MessageKind classifies a provider Message.
type MessageKind string

const (
	MessageText     MessageKind = "message"
	MessageReaction MessageKind = "reaction"
)

// Message is one inbound provider record: a chat message or a reaction.
// Addressing fields are filled when dws reports them.
type Message struct {
	ConversationID string
	MessageID      string
	SenderID       string
	SenderName     string
	Text           string
	AtMs           int64
	Kind           MessageKind
	// Single marks a one-to-one conversation.
	Single bool
	// MentionIDs are the provider ids mentioned in the message;
	// MentionsSelf is the provider's own "mentions the receiver" flag.
	MentionIDs   []string
	MentionsSelf bool
	// ReplyToMessageID and ReplyToSenderID describe a quoted message.
	ReplyToMessageID string
	ReplyToSenderID  string
	// Reaction is the reaction name (or its text when the name is empty)
	// and TargetMessageID the message that received it.
	Reaction        string
	TargetMessageID string
}

// Client drives the dws CLI for one or more conversations of one kind.
type Client struct {
	cfg      Config
	log      *slog.Logger
	mu       sync.Mutex
	seen     map[string]struct{}
	outbound []string // recent sent message ids, for the reaction poll
	subs     map[string]chan Message
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closed   bool
}

// NewClient applies defaults and returns a Client.
func NewClient(cfg Config) *Client {
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 8 * time.Second
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 20
	}
	if cfg.Kind == "" {
		cfg.Kind = KindGroup
	}
	if cfg.Bin == "" {
		cfg.Bin = resolveBin()
	}
	if cfg.CatchUp <= 0 {
		cfg.CatchUp = 10 * time.Minute
	}
	if cfg.Transport == "" {
		cfg.Transport = TransportStream
	}
	if cfg.StreamRestartMin <= 0 {
		cfg.StreamRestartMin = 30 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{cfg: cfg, log: log, seen: map[string]struct{}{}, subs: map[string]chan Message{}}
}

// Config returns the effective configuration.
func (c *Client) Config() Config { return c.cfg }

func resolveBin() string {
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

func (c *Client) seenPath(convID string) string {
	if c.cfg.SeenPath != "" {
		return c.cfg.SeenPath
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

func (c *Client) loadSeen(convID string) int {
	raw, err := os.ReadFile(c.seenPath(convID))
	if err != nil {
		return 0
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil {
		return 0
	}
	for _, id := range ids {
		c.seen[id] = struct{}{}
	}
	return len(ids)
}

func (c *Client) saveSeen(convID string) {
	ids := make([]string, 0, len(c.seen))
	for id := range c.seen {
		ids = append(ids, id)
	}
	if len(ids) > 800 {
		ids = ids[len(ids)-800:]
	}
	raw, _ := json.Marshal(ids)
	_ = os.WriteFile(c.seenPath(convID), raw, 0o600)
}

func (c *Client) args(args ...string) []string {
	out := []string{}
	if c.cfg.Profile != "" {
		out = append(out, "--profile", c.cfg.Profile)
	}
	return append(out, args...)
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.cfg.Bin, c.args(args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%w: %v: %s", ErrCommand, args, trunc(msg, 400))
	}
	return stdout.Bytes(), nil
}

// target appends the conversation selector for the configured kind.
func (c *Client) target(args []string, convID string) []string {
	if c.cfg.Kind == KindDM {
		return append(args, "--open-dingtalk-id", convID)
	}
	return append(args, "--group", convID)
}

type msgList struct {
	Messages []listedMsg `json:"messages"`
}

type listedMsg struct {
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
	layouts := []string{"2006-01-02 15:04:05", time.RFC3339, time.RFC3339Nano}
	// dws prints wall-clock times in China Standard Time.
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

func (c *Client) isSelf(senderID, senderName string) bool {
	for _, id := range c.cfg.SelfIDs {
		if id != "" && senderID == id {
			return true
		}
	}
	for _, n := range c.cfg.SelfNames {
		if n != "" && senderName == n {
			return true
		}
	}
	return false
}

func (c *Client) fromListed(convID string, m listedMsg, text string) Message {
	return Message{
		ConversationID: convID, MessageID: m.MessageID, SenderID: m.SenderID, SenderName: m.Sender,
		Text: text, AtMs: parseCreateTime(m.CreateTime), Kind: MessageText, Single: c.cfg.Kind == KindDM,
	}
}

// Subscribe starts listening to one conversation. The channel closes when
// ctx ends or the client closes. A second Subscribe for the same
// conversation returns the same channel.
func (c *Client) Subscribe(ctx context.Context, convID string) (<-chan Message, error) {
	if strings.TrimSpace(convID) == "" {
		return nil, ErrEmptyConversation
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if ch, ok := c.subs[convID]; ok {
		c.mu.Unlock()
		return ch, nil
	}
	hadSeen := c.loadSeen(convID)
	ch := make(chan Message, 32)
	c.subs[convID] = ch
	runCtx, cancel := context.WithCancel(ctx)
	prev := c.cancel
	c.cancel = func() {
		cancel()
		if prev != nil {
			prev()
		}
	}
	// Unlock before pollOnce: it takes the same (non-reentrant) mutex.
	c.mu.Unlock()

	// Seed seen ids with the current page so a restart does not replay
	// history; with an empty seen store, still deliver the catch-up window.
	seedOnly, cutoff := true, int64(0)
	if hadSeen == 0 {
		seedOnly = false
		cutoff = time.Now().Add(-c.cfg.CatchUp).UnixMilli()
		c.log.Info("dws.subscribe.catch_up", "since", time.UnixMilli(cutoff).UTC())
	}
	seeded, err := c.pollOnce(runCtx, convID, seedOnly, cutoff)
	if err != nil {
		c.mu.Lock()
		delete(c.subs, convID)
		c.mu.Unlock()
		close(ch)
		return nil, err
	}
	c.log.Info("dws.subscribe.ok", "transport", c.cfg.Transport, "conversation", trunc(convID, 40),
		"kind", c.cfg.Kind, "seeded_or_new", seeded, "had_seen", hadSeen)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(ch)
		if c.cfg.Transport == TransportPoll {
			c.runPollLoop(runCtx, convID, false)
			return
		}
		c.runStream(runCtx, convID, ch)
	}()
	return ch, nil
}

// runStream keeps the DingTalk Stream running with backoff. It never falls
// back to polling unless AllowPollFallback is set: polling is slow.
func (c *Client) runStream(ctx context.Context, convID string, ch chan Message) {
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		cooldown, streamErr := c.startEventStream(ctx, convID, ch)
		if ctx.Err() != nil {
			return
		}
		failures++
		wait := c.cfg.StreamRestartMin
		if cooldown > wait {
			wait = cooldown // honour the server's full retry hint
		} else if backoff := time.Duration(failures) * 45 * time.Second; backoff > wait {
			wait = min(backoff, 5*time.Minute) // cap only the synthetic backoff
		}
		if c.cfg.AllowPollFallback && failures >= 5 {
			c.log.Warn("dws.stream.fallback_poll", "failures", failures, "err", streamErr)
			c.runPollLoop(ctx, convID, true)
			return
		}
		c.log.Warn("dws.stream.exit", "attempt", failures, "err", streamErr, "restart_in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Client) runPollLoop(ctx context.Context, convID string, reactions bool) {
	t := time.NewTicker(c.cfg.PollEvery)
	defer t.Stop()
	var react <-chan time.Time
	if reactions {
		rt := time.NewTicker(12 * time.Second)
		defer rt.Stop()
		react = rt.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := c.pollOnce(ctx, convID, false, 0)
			if err != nil {
				c.log.Warn("dws.poll.failed", "err", err)
			} else if n > 0 {
				c.log.Debug("dws.poll.new", "count", n)
			}
		case <-react:
			if n, err := c.pollNegativeReactions(ctx, convID); err != nil {
				c.log.Warn("dws.reaction_poll.failed", "err", err)
			} else if n > 0 {
				c.log.Debug("dws.reaction_poll.new", "count", n)
			}
		}
	}
}

// pollOnce fetches one page. seedOnly marks everything seen without
// delivering. With cutoff > 0 and !seedOnly, messages older than cutoff are
// only seeded and newer ones delivered (first-run catch-up).
func (c *Client) pollOnce(ctx context.Context, convID string, seedOnly bool, cutoff int64) (int, error) {
	args := c.target([]string{"chat", "+chat-messages", "--limit", fmt.Sprintf("%d", c.cfg.Limit), "-y", "-f", "json"}, convID)
	raw, err := c.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	var list msgList
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, fmt.Errorf("%w: chat-messages: %v", ErrParse, err)
	}
	// The API returns newest first; deliver new ones oldest first.
	msgs := list.Messages
	n := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.subs[convID]
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.MessageID == "" {
			continue
		}
		if _, ok := c.seen[m.MessageID]; ok {
			continue
		}
		c.seen[m.MessageID] = struct{}{}
		at := parseCreateTime(m.CreateTime)
		if seedOnly || (cutoff > 0 && at < cutoff) || c.isSelf(m.SenderID, m.Sender) {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		n++
		if ch == nil {
			continue
		}
		select {
		case ch <- c.fromListed(convID, m, text):
		case <-ctx.Done():
			c.saveSeen(convID)
			return n, ctx.Err()
		default:
			c.log.Warn("dws.inbound.dropped", "message", m.MessageID, "reason", "channel_full")
		}
	}
	c.saveSeen(convID)
	return n, nil
}

// Send posts Markdown as the logged-in profile (--as user, no AI badge) and
// returns the provider message id when dws reports one.
func (c *Client) Send(ctx context.Context, convID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if strings.TrimSpace(convID) == "" {
		return "", ErrEmptyConversation
	}
	args := c.target([]string{
		"chat", "+messages-send", "--as", "user", "--markdown", text, "--ai-tag=false",
		"--uuid", fmt.Sprintf("eel-%d", time.Now().UnixNano()), "-y", "-f", "json",
	}, convID)
	raw, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	id := sentMessageID(raw)
	c.rememberOutbound(id)
	return id, nil
}

// sentMessageID extracts the message id of a +messages-send answer.
func sentMessageID(raw []byte) string {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return ""
	}
	keys := []string{"openMsgId", "messageId", "msgId", "id"}
	for _, k := range keys {
		if s, _ := resp[k].(string); s != "" {
			return s
		}
	}
	if data, ok := resp["data"].(map[string]any); ok {
		for _, k := range keys {
			if s, _ := data[k].(string); s != "" {
				return s
			}
		}
	}
	return ""
}

// MarkRead marks a message read.
func (c *Client) MarkRead(ctx context.Context, convID, msgID string) error {
	if strings.TrimSpace(convID) == "" || strings.TrimSpace(msgID) == "" {
		return nil
	}
	_, err := c.run(ctx, "chat", "+conversation-mark-read", "--conversation-id", convID, "--message-id", msgID, "-y", "-f", "json")
	return err
}

// ClearRedPoint clears the unread badge of a conversation.
func (c *Client) ClearRedPoint(ctx context.Context, convID string) error {
	if strings.TrimSpace(convID) == "" {
		return nil
	}
	_, err := c.run(ctx, "chat", "+conversation-clear-red-point", "--conversation-id", convID, "-y", "-f", "json")
	return err
}

// FetchHistory returns recent non-empty messages, oldest first.
func (c *Client) FetchHistory(ctx context.Context, convID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 40
	}
	args := c.target([]string{"chat", "+chat-messages", "--limit", fmt.Sprintf("%d", limit), "-y", "-f", "json"}, convID)
	raw, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var list msgList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: chat-messages: %v", ErrParse, err)
	}
	out := make([]Message, 0, len(list.Messages))
	for i := len(list.Messages) - 1; i >= 0; i-- {
		m := list.Messages[i]
		if text := strings.TrimSpace(m.Text); text != "" {
			out = append(out, c.fromListed(convID, m, text))
		}
	}
	return out, nil
}

// Close stops every subscription and waits for its goroutines.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	return nil
}

func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
