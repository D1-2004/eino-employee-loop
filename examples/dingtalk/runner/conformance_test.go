package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	loop "github.com/D1-2004/eino-employee-loop/kernel/decision"
	"github.com/D1-2004/eino-employee-loop/kernel/decision/hosttest"
	lt "github.com/D1-2004/eino-employee-loop/kernel/decision/looptest"
)

// The local employee is the reference Host of loop/hosttest: its chat Host,
// its wake configuration and the OpenAI model cmd/eel uses pass every check
// but ReusedID, which needs an effect that does not end the wake (the only
// effect here is the reply). The fake channel reports each send with
// hosttest.Committed.

// chatTarget is a chat Host for one wake answering a direct message.
func chatTarget() (hosttest.Target, *Employee) {
	ch := &fakeChannel{}
	e := &Employee{ID: "e1", Name: "Eel", Channel: ch}
	m := dm("m1", "hi")
	host := &chatHost{employee: e, message: m}
	return hosttest.Target{Host: host, Identity: loop.Identity{Employee: e.ID, Scene: m.ConversationID, Receipt: m.MessageID}, Effects: ch.count}, e
}

func replyCall(id, text string) loop.ToolCall {
	return loop.ToolCall{NativeToolCallID: id, Name: "reply", Arguments: map[string]any{"text": text}}
}

func TestChatHostConforms(t *testing.T) {
	target, _ := chatTarget()
	target.Effect(t, replyCall("c1", "hi"))
	target.Refused(t, replyCall("c2", "a second reply"))

	empty, _ := chatTarget()
	empty.Refused(t, replyCall("c1", "  "))
	unknown, _ := chatTarget()
	unknown.Refused(t, loop.ToolCall{NativeToolCallID: "c1", Name: "nope", Arguments: map[string]any{}})

	final, _ := chatTarget()
	final.FinalText(t, "hello")
}

func TestEmployeeWakesConform(t *testing.T) {
	messages := []*schema.Message{schema.SystemMessage("You are Eel."), schema.UserMessage(`{"message_id":"m1","sender":"Ann","text":"hi","direct":true}`)}
	reply := func(id, text string) lt.Call {
		return lt.Call{ID: id, Name: "reply", Args: map[string]any{"text": text}}
	}
	for _, c := range []struct {
		name  string
		steps []lt.Step
		want  loop.Decision
	}{
		{"reply tool", []lt.Step{lt.Tools(reply("c1", "hi"))}, loop.Decision{Kind: loop.Reply, Reply: "hi"}},
		{"final text", []lt.Step{lt.Text("hello")}, loop.Decision{Kind: loop.Reply, Reply: "hello"}},
		{"quiet", []lt.Step{lt.Tools(lt.Call{ID: "c1", Name: "stay_quiet", Args: map[string]any{"reason": "no question"}})}, loop.Decision{Kind: loop.Quiet}},
		{"refusal, then a reply", []lt.Step{lt.Tools(reply("c1", "  ")), lt.Tools(reply("c2", "ok"))}, loop.Decision{Kind: loop.Reply, Reply: "ok"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, e := chatTarget()
			target.Wake(t, e.config(nil), messages, c.steps, c.want)
		})
	}
}

// openAIProvider is a fake OpenAI-compatible chat completions endpoint that
// answers every request with one call to hosttest_read.
type openAIProvider struct {
	mu       sync.Mutex
	requests []hosttest.Request
	fail     bool
}

func (p *openAIProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	got := hosttest.Request{Forced: req.ToolChoice == "required"}
	for _, tool := range req.Tools {
		got.Tools = append(got.Tools, tool.Function.Name)
	}
	p.mu.Lock()
	p.requests = append(p.requests, got)
	fail := p.fail
	p.fail = false
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"finish_reason":"tool_calls",` +
		`"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"hosttest_read","arguments":"{\"ref\":\"r1\"}"}}]}}],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
}

func (p *openAIProvider) sent() []hosttest.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hosttest.Request(nil), p.requests...)
}

func (p *openAIProvider) failNext() {
	p.mu.Lock()
	p.fail = true
	p.mu.Unlock()
}

func TestOpenAIModelConforms(t *testing.T) {
	p := &openAIProvider{}
	srv := httptest.NewServer(p)
	defer srv.Close()
	// The model cmd/eel builds from -llm-base-url and -llm-model.
	chat, err := openaimodel.NewChatModel(t.Context(), &openaimodel.ChatModelConfig{BaseURL: srv.URL, APIKey: "test", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	hosttest.Model(t, chat, hosttest.Provider{Requests: p.sent, FailNext: p.failNext, Usage: &loop.Usage{PromptTokens: 5, CompletionTokens: 2}})
}
