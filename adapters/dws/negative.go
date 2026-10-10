package dws

import "strings"

// NegativeReaction reports whether a DingTalk emotion name/text is a correction /
// reject signal. Adapter-specific — Core only sees Trigger.Kind=reaction.
func NegativeReaction(name, text string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	t := strings.ToLower(strings.TrimSpace(text))
	for _, x := range []string{"打叉", "拒绝", "弱", "鄙视", "wrong", "sayno", "thumbsdown", "dislike"} {
		if n == x || t == x {
			return true
		}
	}
	blob := name + text
	for _, sub := range []string{"❌", "叉", "不对", "错了"} {
		if strings.Contains(blob, sub) {
			return true
		}
	}
	return false
}
