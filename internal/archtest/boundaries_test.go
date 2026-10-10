// Package archtest enforces the service boundaries of the module by
// reading the imports of every non-test Go file. Every package must match a
// rule, and each rule is an allow-list: a new top-level package is not
// importable by the loop, a transport or a gateway until a rule says so.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/D1-2004/eino-employee-loop/"

// imports maps each package directory (module relative) to the module
// packages its non-test files import.
func imports(t *testing.T) map[string]map[string]bool {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		rel = filepath.ToSlash(rel)
		if out[rel] == nil {
			out[rel] = map[string]bool{}
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, module) {
				out[rel][strings.TrimPrefix(p, module)] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal("module root not found")
	}
	return out
}

func under(pkg string, prefixes ...string) bool {
	for _, p := range prefixes {
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

// loop lists the Employee Loop's packages. They may import only each
// other, contract and i18n.
var loop = []string{"kernel", "scene", "toolhost", "store", "employeeloop"}

type rule struct {
	name    string
	applies func(pkg string) bool
	allowed func(dep string) bool
}

func only(prefixes ...string) func(string) bool {
	return func(d string) bool { return under(d, prefixes...) }
}

func nothing(string) bool { return false }

var rules = []rule{
	{"contract imports nothing", func(p string) bool { return p == "contract" }, nothing},
	{"i18n imports only contract", func(p string) bool { return p == "i18n" }, only("contract")},
	{"loop packages import only the loop, contract and i18n", func(p string) bool { return under(p, loop...) },
		only(append([]string{"contract", "i18n"}, loop...)...)},
	{"transports import only contract", func(p string) bool { return under(p, "transport") }, only("contract")},
	{"gateways import only contract and i18n", func(p string) bool { return under(p, "gateway") }, only("contract", "i18n")},
	{"providers import nothing of the module", func(p string) bool { return under(p, "providers") }, nothing},
	{"task managers import only contract, i18n and taskmgr", func(p string) bool { return under(p, "taskmgr") },
		only("contract", "i18n", "taskmgr")},
	{"schema imports nothing of the module", func(p string) bool { return p == "schema" }, nothing},
	{"examples import only contract and i18n", func(p string) bool { return under(p, "examples") }, only("contract", "i18n")},
	{"the catalog document reads only contract and i18n", func(p string) bool { return p == "internal/catalogdoc" }, only("contract", "i18n")},
	{"the command helpers use only i18n", func(p string) bool { return p == "internal/cli" }, only("i18n")},
	{"commands assemble services from anything but the prototype", func(p string) bool { return under(p, "cmd") },
		func(d string) bool { return !under(d, "prototype") }},
	{"the prototype may use the module", func(p string) bool { return under(p, "prototype") },
		func(string) bool { return true }},
	{"only the prototype imports the prototype", func(p string) bool { return !under(p, "prototype") },
		func(d string) bool { return !under(d, "prototype") }},
}

func TestServiceBoundaries(t *testing.T) {
	all := imports(t)
	pkgs := make([]string, 0, len(all))
	for p := range all {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	checked := 0
	for _, pkg := range pkgs {
		matched := false
		for _, r := range rules {
			if !r.applies(pkg) {
				continue
			}
			checked++
			if r.name != "only the prototype imports the prototype" {
				matched = true
			}
			for dep := range all[pkg] {
				if dep != pkg && !under(dep, pkg) && !r.allowed(dep) {
					t.Errorf("%s: %s imports %s", r.name, pkg, dep)
				}
			}
		}
		if !matched {
			t.Errorf("%s matches no boundary rule; add one before it imports the module", pkg)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d package checks ran; is the walk rooted at the module?", checked)
	}
}

// The rules reject the violations they exist for, including imports of a
// package that no rule knows yet.
func TestRulesRejectKnownViolations(t *testing.T) {
	violations := map[string]string{
		"transport/httpapi": "taskmgr",
		"kernel":            "gateway/dws",
		"scene":             "newservice",
		"employeeloop":      "taskmgr/memtask",
		"i18n":              "kernel",
		"gateway/stdin":     "employeeloop",
		"taskmgr/memtask":   "scene",
	}
	for pkg, dep := range violations {
		rejected := false
		for _, r := range rules {
			if r.applies(pkg) && !r.allowed(dep) {
				rejected = true
			}
		}
		if !rejected {
			t.Errorf("%s -> %s is allowed", pkg, dep)
		}
	}
}
