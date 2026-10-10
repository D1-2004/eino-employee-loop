package dws

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// startEventStream runs `dws event +listen-im` (DingTalk Stream) and pumps
// flattened NDJSON into ch. Blocks until ctx cancelled or process exits.
func (p *DWSProvider) startEventStream(ctx context.Context, convID string, ch chan Inbound) error {
	args := []string{
		"event", "+listen-im",
		"--events", "message,reaction",
		"-f", "ndjson",
		"-y",
	}
	switch p.cfg.Kind {
	case "dm":
		args = append(args, "--kind", "sender", "--open-dingtalk-id", convID)
	default:
		args = append(args, "--kind", "group", "--chat-id", convID)
	}

	cmd := exec.CommandContext(ctx, p.cfg.DWSBin, p.dwsArgs(args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("dws event listen-im start: %w", err)
	}
	fmt.Fprintf(os.Stderr, "dws: stream started pid=%d cid=%s events=message,reaction\n",
		cmd.Process.Pid, trunc(convID, 40))

	ready := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintf(os.Stderr, "dws event: %s\n", line)
			if strings.Contains(line, "[event] ready") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(45 * time.Second):
		fmt.Fprintf(os.Stderr, "dws: stream ready timeout — still reading stdout\n")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		in, ok := parseStreamEvent(line, convID)
		if !ok {
			continue
		}
		p.mu.Lock()
		if in.Kind != "reaction" {
			if _, seen := p.seen[in.MsgID]; seen && in.MsgID != "" {
				p.mu.Unlock()
				continue
			}
			if in.MsgID != "" {
				p.seen[in.MsgID] = struct{}{}
			}
		} else {
			rk := "react:" + in.MsgID
			if in.MsgID == "" {
				rk = "react:" + in.TargetMsgID + ":" + in.Reaction + ":" + in.OperatorID
			}
			if _, seen := p.seen[rk]; seen {
				p.mu.Unlock()
				continue
			}
			p.seen[rk] = struct{}{}
		}
		self := false
		if in.Kind != "reaction" {
			self = p.isSelf(dwsMsg{Sender: in.Speaker, SenderID: in.OperatorID})
		} else {
			// Skip our own reactions; keep human ❌ on our/others' messages.
			self = p.isSelf(dwsMsg{Sender: in.Speaker, SenderID: in.OperatorID})
		}
		if self {
			p.mu.Unlock()
			continue
		}
		p.saveSeen(convID)
		p.mu.Unlock()

		select {
		case ch <- in:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return ctx.Err()
		default:
			fmt.Fprintf(os.Stderr, "dws stream: channel full, drop %s %s\n", in.Kind, in.MsgID)
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "dws stream stdout: %v\n", err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return waitErr
}

type streamEvt map[string]any

func parseStreamEvent(line, convID string) (Inbound, bool) {
	var m streamEvt
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		fmt.Fprintf(os.Stderr, "dws stream: bad json: %v head=%q\n", err, trunc(line, 80))
		return Inbound{}, false
	}
	// Unwrap transport envelope when --flatten is off: {type, data:{...}}.
	if data, ok := m["data"].(map[string]any); ok && len(data) > 0 {
		typ0 := strField(m, "type")
		if typ0 == "" {
			typ0 = strField(m, "event_type")
		}
		merged := streamEvt{}
		for k, v := range data {
			merged[k] = v
		}
		for k, v := range m {
			if k == "data" {
				continue
			}
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
		if typ0 != "" {
			merged["type"] = typ0
		}
		m = merged
	}
	typ := strField(m, "type")
	if typ == "" {
		typ = strField(m, "event_type")
	}
	at := int64Field(m, "event_time")
	if at == 0 {
		at = int64Field(m, "timestamp")
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	cid := strField(m, "conversation_id")
	if cid == "" {
		cid = convID
	}

	switch {
	case strings.Contains(typ, "reaction"):
		name := strField(m, "reaction_name")
		text := strField(m, "reaction_text")
		op := strField(m, "operation_type")
		// Only care about add; ignore remove.
		if op != "" && !strings.EqualFold(op, "add") && !strings.EqualFold(op, "create") &&
			!strings.Contains(strings.ToLower(op), "add") {
			return Inbound{}, false
		}
		return Inbound{
			ConvID:      cid,
			MsgID:       strField(m, "event_id"),
			Speaker:     strField(m, "operator"),
			OperatorID:  strField(m, "operator_open_dingtalk_id"),
			Text:        text,
			AtMs:        at,
			Kind:        "reaction",
			Reaction:    firstNonEmpty(name, text),
			TargetMsgID: strField(m, "message_id"),
		}, true

	case strings.Contains(typ, "message_receive") || strings.Contains(typ, "message"):
		content := strField(m, "content")
		if content == "" {
			content = strField(m, "text")
		}
		content = strings.TrimSpace(content)
		if content == "" {
			return Inbound{}, false
		}
		return Inbound{
			ConvID:     cid,
			MsgID:      strField(m, "message_id"),
			Speaker:    strField(m, "sender"),
			OperatorID: strField(m, "sender_open_dingtalk_id"),
			Text:       content,
			AtMs:       at,
			Kind:       "message",
		}, true
	default:
		return Inbound{}, false
	}
}

func strField(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func int64Field(m map[string]any, k string) int64 {
	v, ok := m[k]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
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
	default:
		return 0
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
