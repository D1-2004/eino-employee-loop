package core

import (
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/i18n"
)

// texts resolves every natural-language string Core produces. Replies and
// prompts use the configured locale; detection lexicons match the union of
// that lexicon across all loaded locales, because people may write in a
// language other than the one the employee replies in.
type texts struct {
	b      *i18n.Bundle
	locale string

	mu  sync.Mutex
	lex map[string][]string
}

func newTexts(b *i18n.Bundle, locale string) *texts {
	if b == nil {
		b = Messages()
	}
	if strings.TrimSpace(locale) == "" {
		locale = i18n.Fallback
	}
	return &texts{b: b, locale: b.Resolve(locale), lex: map[string][]string{}}
}

// T returns the localized string for key with {name} placeholders filled.
func (x *texts) T(key string, args ...string) string { return x.b.Text(x.locale, key, args...) }

// lexicon returns the lower-cased union of a lexicon across every loaded locale.
func (x *texts) lexicon(key string) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if l, ok := x.lex[key]; ok {
		return l
	}
	seen := map[string]bool{}
	var out []string
	for _, loc := range x.b.Locales() {
		for _, s := range x.b.List(loc, key) {
			s = strings.ToLower(strings.TrimSpace(s))
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	x.lex[key] = out
	return out
}

func (x *texts) inLexicon(key, normalized string) bool {
	for _, s := range x.lexicon(key) {
		if s == normalized {
			return true
		}
	}
	return false
}

func (x *texts) containsAny(key, normalized string) bool {
	for _, s := range x.lexicon(key) {
		if strings.Contains(normalized, s) {
			return true
		}
	}
	return false
}

// evidence renders a machine evidence scope through the "evidence.<scope>"
// pack key; unknown scopes (e.g. legacy free-text rows) are returned as is.
func (x *texts) evidence(scope string) string {
	if scope == "" || !x.b.Has(x.locale, "evidence."+scope) {
		return scope
	}
	return x.T("evidence." + scope)
}

// memoryKind labels a memory kind; unknown kinds keep their id.
func (x *texts) memoryKind(kind string) string {
	if kind == "" || !x.b.Has(x.locale, "memory.kind."+kind) {
		return kind
	}
	return x.T("memory.kind." + kind)
}
