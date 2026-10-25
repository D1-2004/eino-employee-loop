package state

import (
	"bytes"
	"encoding/json"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
)

// ModelTask projects only private runtime configuration out of a model-facing
// Task copy. The Manager, execution request and SDK Task APIs retain their exact
// accepted configuration. Human instructions, sources and outputs are untouched.
func ModelTask(task Task) (Task, error) {
	raw := task.Definition.Configuration
	if len(raw) == 0 {
		return task, nil
	}
	var marker struct {
		Version      json.RawMessage
		PromptBundle json.RawMessage
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		// Non-Profile JSON is an existing custom-backend contract, not something
		// this projection may recursively inspect or rewrite.
		if json.Valid(raw) {
			return task, nil
		}
		return Task{}, err
	}
	var version string
	if json.Unmarshal(marker.Version, &version) != nil || version != "local-el-profile/1" {
		return task, nil
	}
	if len(marker.PromptBundle) == 0 || bytes.Equal(bytes.TrimSpace(marker.PromptBundle), []byte("null")) {
		return task, nil
	}
	var p contract.Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return Task{}, err
	}
	b := p.PromptBundle
	provenance := struct {
		Contract, SourceRevision, SourceManifestSHA256, CompilerVersion, ContentSHA256 string
	}{b.Contract, b.SourceRevision, b.SourceManifestSHA256, b.CompilerVersion, b.ContentSHA256}
	// promptProvenance is intentionally distinct from the executable Profile's
	// PromptBundle field. This view is explicit and idempotent, never a partial
	// Profile that could be mistaken for runnable frozen configuration.
	projection := map[string]any{"redacted": true, "profileID": p.ID, "profileRevision": p.Revision, "promptProvenance": provenance}
	config, err := json.Marshal(projection)
	if err != nil {
		return Task{}, err
	}
	task.Definition.Configuration = config
	return task, nil
}

func ModelTasks(tasks []Task) ([]Task, error) {
	if tasks == nil {
		return nil, nil
	}
	out := make([]Task, len(tasks))
	for i, task := range tasks {
		projected, err := ModelTask(task)
		if err != nil {
			return nil, err
		}
		out[i] = projected
	}
	return out, nil
}
