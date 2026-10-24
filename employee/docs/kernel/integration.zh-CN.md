> This guide covers the reusable kernel or reference example. For the application and M1–M7 ownership, see [ARCHITECTURE.md](../../../ARCHITECTURE.md).

# 接入本循环

Code examples use the explicit `loop` import alias for package `decision`:

```go
import loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
```


[English](integration.md)

写给在自己平台里运行本循环的团队：Host 要实现什么、需要哪些状态和服务、哪些输入驱动它、怎样自检、怎样把它和现有引擎并排替换、怎样评测。单次决策合同保留 v0.3 行为；当前从 `employee/kernel/decision` 导入，并固定为你的 Host 已验证的提交。历史提交使用旧目录结构。它依赖的契约在 [host.zh-CN.md](host.zh-CN.md)、[events.zh-CN.md](events.zh-CN.md) 和 [local.zh-CN.md](local.zh-CN.md)，这里不重复。

**术语。** *Host* 是运行员工的平台：受理事件、装配上下文、执行工具、保存状态、投递消息。*唤醒（wake）* 是一次决策：Host 把一个会话窗口的输入交给循环，循环运行到得出终态决定（`reply`、`quiet`、`dispatched`、`waiting`）或失败。*冻结* 是在唤醒首次被领取时一次性固定它的配置、消息和引擎；重新领取只读冻结的那份。*journal（模型与工具日志）* 是唤醒的模型请求与响应、工具结果的持久记录；重新领取时重放它，不再问 provider，也不再执行效果。*副本标记* 是每个服务副本对外声明的带版本字符串，形如 `<name>:<N>`（例如写在心跳行里）；Host 比较所有存活副本的标记，只有每个存活副本都声明了它，才启用对应的新规则。升级标记就是把 `N` 调大。*挂点（hook）* 是 `loop.Config` 里由 Host 提供的回调函数。

## 1. 模块是什么

| 是 | 不是 |
| --- | --- |
| 一个 Go 库：`loop.New(cfg, model, host).Run(ctx, wake)` 决定一次唤醒 | 服务、队列、worker 或调度器 |
| 一次唤醒内的模型 ↔ 工具循环：整批校验、回执、终态决定、预算和修复 | 工具实现：权限、效果和回执属于 Host |
| 一份声明式事件目录（`event`），给输入分类并路由 | 受理存储：去重、窗口、租约和取消属于 Host |
| 一个引擎，可以替换另一个接收相同冻结输入的引擎 | 提示词构建器：人设、历史和记忆由 Host 放进 `Wake.Messages` |
| 只评判完整响应：每次 provider attempt 一次 `Generate` | 模型网关或流式转发：超时、journal 和任何流式都归 Host 的模型 |

## 2. 边界与状态

循环决定一次唤醒；有副作用的事情都由 Host 来做（[内核入口](README.md)、[host.zh-CN.md](host.zh-CN.md)）。

**最小 Host。** 你要实现或提供的部分：

