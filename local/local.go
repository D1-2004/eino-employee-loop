// Package local runs one employee on a laptop for verification: a chat
// channel delivers messages, the event catalog routes them, and the loop
// answers through a small Host with two tools (reply, stay_quiet).
//
// Two channels ship with it:
//   - DWS: a DingTalk account (for example a test account) through the dws
//     CLI: personal IM events in, messages sent as that account out;
//   - Robot: a DingTalk app robot over Stream Mode, replies through the
//     message's session webhook.
//
// It is a verification harness, not a production Host: history lives in
// memory, background sessions are not run, and there are no task tools.
package local

import (
	"context"
	"time"
)

// Message is one inbound chat message, as authenticated by its channel.
type Message struct {
	ConversationID string
	MessageID      string
	SenderID       string
	SenderName     string
	Text           string
	// Direct is a one-to-one conversation.
	Direct bool
	// Mentioned is set when the message mentions the employee.
	Mentioned bool
	At        time.Time
	// reply is set by channels that answer through the message itself.
	reply func(ctx context.Context, text string) (string, error)
}

// Addressed reports whether the message is addressed to the employee.
func (m Message) Addressed() bool { return m.Direct || m.Mentioned }

// Channel connects the employee to a chat platform.
type Channel interface {
	// Listen delivers messages until ctx ends or the connection fails.
	Listen(ctx context.Context, deliver func(Message)) error
	// Send posts text into the conversation of m and returns the sent
	// message id when the platform reports one.
	Send(ctx context.Context, m Message, text string) (string, error)
}
