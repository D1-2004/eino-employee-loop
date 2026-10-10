package core

// Field is an abstract work locus — not bound to a DingTalk conversation or enterprise.
// An IM openConversationId is one adapter mapping into Field.ID; other adapters
// (ticket, doc, cron wake, voice call) use the same Core type.
type Field struct {
	ID       string // stable Host-minted id (e.g. scene key)
	Kind     string // conversation | enterprise | ticket | doc | custom | ...
	Title    string
	Locator  string // optional external locator (cid, ticket URL); opaque to Core
	Provider string // dingtalk | stdin | slack | ...; opaque to Core
}

// Actor is who (or what) triggered a wake. Never decoded from model text.
type Actor struct {
	ID      string // stable principal id
	Name    string // display name (data only)
	Kind    string // human | employee | system | webhook
	Channel string // optional transport hint
}

// Trigger classifies why the Host woke the loop.
type Trigger struct {
	Kind  string // user_message | reaction | task_result | follow_up | schedule | repair
	Ref   string // source_ref / message id / task id / reaction event id
	Field Field
	Actor Actor
	AtMs  int64
	Text  string // human-visible utterance when Kind=user_message; correction brief when reaction
	// Reaction is set when Kind=reaction (emoji name, e.g. "打叉").
	Reaction    string
	TargetMsgID string
}
