package loop

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// ExtraParameters is the ToolInfo.Extra key carrying the original JSON
// Schema map, so a model adapter can send it without a lossy round trip.
const ExtraParameters = "employee_loop.parameters"

// ToolInfos converts tools to Eino tool infos, in the given order.
func ToolInfos(tools []Tool) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(tools))
	for _, t := range tools {
		params := t.Schema
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("employee loop: tool %s schema: %w", t.Name, err)
		}
		js := &jsonschema.Schema{}
		if err := json.Unmarshal(raw, js); err != nil {
			return nil, fmt.Errorf("employee loop: tool %s schema: %w", t.Name, err)
		}
		out = append(out, &schema.ToolInfo{
			Name:        t.Name,
			Desc:        t.Description,
			Extra:       map[string]any{ExtraParameters: params},
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(js),
		})
	}
	return out, nil
}

// ValidateArguments checks arguments against the portable subset of JSON
// Schema used by tool contracts: type, required, additionalProperties=false,
// properties, enum, minLength, maxLength, minItems, maxItems and items.
// Hosts with a full validator add it through Config.ValidateBatch.
func ValidateArguments(schemaMap map[string]any, args map[string]any) []string {
	var problems []string
	validateValue("", schemaMap, args, &problems)
	sort.Strings(problems)
	return problems
}

func validateValue(path string, s map[string]any, v any, problems *[]string) {
	if s == nil {
		return
	}
	name := path
	if name == "" {
		name = "arguments"
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		match := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				match = true
				break
			}
		}
		if !match {
			*problems = append(*problems, fmt.Sprintf("%s: value not in enum", name))
			return
		}
	}
	typ, _ := s["type"].(string)
	switch typ {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected object", name))
			return
		}
		props, _ := s["properties"].(map[string]any)
		for _, req := range stringList(s["required"]) {
			if _, ok := obj[req]; !ok {
				*problems = append(*problems, fmt.Sprintf("%s: missing required %q", name, req))
			}
		}
		if ap, ok := s["additionalProperties"].(bool); ok && !ap {
			for k := range obj {
				if _, known := props[k]; !known {
					*problems = append(*problems, fmt.Sprintf("%s: unknown property %q", name, k))
				}
			}
		}
		for k, val := range obj {
			if ps, ok := props[k].(map[string]any); ok {
				validateValue(joinPath(path, k), ps, val, problems)
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected array", name))
			return
		}
		if n, ok := number(s["minItems"]); ok && float64(len(arr)) < n {
			*problems = append(*problems, fmt.Sprintf("%s: too few items", name))
		}
		if n, ok := number(s["maxItems"]); ok && float64(len(arr)) > n {
			*problems = append(*problems, fmt.Sprintf("%s: too many items", name))
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, item := range arr {
				validateValue(fmt.Sprintf("%s[%d]", name, i), is, item, problems)
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected string", name))
			return
		}
		runes := float64(len([]rune(str)))
		if n, ok := number(s["minLength"]); ok && runes < n {
			*problems = append(*problems, fmt.Sprintf("%s: too short", name))
		}
		if n, ok := number(s["maxLength"]); ok && runes > n {
			*problems = append(*problems, fmt.Sprintf("%s: too long", name))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected boolean", name))
		}
	case "number", "integer":
		f, ok := v.(float64)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected number", name))
			return
		}
		if typ == "integer" && f != float64(int64(f)) {
			*problems = append(*problems, fmt.Sprintf("%s: expected integer", name))
		}
	}
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, i := range x {
			if s, ok := i.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}
