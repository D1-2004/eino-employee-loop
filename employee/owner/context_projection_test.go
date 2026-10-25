package owner_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/decision/looptest"
	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestCurrentIsCitableOnceAndRefsJoinByIdentity(t *testing.T) {
	ctx := context.Background()
	f := openFixture(t, t.TempDir())
	defer f.close(t)
	f.admit(t, "old-a", "older original A")
	f.admit(t, "old-b", "older original B")
	const current = "CURRENT-EXACT-CITABLE-MARKER"
	f.admit(t, "current", current)
	opts := f.opts(func(_ context.Context, r ownerloop.Request) (model.ToolCallingChatModel, error) {
		return looptest.New(looptest.Func(func(messages []*schema.Message) looptest.Step {
			raw := messages[1].Content
			if strings.Count(raw, current) != 1 {
				t.Fatalf("current original repeated or absent: %s", raw)
			}
			var input struct {
				CurrentSourceID string `json:"current_source_id"`
				Sources         []struct {
					CitationID string
					Source     contextbuilder.Source
					Ref        contextbuilder.Ref
				}
			}
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Fatal(err)
			}
			if input.CurrentSourceID != "current" || len(input.Sources) != 3 {
				t.Fatalf("lost current citation: %+v", input)
			}
			for _, source := range input.Sources {
				if source.Source.ID != source.Ref.ID || source.CitationID != source.Source.ID {
					t.Fatalf("source joined wrong ref: %+v", source)
				}
			}
			return output(r.Role, "current", current)
		})), nil
	})
	opts.Roles = []ownerloop.RoleConfig{{Name: "memory", MaxModelCalls: 3}}
	worker, err := ownerloop.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Observe(ctx, f.scope, "current"); err != nil {
		t.Fatal(err)
	}
	drain(t, worker, f.scope)
	docs, err := worker.Documents(ctx, f.scope)
	if err != nil || len(docs) != 1 || len(docs[0].Evidence) != 1 || docs[0].Evidence[0].Ref.ID != "current" {
		t.Fatal(docs, err)
	}
}
