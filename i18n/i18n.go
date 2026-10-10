// Package i18n resolves the natural-language strings of the Employee Loop:
// prompts, repair instructions, context labels, tool and event
// descriptions and user-visible loop text.
//
// Packs are flat JSON objects whose values are strings or string arrays,
// stored as locales/<locale>/<domain>.json and merged per locale. The
// embedded packs (Default) are the loop's own: contract, kernel, scene,
// toolhost and employeeloop. Every other package that speaks (a gateway, a
// Task Manager executor, a store, a command, the prototype) embeds its own
// pack and merges it into a clone of Default with LoadFS (see Extend), so a
// service binary ships only the text of the packages it links. Hosts can
// override any key or add a locale with Bundle.Load / Bundle.Merge without
// touching code. Text placeholders use {name}.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed locales
var localesFS embed.FS

// Fallback is the locale used when a key or locale is missing.
const Fallback = "en"

// Bundle holds message packs keyed by canonical locale tag.
type Bundle struct {
	mu       sync.RWMutex
	packs    map[string]map[string]any
	fallback string
}

// New returns an empty bundle with the given fallback locale.
func New(fallback string) *Bundle {
	if fallback == "" {
		fallback = Fallback
	}
	return &Bundle{packs: map[string]map[string]any{}, fallback: fallback}
}

var (
	defaultOnce   sync.Once
	defaultBundle *Bundle
)

// Default returns a shared bundle with the loop's embedded packs loaded.
// Treat it as read-only; call Clone (or Extend) before merging packs or
// host overrides.
func Default() *Bundle {
	defaultOnce.Do(func() {
		b, err := Embedded()
		if err != nil {
			panic(err)
		}
		defaultBundle = b
	})
	return defaultBundle
}

// Embedded loads a fresh bundle from the loop's embedded packs.
func Embedded() (*Bundle, error) {
	b := New(Fallback)
	if err := b.LoadFS(localesFS, "locales"); err != nil {
		return nil, err
	}
	return b, nil
}

