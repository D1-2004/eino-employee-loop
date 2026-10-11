package local

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

// Robot is the app-robot channel: a DingTalk robot connected over Stream
// Mode. People talk to it directly or mention it in a group; it answers
// through the session webhook of each message.
type Robot struct {
	ClientID, ClientSecret string
}

// Listen connects the robot and delivers its messages until ctx ends.
func (r *Robot) Listen(ctx context.Context, deliver func(Message)) error {
	if r.ClientID == "" || r.ClientSecret == "" {
		return errors.New("robot: client id and secret are required")
	}
	cli := client.NewStreamClient(client.WithAppCredential(client.NewAppCredentialConfig(r.ClientID, r.ClientSecret)))
	cli.RegisterChatBotCallbackRouter(func(_ context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		if m, ok := robotMessage(data); ok {
			deliver(m)
		}
		return nil, nil
	})
	if err := cli.Start(ctx); err != nil {
		return err
	}
	defer cli.Close()
	<-ctx.Done()
	return ctx.Err()
}

func robotMessage(data *chatbot.BotCallbackDataModel) (Message, bool) {
	if data == nil || data.SessionWebhook == "" {
		return Message{}, false
	}
	text := strings.TrimSpace(data.Text.Content)
	if text == "" {
		return Message{}, false
	}
	webhook := data.SessionWebhook
	sender := data.SenderStaffId
	if sender == "" {
		sender = data.SenderId
	}
	return Message{
		ConversationID: data.ConversationId,
		MessageID:      data.MsgId,
		SenderID:       sender,
		SenderName:     data.SenderNick,
		Text:           text,
		Direct:         data.ConversationType == "1",
		// A group robot only receives messages that mention it.
		Mentioned: data.IsInAtList || data.ConversationType != "1",
		At:        time.UnixMilli(data.CreateAt),
		reply: func(ctx context.Context, text string) (string, error) {
			return "", chatbot.NewChatbotReplier().SimpleReplyMarkdown(ctx, webhook, []byte("Reply"), []byte(text))
		},
	}, true
}

// Send answers through the message's session webhook.
func (r *Robot) Send(ctx context.Context, m Message, text string) (string, error) {
	if m.reply == nil {
		return "", errors.New("robot: the message has no session webhook")
	}
	return m.reply(ctx, text)
}
