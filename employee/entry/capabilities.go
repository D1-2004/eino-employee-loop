package entry

import (
	"context"
	"errors"
	"fmt"
	"sort"

	ownerloop "github.com/D1-2004/eino-employee-loop/employee/owner"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

var ErrUnsupported = errors.New("entry: capability unsupported by backend")

type Capability string

const (
	CapabilitySchedule    Capability = "schedule"
	CapabilitySignal      Capability = "signal"
	CapabilityOwner       Capability = "owner-context"
	CapabilityRetry       Capability = "retry"
	CapabilityDiagnostics Capability = "diagnostics"
)

type CapabilityProvider interface{ Capabilities() []Capability }
type scheduleBackend interface {
	Schedule(context.Context, work.Scope, work.Schedule) (work.Schedule, error)
}
type signalBackend interface {
	Signal(context.Context, Input, string) error
}
type ownerBackend interface {
	OwnerDocuments(context.Context, work.Scope) ([]ownerloop.Document, error)
}
type retryBackend interface {
	Retry(context.Context, string) error
}
type diagnosticsBackend interface {
	Failures(context.Context, work.Scope) ([]Failure, error)
}

func (s *standalone) Capabilities() []Capability {
	return []Capability{CapabilitySchedule, CapabilitySignal, CapabilityOwner, CapabilityRetry, CapabilityDiagnostics}
}
func backendCapabilities(b Backend, opts Options) (map[Capability]bool, error) {
	caps := map[Capability]bool{}
	if p, ok := b.(CapabilityProvider); ok {
		for _, c := range p.Capabilities() {
			supported := false
			switch c {
			case CapabilitySchedule:
				_, supported = b.(scheduleBackend)
			case CapabilitySignal:
				_, supported = b.(signalBackend)
			case CapabilityOwner:
				_, supported = b.(ownerBackend)
			case CapabilityRetry:
				_, supported = b.(retryBackend)
			case CapabilityDiagnostics:
				_, supported = b.(diagnosticsBackend)
			}
			if !supported || caps[c] {
				return nil, fmt.Errorf("entry: invalid backend capability %q", c)
			}
			caps[c] = true
		}
	}
	required := append([]Capability(nil), opts.RequiredCapabilities...)
	if len(opts.Profile.OwnerLoops) > 0 {
		required = append(required, CapabilityOwner)
	}
	for _, c := range required {
		if !caps[c] {
			return nil, fmt.Errorf("%w: %s", ErrUnsupported, c)
		}
	}
	return caps, nil
}

// Capabilities returns the frozen capability set checked when the backend opened.
func (e *Entry) Capabilities() []Capability {
	out := []Capability{}
	for c := range e.capabilities {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
