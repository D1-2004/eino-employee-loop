package executors_test

import (
	"testing"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
)

// Every shipped locale of this package's pack defines the same keys.
func TestPackLocalesHaveSameKeys(t *testing.T) {
	b := i18n.New(i18n.Fallback)
	if err := executors.LoadMessages(b); err != nil {
		t.Fatal(err)
	}
	if len(b.Locales()) < 2 {
		t.Fatalf("expected at least two locales, got %v", b.Locales())
	}
	if gaps := b.Parity(); len(gaps) > 0 {
		t.Fatalf("pack locales differ: %v", gaps)
	}
}
