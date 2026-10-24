package entry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	contextbuilder "github.com/D1-2004/eino-employee-loop/modules/context"
	"github.com/D1-2004/eino-employee-loop/modules/interaction"
	work "github.com/D1-2004/eino-employee-loop/modules/task/state"
	host "github.com/D1-2004/eino-employee-loop/ports/services"
)

type evolvingTools struct {
	description string
	effects     bool
}

func (p *evolvingTools) DiscoverTools(context.Context, host.Call) ([]host.Tool, error) {
	return []host.Tool{{ID: "lookup", Description: p.description, Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Effect: p.effects}}, nil
}
func (p *evolvingTools) CallTool(context.Context, host.Call, host.ToolCall) (host.ToolResult, error) {
	return host.ToolResult{Content: json.RawMessage(`{"verified":true}`)}, nil
}
func TestHostCatalogFrozenAcrossReopenRejectsChangedToolsAndEffects(t *testing.T) {
	ctx := context.Background()
	provider := &evolvingTools{description: "exact-v1"}
	p := testProfile()
	p.HostTools = true
	opts := Options{Directory: t.TempDir(), Profile: p, Models: models(nil), Services: host.Services{Tools: provider}}
	e, err := openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	s := e.backend.(*standalone)
	scope := p.Scope("dm")
	if _, err = s.context.Admit(ctx, scope, contextbuilder.Source{ID: "source", Actor: p.Owner, Text: "accepted original"}); err != nil {
		t.Fatal(err)
	}
	frame, err := s.context.Build(ctx, scope, contextbuilder.Front, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	before := func(ctx context.Context) error { return s.context.Revalidate(ctx, frame) }
	if _, err = s.hostFunctions(ctx, scope, "execution-stable", frame, before); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	provider.description = "changed-v2"
	e, err = openDefault(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s = e.backend.(*standalone)
	before = func(ctx context.Context) error { return s.context.Revalidate(ctx, frame) }
	if _, err = s.hostFunctions(ctx, scope, "execution-stable", frame, before); err == nil || !strings.Contains(err.Error(), "frozen Host tool catalog changed") {
		t.Fatal(err)
	}
	provider.effects = true
	if _, err = s.hostFunctions(ctx, scope, "new-effect", frame, before); !errors.Is(err, host.ErrUnsupported) {
		t.Fatal(err)
	}
	if err = s.context.Revoke(ctx, scope, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.hostFunctions(ctx, scope, "revoked", frame, before); !errors.Is(err, contextbuilder.ErrRevoked) {
		t.Fatal(err)
	}
}
func TestTypedDeliveryNeedsExplicitTargetAuthority(t *testing.T) {
	ctx := context.Background()
	provider := &entryStagedDelivery{stage: "delivered"}
	e, err := openDefault(ctx, Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(nil), Delivery: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := e.backend.(*standalone)
	scope := testProfile().Scope("dm")
	if _, err = s.context.Admit(ctx, scope, contextbuilder.Source{ID: "source", Actor: scope.Requester, Text: "reply here"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.delivery.Enqueue(ctx, typedRemoteIntent(scope))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.delivery.Deliver(ctx, scope, "remote"); !errors.Is(err, work.ErrScope) || provider.sends != 0 {
		t.Fatal("source scope granted remote destination", provider.sends, err)
	}
}

func typedRemoteIntent(scope work.Scope) interaction.Intent {
	return interaction.Intent{ID: "remote", SourceReceipt: "source", Scope: scope, Kind: "reply", Body: "answer", Target: &interaction.Target{Transport: "dws", Address: "ungranted-address", Environment: "test"}}
}
