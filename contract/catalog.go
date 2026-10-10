package contract

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed catalog/*.json
var catalogFS embed.FS

// Coalesce describes how bursts of one event type collapse into a window.
// A window closes QuietMs after its last item, MaxWaitMs after its first
// item, or when it holds MaxItems, whichever comes first.
type Coalesce struct {
	QuietMs   int64 `json:"quiet_ms"`
	MaxWaitMs int64 `json:"max_wait_ms"`
	MaxItems  int   `json:"max_items"`
}

// TypeSpec is the routing contract of one event type.
type TypeSpec struct {
	Type  string `json:"type"`
	Class Class  `json:"class"`
	Mode  Mode   `json:"mode"`
	Lane  Lane   `json:"lane"`
	// Session is required for foreground/background lanes and empty for host.
	Session SessionKind `json:"session,omitempty"`
	// SessionKey names the Refs key that partitions the session (for example
	// "task" for follow-up sessions). Empty means one session per scene.
	SessionKey string `json:"session_key,omitempty"`
	// RequiresScene rejects events without a scene. Enablement events that
	// concern the whole employee set it to false.
	RequiresScene bool `json:"requires_scene"`
	// Preemptible windows are cancelled, not resumed, when the foreground
	// admits new human input in the same scene (observation semantics).
	Preemptible bool `json:"preemptible,omitempty"`
	// Preempts makes this event cancel unsubmitted preemptible windows of
	// the scene when it is admitted.
	Preempts bool `json:"preempts,omitempty"`
	// Revokes lists session kinds whose open windows become invalid when this
	// event is admitted (policy or configuration revisions).
	Revokes  []SessionKind `json:"revokes,omitempty"`
	Coalesce *Coalesce     `json:"coalesce,omitempty"`
	// SummaryKey is the i18n key of a one-line description.
	SummaryKey string `json:"summary_key"`
}

// Validate enforces the routing invariants of the scene model:
//   - the foreground only receives triggered business events from people;
//   - listening events never enter the foreground;
//   - host-lane events have no session, other lanes always have one.
func (s TypeSpec) Validate() error {
	var problems []string
	if strings.TrimSpace(s.Type) == "" {
		problems = append(problems, "type is required")
	}
	switch s.Class {
	case ClassBusiness, ClassExecution, ClassCompletion, ClassSchedule, ClassEnablement:
	default:
		problems = append(problems, fmt.Sprintf("unknown class %q", s.Class))
	}
	switch s.Mode {
	case ModeTriggered, ModeListening:
	default:
		problems = append(problems, fmt.Sprintf("unknown mode %q", s.Mode))
	}
	switch s.Lane {
	case LaneForeground:
		if s.Mode != ModeTriggered || s.Class != ClassBusiness {
			problems = append(problems, "foreground lane accepts only triggered business events")
		}
		if s.Session != SessionInteractive {
			problems = append(problems, "foreground lane uses the interactive session")
		}
		if s.Preemptible {
			problems = append(problems, "foreground windows are not preemptible")
		}
	case LaneBackground:
		if s.Session == "" || s.Session == SessionInteractive {
			problems = append(problems, "background lane requires a background session kind")
		}
	case LaneHost:
		if s.Session != "" || s.SessionKey != "" {
			problems = append(problems, "host lane has no session")
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown lane %q", s.Lane))
	}
	if s.Lane != LaneHost && !s.RequiresScene {
		problems = append(problems, "session lanes require a scene")
	}
	if s.Coalesce != nil && (s.Coalesce.MaxItems <= 0 || s.Coalesce.MaxWaitMs < s.Coalesce.QuietMs || s.Coalesce.QuietMs < 0) {
		problems = append(problems, "coalesce requires max_items > 0 and 0 <= quiet_ms <= max_wait_ms")
	}
	if strings.TrimSpace(s.SummaryKey) == "" {
		problems = append(problems, "summary_key is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("contract: type %q: %s", s.Type, strings.Join(problems, "; "))
	}
	return nil
}

// SessionRef identifies one session within a scene.
type SessionRef struct {
	Scene string      `json:"scene"`
	Kind  SessionKind `json:"kind"`
	Key   string      `json:"key,omitempty"`
}

// String renders a stable key such as "scene-1/follow_up/task-9".
func (r SessionRef) String() string {
	if r.Key == "" {
		return r.Scene + "/" + string(r.Kind)
	}
	return r.Scene + "/" + string(r.Kind) + "/" + r.Key
}

// Admission is the routing decision for one event.
type Admission struct {
	Event   Event       `json:"event"`
	Spec    TypeSpec    `json:"spec"`
	Session *SessionRef `json:"session,omitempty"`
	// Cancel lists sessions whose unsubmitted windows the SessionQueue must
	// cancel in the same transaction as this admission.
	Cancel []SessionCancel `json:"cancel,omitempty"`
}

// SessionCancel asks the queue to cancel open windows of matching sessions.
// An empty Key matches every session of that kind in the scene.
type SessionCancel struct {
	Scene      string      `json:"scene"`
	Kind       SessionKind `json:"kind"`
	Key        string      `json:"key,omitempty"`
	OnlyPreemp bool        `json:"only_preemptible,omitempty"`
	Reason     string      `json:"reason"`
}

// Cancellation reasons recorded by the queue.
const (
	CancelForegroundAdmission = "foreground_admission"
	CancelRevoked             = "revoked"
)

// Catalog is a registry of event types. It is safe for concurrent use.
type Catalog struct {
	mu    sync.RWMutex
	types map[string]TypeSpec
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{types: map[string]TypeSpec{}} }

// StandardCatalog returns a new catalog preloaded with the standard event
// types (catalog/events.json): the types the module produces or handles end
// to end. Hosts may Register additional types.
func StandardCatalog() *Catalog {
	specs, err := StandardEventTypes()
	if err != nil {
		panic(err)
	}
	return mustCatalog(specs)
}

func mustCatalog(specs []TypeSpec) *Catalog {
	c := NewCatalog()
	for _, s := range specs {
		if err := c.Register(s); err != nil {
			panic(err)
		}
	}
	return c
}

// catalogDoc is the layout of the embedded catalog files.
type catalogDoc struct {
	Version int        `json:"version"`
	Types   []TypeSpec `json:"types,omitempty"`
	Tools   []ToolSpec `json:"tools,omitempty"`
}

func readCatalog(name string) (catalogDoc, error) {
	var doc catalogDoc
	raw, err := catalogFS.ReadFile("catalog/" + name)
	if err != nil {
		return doc, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return doc, fmt.Errorf("contract: catalog %s: %w", name, err)
	}
	return doc, nil
}

// StandardEventTypes decodes the embedded standard event catalog.
func StandardEventTypes() ([]TypeSpec, error) {
	doc, err := readCatalog("events.json")
	return doc.Types, err
}

// Register adds or replaces a type after validating it.
func (c *Catalog) Register(s TypeSpec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.types[s.Type] = s
	return nil
}

// Lookup returns the spec of a type.
func (c *Catalog) Lookup(eventType string) (TypeSpec, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.types[eventType]
	return s, ok
}

// Types returns all registered specs sorted by class then type.
func (c *Catalog) Types() []TypeSpec {
	c.mu.RLock()
	out := make([]TypeSpec, 0, len(c.types))
	for _, s := range c.types {
		out = append(out, s)
	}
	c.mu.RUnlock()
	order := map[Class]int{}
	for i, cl := range Classes {
		order[cl] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return order[out[i].Class] < order[out[j].Class]
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Route validates an event and decides its lane, session and the sessions
// it cancels. Unknown types are rejected: the loop never guesses a route.
func (c *Catalog) Route(e Event) (Admission, error) {
	if err := e.Validate(); err != nil {
		return Admission{}, err
	}
	spec, ok := c.Lookup(e.Type)
	if !ok {
		return Admission{}, fmt.Errorf("%w: %s", ErrUnknownType, e.Type)
	}
	if e.Class != "" && e.Class != spec.Class {
		return Admission{}, fmt.Errorf("%w: class %q does not match %q for %s", ErrInvalidEvent, e.Class, spec.Class, e.Type)
	}
	if e.Mode != "" && e.Mode != spec.Mode {
		return Admission{}, fmt.Errorf("%w: mode %q does not match %q for %s", ErrInvalidEvent, e.Mode, spec.Mode, e.Type)
	}
	e.Class, e.Mode = spec.Class, spec.Mode
	if spec.RequiresScene && strings.TrimSpace(e.Scene.ID) == "" {
		return Admission{}, fmt.Errorf("%w: %s", ErrUnknownScene, e.Type)
	}
	if spec.Lane == LaneForeground && e.Actor.Kind != ActorHuman {
		return Admission{}, fmt.Errorf("%w: foreground events need a human actor", ErrInvalidEvent)
	}
	adm := Admission{Event: e, Spec: spec}
	if spec.Lane != LaneHost {
		key := ""
		if spec.SessionKey != "" {
			key = e.Ref(spec.SessionKey)
			if key == "" {
				return Admission{}, fmt.Errorf("%w: %s needs ref %q", ErrInvalidEvent, e.Type, spec.SessionKey)
			}
		}
		adm.Session = &SessionRef{Scene: e.Scene.ID, Kind: spec.Session, Key: key}
	}
	if spec.Preempts && e.Scene.ID != "" {
		for _, kind := range c.preemptibleKinds() {
			adm.Cancel = append(adm.Cancel, SessionCancel{Scene: e.Scene.ID, Kind: kind, OnlyPreemp: true, Reason: CancelForegroundAdmission})
		}
	}
	for _, kind := range spec.Revokes {
		adm.Cancel = append(adm.Cancel, SessionCancel{Scene: e.Scene.ID, Kind: kind, Reason: CancelRevoked})
	}
	return adm, nil
}

func (c *Catalog) preemptibleKinds() []SessionKind {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[SessionKind]bool{}
	var kinds []SessionKind
	for _, s := range c.types {
		if s.Preemptible && !seen[s.Session] {
			seen[s.Session] = true
			kinds = append(kinds, s.Session)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}
