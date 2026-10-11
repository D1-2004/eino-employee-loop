package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/D1-2004/eino-employee-loop/loop"
)

// Tracer exports wakes to Langfuse through its public ingestion API, with
// the names Multica uses so the same trace views and eval tooling apply:
// a trace "employee_loop" per wake (session = conversation, tag
// "agent-<employee>"), one generation "employee_model" per provider request
// and one tool observation per tool call.
type Tracer struct {
	BaseURL, PublicKey, SecretKey string
	// Environment separates local traces from deployed ones.
	Environment string
	Client      *http.Client
}

// TracerFromEnv reads LANGFUSE_BASE_URL (or LANGFUSE_HOST),
// LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY and LANGFUSE_ENVIRONMENT
// (default "local"). It returns nil when a key is missing.
func TracerFromEnv() *Tracer {
	base := firstEnv("LANGFUSE_BASE_URL", "LANGFUSE_HOST")
	pk, sk := os.Getenv("LANGFUSE_PUBLIC_KEY"), os.Getenv("LANGFUSE_SECRET_KEY")
	if base == "" || pk == "" || sk == "" {
		return nil
	}
	env := os.Getenv("LANGFUSE_ENVIRONMENT")
	if env == "" {
		env = "local"
	}
	return &Tracer{BaseURL: strings.TrimRight(base, "/"), PublicKey: pk, SecretKey: sk, Environment: env, Client: &http.Client{Timeout: 15 * time.Second}}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Trace collects the observations of one wake; every method is a no-op on
// a nil Trace.
type Trace struct {
	tracer   *Tracer
	id       string
	started  time.Time
	mu       sync.Mutex
	body     map[string]any
	children []map[string]any
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Start opens a trace. A nil Tracer returns a nil Trace.
func (t *Tracer) Start(name, session, employee string, metadata map[string]any) *Trace {
	if t == nil {
		return nil
	}
	now := time.Now()
	id := newID()
	return &Trace{tracer: t, id: id, started: now, body: map[string]any{
		"id": id, "name": name, "sessionId": session, "timestamp": stamp(now),
		"tags": []string{"agent-" + employee, "engine-eino"}, "metadata": metadata, "environment": t.Environment,
	}}
}

// ID returns the trace id ("" for a nil Trace).
func (tr *Trace) ID() string {
	if tr == nil {
		return ""
	}
	return tr.id
}

func (tr *Trace) add(kind string, body map[string]any) {
	if tr == nil {
		return
	}
	body["id"], body["traceId"] = newID(), tr.id
	if tr.tracer.Environment != "" {
		body["environment"] = tr.tracer.Environment
	}
	tr.mu.Lock()
	tr.children = append(tr.children, map[string]any{"id": newID(), "type": kind, "timestamp": stamp(time.Now()), "body": body})
	tr.mu.Unlock()
}

// Generation records one provider request.
func (tr *Trace) Generation(name, model string, call loop.ModelCall) {
	if tr == nil {
		return
	}
	end := time.Now()
	body := map[string]any{
		"name": name, "model": model, "startTime": stamp(end.Add(-call.Elapsed)), "endTime": stamp(end),
		"input": call.Request, "metadata": map[string]any{"attempt": call.Attempt},
	}
	if resp := call.Response; resp != nil {
		// The OpenAI chat-completion shape, as a provider-recording Host logs it.
		finish := ""
		if meta := resp.ResponseMeta; meta != nil {
			finish = meta.FinishReason
			if meta.Usage != nil {
				body["usage"] = map[string]any{"input": meta.Usage.PromptTokens, "output": meta.Usage.CompletionTokens, "total": meta.Usage.TotalTokens, "unit": "TOKENS"}
			}
		}
		body["output"] = map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": resp}}}
	}
	if call.Err != nil {
		body["level"], body["statusMessage"] = "ERROR", call.Err.Error()
	}
	tr.add("generation-create", body)
}

// Tool records one Host call.
func (tr *Trace) Tool(call loop.ToolCall, result loop.ToolResult, err error, elapsed time.Duration) {
	if tr == nil {
		return
	}
	end := time.Now()
	body := map[string]any{
		"name": call.Name, "startTime": stamp(end.Add(-elapsed)), "endTime": stamp(end),
		"input": call.Arguments, "output": result, "metadata": map[string]any{"native_tool_call_id": call.NativeToolCallID},
	}
	if err != nil {
		body["level"], body["statusMessage"] = "ERROR", err.Error()
	}
	tr.add("tool-create", body)
}

// End closes the trace with its output and extra metadata and sends it.
func (tr *Trace) End(ctx context.Context, output any, metadata map[string]any) error {
	if tr == nil {
		return nil
	}
	tr.mu.Lock()
	meta, _ := tr.body["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	for k, v := range metadata {
		meta[k] = v
	}
	tr.body["metadata"], tr.body["output"] = meta, output
	batch := append([]map[string]any{{"id": newID(), "type": "trace-create", "timestamp": stamp(tr.started), "body": tr.body}}, tr.children...)
	tr.mu.Unlock()
	return tr.tracer.send(ctx, batch)
}

func (t *Tracer) send(ctx context.Context, batch []map[string]any) error {
	raw, err := json.Marshal(map[string]any{"batch": batch})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, t.BaseURL+"/api/public/ingestion", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.SetBasicAuth(t.PublicKey, t.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMultiStatus {
		return fmt.Errorf("langfuse: ingestion status %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Errors []map[string]any `json:"errors"`
	}
	if json.Unmarshal(body, &result) == nil && len(result.Errors) > 0 {
		return fmt.Errorf("langfuse: %d events rejected: %v", len(result.Errors), result.Errors[0])
	}
	return nil
}
