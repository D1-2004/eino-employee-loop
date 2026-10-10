// Package local is one assembly form for eino-employee-loop.
//
// It wires Core ports with concrete backends suitable for a single machine:
//
//   - SQLite TaskStore + MemoryStore + Conversation + Checkpoint (KV)
//   - MemKV / RedisKV
//   - stdin IM provider
//   - LLM ModelProvider (DeepSeek / DashScope / OpenAI-compatible)
//   - OpenCode Session + JobExecutor
//   - Mock JobExecutor
//
// Core never imports this package. Commands assemble Stack into core.Runtime.
// Channel adapters (e.g. DingTalk) live under adapters/.
package local
