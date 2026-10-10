package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NormHash normalizes whitespace/case and returns a short stable hex digest (idempotency keys).
func NormHash(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
