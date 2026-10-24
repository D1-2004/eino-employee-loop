# Model composition

Each immutable Employee Profile may bind exact loop roles (`chat`, `executor`,
`review`, `task_attention`, `expression`, `package`, `owner_<role>`) to a
`ModelBinding`: Provider name, Provider version, model name and optional effort.
The binding is a model factory configuration, not a new business loop.
Extension-defined sub-loops may bind their own exact roles. Wildcards and
whitespace are rejected; unmatched roles keep the explicit legacy fallback.

`ModelName` remains the legacy fallback for roles without a binding. Old JSON
profiles and their journal/checkpoint identities remain readable. Explicit role
bindings are part of the persisted Profile revision; resume selects from the
admitted Profile, not current admission settings or an environment model name.
Every copied Profile owns its binding map. Credentials and endpoint configuration
stay in the application Provider and never enter the Profile or journal.

`contract.Models` remains the host injection seam. The reference application
provides an exact-version factory registry; registration declares supported
efforts and rejects duplicates, missing versions and unsupported efforts before
model I/O. The OpenAI-compatible Provider passes the frozen model and effort to
Eino's pinned `openai@v0.1.13` ChatModelConfig.ReasoningEffort (empty, low, medium,
high). Endpoint model capability is still the host's responsibility; other
Providers may declare another exact effort set. No ambient effort override is
accepted. The selected identity participates in physical model call journal keys,
including Owner jobs. Changing effort cannot replay a response from another
binding. A host-supplied Models function must honor its resolved request.

## Reference assembly

```go
profile.ModelBindings = map[string]entry.ModelBinding{
    "chat": {Provider: "openai-compatible", Version: "1", Model: "chat-model", Effort: "low"},
    "executor": {Provider: "openai-compatible", Version: "1", Model: "reasoning-model", Effort: "high"},
    "review": {Provider: "openai-compatible", Version: "1", Model: "reasoning-model", Effort: "medium"},
    "owner_memory": {Provider: "openai-compatible", Version: "1", Model: "memory-model", Effort: "low"},
}
providers := models.OpenAIEnvironment()
// A second Provider can register its own exact version and effort declaration.
// providers.Register(models.Ref{Name: "other", Version: "2"}, []string{"", "max"}, factory)
// Pass providers.Models as entry.Options.Models. Credentials remain in the host.
```

When a new revision changes a binding, old queued work still needs its old
Provider version registered. Removing that factory fails closed before I/O.
The physical journal call ID is SHA-256 of the canonical request including the
full model binding identity; rows also record that identity for inspection.
Native Eino checkpoints and old legacy identities keep their existing versions.
