package schema_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/event"
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
