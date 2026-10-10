# Local architecture

```
cmd/demo ──┐
           ├─► loop.Runtime ──► store (SQLite) + checkpoint (SQLite) + MemKV
cmd/dws-   │         ▲
consumer ──┘         │ OnOutbound
                     ▼
              im.Provider (stdin | DWS poll/send)
```

## Checkpoint table

```sql
CREATE TABLE checkpoints(
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL,
  updated_at BIGINT NOT NULL
);
```

Wired as `adk.TurnLoopConfig{Store, CheckpointID}`. Implements `Get` / `Set` / `Delete` (`CheckPointDeleter`) so clean exits delete stale blobs.

## DWS poll

1. Seed: first `+chat-messages` marks current IDs seen (no replay).
2. Every `--poll`: fetch newest page, emit unseen non-self messages as `Inbound`.
3. Persist seen set under `~/.local/share/eino-employee-loop/seen-*.json`.
4. Outbound: `+messages-send --as user --group|--open-dingtalk-id --text --uuid`.
