package core

import "context"

// Session is an optional durable coding/agent session backend (e.g. local OpenCode).
// Core never shells out; providers/session/opencode implements this.
type Session interface {
	// Ensure returns (or creates) a session id for the given scene key.
	Ensure(ctx context.Context, scene, title string) (sessionID string, err error)
	// Run sends a prompt in a new or existing session and returns the assistant text.
	Run(ctx context.Context, sessionID, prompt string) (output string, err error)
	// Resume continues an existing session with a new prompt.
	Resume(ctx context.Context, sessionID, prompt string) (output string, err error)
}
