package dws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/contract"
)

// Provider is the SceneRef.Provider of DingTalk scenes.
const Provider = "dingtalk"

// DefaultSource is the event source namespace of normalized messages.
const DefaultSource = "dingtalk.im"

// Normalization errors.
var (
	// ErrNoSender marks a message without a provider sender id: the loop
	// keys requesters by authenticated id, never by display name.
	ErrNoSender = errors.New("dws: no_sender_id")
	// ErrNoMessageID marks a record without a provider id to dedupe on.
	ErrNoMessageID = errors.New("dws: no_message_id")
	// ErrRawSceneID marks a resolver that returned no scene id or the raw
	// conversation id.
	ErrRawSceneID = errors.New("dws: scene_id_not_minted")
)

// Conversation is what a SceneResolver sees of a provider conversation.
type Conversation struct {
	ID     string
	Single bool
}

// SceneResolver mints the scene of a conversation. Scene ids are the only
// key of per-scene loop state; the conversation id travels as the opaque
// SceneRef.Locator.
type SceneResolver interface {
	ResolveScene(ctx context.Context, conv Conversation) (contract.SceneRef, error)
}

// HashSceneResolver mints stable opaque scene ids from a namespace and the
// conversation id. Use a namespace per deployment (or tenant) so scene ids
// never collide across deployments.
type HashSceneResolver struct {
	Namespace string
	Tenant    string
}

// ResolveScene implements SceneResolver.
func (r HashSceneResolver) ResolveScene(_ context.Context, conv Conversation) (contract.SceneRef, error) {
	sum := sha256.Sum256([]byte(r.Namespace + "\x00" + Provider + "\x00" + conv.ID))
	kind := contract.SceneGroup
	if conv.Single {
		kind = contract.SceneDirect
	}
	return contract.SceneRef{
		ID: "scn_" + hex.EncodeToString(sum[:12]), Kind: kind, Tenant: r.Tenant,
		Provider: Provider, Locator: conv.ID,
	}, nil
}

// Identity is the employee's own provider identity.
type Identity struct {
	IDs   []string
	Names []string
}

// HasID reports whether id is one of the employee's ids.
func (s Identity) HasID(id string) bool {
	if id == "" {
		return false
	}
	for _, x := range s.IDs {
		if x == id {
			return true
		}
	}
	return false
}

// Is reports whether a sender is the employee (by id, else by name).
func (s Identity) Is(id, name string) bool {
	if s.HasID(id) {
		return true
	}
	for _, n := range s.Names {
		if n != "" && n == name {
			return true
		}
	}
	return false
}

// mentionedInText reports an "@Name" mention of the employee in text, for
// providers that do not report mention ids.
func (s Identity) mentionedInText(text string) bool {
	for _, n := range s.Names {
		if n != "" && strings.Contains(text, "@"+n) {
			return true
		}
	}
	return false
}

// RecentIDs is a bounded, concurrency-safe set of provider message ids. The
// Deliverer records the messages it sent; the Normalizer consults it to
// recognise replies and reactions to the employee's messages.
type RecentIDs struct {
	mu    sync.Mutex
	limit int
	order []string
	set   map[string]struct{}
}

// NewRecentIDs returns a set that keeps the last limit ids (default 512).
func NewRecentIDs(limit int) *RecentIDs {
	if limit <= 0 {
		limit = 512
	}
	return &RecentIDs{limit: limit, set: map[string]struct{}{}}
}

// Add records an id.
func (r *RecentIDs) Add(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.set[id]; ok {
		return
	}
	r.set[id] = struct{}{}
	r.order = append(r.order, id)
	for len(r.order) > r.limit {
		delete(r.set, r.order[0])
		r.order = r.order[1:]
	}
}

// Has reports whether an id was recorded.
func (r *RecentIDs) Has(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.set[id]
	return ok
}

