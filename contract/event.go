package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Class answers "what happened". Every event type has exactly one class.
type Class string

const (
	// ClassBusiness covers scene business events: messages, reactions, card
	// actions, membership changes and provider resources such as documents,
	// calendars or approvals.
	ClassBusiness Class = "business"
	// ClassExecution covers work in flight: accepted, started, progress,
	// steered. It never claims that the work finished.
	ClassExecution Class = "execution"
	// ClassCompletion covers terminal facts: a run succeeded, failed or was
	// cancelled (and, in a platform's extension types, for example a
	// collection closed or a plan step finished).
	ClassCompletion Class = "completion"
	// ClassSchedule covers time: routine occurrences, observation windows
	// becoming due, reminders and deadlines.
	ClassSchedule Class = "schedule"
	// ClassEnablement covers capability and lifecycle changes: the employee
	// was enabled or disabled, it was bound to a scene, a listening
	// subscription opened or closed, a policy was revised.
	ClassEnablement Class = "enablement"
)

// Classes lists the classes in documentation order.
var Classes = []Class{ClassBusiness, ClassExecution, ClassCompletion, ClassSchedule, ClassEnablement}

// Mode answers "why did the employee receive this event".
type Mode string

const (
	// ModeTriggered (passive) events are addressed to the employee or report
	// on the employee's own work: an @-mention, a direct message, a reply to
	// its message, an answer to its question, its task finishing.
	ModeTriggered Mode = "triggered"
	// ModeListening (active) events arrive because the employee subscribed,
	// observes or keeps time: unaddressed group messages, provider feeds,
	// routine occurrences, observation windows.
	ModeListening Mode = "listening"
)

// Lane is where an admitted event is processed.
type Lane string

const (
	// LaneForeground is the scene's single interactive session. Only
	// triggered business events from people enter it; it is serialized and
	// human input always goes first.
	LaneForeground Lane = "foreground"
	// LaneBackground holds queued sessions that never take the scene's
	// interactive turn: observation, follow-up of a task, routines, lifecycle.
	LaneBackground Lane = "background"
	// LaneHost events are recorded and applied by the Host without waking a
	// model (receipts, terminal facts, configuration revisions).
	LaneHost Lane = "host"
)

// SessionKind names a session within a scene.
type SessionKind string

const (
	// SessionInteractive is the foreground session. One per scene.
	SessionInteractive SessionKind = "interactive"
	// SessionObservation listens to scene business events nobody addressed
	// to the employee. One per scene; its windows are preemptible.
	SessionObservation SessionKind = "observation"
	// SessionFollowUp handles execution and completion reports of one task.
	// Keyed by the task reference.
	SessionFollowUp SessionKind = "follow_up"
	// SessionRoutine handles occurrences of one routine. Keyed by routine.
	SessionRoutine SessionKind = "routine"
	// SessionLifecycle handles enablement events that need a decision, such
	// as introducing the employee after it was bound to a new scene.
	SessionLifecycle SessionKind = "lifecycle"
)

// SceneKind is the kind of work locus. Hosts may define additional kinds;
// the loop never guesses a kind.
type SceneKind string

const (
	SceneGroup      SceneKind = "group"
	SceneDirect     SceneKind = "dm"
	SceneEnterprise SceneKind = "enterprise"
)

