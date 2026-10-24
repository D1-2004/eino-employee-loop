package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/delivery"
	"path/filepath"
	"testing"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
)

type stagedPort struct {
	sends, lookups     int
	stage              interaction.ReceiptStage
	mutate             func(*interaction.Receipt)
	lookupErr, sendErr error
	absent             bool
	contradictory      bool
}

func (p *stagedPort) receipt(in interaction.Intent) (interaction.Receipt, error) {
	r, err := interaction.ReceiptFor(in, p.stage, "provider-message")
	if err != nil {
		return r, err
	}
	r.Evidence = json.RawMessage(`{"opaque":{"provider":"test-service"}}`)
	if p.stage == interaction.StagePermanentFailure {
		r.Failure = "provider rejected this target"
	}
	if p.mutate != nil {
		p.mutate(&r)
	}
	return r, nil
}
func (p *stagedPort) Send(_ context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends++
	if errors.Is(p.sendErr, interaction.ErrPermanent) && !p.contradictory {
		return interaction.Receipt{}, p.sendErr
	}
	r, err := p.receipt(in)
	return r, errors.Join(err, p.sendErr)
}

func TestContradictorySendRejectionCannotInventPermanentFailure(t *testing.T) {
	p := &stagedPort{stage: interaction.StageProviderAccepted, sendErr: interaction.ErrPermanent, contradictory: true}
	m, in := fixture(t, p)
	in = remoteIntent(in)
	m.ValidateSend = func(context.Context, interaction.Intent) error { return nil }
	ctx := context.Background()
	if _, err := m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	r, err := m.Deliver(ctx, in.Scope, in.ID)
	if err == nil || r.State != "unknown" {
		t.Fatal(r, err)
	}
	p.absent = true
	if _, err = m.Deliver(ctx, in.Scope, in.ID); !errors.Is(err, interaction.ErrUnknown) || p.sends != 1 {
		t.Fatal(err, p)
	}
}
func (p *stagedPort) Lookup(_ context.Context, in interaction.Intent) (interaction.Lookup, error) {
	p.lookups++
	if p.lookupErr != nil {
		return interaction.Lookup{}, p.lookupErr
	}
	if p.absent {
		return interaction.Lookup{ProvenAbsent: true}, nil
	}
	r, err := p.receipt(in)
	return interaction.Lookup{Receipt: &r}, err
}
func remoteIntent(in interaction.Intent) interaction.Intent {
	in.Target = &interaction.Target{Transport: "dws", Address: "trusted-cid", Environment: "pre", ReplyTo: "original-message"}
	in.Payload = &interaction.Payload{Text: in.Body, Format: "text/markdown"}
	return in
}
func TestAcceptedEnvelopePersistsAcrossDatabaseReopenUntilDelivered(t *testing.T) {
	p := &stagedPort{stage: interaction.StageProviderAccepted}
	m, in := fixture(t, p)
	in = remoteIntent(in)
	m.ValidateSend = func(context.Context, interaction.Intent) error { return nil }
	ctx := context.Background()
	if _, err := m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	accepted, err := m.Deliver(ctx, in.Scope, in.ID)
	if !errors.Is(err, interaction.ErrPending) || accepted.State != "provider_accepted" || accepted.Receipt.Stage != interaction.StageProviderAccepted {
		t.Fatal(accepted, err)
	}
	store := m.DB.(*sqlite.Store)
	dir := filepath.Dir(store.Path())
	// Reopen the actual SQLite file, not merely a new controller object.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m = &interaction.Manager{DB: db, Port: p, Owner: "new-process", Authorize: m.Authorize, ValidateSend: m.ValidateSend}
	p.lookupErr = errors.New("provider status temporarily unavailable")
	if _, err = m.Deliver(ctx, in.Scope, in.ID); err == nil {
		t.Fatal("lookup error disappeared")
	}
	rows, _ := m.List(ctx, in.Scope)
	if rows[0].State != "provider_accepted" || rows[0].Receipt.Stage != interaction.StageProviderAccepted {
		t.Fatal(rows)
	}
	p.lookupErr = nil
	p.absent = true
	if _, err = m.Deliver(ctx, in.Scope, in.ID); !errors.Is(err, interaction.ErrPending) {
		t.Fatal(err)
	}
	p.absent = false
	p.stage = interaction.StageDelivered
	delivered, err := m.Deliver(ctx, in.Scope, in.ID)
	if err != nil || delivered.State != "applied" || delivered.Receipt.Stage != interaction.StageDelivered || p.sends != 1 {
		t.Fatal(delivered, err, p)
	}
	if _, err = m.Deliver(ctx, in.Scope, in.ID); err != nil || p.sends != 1 || p.lookups != 3 {
		t.Fatal(err, p)
	}
}

