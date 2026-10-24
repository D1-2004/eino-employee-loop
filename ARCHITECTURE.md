# Employee Loop architecture

Three top-level directories separate the framework, runnable applications and
evaluation. The framework's physical directories follow the existing M1–M7
business architecture directly; there is no additional `modules` layer.

```text
app/         application assembly + concrete Providers
    │ depends on
employee/    seven business modules + reusable kernel and ports
evals/       cases, runner and evidence; exercises app/employee
```

Production code under `employee` must not import `app` or `evals`. A host supplies
concrete Providers to `employee/entry.Open`. SQLite, Markdown, local execution,
file delivery and channel implementations belong to the reference application,
not to the framework.

![Three-root architecture](employee/docs/architecture/module-composition.png)

## Seven business modules

| Module | Directory | Owns | Must not own |
| --- | --- | --- | --- |
| M1 Chat | `employee/chat` | Frontline prompt, tools, input admission and business commands | Another Task store or delivery truth |
| M2 Task | `employee/task` | Worker, Review proposal, timing and attention; `state` owns Task/Goal/Run/Wait/Occurrence transitions | Provider-specific execution state |
| M3 Context | `employee/context` | Source reads, authorized views, citations, frozen frames and revalidation | Authority inferred from model text |
| M4 Owner | `employee/owner` | Attention, memory, dreaming, tone, reflection and summary | Business completion or new task authority |
| M5 Execution | `employee/execution` | Atomic Run bridge, identity checks and provider contracts | A concrete local executor or Goal completion decisions |
| M6 Interaction | `employee/interaction` | Expression/package/message composition, durable Outbox and receipt reconciliation | Concrete local delivery or another Task state machine |
| M7 Entry | `employee/entry` | Profile validation, dependency wiring, registry, startup, shutdown and application API | Concrete Providers or module-specific business algorithms |

Task `state` is the single business aggregate. A Review proposes a transition;
only a validated state command commits it. Execution exit, Goal completion,
provider acceptance and visible delivery are distinct facts.

## Framework support

- `employee/kernel`: shared Agent invocation and native tool binding.
  `kernel/eino` uses Eino Runner, checkpoint/resume and extension composition;
  `kernel/journal` records physical model calls and budgets.
- `employee/kernel/{decision,event,workflow,generation}`: reusable decision APIs.
  These are framework capabilities, not additional application entrypoints.
- `employee/ports/services`: Config, Messages, Resources, Tools and Files contracts.
  Execution and delivery contracts live in their business modules.
- `employee/ports/storage`: transactions, checkpoints, leases and artifacts.
  An implementation must preserve atomicity and recovery, not merely JSON CRUD.
- `employee/contract`: shared Scope, Profile, Input and Failure values.
- `employee/internal`: private implementation helpers.
- `employee/docs`: current guides/specs and historical framework plans.

Go package names match their directory names. Documentation may use explicit
aliases such as `host "…/employee/ports/services"` or
`loop "…/employee/kernel/decision"` to keep a local example readable.

## Default reference application

`go run ./app/local` runs a `package main` application. Its source is an example
of assembling the framework, not a library imported by other applications.

```text
app/local
├── main.go                  CLI and model setup
├── local.go                 Application assembly
└── providers/
    ├── storage/             SQLite, Markdown and memory implementations
    ├── services/            Local readers and Eino read-tool binding
    ├── execution/           Local atomic Run execution
    ├── delivery/            Local file delivery
    ├── channel/             NDJSON / captured-DWS input mapping
    └── sessionfile/         Optional workflow session storage
```

The application supplies `DefaultStorage`, `DefaultExecutor`, `DefaultDelivery`,
`BuildServices` and `PrepareTools` to Entry. Another application imports
`employee/entry` and supplies its own Providers. It may reuse a reference
Provider deliberately, but the employee framework never selects or imports one.
Exact-version module factories are available for hosts that need replacements;
ordinary local CLI use needs no custom registry.

`app/dingtalk` is a separate lightweight channel example. It does not own the
local application's durable Task database and is not a second product CLI.

