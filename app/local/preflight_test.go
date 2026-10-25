package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckProfileIsPureAndPrintsOnlyProvenance(t *testing.T) {
	bundle := syntheticBundle()
	raw, _ := json.Marshal(bundle)
	path := filepath.Join(t.TempDir(), "private compiled.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "must not be opened")
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{"-profile", example("profile.json"), "-prompt-bundle", path, "-dir", state, "-script", "/must-not-open-model-rules.json", "-check-profile"}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("configuration preflight initialized state", err)
	}
	var record struct {
		Type           string         `json:"type"`
		ProfileSHA     string         `json:"profile_sha256"`
		RuntimeChecked bool           `json:"runtime_checked"`
		Source         sourceManifest `json:"source_manifest"`
	}
	if json.Unmarshal(out.Bytes(), &record) != nil || record.Type != "profile_preflight" || len(record.ProfileSHA) != 64 || record.RuntimeChecked || record.Source.Mode != "dev" || record.Source.AcceptanceEligible || len(record.Source.BinarySHA256) != 64 {
		t.Fatalf("wrong provenance: %s", out.String())
	}
	for _, private := range []string{"SYNTHETIC_PRIVATE_LITERAL", "local-owner", "local-workspace", "Preserve the human", path} {
		if strings.Contains(out.String()+errOut.String(), private) {
			t.Fatal("preflight disclosed private configuration")
		}
	}
}
func TestCheckProfileRejectsMissingOrInvalidArtifactBeforeState(t *testing.T) {
	for _, kind := range []string{"absent", "missing_manifest", "checksum", "unresolved", "plugin", "command", "mode"} {
		t.Run(kind, func(t *testing.T) {
			b := syntheticBundle()
			switch kind {
			case "missing_manifest":
				b.SourceManifestSHA256 = ""
				b.ContentSHA256 = b.ContentDigest()
			case "checksum":
				b.Roles["chat"] = "changed"
			case "unresolved":
				b.UnresolvedBindings = []string{"private missing binding"}
				b.ContentSHA256 = b.ContentDigest()
			}
			root := t.TempDir()
			bundlePath := filepath.Join(root, "compiled.json")
			raw, _ := json.Marshal(b)
			if err := os.WriteFile(bundlePath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			profilePath := example("profile.json")
			if kind == "plugin" {
				var p map[string]any
				raw, err := os.ReadFile(profilePath)
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal(raw, &p)
				p["Plugins"] = []map[string]string{{"Name": "unsupported", "Version": "v1"}}
				raw, _ = json.Marshal(p)
				profilePath = filepath.Join(root, "profile.json")
				if err = os.WriteFile(profilePath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			state := filepath.Join(root, "state")
			args := []string{"-profile", profilePath, "-dir", state, "-check-profile"}
			if kind != "absent" {
				args = append(args, "-prompt-bundle", bundlePath)
			}
			if kind == "command" {
				args = append(args, "serve")
			}
			if kind == "mode" {
				args = append(args, "-source-mode", "pretend-clean")
			}
			var out, errOut bytes.Buffer
			if err := run(context.Background(), args, strings.NewReader(""), &out, &errOut); err == nil {
				t.Fatal("invalid pure preflight succeeded")
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("failed preflight touched state", err)
			}
			if strings.Contains(out.String()+errOut.String(), "PRIVATE_LITERAL") {
				t.Fatal("failed preflight printed prompt body")
			}
		})
	}
}
func TestAcceptanceSourceSelectionRequiresActualExactCleanBuild(t *testing.T) {
	revision := strings.Repeat("a", 40)
	clean, dirty := false, true
	for _, test := range []struct {
		mode, expected, actual string
		modified               *bool
		valid                  bool
	}{{"acceptance", revision, revision, &clean, true}, {"acceptance", revision, revision, &dirty, false}, {"acceptance", revision, revision, nil, false}, {"acceptance", "", revision, &clean, false}, {"acceptance", strings.Repeat("b", 40), revision, &clean, false}, {"acceptance", "HEAD", revision, &clean, false}, {"dev", "", revision, &dirty, true}, {"dev", "", "", nil, true}, {"pretend", "", revision, &clean, false}} {
		if err := checkSourceSelection(test.mode, test.expected, test.actual, test.modified); (err == nil) != test.valid {
			t.Fatalf("wrong source admission for %+v: %v", test, err)
		}
	}
}

func TestPurePreflightRejectsAllUnregisteredLocalModulesBeforeAssembly(t *testing.T) {
	for _, module := range []string{"storage", "context", "executor", "review", "scheduler", "owner", "TaskManager"} {
		t.Run(module, func(t *testing.T) {
			var profile entry.Profile
			if err := readJSON(example("profile.json"), &profile); err != nil {
				t.Fatal(err)
			}
			ref := entry.ModuleRef{Name: "external-provider", Version: "fixed-v1"}
			bundle := syntheticBundle()
			switch module {
			case "storage":
				profile.Modules.Storage = ref
			case "context":
				profile.Modules.Context = ref
			case "executor":
				profile.Modules.Executor = ref
			case "review":
				profile.Modules.Review = ref
			case "scheduler":
				profile.Modules.Scheduler = ref
			case "owner":
				profile.Modules.Owner = ref
			case "TaskManager":
				profile.Modules.TaskManager = &ref
				bundle.Roles = map[string]string{"chat": "SYNTHETIC_PRIVATE_LITERAL_chat"}
				bundle.ContentSHA256 = bundle.ContentDigest()
			}
			root := t.TempDir()
			profilePath := filepath.Join(root, "profile.json")
			bundlePath := filepath.Join(root, "compiled.json")
			state := filepath.Join(root, "not-opened")
			for path, value := range map[string]any{profilePath: profile, bundlePath: bundle} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			err := run(context.Background(), []string{"-profile", profilePath, "-prompt-bundle", bundlePath, "-dir", state, "-check-profile"}, strings.NewReader(""), &out, &errOut)
			if err == nil {
				t.Fatal("unregistered module passed pure preflight")
			}
			if module == "storage" || module == "context" || module == "scheduler" || module == "TaskManager" {
				if !strings.Contains(err.Error(), "local module is not registered") {
					t.Fatal("valid generic profile did not reach the actual local registry check", err)
				}
			}
			if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
				t.Fatal("rejected module opened state", statErr)
			}
			if out.Len() != 0 || strings.Contains(errOut.String(), "PRIVATE_LITERAL") {
				t.Fatal("rejected module emitted a manifest or private body")
			}
		})
	}
}
