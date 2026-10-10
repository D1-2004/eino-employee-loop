# Redis key design (cloud KV; local = in-process MemKV with identical semantics)

| purpose | key | value | TTL | op | local equivalent |
|---|---|---|---|---|---|
| dispatch dedup claim | `{t}:dedup:{scene}:{idem}` | task_id | 90 s (dedup window) | `SET NX PX` | MemKV.SetNX |
| per-scene turn lock (single-writer loop per scene) | `{t}:lock:scene:{scene}` | token | 30–60 s, renewed | `SET NX PX` + Lua compare-and-del | MemKV.Lock/Unlock |
| result reported flag | `{t}:reported:{task}` | 1 | 24 h | `SET` | MemKV.Set |
| rate / budget counters | `{t}:cnt:{scene}:{yyyymmddhh}` | int | 2 h | Lua INCR+PEXPIRE | MemKV.Incr |
| hot nearby-conversation cache (optional) | `{t}:conv:{scene}` | LIST of last N JSON msgs | 1 h | `LPUSH`+`LTRIM` | not needed locally (SQLite page cache) |
| task result push to the right pod | stream `{t}:evt:{scene}` | task_result events | MAXLEN ~1000 | `XADD` / `XREADGROUP` | in-process `TurnLoop.Push` |

Rules: Redis is a cache/coordination layer only; every fact needed after a crash lives in PostgreSQL
(tasks, memory_facts, conversation). A lost Redis key can at worst allow ONE duplicate dispatch inside the dedup window,
and even that is blocked by the PG partial unique index `ux_tasks_active_idem`.
