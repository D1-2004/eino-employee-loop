package runner

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

	mu        sync.Mutex
	seen      map[string]bool
	seenOrder []string
	// sent remembers confirmed outbound message IDs until their echo arrives;
	// text alone cannot identify self because a peer may repeat the same text.
	sent      map[string]bool
	sentOrder []string
	self      string
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
	d.seenOrder = append(d.seenOrder, id)
	if len(d.seenOrder) > 4096 {
		delete(d.seen, d.seenOrder[0])
		d.seenOrder = d.seenOrder[1:]
	}
	return true
}

// own reports whether m is the echo of a message the account sent.
func (d *DWS) own(m Message) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.self != "" && m.SenderID == d.self {
		return true
	}
	key := m.MessageID
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
	if strings.TrimSpace(m.MessageID) == "" || strings.TrimSpace(text) == "" {
		return "", errors.New("dws send: a source message and reply text are required")
	}
	conversation := m.ConversationID
	if conversation == "" && m.Direct && m.SenderID != "" {
		var err error
		conversation, err = d.directConversation(ctx, m.SenderID)
		if err != nil {
			return "", err
		}
	}
	if conversation == "" {
		return "", errors.New("dws send: no conversation to reply in")
	}
	out, err := d.run(ctx, "dws send", replyArgs(conversation, m, text)...)
	if err != nil {
		return "", err
	}
	receipt, err := parseDWSReceipt(out)
	if err != nil {
		return "", err
	}
	if receipt.source != "" && receipt.source != m.MessageID || receipt.identity != "" && receipt.identity != "user" {
		return "", errors.New("dws send: reply source or identity mismatch")
	}
	if receipt.task != "" && receipt.status != "SUCCESS" {
		out, err = d.run(ctx, "dws status", "chat", "+messages-query-send-status", "--open-task-id", receipt.task, "-f", "json")
		if err != nil {
			return "", err
		}
		status, err := parseDWSReceipt(out)
		if err != nil {
			return "", err
		}
		if status.task != "" && status.task != receipt.task {
			return "", errors.New("dws send: status task mismatch")
		}
		receipt = status
	}
	if receipt.status != "SUCCESS" || receipt.id == "" || receipt.conversation != conversation || receipt.id == m.MessageID ||
		receipt.source != "" && receipt.source != m.MessageID || receipt.identity != "" && receipt.identity != "user" {
		return "", errors.New("dws send: delivery is unconfirmed or message reference does not match")
	}
	d.mu.Lock()
	if d.sent == nil {
		d.sent = map[string]bool{}
	}
	key := receipt.id
	if !d.sent[key] {
		d.sent[key] = true
		d.sentOrder = append(d.sentOrder, key)
		if len(d.sentOrder) > 512 {
			delete(d.sent, d.sentOrder[0])
			d.sentOrder = d.sentOrder[1:]
		}
	}
	d.mu.Unlock()
	return receipt.id, nil
}

// replyArgs uses the common personal-reply flags of dws v1.0.59 and v1.0.63.
// Profile supplies the identity; both direct and group replies use their cid.
func replyArgs(conversation string, m Message, text string) []string {
	args := []string{"chat", "+messages-reply", "--group", conversation, "--message-id", m.MessageID, "--content", text, "--ai-tag=false", "--uuid", "eel-" + m.MessageID, "-y", "-f", "json"}
	if m.SenderID != "" {
		args = append(args, "--ref-sender", m.SenderID)
	}
	return args
}

func (d *DWS) directConversation(ctx context.Context, peer string) (string, error) {
	out, err := d.run(ctx, "dws conversation", "chat", "+conversation-info", "--open-dingtalk-id", peer, "-f", "json")
	if err != nil {
		return "", err
	}
	var resp struct {
		Success *bool `json:"success"`
		Result  struct {
			ConversationInfo struct {
				ID string `json:"openConversationId"`
			} `json:"conversationInfo"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Success != nil && !*resp.Success || resp.Result.ConversationInfo.ID == "" {
		return "", errors.New("dws conversation: direct conversation was not resolved")
	}
	return resp.Result.ConversationInfo.ID, nil
}

func (d *DWS) run(ctx context.Context, what string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, d.bin(), d.args(args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%s: %w: %s", what, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

type dwsReceipt struct{ id, conversation, status, task, source, identity string }

// Only outbound envelope fields are traversed: referencedMessage is the source,
// never evidence that the reply was sent. openTaskId is never a message ID.
func parseDWSReceipt(raw []byte) (dwsReceipt, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return dwsReceipt{}, fmt.Errorf("dws receipt: %w", err)
	}
	var r dwsReceipt
	r.source, r.identity = str(root, "sourceMessageId"), str(root, "identity")
	var visit func(map[string]any) error
	merge := func(dst *string, value string) error {
		if value == "" {
			return nil
		}
		if *dst != "" && *dst != value {
			return errors.New("dws receipt: conflicting outbound fields")
		}
		*dst = value
		return nil
	}
	visit = func(m map[string]any) error {
		if failed, ok := m["success"].(bool); ok && !failed {
			return errors.New("dws receipt: command reported failure")
		}
		if m["error"] != nil || m["dryRun"] == true || m["_mock"] == true {
			return errors.New("dws receipt: error or dry-run is not delivery")
		}
		for _, field := range []struct {
			dst   *string
			value string
		}{
			{&r.id, str(m, "openMessageId", "openMsgId", "messageId", "msgId")},
			{&r.conversation, str(m, "openConversationId", "conversationId", "openCid")},
			{&r.task, str(m, "openTaskId", "taskId")},
			{&r.status, strings.ToUpper(str(m, "deliveryStatus", "status", "sendStatus"))},
		} {
			if err := merge(field.dst, field.value); err != nil {
				return err
			}
		}
		for _, key := range []string{"result", "data", "sendReceipt", "messageRef"} {
			switch child := m[key].(type) {
			case map[string]any:
				if err := visit(child); err != nil {
					return err
				}
			case []any:
				if len(child) != 1 {
					return errors.New("dws receipt: expected one outbound result")
				}
				item, ok := child[0].(map[string]any)
				if !ok {
					return errors.New("dws receipt: invalid outbound result")
				}
				if err := visit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := visit(root)
	return r, err
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