## Default business path

1. M7 validates identity/configuration and opens the supplied dependencies.
2. M1 admits the source and uses M3 to freeze an authorized model view.
3. M1 submits a business command to M2's single Task aggregate.
4. M2 submits an atomic Run through M5 and checks returned execution facts.
5. Review in M2 checks evidence and proposes the next business transition.
6. M6 freezes a delivery intent and reconciles its actual receipt.
7. M4 processes admitted evidence independently. Its projections return through
   M3's audience/source checks before entering a model context.

Factories receive their own typed dependencies rather than all application
Options. Storage, Context, Executor, Reviewer, Scheduler and Owner replacements
must preserve the same business contracts; they cannot create parallel Task
state, Outbox or authority paths.

## Recovery and online alignment

Frozen sources, configuration, stable effect keys and exact module/plugin
versions bind admitted work. Keep old factories until their work drains.
There is no arbitrary hot unload, latest-version fallback or automatic migration.
Changing a core provider binding requires explicit migration.

Unknown effects are reconciled at the original provider. An Eino checkpoint
restores cognition; it does not prove an external effect. Current permission,
source and audience checks still apply on recovery.

Ports align with online responsibilities and critical semantics. Real DWS
identity, rich delivery, platform source reading and full Buddy budget/wait
protocols still require specific adapters and deployed-version acceptance.
Local file receipts and scripted mechanism tests do not establish that parity.

## Evaluation and evidence

`evals/cases/local` contains the replay profiles, inputs and scripted models.
`evals` runs cases against the application/framework; production `employee` code
never depends on evaluation fixtures. `evals/reports`, `evals/reviews` and
`evals/architecture` retain version-specific results and historical diagrams.
Old SHA values, commands and paths in those records describe their original
version and are not rewritten to imply fresh acceptance.

## Import migration

Paths are relative to `github.com/D1-2004/eino-employee-loop/`. This is a breaking
layout change: neither the original root paths nor the intermediate `modules`,
`kernel`, `ports` and `adapters` root paths have compatibility wrappers. Pinned
consumers remain on their existing revision until explicitly upgraded. No online
consumer upgrade or deployment is implied.

| Original / intermediate path | Current path |
| --- | --- |
| `entry`, `modules/entry` | `employee/entry` |
| `work`, `modules/task/state` | `employee/task/state` |
| `modules/chat`, `modules/task` | `employee/chat`, `employee/task` |
| `contextbuilder`, `modules/context` | `employee/context` |
| `ownerloop`, `modules/owner` | `employee/owner` |
| `execution`, `modules/execution` | `employee/execution` (contract/bridge); local Provider in `app/local/providers/execution` |
| `interaction`, `modules/interaction` | `employee/interaction` (business/contract); local Provider in `app/local/providers/delivery` |
| `agent`, `kernel/eino` | `employee/kernel/eino` |
| `loop`, `kernel/decision` | `employee/kernel/decision` (same test helper subpackages) |
| `event`, `journal`, `workflow`, `generation` or `kernel/*` | `employee/kernel/*` |
| `storage`, `ports/storage` | `employee/ports/storage` |
| `host`, `ports/services` (contracts) | `employee/ports/services` |
| `storage/*`, `adapters/storage/*` | `app/local/providers/storage/*` |
| `adapters/services/*` | `app/local/providers/services/*` |
| `workflow/filestore`, `adapters/sessionfile` | `app/local/providers/sessionfile` |
| `channel`, `adapters/channel` | `app/local/providers/channel` |
| `localapp` library, `cmd/el` | Run `go run ./app/local`; embed through `employee/entry` in another application |
| `local`, `cmd/eel`, `examples/dingtalk` | `app/dingtalk` / `app/dingtalk/runner` |
| `examples/local-el` | `evals/cases/local` |
| `docs` | `employee/docs`; historical acceptance records in `evals` |

Read [local application usage](app/local/README.md),
[framework assembly](employee/docs/module-composition.md),
[contracts](employee/docs/specs/README.md) and [evaluation](evals/README.md).
