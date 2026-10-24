package defaults_test

import (
	"context"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/defaults"
	"github.com/D1-2004/eino-employee-loop/employee/entry"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
)

// A consuming server embeds Entry without importing a main package. Its
// persistent admission survives a new assembly; replay must not call a model.
func TestServerHostEmbedsDefaultsAndReopensAdmission(t *testing.T) {
	ctx := context.Background()
	p := entry.Profile{Version: "local-el-profile/1", ID: "host", Revision: "1", Workspace: "w", Tenant: "t", Employee: "e", Owner: "o", Backend: "standalone", ModelName: "scripted", Scenes: []string{"dm"}, MaxModelCalls: 8, MaxRuns: 2}
	m := looptest.New(looptest.Tools(looptest.Call{Name: "stay_quiet", ID: "quiet", Args: map[string]any{"source_ref": "original"}}))
	opts := entry.Options{Directory: t.TempDir(), Profile: p, Models: func(context.Context, entry.ModelRequest) (model.ToolCallingChatModel, error) { return m, nil }}
	input := entry.Input{ID: "original", Actor: "o", Scene: "dm", Text: "Accepted host input", Origin: "host-test"}
	for round := 0; round < 2; round++ {
		app, err := entry.Open(ctx, defaults.Configure(opts))
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := app.Submit(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if admitted.Replay != (round == 1) {
			t.Fatalf("round %d admission: %+v", round, admitted)
		}
		if err = app.Drain(ctx, 20); err != nil {
			t.Fatal(err)
		}
		if err = app.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if m.Calls() != 1 {
		t.Fatalf("replay called model: %d", m.Calls())
	}
}
