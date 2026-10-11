// Package event classifies the inputs of an employee and routes them to a
// lane and a session of their scene.
//
// Every input is an Event. Its type selects a TypeSpec in a Catalog, which
// fixes two independent facts:
//
//   - Class: what happened (business, execution, completion, schedule,
//     enablement);
//   - Mode: why the employee received it (triggered: addressed to it or about
//     its own work; listening: it subscribed, observes or keeps time).
//
// Routing then picks the lane: the scene's single foreground interactive
// session for triggered business events from people, a queued background
// session (observation, follow_up, routine, lifecycle) for everything that
// needs a decision without taking the interactive turn, or the host lane for
// facts the Host applies without waking a model.
//
// The package only decides; queues, storage and delivery belong to the Host.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Class answers "what happened".
type Class string

const (
	// Business events happen in a scene: messages, reactions, answers,
	// membership changes and provider resources (documents, calendars,
	// approvals).
	Business Class = "business"
	// Execution events report work in flight: accepted, started, progress,
	// steered. They never claim that the work finished.
	Execution Class = "execution"
	// Completion events report terminal facts: a run's result, its terminal
	// state.
	Completion Class = "completion"
	// Schedule events come from time: routine occurrences, observation windows
	// becoming due, reminders.
	Schedule Class = "schedule"
	// Enablement events change what the employee may do: enabled, disabled,
	// bound to a scene, a subscription or policy revised.
	Enablement Class = "enablement"
)

// Classes lists the classes in documentation order.
var Classes = []Class{Business, Execution, Completion, Schedule, Enablement}

// Mode answers "why did the employee receive this event".
type Mode string

const (
	// Triggered (passive) events are addressed to the employee or report on
	// its own work: a mention, a direct message, a reply to its message, an
	// answer to its question, its task finishing.
	Triggered Mode = "triggered"
	// Listening (active) events arrive because the employee subscribed,
	// observes or keeps time: unaddressed group messages, provider feeds,
	// routine occurrences, observation windows.
	Listening Mode = "listening"
)

// Lane is where an admitted event is processed.
type Lane string

const (
	// Foreground is the scene's single interactive session. Only triggered
	// business events from people enter it; human input always goes first.
	Foreground Lane = "foreground"
	// Background holds queued sessions that never take the interactive turn.
	Background Lane = "background"
	// Host events are applied by the Host without waking a model.
	Host Lane = "host"
)

// SessionKind names a session within a scene.
type SessionKind string

const (
	// Interactive is the foreground session; one per scene.
	Interactive SessionKind = "interactive"
	// Observation listens to scene events nobody addressed to the employee;
	// one per scene, its windows are preemptible.
	Observation SessionKind = "observation"
	// FollowUp handles the execution and completion reports of one task,
	// keyed by the task reference.
	FollowUp SessionKind = "follow_up"
	// Routine handles the occurrences of one routine, keyed by routine.
	Routine SessionKind = "routine"
	// Lifecycle handles enablement events that need a decision, such as
	// introducing the employee in a newly bound scene.
	Lifecycle SessionKind = "lifecycle"
)

// SceneKind is the kind of a scene. Hosts may define more; a kind is never
// guessed.
type SceneKind string

const (
	Group      SceneKind = "group"
	Direct     SceneKind = "dm"
	Enterprise SceneKind = "enterprise"
)

// SceneRef identifies the scene of an event. ID is minted by the Host and is
// the only key of per-scene state; Locator is the provider identity (for
// example a conversation id), used for display and provider calls only.
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
	Human       ActorKind = "human"
	Employee    ActorKind = "employee"
	System      ActorKind = "system"
	Integration ActorKind = "integration"
)

// Actor comes from authenticated Host data, never from message text.
type Actor struct {
	ID          string    `json:"id"`
	Kind        ActorKind `json:"kind"`
	DisplayName string    `json:"display_name,omitempty"`
}

// Event is the single envelope every input arrives in.
type Event struct {
	// ID is stable per producer: replaying the same (Source, ID) carries the
	// same content (see Fingerprint).
	ID string `json:"id"`
	// Source is the producer namespace, for example "chat.im".
	Source string `json:"source"`
	// Employee addresses the employee that receives the event when a producer
	// serves several employees.
	Employee string `json:"employee,omitempty"`
	// Type selects the TypeSpec, for example "message.addressed".
	Type string `json:"type"`
	// Class and Mode are filled from the TypeSpec by Route; a producer value
	// that disagrees with the catalog is rejected.
	Class      Class     `json:"class,omitempty"`
	Mode       Mode      `json:"mode,omitempty"`
	Scene      SceneRef  `json:"scene"`
	Actor      Actor     `json:"actor"`
	OccurredAt time.Time `json:"occurred_at"`
	// Locale is the BCP 47 tag of human content when known.
	Locale string `json:"locale,omitempty"`
	// Refs are correlation references ("task", "run", "routine", "reply_to").
	Refs map[string]string `json:"refs,omitempty"`
	// Text is the human-visible content, if any.
	Text string `json:"text,omitempty"`
	// Payload is producer-specific data.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Well-known reference keys.
const (
	RefTask    = "task"
	RefRun     = "run"
	RefRoutine = "routine"
	RefReplyTo = "reply_to"
)

// Errors returned by validation and routing.
var (
	ErrInvalid      = errors.New("event: invalid event")
	ErrUnknownType  = errors.New("event: unknown event type")
	ErrMissingScene = errors.New("event: event has no scene")
)

// Validate checks producer-independent envelope invariants;
// schema/event.schema.json states the same rules for the JSON encoding and
// leaves the edges its description lists to Validate.
// Scene and actor may be empty here: Route checks them against the type.
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
	switch e.Actor.Kind {
	case "", Human, Employee, System, Integration:
	default:
		problems = append(problems, fmt.Sprintf("unknown actor kind %q", e.Actor.Kind))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	return nil
}

// Ref returns a correlation reference or "".
func (e Event) Ref(key string) string { return e.Refs[key] }

// Fingerprint is a stable digest of the content, ignoring the derived Class
// and Mode, so a Host can tell an idempotent replay from a conflicting reuse
// of the same (Source, ID).
func (e Event) Fingerprint() string {
	e.Class, e.Mode = "", ""
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
