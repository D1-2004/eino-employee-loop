package local

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/prototype/core"
	"github.com/cloudwego/eino/adk"
)

// Stack holds every local backend a demo / consumer needs.
type Stack struct {
	SQL        *SQLStore
	Checkpoint *Checkpoint
	KV         core.KV
	Conv       *ConvStore
	Models     core.ModelProvider
	Executor   core.JobExecutor
	Session    *Session // optional OpenCode
	IM         core.IMProvider
	// Locale and Messages select the packs for every text the stack produces.
	Locale   string
	Messages *i18n.Bundle
}

// StackOpts configures Open.
type StackOpts struct {
	DBPath        string
	LLM           Config
	Executor      string // mock | opencode
	OpenCodeDir   string
	OpenCodeModel string
	StdinSpeaker  string
	Scene         string // used by OpenCodeExecutor
	// Locale selects Host reply / prompt packs. Empty = $EMPLOYEE_LOCALE, else "en".
	Locale string
	// Messages overrides the locale packs (nil = core.Messages()).
	Messages *i18n.Bundle
}

// ResolveLocale returns explicit, else $EMPLOYEE_LOCALE, else i18n.Fallback.
func ResolveLocale(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("EMPLOYEE_LOCALE")); s != "" {
		return s
	}
	return i18n.Fallback
}

// Open builds a local stack. Caller must Close.
func Open(ctx context.Context, opts StackOpts) (*Stack, error) {
	if opts.DBPath == "" {
		return nil, fmt.Errorf("local: DBPath required")
	}
	locale := ResolveLocale(opts.Locale)
	msgs := opts.Messages
	if msgs == nil {
		msgs = core.Messages()
	}
	sq, err := OpenSQLite(opts.DBPath)
	if err != nil {
		return nil, err
	}
	sq.Locale, sq.Messages = locale, msgs
	cps, err := AttachCheckpoint(sq.DB())
	if err != nil {
		_ = sq.Close()
		return nil, err
	}
	conv, err := NewConvStore(sq, false)
	if err != nil {
		_ = sq.Close()
		return nil, err
	}
	scene := opts.Scene
	if scene == "" {
		scene = "local"
	}
	st := &Stack{
		SQL: sq, Checkpoint: cps, KV: NewMemKV(), Conv: conv, Models: New(opts.LLM),
		IM:     NewStdinProvider("stdin", opts.StdinSpeaker),
		Locale: locale, Messages: msgs,
	}
	switch opts.Executor {
	case "opencode":
		sess := &Session{WorkDir: opts.OpenCodeDir, Model: opts.OpenCodeModel, Locale: locale, Messages: msgs}
		st.Session = sess
		st.Executor = OpenCodeExecutor{Session: sess, Scene: scene}
	default:
		st.Executor = MockExecutor{Locale: locale, Messages: msgs}
	}
	_ = ctx
	return st, nil
}

// CheckPointStore exposes the Eino port.
func (s *Stack) CheckPointStore() adk.CheckPointStore { return s.Checkpoint }

// Close releases SQLite (checkpoint shares the same DB).
func (s *Stack) Close() error {
	if s == nil || s.SQL == nil {
		return nil
	}
	return s.SQL.Close()
}

// RuntimeOpts tunes core.Config when wiring.
type RuntimeOpts struct {
	Field        core.Field
	Instruction  string
	JobDelay     time.Duration
	CheckpointID string
	OnOutbound   func(context.Context, string, string)
	Events       core.EventSink
	AckMode      string
}

// WireRuntime builds a treat-mode Runtime on this stack.
func (s *Stack) WireRuntime(ctx context.Context, opts RuntimeOpts) (*core.Runtime, error) {
	field := opts.Field
	tenant := field.Provider
	if tenant == "" {
		tenant = "local"
	}
	scene := field.ID
	if scene == "" {
		scene = "default"
	}
	cd := core.DemoCase(s.Messages, s.Locale, opts.Instruction)
	cfg := core.DefaultConfig(tenant, scene, s.Models, cd)
	cfg.Field = field
	cfg.Locale, cfg.Messages = s.Locale, s.Messages
	if opts.JobDelay > 0 {
		cfg.JobDelay = opts.JobDelay
	}
	cfg.CheckPointStore = s.Checkpoint
	if opts.CheckpointID != "" {
		cfg.CheckpointID = opts.CheckpointID
	} else {
		cfg.CheckpointID = tenant + "|" + scene
	}
	cfg.Executor = s.Executor
	if s.Session != nil {
		cfg.Session = s.Session
		if oc, ok := cfg.Executor.(OpenCodeExecutor); ok {
			oc.Scene = scene
			cfg.Executor = oc
		}
	}
	cfg.OnOutbound = opts.OnOutbound
	cfg.Events = opts.Events
	if opts.AckMode != "" {
		cfg.AckMode = opts.AckMode
	}
	applyHostBudgetEnv(&cfg)
	return core.NewRuntime(ctx, cfg, s.SQL, s.SQL, s.KV, s.Conv, core.NewStats())
}

// applyHostBudgetEnv lets an assembly override Core Host turn-budget policy via env
// (MAX_MODEL_ROUNDS_PER_TURN, BUDGET_FALLBACK_DISPATCH). Empty = keep DefaultConfig.
func applyHostBudgetEnv(cfg *core.Config) {
	if v := strings.TrimSpace(os.Getenv("MAX_MODEL_ROUNDS_PER_TURN")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxModelRoundsPerTurn = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("BUDGET_FALLBACK_DISPATCH")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			cfg.BudgetFallbackDispatch = false
		case "1", "true", "on", "yes":
			cfg.BudgetFallbackDispatch = true
		}
	}
}
