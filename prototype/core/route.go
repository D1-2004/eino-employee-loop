package core

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// hostDirectReply returns a Host-owned reply that must bypass the model loop.
// Used for pure greetings and trivial arithmetic so old registry/memory cannot leak.
func (x *texts) hostDirectReply(text string) (string, bool) {
	if x.isGreeting(text) {
		return x.greetingReply(text), true
	}
	if ans, ok := x.tryEvalArithmetic(text); ok {
		return ans, true
	}
	return "", false
}

// greetingGroups pairs greeting lexicons with their reply keys; anything else
// that counts as a greeting gets the generic hello reply.
var greetingGroups = []string{"morning", "noon", "afternoon", "evening", "night", "thanks", "ok", "decline"}

func (x *texts) greetingReply(text string) string {
	t := normalizeUtterance(text)
	for _, g := range greetingGroups {
		if x.inLexicon("lexicon.greeting."+g, t) {
			return x.T("runtime.reply.greeting." + g)
		}
	}
	return x.T("runtime.reply.greeting.hello")
}

func (x *texts) isGreetingWord(t string) bool {
	if x.inLexicon("lexicon.greeting.hello", t) {
		return true
	}
	for _, g := range greetingGroups {
		if x.inLexicon("lexicon.greeting."+g, t) {
			return true
		}
	}
	return false
}

var arithRe = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([+\-*/×÷])\s*(\d+(?:\.\d+)?)\s*[=？?]?\s*$`)

func (x *texts) tryEvalArithmetic(text string) (string, bool) {
	t := strings.TrimSpace(text)
	t = strings.ReplaceAll(t, "＋", "+")
	t = strings.ReplaceAll(t, "－", "-")
	t = strings.ReplaceAll(t, "＝", "=")
	m := arithRe.FindStringSubmatch(t)
	if m == nil {
		return "", false
	}
	a, err1 := strconv.ParseFloat(m[1], 64)
	b, err2 := strconv.ParseFloat(m[3], 64)
	if err1 != nil || err2 != nil {
		return "", false
	}
	var v float64
	switch m[2] {
	case "+":
		v = a + b
	case "-":
		v = a - b
	case "*", "×":
		v = a * b
	case "/", "÷":
		if b == 0 {
			return x.T("runtime.reply.divide_by_zero"), true
		}
		v = a / b
	default:
		return "", false
	}
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10), true
	}
	return strconv.FormatFloat(v, 'f', -1, 64), true
}

// isVagueFragment: underspecified references that need a clarifying ask, not dispatch/silence.
func (x *texts) isVagueFragment(text string) bool {
	t := normalizeUtterance(text)
	if t == "" || x.isGreeting(text) || isTrivialQA(text) || x.isStatusLookup(text) {
		return false
	}
	if x.containsAny("lexicon.vague", t) {
		return true
	}
	if utf8.RuneCountInString(t) <= 16 {
		for _, p := range x.lexicon("lexicon.vague_prefix") {
			if hasWordPrefix(t, p) {
				rest := strings.TrimSpace(strings.TrimPrefix(t, p))
				if rest != "" && !x.hasActionVerb(rest) {
					return true
				}
			}
		}
	}
	return false
}

// hasWordPrefix reports whether t starts with p; a prefix ending in a letter of
// a space-separated script must also end at a word boundary ("um" ≠ "umbrella").
func hasWordPrefix(t, p string) bool {
	if !strings.HasPrefix(t, p) {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(p)
	if last > unicode.MaxASCII || !unicode.IsLetter(last) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(t[len(p):])
	return next == utf8.RuneError || !unicode.IsLetter(next)
}

func (x *texts) hasActionVerb(t string) bool {
	return x.containsAny("lexicon.action_verbs", strings.ToLower(t))
}

// slimContextForFastPath: greetings / trivial Q&A must not see registry or fat memory.
func (x *texts) slimContextForFastPath(winText string) bool {
	return x.isGreeting(winText) || isTrivialQA(winText)
}

func (x *texts) clarifyingAsk(winText string) string {
	if x.isVagueFragment(winText) {
		return x.T("runtime.ask.vague")
	}
	if x.isAmbiguousShort(winText) {
		return x.T("runtime.ask.ambiguous")
	}
	return x.T("runtime.ask.unclear")
}

func (x *texts) hostRouteHint(winText string) string {
	if h := x.hostUtteranceHint(winText); h != "" {
		return h
	}
	if x.isVagueFragment(winText) {
		return x.T("runtime.hint.vague")
	}
	return ""
}

// isGreetingOrAckSkip detects utterances that should skip the stiff ack path
// (greetings, status/memory lookups, summaries — Host answers these in the decision turn).
func (x *texts) isGreetingOrAckSkip(text string) bool {
	return x.isGreeting(text) || isTrivialQA(text) || x.isStatusLookup(text)
}

func normalizeUtterance(text string) string {
	t := strings.TrimSpace(strings.ToLower(text))
	return strings.Trim(t, "\uFF01!\u3002.?\uFF1F~\uFF5E\u3001\uFF0C,")
}

func (x *texts) isGreeting(text string) bool {
	t := normalizeUtterance(text)
	if t == "" {
		return true
	}
	rs := []rune(t)
	if len(rs) <= 2 {
		hasWord := false
		for _, r := range rs {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x2e80 {
				hasWord = true
				break
			}
		}
		if !hasWord {
			return true
		}
	}
	return x.isGreetingWord(t)
}

func (x *texts) isStatusLookup(text string) bool {
	return x.containsAny("lexicon.status", normalizeUtterance(text))
}

var (
	trivialArithRe = regexp.MustCompile(`^[\d\s.+\-*/×÷=？?]+$`)
	digitRe        = regexp.MustCompile(`\d`)
)

// isTrivialQA: short math / one-shot fact that must be answered directly without tools or recap.
func isTrivialQA(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if utf8.RuneCountInString(t) > 24 {
		return false
	}
	// arithmetic like 1+1, 2*3, 10-4, 3÷3
	if trivialArithRe.MatchString(t) && digitRe.MatchString(t) {
		return true
	}
	switch normalizeUtterance(t) {
	case "1+1", "1＋1", "2+1", "2＋1", "3+3", "3＋3":
		return true
	}
	return false
}

// isAmbiguousShort: one vague token without object — prefer ask_human over dispatch.
func (x *texts) isAmbiguousShort(text string) bool {
	t := normalizeUtterance(text)
	if t == "" || x.isGreeting(text) || isTrivialQA(text) || x.isStatusLookup(text) {
		return false
	}
	if x.inLexicon("lexicon.ambiguous", t) {
		return true
	}
	// single CJK token / short english verb without object
	return utf8.RuneCountInString(t) <= 2 && !digitRe.MatchString(t)
}

func (x *texts) hostUtteranceHint(winText string) string {
	switch {
	case isTrivialQA(winText):
		return x.T("runtime.hint.trivial")
	case x.isGreeting(winText):
		return x.T("runtime.hint.greeting")
	case x.isAmbiguousShort(winText):
		return x.T("runtime.hint.ambiguous")
	case x.isStatusLookup(winText):
		return x.T("runtime.hint.status")
	default:
		return ""
	}
}

// isRerun reports whether the window explicitly asks to run finished work again.
func (x *texts) isRerun(winText string) bool {
	return x.containsAny("lexicon.rerun", strings.ToLower(winText))
}
