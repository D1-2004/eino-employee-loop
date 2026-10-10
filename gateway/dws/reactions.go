package dws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The poll transport cannot receive reactions, so it checks recent sent
// messages for negative reactions. The stream transport delivers them.

func (c *Client) rememberOutbound(msgID string) {
	if msgID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outbound = append(c.outbound, msgID)
	if len(c.outbound) > 40 {
		c.outbound = c.outbound[len(c.outbound)-40:]
	}
}

type emotionReplies struct {
	Items   []emotionItem `json:"items"`
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

func (c *Client) pollNegativeReactions(ctx context.Context, convID string) (int, error) {
	c.mu.Lock()
	ids := append([]string{}, c.outbound...)
	c.mu.Unlock()
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > 20 {
		ids = ids[len(ids)-20:]
	}
	joined := strings.Join(ids, ",")
	raw, err := c.run(ctx, "chat", "message", "list-emotion-replies", "--msg-ids", joined, "-y", "-f", "json")
	if err != nil {
		// Older CLI path.
		if raw, err = c.run(ctx, "chat", "+messages-list-emotion-replies", "--msg-ids", joined, "-y", "-f", "json"); err != nil {
			return 0, err
		}
	}
	var wrap emotionReplies
	if err := json.Unmarshal(raw, &wrap); err != nil {
		var arr []emotionItem
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			return 0, fmt.Errorf("%w: emotion replies: %v", ErrParse, err)
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
	negative := c.cfg.Negative
	if negative == nil {
		negative = DefaultLexicon().Negative
	}
	n := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.subs[convID]
	for _, it := range items {
		msgID := firstNonEmpty(it.MessageID, it.MsgID, it.OpenMsgID)
		details := it.Emotions
		if len(details) == 0 {
			details = it.Replies
		}
		if len(details) == 0 {
			details = it.Reactions
		}
		for _, d := range details {
			name := firstNonEmpty(d.Name, d.Emoji, d.Reaction)
			if !negative(name, d.Text) {
				continue
			}
			opID := firstNonEmpty(d.OperatorID, d.UserID)
			key := "react:" + msgID + ":" + name + ":" + opID
			if _, ok := c.seen[key]; ok {
				continue
			}
			c.seen[key] = struct{}{}
			op := firstNonEmpty(d.Operator, d.UserName)
			if c.isSelf(opID, op) {
				continue
			}
			m := Message{
				ConversationID: convID, MessageID: key, SenderName: op, SenderID: opID,
				Text: d.Text, AtMs: time.Now().UnixMilli(), Kind: MessageReaction, Single: c.cfg.Kind == KindDM,
				Reaction: name, TargetMessageID: msgID,
			}
			n++
			if ch != nil {
				select {
				case ch <- m:
				default:
					c.log.Warn("dws.inbound.dropped", "kind", m.Kind, "message", key, "reason", "channel_full")
				}
			}
		}
	}
	if n > 0 {
		c.saveSeen(convID)
	}
	return n, nil
}
