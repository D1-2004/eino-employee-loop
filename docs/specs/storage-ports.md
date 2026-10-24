# State and artifact ports

Business modules import `ports/storage` and depend on `storage.State` and `storage.Artifacts`, not a database
driver. The default host still uses SQLite as authority and Markdown as a
human-readable projection; this change introduces no migration or dual writes.

`State.Transact` supplies one atomic unit for aggregate CAS, operation receipts
and events. A callback error (including a CAS conflict) rolls back every write.
Document revision zero means create-only; updates require the exact revision.
Event IDs are idempotent only for identical kind and bytes. Transactions must not
perform model or network I/O, escape the callback, or recursively access State.
All returned bytes are detached from adapter-owned data.

State also supplies scoped/versioned Eino checkpoints and fenced workflow
leases. These remain explicit capabilities because an adapter that implements
only JSON CRUD cannot safely replace the runtime's persistence. Close fails with
`ErrBusy` while a workflow lease is active.

Artifacts use a relative `.md` name and content-hash CAS. Empty expected hash is
create-only. Missing reads use `fs.ErrNotExist`; changed content uses
`ErrArtifactConflict`. They do not participate in State transactions, so callers
must journal projection intent in State and reconcile artifacts idempotently.

`adapters/storage/sqlite` and `adapters/storage/markdown` retain their exported adapter types. This does not preserve old import
paths. SQLite keeps `Transaction(func(*Tx) error)` and `Checkpoints` for source
compatibility; business modules use the neutral `Transact`/`CheckpointStore` API.
`adapters/storage/memory` is an independent process-local reference adapter, intended for
conformance and ephemeral embedding, not crash recovery or cross-process use.
