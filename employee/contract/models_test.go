package contract

import (
	"encoding/json"
	"testing"
)

func TestModelSelectionSnapshotsAndLegacyIdentity(t *testing.T) {
	binding := ModelBinding{Provider: "provider", Version: "2", Model: "reasoner", Effort: "high"}
	p := Profile{ModelName: "legacy", ModelBindings: map[string]ModelBinding{"review": binding}, Scenes: []string{"scene"}}
	request, err := ResolveModelRequest(p, "review", "receipt", Scope{})
	if err != nil || request.Binding != binding || request.ModelName != "reasoner" {
		t.Fatal(request, err)
	}
	p.ModelBindings["review"] = ModelBinding{Provider: "changed"}
	p.Scenes[0] = "changed"
	if request.Profile.ModelBindings["review"] != binding || request.Profile.Scenes[0] != "scene" {
		t.Fatal("request profile aliases caller buffers")
	}
	legacy, err := ResolveModelRequest(p, "chat", "receipt", Scope{})
	if err != nil || legacy.Binding.Identity() != "legacy" {
		t.Fatal(legacy, err)
	}
	old := `{"Version":"local-el-profile/1","ModelName":"old"}`
	var restored Profile
	if err = json.Unmarshal([]byte(old), &restored); err != nil {
		t.Fatal(err)
	}
	req, err := ResolveModelRequest(restored, "chat", "r", Scope{})
	if err != nil || req.ModelName != "old" || req.Binding.Identity() != "old" {
		t.Fatal(req, err)
	}
	raw, _ := json.Marshal(restored)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if _, ok := fields["ModelBindings"]; ok {
		t.Fatal("legacy serialized profile changed by empty binding")
	}
	changed := binding
	changed.Effort = "low"
	if changed.Identity() == binding.Identity() {
		t.Fatal("effort missing from model identity")
	}
}
