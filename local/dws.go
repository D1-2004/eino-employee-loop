package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DWS is the account channel: the employee is a DingTalk account the dws
// CLI is logged in to. It listens to the account's direct messages and the
// group messages that mention it, and answers as that account.
type DWS struct {
	// Bin is the dws executable (default "dws").
	Bin string
	// Profile selects the account ("corpId:userId"); empty uses the current one.
	Profile string
	Log     *slog.Logger

	mu   sync.Mutex
	seen map[string]bool
	// sent remembers "conversation|text" of our own messages until their echo
	// arrives; the echo reveals self, the account's id as seen by itself.
	sent map[string]bool
	self string
}

func (d *DWS) bin() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "dws"
}

func (d *DWS) args(args ...string) []string {
	if d.Profile != "" {
		args = append(args, "--profile", d.Profile)
	}
	return args
}

func (d *DWS) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// Listen runs one personal event stream for direct messages and one for
// mentions, and restarts a stream that ends before ctx does.
func (d *DWS) Listen(ctx context.Context, deliver func(Message)) error {
	var wg sync.WaitGroup
	for _, kind := range []string{"all-direct", "at-me"} {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			for ctx.Err() == nil {
				err := d.stream(ctx, kind, deliver)
				if ctx.Err() != nil {
					return
				}
				d.log().Warn("dws.stream_ended", "kind", kind, "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
		}(kind)
	}
	wg.Wait()
	return ctx.Err()
}

func (d *DWS) stream(ctx context.Context, kind string, deliver func(Message)) error {
	cmd := exec.CommandContext(ctx, d.bin(), d.args("event", "+listen-im", "--kind", kind, "--events", "message", "-f", "ndjson", "-y")...)
	// Stop with SIGTERM so dws cancels the personal subscription it created.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			d.log().Debug("dws.stderr", "kind", kind, "line", sc.Text())
		}
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		d.log().Debug("dws.event", "kind", kind, "line", sc.Text())
		m, ok, err := parseDWSEvent(sc.Bytes())
		if err != nil {
			d.log().Warn("dws.bad_event", "err", err)
			continue
		}
		if ok && !d.own(m) && d.first(m.MessageID) {
			// The subscription says what the flat record may omit.
			m.Direct = m.Direct || kind == "all-direct"
			m.Mentioned = m.Mentioned || kind == "at-me"
			deliver(m)
		}
	}
	return errors.Join(sc.Err(), cmd.Wait())
}

// first reports whether a message id is new: both streams may carry the
// same message (a mention in a direct conversation).
func (d *DWS) first(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if id == "" || d.seen[id] {
		return id == ""
	}
	d.seen[id] = true
	return true
}

// own reports whether m is the echo of a message the account sent.
func (d *DWS) own(m Message) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.self != "" && m.SenderID == d.self {
		return true
	}
	key := m.ConversationID + "|" + strings.TrimSpace(m.Text)
	if d.sent[key] {
		delete(d.sent, key)
		d.self = m.SenderID
		return true
	}
	return false
}

// Send answers as the account with a quote reply to m (in the direct
// conversation with its sender, or in its group), like a colleague replying
// to a specific message. The uuid makes a retried send idempotent.
func (d *DWS) Send(ctx context.Context, m Message, text string) (string, error) {
	args := []string{"chat", "+messages-reply", "--as", "user", "--message-id", m.MessageID, "--content", text, "--ai-tag=false", "--uuid", "eel-" + m.MessageID, "-y", "-f", "json"}
	if m.Direct {
		args = append(args, "--open-dingtalk-id", m.SenderID)
	} else {
		args = append(args, "--group", m.ConversationID)
	}
	out, err := exec.CommandContext(ctx, d.bin(), d.args(args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("dws send: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("dws send: %w", err)
	}
	d.mu.Lock()
	if d.sent == nil {
		d.sent = map[string]bool{}
	}
	d.sent[m.ConversationID+"|"+strings.TrimSpace(text)] = true
	d.mu.Unlock()
	return sentID(out), nil
}

// parseDWSEvent decodes one NDJSON record of `dws event +listen-im`. ok is
// false for records without text.
func parseDWSEvent(line []byte) (Message, bool, error) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return Message{}, false, err
	}
	// Unwrap the transport envelope {type, data:{...}}.
	if data, ok := raw["data"].(map[string]any); ok {
		for k, v := range raw {
			if _, exists := data[k]; !exists && k != "data" {
				data[k] = v
			}
		}
		raw = data
	}
	typ := str(raw, "type", "event_type")
	if typ != "" && !strings.Contains(typ, "message") {
		return Message{}, false, nil
	}
	content := strings.TrimSpace(str(raw, "content", "text"))
	if content == "" {
		return Message{}, false, nil
	}
	m := Message{
		ConversationID: str(raw, "conversation_id", "open_conversation_id"),
		MessageID:      str(raw, "message_id", "open_msg_id"),
		SenderID:       str(raw, "sender_open_dingtalk_id", "sender_id"),
		SenderName:     str(raw, "sender", "sender_nick"),
		Text:           content,
		Direct:         str(raw, "conversation_type") == "1",
		At:             time.Now(),
	}
	if ms, err := strconv.ParseInt(str(raw, "event_time", "timestamp"), 10, 64); err == nil && ms > 0 {
		m.At = time.UnixMilli(ms)
	}
	for _, k := range []string{"is_in_at_list", "at_me", "is_at_me"} {
		if b, _ := raw[k].(bool); b {
			m.Mentioned = true
		}
	}
	return m, true, nil
}

// str returns the first non-empty field among keys, rendering numbers.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		case json.Number:
			return v.String()
		}
	}
	return ""
}

// sentID extracts the message id of a +messages-send answer.
func sentID(raw []byte) string {
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil {
		return ""
	}
	if data, ok := resp["data"].(map[string]any); ok {
		if id := str(data, "openMsgId", "messageId", "msgId", "id"); id != "" {
			return id
		}
	}
	return str(resp, "openMsgId", "messageId", "msgId", "id")
}
