package interaction

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

// Target is a trusted Host transport locator. Scope continues to identify the
// business/source authority and never grants permission to this destination.
type Target struct {
	Transport, Address, SceneLocator, ReplyTo, Environment string
}
type ResourceRef struct{ URI, Version, SHA256, MIMEType, Name string }
type Presentation struct {
	MediaType, SchemaVersion string
	Data                     json.RawMessage
}
type Payload struct {
	Text, Format string
	Resources    []ResourceRef
	Presentation *Presentation
}
type Intent struct {
	ID, SourceReceipt, TaskID, Kind, Body string
	GoalRevision                          uint64
	Scope                                 work.Scope
	Target                                *Target
	Payload                               *Payload
}
type ReceiptStage string

const (
	StageProviderAccepted ReceiptStage = "provider_accepted"
	StageDelivered        ReceiptStage = "delivered"
	StageLocalApplied     ReceiptStage = "local_applied"
	StagePermanentFailure ReceiptStage = "permanent_failure"
)

type Receipt struct {
	ProviderOperationID                   string
	ID, ProviderMessageID, BodySHA256     string
	ObservedAt                            time.Time
	Stage                                 ReceiptStage
	Transport, IntentSHA256, TargetSHA256 string
	Evidence                              json.RawMessage
	Failure                               string
}
type Lookup struct {
	Receipt *Receipt
	// ProvenAbsent preserves the legacy untyped port's definitive-absence
	// contract. It never authorizes resending a typed remote envelope.
	ProvenAbsent               bool
	IntentSHA256, TargetSHA256 string
	Evidence                   json.RawMessage
}

func IntentFingerprint(in Intent) (string, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return contextbuilder.Digest(raw), nil
}
func TargetFingerprint(target *Target) (string, error) {
	raw, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	return contextbuilder.Digest(raw), nil
}

// ReceiptFor creates binding fields, not delivery evidence. The adapter must
// choose its actual observed stage and attach genuine provider evidence.
func ReceiptFor(in Intent, stage ReceiptStage, providerID string) (Receipt, error) {
	fingerprint, err := IntentFingerprint(in)
	if err != nil {
		return Receipt{}, err
	}
	target, err := TargetFingerprint(in.Target)
	if err != nil {
		return Receipt{}, err
	}
	transport := ""
	if in.Target != nil {
		transport = in.Target.Transport
	}
	return Receipt{ID: in.ID, ProviderMessageID: providerID, BodySHA256: contextbuilder.Digest([]byte(in.Body)), ObservedAt: time.Now().UTC(), Stage: stage, Transport: transport, IntentSHA256: fingerprint, TargetSHA256: target}, nil
}
func sameJSON(a, b any) bool {
	left, e := json.Marshal(a)
	right, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(left, right)
}
func cloneIntent(in Intent) (Intent, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return Intent{}, err
	}
	var out Intent
	err = json.Unmarshal(raw, &out)
	return out, err
}

// ValidateIntent checks the immutable transport envelope independently of a provider.
func ValidateIntent(in Intent) error {
	if in.ID == "" || in.SourceReceipt == "" || in.Kind == "" {
		return errors.New("delivery: complete intent required")
	}
	if _, err := in.Scope.Key(); err != nil {
		return err
	}
	if in.Target != nil {
		t := in.Target
		if t.Transport == "" || t.Environment == "" || t.Address == "" && t.SceneLocator == "" {
			return errors.New("delivery: explicit transport, environment and destination required")
		}
		for _, v := range []string{t.Transport, t.Environment, t.Address, t.SceneLocator, t.ReplyTo} {
			if len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
				return errors.New("delivery: invalid target locator")
			}
		}
	}
	if in.Payload == nil {
		if in.Body == "" {
			return errors.New("delivery: body or typed payload required")
		}
		return nil
	}
	p := in.Payload
	if p.Text != in.Body || p.Format != "" && p.Format != "text/plain" && p.Format != "text/markdown" || len(p.Resources) > 64 {
		return errors.New("delivery: invalid payload text/format/resources")
	}
	if p.Text == "" && len(p.Resources) == 0 && p.Presentation == nil {
		return errors.New("delivery: empty payload")
	}
	for _, r := range p.Resources {
		digest, err := hex.DecodeString(r.SHA256)
		if r.URI == "" || r.Version == "" || r.MIMEType == "" || err != nil || len(digest) != 32 {
			return errors.New("delivery: immutable resource URI/version/SHA256/MIMEType required")
		}
	}
	if p.Presentation != nil && (p.Presentation.MediaType == "" || p.Presentation.SchemaVersion == "" || len(p.Presentation.Data) > 256<<10 || !json.Valid(p.Presentation.Data)) {
		return errors.New("delivery: versioned JSON presentation required")
	}
	return nil
}

// replayMatches compares original command arguments; omitted typed fields reuse
// the already frozen Host resolution rather than resolving a destination again.
func replayMatches(stored, command Intent) bool {
	if command.Target != nil && !sameJSON(command.Target, stored.Target) || command.Payload != nil && !sameJSON(command.Payload, stored.Payload) {
		return false
	}
	command.Target, command.Payload = stored.Target, stored.Payload
	return sameJSON(command, stored)
}
func preparedMatches(command, prepared Intent) bool {
	if command.Target != nil && !sameJSON(command.Target, prepared.Target) || command.Payload != nil && !sameJSON(command.Payload, prepared.Payload) {
		return false
	}
	prepared.Target, prepared.Payload = command.Target, command.Payload
	return sameJSON(command, prepared)
}
