# Chat concurrency

Chat serializes mutable inbox execution within one configured scene. Each scene
owns one mutex; the lock spans its native model invocation, checkpoint result and
job settlement. Other scenes never wait on that mutex. Inbox admission remains a
short storage transaction and does not acquire an execution mutex.

`StepScene(ctx, scene)` processes at most one eligible FIFO job in that scene. It
returns no progress when another worker already owns the scene. `Step` preserves
the global admission ordering when idle, skips busy scenes, and reselects the
scene's oldest eligible job after acquiring its mutex. Reselecting prevents a
cancelled or resumed earlier job from being overtaken by a stale list snapshot.
Native identity, frozen profile/frame and effect idempotency remain unchanged.

Entry.Start installs one existing worker loop per configured scene, so separate
scenes can actually run concurrently. Task, delivery, timing and Owner loops
retain their current ownership. RunOnce and Drain remain bounded sequential
callers of the same scene steps; they skip a busy scene rather than introducing a
second execution of it. The existing 100ms idle poll remains unchanged.

Resume and explicit Retry resolve the persisted scene, acquire only its mutex,
and reload the job under that lock. A busy same-scene operation returns
`storage.ErrBusy` for explicit retry, rather than holding a control request behind
a model call. They continue to validate the frozen identity, source permissions,
exact native target and current state. Different-scene control is independent.
Close uses the existing worker cancellation and wait; it does not close stores
until all scene workers have settled.
