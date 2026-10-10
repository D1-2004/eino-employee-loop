// Package local is the all-in-one local stack for eino-employee-loop:
//
//   - SQLite TaskStore + MemoryStore + Conversation + Checkpoint (KV)
//   - MemKV / RedisKV
//   - stdin IM provider
//   - LLM ModelProvider (DeepSeek / DashScope / OpenAI-compatible)
//   - OpenCode Session + JobExecutor
//   - Mock JobExecutor
//
// Core never imports this package. Commands wire Stack / OpenSQLite into core.Runtime.
// Cloud and DingTalk adapters live under providers/dws (and future providers/*).
package local
