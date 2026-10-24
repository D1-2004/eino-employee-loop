package contract

import "github.com/D1-2004/eino-employee-loop/employee/internal/jsoncheck"

// ValidateJSON checks complete JSON, duplicate fields and bounded nesting.
func ValidateJSON(raw []byte, maxDepth int) error { return jsoncheck.Unique(raw, maxDepth) }
