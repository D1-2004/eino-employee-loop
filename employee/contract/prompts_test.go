package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func promptProfile() Profile {
	b := &PromptBundle{Contract: NativePromptContract, SourceRevision: "synthetic-source/1", SourceManifestSHA256: Digest([]byte("synthetic sources")), CompilerVersion: "synthetic-compiler/1", Roles: map[string]string{}}
	for _, role := range []string{"chat", "executor", "review", "task_attention", "greeting", "routine", "owner_memory", "expression"} {
		b.Roles[role] = "SYNTHETIC " + role + `; literal JSON is valid: {"answer":"ok"}`
	}
	b.ContentSHA256 = b.ContentDigest()
	return Profile{Backend: "standalone", PromptBundle: b, Instructions: "LEGACY_MUST_NOT_APPEAR", OwnerLoops: []string{"memory"}, DeliveryLoops: []string{"expression"}}
}

func TestPromptBundleRejectsDriftMissingRolesAndUnsupportedConsumers(t *testing.T) {
	for _, failure := range []string{"content", "provenance", "checksum", "missing", "unknown-role", "owner-path", "unresolved", "contract", "backend", "executor", "review", "owner"} {
		t.Run(failure, func(t *testing.T) {
			p := promptProfile()
			switch failure {
			case "content":
				p.PromptBundle.Roles["chat"] += " changed"
			case "provenance":
				p.PromptBundle.SourceRevision = "changed"
			case "checksum":
				p.PromptBundle.ContentSHA256 = strings.Repeat("A", 64)
			case "missing":
				delete(p.PromptBundle.Roles, "owner_memory")
				p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
			case "unknown-role":
				p.PromptBundle.Roles["foreign/actor"] = "unsupported"
				p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
			case "owner-path":
				p.PromptBundle.Roles["owner_../../escape"] = "unsupported"
				p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
			case "unresolved":
				p.PromptBundle.UnresolvedBindings = []string{"unsupported_source_binding"}
				p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
			case "contract":
				p.PromptBundle.Contract = "foreign-tools/1"
				p.PromptBundle.ContentSHA256 = p.PromptBundle.ContentDigest()
			case "backend":
				p.Backend = "replacement"
			case "executor":
				p.Modules.Executor = ModuleRef{Name: "replacement", Version: "1"}
			case "review":
				p.Modules.Review = ModuleRef{Name: "replacement", Version: "1"}
			case "owner":
				p.Modules.Owner = ModuleRef{Name: "replacement", Version: "1"}
			}
			if _, err := ResolveInstructions(p, "chat"); err == nil {
				t.Fatal("invalid bundle silently adopted or fell back")
			}
		})
	}
}

func TestPromptFreezePreservesLiteralTextAndLegacyWireIdentity(t *testing.T) {
	p := promptProfile()
	selected := p.Snapshot()
	want := p.PromptBundle.Roles["chat"]
	p.PromptBundle.Roles["chat"] = "caller mutation"
	p.PromptBundle.UnresolvedBindings = []string{"caller mutation"}
	got, err := ResolveInstructions(selected, "chat")
	if err != nil || got != want || strings.Contains(got, "LEGACY") {
		t.Fatal("literal braces rejected or caller mutation changed frozen prompt", err)
	}
	if _, err = ResolveInstructions(selected, "unmapped"); err == nil {
		t.Fatal("missing invoked role used fallback")
	}
	legacy := Profile{Instructions: "legacy exact\n"}
	raw, _ := json.Marshal(legacy)
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, found := fields["PromptBundle"]; found {
		t.Fatal("absent bundle changed legacy wire identity")
	}
	if got, err = ResolveInstructions(legacy, "extension-custom-role"); err != nil || got != legacy.Instructions {
		t.Fatal("legacy behavior changed", err)
	}
}
