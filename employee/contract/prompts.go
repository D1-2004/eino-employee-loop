package contract

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// NativePromptContract describes literal fragments for the builtin native roles.
// Source templates and their dynamic bindings are compiled by the Host, outside
// Employee. These fragments cannot replace native tools or grant permissions.
const NativePromptContract = "employee-native-roles/1"

type PromptBundle struct {
	Contract             string            `json:"contract"`
	SourceRevision       string            `json:"sourceRevision"`
	SourceManifestSHA256 string            `json:"sourceManifestSHA256"`
	CompilerVersion      string            `json:"compilerVersion"`
	ContentSHA256        string            `json:"contentSHA256,omitempty"`
	Roles                map[string]string `json:"roles"`
	// A compiler must explicitly report any source bindings it could not resolve.
	// Literal JSON examples/braces in role text are valid and are never scanned.
	UnresolvedBindings []string `json:"unresolvedBindings,omitempty"`
}

// ContentDigest hashes canonical Go JSON of the whole artifact with only its
// own checksum omitted. This binds both literal text and provenance metadata.
func (b PromptBundle) ContentDigest() string {
	b.ContentSHA256 = ""
	raw, _ := json.Marshal(b)
	return Digest(raw)
}

func promptSHA(s string) bool {
	decoded, err := hex.DecodeString(s)
	return err == nil && len(decoded) == 32 && s == strings.ToLower(s)
}

func (b PromptBundle) Validate() error {
	if b.Contract != NativePromptContract {
		return errors.New("contract: unsupported native prompt contract")
	}
	for _, value := range []string{b.SourceRevision, b.CompilerVersion} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
			return errors.New("contract: exact prompt source revision and compiler version required")
		}
	}
	if !promptSHA(b.SourceManifestSHA256) || !promptSHA(b.ContentSHA256) || b.ContentDigest() != b.ContentSHA256 {
		return errors.New("contract: prompt source/content SHA256 mismatch")
	}
	if len(b.UnresolvedBindings) != 0 {
		return errors.New("contract: unresolved prompt source bindings")
	}
	if len(b.Roles) == 0 || len(b.Roles) > 64 {
		return errors.New("contract: bounded prompt roles required")
	}
	total := 0
	for role, text := range b.Roles {
		if !nativePromptRole(role) || strings.TrimSpace(text) == "" || len(text) > 256*1024 {
			return fmt.Errorf("contract: invalid native prompt role %q", role)
		}
		total += len(text)
	}
	if total > 2*1024*1024 {
		return errors.New("contract: prompt bundle exceeds 2 MiB")
	}
	return nil
}

func nativePromptRole(role string) bool {
	switch role {
	case "chat", "executor", "review", "task_attention", "greeting", "routine", "expression", "package":
		return true
	}
	if !strings.HasPrefix(role, "owner_") {
		return false
	}
	// Owner role names also select persisted projection paths. Keep compiled
	// custom policies to a single bounded path component, never a source path.
	name := strings.TrimPrefix(role, "owner_")
	if name == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ValidatePrompts is also used before actual model I/O when a frozen Profile is
// recovered. Absence preserves legacy prompt bytes; an explicit bundle never
// falls back to the global Instructions string.
func (p Profile) ValidatePrompts() error {
	if p.PromptBundle == nil {
		return nil
	}
	if err := p.PromptBundle.Validate(); err != nil {
		return err
	}
	if p.Backend != "standalone" {
		return errors.New("contract: prompt bundle requires the standalone native backend")
	}
	for _, ref := range []ModuleRef{p.Modules.Executor, p.Modules.Review, p.Modules.Owner} {
		if ref.Effective() != (ModuleRef{Name: "builtin", Version: "1"}) {
			return errors.New("contract: prompt bundle is unsupported by replacement executor/review/owner modules")
		}
	}
	roles := []string{"chat", "executor", "review", "task_attention", "greeting", "routine"}
	roles = append(roles, p.DeliveryLoops...)
	for _, role := range p.OwnerLoops {
		roles = append(roles, "owner_"+role)
	}
	for _, role := range roles {
		if _, found := p.PromptBundle.Roles[role]; !found {
			return fmt.Errorf("contract: missing prompt for enabled role %q", role)
		}
	}
	return nil
}

// ResolveInstructions resolves only immutable Profile data, before constructing
// the native request/journal. Native protocol and context remain separate.
func ResolveInstructions(p Profile, role string) (string, error) {
	if p.PromptBundle == nil {
		return p.Instructions, nil
	}
	if err := p.ValidatePrompts(); err != nil {
		return "", err
	}
	text, found := p.PromptBundle.Roles[role]
	if !found {
		return "", fmt.Errorf("contract: missing prompt for native role %q", role)
	}
	return text, nil
}