// SceneRef identifies the scene an event belongs to. ID is minted by the
// Host and is the only key used for per-scene state. Locator is the opaque
// provider identity (for example a conversation id) used for display and
// provider calls, never as a state key.
type SceneRef struct {
	ID       string    `json:"id"`
	Kind     SceneKind `json:"kind"`
	Tenant   string    `json:"tenant,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Locator  string    `json:"locator,omitempty"`
}

// ActorKind classifies who produced an event.
type ActorKind string

const (
	ActorHuman       ActorKind = "human"
	ActorEmployee    ActorKind = "employee"
	ActorSystem      ActorKind = "system"
	ActorIntegration ActorKind = "integration"
)

// Actor is supplied by the Host from authenticated data, never decoded from
// model output or message text. DisplayName is data, not identity.
type Actor struct {
	ID          string    `json:"id"`
	Kind        ActorKind `json:"kind"`
	DisplayName string    `json:"display_name,omitempty"`
}

// Event is the single envelope every input arrives in.
type Event struct {
	// ID is stable per producer: replaying the same (Source, ID) must carry
	// the same content (see EventLog.Admit).
	ID string `json:"id"`
	// Source is the producer namespace, for example "chat.im" or
	// "task-manager".
	Source string `json:"source"`
	// Employee addresses the employee whose loop receives the event. A
	// producer that serves several employees (a shared Task Manager, a
	// multi-employee gateway) must set it so the event can be routed
	// (SinkResolver). A loop treats an empty value as addressed to itself
	// and rejects another employee's event with ErrUnknownEmployee.
	Employee string `json:"employee,omitempty"`
	// Type selects the TypeSpec in a Catalog, for example "message.addressed".
	Type string `json:"type"`
	// Class and Mode are filled from the TypeSpec on admission. A producer
	// may set them; a mismatch with the catalog is rejected.
	Class Class `json:"class,omitempty"`
	Mode  Mode  `json:"mode,omitempty"`

	Scene      SceneRef  `json:"scene"`
	Actor      Actor     `json:"actor"`
	OccurredAt time.Time `json:"occurred_at"`
	// Locale is the BCP 47 tag of human content when the producer knows it.
	Locale string `json:"locale,omitempty"`
	// Refs carries correlation references: "task", "run", "routine",
	// "reply_to", "window", ... Keys are documented per type.
	Refs map[string]string `json:"refs,omitempty"`
	// Text is the human-visible content when the event carries any.
	Text string `json:"text,omitempty"`
	// Payload is producer-specific data validated by PayloadSchema.
	PayloadSchema string          `json:"payload_schema,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// Errors returned by event validation and routing.
var (
	ErrInvalidEvent = errors.New("contract: invalid event")
	ErrUnknownType  = errors.New("contract: unknown event type")
	ErrUnknownScene = errors.New("contract: event has no scene")
)

// Validate checks producer-independent envelope invariants.
func (e Event) Validate() error {
	var problems []string
	if strings.TrimSpace(e.ID) == "" {
		problems = append(problems, "id is required")
	}
	if strings.TrimSpace(e.Source) == "" {
		problems = append(problems, "source is required")
	}
	if strings.TrimSpace(e.Type) == "" {
		problems = append(problems, "type is required")
	}
	if e.OccurredAt.IsZero() {
		problems = append(problems, "occurred_at is required")
	}
	if e.Actor.Kind != "" && !validActorKind(e.Actor.Kind) {
		problems = append(problems, fmt.Sprintf("unknown actor kind %q", e.Actor.Kind))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidEvent, strings.Join(problems, "; "))
	}
	return nil
}

func validActorKind(k ActorKind) bool {
	switch k {
	case ActorHuman, ActorEmployee, ActorSystem, ActorIntegration:
		return true
	}
	return false
}

// Fingerprint is a stable digest of the event content used by EventLog to
// tell an idempotent replay from a conflicting reuse of the same id.
func (e Event) Fingerprint() string {
	cp := e
	cp.Class, cp.Mode = "", ""
	raw, _ := json.Marshal(cp)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Ref returns a correlation reference or "".
func (e Event) Ref(key string) string {
	if e.Refs == nil {
		return ""
	}
	return e.Refs[key]
}

// Well-known reference keys.
const (
	RefTask    = "task"
	RefRun     = "run"
	RefRoutine = "routine"
	RefReplyTo = "reply_to"
	RefWindow  = "window"
)

// Standard business event types produced by gateways (catalog/events.json).
const (
	// TypeMessageAddressed is a message addressed to the employee: a direct
	// message, an @-mention or a reply to the employee's message.
	TypeMessageAddressed = "message.addressed"
	// TypeMessageObserved is a scene message nobody addressed to the employee.
	TypeMessageObserved = "message.observed"
	// TypeReactionAdded is a reaction to the employee's message that the
	// gateway judged relevant (for example a negative one).
	TypeReactionAdded = "reaction.added"
)
