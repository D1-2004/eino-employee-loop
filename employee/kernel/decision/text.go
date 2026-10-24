package decision

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

// The loop's own texts are the repair instructions it appends after a
// rejected response. Each locale is one JSON file with the same keys; a
// Host replaces them all with Config.RepairText.
//
//go:embed locales/*.json
var localeFS embed.FS

var (
	textsOnce sync.Once
	texts     map[string]map[string]string
)

func loadTexts() {
	texts = map[string]map[string]string{}
	entries, _ := localeFS.ReadDir("locales")
	for _, e := range entries {
		raw, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			panic(err)
		}
		pack := map[string]string{}
		if err := json.Unmarshal(raw, &pack); err != nil {
			panic(err)
		}
		texts[strings.TrimSuffix(e.Name(), ".json")] = pack
	}
}

// Locales lists the built-in locales.
func Locales() []string {
	textsOnce.Do(loadTexts)
	out := make([]string, 0, len(texts))
	for tag := range texts {
		out = append(out, tag)
	}
	return out
}

// text resolves a key in locale, then its base language, then English.
func text(locale, key string) string {
	textsOnce.Do(loadTexts)
	if s := texts[locale][key]; s != "" {
		return s
	}
	if base, _, _ := strings.Cut(locale, "-"); base != "" {
		for tag, pack := range texts {
			if strings.HasPrefix(tag, base) && pack[key] != "" {
				return pack[key]
			}
		}
	}
	return texts["en"][key]
}
