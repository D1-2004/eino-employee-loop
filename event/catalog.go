package event

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed events.json
var standardJSON []byte

// Coalesce describes how a burst of one type collapses into one window: the
// window closes QuietMs after its last item, MaxWaitMs after its first item,
// or when it holds MaxItems, whichever comes first.
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
	// Session is required for the foreground and background lanes.
	Session SessionKind `json:"session,omitempty"`
	// SessionKey names the Refs key that partitions the session ("task" for
	// follow-ups). Empty means one session of this kind per scene.
	SessionKey string `json:"session_key,omitempty"`
	// RequiresScene rejects events without a scene; only employee-wide
	// enablement events clear it.
	RequiresScene bool `json:"requires_scene"`
	// Preemptible windows are cancelled, not resumed, when the scene admits
	// new human input for the foreground.
	Preemptible bool `json:"preemptible,omitempty"`
	// Preempts makes this type cancel the scene's preemptible windows.
	Preempts bool `json:"preempts,omitempty"`
	// Revokes lists session kinds whose windows become invalid.
	Revokes  []SessionKind `json:"revokes,omitempty"`
	Coalesce *Coalesce     `json:"coalesce,omitempty"`
	// Summary is a one-line description per BCP 47 locale; "en" is required.
	Summary map[string]string `json:"summary"`
}

