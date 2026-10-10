package hostext_test

import (
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/examples/hostext"
	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/internal/catalogdoc"
	kt "github.com/D1-2004/eino-employee-loop/kernel/kerneltest"
	"github.com/D1-2004/eino-employee-loop/toolhost"
)

// Every shipped locale of the profile's pack defines the same keys.
func TestPackLocalesHaveSameKeys(t *testing.T) {
	b := i18n.New(i18n.Fallback)
	if err := hostext.LoadMessages(b); err != nil {
		t.Fatal(err)
	}
	if len(b.Locales()) < 2 {
		t.Fatalf("expected at least two locales, got %v", b.Locales())
	}
	if gaps := b.Parity(); len(gaps) > 0 {
		t.Fatalf("pack locales differ: %v", gaps)
	}
}

// The profile extends the standard catalogs without touching them, and its
// texts come from its own pack, never from the loop's.
func TestProfileExtendsTheStandardCatalogs(t *testing.T) {
	types, err := hostext.EventTypes()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := hostext.Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) == 0 || len(tools) == 0 {
		t.Fatal("the profile ships types and tools")
	}
	cat, err := hostext.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	std := contract.StandardCatalog()
	for _, s := range types {
		if _, ok := std.Lookup(s.Type); ok {
			t.Errorf("%s leaked into the standard catalog", s.Type)
		}
		if _, ok := cat.Lookup(s.Type); !ok {
			t.Errorf("Catalog misses %s", s.Type)
		}
	}
	for _, s := range std.Types() {
		if _, ok := cat.Lookup(s.Type); !ok {
			t.Errorf("Catalog misses standard type %s", s.Type)
		}
	}
	loop := i18n.Default()
	msgs, err := hostext.Messages()
	if err != nil {
		t.Fatal(err)
	}
	keys := i18n.ContractKeys(types, tools)
	for _, loc := range msgs.Locales() {
		for _, k := range keys {
			if !msgs.Has(loc, k) {
				t.Errorf("%s: missing %s", loc, k)
			}
		}
	}
	for _, s := range types {
		if loop.Has(i18n.Fallback, s.SummaryKey) {
			t.Errorf("%s: the loop's default bundle carries %s", s.Type, s.SummaryKey)
		}
	}
	for _, s := range tools {
		if loop.Has(i18n.Fallback, s.DescriptionKey) {
			t.Errorf("%s: the loop's default bundle carries %s", s.Name, s.DescriptionKey)
		}
	}
}

// toolhost offers only the standard tools even when its catalog lists the
// profile's: the platform executes those with its own contract.Host.
func TestToolhostDoesNotOfferProfileTools(t *testing.T) {
	std, err := contract.StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	ext, err := hostext.Tools()
	if err != nil {
		t.Fatal(err)
	}
	h := toolhost.New(toolhost.Ports{Journal: kt.NewJournal()}, toolhost.WithCatalog(append(std, ext...)))
	for _, s := range h.Specs() {
		for _, e := range ext {
			if s.Name == e.Name {
				t.Errorf("toolhost offers %s", s.Name)
			}
		}
	}
}

// A platform can render its profile next to the standard catalogs.
func TestProfileRendersWithItsPack(t *testing.T) {
	types, _ := hostext.EventTypes()
	tools, _ := hostext.Tools()
	std, err := catalogdoc.StandardProfile()
	if err != nil {
		t.Fatal(err)
	}
	b := catalogdoc.Messages().Clone()
	if err := hostext.LoadMessages(b); err != nil {
		t.Fatal(err)
	}
	for _, loc := range []string{"en", "zh-CN"} {
		doc := catalogdoc.Render(b, loc, std, catalogdoc.Profile{Types: types, Tools: tools})
		if strings.Contains(doc, "hostext.") || strings.Contains(doc, "catalogdoc.") {
			t.Errorf("%s: unresolved key in output", loc)
		}
		for _, s := range tools {
			if !strings.Contains(doc, "| `"+s.Name+"` |") {
				t.Errorf("%s: tool %s not rendered", loc, s.Name)
			}
		}
	}
}
