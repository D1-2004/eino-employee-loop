package runner

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localeFS embed.FS

var (
	packsOnce sync.Once
	packs     map[string]map[string]string
)

func loadPacks() {
	packs = map[string]map[string]string{}
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
		packs[strings.TrimSuffix(e.Name(), ".json")] = pack
	}
}

// text resolves key in locale (then its base language, then English) and
// replaces {name}-style placeholders from pairs.
func text(locale, key string, pairs ...string) string {
	packsOnce.Do(loadPacks)
	s := packs[locale][key]
	if s == "" {
		if base, _, _ := strings.Cut(locale, "-"); base != "" {
			for tag, pack := range packs {
				if strings.HasPrefix(tag, base) && pack[key] != "" {
					s = pack[key]
					break
				}
			}
		}
	}
	if s == "" {
		s = packs["en"][key]
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, "{"+pairs[i]+"}", pairs[i+1])
	}
	return s
}
