package dws

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// reactionsJSON is the gateway's default reaction policy: the provider
// identifiers of negative (correction / rejection) reactions. It is
// recognition data, not localized text, so it lives with the gateway and
// not in an i18n pack. Non-ASCII identifiers are JSON escapes.
//
//go:embed reactions.json
var reactionsJSON []byte

// LexiconConfig is the reaction policy of a gateway.
type LexiconConfig struct {
	// NegativeNames are reaction identifiers matched exactly (trimmed,
	// case-insensitive) against a reaction's name or its whole text.
	NegativeNames []string `json:"negative_names"`
	// NegativeSubstrings, when set, also flag a reaction whose name or text
	// contains one of them. Empty by default: substring matching is opt-in.
	NegativeSubstrings []string `json:"negative_substrings,omitempty"`
}

// DefaultLexiconConfig decodes the embedded reactions.json.
func DefaultLexiconConfig() (LexiconConfig, error) {
	var c LexiconConfig
	dec := json.NewDecoder(strings.NewReader(string(reactionsJSON)))
	dec.DisallowUnknownFields()
	err := dec.Decode(&c)
	return c, err
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
