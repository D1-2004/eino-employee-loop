package catalogdoc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/internal/catalogdoc"
)

// shipped lists the locales whose catalog documents are committed.
var shipped = []string{"en", "zh-CN"}

// The committed documents must equal the generator output; regenerate with
// catalogdoc.Command(locale) after changing a catalog or a pack.
func TestCommittedCatalogDocsAreGenerated(t *testing.T) {
	for _, loc := range shipped {
		want, err := catalogdoc.Standard(loc)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("..", "..", catalogdoc.Dir, catalogdoc.FileName(loc))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run %s)", loc, err, catalogdoc.Command(loc))
		}
		if string(got) != want {
			t.Errorf("%s is stale; run: %s", path, catalogdoc.Command(loc))
		}
	}
}

func TestEveryTypeAndToolIsRendered(t *testing.T) {
	std, err := catalogdoc.StandardProfile()
	if err != nil {
		t.Fatal(err)
	}
	types, tools := std.Types, std.Tools
	for _, loc := range shipped {
		if missing := catalogdoc.Check(catalogdoc.Messages(), loc, types, tools); len(missing) > 0 {
			t.Errorf("%s misses keys: %v", loc, missing)
		}
		doc, err := catalogdoc.Standard(loc)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range types {
			if !strings.Contains(doc, "| `"+s.Type+"` |") || strings.Contains(doc, s.SummaryKey) {
				t.Errorf("%s: type %s not rendered with its summary", loc, s.Type)
			}
		}
		for _, s := range tools {
			if !strings.Contains(doc, "| `"+s.Name+"` |") || strings.Contains(doc, s.DescriptionKey) {
				t.Errorf("%s: tool %s not rendered with its description", loc, s.Name)
			}
		}
		if strings.Contains(doc, "catalogdoc.") {
			t.Errorf("%s: unresolved message key in output", loc)
		}
	}
}

func TestRenderEscapesCellsAndOrdersParameters(t *testing.T) {
	b := catalogdoc.Messages().Clone()
	if err := b.Merge("en", map[string]any{"test.summary": "a | b\nc"}); err != nil {
		t.Fatal(err)
	}
	types := []contract.TypeSpec{{Type: "custom.thing", Class: "custom", Mode: contract.ModeListening, Lane: contract.LaneHost, SummaryKey: "test.summary"}}
	tools := []contract.ToolSpec{{
		Name: "t", Kind: contract.ToolRead, Description: "d",
		Parameters: map[string]any{"type": "object", "required": []any{"z", "a"},
			"properties": map[string]any{"z": map[string]any{}, "a": map[string]any{}, "m": map[string]any{}, "b": map[string]any{}}},
	}}
	doc := catalogdoc.Render(b, "en", catalogdoc.Profile{Types: types, Tools: tools}, catalogdoc.Profile{})
	if !strings.Contains(doc, "a \\| b c") {
		t.Errorf("cell not escaped:\n%s", doc)
	}
	if !strings.Contains(doc, "### `custom`\n") {
		t.Errorf("custom class not rendered:\n%s", doc)
	}
	if !strings.Contains(doc, "`z`, `a`, `b?`, `m?`") {
		t.Errorf("parameter order:\n%s", doc)
	}
	if strings.Contains(doc, b.Text("en", "catalogdoc.extensions.title")) {
		t.Errorf("an empty extension profile is not rendered:\n%s", doc)
	}
	ext := catalogdoc.Render(b, "en", catalogdoc.Profile{}, catalogdoc.Profile{Types: types})
	if !strings.Contains(ext, "## "+b.Text("en", "catalogdoc.extensions.title")+"\n") || !strings.Contains(ext, "#### `custom`\n") {
		t.Errorf("extension profile section:\n%s", ext)
	}
}

func TestFileNamesAndCommands(t *testing.T) {
	cases := map[string]string{"en": "catalog.md", "": "catalog.md", "zh-CN": "catalog.zh-CN.md", "zh_cn": "catalog.zh-CN.md"}
	for loc, want := range cases {
		if got := catalogdoc.FileName(loc); got != want {
			t.Errorf("FileName(%q) = %q, want %q", loc, got, want)
		}
	}
	if got := catalogdoc.Command("zh-CN"); got != "go run ./cmd/catalog-doc -locale zh-CN -o docs/catalog.zh-CN.md" {
		t.Errorf("Command = %q", got)
	}
}
