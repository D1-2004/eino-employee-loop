package schema_test

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/schema"
)

// validator checks instances against the subset of JSON Schema 2020-12 the
// embedded schemas use: $ref (relative file and #/$defs pointers), type,
// enum, const, required, properties, additionalProperties, items,
// minLength, minimum, format date-time, allOf and anyOf. The module has no
// full validator dependency (eino-contrib/jsonschema only reflects).
type validator struct {
	docs map[string]map[string]any
}

func loadValidator(t *testing.T) *validator {
	t.Helper()
	v := &validator{docs: map[string]map[string]any{}}
	for _, name := range schema.JSONSchemas() {
		raw, err := schema.Read(name)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v.docs[name] = doc
	}
	return v
}

// resolve returns the schema a $ref names, relative to file, and the file
// that holds it.
func (v *validator) resolve(file, ref string) (map[string]any, string, error) {
	target, pointer, _ := strings.Cut(ref, "#")
	if target == "" {
		target = file
	}
	node, ok := v.docs[target]
	if !ok {
		return nil, "", fmt.Errorf("unknown schema file %q", target)
	}
	for _, part := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if part == "" {
			continue
		}
		next, ok := node[part].(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("unresolved pointer %q in %s", pointer, target)
		}
		node = next
	}
	return node, target, nil
}

// Validate returns the problems of instance against a schema file.
func (v *validator) Validate(file string, instance any) []string {
	var raw []byte
	switch x := instance.(type) {
	case []byte:
		raw = x
	case json.RawMessage:
		raw = x
	default:
		var err error
		if raw, err = json.Marshal(instance); err != nil {
			return []string{err.Error()}
		}
	}
	var val any
	if err := json.Unmarshal(raw, &val); err != nil {
		return []string{err.Error()}
	}
	var problems []string
	v.check(file, v.docs[file], val, "$", &problems)
	sort.Strings(problems)
	return problems
}

func (v *validator) check(file string, s map[string]any, val any, path string, problems *[]string) {
	add := func(format string, args ...any) {
		*problems = append(*problems, path+": "+fmt.Sprintf(format, args...))
	}
	if s == nil {
		add("missing schema")
		return
	}
	if ref, ok := s["$ref"].(string); ok {
		target, targetFile, err := v.resolve(file, ref)
		if err != nil {
			add("%v", err)
			return
		}
		v.check(targetFile, target, val, path, problems)
	}
	for _, sub := range list(s["allOf"]) {
		v.check(file, sub, val, path, problems)
	}
	if anyOf := list(s["anyOf"]); len(anyOf) > 0 {
		matched := false
		for _, sub := range anyOf {
			var p []string
			v.check(file, sub, val, path, &p)
			if len(p) == 0 {
				matched = true
				break
			}
		}
		if !matched {
			add("matches no anyOf branch")
		}
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, val) {
		add("must equal %v", c)
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if reflect.DeepEqual(e, val) {
				found = true
				break
			}
		}
		if !found {
			add("value %v not in enum", val)
		}
	}
	if typ, ok := s["type"].(string); ok && !hasType(typ, val) {
		add("expected %s, got %T", typ, val)
		return
	}
	switch x := val.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if _, ok := x[r.(string)]; !ok {
				add("missing required %q", r)
			}
		}
		for k, item := range x {
			if ps, ok := props[k].(map[string]any); ok {
				v.check(file, ps, item, path+"."+k, problems)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					add("unknown property %q", k)
				}
			case map[string]any:
				v.check(file, ap, item, path+"."+k, problems)
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, item := range x {
				v.check(file, items, item, fmt.Sprintf("%s[%d]", path, i), problems)
			}
		}
	case string:
		if n, ok := s["minLength"].(float64); ok && float64(len([]rune(x))) < n {
			add("shorter than %v", n)
		}
		if s["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, x); err != nil {
				add("not a date-time: %v", err)
			}
		}
	case float64:
		if n, ok := s["minimum"].(float64); ok && x < n {
			add("below minimum %v", n)
		}
	}
}

// list returns the schema objects of an allOf/anyOf array.
func list(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hasType(typ string, val any) bool {
	switch typ {
	case "object":
		_, ok := val.(map[string]any)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "number":
		_, ok := val.(float64)
		return ok
	case "integer":
		f, ok := val.(float64)
		return ok && f == math.Trunc(f)
	case "null":
		return val == nil
	}
	return false
}

// refs collects every $ref in a schema document.
func refs(node any, out *[]string) {
	switch x := node.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "$ref" {
				if s, ok := v.(string); ok {
					*out = append(*out, s)
				}
				continue
			}
			refs(v, out)
		}
	case []any:
		for _, v := range x {
			refs(v, out)
		}
	}
}
