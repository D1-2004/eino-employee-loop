package dws

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// reactionFiles hold the gateway's default reaction policy: the provider
// identifiers of negative (correction / rejection) reactions. They are
// recognition data owned by the gateway, not localized output, so they live
// here and not in an i18n pack. default.json holds locale-neutral
// identifiers (ASCII names, emoji); <locale>.json holds the display names
// the provider uses in that locale. The provider reports reactions by the
// name the reacting user saw, whose locale the gateway cannot know, so the
// default policy is the union of every file.
//
//go:embed reactions/*.json
var reactionFiles embed.FS

// LexiconConfig is the reaction policy of a gateway.
type LexiconConfig struct {
	// NegativeNames are reaction identifiers matched exactly (trimmed,
	// case-insensitive) against a reaction's name or its whole text.
	NegativeNames []string `json:"negative_names"`
	// NegativeSubstrings, when set, also flag a reaction whose name or text
	// contains one of them. Empty by default: substring matching is opt-in.
	NegativeSubstrings []string `json:"negative_substrings,omitempty"`
}

// ReactionLocales lists the embedded reaction files by name without the
// extension ("default" plus one entry per locale), sorted.
func ReactionLocales() []string {
	entries, _ := fs.ReadDir(reactionFiles, "reactions")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// LexiconConfigFor decodes the named embedded reaction files (see
// ReactionLocales) and merges them.
func LexiconConfigFor(names ...string) (LexiconConfig, error) {
	var merged LexiconConfig
	for _, name := range names {
		raw, err := reactionFiles.ReadFile("reactions/" + name + ".json")
		if err != nil {
			return LexiconConfig{}, fmt.Errorf("dws: reaction file %q: %w", name, err)
		}
		var c LexiconConfig
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return LexiconConfig{}, fmt.Errorf("dws: reaction file %q: %w", name, err)
		}
		merged.NegativeNames = append(merged.NegativeNames, c.NegativeNames...)
		merged.NegativeSubstrings = append(merged.NegativeSubstrings, c.NegativeSubstrings...)
	}
	return merged, nil
}

// DefaultLexiconConfig merges every embedded reaction file.
func DefaultLexiconConfig() (LexiconConfig, error) {
	return LexiconConfigFor(ReactionLocales()...)
}

// Lexicon recognises negative reactions.
type Lexicon struct {
	names      map[string]bool
	substrings []string
}

// NewLexicon builds a lexicon from a configuration.
func NewLexicon(c LexiconConfig) Lexicon {
	l := Lexicon{names: map[string]bool{}}
	for _, n := range c.NegativeNames {
		if n = norm(n); n != "" {
			l.names[n] = true
		}
	}
	for _, s := range c.NegativeSubstrings {
		if s = strings.TrimSpace(s); s != "" {
			l.substrings = append(l.substrings, s)
		}
	}
	return l
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

var (
	defaultLexOnce sync.Once
	defaultLex     Lexicon
)

// DefaultLexicon is NewLexicon(DefaultLexiconConfig()), built once. It
// panics if the embedded configuration is invalid (a build-time invariant).
func DefaultLexicon() Lexicon {
	defaultLexOnce.Do(func() {
		c, err := DefaultLexiconConfig()
		if err != nil {
			panic(err)
		}
		defaultLex = NewLexicon(c)
	})
	return defaultLex
}

// Negative reports whether a reaction is a negative signal: its name or its
// whole text is a configured identifier, or (when configured) contains a
// configured substring.
func (l Lexicon) Negative(name, text string) bool {
	if l.names[norm(name)] || l.names[norm(text)] {
		return true
	}
	for _, sub := range l.substrings {
		if strings.Contains(name, sub) || strings.Contains(text, sub) {
			return true
		}
	}
	return false
}
