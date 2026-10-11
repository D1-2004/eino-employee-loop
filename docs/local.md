# Local verification

[中文](local.zh-CN.md)

`cmd/eel` runs one employee on your machine against real DingTalk, so the loop
can be verified end to end without deploying a Host. It is a verification
harness: history is kept in memory, background sessions are not run and the
only tools are `reply` and `stay_quiet`.

## Model

Any OpenAI-compatible endpoint:

```bash
export LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
export LLM_API_KEY=...
export LLM_MODEL=qwen3.8-max
```

## Mode 1: a DingTalk account (dws)

The employee *is* an account the [dws CLI](https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli)
is logged in to. It subscribes to the account's personal IM events — every
direct message (`--kind all-direct`) and every group message that mentions it
(`--kind at-me`) — and answers as that account with a quote reply to the message.

```bash
go run ./cmd/eel -channel dws -profile <corpId:userId> -name <display name> -locale zh-CN
```

Notes:

- Stop with Ctrl-C (SIGTERM): dws then cancels the subscriptions it created.
- Personal events cover messages from the account's own organization; a
  cross-organization direct message may not be delivered.
- The account's own replies come back as events; the channel recognises the
  echo and ignores it.

## Mode 2: a Stream Mode robot

The employee is an app robot connected over DingTalk Stream Mode
(`/v1.0/im/bot/messages/get`). People message it directly or mention it in a
group; it answers through each message's session webhook.

```bash
go run ./cmd/eel -channel robot -robot-client-id <AppKey> -robot-client-secret <AppSecret>
```

Use a robot nothing else is connected to: Stream Mode spreads one app's
messages across all its connections.

## Routing

Every message becomes an `event.Event` (`message.addressed` when it is direct or
mentions the employee, `message.observed` otherwise) and goes through
`event.Standard().Route`. Addressed messages wake the scene's foreground
session (one wake at a time per conversation); observed messages are only
remembered as history for the next wake.

## Langfuse

With `LANGFUSE_BASE_URL` (or `LANGFUSE_HOST`), `LANGFUSE_PUBLIC_KEY` and
`LANGFUSE_SECRET_KEY` set, each wake becomes one trace with the same names a
production Host uses:

| Item | Value |
| --- | --- |
| trace | `employee_loop`, session = conversation id, tags `agent-<id>`, `engine-eino` |
| generation | `employee_model` per provider request, with input messages, output and token usage |
| tool | one per tool call (`reply`, `stay_quiet`) |
| metadata | `employee_loop_engine=eino`, `employee_contract_event/class/mode/lane/session`, `employee_decision`, `employee_model_calls` |
| environment | `LANGFUSE_ENVIRONMENT`, default `local` |

The current message is sent as JSON with its `message_id`, so tools that
attribute a trace to a message by looking for its id in the first generation's
last user message work unchanged.

## Local debugging

`-channel console` runs the same pipeline in the terminal, without DingTalk.
Each line is a message: `text` is a direct message from you (`-user`),
`#Name: text` a group message from Name that does not @ the employee (kept as
history), `@Name: text` one that does. Piped input is echoed and ends at EOF.

`-model-script <file>` replaces the LLM with a JSON array of responses, used in
order, one per model request. Each is one of
`{"tool_calls": [{"name": "reply", "arguments": {"text": "Hi"}}]}`,
`{"text": "a plain answer"}` or `{"error": "a provider error"}`; a request
after the last response fails its wake. It runs on `loop/looptest`, the
scripted model of Host tests, which is why `local` imports a test helper.

```bash
go run ./cmd/eel -channel console -model-script local/testdata/console.json -name Eel -user Ann -locale zh-CN < local/testdata/console.txt
```

`-v` logs each route, model request, tool call and decision to stderr;
`-bodies` adds message texts, requests, responses and tool arguments.

With [Delve](https://github.com/go-delve/delve) (1.26 or later for Go 1.26):

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
dlv debug -r stdin:local/testdata/console.txt ./cmd/eel -- -channel console -model-script local/testdata/console.json -locale zh-CN
```

Tests: `go test -race ./...` (`local/console_test.go` runs the console end to end).

## Running an eval suite

Point the eval harness at the local employee instead of a deployed one: the
employee is the dws account (or the robot), its traces are in the `local`
environment under tag `agent-<id>`, and the actors message it like any
colleague. Cases that need background work, memory or Host APIs are out of
scope for the local harness; chat-behaviour cases (direct answers, strict
formats, refusals, clarification, length limits) apply as they are.
