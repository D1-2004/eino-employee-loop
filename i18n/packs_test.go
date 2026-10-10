package i18n

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// loopDomains are the packs the loop's default bundle may embed.
var loopDomains = map[string]bool{"contract": true, "kernel": true, "scene": true, "toolhost": true, "employeeloop": true}

// contractPrefixes are the key prefixes of the contract pack.
var contractPrefixes = []string{"class.", "mode.", "lane.", "session.", "evidence.", "event.", "tool."}

// The default bundle carries only loop text: each embedded pack is a loop
// domain and holds only that domain's keys.
func TestEmbeddedPacksAreLoopDomainsOnly(t *testing.T) {
	locales, err := fs.ReadDir(localesFS, "locales")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, loc := range locales {
		files, err := fs.ReadDir(localesFS, path.Join("locales", loc.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			domain := strings.TrimSuffix(f.Name(), ".json")
			seen = append(seen, loc.Name()+"/"+domain)
			if !loopDomains[domain] {
				t.Errorf("%s/%s is not a loop pack; embed it in its own package", loc.Name(), f.Name())
				continue
			}
			b := New(Fallback)
			raw, err := fs.ReadFile(localesFS, path.Join("locales", loc.Name(), f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Load(loc.Name(), raw); err != nil {
				t.Fatal(err)
			}
			for _, k := range b.Keys(loc.Name()) {
				if !ownKey(domain, k) {
					t.Errorf("%s/%s holds %q, which is not a %s key", loc.Name(), f.Name(), k, domain)
				}
			}
		}
	}
	sort.Strings(seen)
	if len(seen) == 0 {
		t.Fatal("no embedded packs")
	}
}

func ownKey(domain, key string) bool {
	if domain != "contract" {
		return strings.HasPrefix(key, domain+".")
	}
	for _, p := range contractPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func TestParityReportsMissingKeys(t *testing.T) {
	b := New(Fallback)
	_ = b.Merge("en", map[string]any{"a": "A", "b": "B"})
	_ = b.Merge("zh-CN", map[string]any{"a": "A"})
	if got := strings.Join(b.Parity(), ","); got != "zh-CN: missing b" {
		t.Fatalf("parity: %q", got)
	}
}