| 部分 | 定义在 |
| --- | --- |
| 受理：构造 `event.Event`，用 `event.Catalog.Route` 路由（`event.Standard()` 加你注册的类型） | [events.zh-CN.md](events.zh-CN.md)、[host.zh-CN.md §1](host.zh-CN.md#1-受理事件) |
| 一个 `loop.Wake{Identity, Messages}`；`Identity{Tenant, Employee, Scene, Receipt}` 来自可信的受理数据，绝不来自消息文本 | [host.zh-CN.md §3](host.zh-CN.md#3-装配唤醒) |
| 本次唤醒的 `[]loop.Tool`，以及每次 `Run` 新建的 `loop.Config` | [host.zh-CN.md §4](host.zh-CN.md#4-提供工具) |
| `loop.Host.Execute(ctx, Identity, ToolCall) (ToolResult, error)` | [host.zh-CN.md §5](host.zh-CN.md#5-执行调用) |
| 可选：`loop.FinalTextHost.AcceptFinalText(ctx, Identity, text)` | [host.zh-CN.md §5](host.zh-CN.md#5-执行调用) |
| 一个可持久的 Eino `model.ToolCallingChatModel`：每次 `Generate` 一个 provider 请求，关掉 SDK 重试 | [host.zh-CN.md §6](host.zh-CN.md#6-让模型可持久) |

Host 的策略以挂点接入，只读冻结数据和交给它的状态：

| 挂点 | 签名 | 用途 |
| --- | --- | --- |
| `TurnTools` | `func(call int, outcomes []ToolOutcome) []string` | 收窄某次 provider attempt 提供的工具 |
| `ValidateArguments` | `func(Tool, map[string]any) []string` | 用你自己的参数规则代替内置 schema 子集；发现的问题以 `ErrInvalidArguments` 拒绝整批 |
| `ValidateBatch` | `func(call int, outcomes []ToolOutcome, calls []ToolCall) error` | 你的整批规则；批次的最后一道检查，且是纯检查 |
| `ValidateText` | `func(string) error` | 公开文字：普通回复、final text 和每个工具的 `PublicArgs` |
| `ValidateDecision` | `func(Decision) error` | Host 结果可以携带哪些终态决定；被拒的决定使该调用失败，随后使唤醒失败（final text：立即） |
| `FinalTextAllowed` | `func(call int, outcomes []ToolOutcome) bool` | 哪些普通文本回答可以结束唤醒 |
| `Progress` | `func(ToolCall) bool` | 哪些成功的调用会清零连续错误 |
| `RepairText` | `func(call int, outcomes []ToolOutcome, err error) string` | 响应被拒后追加的指令 |
| `Permanent` | `func(error) bool` | 立即结束唤醒的 provider 错误 |

`call` 是 provider attempt 的序号（从 1 开始，即 `ModelCall.Attempt`）；`outcomes` 是到此为止的 Host 结果，按调用顺序，只读。观察者（`OnModelCall`、`OnToolCall`、`OnBatchAccepted`、`OnBatchRejected`、`OnModelFailure`）从不改变唤醒，也不得阻塞。规则需要的信息超出 `call` 和 `outcomes` 时，在 `OnBatchAccepted` 里记录被接受的批次（绝不记录被拒的），并为每次 `Run` 新建 `Config`。业务规则留在 Host，经这些挂点注入；模块里没有业务词汇。

### 状态

循环在两次运行之间不保存任何东西。一次 `Run` 期间，它在内存里持有对话记录、预算计数和当前批次；重新领取从空状态开始，重放 journal。所有持久状态都属于 Host；[`employee/docs/reference/host.sql`](../reference/host.sql) 有参考表。

| 状态 | 为什么 | 参考 | 没有它 |
| --- | --- | --- | --- |
| 按 `(employee, source, id)` 的事件回执，带指纹 | 渠道至少投递一次 | `event_receipts` | 回答两次 |
| 场域目录 | 每个员工、租户和场合一个稳定的场域 ID | Host 自己的 | 跨租户泄漏 |
| 窗口、租约、attempt | 合窗、每个会话同时只有一次唤醒、崩溃后重新领取 | `session_windows` | 并发或乱序回复 |
| 冻结的唤醒和配置（含引擎） | 重新领取发出相同的请求 | `session_windows.wake` | journal 冲突 |
| 模型 journal | 重新领取不再问 provider | `window_model_turns` | 双倍成本、不同的回答 |
| 模型 attempt 预留 | 崩溃或重新领取不会放大 provider 请求 | 发送前先插入 `window_model_turns` 行（response 为 NULL）；限制每个窗口的行数 | 双倍成本、绕过预算 |
| 按 `(window, turn, native id)` 的工具记录 | 每个效果只执行一次 | `window_tool_records` | 重复发送或派发 |
| 结果 | 失败的唤醒可能已有提交的效果 | `session_windows.outcome` | 丢失效果 |
| 发件箱：`pending`、`unknown`、`delivered`、`failed` | 结果未知的发送只核对、绝不重发 | `outbox` | 重复或丢失消息 |
| 历史、记忆、人 | 下一次唤醒的上下文 | Host 自己的 | 没有连续性 |

### 依赖

| 依赖 | 是否必需 | 说明 |
| --- | --- | --- |
| 兼容 OpenAI、支持原生工具调用的聊天端点 | 是 | 设置 `RequireNativeToolCall` 时需要 `tool_choice: "required"`；关掉 SDK 重试（[host.zh-CN.md §6](host.zh-CN.md#6-让模型可持久)） |
| Go 1.26.1 或更高、CloudWeGo Eino | 是 | 版本见 `go.mod`；Eino 版本由你的依赖解析决定 |
| 存放上述状态的存储 | 生产环境需要 | `employee/docs/reference/host.sql` 是 PostgreSQL，没有外键 |
| 收发消息的渠道 | 是 | 归 Host |
| Langfuse | 否 | trace 由 Host（或 `local`）写；循环只调用观察者 |

## 3. 一致性检查

在 Host 自己的测试里，用真实的 Host 和模型适配器、外接假传输（假渠道、测试库里的发件箱、`httptest` 起的 provider）运行 [`employee/kernel/decision/hosttest`](../../kernel/decision/hosttest/hosttest.go)（契约见 [host.zh-CN.md](host.zh-CN.md#一致性检查)）：

```go
target := hosttest.Target{Host: host, Identity: id, Effects: outbox.Len}
target.Effect(t, loop.ToolCall{NativeToolCallID: "c1", Name: "reply", Arguments: args})
target.Refused(t, loop.ToolCall{NativeToolCallID: "c2", Name: "reply", Arguments: unknownSource})
target.FinalText(t, "hello")
target.Wake(t, cfg, messages, steps, loop.Decision{Kind: loop.Reply, Reply: "hi"})
target.ReusedID(t, cfg, messages, note, last, loop.Decision{Kind: loop.Quiet})
hosttest.Model(t, adapter, provider)
```

- `Effects` 返回 Host 到目前为止提交了多少个效果（发件箱行数、假渠道发出的消息数）。
- 让假传输在每次提交之后立刻调用 `hosttest.Committed(ctx)`。在 `Effect` 下它会在那一刻取消 context，检查由此证明「提交后被取消仍返回回执」；在别处它什么也不做。
- `hosttest.Model` 需要一个 `hosttest.Provider`：`Requests` 返回到目前为止收到的请求，`FailNext` 让下一个请求以 SDK 会重试的方式失败（例如 HTTP 500）；`Usage` 可选。
- `Effect`、`FinalText`、`Wake` 和 `ReusedID` 需要一个它们的调用还没执行过的 Host 状态：每项检查用新的窗口或新的测试库。

| 能力 | `hosttest` |
| --- | --- |
| 效果工具：有回执，提交后 `ctx` 立刻被取消时也有 | `Effect` |
| 按 `(window, turn, native call id)` 幂等；两次响应可能复用 `call_0` | `Effect`、`ReusedID` |
| 在任何效果之前拒绝（`loop.ErrToolRefused`） | `Refused` |
| final text 只提交一次 | `FinalText` |
| 终态工具、决定；重新领取发出相同请求、不再提交 | `Wake` |
| 可持久模型：每次 `Generate` 一个请求、工具来自 `WithTools`、强制调用、provider 自己的调用 ID | `Model` |

生产 Host 必须通过全部检查，包括 `ReusedID`：只按原生 ID 做键的 journal 过不了它，因为 provider 会在不同响应里复用 `call_0` 这类 ID。`local` 通过除 `ReusedID` 之外的全部检查，因为它唯一的效果会结束唤醒。

**final text。** 设置 `Config.AcceptFinalText`（同时设 `RequireTerminalTool`），并实现 `FinalTextHost.AcceptFinalText`，它只提交一次。不设 `FinalTextAllowed` 时，只有第一次调用、且在任何工具执行之前，可以用普通文字结束；设了它，每次调用都由它决定，所以第一次调用的情形要自己包含进去。`FinalTextLimit`（默认 8000 字节）按 trim 后的文本计算。

另外必须做到、由 Host 自己的测试检查：

- 每次 provider attempt 在发送前持久预留（见“状态”），预留耗尽通过 `Permanent` 报告。
- `Run` 失败时也保留 `Receipts` 和 `ToolOutcomes`；通过发件箱送达。
- 错误文本会进入模型：拒绝和工具错误会进入对话记录和下一次请求，所以要去掉主机地址、凭证和内部 ID。
- 同步调用方已经不再等待时，通过正常渠道送达回复；绝不让回复只留在没人读的槽里。
- 每个工具的 schema 从 `ToolInfo.Extra[loop.ExtraParameters]` 发送。只要 schema 用到 Eino 类型化副本（`ParamsOneOf`）装不下的东西，例如 draft-07 的元组 `items` 或 draft-04 的布尔 `exclusiveMinimum`（对这些它只保留一个裸 object schema），这一条就是必需的；其他情况也推荐这样做。
- 用解码 JSON 的方式构造 `Tool.Schema`，或提供 `ValidateArguments`：内置检查只把 `enum` 读成 `[]any`，所以在 Go 里写成 `[]string` 的 enum 会展示给模型，但要经过一次 JSON 往返才会被强制。
- 用 `errors.Is` 和导出的哨兵分类错误，绝不按文本。

要运行的检查：

- 模块内：`go test -race ./...`。
- 每次升级 Eino 或本模块后，运行 [host.zh-CN.md §6](host.zh-CN.md#6-让模型可持久) 的对齐（parity）测试，加 `-v`，确认有 `--- PASS: TestParityWithV021`：没有这个测试的版本会打印 "no tests to run"，退出码仍是 0。
- 两个引擎并存时：用同一份脚本驱动两者，把请求字节、Host 调用和结果冻结进 golden 文件，并把剩余差异列进一个测试，列表过期时测试失败。

## 4. 驱动

每个输入都是一个 `event.Event`；它的类型决定类别和性质（[events.zh-CN.md](events.zh-CN.md)）。按下表映射你的输入：

| 你的输入 | 类型 | 会话 | 说明 |
| --- | --- | --- | --- |
| 私聊、@ 提及，或回复员工的消息 | `message.addressed` | interactive | 行为者是人，由 Host 认证 |
| 回复送达后同一个人紧接着继续说 | `message.follow_up` | interactive | 依据送达回执判断，不看文本 |
| 回答员工的问题或卡片 | `human.response` | interactive | |
| 对员工消息的表情回应 | `reaction.added` | interactive | |
| 没有指向员工的群消息 | `message.observed` | observation | 需要能投递全部群消息的渠道；没有时，旁听用例报“不可测”，绝不报通过 |
| 成员加入或离开；关注的资源变化 | `member.changed`、`resource.changed` | observation | 主动监听，可抢占 |
| 后台工作已受理、已开始、被纠正或终止 | `execution.accepted`、`execution.started`、`execution.steered`、`execution.terminal` | host 通道 | 事实；不唤醒 |
| 后台工作的进展 | `execution.progress` | 每个任务一个 follow_up | 执行方必须主动发出（见下） |
| 一次运行的结果 | `execution.result` | 每个任务一个 follow_up | 结构化结果（见下） |
| 例行任务或旁听窗口到点 | `routine.due`、`observation.due` | routine、observation | |
| 员工被启用或停用；订阅或策略变化 | `employee.enabled`、`employee.disabled`、`subscription.changed`、`policy.changed` | host 通道 | 停用撤销 observation 和 lifecycle 窗口；订阅或策略变化撤销 observation |
| 员工被绑定到一个新场域 | `scene.bound` | lifecycle | |
| 其他 | 你自己的类型 | | 先注册；未知类型被拒绝 |

**后台工作。** 循环从不从执行方的运行日志、stdout 或推理过程推断进展：执行方必须调用明确的进展 API，每个阶段一次，带幂等 ID（重试发送相同内容）。让员工始终是源会话里唯一的发言者：执行方上传文件并汇报回执，自己不发消息。执行方合同要有意识地定；每条严格规则都有代价：

| 规则 | 为什么 | 代价 | 更好的做法 |
| --- | --- | --- | --- |
| 结果恰好是一个 JSON 对象，前后没有任何内容，字段来自白名单，没有 null | 控制数据只来自一次严格解码 | JSON 前多一句话的正确结果也会被拒：没有 summary、目标不完成、附带的文件被丢弃 | 在执行方提供按 schema 校验的“提交结果”工具，或在本地校验后要求重写一次；对“差一点”的输出保留诊断 summary |
| 进展只能经明确的 API 调用发出 | 不泄露私有推理；用户看到的措辞归员工 | 模型从不调用时，长任务全程无声 | 运行器同时上报结构化的阶段事件，由 Host 据此起草进展 |
| 执行方从不向源会话发消息 | 一个声音，不重复回复 | 只写在提示词里的规则偶尔会被打破 | 在执行方的工具层强制，而不只靠提示词 |
| 执行方凭据的有效期覆盖整个运行 | 回调 Host 一直可用 | 短期 token 会让长任务的最终汇报失败 | 续期 token，或按执行方的寿命签发 |

## 5. 切换引擎

- **同一个准备好的唤醒。** 在 Host 里把每个唤醒准备一次（冻结配置、身份、初始消息、逐次调用规则），交给每个引擎同一个准备好的唤醒、同一个可持久模型和同一个 Host；每个引擎返回同样形状的结果。逐次调用规则放在一个 Host 对象里：现有引擎直接调用它，本循环经挂点调用它，这样一条规则永远不会写两遍。
- **选择并冻结。** 按员工或模板选择引擎（A/B 测试于是只是一项设置，不需要部署），并在首次领取时把它冻结进唤醒快照；快照只在冻结的引擎上重放。
- **默认拒绝（fail closed）。** 如果现有引擎有在批次执行**中途**生效、任何挂点都表达不了的规则，就永远不要为用到它们的配置冻结本循环，并在适配层也拒绝这类唤醒。
- **给开关加版本。** 只有所有存活副本都声明同一个副本标记时，才让新快照选择本循环。冻结的快照会发出不同请求或接受不同响应时就调大标记的编号，并调到任何分支都没用过的号：一个号绝不能对应两套规则。
- **跨版本先排空。** 在会改变标记的部署或回滚之前：把员工切回现有引擎，等到没有冻结在本循环上的未完成唤醒，部署并确认每个副本都声明了新标记，再切回来。只改设置不需要排空。
- **流式。** 循环只评判完整响应。要提前发出某一帧（例如第一句反馈），就在你的模型里流式：在那里检查并处理这一帧，返回完整消息，让批次用同一个原生 ID 再执行一次，`Execute` 返回已记录的结果（[host.zh-CN.md §6](host.zh-CN.md#6-让模型可持久)）。如果循环拒绝了完整响应，提前的效果仍然已经提交，所以只对能通过所在批次规则的调用提前执行。把这个模型装饰器放在 Host 里，两个引擎就以同样的方式流式。

## 6. 评测 Host

模块不带评测工具；用例和评测工具都由你自己提供。

| 层级 | 说明什么 | 怎么做 |
| --- | --- | --- |
| 单元与一致性 | 模块和你的 Host 遵守契约 | §3 |
| 本地，脚本化 | 不用 LLM 的确定性决定 | `app/dingtalk -channel console -model-script …`（[local.zh-CN.md](local.zh-CN.md#本地调试)） |
| 本地，真实 | 真实模型和真实聊天下的行为 | 仅限钉钉：同一批用例分别经账号（`dws` 命令行）和钉钉 Stream 模式机器人跑（[local.zh-CN.md](local.zh-CN.md)）；其他平台请经你自己的 Host 跑真实用例 |
| Host 内 A/B | 本循环不比现有引擎差 | 两个引擎在你真实的 Host 后面跑同一批用例 |

要清楚每一层**不能**说明什么。本地运行器验证的是模块、渠道和 trace，不是你 Host 的规则。只调用一次模型的唤醒只会以 call=1、没有 outcomes 调用逐次调用的挂点，后续调用的规则测不到。golden 和对齐（parity）测试证明脚本输入下请求相同，不证明真实模型下行为相同。只要 Host 规则或引擎适配层有改动，就要端到端重跑 A/B。

每次唤醒记一条**唤醒记录**，全部来自 `loop.Outcome`、`Identity` 和 Host 放进唤醒的 ID：

```json
{
  "wake_id": "w-17", "scene": "scene-3", "source_message_ids": ["m-41"],
  "event": {"type": "message.addressed", "lane": "foreground", "session": "interactive"},
  "decision": "reply", "reply": "C2", "model_calls": 1,
  "tools": [{"name": "reply", "native_call_id": "call_0", "executed": true, "refused": false,
             "error": "", "receipt": "r-9", "args": {"text": "C2"}}],
  "work_refs": [], "run_refs": []
}
```

每个用例声明自己需要什么（传输、Host 能力、证据）：缺传输或能力时用例被阻塞，缺证据时检查是空的，绝不算通过。一个最小用例（字段名只是示例，不是模块定义的 schema）：

```json
{
  "id": "dm-receipt-01",
  "needs": {"transport": ["direct_message"], "host": [], "evidence": ["wake_records"]},
  "steps": [
    {"id": "ctx", "actor": "lead", "text": "候选是 {C1}、{C2}、{C3}；{G1} 和 {G2} 已收到回执。", "wait": {"mode": "silence", "window_s": 30}},
    {"id": "ask", "actor": "lead", "text": "哪个候选没有回执？只回编号。", "wait": {"mode": "reply"}}
  ],
  "judge": {
    "criteria": "只回一次，内容是 {MISS}，不加别的。",
    "checks": [{"steps": ["ask"], "replies": [1, 1], "include_all": ["{MISS}"]},
               {"steps": ["ask"], "max_model_calls_per_wake": 3}]
  }
}
```

A/B 做法：给每个引擎单独的会话，或在用例之间重置历史，因为共用一个会话的用例会相互污染，后跑的引擎吃亏；每轮交替顺序；回读引擎设置，并在每条 trace 里核对引擎；开跑前确认 tracing 已打开；每个用例至少 3 次有效尝试，窗口内有重启或部署的尝试作废。一个可按需调整的“不更差”示例标准：硬失败不增加；没有现有引擎总能通过、本循环却大多失败的用例；私有文本泄漏为零；没有新的已知缺口；每次唤醒的模型调用最多多 10%；首次可见回复的 P50 最多慢 1 秒，并在开跑前定好时延口径（是否按步配对）。

## 7. 可观测性

trace 名称沿用参考运行器（[local.zh-CN.md](local.zh-CN.md#langfuse)）；它还会发出 `agent_id`、`employee_job_kind`、`employee_source_message_id`，失败时还有 `employee_failure`。只有一个引擎的 Host 可以像 `local` 一样从 `OnModelCall` 和 `OnToolCall` 发 trace。会切换引擎的 Host 应从可持久模型和 Host 包装层发出，这样两个引擎产生相同形状的 trace（包括流式参数），A/B 比较只按 metadata 过滤。在参考运行器里，以被接受的 final text 结束的唤醒没有工具观测：看 `employee_decision`，不看工具列表。