func TestTypedForeignOrLegacyReceiptNeverAppliesOrResends(t *testing.T) {
	for name, mutate := range map[string]func(*interaction.Receipt){
		"intent-fingerprint": func(r *interaction.Receipt) { r.IntentSHA256 = "foreign" },
		"target-fingerprint": func(r *interaction.Receipt) { r.TargetSHA256 = "foreign" },
		"transport":          func(r *interaction.Receipt) { r.Transport = "another-transport" },
		"legacy-stage":       func(r *interaction.Receipt) { r.Stage = "" },
		"platform-as-local":  func(r *interaction.Receipt) { r.Stage = interaction.StageLocalApplied },
		"invalid-evidence":   func(r *interaction.Receipt) { r.Evidence = json.RawMessage(`{"broken"`) },
	} {
		t.Run(name, func(t *testing.T) {
			p := &stagedPort{stage: interaction.StageDelivered, mutate: mutate}
			m, in := fixture(t, p)
			in = remoteIntent(in)
			m.ValidateSend = func(context.Context, interaction.Intent) error { return nil }
			ctx := context.Background()
			if _, err := m.Enqueue(ctx, in); err != nil {
				t.Fatal(err)
			}
			r, err := m.Deliver(ctx, in.Scope, in.ID)
			if err == nil || r.State != "unknown" || r.Receipt != nil {
				t.Fatal(r, err)
			}
			p.absent = true
			if _, err = m.Deliver(ctx, in.Scope, in.ID); !errors.Is(err, interaction.ErrUnknown) || p.sends != 1 {
				t.Fatal(err, p)
			}
			p.absent = false
			p.mutate = nil
			r, err = m.Deliver(ctx, in.Scope, in.ID)
			if err != nil || r.State != "applied" || p.sends != 1 {
				t.Fatal(r, err, p)
			}
		})
	}
}

func TestForeignLookupPreservesAcceptedEvidenceAndDefinitiveFailureIsTerminal(t *testing.T) {
	p := &stagedPort{stage: interaction.StageProviderAccepted}
	m, in := fixture(t, p)
	in = remoteIntent(in)
	m.ValidateSend = func(context.Context, interaction.Intent) error { return nil }
	ctx := context.Background()
	if _, err := m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Deliver(ctx, in.Scope, in.ID); !errors.Is(err, interaction.ErrPending) {
		t.Fatal(err)
	}
	p.stage = interaction.StageDelivered
	p.mutate = func(r *interaction.Receipt) { r.ProviderMessageID = "other-message" }
	r, err := m.Deliver(ctx, in.Scope, in.ID)
	if err == nil || r.State != "provider_accepted" || r.Receipt.ProviderMessageID != "provider-message" {
		t.Fatal(r, err)
	}
	p.mutate = nil
	p.stage = interaction.StagePermanentFailure
	r, err = m.Deliver(ctx, in.Scope, in.ID)
	if !errors.Is(err, interaction.ErrPermanent) || r.State != "failed" || r.LastError == "" {
		t.Fatal(r, err)
	}
	lookups := p.lookups
	if _, err = m.Deliver(ctx, in.Scope, in.ID); err != nil || p.lookups != lookups || p.sends != 1 {
		t.Fatal(err, p)
	}
}

