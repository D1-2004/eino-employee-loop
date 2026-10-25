# Cancellation and model request bounds

A request cancellation and a verified execution exit are different facts. The
Host still owns durable Run settlement and exit evidence; an Eino cancellation
checkpoint is only the continuation of its agent graph.

## Native lifecycle

`employee/kernel/eino.Runtime` uses Eino `adk.WithCancel`, immediate recursive
cancellation and the native checkpoint store. The graph runs with a separate
persistence context so shutdown does not abort the checkpoint write. After the
request is cancelled, persistence has at most five seconds of context lifetime
to settle. Checkpoint providers must honour context cancellation; a provider that
ignores it cannot be forcibly terminated by Go.

Eino 0.9.21's non-streaming cancellation monitor delegates `Generate` to the
underlying model without cancelling that model context. Graph interruption alone
can therefore return a checkpoint while a blocked model remains alive. The
runtime attaches the owning request to model and tool I/O using Eino's native
`ChatModelAgentMiddleware` endpoint wrappers. It signals the native graph cancel
before cancelling the I/O context, preserving the checkpoint boundary. Wrappers
preserve ADK context values, tool call IDs, resume targets and authorization
bindings. Normal completion also releases request-owned streaming contexts.

Cancellation remains cooperative. Providers must stop model HTTP requests,
streams and tools on `ctx.Done()`. A tool that already performed an external
operation needs the Host's existing idempotency key/receipt before it is resumed;
this mechanism does not make arbitrary external effects exactly-once or prove
that a remote executor exited.

`Recover` resumes the native cancellation checkpoint without inventing an answer.
Targeted business interrupts still require `Resume` and explicit interrupt IDs.
The cancelled request is not retried in place as a fresh workflow.

## Compatibility decision loop

The compatibility loop's ADK graph uses a detached cancellation context, while
its provider, Host and callbacks receive the original request context directly.
Its cancellation is already propagated to cooperative `Generate` and Host calls.
That separation is retained to preserve the pinned decision/parity semantics.
Cancellation tests wait for the actual model/Host call to exit, not only for the
outer loop to return, and assert no extra effect attempt.

## Reference OpenAI-compatible provider

`app/local/providers/models` sets a two-minute HTTP request timeout by default.
`LLM_REQUEST_TIMEOUT` can override it with a positive Go duration such as `45s`
or `3m`. Zero, negative and invalid durations are rejected before HTTP I/O.
This operational limit does not select a model, alter effort, replace a frozen
binding or bypass the registered provider/version checks. Incoming request
cancellation still aborts a request earlier than its HTTP timeout.

Tests use cooperative blocking models/tools and a local HTTP test server. They
verify actual I/O exit, persisted cancellation checkpoints and recovery without
repeating a completed earlier tool effect. They send no real model or IM traffic.
