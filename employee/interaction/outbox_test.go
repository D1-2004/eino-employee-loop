package interaction_test

import (
	"context"
	"errors"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/delivery"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	cb "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

type uncertainPort struct {
	sends, lookups int
	proof          interaction.Lookup
	fail           error
	cancel         context.CancelFunc
}

func (p *uncertainPort) Send(_ context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends++
	if p.cancel != nil {
		p.cancel()
	}
	return interaction.Receipt{ID: in.ID, ProviderMessageID: "provider-message", BodySHA256: cb.Digest([]byte(in.Body)), ObservedAt: time.Now()}, p.fail
}
func (p *uncertainPort) Lookup(context.Context, interaction.Intent) (interaction.Lookup, error) {
	p.lookups++
	return p.proof, nil
}
func fixture(t *testing.T, p interaction.Port) (*interaction.Manager, interaction.Intent) {
	t.Helper()
	db, e := sqlite.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	s := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "scene", Requester: "owner"}
	m := &interaction.Manager{DB: db, Port: p, Owner: "process-one", Authorize: func(_ context.Context, scope work.Scope) error {
		if scope != s {
			return work.ErrScope
		}
		return nil
	}}
	return m, interaction.Intent{ID: "delivery-1", SourceReceipt: "input-1", Kind: "reply", Body: "frozen answer", Scope: s}
}
func TestUnknownDeliveryRequiresProofBeforeResend(t *testing.T) {
	p := &uncertainPort{fail: errors.New("connection lost after provider acceptance")}
	m, in := fixture(t, p)
	ctx := context.Background()
	if _, e := m.Enqueue(ctx, in); e != nil {
		t.Fatal(e)
	}
	got, e := m.Deliver(ctx, in.Scope, in.ID)
	if e == nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	if _, e = m.Deliver(ctx, in.Scope, in.ID); !errors.Is(e, interaction.ErrUnknown) {
		t.Fatal(e)
	}
	if p.sends != 1 || p.lookups != 1 {
		t.Fatal("unknown sent duplicate", p)
	}
	// A reconstructed controller must reconcile the original stable provider key.
	m = &interaction.Manager{DB: m.DB, Port: p, Owner: "process-two", Authorize: m.Authorize}
	p.proof.Receipt = &interaction.Receipt{ID: in.ID, ProviderMessageID: "accepted-before-disconnect", BodySHA256: cb.Digest([]byte(in.Body))}
	got, e = m.Deliver(ctx, in.Scope, in.ID)
	if e != nil || got.State != "applied" || got.Receipt.ProviderMessageID != "accepted-before-disconnect" {
		t.Fatal(got, e)
	}
	if _, e = m.Deliver(ctx, in.Scope, in.ID); e != nil {
		t.Fatal(e)
	}
	if p.sends != 1 || p.lookups != 2 {
		t.Fatal("applied receipt caused I/O", p)
	}
	changed := in
	changed.Body = "different answer"
	if _, e = m.Enqueue(ctx, changed); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal(e)
	}
}
func TestProvenAbsentAllowsSendAndCancellationStillSettlesReceipt(t *testing.T) {
	p := &uncertainPort{fail: errors.New("no response")}
	m, in := fixture(t, p)
	ctx := context.Background()
	if _, e := m.Enqueue(ctx, in); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Deliver(ctx, in.Scope, in.ID); e == nil {
		t.Fatal("missing failure")
	}
	p.proof.ProvenAbsent = true
	p.fail = nil
	c, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	got, e := m.Deliver(c, in.Scope, in.ID)
	if e != nil || got.State != "applied" {
		t.Fatal("accepted delivery lost during cancellation", got, e)
	}
	rows, e := m.List(ctx, in.Scope)
	if e != nil || len(rows) != 1 || rows[0].State != "applied" || p.sends != 2 {
		t.Fatal(rows, e, p)
	}
	other := in.Scope
	other.Requester = "intruder"
	if _, e = m.List(ctx, other); !errors.Is(e, work.ErrScope) {
		t.Fatal(e)
	}
}
func TestLocalDeliveryIsVerifiedImmutableFile(t *testing.T) {
	md, e := markdown.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer md.Close()
	p := delivery.Local{Files: md}
	m, in := fixture(t, p)
	ctx := context.Background()
	if _, e = m.Enqueue(ctx, in); e != nil {
		t.Fatal(e)
	}
	got, e := m.Deliver(ctx, in.Scope, in.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(got.Receipt.ProviderMessageID, "local-file/") {
		t.Fatal(got)
	}
	name := strings.TrimPrefix(got.Receipt.ProviderMessageID, "local-file/")
	file, e := md.Read(ctx, name)
	if e != nil || file.Body != in.Body || file.SHA256 != got.Receipt.BodySHA256 {
		t.Fatal(file, e)
	}
	if _, e = p.Send(ctx, in); e != nil {
		t.Fatal("local replay changed immutable file", e)
	}
	changed := in
	changed.Body = "different body"
	if _, e = p.Send(ctx, changed); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal("same delivery key overwrote original", e)
	}
}

type blockingPort struct {
	sends, lookups   atomic.Int32
	started, release chan struct{}
}

func (p *blockingPort) Send(_ context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends.Add(1)
	close(p.started)
	<-p.release
	return interaction.Receipt{ID: in.ID, ProviderMessageID: "verified", BodySHA256: cb.Digest([]byte(in.Body))}, nil
}
func (p *blockingPort) Lookup(context.Context, interaction.Intent) (interaction.Lookup, error) {
	p.lookups.Add(1)
	return interaction.Lookup{ProvenAbsent: true}, nil
}
func TestInFlightDeliveryCannotLookupAbsentAndDuplicateSend(t *testing.T) {
	p := &blockingPort{started: make(chan struct{}), release: make(chan struct{})}
	m, in := fixture(t, p)
	ctx := context.Background()
	if _, e := m.Enqueue(ctx, in); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := m.Deliver(ctx, in.Scope, in.ID); done <- e }()
	<-p.started
	if _, e := m.Deliver(ctx, in.Scope, in.ID); !errors.Is(e, interaction.ErrUnknown) {
		t.Fatal(e)
	}
	close(p.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if p.sends.Load() != 1 || p.lookups.Load() != 0 {
		t.Fatal("active send treated as absent", p.sends.Load(), p.lookups.Load())
	}
}

type wrongReceiptPort struct{ uncertainPort }

func (p *wrongReceiptPort) Send(context.Context, interaction.Intent) (interaction.Receipt, error) {
	p.sends++
	return interaction.Receipt{ID: "wrong-id", ProviderMessageID: "untrusted", BodySHA256: "wrong"}, nil
}
func TestUnverifiedProviderReceiptRemainsUnknownAndCanReconcile(t *testing.T) {
	p := &wrongReceiptPort{}
	m, in := fixture(t, p)
	ctx := context.Background()
	if _, e := m.Enqueue(ctx, in); e != nil {
		t.Fatal(e)
	}
	got, e := m.Deliver(ctx, in.Scope, in.ID)
	if e == nil || got.State != "unknown" || got.Receipt != nil {
		t.Fatal("invalid provider receipt accepted", got, e)
	}
	p.proof.Receipt = &interaction.Receipt{ID: in.ID, ProviderMessageID: "read-back-proof", BodySHA256: cb.Digest([]byte(in.Body))}
	got, e = m.Deliver(ctx, in.Scope, in.ID)
	if e != nil || got.State != "applied" || p.sends != 1 || p.lookups != 1 {
		t.Fatal("invalid response blocked same-controller recovery", got, e, p)
	}
}
