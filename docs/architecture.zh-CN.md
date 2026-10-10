# 架构

[English](architecture.md)

本文说明 Employee Loop 如何把事件变成决策：事件如何到达场域、由哪个会话处理，以及每一步为什么可以安全重试。代码位于 `contract`（类型与端口）、`scene`（Hub 与 Worker）、`kernel`（一次唤醒）和 `store/memstore`（内存参考实现）。

- [模型：事件、场域、会话](#model-events-scenes-sessions)
- [通道](#lanes)
- [事件的生命周期](#lifecycle-of-an-event)
- [领取顺序与并发](#claim-order-and-concurrency)
- [抢占与撤销](#preemption-and-revocation)
- [失败处理](#failure-handling)
- [幂等、租约与围栏](#idempotency-leases-and-fencing)
- [日志：重放一次唤醒](#journals-replaying-a-wake)
- [发件箱与送达状态](#outbox-and-delivery-states)
- [群聊主动旁听：兼容性说明](#proactive-group-observation-compatibility-notes)

<a id="model-events-scenes-sessions"></a>
## 模型：事件、场域、会话

每个输入都是一个 `contract.Event`。事件类型在目录中选出一个 `TypeSpec`，它固定了事件的**类别**（发生了什么：business、execution、completion、schedule、enablement）和**模式**（员工为什么收到它：triggered 即被动触发，listening 即主动监听）。见 [events.zh-CN.md](events.zh-CN.md)。

每个事件属于一个**场域**：员工 x 租户 x 场所（一个群、一次私聊、一个企业，或 Host 定义的其他种类）。场域 id（`SceneRef.ID`）由 Host 或网关生成，是场域状态的唯一键；提供方自己的身份放在不透明的 `SceneRef.Locator` 中。

一个场域拥有若干会话：

```
scene
├── interactive  (前台)  每个场域恰好一个；有人在和员工说话
├── observation  (后台)  每个场域一个；没有人指向员工的消息
├── follow_up    (后台)  每个任务一个（引用 "task"）；在途工作的进展与结果
├── routine      (后台)  每个例行任务一个（引用 "routine"）；例行任务到点
└── lifecycle    (后台)  每个场域一个；需要决策的启用类事件
```

会话以**窗口**为单位处理事件。一个窗口就是一次模型唤醒：一组已封口、有序、已受理的事件（`contract.Window.Items`），模型一起看到它们。同一会话的突发事件会合并到一个窗口（见 [events.zh-CN.md](events.zh-CN.md#coalescing)）。

<a id="lanes"></a>
## 通道

| 通道 | 会话 | 谁可以进入 | 唤醒 |
| --- | --- | --- | --- |
| `foreground` | `interactive` | 来自人类行为者的被动触发业务事件 | 是；纯文本可以作为回复 |
| `background` | `observation`、`follow_up`、`routine`、`lifecycle`（以及 Host 定义的种类） | 主动监听事件、工作汇报、例行任务、生命周期 | 是；只有终止工具能结束 |
| `host` | 无 | 回执、终态事实、配置修订 | 否；记录后由 `Hub.OnHostEvent` 应用 |

`TypeSpec.Validate` 强制通道规则：只有被动触发的业务事件可以进入前台，前台总是使用交互会话，后台类型需要后台会话种类，host 类型没有会话。`Catalog.Route` 还会拒绝行为者不是 `human` 的前台事件。后台唤醒以 `RequireTerminal` 运行：模型的纯文本是私有的，只有 Host 接受的终止决策（reply、quiet、dispatched、waiting）才能结束唤醒。

<a id="lifecycle-of-an-event"></a>
## 事件的生命周期

```
producer ──Publish──► Hub.Admit
                        │ Catalog.Route        校验、分类、选择通道与会话、计算取消项
                        │ EventLog.Admit       每个 (employee, source, event id) 一个回执；已生效的重放 -> duplicate
                        ├─ host 通道:          SessionQueue.Cancel(回执, 撤销) -> OnHostEvent
                        ├─ 会话通道:           SessionQueue.Enqueue（合窗 + 抢占，同一事务）
                        └ EventLog.MarkApplied 以上每一步对同一回执幂等
Worker ──StepOnce──► Hub.Step
                        │ SessionQueue.Claim   前台优先，租约 + 代数
                        │ Runner.RunWindow     KernelRunner：上下文、按通道/会话筛选工具、kernel.Run
                        └ SessionQueue.Complete(result, deliveries)  由租约围栏保护的一次提交；
                                               决策中的回复文本（quiet 从不发送）
Worker ──DeliverOnce──► Hub.Deliver
                        │ Outbox.Pending -> Transition pending->unknown -> Deliverer.Deliver
                        └ Transition unknown->delivered | failed（仅限明确拒绝）
Worker ──ReconcileOnce──► Hub.Reconcile（Deliverer 实现了 DeliveryReconciler）
                        │ Outbox.Unknown -> DeliveryReconciler.DeliveryStatus（从不发送）
                        └ Transition unknown->delivered | failed，或保持 unknown
```

1. **受理。** `Catalog.Route` 校验信封，拒绝未知类型（Loop 从不猜测路由），核对生产者给出的 class/mode 与目录是否一致，在类型要求时要求场域，并返回带会话和取消项的 `Admission`。`EventLog.Admit` 为每个 `(employee, source, event id)` 保存一个回执。回执与它的效果（第 2 步，或 host 通道的取消项与 `OnHostEvent`）不在同一事务中：效果提交之后才把回执标记为已生效（`EventLog.MarkApplied`）。未生效回执的重复发布会重新执行效果，而每一步对同一回执都幂等（`Enqueue` 返回已持有该回执的窗口，`Cancel` 对一个回执只取消一次，`OnHostEvent` 必须幂等），所以两者之间的失败或崩溃既不会丢事件，也不会重复生效。
2. **入队。** 会话通道的事件加入该会话打开的窗口，或开启一个新窗口。事件带来的取消项在同一事务中生效。事件无法加入打开的窗口时（不合窗、达到 `max_items` 或超过 `max_wait_ms`），先把该窗口封口，到期时间不晚于当前时刻，使它先于后继窗口运行，绝不会一直停在打开状态。
3. **领取。** Worker 领取一个已到期、已就绪的窗口并获得租约。
4. **唤醒。** `KernelRunner` 按窗口的通道与会话筛选工具，用本次唤醒的语言（最后一个事件的语言优先于默认语言）解析工具说明和会话角色，冻结 Host 的上下文块，然后运行 `kernel.Run`。有日志时，重新领取会复用第一次记录的模型请求，不再调用 `Context`，因此第一次领取之后受理的输入不会改变被重放的唤醒。
5. **完成。** 除 `quiet` 外，任何带回复文本的决策都会成为一条 `<window id>/reply` 投递：回复、派发确认或提问。前台投递带 `reply_to` = 最后一个事件 id。`SessionQueue.Complete` 在一次受租约围栏保护的提交中记录结果并写入投递。如果窗口在此期间被取消，租约不再匹配，什么都不会发出；如果提交失败，窗口保持运行中，租约过期后由日志重放，所以回复不会因窗口已完成而丢失。
6. **送达。** 送达循环通过渠道的 `Deliverer` 发送待发投递，并通过状态查询核对结果未知的投递。

任务以事件的形式回报：`dispatch_task` 立即返回回执，Task Manager 之后发布 `execution.*` 事件，结果会开启一个按任务分区的 `follow_up` 窗口（见 [services.zh-CN.md](services.zh-CN.md)）。

<a id="claim-order-and-concurrency"></a>
## 领取顺序与并发

`SessionQueue.Claim`（memstore 的语义，每个实现都必须满足）：

- 每个 `due_at` 已过的打开窗口都被封口（`open -> ready`）；租约过期的运行中窗口回到 `ready`。
- 候选窗口必须是 `ready`、已到期、在请求的通道/会话范围内，并且是其会话中最早的未完成（`open`、`ready` 或 `running`）窗口。会话在重试中也保持先进先出：等待重试退避的窗口让同会话后来的窗口排在它后面，消息按到达顺序得到回答，每个会话至多运行一个窗口。设置 `SerializeScene` 时，其场域内不能有任何运行中的窗口。
- 只要场域里还有打开或就绪的前台窗口，后台窗口就要等待；场域里有运行中的前台窗口时，可抢占窗口也要等待：人的输入在其场域内永远优先，前台唤醒期间受理的旁听只在它之后运行，与执行者数量无关。
- 前台窗口排在后台窗口之前，然后按 `due_at`，再按 id。`scene.Worker.StepOnce` 先只请求前台通道，再请求任意通道。
- 每次领取使 `attempts` 加一，并签发新租约 `(window id, 随机令牌, generation = attempts, until)`。`ClaimRequest.LeaseFor` 为零时，memstore 使用 90 秒租约。

Worker 不保存状态：任意数量的 `scene.Worker` 步进循环，无论在一个进程还是多个进程中，都可以服务同一组端口。

<a id="preemption-and-revocation"></a>
## 抢占与撤销

**抢占。** 带 `preempts` 的类型（`message.addressed`、`message.follow_up`、`human.response`、`reaction.added`）会取消该场域中所有被某个类型标记为 `preemptible` 的会话种类的窗口（目前是 `observation`）。取消覆盖 `open`、`ready` 和 `running` 窗口，并与受理在同一事务中发生。运行中的窗口失去租约：它之后的日志写入和 `Complete` 都以 `ErrLease` 失败，`Hub.Step` 丢弃它的决策，因此有人找员工之后，被抢占的旁听不会再发言。

**撤销。** 带 `revokes` 的类型会取消所列会话种类的全部窗口，无论是否可抢占：`policy.changed` 和 `subscription.changed` 撤销 `observation`；`employee.disabled` 撤销 `observation` 和 `lifecycle`。host 通道事件在 `Hub.Admit` 中先执行撤销，再调用 `OnHostEvent`，撤销对每个回执只执行一次：重新生效的重复事件不会撤销第一次尝试之后受理的窗口。没有场域 id 的事件会撤销该员工所有场域中的对应窗口。

被取消的窗口会记录原因（`foreground_admission` 或 `revoked`），不会重试。它们的事件仍留在事件日志中。

<a id="failure-handling"></a>
## 失败处理

`Hub.Step` 对 Runner 的错误分类：

| Runner 结果 | 窗口 | 说明 |
| --- | --- | --- |
| 成功 | `done` | 回复随完成一起提交 |
| 成功，但回复的投递 id 已被不同内容占用 | `held`，原因 `delivery_conflict` | `Complete` 没有提交任何内容 |
| `scene.HoldError{Reason}` | `held`，记录原因 | 明确停止，从不重试（例如 `empty_window`） |
| 日志不匹配（`kernel.ErrJournal` + `contract.ErrConflict`） | `held`，原因 `journal_conflict` | 重新领取与记录不再一致；绝不重新询问模型 |
| 其他错误 | 在 `RetryDelay`（默认 1 秒）后重新 `ready` | 领取达到 `MaxAttempts` 次（默认 3）后以 `attempts_exhausted` 置为 `held` |
| context 取消（停机） | 不变 | 租约过期后由另一个 Worker 重放日志 |

在一次唤醒内，内核消耗 `contract.Budget`（默认：3 次响应、3 次连续错误、3 次提供方请求）。无效输出永远不会进入对话记录，内核会追加一条本地化的修复指令。Host 拒绝（不带回执的 `contract.ErrToolRefused`）会消耗预算，但允许模型修正调用；失败的效果会使唤醒失败。见 [tools.zh-CN.md](tools.zh-CN.md)。

<a id="idempotency-leases-and-fencing"></a>
## 幂等、租约与围栏

| 步骤 | 身份 | 相同内容重放 | 不同内容重放 |
| --- | --- | --- | --- |
| 受理 | `(employee, source, event id)` + `Event.Fingerprint()` | 原回执，`duplicate: true`；未生效的回执会重新执行效果 | `ErrConflict`（HTTP 409 `conflict`） |
| 窗口项 | 回执 id | 持有它的窗口 | - |
| host 通道取消 | 回执 id | 记录的数量，不再取消 | - |
| 窗口 | 每个会话一个打开的窗口 | 加入该窗口 | - |
| 模型轮次 | `(window, seq)` + 请求字节 | 记录的响应 | `ErrConflict`，唤醒停止 |
| 工具调用 | `(window, model turn, native call id)` | 记录的结果和原样的错误文本 | 工具名或参数不同时 `ErrConflict`，唤醒停止 |
| Task Manager 变更 | `(employee, idempotency key)`，key = `<window id>/<model turn>/<native call id>` | 已存回执 | `ErrConflict` |
| 投递 | `<window id>/reply`，由 `Complete` 写入 | 已有投递 | 文本或场域不同时 `ErrConflict`，本次完成不提交任何内容 |
| Task Manager 事件 | `<task>/<run>/<kind>`（source `task-manager`） | 受理时判为重复 | `ErrConflict` |

指纹是去掉 `class` 和 `mode` 后事件 JSON 的 SHA-256，因此生产者可以省略或带上这两个字段。

领取之后的每次写入都受租约**围栏**保护：`Complete`、`Retry`、`Hold` 以及每次日志读写都会把 `(window id, token, generation)` 与当前租约比对，不一致则以 `contract.ErrLease` 失败。失去租约的 Worker（过期、被抢占、被撤销）无法完成窗口、无法写日志，也无法投递回复，因为回复是完成的一部分。

<a id="journals-replaying-a-wake"></a>
## 日志：重放一次唤醒

设置 `KernelRunner.Journal` 后，每次领取都通过一个 `kernel.WakeJournal`（`NewWakeJournal(journal, lease)`）运行唤醒，它包装模型（`Model`）和 Host（`Host`）：

- **冻结输入。** `FrozenTranscript` 返回第一次领取记录的模型第 0 轮请求；`KernelRunner` 把它作为唤醒的对话记录传入，不再调用 `Context`。因此两次领取之间进入场域的消息不会改变被重放的唤醒。
- **模型轮次**按顺序重放。重放轮次的请求 JSON 必须逐字节一致；否则唤醒以 `kernel.ErrJournal` + `contract.ErrConflict` 停止，Hub 把窗口置为 `held`（`journal_conflict`），绝不转而实时调用模型。
- **工具调用**按 `(model turn, native call id)` 重放（提供方可能在不同轮次复用 id），工具名和参数必须一致。记录的错误按原文重放；`ToolRecord.Refused` 保证 `errors.Is(err, contract.ErrToolRefused)` 不变。
- **记录什么。** 成功、拒绝以及带回执的错误立即记录。提供方失败和其他工具错误是暂时的：只有在之后的模型轮次被记录时才记录，因为模型已经看到它们，重放必须再次呈现。以暂时失败结束的唤醒不会记录该失败，重新领取时会在同一幂等键下重新询问模型或重新执行调用。
- 任何日志读写失败（`kernel.ErrJournal`）都会立即结束唤醒。

因此崩溃后被重新领取的窗口不会重复任何已发出的提供方请求，也不会重复已提交的效果；按 `contract.Host` 的要求，Host 仍需按身份加调用 id 对效果去重。

<a id="outbox-and-delivery-states"></a>
## 发件箱与送达状态

```
pending ──Hub.Deliver──► unknown ──发送成功──────────────► delivered
                            │  └──明确拒绝────────────────► failed
                            │     （其他错误：保持 unknown）
                            └──Hub.Reconcile（状态查询，从不发送）
                                   ├─ delivered | provider_accepted
                                   ├─ failed（确定未发送）
                                   └─ unknown（稍后再问）
(suppressed：保留给 Host)
```

`Hub.Deliver` 在调用 `Deliverer` **之前**把投递从 `pending` 改为 `unknown`。发送成功后改为 `delivered`（带提供方 id）。只有明确拒绝，即包装了 `contract.ErrDeliveryRejected` 的错误（渠道没有发送，也不会再发送），才改为 `failed`。其他错误（传输失败、超时、5xx）结果不确定，投递保持 `unknown`；两次状态转换之间崩溃也是如此。`Outbox.Pending` 从不返回 `unknown` 投递，所以它不会被发送两次。

`Hub.Reconcile` 对停留超过最短时长的 `unknown` 投递（`Outbox.Unknown`）询问 `contract.DeliveryReconciler`，从不发送。回答 `delivered` 或 `provider_accepted` 时记录提供方 id，`failed` 表示渠道确知消息没有发出，`unknown` 则留待下一轮。`Deliverer` 同时实现 `DeliveryReconciler` 时，`scene.Worker` 每隔 `ReconcileEvery` 对停留超过 `ReconcileAfter` 的投递执行核对。`httpapi.DelivererClient` 通过 `POST /v1/deliveries/status` 实现它，`dws.Deliverer` 根据自启动以来发出的投递作答。`suppressed` 保留给在发送前撤回投递的 Host。Deliverer 不得重试结果未知的发送（`contract.Deliverer`、`httpapi.DelivererClient`）。

<a id="proactive-group-observation-compatibility-notes"></a>
## 群聊主动旁听：兼容性说明

旁听群聊最容易让员工变得吵闹或过时。契约用以下规则保证安全；每条都说明本模块强制什么、Host 需要提供什么。

- **场域级关注不是会话生命周期。** 员工是否旁听一个场域是一项订阅（`subscription.changed`，启用类），由 Host 和网关持有，而不是一个一直打开的会话。旁听会话在窗口之间不保存状态；关闭订阅会撤销它的窗口。
- **旁听窗口可抢占且无状态。** 有人找员工时，旁听窗口（待处理的和运行中的）会在受理的同一事务中被取消。不会恢复任何东西：下一个旁听窗口在唤醒时从场域重新冻结上下文（在 `employeeloop` 中，会话日志记录被旁听的人类消息，每次唤醒都得到新的历史块）。本模块不会重新入队被取消窗口的事件；希望重新复核的 Host 可以发布 `observation.due` 或新事件。
- **旁听从不与前台唤醒并行。** 抢占会取消有人找员工那一刻已存在的旁听窗口；前台唤醒运行期间受理的旁听，在该唤醒结束前不会被领取（场域有运行中的前台窗口时，`Claim` 会扣住可抢占窗口），之后以新的历史运行。前台回复在送达时进入会话日志；旁听窗口会合窗数秒，因此回复通常先被记录；需要硬保证的 Host 可以让旁听 Host 在场域有待投递消息时拒绝发言。
- **每个场域至多一个活跃的旁听窗口。** 旁听会话每个场域一个；合窗保证每个会话至多一个打开的窗口，领取只运行会话中最早的未完成窗口。没有合窗规则的类型（`member.changed`、`resource.changed`、`observation.due`）可以在其后排队更多就绪窗口，它们依次运行。
- **决策预算与发言预算分开。** `KernelRunner.Budgets` 按通道限制每次唤醒（决策预算）。员工在一个场域里可以多频繁地发言（发言预算）属于 Host 策略：按场域统计投递次数（例如用 Redis 的 `INCR`），预算用完时让旁听 Host 拒绝或强制 `stay_quiet`。本模块不带发言预算。
- **结果未知的投递只核对，不重发。** 见[发件箱与送达状态](#outbox-and-delivery-states)。
- **版本化的配置快照会撤销窗口。** Host 冻结窗口运行时的配置版本，并在版本变化时发布 `policy.changed`（场域）或 `subscription.changed`；它们的 `revokes` 会取消基于旧版本的旁听窗口，包括运行中的窗口，其迟到的结果会被围栏丢弃。版本号如何携带（例如放在 `payload` 或 `refs` 中）由 Host 决定。
