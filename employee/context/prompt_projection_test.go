package context_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	cb "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func TestBundleTaskProjectionPrecedesFrameBudgetAndPreservesOriginals(t *testing.T) {
	b, _, tasks, scope, _ := fixture(t)
	ctx := context.Background()
	// These field names are ordinary human data and must not be recursively
	// deleted just because a runtime Profile has fields with the same names.
	human := `Preserve this JSON exactly: {"PromptBundle":{"roles":{"example":"human data"}}}`
	if _, err := b.Admit(ctx, scope, cb.Source{ID: "source", Actor: scope.Requester, Text: human}); err != nil {
		t.Fatal(err)
	}
	p := contract.Profile{Version: "local-el-profile/1", ID: "synthetic", Revision: "1", PromptBundle: &contract.PromptBundle{Contract: contract.NativePromptContract, SourceRevision: "synthetic/1", SourceManifestSHA256: contract.Digest([]byte("sources")), CompilerVersion: "compiler/1", Roles: map[string]string{"greeting": strings.Repeat("PRIVATE_OTHER_ROLE ", 10000)}}}
	p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
	config, _ := json.Marshal(p)
	receipt, err := tasks.Dispatch(ctx, scope, work.Source{Receipt: "source", Actor: scope.Requester, Text: human}, "dispatch", work.Definition{Configuration: config, Goal: human, Instruction: human, MaxRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := b.Build(ctx, scope, cb.Execution, "", receipt.TaskID)
	if err != nil {
		t.Fatal("unrelated role inflated mandatory execution budget", err)
	}
	if len(frame.Tasks) != 1 || !bytes.Contains(frame.Tasks[0].Definition.Configuration, []byte(`"redacted":true`)) {
		t.Fatal("returned model frame retained private role configuration")
	}
	if frame.Tasks[0].Definition.Instruction != human || len(frame.Sources) != 1 || frame.Sources[0].Text != human {
		t.Fatal("typed projection changed human business data")
	}
	original, err := tasks.Get(ctx, scope, receipt.TaskID)
	if err != nil || !bytes.Equal(original.Definition.Configuration, config) {
		t.Fatal("projection changed persisted/SDK Task configuration", err)
	}
	// Delivery reattaches a historical original Task after Build. Historical
	// frames and custom typed frames share this final serialization boundary.
	frame.Tasks = []work.Task{original}
	raw, err := json.Marshal(frame)
	if err != nil || len(raw) > cb.DefaultBudget().MaxBytes || bytes.Contains(raw, []byte("PRIVATE_OTHER_ROLE")) {
		t.Fatal("final frame serialization leaked bundle or exceeded budget", err)
	}
	if !bytes.Contains(original.Definition.Configuration, []byte("PRIVATE_OTHER_ROLE")) {
		t.Fatal("serializer mutated caller's Task")
	}
}

func TestLegacyFrameAndTaskConfigurationBytesRemainUnchanged(t *testing.T) {
	config := json.RawMessage(`{ "Instructions" : "legacy runtime data", "custom": {"PromptBundle":"human nested data"} }`)
	task := work.Task{ID: "old", Definition: work.Definition{Configuration: config, Instruction: "original"}}
	frame := cb.Frame{View: cb.Execution, Tasks: []work.Task{task}, Gaps: []string{}}
	type oldFrame cb.Frame // Same historical fields, without the new serializer.
	want, err := json.Marshal(oldFrame(frame))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(frame)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("legacy frame bytes changed", err)
	}
	projected, err := work.ModelTask(task)
	if err != nil || !bytes.Equal(projected.Definition.Configuration, config) {
		t.Fatal("legacy raw configuration changed", err)
	}
}

func TestCustomConfigurationWithPromptBundleFieldIsNotANativeProfile(t *testing.T) {
	for _, raw := range []string{
		`{ "PromptBundle": {"roles":"human business data"}, "ID": 42 }`,
		`{ "Version":"custom-backend/1", "PromptBundle":"business data", "ID": 42 }`,
	} {
		task := work.Task{ID: "custom", Definition: work.Definition{Configuration: json.RawMessage(raw)}}
		projected, err := work.ModelTask(task)
		if err != nil || string(projected.Definition.Configuration) != raw {
			t.Fatal("custom backend configuration was treated as native prompt configuration", err)
		}
		frame := cb.Frame{View: cb.Execution, Tasks: []work.Task{task}}
		type customFrame cb.Frame
		want, _ := json.Marshal(customFrame(frame))
		got, err := json.Marshal(frame)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("custom backend Frame bytes changed", err)
		}
	}
	invalid := work.Task{Definition: work.Definition{Configuration: json.RawMessage(`{"Version":"local-el-profile/1","PromptBundle":"invalid native bundle"}`)}}
	if _, err := work.ModelTask(invalid); err == nil {
		t.Fatal("invalid explicit native bundle was exposed as raw configuration")
	}
	if _, err := json.Marshal(cb.Frame{Tasks: []work.Task{invalid}}); err == nil {
		t.Fatal("invalid native bundle reached model-frame serialization")
	}
}
