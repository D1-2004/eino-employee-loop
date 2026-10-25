# Host-registered plugins

Registration lives in `employee/entry`; native extension composition lives in
`employee/kernel/eino`; Owner implementation lives in `employee/owner` (Go name `owner`).

Plugins compose Eino middleware, callbacks and read-only AgentAsTool children.
They are trusted, compiled Go factories, selected by exact immutable
`Name` + `Version` references in `Profile.Plugins`. Unknown, duplicate or missing
versions fail closed; neither URLs, scripts nor a `latest` fallback are executed.

The same frozen profile applies to Chat, execution, Review, delivery, attention
and every Owner worker. Owner observations resolve plugins from their persisted
`job.Configuration`, including after process recreation. Changing the current
profile must not rebind an admitted observation to newer plugin code. Hosts must
keep old factories available until their admitted work has drained. Removing a
reference affects new admissions; hot unloading and automatic migration are not
provided, and recovering work whose factory was removed fails explicitly.

`owner.Options.ConfigureRuntime` lets trusted Hosts compose the native runtime
using the observation's frozen `Request`. Entry resolves the selected registry
factories on the actual native config; passing only global callbacks does not
satisfy the plugin contract.

Owner validates calls against the final tools offered by the native runtime,
including registered read-only AgentAsTool children. A valid nonterminal call
may run before the single `commit_owner_update` terminal call. Unknown tools,
invalid arguments, duplicate call IDs and batches mixing the terminal commit
with other calls are rejected before invocation. The terminal proposal retains
its original source/citation validation and atomic commit; child output is not
a replacement source or an additional business permission. Reopening completed
Owner work does not invoke its child or commit again.

Owner workers depend on a narrow source/audience Context interface and typed
state/artifact ports. Hosts may add policies with trusted
`owner.Options.RolePolicies`; each observation freezes the chosen policy
text and profile revision before execution. Recreating a worker with a newer
policy does not rewrite old work. Historical observations lacking policy text
retain compatibility only with the six preexisting builtin roles.

Before retaining earlier facts, Owner checks every prior source against the
target audience. A denied source marks that observation failed with a durable
diagnostic, without loading it into the target model or overwriting the shared
prior document. Other observations continue. Recovery rechecks every frozen
source against the current Host audience policy, so a policy revocation also
blocks previously admitted cross-scene material.

The CLI includes a compiled `audit@v1` factory, discovered with `el -list-plugins`
without a profile, state directory or credentials. A profile may select
`"plugins": [{"Name":"audit","Version":"v1"}]`. The factory emits JSON
metadata to stderr for runtime configuration and native lifecycle callbacks:
plugin identity, role and event only. It does not serialize prompts, sources,
tool arguments, model outputs, identifiers, credentials or error text. Callback
logging is best effort; this reference logger is not a durable compliance sink.
Stdout remains the command's machine-readable business output.

A plugin's child agents do not automatically inherit the parent's durable model
journal, token/request budgets or capability restrictions. `ReadOnlyAgents` is
a Host assembly contract, not a sandbox for arbitrary Go code. Hosts must supply
children with their own read-only ports, bounded models, recovery, cancellation
and cleanup. Plugin middleware cannot replace offered Host tools, terminal
effects or admission checks.
