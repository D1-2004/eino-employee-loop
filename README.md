# Eino Employee Loop

[中文](README.zh-CN.md)

An event + scene driven decision loop for digital employees, built on
[CloudWeGo Eino](https://github.com/cloudwego/eino). It answers one question per
wake — *given what just happened in this scene, which tools should the employee
call, and is it done?* — and leaves everything else to the Host that embeds it.

```
            Host (yours)                                   this module
 ┌──────────────────────────────────────────┐      ┌────────────────────────────┐
 │ channels ─► event.Event ─► catalog.Route ─┼─────►│ event: class · mode · lane │
 │ queues · windows · context assembly      │      │        session · cancels   │
 │ tools · permissions · receipts           │◄────►│ loop:  model ↔ tools,      │
 │ storage · replay · delivery              │      │        budgets, terminals  │
 └──────────────────────────────────────────┘      └────────────────────────────┘
```

## Packages

| Package | What it does |
| --- | --- |
| `event` | Classifies inputs (class × mode) and routes them to a lane and session of their scene, with preemption and revocation. Standard catalog in `event/events.json`. |
| `loop` | Runs one wake on an Eino `ToolCallingChatModel`: native tool batches, validation before any effect, receipts for effects, terminal decisions, budgets, repair turns. |
| `loop/looptest` | A scripted Eino model for Host tests. |
| `local`, `cmd/eel` | Runs one employee on a laptop against real DingTalk (an account through the dws CLI, or a Stream Mode robot), traced in Langfuse. |
| `schema` | JSON Schema of `event.Event` and reference PostgreSQL storage for a durable Host. |

## The boundary

A Host calls the loop the same way it would call any other decision engine:

```go
out, err := loop.New(loop.Config{
	Tools:               tools,          // the Host's capabilities
	Budget:              loop.DefaultBudget(),
	RequireTerminalTool: true,           // plain text is private; finish with a tool
}, model, host).Run(ctx, loop.Wake{Identity: id, Messages: messages})
```

- **In:** `Config` (tools, budget, protocol switches and hooks), an Eino model,
  a `Host` (`Execute(ctx, Identity, ToolCall) (ToolResult, error)`), and the
  wake's frozen messages.
- **Out:** `Outcome` — the decision (`reply`, `quiet`, `dispatched`,
  `waiting`), receipts, every tool outcome in call order, budget usage, token
  usage and the accepted transcript.

The types mirror a typical employee kernel (`Tool`, `ToolCall`, `ToolResult`,
`Decision`, `Receipt`, `ToolOutcome`, `Outcome`, `Budget`), so an existing Host
swaps engines by mapping fields, not concepts. Hooks keep Host policy outside the
module: `ValidateBatch`, `ValidateText`, `TurnTools` (which tools each model call
offers), `RepairText`, `Permanent`, and observers for model calls, tool calls,
rejected batches and failures.

Rules the loop enforces:

- a batch is validated as a whole before any call runs (unknown or not-offered
  tools, exclusive tools, conflicting static terminal dispositions, argument
  schemas, Host rules) — a rejected batch is repaired, never half-executed;
- an effect without a Host receipt is never done;
- a Host refusal (`ErrToolRefused`) goes back to the model as a tool result;
- invalid output never enters the transcript;
- the budget bounds valid responses, consecutive errors and provider attempts.

## Events and scenes

Every input is an `event.Event`. Its type fixes **what happened** (class) and
**why the employee received it** (mode):

| | triggered (passive) | listening (active) |
| --- | --- | --- |
| business | `message.addressed`, `message.follow_up`, `human.response`, `reaction.added` | `message.observed`, `member.changed`, `resource.changed` |
| execution | `execution.accepted`, `execution.started`, `execution.progress`, `execution.steered` | |
| completion | `execution.result`, `execution.terminal` | |
| schedule | | `routine.due`, `observation.due` |
| enablement | `employee.enabled`, `employee.disabled`, `subscription.changed`, `policy.changed`, `scene.bound` | |

Routing gives each scene **one foreground interactive session** (triggered
business events from people, serialized, human input first) and **queued
background sessions** — `observation` (listening, preemptible), `follow_up` (one
per task), `routine` (one per routine), `lifecycle` — plus a **host lane** for
facts applied without waking a model. Addressed human input preempts observation
windows; policy and subscription changes revoke them. See
[docs/events.md](docs/events.md).

## Run it locally

```bash
export LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=...      # any OpenAI-compatible endpoint
export LANGFUSE_BASE_URL=... LANGFUSE_PUBLIC_KEY=... LANGFUSE_SECRET_KEY=...   # optional
go run ./cmd/eel -channel dws -profile <corpId:userId> -locale zh-CN       # a DingTalk account
go run ./cmd/eel -channel robot -robot-client-id <id> -robot-client-secret <secret>   # a Stream robot
```

See [docs/local.md](docs/local.md) for both modes, Langfuse traces and running an
eval suite against the local employee.

## Documentation

- [docs/events.md](docs/events.md) — classification, routing invariants, sessions, preemption, extension types.
- [docs/host.md](docs/host.md) — what a Host implements: tools, receipts, refusals, wake assembly, storage, replay, delivery.
- [docs/local.md](docs/local.md) — local verification.

Texts are multilingual: the loop's repair instructions (`loop/locales`), the
local employee's prompt (`local/locales`) and event summaries
(`event/events.json`) ship in `en` and `zh-CN`, with tests that keep the key sets
equal. Add a locale by adding files.

## License

MIT
