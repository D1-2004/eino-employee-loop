package core

import (
	"embed"
	"sync"

	"github.com/D1-2004/eino-employee-loop/i18n"
)

//go:embed locales
var localesFS embed.FS

// LoadMessages merges the the prototype runtime's pack into b.
func LoadMessages(b *i18n.Bundle) error { return b.LoadFS(localesFS, "locales") }

var (
	messagesOnce sync.Once
	messages     *i18n.Bundle
)

// Messages returns the loop's default bundle plus the prototype runtime pack, built once. Treat it as read-only.
func Messages() *i18n.Bundle {
	messagesOnce.Do(func() {
		b, err := i18n.Extend(localesFS, "locales")
		if err != nil {
			panic(err)
		}
		messages = b
	})
	return messages
}
