# Frozen prompt composition for local Host alignment

Status: implemented mechanism; actual Host alignment/quality remain pending. This adds a generic local input seam; it
does not publish any private Host template or certify online prompt parity.

The local application must be able to use externally compiled Host instructions
for its native roles. A global persona appended to every loop is insufficient: a
foreground role, execution, Review, delivery and independent Owner agents have
different contracts. Reuse native Eino SystemMessage, the current Profile freeze,
model bindings and Owner RolePolicies. Do not add a registry, templating engine,
new business state or a second Task/Outbox.

## Input and provenance

An optional `employee-native-roles/1` compiled prompt bundle is loaded by app/local from an explicit file
outside the repository. It contains only literal role instructions, a versioned
native-role contract, source revision, source-manifest SHA256, compiler version
and a content SHA256. The generic schema and synthetic examples may be public;
private source/compiled text, identities, configuration and credentials stay out
of this repository. Origin metadata describes provenance, not a signature or proof
that an arbitrary environment has deployed that source.

Validate bounded strict JSON, exact contract/hash and selected roles. Explicitly
selected bundles must not silently fall back to Profile.Instructions. Missing or
incompatible roles and unresolved source bindings are errors. Legacy profiles
without a bundle retain their exact old prompt behavior and persistence identity.
Freeze a detached bundle with the admitted Profile; a resume or Owner job must use
its original instructions, not a changed file/current Profile. Model/effort routing
continues to use existing ModelBindings.

## Native composition

The selected business fragment accompanies each role's existing native protocol
and tools. Native permission, result/receipt, tool schema, terminal and budget
checks remain authoritative. Ordinary kernel roles resolve their own fragment;
the builtin Owner receives selected fragments through existing RolePolicies so
its original job.Policy and configuration freeze also cover independent agents.
Unsupported replacement backends must not claim automatic prompt adoption.

A Host compiler owns mapping its source families/blocks and dynamic context to
the actual native role/tool/input contract. It must declare unsupported mappings,
not append a complete foreign EL tool manual to every Agent. Trusted identity,
time, current input, coverage, capabilities, task/execution and delivery facts
still come from actual Providers and frozen runtime context; source templates
cannot create those capabilities or authorize an effect.

## Acceptance

Exercise actual native model requests for distinct roles and an independent
Owner, including reopen with a changed current bundle. Verify missing/mismatched
role/hash rejection and preserved old-profile behavior. The local CLI must load
the same frozen artifact it reports; record content digests rather than printing
private instructions. Existing deterministic replay remains mechanism evidence.
Real-model quality requires a separately frozen actual-model run, genuine local
files/Task/receipts and its original independent quality rubric. A script/replay,
compile, health check or successful IM canary is not that quality gate.

## Concrete fields

`contract`, `sourceRevision`, `sourceManifestSHA256`, `compilerVersion`,
`contentSHA256`, `roles` and optional `unresolvedBindings` form the artifact.
`ContentDigest()` is SHA256 of Go canonical JSON for the entire bundle with
`contentSHA256` omitted; it binds provenance and instructions together. A source
manifest hash alone is not a compiled-text hash. Nonempty unresolved bindings
reject; literal JSON examples and braces are valid instructions.

The builtin base roles are `chat`, `executor`, `review`, `task_attention`,
`greeting` and `routine`; add selected delivery loops and `owner_<enabled-role>`.
Only actually enabled Owner roles are required. Same names in another Host do
not prove equivalent semantics: its compiler must document real mappings, partial
adaptations and unsupported roles. Named replacement executor/review/owner
modules and complete external backends are rejected in this native mode. A
trusted injected default factory remains responsible for using its supplied
native Runner; this contract does not certify arbitrary injected code.

CLI Task configuration displays are explicitly redacted to profile identity and
bundle provenance. Private state and the trusted SDK Task API retain original
frozen data for recovery; CLI redaction is not encryption or an authorization
boundary. Existing task files and logs must stay private.

## Model-visible Task projection

A full bundle belongs to the accepted runtime configuration, not every model's
user/tool context. Typed Frame.Tasks and read_task outputs project bundled Task
configuration to profile identity and source digests before context budgeting.
Frame serialization also applies this projection after a delivery role replaces
its Task list. Other role instructions must not enter user/tool messages or
consume another role's bounded context. Private persistence and the trusted SDK
Task API retain complete original configuration for recovery. No-bundle frames
preserve their original JSON bytes; user-authored data is not recursively erased.
