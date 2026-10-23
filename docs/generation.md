# Structured generation workflows

`generation.Run` uses an Eino graph for a bounded
**prepare → model → validate → journal → retry/accept** workflow. Use it for
review, attention, reflection, memory extraction and expression stages where a
model proposes structured data and the Host applies the accepted result later.
Use `loop` for autonomous native-tool interactions instead.

The four typed Providers keep business types and permissions in the assembly:

- Context Provider assembles frozen evidence, checks current access, renews leases
  and reserves a physical attempt **before** returning its prepared input.
- Model Provider performs one request (or returns its exact journaled response).
- Decision Provider validates structure and business evidence. A valid proposal is
  not proof of an action, delivery or a successful business commit.
- Journal Provider saves the response/diagnostic under the original attempt and
  settles reservations before another preparation starts. Its failure is fatal.

An attempt retains preparation, model and validation errors separately. A retry
policy can skip an unavailable model candidate without treating a Host storage or
permission error as retryable. `MaxAttempts` bounds this invocation; durable
providers must additionally enforce their original persisted budget, so restarting
a process never resets the allowance. No Provider is implicitly optional.

The graph scheduler survives request cancellation only to reach the journal node.
Each Provider receives the original request context; a journal may perform a
bounded independent write to retain a known committed result. No new preparation
or inference starts after cancellation. Unknown commit outcomes are recovered
through the Host's journal and idempotency keys, not by re-running generation.

The graph is stateless across invocations. Existing durable stores remain the
source of truth; an assembly returns a previously accepted checkpoint directly
instead of maintaining a second set of Eino checkpoints. Each business Provider
still needs separate local real-model and write/readback acceptance evidence.

The opt-in `TestOwnModelConsumerGenerationFixture` loads a consumer-exported
synthetic fixture (`Messages`, frozen `Tools`, `ExpectedTool`) through
`EEL_GENERATION_FIXTURE`, calls the owner's configured model, validates native
tool arguments and saves the original response at `EEL_GENERATION_RESULT`.
The consumer must then run its actual parser and apply/readback checks against
the same synthetic evidence. Structural success does not certify business
completion, actual remote tools or delivery. The test never connects to IM.

Fixtures may supply `Mode=json`, an output `JSONSchema`, and frozen
`RequestFields` (tool choice, parallel calls, token/thinking controls). The test
bridge preserves these fields in the final serialized request. In particular,
the pinned Eino OpenAI adapter otherwise converts a forced single-tool choice to
a named tool, which differs from a Host that froze literal `required`. Validation
continues to check the original finish reason; it never rewrites `stop` into
`tool_calls` to make an incompatible response pass.
