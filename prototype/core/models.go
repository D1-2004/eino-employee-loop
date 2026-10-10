package core

import (
	"context"

	"github.com/cloudwego/eino/components/model"
)

// ModelProvider supplies the chat models Core uses. Core never imports a vendor SDK.
type ModelProvider interface {
	// Decision returns the tool-calling model for the main decision loop.
	Decision(ctx context.Context) (model.ToolCallingChatModel, error)
	// Ack returns a fast, no-thinking, no-tools model for the instant acknowledgement.
	Ack(ctx context.Context) (model.BaseChatModel, error)
}
