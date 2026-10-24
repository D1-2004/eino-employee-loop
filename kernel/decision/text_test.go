package decision

import (
	"sort"
	"strings"
	"testing"
)

func TestLocalePacksHaveTheSameKeys(t *testing.T) {
	textsOnce.Do(loadTexts)
	keys := func(pack map[string]string) string {
		var out []string
		for k := range pack {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	want := keys(texts["en"])
	if want == "" {
		t.Fatal("no English pack")
	}
	for tag, pack := range texts {
		if got := keys(pack); got != want {
			t.Errorf("%s keys %s, want %s", tag, got, want)
		}
	}
	if text("zh-Hans", "repair.invalid") != texts["zh-CN"]["repair.invalid"] || text("fr", "repair.invalid") != texts["en"]["repair.invalid"] {
		t.Fatal("locale fallback")
	}
}
