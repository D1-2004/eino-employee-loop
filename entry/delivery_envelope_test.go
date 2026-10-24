package entry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/loop/looptest"
	"github.com/D1-2004/eino-employee-loop/work"
)

type entryStagedDelivery struct {
	stage          interaction.ReceiptStage
	sends, lookups int
	last           interaction.Intent
}

func (p *entryStagedDelivery) observed(in interaction.Intent) (interaction.Receipt, error) {
	// The provider observes the frozen target supplied by the Host, independently
	// of the business scene and of later resolver configuration.
	raw, _ := json.Marshal(in)
	_ = json.Unmarshal(raw, &p.last)
	r, err := interaction.ReceiptFor(in, p.stage, "actual-provider-message")
	r.Evidence = json.RawMessage(`{"receipt_source":"independent-provider"}`)
	return r, err
}
func (p *entryStagedDelivery) Send(_ context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends++
	return p.observed(in)
}
func (p *entryStagedDelivery) Lookup(_ context.Context, in interaction.Intent) (interaction.Lookup, error) {
	p.lookups++
	r, err := p.observed(in)
	return interaction.Lookup{Receipt: &r}, err
}

func TestEntryAcceptedTransportDoesNotRecordDeliveryUntilReconciledAfterReopen(t *testing.T) {
	ctx := context.Background()
	p := &entryStagedDelivery{stage: interaction.StageProviderAccepted}
	address, resolutions := "trusted-transport-address", 0
	ms := map[string]*looptest.Model{
		"chat":     looptest.New(dispatchStep(t)),
		"executor": looptest.New(call("finish_run", "done", map[string]any{"state": "succeeded", "summary": "Actual local computation completed"})),
		"review":   looptest.New(reviewStep(t)),
	}
	opts := Options{Directory: t.TempDir(), Profile: testProfile(), Models: models(ms), Delivery: p, AuthorizeDelivery: func(_ context.Context, in interaction.Intent) error {
		if in.Target == nil || in.Target.Address != "trusted-transport-address" || in.Target.Transport != "dws" || in.Target.Environment != "pre" {
			return work.ErrScope
		}
		return nil
	}, ResolveDelivery: func(_ context.Context, in interaction.Intent) (interaction.Intent, error) {
		resolutions++
		in.Target = &interaction.Target{Transport: "dws", Address: address, Environment: "pre", ReplyTo: "platform-origin-message"}
		in.Payload = &interaction.Payload{Text: in.Body, Format: "text/markdown"}
		return in, nil
	}}
	e, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	input := testInput("accepted-task-source", "Complete a calculation and report its actual result")
	if _, err = e.Submit(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 40); !errors.Is(err, interaction.ErrPending) {
		t.Fatal("accepted was treated as terminal delivery", err)
	}
	tasks, err := e.Tasks(ctx, "dm")
	if err != nil || len(tasks) != 1 || tasks[0].State != work.Completed || len(tasks[0].Deliveries) != 0 {
		t.Fatal(tasks, err)
	}
	rows, err := e.Deliveries(ctx, "dm")
	if err != nil || len(rows) != 1 || rows[0].State != "provider_accepted" || rows[0].Receipt.Stage != interaction.StageProviderAccepted {
		t.Fatal(rows, err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	address = "later-host-target"
	p.stage = interaction.StageDelivered
	e, err = Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Submit(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = e.Drain(ctx, 20); err != nil {
		t.Fatal(err)
	}
	tasks, err = e.Tasks(ctx, "dm")
	rows, _ = e.Deliveries(ctx, "dm")
	if err != nil || len(tasks[0].Deliveries) != 1 || len(rows) != 1 || rows[0].State != "applied" || rows[0].Receipt.Stage != interaction.StageDelivered {
		t.Fatal(tasks, rows, err)
	}
	if p.sends != 1 || p.lookups != 1 || p.last.Target.Address != "trusted-transport-address" || resolutions != 1 {
		t.Fatal("replayed delivery was redirected or resubmitted", p, resolutions)
	}
	if ms["chat"].Calls() != 1 || ms["executor"].Calls() != 1 || ms["review"].Calls() != 1 {
		t.Fatal("delivery reconciliation reexecuted business work")
	}
}
