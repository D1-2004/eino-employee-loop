# Frozen delivery envelopes

The existing outbox remains the only delivery aggregate. Business Scope is the
authority/source scope of the accepted work; it is not a transport destination.
An optional Target contains transport, address or scene locator, reply target and
environment supplied by the trusted Host. Owning a business Scope does not grant
permission to send to an arbitrary Target. ValidateSend remains the Host's
current permission/revocation check immediately before the first Send.
Entry wires ResolveDelivery into preparation and additionally requires
AuthorizeDelivery for a typed remote destination; source validation alone is
insufficient. A standalone Manager similarly requires a Host ValidateSend hook.

Manager.PrepareIntent can fill Target/Payload outside the state transaction.
It cannot change ID, source, business scope, Task, Goal, kind or body, nor replace
an explicit Target/Payload. Existing intents replay their frozen envelope without
calling the resolver. Repeating the base command with omitted typed fields uses
the stored values; explicitly changed typed fields or changed base arguments
conflict. Concurrent preparation still commits one frozen envelope through CAS.

Payload optionally declares text and format, immutable resource references and a
versioned structured presentation. Payload text equals Body; Body stays available
to old ports. Resource references require an immutable version and SHA256. The
envelope carries data, not authority or an assumed DWS card protocol: an adapter
must explicitly implement the chosen format/presentation. Local only supports
text/plain and text/markdown without resources or structured presentations.
Its optional typed locator is `local-file / workspace / local` (transport,
address, environment), with no platform scene locator. It freezes a separate
immutable envelope file before writing the Markdown body; replaying the same key
with another destination or payload conflicts even if the body is identical.

Receipts distinguish provider_accepted, delivered, local_applied and
permanent_failure. New receipts bind the entire frozen Intent fingerprint and
Target fingerprint, transport and provider identity. Opaque JSON evidence is
stored without interpreting it as a tool instruction. provider_accepted persists
as its own nonterminal state and only Lookup can establish delivered. A local
Markdown receipt is local_applied with transport local-file; it is never an IM
delivery claim. A verified permanent failure is terminal and retains its reason.
Accepted observations return ErrPending; a caller must retain the pending outbox
instead of interpreting a successful provider submission as completed delivery.
ProviderOperationID can identify an accepted operation before a real message ID
exists. Known operation/message identities cannot change on reconciliation.

Timeout, unknown outcome and a foreign receipt remain unresolved and never cause
a typed remote envelope to be sent again. NotFound/ProvenAbsent is not a delivery
proof and does not authorize resubmission of typed remote work. Lookup errors do
not erase an already accepted provider identity or convert it into failure.
Stage/transport/fingerprint/target mismatches are rejected before applying facts.
The local file adapter can resume a definitively absent immutable write using
its exact envelope/target proof. This exception has no remote-delivery meaning.
A simultaneous receipt and contradictory rejection error remains unknown rather
than being converted to permanent failure.

Compatibility: old untyped ports (both Target and Payload absent) originally
promised a confirmed receipt when Send/Lookup returned an ID, provider ID and body
hash. An empty Stage preserves that explicit legacy contract; it is not accepted
for a typed envelope. Legacy ProvenAbsent retains its old safe-absence retry
contract. Old examples therefore continue to run, while new remote adapters must
provide explicit receipt stages and complete envelope binding. Existing stored
applied rows are unchanged historical facts, not retroactively relabeled.
