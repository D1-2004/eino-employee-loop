package entry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Guard source ownership, not filenames: a schema/worker cannot quietly move
// back into Entry, and a business module cannot acquire application/adapter code.
func TestArchitectureDependenciesAndModuleOwnership(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	dirs, err := os.ReadDir(filepath.Join(root, "modules"))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, dir := range dirs {
		if dir.IsDir() {
			got = append(got, dir.Name())
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "chat,context,entry,execution,interaction,owner,task" {
		t.Fatal("the source tree no longer matches the seven business modules", got)
	}
	base := "github.com/D1-2004/eino-employee-loop/"
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "archive" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, dependency := range tree.Imports {
			value, _ := strconv.Unquote(dependency.Path.Value)
			if !strings.HasPrefix(value, base) {
				continue
			}
			value = strings.TrimPrefix(value, base)
			if strings.HasPrefix(rel, "kernel/") && (strings.HasPrefix(value, "modules/") || strings.HasPrefix(value, "app/") || strings.HasPrefix(value, "adapters/")) {
				t.Errorf("generic kernel imports application behavior: %s -> %s", rel, value)
			}
			if strings.HasPrefix(rel, "modules/") && !strings.HasPrefix(rel, "modules/entry/") && (strings.HasPrefix(value, "modules/entry") || strings.HasPrefix(value, "app/") || strings.HasPrefix(value, "adapters/")) {
				t.Errorf("business module imports assembly/adapter: %s -> %s", rel, value)
			}
			if strings.HasPrefix(rel, "modules/entry/") && strings.HasPrefix(value, "adapters/") {
				t.Errorf("default concrete choices belong in app/local: %s -> %s", rel, value)
			}
		}
		if strings.HasPrefix(rel, "modules/entry/") {
			ast.Inspect(tree, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.ValueSpec:
					for _, name := range node.Names {
						if strings.HasSuffix(strings.ToLower(name.Name), "policy") {
							t.Errorf("business policy returned to Entry: %s:%s", rel, name.Name)
						}
					}
				case *ast.BasicLit:
					if node.Kind == token.STRING {
						value, _ := strconv.Unquote(node.Value)
						switch value {
						case "entry-jobs", "owner-jobs", "review-holds", "execution-requests", "execution-file-effects", "outbox", "message-jobs":
							t.Errorf("Entry is interpreting another module's private storage: %s: %s", rel, value)
						}
					}
				case *ast.CallExpr:
					if selector, ok := node.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := selector.X.(*ast.Ident); ok && (pkg.Name == "kernel" && selector.Sel.Name == "Spec" || pkg.Name == "loop" && selector.Sel.Name == "ToolInfos") {
							t.Errorf("business tool construction returned to Entry: %s", rel)
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
