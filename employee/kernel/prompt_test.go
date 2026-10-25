package kernel_test

import (
	"context"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/kernel"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestPromptValidationPrecedesModelAndLegacyMessageIsUnchanged(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := looptest.New(looptest.Func(func(in []*schema.Message) looptest.Step {
		if len(in) != 2 || in[0].Content != "NATIVE_PROTOCOL\nLEGACY_EXACT\n" {
			t.Fatal("legacy system message changed")
		}
		return looptest.Text("ok")
	}))
	factories := 0
	r := &kernel.Runner{State: db, Owner: "process", Authorize: func(context.Context, contract.Scope) error { return nil }, Models: func(context.Context, contract.ModelRequest) (model.ToolCallingChatModel, error) {
		factories++
		return m, nil
	}}
	p := contract.Profile{Backend: "standalone", ID: "test", Revision: "1", ModelName: "scripted", MaxModelCalls: 4, Instructions: "LEGACY_EXACT\n", PromptBundle: &contract.PromptBundle{Contract: contract.NativePromptContract}}
	call := kernel.Invocation{Profile: p, Scope: contract.Scope{Workspace: "w", Tenant: "t", Employee: "e", Scene: "dm", Requester: "o"}, Role: "chat", Receipt: "original", System: "NATIVE_PROTOCOL", Context: map[string]any{}, AllowText: true}
	if _, err = r.Run(ctx, call); err == nil || factories != 0 || m.Calls() != 0 {
		t.Fatal("invalid prompt invoked model before rejection", err)
	}
	call.Profile.PromptBundle = nil
	if _, err = r.Run(ctx, call); err != nil || factories != 1 || m.Calls() != 1 {
		t.Fatal("legacy native request failed", err)
	}
}
