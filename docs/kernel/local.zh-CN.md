> This guide covers the reusable kernel/example. For the application and M1–M7 ownership, see [ARCHITECTURE.md](../../ARCHITECTURE.md).

# 本地验证

[English](local.md)

`examples/dingtalk` 在你的机器上跑一个员工，接入真实钉钉，不部署 Host 也能端到端验证循环。它是验证用的：历史保存在内存中，不运行后台会话，工具只有 `reply` 和 `stay_quiet`。

## 模型

任意 OpenAI 兼容端点：

```bash
export LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
export LLM_API_KEY=...
export LLM_MODEL=qwen3.8-max
```

## 模式一：钉钉账号（dws）

员工*就是*一个已登录 [dws CLI](https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli) 的账号。它订阅该账号的个人 IM 事件——全部单聊（`--kind all-direct`）和所有 @ 它的群消息（`--kind at-me`）——并以该账号引用原消息回复。

```bash
go run ./examples/dingtalk -channel dws -profile <corpId:userId> -name <显示名> -locale zh-CN
```

注意：

- 用 Ctrl-C（SIGTERM）停止：dws 会取消它创建的订阅。
- 个人事件覆盖本组织成员的消息；跨组织单聊可能收不到。
- 账号自己发出的回复也会以事件回来；渠道会识别这类回声并忽略。

## 模式二：Stream 模式机器人

员工是一个经钉钉 Stream 模式连接的应用机器人（`/v1.0/im/bot/messages/get`）。大家直接给它发消息或在群里 @ 它；它通过每条消息的 session webhook 回复。

```bash
go run ./examples/dingtalk -channel robot -robot-client-id <AppKey> -robot-client-secret <AppSecret>
```

请使用没有其他连接的机器人：Stream 模式会把同一应用的消息分散到它的所有连接上。

## 路由

每条消息都会变成 `event.Event`（单聊或 @ 员工为 `message.addressed`，否则为 `message.observed`），经过 `event.Standard().Route`。被找的消息唤醒场域的前台会话（每个会话同一时刻只有一次唤醒）；被旁听的消息只记入历史，供下一次唤醒使用。

## Langfuse

设置 `LANGFUSE_BASE_URL`（或 `LANGFUSE_HOST`）、`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY` 后，每次唤醒成为一条 trace，命名与生产 Host 相同：

| 项 | 值 |
| --- | --- |
| trace | `employee_loop`，session = 会话 id，tags `agent-<id>`、`engine-eino` |
| generation | 每次模型请求一个 `employee_model`，含输入消息、输出和 token 用量 |
| tool | 每次工具调用一个（`reply`、`stay_quiet`） |
| metadata | `employee_loop_engine=eino`、`employee_contract_event/class/mode/lane/session`、`employee_decision`、`employee_model_calls` |
| environment | `LANGFUSE_ENVIRONMENT`，默认 `local` |

当前消息以带 `message_id` 的 JSON 发送，因此按「在首个 generation 的最后一条 user 消息里找消息 id」归属 trace 的工具无需修改即可使用。

## 本地调试

`-channel console` 在终端里跑同一套流程，不需要钉钉。每行是一条消息：`文本` 是你（`-user`）发的单聊；`#Name: 文本` 是 Name 发的、没有 @ 员工的群消息（只记入历史）；`@Name: 文本` 是 @ 了员工的群消息。管道输入会回显，读到 EOF 结束。

`-model-script <文件>` 用 JSON 数组里的响应代替 LLM，按顺序每次模型请求用一条：`{"tool_calls": [{"name": "reply", "arguments": {"text": "你好"}}]}`、`{"text": "普通文本回答"}` 或 `{"error": "服务商错误"}`。响应用完后再有请求，该次唤醒失败。它基于 Host 测试用的脚本化模型 `loop/looptest`，所以 `local` 会引入这个测试辅助包。

```bash
go run ./examples/dingtalk -channel console -model-script examples/dingtalk/runner/testdata/console.json -name Eel -user Ann -locale zh-CN < examples/dingtalk/runner/testdata/console.txt
```

`-v` 把每次路由、模型请求、工具调用和决定写到 stderr；`-bodies` 再加上消息文本、请求、响应和工具参数。

用 [Delve](https://github.com/go-delve/delve)（Go 1.26 需要 1.26 及以上）：

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
dlv debug -r stdin:examples/dingtalk/runner/testdata/console.txt ./examples/dingtalk -- -channel console -model-script examples/dingtalk/runner/testdata/console.json -locale zh-CN
```

测试：`go test -race ./...`（`local/console_test.go` 端到端跑控制台）。

## 跑评测集

把评测工具指向本地员工，而不是已部署的员工：员工就是那个 dws 账号（或机器人），它的 trace 在 `local` 环境、tag `agent-<id>` 下，演员像对普通同事一样给它发消息。需要后台工作、记忆或 Host API 的用例不在本地范围内；聊天行为类用例（直接回答、严格格式、拒绝、澄清、长度限制）可以直接用。