// LoadFS merges every pack under root of fsys, laid out as
// <root>/<locale>/<domain>.json, into b. Packages embed their own packs and
// load them with it.
func (b *Bundle) LoadFS(fsys fs.FS, root string) error {
	locales, err := fs.ReadDir(fsys, root)
	if err != nil {
		return err
	}
	for _, dir := range locales {
		if !dir.IsDir() {
			continue
		}
		files, err := fs.ReadDir(fsys, path.Join(root, dir.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			raw, err := fs.ReadFile(fsys, path.Join(root, dir.Name(), f.Name()))
			if err != nil {
				return err
			}
			if err := b.Load(dir.Name(), raw); err != nil {
				return fmt.Errorf("i18n: %s/%s: %w", dir.Name(), f.Name(), err)
			}
		}
	}
	return nil
}

// Extend returns a clone of Default with the packs under root of fsys
// merged in. Packages that own a pack build their default bundle with it.
func Extend(fsys fs.FS, root string) (*Bundle, error) {
	b := Default().Clone()
	if err := b.LoadFS(fsys, root); err != nil {
		return nil, err
	}
	return b, nil
}

// Parity reports keys that some loaded locale defines and another does not,
// as "<locale>: missing <key>" lines sorted; nil when every locale has the
// same key set. Packages test their own packs with it.
func (b *Bundle) Parity() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	all := map[string]bool{}
	for _, pack := range b.packs {
		for k := range pack {
			all[k] = true
		}
	}
	var out []string
	for loc, pack := range b.packs {
		for k := range all {
			if _, ok := pack[k]; !ok {
				out = append(out, loc+": missing "+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Clone returns an independent copy that can take overrides.
func (b *Bundle) Clone() *Bundle {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c := New(b.fallback)
	for loc, pack := range b.packs {
		cp := make(map[string]any, len(pack))
		for k, v := range pack {
			cp[k] = v
		}
		c.packs[loc] = cp
	}
	return c
}

// Load merges a JSON pack into a locale. Values must be strings or arrays of
// strings; later loads override earlier keys.
func (b *Bundle) Load(locale string, raw []byte) error {
	var entries map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	return b.Merge(locale, entries)
}

// Merge adds entries to a locale.
func (b *Bundle) Merge(locale string, entries map[string]any) error {
	loc := Canonical(locale)
	if loc == "" {
		return fmt.Errorf("i18n: empty locale")
	}
	clean := make(map[string]any, len(entries))
	for k, v := range entries {
		switch x := v.(type) {
		case string:
			clean[k] = x
		case []string:
			clean[k] = append([]string(nil), x...)
		case []any:
			list := make([]string, 0, len(x))
			for _, item := range x {
				s, ok := item.(string)
				if !ok {
					return fmt.Errorf("i18n: %s: list items must be strings", k)
				}
				list = append(list, s)
			}
			clean[k] = list
		default:
			return fmt.Errorf("i18n: %s: values must be strings or string lists", k)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pack := b.packs[loc]
	if pack == nil {
		pack = map[string]any{}
		b.packs[loc] = pack
	}
	for k, v := range clean {
		pack[k] = v
	}
	return nil
}

// Locales lists loaded locales.
func (b *Bundle) Locales() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.packs))
	for k := range b.packs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Canonical normalizes a BCP 47 tag: "zh_hans" -> "zh-Hans", "EN-us" -> "en-US".
func Canonical(tag string) string {
	tag = strings.TrimSpace(strings.ReplaceAll(tag, "_", "-"))
	if tag == "" {
		return ""
	}
	parts := strings.Split(tag, "-")
	parts[0] = strings.ToLower(parts[0])
	for i := 1; i < len(parts); i++ {
		switch len(parts[i]) {
		case 2:
			parts[i] = strings.ToUpper(parts[i])
		case 4:
			parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
		default:
			parts[i] = strings.ToLower(parts[i])
		}
	}
	return strings.Join(parts, "-")
}

// scriptRegions maps script subtags to the region pack that carries them.
var scriptRegions = map[string]string{"zh-Hans": "zh-CN", "zh-Hant": "zh-TW"}

// Resolve picks the best loaded locale for a tag: exact match, script alias,
// same base language, then the fallback.
func (b *Bundle) Resolve(tag string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.resolveLocked(tag)
}

func (b *Bundle) resolveLocked(tag string) string {
	loc := Canonical(tag)
	if loc == "" {
		return b.fallback
	}
	if _, ok := b.packs[loc]; ok {
		return loc
	}
	parts := strings.Split(loc, "-")
	if len(parts) >= 2 {
		if alias, ok := scriptRegions[parts[0]+"-"+parts[1]]; ok {
			if _, ok := b.packs[alias]; ok {
				return alias
			}
		}
	}
	base := parts[0]
	if _, ok := b.packs[base]; ok {
		return base
	}
	candidates := make([]string, 0)
	for k := range b.packs {
		if strings.HasPrefix(k, base+"-") {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) > 0 {
		sort.Strings(candidates)
		return candidates[0]
	}
	return b.fallback
}

func (b *Bundle) lookup(locale, key string) (any, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	loc := b.resolveLocked(locale)
	if v, ok := b.packs[loc][key]; ok {
		return v, true
	}
	if v, ok := b.packs[b.fallback][key]; ok {
		return v, true
	}
	return nil, false
}

// Has reports whether a key resolves for the locale (including fallback).
func (b *Bundle) Has(locale, key string) bool {
	_, ok := b.lookup(locale, key)
	return ok
}

// Text returns the string for key with {name} placeholders replaced by the
// name/value pairs in args. A missing key returns the key itself so the gap
// is visible; Check reports such gaps in tests.
func (b *Bundle) Text(locale, key string, args ...string) string {
	v, ok := b.lookup(locale, key)
	if !ok {
		return key
	}
	s, ok := v.(string)
	if !ok {
		return key
	}
	return Format(s, args...)
}

// List returns a string list. A missing key returns nil.
func (b *Bundle) List(locale, key string) []string {
	v, ok := b.lookup(locale, key)
	if !ok {
		return nil
	}
	list, ok := v.([]string)
	if !ok {
		return nil
	}
	return append([]string(nil), list...)
}

// Format replaces {name} placeholders with name/value pairs.
func Format(s string, args ...string) string {
	if len(args) < 2 {
		return s
	}
	pairs := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		pairs = append(pairs, "{"+args[i]+"}", args[i+1])
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// Missing returns, for each loaded locale, the keys absent from that locale's
// own pack (fallback not applied). Use it to keep packs complete.
func (b *Bundle) Missing(keys []string) map[string][]string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := map[string][]string{}
	for loc, pack := range b.packs {
		for _, k := range keys {
			if _, ok := pack[k]; !ok {
				out[loc] = append(out[loc], k)
			}
		}
	}
	return out
}

// Keys returns the keys of a locale's own pack.
func (b *Bundle) Keys(locale string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pack := b.packs[Canonical(locale)]
	out := make([]string, 0, len(pack))
	for k := range pack {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
