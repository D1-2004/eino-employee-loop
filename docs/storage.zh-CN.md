# 存储

[English](storage.md)

Loop 只依赖 `contract/store.go` 中的存储端口。本模块只带内存实现：Loop 用 `store/memstore`，Task Manager 用 `taskmgr/memtask`，不带任何持久化实现。`schema/` 中的 SQL 文件是参考 DDL，每个拥有表的服务各一对：

| 服务 | PostgreSQL | SQLite | 对应 |
| --- | --- | --- | --- |
| Employee Loop | [loop.postgres.sql](../schema/loop.postgres.sql) | [loop.sqlite.sql](../schema/loop.sqlite.sql) | Loop 的端口（`EventLog` .. `Conversation`） |
| Task Manager | [taskmgr.postgres.sql](../schema/taskmgr.postgres.sql) | [taskmgr.sqlite.sql](../schema/taskmgr.sqlite.sql) | 持久化的 `contract.TaskManager` |

两个服务可以把表放在不同的数据库中。每一对文件定义相同的表、列和索引名（由测试保持一致，并检查没有表同时属于两个服务），`schema/sql_test.go` 会执行 SQLite 文件，并在设置 `EEL_TEST_POSTGRES_URL` 时执行 PostgreSQL 文件。部署方把自己的持久化端口传给 `employeeloop.Config.Ports`；留空的端口仍在内存中。

