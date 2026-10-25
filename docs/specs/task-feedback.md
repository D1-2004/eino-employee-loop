# Task control, review and visible feedback

TaskManager remains the only Task/Goal/Run authority. Review tool proposals must
pass the same snapshot and transition validation as the transactional commit.
Correctable business rejections return a tool error for the model to correct;
authorization, revoked context, unknown execution and storage failures never become
permission to retry a write. A failed Run cannot be marked complete.

Control tools accept an optional short `reply`. The acknowledgement enters the
existing Outbox only after the control/dispatch receipt is committed; its id is
stable across replay. It acknowledges acceptance, never proves worker exit,
business completion or transport delivery.

Steer checks successor run capacity before cancellation. With no capacity it
rejects the request without changing the active writer, accepted instruction or
Goal. The model can explain the rejection using `reply`. Review `continue` at the
limit instead persists a paused Task with an explicit budget reason and retained
next instruction; the original evidence and execution exit remain authoritative.
No automatic budget reset, replacement Task or permission expansion is allowed.
The caller can inspect artifacts or explicitly stop. This slice does not expose
an API to extend an accepted frozen budget; an authorized revision and subsequent
resume require a separate host integration. Automatic resumption after exhaustion
is not claimed.

Failed, paused and stopped transitions create deterministic status intents from
durable Task events in the same Outbox. They state the business status and actual
writer activity separately. Replays do not duplicate the intent. A status update
never renders itself as a successful result; transport receipts remain independent.
The SDK's unspecified MaxRuns default is 4; callers may explicitly choose a
bounded different value. A budget is frozen into each accepted Task.

`find_tasks` returns at most 12 summaries by default, ordered by latest update,
with offset pagination and explicit `has_more`, `truncated` and omitted-field
markers. `read_task` retains the authorized original. A large mandatory current
message is never shortened: Chat records the original job as failed with the
budget error, publishes a bounded explanation, then allows later admissions to
proceed. Authorization failures and unavailable storage do not become model
permission to mutate another task.

Correctable errors are marked at the command's no-commit rejection boundary,
never by a generic wrapper around the entire tool handler. A successful Task
receipt followed by an ACK target-resolution, Outbox or storage failure is a hard
invocation failure; the model must not repeat the command using a new call ID.
Task lookup NotFound can be corrected only at a pure-read preflight. Answer source
admission happens in Submit; only the subsequent atomic Answer rejection is
model-correctable. Source admission, Host and transport errors remain fatal.
