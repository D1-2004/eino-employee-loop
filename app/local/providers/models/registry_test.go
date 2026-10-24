package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestExactProviderAndEffortRejectedBeforeIO(t *testing.T) {
	reg := New(nil)
	count := 0
	f := func(_ context.Context, r contract.ModelRequest) (model.ToolCallingChatModel, error) {
		count++
		return nil, nil
	}
	ref := Ref{Name: "custom", Version: "2"}
	if err := reg.Register(ref, []string{"high"}, f); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(ref, []string{"high"}, f); err == nil {
		t.Fatal("duplicate accepted")
	}
	p := contract.Profile{ModelName: "old", ModelBindings: map[string]contract.ModelBinding{"review": {Provider: "custom", Version: "1", Model: "reasoner", Effort: "high"}}}
	req := contract.ModelRequest{Role: "review", Profile: p}
	if _, err := reg.Models(context.Background(), req); err == nil || count != 0 {
		t.Fatal("unknown version invoked", err, count)
	}
	p.ModelBindings["review"] = contract.ModelBinding{Provider: "custom", Version: "2", Model: "reasoner", Effort: "low"}
	if _, err := reg.Models(context.Background(), req); err == nil || count != 0 {
		t.Fatal("unsupported effort invoked", err, count)
	}
	p.ModelBindings["review"] = contract.ModelBinding{Provider: "custom", Version: "2", Model: "reasoner", Effort: "high"}
	if _, err := reg.Models(context.Background(), req); err != nil || count != 1 {
		t.Fatal(err, count)
	}
}

func TestOpenAIProviderTransmitsFrozenModelAndEffort(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	t.Setenv("LLM_BASE_URL", server.URL+"/v1")
	t.Setenv("LLM_API_KEY", "local-test")
	t.Setenv("LLM_MODEL", "changed-environment-model")
	p := contract.Profile{ModelName: "legacy", ModelBindings: map[string]contract.ModelBinding{"executor": {Provider: OpenAIRef.Name, Version: OpenAIRef.Version, Model: "frozen-reasoner", Effort: "high"}}}
	m, err := OpenAIEnvironment().Models(context.Background(), contract.ModelRequest{Role: "executor", Profile: p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "frozen-reasoner" || payload["reasoning_effort"] != "high" {
		t.Fatal(payload)
	}
}
