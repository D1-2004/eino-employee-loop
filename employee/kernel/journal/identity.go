package journal

import (
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type identifiedModel struct {
	model.ToolCallingChatModel
	identity string
}

func (m *identifiedModel) ModelIdentity() string { return m.identity }
func (m *identifiedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.ToolCallingChatModel.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return WithIdentity(bound, m.identity), nil
}

// WithIdentity records the host-resolved frozen selection without changing the
// injected model's generation or tool-binding behavior.
func WithIdentity(m model.ToolCallingChatModel, identity string) model.ToolCallingChatModel {
	if m == nil {
		return nil
	}
	return &identifiedModel{ToolCallingChatModel: m, identity: identity}
}
