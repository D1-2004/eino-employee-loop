package i18n_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/i18n"
)

func TestShippedLocalesDefineEveryContractKey(t *testing.T) {
	types, err := contract.StandardEventTypes()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := contract.StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	b := i18n.Default()
	keys := i18n.ContractKeys(types, tools)
	for _, loc := range b.Locales() {
		for _, k := range keys {
			if !b.Has(loc, k) {
				t.Errorf("%s: missing %s", loc, k)
			}
		}
	}
	if missing := b.Missing(keys); len(missing) > 0 {
		t.Fatalf("locales must not rely on fallback for contract keys: %v", missing)
	}
}

func TestShippedLocalesHaveIdenticalKeySets(t *testing.T) {
	b := i18n.Default()
	locales := b.Locales()
	if len(locales) < 2 {
		t.Fatalf("expected at least two locales, got %v", locales)
	}
	ref := b.Keys(locales[0])
	for _, loc := range locales[1:] {
		got := b.Keys(loc)
		if strings.Join(got, ",") != strings.Join(ref, ",") {
			t.Errorf("%s keys differ from %s:\n%s", loc, locales[0], diff(ref, got))
		}
	}
}

func diff(a, b []string) string {
	in := map[string]int{}
	for _, k := range a {
		in[k]++
	}
	for _, k := range b {
		in[k]--
	}
	var out []string
	for k, v := range in {
		if v > 0 {
			out = append(out, "- "+k)
		} else if v < 0 {
			out = append(out, "+ "+k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func TestResolveFallsBackByScriptAndLanguage(t *testing.T) {
	b := i18n.Default()
	cases := map[string]string{"zh-Hans": "zh-CN", "zh_hans_cn": "zh-CN", "zh": "zh-CN", "en-GB": "en", "fr": "en", "": "en"}
	for in, want := range cases {
		if got := b.Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOverridesAndPlaceholders(t *testing.T) {
	b := i18n.Default().Clone()
	if err := b.Merge("en", map[string]any{"kernel.terminal.tools": "Use {tools}."}); err != nil {
		t.Fatal(err)
	}
	if got := b.Text("en", "kernel.terminal.tools", "tools", "reply, stay_quiet"); got != "Use reply, stay_quiet." {
		t.Fatalf("override: %q", got)
	}
	if got := i18n.Default().Text("en", "kernel.terminal.tools", "tools", "x"); strings.HasPrefix(got, "Use ") {
		t.Fatal("Clone must not mutate the shared bundle")
	}
	if err := b.Load("fr", []byte(`{"kernel.block.window":"Fenêtre actuelle :"}`)); err != nil {
		t.Fatal(err)
	}
	if got := b.Text("fr-CA", "kernel.block.window"); got != "Fenêtre actuelle :" {
		t.Fatalf("added locale: %q", got)
	}
	if got := b.Text("fr", "kernel.block.memory"); got != i18n.Default().Text("en", "kernel.block.memory") {
		t.Fatalf("missing keys fall back to en: %q", got)
	}
}
