# Versioned workflows and multi-turn sessions

The `workflow` package composes **prepare → agent → checkpoint → commit** as an
Eino graph. The agent node runs this module's ADK `loop`, not an external loop
implementation. An assembly registers named, versioned Providers: foreground,
review, attention, reflection, memory, context, tone and expression can each
have their own messages, tools, model, budgets and business validator. Registering
a Provider grants no authority: a mandatory admission gate checks trusted scope
before storage or Provider access. Configuration selects names and versions;
it cannot load arbitrary code or make one background module depend on another.

## Provider boundary

- `Provider.Prepare` supplies the frozen `loop.Config`, Eino model, Host and
  messages for one wake. It receives prior typed conversation and wake records.
  Context, persona, tool schema and model routing belong here. Existing Host
  policies are injected through loop hooks; do not copy business permission rules
  into the graph.
- `Provider.Authorize` rechecks current access on every run, including replay and
  commit recovery. Stored history and model responses do not grant access.
- `Provider.Commit` applies a validated result using the wake's scoped receipt
  as its idempotency key. It returns a durable receipt after writing and reading
  back business state. For tools that already committed, it projects the existing
  receipts instead of repeating their effects. It must tolerate retry after an
  unknown commit; the workflow never guesses that an error means no effect.
- `Store.Acquire` supplies an exclusive session lease. The lease protects the
  complete generation and commit; cloud implementations must fence writes and
  effects with the current lease. `Lease.Save` is atomic and monotonic.
- Model journals and tool receipts remain Provider responsibilities. Each physical
  call is reserved before I/O. A crash during generation must replay the exact
  frozen request, not silently ask a changed model or repeat an effect.

## Identity, sessions and recovery

A key contains workspace, tenant, employee, scene, lane and subject. All fields
come from trusted admission. Foreground can use subject `interactive`; follow-up
uses the Task ID; routine uses its definition ID. Neither user text nor model
output supplies this key. Two scenes or tenants never share a transcript.

Each input has a unique receipt, kind, Provider version and frozen messages.
Reusing a receipt with different input is a conflict. A session is pinned to its
Provider kind/version; new versions use a new session or an explicit migration
outside this API. Unknown versions fail closed. Finished receipts return the
stored result after current authorization; no model or tool runs again.

The generation result is saved before `Commit`. If commit fails, retry resumes
that saved result without another generation. If generation itself fails, the
outcome (including committed tool effects) and error remain recorded. Retrying
that receipt returns the same failure; a deliberate new receipt can continue
after the Provider inspects and resolves prior failures. Failed transcripts are
not automatically appended to conversation: they may contain interrupted tool
batches. Successful history preserves typed assistant/tool pairs; system policy
is assembled afresh by the Provider. History truncation must preserve whole wakes.

`filestore` is a local reference Store with private files, atomic replacement and
an exclusive lock that is never automatically stolen. A process crash can leave
a lock; the operator must prove the old process has exited before removing it.
It is not a distributed lease and must not be used for cloud replicas. Durable
business effects still require an idempotent Host/model journal.

## Isolation and extension

The existing `loop.New(...).Run(...)` path and its default behavior are unchanged.
The new graph only runs when an assembly explicitly constructs a Registry, a
Store and a non-nil gate. No global registry or environment wildcard enables it.
Plugins register Providers under unique kind/version pairs. Existing sessions
continue to request their pinned version; missing plugins return an error.

For the Multica alignment, foreground tools use the existing frozen registry and
Host; Review/Goal, Attention, feedback reflection, memory, Context/Tone and
expression are independent Providers. Their actual schemas, prompts, source
checks and commit implementations need per-Provider acceptance evidence. Merely
registering their names or exercising a synthetic Provider does not certify that
the corresponding business loop has been ported.

Architecture references: [Eino ADK](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_preview/)
and [Runner/checkpoint](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/).
The fixed dependency's source is authoritative for executable APIs.

## Typed Host projections

`loop.Config.ResultData` may attach a JSON projection to `Outcome.HostData` after
the native wake has settled, including a failed wake with committed receipts.
The module treats this data as opaque: it cannot supply authority, alter the
native decision/receipts or replace validation. A projection error fails the wake
while retaining its core outcomes. Providers bind their projection to the exact
scope, input receipt and projection version, and reauthorize it before commit.

This lets an embedding Host preserve richer result types (card presentation,
progress identity and provider-native messages) through the generation/commit
checkpoint and process restart. It avoids reconstructing those fields from
assistant text or rerunning tools merely to recover metadata.

The Multica foreground adapter composes separate Context, Model, Tool and Commit
Providers. Context calls the same Host preparation/Turn rules as Embedded;
Model retains the durable journal and first-feedback decorator; tools execute
through the existing Host; commit receives the typed persisted projection.
Historical session records are input data only: Context must select currently
authorized history, and the adapter never implicitly merges private old tool
results into a new wake. Merely sharing a scene does not grant that access.

## Local verification

Run deterministic recovery/isolation checks with `go test -race ./workflow/...`.
The real-model check is opt-in and uses only local session files and local
artifact receipts; it sends no messages and deploys no server:

```sh
export LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=...
EEL_RUN_WORKFLOW_MODEL_CHECK=1 go test -tags=workflowintegration ./workflow \
  -run TestOwnModelMultiTurnSessionAcceptance -count=1 -v
```

It verifies recall after reconstructing the Runtime, cross-scene isolation and
receipt replay without another model call or artifact write. It does not prove
that business tools or all Multica Providers have been ported.

For native-loop alignment, optional `TransformResponse`, `BeforeTool` and
`CountBatch` hooks support derived native arguments, use-time refusal and
standalone protocol repair. `CheckContextAfterTool` preserves receipts while
rejecting a terminal decision after cancellation. `RawFinalTextLimit` retains a
Host's pre-trim byte contract. All are disabled by default; existing response
observers keep the original provider output, and transformed batches still pass
the regular validators.

Some frozen hosts validate arguments call by call. `PerCallValidation` makes
unknown/not-offered names and argument errors paired tool results, rather than
rejecting the whole batch. Unknown capabilities **never reach Host.Execute**.
Static exclusivity, terminal/public-text rules and `ValidateBatch` still run
before any call. The Host's `BeforeTool` may provide its exact parameter check.
`RecordToolOutcome` selects which settled results belong in the outcome journal;
it cannot discard receipts or terminal results. `BudgetError` optionally supplies
the Host's frozen budget rule before each physical attempt; the outer configured
physical limit remains a hard ceiling. These switches are opt-in and do not
change old default behavior. They allow a candidate to replay historical input
semantics without silently transferring that input to another engine.
