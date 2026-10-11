# 事件与场域

[English](events.md)

员工由**场域中的事件**驱动。场域是工作发生的地方——群聊、单聊、企业空间——用 Host 签发的 `scene.id` 标识。事件是员工可能需要处理的任何事情。`event.Catalog.Route` 为每个事件决定*在哪里*处理；循环决定*做什么*。

## 分类

每个类型回答两个相互独立的问题。

**类别——发生了什么**

| 类别 | 覆盖 | 对应说法 |
| --- | --- | --- |
| `business` | 消息、表情回应、回答、成员变化、提供方资源 | 钉钉场域事件（业务事件） |
| `execution` | 在途工作：已受理、已开始、进展、被纠正 | 执行事件 |
| `completion` | 终态事实：一次执行的结果、终止状态 | 完成事件 |
| `schedule` | 时间：例行任务到点、旁听窗口到期、提醒 | 定时事件 |
| `enablement` | 启用、停用、绑定到场域、订阅或策略变化 | 启用事件 |

**性质——员工为什么收到它**

| 性质 | 含义 |
| --- | --- |
| `triggered`（被动触发） | 指向员工，或汇报员工自己的工作：@、私聊、回复员工、回答员工的问题、员工的任务结束。 |
| `listening`（主动监听） | 因为员工订阅、旁听或计时而收到：没有指向员工的群消息、提供方推送、例行任务、旁听窗口。 |

## 通道与会话

| 通道 | 会话 | 接收 | 是否唤醒模型 |
| --- | --- | --- | --- |
| `foreground`（前台） | `interactive`——每个场域一个 | 来自人的被动触发业务事件 | 是，串行；人的输入优先 |
| `background`（后台） | `observation`（每个场域一个，可抢占）、`follow_up`（每个任务一个）、`routine`（每个例行任务一个）、`lifecycle` | 主动监听事件、工作汇报、例行任务、生命周期决定 | 是，排队；从不占用交互轮次 |
| `host` | 无 | 回执、终态事实、配置版本 | 否，由 Host 直接应用 |

标准类型（`event/events.json`）：

| 类型 | 类别 | 性质 | 通道 / 会话 |
| --- | --- | --- | --- |
| `message.addressed` | business | triggered | foreground / interactive |
| `message.follow_up` | business | triggered | foreground / interactive |
| `human.response` | business | triggered | foreground / interactive |
| `reaction.added` | business | triggered | foreground / interactive |
| `message.observed` | business | listening | background / observation |
| `member.changed` | business | listening | background / observation |
| `resource.changed` | business | listening | background / observation |
| `execution.accepted` | execution | triggered | host |
| `execution.started` | execution | triggered | host |
| `execution.progress` | execution | triggered | background / follow_up（键 `task`） |
| `execution.steered` | execution | triggered | host |
| `execution.result` | completion | triggered | background / follow_up（键 `task`） |
| `execution.terminal` | completion | triggered | host |
| `routine.due` | schedule | listening | background / routine（键 `routine`） |
| `observation.due` | schedule | listening | background / observation |
| `employee.enabled` | enablement | triggered | host |
| `employee.disabled` | enablement | triggered | host（撤销 observation、lifecycle） |
| `subscription.changed` | enablement | triggered | host（撤销 observation） |
| `policy.changed` | enablement | triggered | host（撤销 observation） |
| `scene.bound` | enablement | triggered | background / lifecycle |

## 路由不变量

`TypeSpec.Validate` 与 `Catalog.Route` 强制：

1. 只有**被动触发的业务**事件进入**前台**，总在 `interactive` 会话中，行为者必须是人。
2. **主动监听**事件从不进入前台。
3. 前台窗口从不可抢占。
4. **host 通道**类型没有会话；其他通道总有会话。
5. 会话通道要求场域；只有员工级的 host 事件可以省略。
6. 带 `session_key` 的类型按该引用划分会话；缺少引用的事件被拒绝。
7. 未知类型被拒绝（`ErrUnknownType`）：从不猜测路由。生产者给出的 `class`/`mode` 必须与目录一致。

## 抢占与撤销

`Admission.Cancel` 列出 Host 必须在**受理的同一事务中**取消的内容：

- 带 `preempts` 的类型（有人找员工）取消该场域**可抢占**的窗口（`observation`）——有人找员工之后，进行中的旁听不会再发言；
- 带 `revokes` 的类型取消所列种类的全部窗口；没有场域时作用于该员工的所有场域。

被取消的旁听窗口不会恢复：下一个旁听窗口重新冻结上下文。这与主动群旁听的设计一致——无状态、可抢占、有独立发言预算的窗口，结果不确定的送达只核对、绝不重发。

## 合窗

带 `coalesce` 的类型让一串突发事件共享一次唤醒：窗口在最后一项之后 `quiet_ms`、第一项之后 `max_wait_ms`，或达到 `max_items` 时关闭。被找的消息与工作汇报合窗 250 ms（最多 1 秒、16 项）；被旁听的群消息合窗 12 秒（最多 30 秒、20 项）。这些数值是默认 profile，Host 重新注册该类型即可覆盖（见 [host.zh-CN.md](host.zh-CN.md#1-受理事件)）。

## Host 自定义类型

Host 在标准目录之上注册自己的类型：

```go
c := event.Standard()
err := c.Register(event.TypeSpec{
	Type: "plan.step_completed", Class: event.Completion, Mode: event.Triggered,
	Lane: event.Background, Session: event.FollowUp, SessionKey: event.RefTask, RequiresScene: true,
	Summary: map[string]string{"en": "A step of a plan finished.", "zh-CN": "计划中的一步已完成。"},
})
```

`Summary` 每个语言一行；`Describe(locale)` 依次回退到基础语言和英文。