// Validate enforces the routing invariants of the scene model:
//   - the foreground only receives triggered business events;
//   - listening events never enter the foreground;
//   - host-lane types have no session, the other lanes always have one.
func (s TypeSpec) Validate() error {
	var problems []string
	if strings.TrimSpace(s.Type) == "" {
		problems = append(problems, "type is required")
	}
	switch s.Class {
	case Business, Execution, Completion, Schedule, Enablement:
	default:
		problems = append(problems, fmt.Sprintf("unknown class %q", s.Class))
	}
	switch s.Mode {
	case Triggered, Listening:
	default:
		problems = append(problems, fmt.Sprintf("unknown mode %q", s.Mode))
	}
	switch s.Lane {
	case Foreground:
		if s.Mode != Triggered || s.Class != Business {
			problems = append(problems, "the foreground accepts only triggered business events")
		}
		if s.Session != Interactive {
			problems = append(problems, "the foreground uses the interactive session")
		}
		if s.Preemptible {
			problems = append(problems, "foreground windows are not preemptible")
		}
	case Background:
		if s.Session == "" || s.Session == Interactive {
			problems = append(problems, "the background requires a background session kind")
		}
	case Host:
		if s.Session != "" || s.SessionKey != "" {
			problems = append(problems, "the host lane has no session")
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown lane %q", s.Lane))
	}
	if s.Lane != Host && !s.RequiresScene {
		problems = append(problems, "session lanes require a scene")
	}
	if c := s.Coalesce; c != nil && (c.MaxItems <= 0 || c.QuietMs < 0 || c.MaxWaitMs < c.QuietMs) {
		problems = append(problems, "coalesce requires max_items > 0 and 0 <= quiet_ms <= max_wait_ms")
	}
	if strings.TrimSpace(s.Summary["en"]) == "" {
		problems = append(problems, "summary.en is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("event: type %q: %s", s.Type, strings.Join(problems, "; "))
	}
	return nil
}

// Describe returns the summary in locale, falling back to its base language
// and then to English.
func (s TypeSpec) Describe(locale string) string {
	if text := s.Summary[locale]; text != "" {
		return text
	}
	if base, _, ok := strings.Cut(locale, "-"); ok {
		for tag, text := range s.Summary {
			if strings.HasPrefix(tag, base) && text != "" {
				return text
			}
		}
	}
	return s.Summary["en"]
}

// Session identifies one session of a scene.
type Session struct {
	Scene string      `json:"scene"`
	Kind  SessionKind `json:"kind"`
	Key   string      `json:"key,omitempty"`
}

// String renders a stable key such as "scene-1/follow_up/task-9".
func (s Session) String() string {
	if s.Key == "" {
		return s.Scene + "/" + string(s.Kind)
	}
	return s.Scene + "/" + string(s.Kind) + "/" + s.Key
}

// Cancel asks the Host to cancel the open windows of matching sessions in
// the same transaction as the admission. An empty Scene matches every scene
// of the employee; Preemptible limits it to preemptible windows.
type Cancel struct {
	Scene       string      `json:"scene,omitempty"`
	Kind        SessionKind `json:"kind"`
	Preemptible bool        `json:"preemptible,omitempty"`
	Reason      string      `json:"reason"`
}

// Cancellation reasons.
const (
	CancelPreempted = "preempted"
	CancelRevoked   = "revoked"
)

// Admission is the routing decision for one event.
type Admission struct {
	Event Event    `json:"event"`
	Spec  TypeSpec `json:"spec"`
	// Session is nil for host-lane events.
	Session *Session `json:"session,omitempty"`
	Cancel  []Cancel `json:"cancel,omitempty"`
}

// Catalog is a registry of event types, safe for concurrent use.
type Catalog struct {
	mu    sync.RWMutex
	types map[string]TypeSpec
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{types: map[string]TypeSpec{}} }

// Standard returns a catalog preloaded with the standard types (events.json).
// Hosts Register their own types on top.
func Standard() *Catalog {
	var doc struct {
		Version int        `json:"version"`
		Types   []TypeSpec `json:"types"`
	}
	dec := json.NewDecoder(strings.NewReader(string(standardJSON)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		panic(fmt.Errorf("event: events.json: %w", err))
	}
	c := NewCatalog()
	for _, s := range doc.Types {
		if err := c.Register(s); err != nil {
			panic(err)
		}
	}
	return c
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

// Types returns every spec sorted by class, then type.
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

// Route validates an event and decides its lane, its session and the
// sessions it cancels. Unknown types are rejected: a route is never guessed.
func (c *Catalog) Route(e Event) (Admission, error) {
	if err := e.Validate(); err != nil {
		return Admission{}, err
	}
	spec, ok := c.Lookup(e.Type)
	if !ok {
		return Admission{}, fmt.Errorf("%w: %s", ErrUnknownType, e.Type)
	}
	if (e.Class != "" && e.Class != spec.Class) || (e.Mode != "" && e.Mode != spec.Mode) {
		return Admission{}, fmt.Errorf("%w: %s is %s/%s, not %s/%s", ErrInvalid, e.Type, spec.Class, spec.Mode, e.Class, e.Mode)
	}
	e.Class, e.Mode = spec.Class, spec.Mode
	if spec.RequiresScene && strings.TrimSpace(e.Scene.ID) == "" {
		return Admission{}, fmt.Errorf("%w: %s", ErrMissingScene, e.Type)
	}
	if spec.Lane == Foreground && e.Actor.Kind != Human {
		return Admission{}, fmt.Errorf("%w: foreground events need a human actor", ErrInvalid)
	}
	adm := Admission{Event: e, Spec: spec}
	if spec.Lane != Host {
		key := ""
		if spec.SessionKey != "" {
			if key = e.Ref(spec.SessionKey); key == "" {
				return Admission{}, fmt.Errorf("%w: %s needs ref %q", ErrInvalid, e.Type, spec.SessionKey)
			}
		}
		adm.Session = &Session{Scene: e.Scene.ID, Kind: spec.Session, Key: key}
	}
	if spec.Preempts && e.Scene.ID != "" {
		for _, kind := range c.preemptibleKinds() {
			adm.Cancel = append(adm.Cancel, Cancel{Scene: e.Scene.ID, Kind: kind, Preemptible: true, Reason: CancelPreempted})
		}
	}
	for _, kind := range spec.Revokes {
		adm.Cancel = append(adm.Cancel, Cancel{Scene: e.Scene.ID, Kind: kind, Reason: CancelRevoked})
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
