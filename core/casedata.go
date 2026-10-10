package core

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MinimalTools returns a small tool set for a Grok-Bot-style demo
// (ack → dispatch → result → reply), without the full 31-tool catalog.
func MinimalTools() ([]*schema.ToolInfo, map[string]bool) {
	falseV := false
	obj := func(props map[string]any, req []any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": falseV, "properties": props, "required": req}
	}
	toolsAny := []any{
		fn("find_tasks", "Find earlier tasks in this conversation (Host registry).", obj(map[string]any{
			"query": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("read_task", "Read Host-recorded status/result of one task_ref.", obj(map[string]any{
			"task_ref": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref"})),
		fn("dispatch_task", "Create a real background task. Returns an async receipt; the result arrives later as a Host event.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "goal": map[string]any{"type": "string"},
			"prompt": map[string]any{"type": "string"},
			"reply":  map[string]any{"type": "string", "description": "One-line confirmation of what you are starting."},
		}, []any{"source_ref", "goal", "prompt", "reply"})),
		fn("reply", "Reply directly using current facts, then finish without creating a task.", obj(map[string]any{
			"reply": map[string]any{"type": "string", "description": "DingTalk markdown body: short, conclusion first; no tables."},
			"source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "reply"})),
		fn("ask_human", "Ask the user one brief clarification, then wait.", obj(map[string]any{
			"summary": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "summary"})),
		fn("stay_quiet", "Record that this window needs no response.", map[string]any{"type": "object"}),
		fn("memory_capture", "Record a fact the sender explicitly asks to remember.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"type": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "quote": map[string]any{"type": "string"},
		}, []any{"source_ref", "audience", "type", "subject", "quote"})),
		fn("memory_lookup", "Look up memory when the brief lacks the fact.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "scope": map[string]any{"type": "string", "enum": []any{"me", "scene"}},
			"query": map[string]any{"type": "string"},
		}, []any{"source_ref", "scope", "query"})),
		fn("memory_forget", "Archive one memory record the sender asks to remove.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "record_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "record_ref"})),
		fn("stop_task", "Stop the requester's own source-bound task.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"}, "task_ref": map[string]any{"type": "string"},
		}, []any{"source_ref", "task_ref"})),
		fn("read_context", "Read older dialogue already frozen for this wake.", obj(map[string]any{
			"source_ref": map[string]any{"type": "string"},
			"section":    map[string]any{"type": "string", "enum": []any{"older_conversation", "capability_details"}},
		}, []any{"source_ref", "section"})),
	}
	tools, err := toolsFromCase(toolsAny)
	if err != nil {
		panic(err)
	}
	term := map[string]bool{"reply": true, "ask_human": true, "stay_quiet": true, "dispatch_task": true, "stop_task": true}
	return tools, term
}

func fn(name, desc string, params map[string]any) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": desc, "parameters": params}}
}

// DemoCase builds CaseData for the interactive / scripted demo.
// System text resolution: non-empty system arg > SYSTEM_INSTRUCTION / EINO_SYSTEM_INSTRUCTION env > DefaultColleagueInstruction.
func DemoCase(system string) CaseData {
	system = ResolveSystemInstruction(system)
	tools, term := MinimalTools()
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	wd := int((now.Weekday() + 6) % 7)
	clk := fmt.Sprintf(`Frozen clock: {"at": %q, "today": %q, "yesterday": %q, "week_start": %q, "week_end": %q}`,
		now.Format(time.RFC3339Nano), now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02"),
		now.AddDate(0, 0, -wd).Format("2006-01-02"), now.AddDate(0, 0, 6-wd).Format("2006-01-02"))
	return CaseData{System: system, Clock: clk, ClockAtMs: now.UnixMilli(), Tools: tools, Terminal: term}
}

// ResolveSystemInstruction picks instruction text: explicit > env > default Chinese colleague prompt.
func ResolveSystemInstruction(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	for _, k := range []string{"SYSTEM_INSTRUCTION", "EINO_SYSTEM_INSTRUCTION"} {
		if s := strings.TrimSpace(os.Getenv(k)); s != "" {
			return s
		}
	}
	return DefaultColleagueInstruction
}

// DefaultColleagueInstruction is the default DingTalk colleague system prompt (Chinese).
const DefaultColleagueInstruction = `你是钉钉里的同事，帮人把当前工作办完。

结合当前外层原话、引用和最近消息的发言人、顺序、间隔理解意图。人纠正你的日期、对象或表达时，承接同一件事；问候和普通确认不复活旧事项。请求人、引用作者、被提到的人和你是不同角色，引用附带的 @ 不改变请求人。对象明确就办，确有不同解释才问一句，不重复确认已给的授权。

Shared 工具和后台沙箱都是你的能力。沙箱有独立执行环境，可运行命令、处理文件、按实际装配发现 Skill/MCP。前台没有专用工具不等于做不到；新查询或操作按 Host 的工具、权限与执行合同直接做或交给后台尝试。解读先读原文，不凭标题编正文。问答或改写且材料足够时直接答；只在决策缺细节时补读。

进展、继续和停止先读对应工作，不重复开工。替人询问同事并收集回答，使用已提供的协同能力，保留对象、问题与期限。只说有依据的话：员工旧发言不证明已执行，总结时保留“当时回复说”的归属；人设不是群成员名单。空缓存、截断、未披露和没有结果不同，未知保持未知，身份推断不说成已用 ID 确认，不编事实、进度或送达。

像同事说话，简短自然，结论在前，遵守当前要求的格式，不用钉钉不支持的表格。内部标识仅在明确审计要求且 Host 允许时提供；凭据不读取或打印。不添加未授权的发送、删除、权限变更或其他目标；只确认尚未明确的风险部分，不创建或修改别的数字员工。

承接放在真实受理动作的回复里。有 report_progress 时只报告有意义的新进展，后台结果交给 Host，不重复发受理、进度或结果。用户明确要求的消息和文件按授权目标交付，有回执才说送达。截断内容读完必要部分，技能按实际装配按需读取。做完给结论，受阻说明实际原因和下一步。

HOST 补充（权威）：
- 用户可见回复走钉钉 Markdown：标题、列表、加粗；禁止表格。
- 禁止套话「我看下 / 我看下进度 / 我看下记忆这块」；问进度或记忆直接答，不要先发空确认。
- 同一请求最多一条受理确认：优先写进 dispatch_task.reply；已秒回过就不要再发第二句「收到」。
- 派活用 dispatch_task；问进度先看 Host 任务登记，勿重复派发。
- 只有 Host 登记的 receipt/result 才能说「已完成/已送达」；默认不把 task_ref/receipt 甩给用户。
- memory_capture 仅在对方明确要求记住时调用；quote 须是原话摘录；置顶偏好默认生效。
- 后台进展由 Host 事件通道投递；你不要每一步都刷屏。
- 简单问答（算术、是非、定义、一句话事实）：直接 reply 短答（一两行内），禁止工具、禁止复述旧话题/记忆/任务清单。例：「1+1」→「2」。
- 纯寒暄（Hi/你好/早）：一句问候即可；禁止翻出天气、任务、记忆或昨天进度。
- 对象或意图含糊（单字「调度」等）：ask_human 问一句，禁止直接派活。
- Host 已发去重提示时：不要再写长篇去重说明，也不要复读任务 ID。
`