func TestResolverFreezesTargetAndPayloadWithoutChangingReplayCommand(t *testing.T) {
	p := &stagedPort{stage: interaction.StageDelivered}
	m, in := fixture(t, p)
	ctx := context.Background()
	currentAddress, calls := "first-trusted-target", 0
	m.PrepareIntent = func(_ context.Context, base interaction.Intent) (interaction.Intent, error) {
		calls++
		base = remoteIntent(base)
		base.Target.Address = currentAddress
		return base, nil
	}
	first, err := m.Enqueue(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	currentAddress = "later-target"
	replay, err := m.Enqueue(ctx, in)
	if err != nil || calls != 1 || replay.Intent.Target.Address != first.Intent.Target.Address {
		t.Fatal(replay, err, calls)
	}
	changed := in
	changed.Body = "mutated base command"
	if _, err = m.Enqueue(ctx, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal(err)
	}
	changed = remoteIntent(in)
	changed.Target.Address = currentAddress
	if _, err = m.Enqueue(ctx, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal(err)
	}
	// The resolver and caller retain pointers, but cannot mutate durable state.
	first.Intent.Target.Address = "caller-mutated"
	rows, err := m.List(ctx, in.Scope)
	if err != nil || rows[0].Intent.Target.Address != "first-trusted-target" {
		t.Fatal(rows, err)
	}
}

func TestResolverCannotChangeSourceOrOverrideExplicitTarget(t *testing.T) {
	for _, mutate := range []func(*interaction.Intent){func(in *interaction.Intent) { in.Scope.Scene = "new-scene" }, func(in *interaction.Intent) { in.Target.Address = "different-target" }} {
		p := &stagedPort{stage: interaction.StageDelivered}
		m, in := fixture(t, p)
		in = remoteIntent(in)
		m.PrepareIntent = func(_ context.Context, in interaction.Intent) (interaction.Intent, error) {
			mutate(&in)
			return in, nil
		}
		if _, err := m.Enqueue(context.Background(), in); err == nil {
			t.Fatal("resolver changed accepted authority/destination")
		}
		rows, _ := m.List(context.Background(), in.Scope)
		if len(rows) != 0 {
			t.Fatal(rows)
		}
	}
}

func TestTypedLocalReceiptAndEnvelopeAreImmutable(t *testing.T) {
	files, err := markdown.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	p := delivery.Local{Files: files}
	m, in := fixture(t, p)
	in.Target = &interaction.Target{Transport: "local-file", Address: "workspace", Environment: "local"}
	in.Payload = &interaction.Payload{Text: in.Body, Format: "text/markdown"}
	ctx := context.Background()
	if _, err = m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	r, err := m.Deliver(ctx, in.Scope, in.ID)
	if err != nil || r.State != "applied" || r.Receipt.Stage != interaction.StageLocalApplied || r.Receipt.Transport != "local-file" {
		t.Fatal(r, err)
	}
	changed := in
	target := *in.Target
	target.ReplyTo = "another-source"
	changed.Target = &target
	if _, err = p.Send(ctx, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("same body reattributed to new target", err)
	}
}

func TestRemoteTargetNeedsHostValidationAndPermanentSendFailureDoesNotRetry(t *testing.T) {
	p := &stagedPort{stage: interaction.StageDelivered}
	m, in := fixture(t, p)
	in = remoteIntent(in)
	ctx := context.Background()
	if _, err := m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Deliver(ctx, in.Scope, in.ID); err == nil || p.sends != 0 {
		t.Fatal("source scope granted target authority", err, p)
	}
	m.ValidateSend = func(context.Context, interaction.Intent) error { return nil }
	p.sendErr = interaction.ErrPermanent
	r, err := m.Deliver(ctx, in.Scope, in.ID)
	if !errors.Is(err, interaction.ErrPermanent) || r.State != "failed" {
		t.Fatal(r, err)
	}
	if _, err = m.Deliver(ctx, in.Scope, in.ID); err != nil || p.sends != 1 {
		t.Fatal(err, p)
	}
}

func TestPayloadResourcesAndPresentationAreFrozenWithoutAssumingAdapterSupport(t *testing.T) {
	p := &stagedPort{stage: interaction.StageDelivered}
	m, in := fixture(t, p)
	in = remoteIntent(in)
	const digest = "0000000000000000000000000000000000000000000000000000000000000000"
	in.Payload.Resources = []interaction.ResourceRef{{URI: "resource://immutable-result", Version: "revision-1", SHA256: digest, MIMEType: "application/pdf", Name: "result.pdf"}}
	in.Payload.Presentation = &interaction.Presentation{MediaType: "application/vnd.host.presentation+json", SchemaVersion: "host-layout/1", Data: json.RawMessage(`{"title":"Result","resource":"resource://immutable-result"}`)}
	ctx := context.Background()
	if _, err := m.Enqueue(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Payload.Resources[0].Version = "revision-2"
	in.Payload.Presentation.Data[0] = '['
	in.Target.Address = "caller-mutated-target"
	rows, err := m.List(ctx, in.Scope)
	if err != nil || rows[0].Intent.Payload.Resources[0].Version != "revision-1" || rows[0].Intent.Payload.Resources[0].SHA256 != digest || rows[0].Intent.Target.Address != "trusted-cid" || !json.Valid(rows[0].Intent.Payload.Presentation.Data) {
		t.Fatal(rows, err)
	}
	// An envelope can carry a Host schema, but Local explicitly refuses to
	// claim it rendered a remote card or uploaded an attachment.
	files, err := markdown.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	local := delivery.Local{Files: files}
	unsupported := rows[0].Intent
	unsupported.Target = nil
	if _, err = local.Send(ctx, unsupported); !errors.Is(err, interaction.ErrPermanent) {
		t.Fatal(err)
	}
}
