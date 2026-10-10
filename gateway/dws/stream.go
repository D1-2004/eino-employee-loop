package dws

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// startEventStream runs `dws event +listen-im` (DingTalk Stream) and pumps
// NDJSON into ch until ctx ends or the process exits. cooldown is the
// largest retry hint dws printed on stderr.
func (c *Client) startEventStream(ctx context.Context, convID string, ch chan Message) (cooldown time.Duration, err error) {
	args := []string{"event", "+listen-im", "--events", "message,reaction", "-f", "ndjson", "-y"}
	if c.cfg.Kind == KindDM {
		args = append(args, "--kind", "sender", "--open-dingtalk-id", convID)
	} else {
		args = append(args, "--kind", "group", "--chat-id", convID)
	}
	cmd := exec.CommandContext(ctx, c.cfg.Bin, c.args(args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("%w: listen-im start: %v", ErrCommand, err)
	}
	c.log.Info("dws.stream.started", "pid", cmd.Process.Pid, "conversation", trunc(convID, 40))

	ready := make(chan struct{}, 1)
	var coolMu sync.Mutex
	var coolHint time.Duration
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			c.log.Debug("dws.stream.stderr", "line", line)
			if strings.Contains(line, "[event] ready") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if d := parseRetryAfter(line, time.Now()); d > 0 {
				coolMu.Lock()
				coolHint = max(coolHint, d)
				coolMu.Unlock()
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(45 * time.Second):
		c.log.Warn("dws.stream.ready_timeout")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return 0, ctx.Err()
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m, ok, perr := parseStreamEvent(line, convID, c.cfg.Kind == KindDM)
		if perr != nil {
			c.log.Warn("dws.stream.bad_line", "err", perr, "head", trunc(line, 80))
		}
		if !ok || !c.admitStreamed(convID, m) {
			continue
		}
		select {
		case ch <- m:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return 0, ctx.Err()
		default:
			c.log.Warn("dws.inbound.dropped", "kind", m.Kind, "message", m.MessageID, "reason", "channel_full")
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		c.log.Warn("dws.stream.stdout", "err", err)
	}
	waitErr := cmd.Wait()
	coolMu.Lock()
	cd := coolHint
	coolMu.Unlock()
	if ctx.Err() != nil {
		return cd, ctx.Err()
	}
	return cd, waitErr
}

// admitStreamed deduplicates a streamed record and drops the employee's own.
func (c *Client) admitStreamed(convID string, m Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := m.MessageID
	if m.Kind == MessageReaction {
		key = "react:" + m.MessageID
		if m.MessageID == "" {
			key = "react:" + m.TargetMessageID + ":" + m.Reaction + ":" + m.SenderID
		}
	}
	if key != "" {
		if _, seen := c.seen[key]; seen {
			return false
		}
		c.seen[key] = struct{}{}
	}
	if c.isSelf(m.SenderID, m.SenderName) {
		return false
	}
	c.saveSeen(convID)
	return true
}

// parseRetryAfter extracts "Retry After: 518s" or "cooldown until
// <RFC3339>" hints from a dws stderr line, plus a small cushion.
func parseRetryAfter(line string, now time.Time) time.Duration {
	const cushion = 5 * time.Second
	if i := strings.Index(line, "Retry After:"); i >= 0 {
		if fs := strings.Fields(line[i+len("Retry After:"):]); len(fs) > 0 {
			if d, err := time.ParseDuration(fs[0]); err == nil && d > 0 {
				return d + cushion
			}
		}
	}
	if i := strings.Index(line, "cooldown until "); i >= 0 {
		if fs := strings.Fields(line[i+len("cooldown until "):]); len(fs) > 0 {
			if t, err := time.Parse(time.RFC3339, strings.TrimRight(fs[0], ".,;")); err == nil {
				if d := t.Sub(now); d > 0 {
					return d + cushion
				}
			}
		}
	}
	return 0
}

// Stream field names recognised beyond the basic message and reaction
// fields. dws versions differ; every name is optional.
var (
	mentionListFields = []string{"at_open_dingtalk_ids", "at_users", "at_user_ids"}
	mentionSelfFields = []string{"is_in_at_list", "at_me", "is_at_me"}
	replyIDFields     = []string{"reply_to_message_id", "quote_message_id", "replied_message_id"}
	replySenderFields = []string{"reply_to_sender_open_dingtalk_id", "quote_sender_open_dingtalk_id", "replied_sender_open_dingtalk_id"}
	idObjectFields    = []string{"open_dingtalk_id", "openDingTalkId", "dingtalk_id", "id"}
)

// DingTalk conversation_type value of a one-to-one chat.
const singleConversationType = "1"

// parseStreamEvent decodes one NDJSON record. ok is false for records that
// carry nothing to deliver (unknown types, reaction removals, empty text).
func parseStreamEvent(line, convID string, single bool) (Message, bool, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return Message{}, false, fmt.Errorf("%w: %v", ErrParse, err)
	}
	// Unwrap the transport envelope {type, data:{...}} when not flattened.
	if data, ok := m["data"].(map[string]any); ok && len(data) > 0 {
		typ0 := firstNonEmpty(strField(m, "type"), strField(m, "event_type"))
		merged := map[string]any{}
		for k, v := range data {
			merged[k] = v
		}
		for k, v := range m {
			if _, exists := merged[k]; !exists && k != "data" {
				merged[k] = v
			}
		}
		if typ0 != "" {
			merged["type"] = typ0
		}
		m = merged
	}
	typ := firstNonEmpty(strField(m, "type"), strField(m, "event_type"))
	at := int64Field(m, "event_time")
	if at == 0 {
		at = int64Field(m, "timestamp")
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	cid := firstNonEmpty(strField(m, "conversation_id"), convID)
	if strField(m, "conversation_type") == singleConversationType {
		single = true
	}

	switch {
	case strings.Contains(typ, "reaction"):
		op := strings.ToLower(strField(m, "operation_type"))
		// Only additions matter; removals are ignored.
		if op != "" && op != "create" && !strings.Contains(op, "add") {
			return Message{}, false, nil
		}
		text := strField(m, "reaction_text")
		return Message{
			ConversationID: cid, MessageID: strField(m, "event_id"),
			SenderName: strField(m, "operator"), SenderID: strField(m, "operator_open_dingtalk_id"),
			Text: text, AtMs: at, Kind: MessageReaction, Single: single,
			Reaction: firstNonEmpty(strField(m, "reaction_name"), text), TargetMessageID: strField(m, "message_id"),
		}, true, nil

	case strings.Contains(typ, "message"):
		content := strings.TrimSpace(firstNonEmpty(strField(m, "content"), strField(m, "text")))
		if content == "" {
			return Message{}, false, nil
		}
		msg := Message{
			ConversationID: cid, MessageID: strField(m, "message_id"),
			SenderName: strField(m, "sender"), SenderID: strField(m, "sender_open_dingtalk_id"),
			Text: content, AtMs: at, Kind: MessageText, Single: single,
		}
		for _, f := range mentionListFields {
			msg.MentionIDs = append(msg.MentionIDs, idList(m[f])...)
		}
		for _, f := range mentionSelfFields {
			if b, ok := m[f].(bool); ok && b {
				msg.MentionsSelf = true
			}
		}
		for _, f := range replyIDFields {
			msg.ReplyToMessageID = firstNonEmpty(msg.ReplyToMessageID, strField(m, f))
		}
		for _, f := range replySenderFields {
			msg.ReplyToSenderID = firstNonEmpty(msg.ReplyToSenderID, strField(m, f))
		}
		return msg, true, nil
	}
	return Message{}, false, nil
}

// idList reads a list of ids given as strings or as objects with an id field.
func idList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, it := range items {
		switch x := it.(type) {
		case string:
			if x != "" {
				out = append(out, x)
			}
		case map[string]any:
			for _, f := range idObjectFields {
				if s := strField(x, f); s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}

func strField(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func int64Field(m map[string]any, k string) int64 {
	switch t := m[k].(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
