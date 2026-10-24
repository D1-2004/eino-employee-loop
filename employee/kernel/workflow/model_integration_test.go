//go:build workflowintegration

package workflow_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	filestore "github.com/D1-2004/eino-employee-loop/app/local/providers/sessionfile"
	loop "github.com/D1-2004/eino-employee-loop/employee/kernel/decision"
	"github.com/D1-2004/eino-employee-loop/employee/kernel/workflow"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// This acceptance checks the new public session API with a real model and
// local receipts only. It does not certify a Multica business Provider.
type liveProvider struct {
	model          model.ToolCallingChatModel
	dir            string
	calls, commits int
}

func (p *liveProvider) Authorize(_ context.Context, r workflow.Request) error {
	if r.Key.Workspace != "local-acceptance" || r.Key.Tenant != "local-owner" {
		return errors.New("unapproved local scope")
	}
	return nil
}
func (p *liveProvider) Prepare(_ context.Context, r workflow.Request, s workflow.Snapshot) (workflow.Prepared, error) {
	messages := []*schema.Message{schema.SystemMessage("You maintain a private conversation. When given a verification code, reply exactly STORED. When asked for that code, output only its exact value if it exists in this conversation; otherwise output exactly UNKNOWN. Never guess.")}
	messages = append(messages, s.History()...)
	messages = append(messages, r.Messages...)
	return workflow.Prepared{Config: loop.Config{OnModelCall: func(context.Context, loop.ModelCall) { p.calls++ }}, Model: p.model, Messages: messages}, nil
}
func (p *liveProvider) Commit(_ context.Context, r workflow.Request, out loop.Outcome) (string, error) {
	// A real private artifact is written and read back before a commit receipt.
	path := filepath.Join(p.dir, r.Receipt+".json")
	raw, err := json.Marshal(map[string]string{"receipt": r.Receipt, "reply": out.Reply})
	if err != nil {
		return "", err
	}
	if existing, e := os.ReadFile(path); e == nil {
		if string(existing) != string(raw) {
			return "", workflow.ErrConflict
		}
		return "local-file/" + r.Receipt, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		return "", err
	}
	readback, err := os.ReadFile(path)
	if err != nil || string(readback) != string(raw) {
		return "", errors.New("local receipt readback failed")
	}
	p.commits++
	return "local-file/" + r.Receipt, nil
}

func TestOwnModelMultiTurnSessionAcceptance(t *testing.T) {
	if os.Getenv("EEL_RUN_WORKFLOW_MODEL_CHECK") != "1" {
		t.Skip("set EEL_RUN_WORKFLOW_MODEL_CHECK=1 to use your configured model")
	}
	for _, k := range []string{"LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL"} {
		if os.Getenv(k) == "" {
			t.Fatalf("missing %s", k)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL"), Timeout: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := &liveProvider{model: m, dir: dir}
	registry := &workflow.Registry{}
	if err = registry.Register("foreground", "local-v1", p); err != nil {
		t.Fatal(err)
	}
	gate := func(_ context.Context, r workflow.Request) error { return p.Authorize(ctx, r) }
	r := &workflow.Runtime{Registry: registry, Store: filestore.Store{Dir: filepath.Join(dir, "sessions")}, Gate: gate}
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	code := hex.EncodeToString(nonce)
	request := func(receipt, scene, text string) workflow.Request {
		return workflow.Request{Key: workflow.Key{Workspace: "local-acceptance", Tenant: "local-owner", Employee: "local-employee", Scene: scene, Lane: "foreground", Subject: "interactive"}, Receipt: receipt, Kind: "foreground", Version: "local-v1", Messages: []*schema.Message{schema.UserMessage(text)}}
	}
	first := request("first", "scene-a", "My verification code is "+code)
	out, err := r.Run(ctx, first)
	if err != nil || strings.TrimSpace(out.Outcome.Reply) != "STORED" {
		t.Fatal(out, err)
	}
	// Restart the Runtime and load structured history from the private disk.
	r = &workflow.Runtime{Registry: registry, Store: r.Store, Gate: gate}
	out, err = r.Run(ctx, request("second", "scene-a", "What is my verification code?"))
	if err != nil || strings.TrimSpace(out.Outcome.Reply) != code {
		t.Fatal("multi-turn recall failed", out, err)
	}
	out, err = r.Run(ctx, request("isolated", "scene-b", "What is my verification code?"))
	if err != nil || strings.TrimSpace(out.Outcome.Reply) != "UNKNOWN" {
		t.Fatal("cross-scene isolation failed", out, err)
	}
	calls, commits := p.calls, p.commits
	out, err = r.Run(ctx, first)
	if err != nil || !out.Replay || p.calls != calls || p.commits != commits {
		t.Fatal("replay caused model or artifact side effect", out, err)
	}
	t.Logf("model=%s calls=%d durable_local_commits=%d; multi-turn/restart/isolation/replay passed", os.Getenv("LLM_MODEL"), p.calls, p.commits)
}
