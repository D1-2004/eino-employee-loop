// Package delivery implements immutable local file delivery.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/interaction"
	"github.com/D1-2004/eino-employee-loop/employee/ports/storage"
)

func sameJSON(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}

// Local writes a real immutable Markdown delivery, not an IM claim.
type Local struct{ Files storage.Artifacts }

func (l Local) path(in interaction.Intent) string {
	key, _ := in.Scope.Key()
	return "deliveries/" + contextbuilder.Digest([]byte(key+"/"+in.ID)) + ".md"
}
func (l Local) envelopePath(in interaction.Intent) string { return l.path(in) + ".intent.md" }
func (l Local) Lookup(ctx context.Context, in interaction.Intent) (interaction.Lookup, error) {
	if l.Files == nil {
		return interaction.Lookup{}, errors.New("delivery: local artifacts required")
	}
	if in.Target != nil && (in.Target.Transport != "local-file" || in.Target.Address != "workspace" || in.Target.Environment != "local" || in.Target.SceneLocator != "") {
		return interaction.Lookup{}, errors.Join(interaction.ErrPermanent, errors.New("delivery: Local supports only the local workspace locator"))
	}
	if in.Payload != nil && (len(in.Payload.Resources) > 0 || in.Payload.Presentation != nil) {
		return interaction.Lookup{}, errors.Join(interaction.ErrPermanent, errors.New("delivery: Local supports text only"))
	}
	if err := interaction.ValidateIntent(in); err != nil {
		return interaction.Lookup{}, err
	}
	envelope, envelopeErr := l.Files.Read(ctx, l.envelopePath(in))
	if envelopeErr != nil && !errors.Is(envelopeErr, fs.ErrNotExist) {
		return interaction.Lookup{}, envelopeErr
	}
	if envelopeErr == nil {
		var frozen interaction.Intent
		if err := json.Unmarshal([]byte(envelope.Body), &frozen); err != nil {
			return interaction.Lookup{}, err
		}
		if !sameJSON(frozen, in) {
			return interaction.Lookup{}, storage.ErrConflict
		}
	}
	d, err := l.Files.Read(ctx, l.path(in))
	if errors.Is(err, fs.ErrNotExist) {
		fingerprint, _ := interaction.IntentFingerprint(in)
		target, _ := interaction.TargetFingerprint(in.Target)
		return interaction.Lookup{ProvenAbsent: true, IntentSHA256: fingerprint, TargetSHA256: target}, nil
	}
	if err != nil {
		return interaction.Lookup{}, err
	}
	if d.Body != in.Body {
		return interaction.Lookup{}, storage.ErrConflict
	}
	// Historical untyped Markdown files have only a body-level proof. They
	// cannot be retroactively attributed to a newly supplied remote envelope.
	if envelopeErr != nil && (in.Target != nil || in.Payload != nil) {
		return interaction.Lookup{}, interaction.ErrUnknown
	}
	receipt, e := interaction.ReceiptFor(in, interaction.StageLocalApplied, "local-file/"+d.Path)
	if e != nil {
		return interaction.Lookup{}, e
	}
	receipt.Transport = "local-file"
	receipt.Evidence, _ = json.Marshal(map[string]string{"path": d.Path, "sha256": d.SHA256})
	return interaction.Lookup{Receipt: &receipt}, nil
}
func (l Local) Send(ctx context.Context, in interaction.Intent) (interaction.Receipt, error) {
	old, err := l.Lookup(ctx, in)
	if err != nil {
		return interaction.Receipt{}, err
	}
	if old.Receipt != nil {
		return *old.Receipt, nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return interaction.Receipt{}, err
	}
	if prior, e := l.Files.Read(ctx, l.envelopePath(in)); e == nil {
		if prior.Body != string(raw) {
			return interaction.Receipt{}, storage.ErrConflict
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return interaction.Receipt{}, e
	} else {
		if _, err = l.Files.Write(ctx, l.envelopePath(in), "", string(raw)); err != nil {
			return interaction.Receipt{}, err
		}
	}
	if _, err = l.Files.Write(ctx, l.path(in), "", in.Body); err != nil {
		return interaction.Receipt{}, err
	}
	out, err := l.Lookup(ctx, in)
	if err != nil {
		return interaction.Receipt{}, err
	}
	if out.Receipt == nil {
		return interaction.Receipt{}, interaction.ErrUnknown
	}
	return *out.Receipt, nil
}
