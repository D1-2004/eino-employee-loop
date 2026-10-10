# Multica employee_loop → Eino mapping

Source studied (Mac Studio):

- `/Users/yuanzhan/d1/dt-fde-multica/server/internal/service/employeeloop/`
  - `SOURCE_MAP.md`, `types.go`, `loop.go`, `prompt_builder.go`, `history_presentation.go`, `tools.go`, `service.go`
- `/Users/yuanzhan/d1/dt-fde-multica/server/internal/scene/scene.go` — Field abstraction (group/dm/enterprise)

## Context composition (Multica `buildContext`)

1. system = `BuildPrompt(persona)` only (trusted config)
2. memory snapshot (user data)
3. task brief (user data)
4. recent conversation turns (`conversation_turns_v1`, ≤20 msgs / 16KB)
5. resources of current window
6. current conversation window (human priority queue)
7. background follow-ups

Eino port: `core.Runtime.genInput` builds analogous sections with ConvTokens/MemTokens/RegTokens budgets. History is restored from `Conversation.Recent` rather than Multica's frozen JSON snapshot (local SQLite).

## Host vs model

- Model never grants tenant/scene permissions.
- Effect tools require Host receipts.
- Terminal dispositions: Reply / Quiet / Dispatched / Waiting — accepted dispatch may carry reply without a second model call.
- Process events are Host-surfaced (`core.EventSink`), not model chatter.

## Gaps still open

- MaxModelRoundsPerTurn=4 + BudgetFallbackDispatch (Host auto-dispatch when exhausted)
- Structured `conversation_turns_v1` history presentation (we use free-text nearby/window)
- Full ToolProvider injection replacing Case.Tools hardcoding in DemoCase
- Field kinds beyond conversation (enterprise / ticket) need adapters
