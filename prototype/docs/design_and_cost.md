# Eino Employee Loop — 架构、实测与本地/上云成本

## 1. 目标形态（对齐 Grok Bot）

收到消息 → **先秒回一句** → 决策轮并行跑 → 需要干活就 `dispatch_task`（回执立刻返回）→ 后台模拟执行 → 结果经 `TurnLoop.Push` 回流 → 模型再 `reply`。用户中途再说话：`WithPreempt` 打断当前决策轮。

与「一次回复拆两次模型调用」不同：这是完整运行时——**事项登记（Task registry）** 与 **记忆（Memory）** 是 Host 拥有的数据，不是模型自述。

## 2. 架构

```
┌──────────── ack（thinking off）────────────┐
│  DeepSeek · 无工具 · ≤20 字确认 / <none>     │
└────────────────────────────────────────────┘
用户消息 ──► TurnLoop.Push ──► ChatModelAgent（thinking on）
                 ▲                    │
                 │              find/read/dispatch/memory/reply
                 │                    ▼
           task_result ◄── 后台 job ◄── TaskStore.Create(+dedup)
                                      MemoryStore / KV / Conversation
```

| 模块 | 职责 | 本地 | 上云 |
|------|------|------|------|
| TaskStore | 派发、状态机、幂等去重、find/read | SQLite WAL | PostgreSQL（部分唯一索引） |
| MemoryStore | 带出处的事实、检索、归档压缩 | SQLite + FTS5(trigram) | PG + pg_trgm（可加 pgvector） |
| KV | 去重认领、场景锁、已汇报标记 | 进程内 MemKV | Redis SET NX / Lua |
| Conversation | 近邻对话、ack/reply 日志 | 同库表 | PG；可选 Redis LIST 热缓存 |
| Runtime | ack∥决策、抢占、协议修复、上下文预算 | Eino ADK v0.9.21 | 同代码；多副本靠 Redis 锁/队列 |

## 3. Schema 要点

- `tasks`：`ux_tasks_active_idem (tenant,scene,idem_key) WHERE status IN (queued,running)` — 跨进程竞态只赢一个。
- `memory_facts`：`quote` 必须是发送者原话子串；`evidence_scope` 区分 user_said / employee_said / host_digest；压缩只 **archive**，写一条 digest，不 LLM 改写。
- 多租户：所有键带 `tenant`；PG 侧可开 RLS（见根目录 `docs/storage.zh-CN.md` 与 `schema/postgres.sql`）。

## 4. 实测（本机 Linux，DeepSeek `deepseek-flash`，API 并发 ≤2）

### 4.1 存储微基准（n=2000；详见 `bench.json`）

| 操作 | SQLite+MemKV p50 | PG17+Redis p50 |
|------|------------------|----------------|
| task.create | ~102 µs | ~145 µs |
| task.get | ~23 µs | ~47 µs |
| mem.put | ~424 µs | ~567 µs |
| mem.search | ~123 µs | ~94 µs |
| kv.setnx | <1 µs | ~25 µs |
| 16 写并发 create | ~2.9k w/s | ~15k w/s |
| 64 协程同 idem 竞态 | 恰 1 胜 | 恰 1 胜 |
| 2 万条 task+fact | ~1.2 KB/条 | ~1.2 KB/条 |

相对一次 LLM 调用（秒级），存储开销可忽略。SQLite 单写者在高并发写时吞吐下降；多员工并行应上 PG+Redis。

### 4.2 运行时 A/B（treat = 登记+记忆+ack；control = 无登记；各 26 run）

| 指标 | treat | control |
|------|-------|---------|
| 最终消息 ack 中位 / p95 | **713 / 966 ms** | 747 / 981 ms |
| 最终决策终态中位 / p95 | **5.7 / 27 s** | 10.7 / 26 s |
| 派发尝试 → 建任务 | **30 → 30** | 51 → 51 |
| 状态问句上又派发 | **0** | 9 |
| 同目标重复建任务 | **0** | 3 |
| 无依据「已执行」声明 | **0** | 3 |
| v2 judge（可评判 18） | 10/18 | 11/18 |
| Prompt tokens/次 · cache hit | ~22.6k · 97.5% | ~22.5k · 97.3% |
| 协议修复（失败） | 9（1） | 7（1） |
| SQLite DB/run 中位 | ~342 KB | — |

成功样例：`S-webhook-dup` 同句连发两次 → treat 只建 1 个任务；`S-preempt` 0.9s 改口 → 第一轮 cancel；`S-memory-priv` 私人记忆不对另一说话人泄露。

### 4.3 误差类对比

| 误差类 | control / 无状态环 | treat（有登记） |
|--------|-------------------|-----------------|
| 状态问句重复派发 | 9 次 | **0** |
| 同目标重复建任务 | 3 | **0** |
| 无依据执行声明 | 3 | **0** |
| 寒暄翻旧待办 | 仍可能 | 仍可能（产品策略） |
| 纯文本无 decision tool | 占轮次 | protocol_repair 补一轮 |

Judge 通过率两边接近：很多失败来自冻结 rubric（日期/工具白名单），不是登记能单独解决的。

## 5. 工程成本估算

| 项 | 量级 | 说明 |
|----|------|------|
| 存储层（接口+SQLite+PG+Redis+契约测） | ~1.0–1.5 kLOC / 3–5 人日 | 已完成 |
| Runtime（ack∥决策、工具、去重、抢占、修复） | ~1.5–2 kLOC / 5–8 人日 | 已完成 |
| 评测回放与分析 | ~0.5 kLOC / 2–3 人日 | experiments 仓 |
| 生产加固（RLS、迁移、观测、真实执行器） | +5–10 人日 | 未做 |
| 风险 | 中 | SQLite 单写；ack 与决策语义漂移；模型 plain-text |

**LLM 成本粗算**：每次决策 ~23k prompt + ~0.9k completion；Flash 非高峰下单次约厘～分人民币；ack 另加百级 token。Registry/memory 注入通常 <1k token/轮。

## 6. 推荐栈

| 阶段 | 推荐 |
|------|------|
| 本地 / 单机 PoC | **SQLite WAL + MemKV + Eino ADK**（本仓默认） |
| 预发 / 多副本 | **PostgreSQL 17 + Redis 7**（同接口） |
| 执行器 | Mock → 真实沙箱/MCP；回执字段保持 Host 语义 |

## 7. 已知限制

- 后台 job 仍是模拟结果（带「演示结果」标记）。
- 完整 31 工具 + 长 system 使首包大；生产应渐进披露工具目录。
- ADK 抢占会丢未执行的 tool_calls，需历史回滚（已做）。
- v2 judge 对日期等硬规则仍会刷掉部分「行为正确」的 run。
