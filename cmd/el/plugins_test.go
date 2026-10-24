package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/entry"
)

func TestCompiledPluginDiscoveryNeedsNoProfileStateOrModel(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"-list-plugins"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var catalog []compiledPlugin
	if err := json.Unmarshal(out.Bytes(), &catalog); err != nil || len(catalog) != 1 || catalog[0].Name != "audit" || catalog[0].Version != "v1" {
		t.Fatal(out.String(), err)
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
}

func TestCLIProfileRunsCompiledAuditPluginInChatAndOwner(t *testing.T) {
	for _, version := range []string{"v1", "unknown"} {
		t.Run(version, func(t *testing.T) {
			var profile entry.Profile
			if err := readJSON(example("profile.json"), &profile); err != nil {
				t.Fatal(err)
			}
			profile.Plugins = []entry.PluginRef{{Name: "audit", Version: version}}
			profile.OwnerLoops = []string{"memory"}
			dir := t.TempDir()
			profilePath, scriptPath := filepath.Join(dir, "profile.json"), filepath.Join(dir, "script.json")
			script := ruleScript{Version: "local-el-script/1", ModelName: profile.ModelName, Rules: []modelRule{
				{Role: "chat", ToolCalls: []scriptCall{{ID: "quiet", Name: "stay_quiet", Arguments: map[string]any{"source_ref": "audit-source"}}}},
				{Role: "owner_memory", ToolCalls: []scriptCall{{ID: "commit", Name: "commit_owner_update", Arguments: map[string]any{
					"body": "MODEL_OUTPUT_SECRET", "citations": []any{map[string]any{"source_id": "audit-source", "quote": "concise"}},
				}}}},
			}}
			for path, value := range map[string]any{profilePath: profile, scriptPath: script} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			args := []string{"-profile", profilePath, "-dir", filepath.Join(dir, "state"), "-script", scriptPath, "submit", "-id", "audit-source", "-text", "Be concise PRIVATE_INPUT_SECRET sk-test-secret"}
			err := run(context.Background(), args, strings.NewReader(""), &out, &errOut)
			if version != "v1" {
				if err == nil || !strings.Contains(err.Error(), "audit@unknown is not registered") {
					t.Fatal(err)
				}
				if errOut.Len() != 0 {
					t.Fatal("unknown plugin executed", errOut.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err, errOut.String())
			}
			for _, secret := range []string{"MODEL_OUTPUT_SECRET", "PRIVATE_INPUT_SECRET", "sk-test-secret", "audit-source", profile.Owner, profile.Workspace} {
				if strings.Contains(errOut.String(), secret) {
					t.Fatal("audit leaked private content", secret)
				}
			}
			roles := map[string]bool{}
			decoder := json.NewDecoder(&errOut)
			for decoder.More() {
				var event struct{ Type, Plugin, Version, Role, Event string }
				if err = decoder.Decode(&event); err != nil {
					t.Fatal(err)
				}
				if event.Type != "plugin_audit" || event.Plugin != "audit" || event.Version != "v1" {
					t.Fatal(event)
				}
				roles[event.Role] = true
			}
			if !roles["chat"] || !roles["owner_memory"] {
				t.Fatal("compiled factory missed runtime", roles)
			}
			state := readState(t, out.String())
			if len(state.Tasks) != 0 || len(state.Deliveries) != 0 {
				t.Fatal(state)
			}
		})
	}
}
