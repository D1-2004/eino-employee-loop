package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/cloudwego/eino/schema"
)

func TestOpenAIEnvironmentBoundsHungHTTP(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		close(started)
		defer close(exited)
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("LLM_BASE_URL", server.URL+"/v1")
	t.Setenv("LLM_API_KEY", "fake-local")
	t.Setenv("LLM_REQUEST_TIMEOUT", "100ms")
	m, err := openAI(context.Background(), contract.ModelRequest{ModelName: "frozen"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not start")
	}
	select {
	case err = <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request ignored configured timeout")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP transport retained the timed-out request")
	}
}
func TestOpenAIEnvironmentRejectsInvalidTimeoutBeforeHTTP(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("LLM_API_KEY", "fake-local")
	for _, value := range []string{"0", "0s", "-1s", "invalid"} {
		t.Setenv("LLM_REQUEST_TIMEOUT", value)
		if _, err := openAI(context.Background(), contract.ModelRequest{ModelName: "frozen"}); err == nil {
			t.Fatalf("accepted timeout %q", value)
		}
	}
}
