package contract

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTaskManagerSelectionPreservesLegacyWireAndDetachedBinding(t *testing.T) {
	legacy := ModuleSelection{}
	old := struct{ Storage, Context, Executor, Review, Scheduler, Owner ModuleRef }{}
	want, _ := json.Marshal(old)
	got, _ := json.Marshal(legacy)
	if !bytes.Equal(got, want) {
		t.Fatal("legacy module JSON changed")
	}
	before, _ := json.Marshal(legacy.Core())
	ref := ModuleRef{Name: "builtin", Version: "1"}
	explicit := ModuleSelection{TaskManager: &ref}
	normalized, _ := json.Marshal(explicit.Core())
	if !bytes.Equal(before, normalized) {
		t.Fatal("default module binding changed")
	}
	ref = ModuleRef{Name: "host-tasks", Version: "1"}
	p := Profile{Modules: ModuleSelection{TaskManager: &ref}}
	frozen := p.Snapshot()
	ref.Version = "2"
	if frozen.Modules.TaskManagerRef().Version != "1" {
		t.Fatal("accepted provider aliases caller's selection")
	}
	raw, _ := json.Marshal(frozen)
	var restored Profile
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Modules.TaskManagerRef() != frozen.Modules.TaskManagerRef() {
		t.Fatal("exact provider not retained", err)
	}
}