- [端口与表](#ports-and-tables)
- [状态机](#state-machines)
- [幂等键与唯一性](#idempotency-keys-and-uniqueness)
- [持久化实现需要的事务](#transactions-a-durable-implementation-needs)
- [为什么不用外键](#why-no-foreign-keys)
- [PostgreSQL 上的索引](#indexes-on-postgresql)
- [短期协调](#short-lived-coordination)
- [租户](#tenancy)

<a id="ports-and-tables"></a>
## 端口与表

| 归属 | 端口 | 方法 | 表 |
| --- | --- | --- | --- |
| Loop | `EventLog` | `Admit`、`MarkApplied`、`Receipt` | `event_receipts` |
| Loop | `SessionQueue` | `Enqueue`、`Claim`、`Complete`、`Retry`、`Hold`、`Cancel`、`Window` | `session_windows`、`window_items`、`session_cancellations`、`outbox_deliveries`（`Complete`） |
| Loop | `Journal` | `ModelTurns`、`SaveModelTurn`、`Tools`、`SaveTool` | `journal_model_turns`、`journal_tool_records` |
| Loop | `Outbox` | `Post`、`Transition`、`Pending`、`Unknown` | `outbox_deliveries` |
| Loop | `MemoryStore` | `Put`、`Search`、`Recent`、`Archive`、`Compact`、`CountActive`、`GetFact` | `memory_facts` |
| Loop | `Conversation` | `Append`、`Recent` | `conversation_messages` |
| Task Manager | `TaskManager`（服务边界，没有存储端口） | `Dispatch`、`Get`、`Find`、`Steer`、`Stop`、`Continue` | `tasks`、`task_requests`、`task_runs`、`task_events` |

Task Manager 的存储归它自己：契约只定义它的服务边界，参考表给出一种持久化的做法。

列说明：

- `event_receipts.event` 保存路由后的事件（已填入 class 与 mode），以便由 `window_items` 重建窗口。`applied_at` 在受理的效果提交后由 `MarkApplied` 设置（`EventReceipt.Applied`）。
- `session_cancellations` 记录一个 host 通道回执的取消（`SessionQueue.Cancel`），重新生效的重复事件不会再取消任何窗口。
- `session_windows` 带租约（`lease_worker`、`lease_token`、`lease_generation`、`lease_until`）和记录的 `result`（`contract.WindowResult`）。
- `window_items.seq` 是窗口内各项的顺序。
- `outbox_deliveries.scene` 保存完整的 `SceneRef`（网关需要 locator）；`scene_id` 是查询键。
- `memory_facts` 与 `conversation_messages` 保留 `contract.Fact` 和 `contract.Msg` 的 Unix 毫秒字段。
- `tasks` 保存 `contract.TaskView` 的字段，以及 Task Manager 运行所需的字段（`prompt`、`locale`、`current_run`）。
- `task_events` 既是任务历史（`event` 为 NULL 的行），也是 Task Manager 发布给 Loop 的执行事件的有序发件箱；每个事件都带 `employee`，因此一个 Task Manager 可以向多个 Loop 报告。

<a id="state-machines"></a>
## 状态机

**窗口**（`contract.WindowState`）：

```
      封口 (到期 / max_items / 后继)         领取                完成
  open ───────────────────────────► ready ─────► running ───────────────► done
   │                                  ▲            │  │ 重试 (not_before)
   │                                  └────────────┘  │ 租约过期 -> ready
   │                                                  └─ 挂起 ──► held
   └── 抢占 / 撤销 (open、ready 或 running) ───────────────────► cancelled
```

`done`、`held` 和 `cancelled` 是终态。离开 `running` 的每次转换都受租约围栏保护。同一会话的新项无法加入打开的窗口时，该窗口也会在创建后继窗口之前封口。

**投递**（`contract.DeliveryState`）：`pending -> unknown -> delivered | failed`；只有明确拒绝才是 `failed`，其他发送错误保持 `unknown`。`unknown -> delivered | provider_accepted | failed` 只能通过核对（状态查询）。`unknown` 从不重发；`suppressed` 保留给 Host。见 [architecture.zh-CN.md](architecture.zh-CN.md#outbox-and-delivery-states)。

**任务**（`TaskView` 使用的 `contract.Task*` 状态）：

```
queued ──开始执行──► running ──► done | failed
  ▲                       │
  │ continue (done/failed)│ stop ──► cancelled
  └───────────────────────┘ steer：保持活跃，开新的执行
```

每次状态变化都使 `revision` 加一。控制在同一事务中按此顺序检查：已记录的 `idempotency_key` 返回已存回执（内容不同则 `ErrConflict`）；然后正的 `expected_revision` 必须等于 `revision`（`ErrStaleRevision`）；然后 `Steer` 与 `Stop` 需要活跃任务（`queued`/`running`），`Continue` 需要 `done` 或 `failed` 的任务（`ErrTaskState`）。幂等键在先，是因为已生效的控制已经推进了任务，它的回放必须拿到回执，而不是版本过期或状态拒绝。

**执行**（`taskmgr.Run*`）：`accepted -> running -> done | failed | cancelled`，或在纠正开启新执行时变为 `superseded`。

**任务事件**：`pending -> published | rejected`。同一任务的待发事件按 `seq` 顺序发布；暂时性失败会阻塞其后的事件，永久性拒绝（无效事件、未知类型或场域、冲突）会被记录并跳过。

**记忆事实**：`active -> archived`（遗忘或压缩）。文本从不删除或改写；压缩写入一条 `digest` 事实，逐字列出被归档的原文。

<a id="idempotency-keys-and-uniqueness"></a>
## 幂等键与唯一性

| 键 | 由谁保证 | 用途 |
| --- | --- | --- |
| `(employee, source, event_id)` + `fingerprint` | `ux_event_receipts_identity` | 每个事件只受理一次；内容相同 -> 重复，内容不同 -> 冲突 |
| 每个 `(employee, scene_id, session_kind, session_key)` 一个 `open` 窗口 | `ux_session_windows_open` | 合窗目标 |
| 每个会话一个 `running` 窗口 | `ux_session_windows_running` | 会话串行 |
| `receipt_id` 只属于一个窗口 | `ux_window_items_receipt` | 事件只处理一次；对已入队的回执再次 `Enqueue` 返回原窗口 |
| `(employee, receipt_id)` | `session_cancellations` 主键 | 一个 host 通道回执只取消一次 |
| `(window_id, seq)` | 主键 | 模型轮次按序重放 |
| `(window_id, turn, native_id)` | 主键 | 每个工具调用只执行一次；提供方可能在不同模型轮次复用原生 id |
| `(employee, id)`，id 为 `<window>/reply` | 主键 | 每个窗口一条回复 |
| `(employee, scope, idempotency_key)` + `fingerprint` | `task_requests` 主键 | Task Manager 变更；scope 为 `dispatch` 或 `control` |
| `event_id` = `<task>/<run>/<kind>` | `ux_task_events_event_id` | 重新发布的事件在 Loop 处判为重复 |
| 活跃事实中的 `(tenant, scene, audience, subject, text_hash)` | `ux_memory_facts_active_text` | 记忆去重 |
| `(tenant, scene, seq)` | 主键 | 会话顺序 |
| `source_ref` 非空时的 `(tenant, scene, source_ref, kind)` | `ux_conversation_messages_source` | `Conversation.Append` 幂等，重试的发布只记录一次消息 |

<a id="transactions-a-durable-implementation-needs"></a>
## 持久化实现需要的事务

- `SessionQueue.Enqueue`：在 `window_items` 中查找回执（存在则返回其窗口），执行 `Admission.Cancel`，新项无法加入时封口会话的打开窗口，再插入或追加该项，全部在同一事务中，使前台受理与它带来的抢占一起提交。
- 受理顺序是先回执、再效果、最后 `MarkApplied`（`UPDATE event_receipts SET applied_at = now() WHERE id = $receipt`）。三者不必在同一事务中：`applied_at` 为 NULL 的回执的重复事件会重新执行效果，而效果对同一回执幂等。
- `SessionQueue.Cancel`：在同一事务中插入 `session_cancellations` 行并取消窗口；主键冲突时返回记录的数量，不再取消。
- `Claim`：封口每个 `due_at` 已过的打开窗口。
- `Claim`：在一条语句中选出候选并设置 `running` 与租约，或使用 `SELECT ... FOR UPDATE SKIP LOCKED`；运行中窗口的部分唯一索引会拒绝同一会话的第二个执行者。候选必须是其会话中最早的未完成窗口（同一会话中不存在 `(opened_at, id)` 更小的 `open`/`ready`/`running` 行，由 `ix_session_windows_session` 支撑）；后台候选要求其场域没有打开或就绪的前台行，`preemptible` 候选还要求没有运行中的前台行（`ix_session_windows_scene`）。窗口 id 必须按创建顺序排序。
- `Complete`、`Retry`、`Hold` 与日志写入：`UPDATE/INSERT ... WHERE id = $window AND state = 'running' AND lease_token = $token AND lease_generation = $gen`；影响零行即 `ErrLease`。`Complete` 在受围栏保护的更新所在事务中插入窗口的 `outbox_deliveries` 行（已有行的文本或场域不同则以 `ErrConflict` 回滚），所以已完成的窗口一定在发件箱里留有回复。
- `Outbox.Unknown` 读取 `state = 'unknown' AND updated_at < $before`（`ix_outbox_deliveries_unknown`）；核对只用比较并设置的 `Transition` 把行移出 `unknown`。
- Task Manager：状态变化、`task_requests` 行以及它产生的 `task_events` 行放在同一事务中（事务性发件箱），然后再发布。

<a id="why-no-foreign-keys"></a>
## 为什么不用外键

参考 DDL 中没有 `FOREIGN KEY`、`REFERENCES`、级联或触发器：

- 服务可以分开部署，把表放在不同的数据库中（`loop.*.sql` 与 `taskmgr.*.sql`）；跨库的引用本来也无法强制。
- 每张表有自己的保留期。回执、日志和发件箱是审计记录；随窗口或任务一起删除它们的级联会悄悄毁掉证据。
- 关系由端口实现在自己的事务中检查（例如 `Enqueue` 写入回执关联，`Complete` 检查租约），围栏比引用约束更能保护顺序。
- 热点表（`session_windows`、`outbox_deliveries`）避免外键检查带来的额外加锁。
- 同一份 DDL 可以在 SQLite 和 PostgreSQL 上运行。

<a id="indexes-on-postgresql"></a>
## PostgreSQL 上的索引

`loop.postgres.sql` 与 `taskmgr.postgres.sql` 中的每个二级索引都是建表之后单独的一条 `CREATE [UNIQUE] INDEX CONCURRENTLY IF NOT EXISTS` 语句。PostgreSQL 不允许在事务块中使用 `CONCURRENTLY`，因此请以自动提交方式执行每个文件（普通的 `psql -f`，不要加 `--single-transaction`），或在会把文件包进事务的迁移工具中，让每条索引语句成为单独的迁移步骤。主键随表一起创建。

参考实现对 `tasks.goal` 和 `memory_facts.text` 的文本搜索使用子串匹配。部署方可以增加 `pg_trgm` GIN 索引；部分托管 PostgreSQL 不允许应用角色执行 `CREATE EXTENSION`，因此请对扩展加保护，并让该索引保持可选。

<a id="short-lived-coordination"></a>
## 短期协调

Loop 不需要协调存储：崩溃后需要的一切都在上面的表里，契约也不定义键值端口。Host 自己加的策略（发言预算、附带工作的锁）把状态放在合适的地方即可，这些状态可以丢失。类 Redis 存储的建议键如下，`{t}` 为租户：

| 用途 | 键 | 值 | TTL | 操作 |
| --- | --- | --- | --- | --- |
| 每个场域每小时的发言预算 | `{t}:speak:{scene}:{yyyymmddhh}` | 整数 | 2 小时 | `INCR` + `PEXPIRE` |
| 需要串行化附带工作的 Host 的场域锁 | `{t}:lock:scene:{scene}` | 令牌 | 30-60 秒，续期 | `SET NX PX` + 比较后删除 |
| 短期去重占位 | `{t}:dedup:{scope}:{key}` | id | 去重窗口 | `SET NX` |
| 可选的热会话缓存 | `{t}:conv:{scene}` | 最近 N 条消息 | 1 小时 | 列表追加 + 截断 |

丢失一个键最多在其窗口内允许一次重复；持久的幂等性仍由上面的唯一索引保证。

<a id="tenancy"></a>
## 租户

Loop 的表以 `employee` 为键；记忆、会话和任务还以 `tenant` 为键。多租户的 PostgreSQL 部署可以在租户列上增加行级安全（例如策略 `USING (tenant = current_setting('app.tenant', true))`，并在每个连接上设置该参数）。原型中 Eino TurnLoop 的 checkpoint 表已由日志表取代。
