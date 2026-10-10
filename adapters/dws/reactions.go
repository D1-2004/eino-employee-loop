package dws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Track recent outbound message IDs so poll mode can check ❌ reactions.
// Stream mode delivers reactions natively; this is the poll fallback.

func (p *DWSProvider) rememberOutbound(msgID string) {
	if msgID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outbound == nil {
		p.outbound = make([]string, 0, 32)
	}
	p.outbound = append(p.outbound, msgID)
	if len(p.outbound) > 40 {
		p.outbound = p.outbound[len(p.outbound)-40:]
	}
}

type emotionReplies struct {
	Items []emotionItem `json:"items"`
	// alternate shapes
	Results []emotionItem `json:"results"`
	Data    []emotionItem `json:"data"`
}
type emotionItem struct {
	MessageID string          `json:"messageId"`
	MsgID     string          `json:"msgId"`
	OpenMsgID string          `json:"openMsgId"`
	Emotions  []emotionDetail `json:"emotions"`
	Replies   []emotionDetail `json:"replies"`
	Reactions []emotionDetail `json:"reactions"`
}
type emotionDetail struct {
	Name       string `json:"name"`
	Emoji      string `json:"emoji"`
	Reaction   string `json:"reactionName"`
	Text       string `json:"text"`
	Operator   string `json:"operator"`
	OperatorID string `json:"operatorOpenDingTalkId"`
	UserName   string `json:"userName"`
	UserID     string `json:"userId"`
}

func (p *DWSProvider) pollNegativeReactions(ctx context.Context, convID string) (int, error) {
	p.mu.Lock()
	ids := append([]string{}, p.outbound...)
	p.mu.Unlock()
	if len(ids) == 0 {
		// Fall back: recent chat page message ids (any) — still useful for ❌ on our replies once Send records them.
		return 0, nil
	}
	// Batch up to 20
	if len(ids) > 20 {
		ids = ids[len(ids)-20:]
	}
	raw, err := p.run(ctx, "chat", "message", "list-emotion-replies",
		"--msg-ids", strings.Join(ids, ","),
		"-y", "-f", "json")
	if err != nil {
		// Older CLI path
		raw, err = p.run(ctx, "chat", "+messages-list-emotion-replies",
			"--msg-ids", strings.Join(ids, ","),
			"-y", "-f", "json")
		if err != nil {
			return 0, err
		}
	}
	var wrap emotionReplies
	if err := json.Unmarshal(raw, &wrap); err != nil {
		// try array root
		var arr []emotionItem
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			return 0, fmt.Errorf("parse emotion replies: %w head=%q", err, trunc(string(raw), 120))
		}
		wrap.Items = arr
	}
	items := wrap.Items
	if len(items) == 0 {
		items = wrap.Results
	}
	if len(items) == 0 {
		items = wrap.Data
	}
	n := 0
	p.mu.Lock()
	ch := p.subs[convID]
	defer p.mu.Unlock()
	for _, it := range items {
		msgID := firstNonEmpty(it.MessageID, firstNonEmpty(it.MsgID, it.OpenMsgID))
		details := it.Emotions
		if len(details) == 0 {
			details = it.Replies
		}
		if len(details) == 0 {
			details = it.Reactions
		}
		for _, d := range details {
			name := firstNonEmpty(d.Name, firstNonEmpty(d.Emoji, d.Reaction))
			text := d.Text
			if !NegativeReaction(name, text) {
				continue
			}
			rk := "react:" + msgID + ":" + name + ":" + firstNonEmpty(d.OperatorID, d.UserID)
			if _, ok := p.seen[rk]; ok {
				continue
			}
			p.seen[rk] = struct{}{}
			op := firstNonEmpty(d.Operator, d.UserName)
			opid := firstNonEmpty(d.OperatorID, d.UserID)
			if p.isSelf(dwsMsg{Sender: op, SenderID: opid}) {
				continue
			}
			in := Inbound{
				ConvID: convID, MsgID: rk, Speaker: op, OperatorID: opid,
				Text: text, AtMs: time.Now().UnixMilli(), Kind: "reaction",
				Reaction: name, TargetMsgID: msgID,
			}
			n++
			if ch != nil {
				select {
				case ch <- in:
				default:
					fmt.Fprintf(os.Stderr, "dws reaction-poll: channel full\n")
				}
			}
		}
	}
	if n > 0 {
		p.saveSeen(convID)
	}
	return n, nil
}
