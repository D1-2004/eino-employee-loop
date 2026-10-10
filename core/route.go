package core

import (
	"regexp"
	"strconv"
	"strings"
)

// hostDirectReply returns a Host-owned reply that must bypass the model loop.
// Used for pure greetings and trivial arithmetic so old registry/memory cannot leak.
func hostDirectReply(text string) (string, bool) {
	if isGreeting(text) {
		return greetingReply(text), true
	}
	if ans, ok := tryEvalArithmetic(text); ok {
		return ans, true
	}
	return "", false
}

func greetingReply(text string) string {
	t := normalizeUtterance(text)
	switch t {
	case "早上好", "早":
		return "早，我在。"
	case "中午好":
		return "中午好，我在。"
	case "下午好":
		return "下午好，我在。"
	case "晚上好":
		return "晚上好，我在。"
	case "晚安":
		return "晚安。"
	case "谢谢", "多谢", "感谢", "thx", "thanks", "thank you":
		return "不客气。"
	case "ok", "okay", "好的", "好", "嗯", "哦", "收到":
		return "好。"
	case "不需要", "先不用", "算了":
		return "好，先这样。"
	default:
		return "你好，我在。有事直接说。"
	}
}

var arithRe = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([+\-*/×÷])\s*(\d+(?:\.\d+)?)\s*[=？?]?\s*$`)

func tryEvalArithmetic(text string) (string, bool) {
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
			return "除数不能是 0。", true
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
func isVagueFragment(text string) bool {
	t := normalizeUtterance(text)
	if t == "" || isGreeting(text) || isTrivialQA(text) || isStatusLookup(text) {
		return false
	}
	hints := []string{
		"模型请求", "模型事件", "那个事件", "那个请求", "刚才那个", "之前那个",
		"这个事件", "这件事", "那个事", "咋回事", "怎么回事",
	}
	for _, h := range hints {
		if strings.Contains(t, h) {
			return true
		}
	}
	rs := []rune(t)
	if len(rs) <= 16 {
		prefix := []string{"好吧", "好的", "嗯", "哦", "额", "呃"}
		for _, p := range prefix {
			if strings.HasPrefix(t, p) {
				rest := strings.TrimSpace(strings.TrimPrefix(t, p))
				if rest != "" && !hasActionVerb(rest) {
					return true
				}
			}
		}
	}
	return false
}

func hasActionVerb(t string) bool {
	verbs := []string{"查", "看", "写", "发", "跑", "停", "取消", "总结", "对比", "记住", "删", "改", "帮我", "请", "告诉"}
	for _, v := range verbs {
		if strings.Contains(t, v) {
			return true
		}
	}
	return false
}

// slimContextForFastPath: greetings / trivial Q&A must not see registry or fat memory.
func slimContextForFastPath(winText string) bool {
	return isGreeting(winText) || isTrivialQA(winText)
}

func clarifyingAsk(winText string) string {
	if isVagueFragment(winText) {
		return "你具体指哪一次？补一句对象或时间我接着看。"
	}
	if isAmbiguousShort(winText) {
		return "你具体想办哪件事？补一句对象或目标我再动手。"
	}
	return "没太听清，你再说具体一点？"
}

func hostRouteHint(winText string) string {
	if h := hostUtteranceHint(winText); h != "" {
		return h
	}
	if isVagueFragment(winText) {
		return "Host route hint: utterance is a vague/incomplete reference. Call ask_human with one brief clarifying question. Do NOT stay_quiet. Do NOT dispatch_task. Do NOT revive unrelated prior work."
	}
	return ""
}
