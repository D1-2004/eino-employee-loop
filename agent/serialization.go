package agent

import "github.com/cloudwego/eino/schema"

// JSON-decoded model metadata and ADK session state can contain nested dynamic
// objects. Register those concrete interface payloads before any checkpoint;
// process rebuilds must use the same stable wire names.
func init() {
	schema.Register[map[string]any]()
	schema.Register[[]any]()
}
