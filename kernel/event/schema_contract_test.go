package event_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/kernel/event"
)

// The JSON Schema lists exactly the fields event.Event encodes.
func TestEventSchemaMatchesTheGoType(t *testing.T) {
	raw, err := os.ReadFile("event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var want []string
	typ := reflect.TypeOf(event.Event{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		want = append(want, name)
	}
	var got []string
	for name := range doc.Properties {
		got = append(got, name)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schema properties %v, Go fields %v", got, want)
	}
}

// The JSON Schema states the rules of event.Event.Validate, for an event
// decoded with DisallowUnknownFields, except at the edges its description
// lists.
func TestEventSchemaAgreesWithValidate(t *testing.T) {
	raw, err := os.ReadFile("event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	encode := func(e event.Event) string {
		out, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	full := event.Event{
		ID: "m1", Source: "chat.im", Employee: "e1", Type: "message.addressed", Class: event.Business, Mode: event.Triggered,
		Scene:      event.SceneRef{ID: "s1", Kind: event.Group, Tenant: "t1", Provider: "dingtalk", Locator: "cid"},
		Actor:      event.Actor{ID: "u1", Kind: event.Human, DisplayName: "Ann"},
		OccurredAt: at, Locale: "zh-CN", Refs: map[string]string{event.RefReplyTo: "m0"}, Text: "hi", Payload: json.RawMessage(`{"k":1}`),
	}
	// with changes one field of the valid event.
	with := func(change func(*event.Event)) string {
		e := full
		change(&e)
		return encode(e)
	}
	for _, c := range []struct {
		name  string
		doc   string
		valid bool
	}{
		{"every field", encode(full), true},
		{"Go's encoding of an employee-wide event (empty scene and actor)", with(func(e *event.Event) { e.Scene, e.Actor = event.SceneRef{}, event.Actor{} }), true},
		{"scene and actor absent", `{"id":"e1","source":"admin","type":"employee.enabled","occurred_at":"2026-10-11T08:00:00Z"}`, true},
		{"a class the catalog decides on (Route rejects it)", with(func(e *event.Event) { e.Class = "other" }), true},
		{"blank id", with(func(e *event.Event) { e.ID = "  " }), false},
		{"missing source", `{"id":"e1","type":"message.addressed","occurred_at":"2026-10-11T08:00:00Z"}`, false},
		{"blank type", with(func(e *event.Event) { e.Type = "\t" }), false},
		{"zero time", with(func(e *event.Event) { e.OccurredAt = time.Time{} }), false},
		{"missing time", `{"id":"e1","source":"s","type":"message.addressed"}`, false},
		{"unknown actor kind", with(func(e *event.Event) { e.Actor.Kind = "robot" }), false},
		{"unknown field", `{"id":"e1","source":"s","type":"message.addressed","occurred_at":"2026-10-11T08:00:00Z","extra":1}`, false},
		{"unknown scene field", `{"id":"e1","source":"s","type":"message.addressed","scene":{"id":"s1","name":"x"},"occurred_at":"2026-10-11T08:00:00Z"}`, false},
		{"non-string ref", `{"id":"e1","source":"s","type":"message.addressed","refs":{"task":7},"occurred_at":"2026-10-11T08:00:00Z"}`, false},
	} {
		goValid, schemaValid := judge(t, doc, c.doc)
		if goValid != c.valid || schemaValid != c.valid {
			t.Errorf("%s: Go valid=%v, schema valid=%v, want %v\n%s", c.name, goValid, schemaValid, c.valid, c.doc)
		}
	}
	// The edges the schema's description leaves to Validate, where the two
	// disagree.
	for _, c := range []struct {
		name  string
		doc   string
		valid bool // Validate's verdict
	}{
		{"null scene", `{"id":"e1","source":"s","type":"message.addressed","scene":null,"occurred_at":"2026-10-11T08:00:00Z"}`, true},
		{"null text", `{"id":"e1","source":"s","type":"message.addressed","text":null,"occurred_at":"2026-10-11T08:00:00Z"}`, true},
		{"zero time with an offset", `{"id":"e1","source":"s","type":"message.addressed","occurred_at":"0001-01-01T08:00:00+08:00"}`, false},
		{"malformed time", `{"id":"e1","source":"s","type":"message.addressed","occurred_at":"not a time"}`, false},
		{"U+0085 id", `{"id":"\u0085","source":"s","type":"message.addressed","occurred_at":"2026-10-11T08:00:00Z"}`, false},
	} {
		goValid, schemaValid := judge(t, doc, c.doc)
		if goValid != c.valid || schemaValid == c.valid {
			t.Errorf("%s: Go valid=%v, schema valid=%v, want %v and %v\n%s", c.name, goValid, schemaValid, c.valid, !c.valid, c.doc)
		}
	}
}

// judge returns the verdicts of Validate, on an event decoded with
// DisallowUnknownFields, and of the schema s on the JSON document raw.
func judge(t *testing.T, s map[string]any, raw string) (goValid, schemaValid bool) {
	t.Helper()
	var e event.Event
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	goValid = dec.Decode(&e) == nil && e.Validate() == nil
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return goValid, matches(t, s, v)
}

// matches evaluates the keywords event.schema.json uses and fails the test
// on any other, so a new keyword cannot be skipped silently.
func matches(t *testing.T, s map[string]any, v any) bool {
	t.Helper()
	obj, isObj := v.(map[string]any)
	str, isStr := v.(string)
	for key, val := range s {
		switch key {
		case "$schema", "$id", "title", "description", "format":
		case "type":
			if (val == "object" && !isObj) || (val == "string" && !isStr) {
				return false
			}
		case "required":
			for _, name := range val.([]any) {
				if _, ok := obj[name.(string)]; isObj && !ok {
					return false
				}
			}
		case "properties":
			for name, sub := range val.(map[string]any) {
				if pv, ok := obj[name]; isObj && ok && !matches(t, sub.(map[string]any), pv) {
					return false
				}
			}
		case "additionalProperties":
			props, _ := s["properties"].(map[string]any)
			for name, pv := range obj {
				if _, known := props[name]; known {
					continue
				}
				if sub, ok := val.(map[string]any); (ok && !matches(t, sub, pv)) || val == false {
					return false
				}
			}
		case "enum":
			found := false
			for _, option := range val.([]any) {
				found = found || reflect.DeepEqual(option, v)
			}
			if !found {
				return false
			}
		case "const":
			if !reflect.DeepEqual(val, v) {
				return false
			}
		case "not":
			if matches(t, val.(map[string]any), v) {
				return false
			}
		case "pattern":
			if isStr && !regexp.MustCompile(val.(string)).MatchString(str) {
				return false
			}
		default:
			t.Fatalf("event.schema.json: keyword %q is not evaluated by this test", key)
		}
	}
	return true
}
