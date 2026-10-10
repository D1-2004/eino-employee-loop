// Package opencode adapts the local OpenCode CLI into core.Session.
//
// Binary discovery (Mac Studio typical):
//
//	~/.nvm/versions/node/*/bin/opencode  or PATH
//
// Data:
//
//	~/.local/share/opencode/opencode.db
//	~/Library/Application Support/opencode/opencode.json
//
// CLI hooks used:
//
//	opencode run --title <title> --format json <prompt>     // create
//	opencode run -s <sessionID> --format json <prompt>     // resume
//	opencode session list
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/prototype/core"
)

// Session shells out to the local `opencode` binary. No API tokens are stored here.
type Session struct {
	Bin     string // empty = look up "opencode" on PATH
	WorkDir string // --dir for project context; empty = process cwd
	Model   string // optional provider/model
	// Locale and Messages select the bootstrap prompt pack (nil Messages = core.Messages()).
	Locale   string
	Messages *i18n.Bundle
	mu       sync.Mutex
	cache    map[string]string // scene -> sessionID
}

func (s *Session) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	if p, err := exec.LookPath("opencode"); err == nil {
		return p
	}
	return "opencode"
}

func (s *Session) Ensure(ctx context.Context, scene, title string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]string{}
	}
	if id, ok := s.cache[scene]; ok && id != "" {
		return id, nil
	}
	msgs := s.Messages
	if msgs == nil {
		msgs = core.Messages()
	}
	out, err := s.run(ctx, nil, title, msgs.Text(s.Locale, "executor.opencode.bootstrap", "scene", scene))
	if err != nil {
		return "", err
	}
	id := parseSessionID(out)
	if id == "" {
		id, err = s.newestByTitle(ctx, title)
		if err != nil {
			return "", err
		}
	}
	if id == "" {
		return "", fmt.Errorf("opencode: could not determine session id for scene %q", scene)
	}
	s.cache[scene] = id
	return id, nil
}

func (s *Session) Run(ctx context.Context, sessionID, prompt string) (string, error) {
	if sessionID == "" {
		return s.run(ctx, nil, "", prompt)
	}
	return s.Resume(ctx, sessionID, prompt)
}

func (s *Session) Resume(ctx context.Context, sessionID, prompt string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("opencode: empty session id")
	}
	return s.run(ctx, &sessionID, "", prompt)
}

func (s *Session) run(ctx context.Context, sessionID *string, title, prompt string) (string, error) {
	args := []string{"run", "--format", "json"}
	if s.WorkDir != "" {
		args = append(args, "--dir", s.WorkDir)
	}
	if s.Model != "" {
		args = append(args, "-m", s.Model)
	}
	if sessionID != nil && *sessionID != "" {
		args = append(args, "-s", *sessionID)
	}
	if title != "" {
		args = append(args, "--title", title)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, s.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("opencode run: %w (%s)", err, trunc(stderr.String(), 400))
	}
	return extractAssistant(stdout.String()), nil
}

func (s *Session) newestByTitle(ctx context.Context, title string) (string, error) {
	cmd := exec.CommandContext(ctx, s.bin(), "session", "list")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ses_") && (title == "" || strings.Contains(line, title)) {
			return strings.Fields(line)[0], nil
		}
	}
	return "", nil
}

func parseSessionID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		for _, k := range []string{"sessionID", "sessionId", "session_id", "id"} {
			if v, ok := m[k].(string); ok && strings.HasPrefix(v, "ses_") {
				return v
			}
		}
		if prop, ok := m["properties"].(map[string]any); ok {
			for _, k := range []string{"sessionID", "sessionId", "id"} {
				if v, ok := prop[k].(string); ok && strings.HasPrefix(v, "ses_") {
					return v
				}
			}
		}
	}
	if i := strings.Index(raw, "ses_"); i >= 0 {
		j := i
		for j < len(raw) && (isalnum(raw[j]) || raw[j] == '_') {
			j++
		}
		return raw[i:j]
	}
	return ""
}

func extractAssistant(raw string) string {
	var texts []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			if line != "" {
				texts = append(texts, line)
			}
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		typ, _ := m["type"].(string)
		if typ == "text" || typ == "message" || typ == "assistant" {
			if c, ok := m["content"].(string); ok && c != "" {
				texts = append(texts, c)
			}
			if c, ok := m["text"].(string); ok && c != "" {
				texts = append(texts, c)
			}
		}
	}
	if len(texts) == 0 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func isalnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ core.Session = (*Session)(nil)