// Normalizer turns provider Messages into contract Events. It holds no
// provider connection and is safe for concurrent use.
type Normalizer struct {
	// Scenes mints scene refs. Required.
	Scenes SceneResolver
	// Self is the employee's provider identity.
	Self Identity
	// Own lists messages the employee sent (see Deliverer.Own).
	Own *RecentIDs
	// Negative classifies reactions; nil uses DefaultLexicon().Negative
	// (the embedded reactions.json). Pass NewLexicon(cfg).Negative to use
	// another reaction policy.
	Negative func(name, text string) bool
	// Employee addresses the events (contract.Event.Employee); empty leaves
	// them addressed to whichever loop receives them.
	Employee string
	// Source is the event source; empty uses DefaultSource.
	Source string
	// Locale is the content locale when known (BCP 47).
	Locale string
	// Now stamps records without a time; nil uses time.Now.
	Now func() time.Time
}

// Normalize maps one Message to an Event. ok is false when the message is
// not an event for the loop: the employee's own messages, empty text,
// non-negative reactions and reactions to other people's messages.
//
//   - a direct message, an @-mention of the employee or a reply to the
//     employee's message becomes message.addressed (Refs reply_to for
//     replies);
//   - any other group message becomes message.observed;
//   - a negative reaction to the employee's message becomes reaction.added
//     (Refs reply_to is the reacted message, Text the reaction).
func (n *Normalizer) Normalize(ctx context.Context, m Message) (contract.Event, bool, error) {
	if n.Self.Is(m.SenderID, m.SenderName) {
		return contract.Event{}, false, nil
	}
	var typ string
	refs := map[string]string{}
	switch m.Kind {
	case MessageReaction:
		if !n.ownMessage(m.TargetMessageID) || !n.negative(m.Reaction, m.Text) {
			return contract.Event{}, false, nil
		}
		typ = contract.TypeReactionAdded
		refs[contract.RefReplyTo] = m.TargetMessageID
	case MessageText, "":
		if strings.TrimSpace(m.Text) == "" {
			return contract.Event{}, false, nil
		}
		typ = contract.TypeMessageObserved
		if n.addressed(m) {
			typ = contract.TypeMessageAddressed
		}
		if m.ReplyToMessageID != "" {
			refs[contract.RefReplyTo] = m.ReplyToMessageID
		}
	default:
		return contract.Event{}, false, nil
	}
	if m.MessageID == "" {
		return contract.Event{}, false, ErrNoMessageID
	}
	if m.SenderID == "" {
		return contract.Event{}, false, ErrNoSender
	}
	if n.Scenes == nil {
		return contract.Event{}, false, fmt.Errorf("%w: no resolver", ErrRawSceneID)
	}
	scene, err := n.Scenes.ResolveScene(ctx, Conversation{ID: m.ConversationID, Single: m.Single})
	if err != nil {
		return contract.Event{}, false, err
	}
	if strings.TrimSpace(scene.ID) == "" || scene.ID == m.ConversationID {
		return contract.Event{}, false, ErrRawSceneID
	}
	if scene.Locator == "" {
		scene.Locator = m.ConversationID
	}
	if scene.Provider == "" {
		scene.Provider = Provider
	}
	text := m.Text
	if m.Kind == MessageReaction {
		text = m.Reaction
	}
	at := time.UnixMilli(m.AtMs).UTC()
	if m.AtMs <= 0 {
		at = n.now().UTC()
	}
	source := n.Source
	if source == "" {
		source = DefaultSource
	}
	e := contract.Event{
		ID: m.MessageID, Source: source, Employee: n.Employee, Type: typ, Scene: scene,
		Actor:      contract.Actor{ID: m.SenderID, Kind: contract.ActorHuman, DisplayName: m.SenderName},
		OccurredAt: at, Locale: n.Locale, Text: text,
	}
	if len(refs) > 0 {
		e.Refs = refs
	}
	return e, true, nil
}

func (n *Normalizer) addressed(m Message) bool {
	if m.Single || m.MentionsSelf || n.Self.mentionedInText(m.Text) {
		return true
	}
	for _, id := range m.MentionIDs {
		if n.Self.HasID(id) {
			return true
		}
	}
	return n.Self.HasID(m.ReplyToSenderID) || n.ownMessage(m.ReplyToMessageID)
}

func (n *Normalizer) ownMessage(id string) bool { return n.Own.Has(id) }

func (n *Normalizer) negative(name, text string) bool {
	if n.Negative != nil {
		return n.Negative(name, text)
	}
	return DefaultLexicon().Negative(name, text)
}

func (n *Normalizer) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}
