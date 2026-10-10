package local

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/dingtalk-fde/eino-employee-loop/core"
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
}

// Open builds a local stack. Caller must Close.
func Open(ctx context.Context, opts StackOpts) (*Stack, error) {
	if opts.DBPath == "" {
		return nil, fmt.Errorf("local: DBPath required")
	}
	sq, err := OpenSQLite(opts.DBPath)
	if err != nil {
		return nil, err
	}
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
		IM: NewStdinProvider("stdin", opts.StdinSpeaker),
	}
	switch opts.Executor {
	case "opencode":
		sess := &Session{WorkDir: opts.OpenCodeDir, Model: opts.OpenCodeModel}
		st.Session = sess
		st.Executor = OpenCodeExecutor{Session: sess, Scene: scene}
	default:
		st.Executor = MockExecutor{}
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
	Field          core.Field
	Instruction    string
	JobDelay       time.Duration
	CheckpointID   string
	OnOutbound     func(context.Context, string, string)
	Events         core.EventSink
	AckMode        string
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
	cd := core.DemoCase(opts.Instruction)
	cfg := core.DefaultConfig(tenant, scene, s.Models, cd)
	cfg.Field = field
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
