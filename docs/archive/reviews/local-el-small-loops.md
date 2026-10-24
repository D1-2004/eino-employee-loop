# 本地 EL 小 Loop 覆盖与证据

本页记录当前源码实现及可复验边界，整体 Entry 业务验收以主 Plan 为准。这里的
SQLite/Markdown 回读与脚本模型测试不等于真实模型质量验收或钉钉 IM 已投递。

## Owner Context 独立加工

`ownerloop.Worker` 接口为 `Observe(ctx, scope, sourceID)` 与
`Tick(ctx, scope)`。Observe 为每个显式启用的 role 写 durable admission；没有新来源时
Tick 只修复已提交投影或清理已撤回引用，不凭定时空转宣称完成认知工作。

| 小 Loop | 输入及结果 | 实现与本地证据 |
| --- | --- | --- |
| Attention | 原始请求、承诺、问题、期限 → 可引用关注视图 | `ownerloop` attention 原生 Agent；`work.Scheduler.Observe/Attention/ResolveAttention` 保存结构化 attention，执行授权仍来自原始 owner 输入 |
| Memory | 原始明确事实/偏好 → memory.md | 独立 Agent/预算；前版引用重新读取原文，支持保留消息窗口外仍授权事实 |
| Dreaming | 原始事实 → 明确标记猜想的联系与想法 | 独立 Agent/预算与角色合同；不创建任务，不写入业务完成事实 |
| Tone | 明确沟通偏好 → tone.md | 独立 Agent/预算；只允许引用精确原话，不将猜测当授权 |
| Reflection | 原始反馈/纠正 → reflection.md | 独立 Agent/预算；记录改进判断，不能宣称已执行修复 |
| Summary/Context | 原始资料 → context.md | 独立 Agent/预算；列明当前来源边界与 gaps，原任务完成以 Task Review 为准 |

六类角色都使用 `agent.Runtime` → Eino Runner / ChatModelAgent，只有
`commit_owner_update` 原生工具能提出结果。Eino `ModelRetryConfig` 负责合同修复，
标准 callbacks 可经 Options 传入。Host 在 native 工具前、提交前、SQLite 事务内
核验原文引用的 scope/revision/hash；Model journal 独立按 role/receipt 计预算。
业务投影提交后再写 md；崩溃恢复从 SQLite 修复 md。并发人工编辑产生 CAS conflict，
不会静默覆盖。旧观察保存完整 Configuration、role 与预算，重建 Worker 不重绑当前配置。
每份派生文档保存 OwnerScope/OwnerEpoch，提交事务再核对 reset 水印；普通更新不改变
水印。跨 scene 引用逐条按原 scope 授权，reset 或撤回使旧文档立即失去事实资格。

来源窗口为当前 scene 最近 20 条完整原消息；前版事实引用对应的原始来源最多补齐
64 条，跨 scene 带各自授权 scope。模型输入不把旧 Owner Markdown 当作事实来源。
每版明确“仅保留引用来源支持的事实”；超出边界明确记录 gaps。引用撤回使已发布
派生视图失效，替换 md 中原文并保留等待新来源的状态。

`ownerloop/owner_test.go` 覆盖六类真实 SQLite/md 回读、重建 Worker 不重复模型、
撤回来源使六视图失效、伪造引文耗尽独立预算且不发布、人工编辑冲突后投影恢复不
重复模型、窗口外旧事实重新读原文、待处理工作沿用冻结配置。

## Watch / Wait / Occurrence / Attention

`work.Scheduler` 必须绑定授权与原始来源校验，所有调用带同一个完整 `work.Scope`。

| 接口 | 实际语义 |
| --- | --- |
| `Schedule` | 绑定既有 Task/Goal/原始 Source；支持定时、周期或精确 event condition，幂等冲突不改原计划 |
| `Signal` | 持久保存原始来源支持的信号；同 signal ID + 不同内容冲突 |
| `Tick(scope, now)` | 每次推进一个到期事实；timer/event Wait 在原 Task 上解决，全部 Wait 解决才回到原 Review |
| `Occurrences` / `Acknowledge` | 读取和确认 durable 发生次；不会创建替代 Task 或自动完成 Goal |
| `AcknowledgeForGoal` | 验证 call=occurrenceID 的真实 Control 回执，确认原 Source、Task、新 Goal/Run，再事务确认发生次并重绑同一计划 |
| `RecoverOccurrence` | 在 Control 已落库、ack 未落库的重启窗口，从该 occurrence 的真实回执恢复原子 ack/rebind；Tick 在因 Goal 不符禁用计划前先尝试此恢复 |
| `Schedules` | 读取当前 Watch/Schedule 与发出数量、禁用状态 |
| `Observe` / `Attention` / `ResolveAttention` | 原始来源支持的结构化关注收件箱；解决/忽略要求明确的 owner 来源，deadline 本身不能代替授权 |

周期按原 Due + occurrenceNumber × Every 计算，保留每次事实，不把停机期间多次
发生合成一次。一次 Tick 只处理一条，Limit 约束总数。未消费发生次绑定原 Task/Goal；
外部纠正 Goal 后旧计划禁用，不把老授权迁移到新目标。只有发生次本身产生的、已
实际受理的原 Task 继续命令，才能显式重绑计划。Run 总预算继续由 TaskManager 执行。
event Wait 不消费早于该 Wait 建立时间的旧事件。
Wait 保存独立 Since，逐个解决多个 Wait 不会因 Task 更新时间改变而误丢仍有效信号。
新建 event Watch 同样保存 Since，早于订阅受理的旧 Signal 不能生成新的发生次。

`work/scheduler_test.go` 覆盖真实 SQLite 重启后的 timer + event 联合 Wait、同一
原 Task 回 Review、旧事件隔离、伪造来源拒绝、周期到期与停止边界、原 Task 继续
后的原子 ack/rebind、ack 重放、三次发生总数限制、结构化 attention 明确解决。
还覆盖 Control 与 ack 之间重启、既有周期计划恢复且不重复 Task/Run，以及重绑之后
原 Schedule 受理请求仍幂等重放。

## 需要在 Entry 总验收中确认的装配

- 前台事件受理后 Observe，独立 owner worker 推进显式启用角色；不阻塞 ChatLoop。
- Scheduler Tick、Signal 与相关 native 工具进入同一个 TaskManager，Occurrence 的
  continue/skip 由实际原生模型提出并保存回执，再确认发生次。
- attention 自身保存关注事实；转成任务必须有原始 Source 和明确业务决策，不因
  attention 视图生成就重复 Dispatch。
- 真实模型分别验证六角色输出的事实与推测边界，整体运行时验收不由脚本模型代替。

本组工程检查：`go test -race ./ownerloop ./work`、`go vet ./ownerloop ./work`。
检查结果以本次实际命令输出与主 Plan 的当前 revision 为准。
