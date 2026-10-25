# Bounded context and durable inbox ordering

M1 admits each new inbox job with a `Sequence` allocated by the State provider's
transactional event sequence, in the same transaction as its job record. Replay
never allocates a second sequence. Processing sorts jobs by that sequence, not by
hash-derived identity. Existing records without a sequence remain readable and
sort before newly admitted jobs, by their stored source admission time and job ID
as a stable tie breaker. Historical arrival order cannot be reconstructed when
older providers did not preserve source times. Retries keep their original place;
there is no implicit priority or insertion rule.

M3 defaults to a 128 KiB serialized Frame budget, 20 historical sources, 12 Front
task summaries, and 1 KiB per task summary field. `WithBudget` can set explicit
positive limits; zero fields retain defaults. This bounds the initial context
payload, not subsequent model tool results, instructions or native checkpoints.
Providers and model adapters remain responsible for those additional envelopes.

Current is an exact original source and appears only in Current, never again in
Sources. Its revision/hash remains in Refs and is revalidated on every attempt.
If mandatory input (Current, or the execution Task and its source evidence) cannot
fit, Build returns `ErrBudget` and a diagnostic gap. It never silently shortens a
human instruction or presents an incomplete execution constraint as complete.

History and Owner projections are optional whole records. Newest history is
preferred within the space remaining after task routing summaries, selected history retains chronological order, and omitted data is
explicit in Gaps. Front tasks contain identity, revision, state and bounded whole
summary fields; source bodies, run outputs and configuration are read
through existing authorized task tools. Current Run identity/state and at most four
unresolved Wait identifiers with bounded whole prompts remain available for precise
answer/resume routing. Owner joins sources to frozen refs by ID, including Current;
its native model input includes that source once and uses current_source_id as the
current-source selector. Oversized summary fields are omitted,
not clipped. Execution continues to use its authoritative Task and exact sources.
Owner evidence, audience policy, frozen refs and owner epoch fencing apply after
selection exactly as before. A frozen frame is checked against the active byte
budget on replay so an older oversized frame is not silently sent to a model.
