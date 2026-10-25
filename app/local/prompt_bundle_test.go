package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
)

func syntheticBundle() contract.PromptBundle {
	b := contract.PromptBundle{Contract: contract.NativePromptContract, SourceRevision: "synthetic-source/1", SourceManifestSHA256: contract.Digest([]byte("synthetic sources")), CompilerVersion: "synthetic-compiler/1", Roles: map[string]string{}}
	for _, role := range []string{"chat", "executor", "review", "task_attention", "greeting", "routine"} {
		b.Roles[role] = "SYNTHETIC_PRIVATE_LITERAL_" + role
	}
	b.ContentSHA256 = b.ContentDigest()
	return b
}

func TestExternalPromptCLIReportsAndPersistsSameFrozenArtifact(t *testing.T) {
	b := syntheticBundle()
	path := filepath.Join(t.TempDir(), "external.json")
	write := func() {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	dir := t.TempDir()
	invoke := func(command ...string) (string, error) {
		args := []string{"-profile", example("profile.json"), "-prompt-bundle", path, "-dir", dir, "-script", example("script.json")}
		var out, errOut bytes.Buffer
		err := run(context.Background(), append(args, command...), strings.NewReader(""), &out, &errOut)
		return out.String(), err
	}
	out, err := invoke("replay", "-in", example("input.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, b.ContentSHA256) || strings.Contains(out, "SYNTHETIC_PRIVATE_LITERAL") {
		t.Fatal("CLI omitted artifact digest or leaked literal instructions")
	}
	state := readState(t, out)
	if len(state.Tasks) != 1 {
		t.Fatal("missing real Task")
	}
	if !strings.Contains(string(state.Tasks[0].Definition.Configuration), `"redacted":true`) {
		t.Fatal("CLI snapshot claimed to contain a complete private configuration")
	}
	var profile contract.Profile
	if err = readJSON(example("profile.json"), &profile); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := db.Get(context.Background(), "profiles", profile.Key(), profile.ID+"/"+profile.Revision)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var frozen contract.Profile
	if err != nil || json.Unmarshal(doc.Body, &frozen) != nil || frozen.PromptBundle == nil || frozen.PromptBundle.ContentSHA256 != b.ContentSHA256 || frozen.PromptBundle.Roles["chat"] != b.Roles["chat"] {
		t.Fatal("reported and admitted prompt artifacts differ", err)
	}
	b.Roles["chat"] = "changed under same profile revision"
	b.ContentSHA256 = b.ContentDigest()
	write()
	if _, err = invoke("status"); err == nil || !strings.Contains(err.Error(), "same revision") {
		t.Fatal("same revision silently rebound to a changed file", err)
	}
}

func TestExternalPromptLoadFailsClosedBeforeStateCreation(t *testing.T) {
	for _, failure := range []string{"missing-file", "checksum", "missing-role", "unresolved", "unknown-field"} {
		t.Run(failure, func(t *testing.T) {
			b := syntheticBundle()
			path := filepath.Join(t.TempDir(), "external.json")
			switch failure {
			case "checksum":
				b.Roles["chat"] = "drift"
			case "missing-role":
				delete(b.Roles, "routine")
				b.ContentSHA256 = b.ContentDigest()
			case "unresolved":
				b.UnresolvedBindings = []string{"unresolved_source"}
				b.ContentSHA256 = b.ContentDigest()
			}
			raw, _ := json.Marshal(b)
			if failure == "unknown-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"templateURL":"unsupported"}`)...)
			}
			if failure != "missing-file" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(t.TempDir(), "not-opened")
			var out, errOut bytes.Buffer
			err := run(context.Background(), []string{"-profile", example("profile.json"), "-prompt-bundle", path, "-dir", dir, "status"}, strings.NewReader(""), &out, &errOut)
			if err == nil {
				t.Fatal("invalid external artifact accepted")
			}
			if _, err = os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid artifact reached storage assembly", err)
			}
		})
	}
	p := contract.Profile{PromptBundle: &contract.PromptBundle{}}
	if err := loadPromptBundle("unused-file", &p); err == nil {
		t.Fatal("CLI overrode profile's explicit bundle")
	}
}
