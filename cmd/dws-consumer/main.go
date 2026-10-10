// DWS conversation consumer: poll one openConversationId via dws, feed the employee Runtime,
// send replies back with +messages-send. Intended to run on a machine that has `dws` logged in
// (e.g. the user's dxxh host). Does not hardcode secrets — pass --profile / DWS_PROFILE.
//
//	export DEEPSEEK_API_KEY=...
//	export DWS_PROFILE='corpId:userId'   // optional; default = current dws profile
//	go run ./cmd/dws-consumer --cid 'cid...' --db ./data/employee.db
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dingtalk-fde/eino-employee-loop/checkpoint"
	"github.com/dingtalk-fde/eino-employee-loop/core"
	"github.com/dingtalk-fde/eino-employee-loop/providers/im"
	"github.com/dingtalk-fde/eino-employee-loop/providers/llm"
	"github.com/dingtalk-fde/eino-employee-loop/providers/session/opencode"
	"github.com/dingtalk-fde/eino-employee-loop/providers/store"
	"github.com/dingtalk-fde/eino-employee-loop/providers/tools"
	"github.com/joho/godotenv"
)

func main() {
	cid := flag.String("cid", os.Getenv("DWS_CID"), "openConversationId (group) or openDingTalkId (dm)")
	kind := flag.String("kind", envOr("DWS_KIND", "group"), "group | dm")
	profile := flag.String("profile", os.Getenv("DWS_PROFILE"), "dws --profile (corpId:userId); empty = current")
	db := flag.String("db", filepath.Join("data", "employee.db"), "SQLite path")
	dotenv := flag.String("dotenv", ".env", "optional dotenv")
	poll := flag.Duration("poll", 8*time.Second, "DWS poll interval")
	self := flag.String("self-name", os.Getenv("DWS_SELF_NAME"), "skip inbound from these display names (comma-separated)")
	selfID := flag.String("self-id", os.Getenv("DWS_SELF_ID"), "skip inbound from these senderId/openDingTalkId (comma-separated)")
	tenant := flag.String("tenant", "dws", "tenant id")
	scene := flag.String("scene", "", "scene id (default = cid)")
	jobMs := flag.Int("job-ms", 4000, "simulated background job duration")
	execName := flag.String("executor", envOr("EXECUTOR", "mock"), "mock | opencode")
	ocDir := flag.String("opencode-dir", os.Getenv("OPENCODE_DIR"), "OpenCode working dir (sandbox) for --executor opencode")
	ocModel := flag.String("opencode-model", os.Getenv("OPENCODE_MODEL"), "optional OpenCode provider/model")
	flag.Parse()
	_ = godotenv.Load(*dotenv)

	if strings.TrimSpace(*cid) == "" {
		fmt.Fprintln(os.Stderr, "required: --cid or DWS_CID")
		os.Exit(2)
	}
	lcfg, lerr := llm.FromEnv()
	if lerr != nil {
		fmt.Fprintln(os.Stderr, lerr)
		os.Exit(2)
	}
	if *scene == "" {
		*scene = sanitizeScene(*cid)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := os.MkdirAll(filepath.Dir(*db), 0o755); err != nil && filepath.Dir(*db) != "." {
		// ok if Dir is "."
	}
	_ = os.MkdirAll(filepath.Dir(*db), 0o755)

	sq, err := store.OpenSQLite(*db)
	must(err)
	defer sq.Close()
	cps, err := checkpoint.Attach(sq.DB())
	must(err)
	conv, err := store.NewConvStore(sq, false)
	must(err)

	var selfNames []string
	for _, n := range strings.Split(*self, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			selfNames = append(selfNames, n)
		}
	}
	var selfIDs []string
	for _, n := range strings.Split(*selfID, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			selfIDs = append(selfIDs, n)
		}
	}

	prov := im.NewDWSProvider(im.DWSConfig{
		Profile:   *profile,
		PollEvery: *poll,
		Kind:      *kind,
		SelfNames: selfNames,
		SelfIDs:   selfIDs,
	})
	defer prov.Close()

	cd := core.DemoCase("")
	cfg := core.DefaultConfig(*tenant, *scene, llm.New(lcfg), cd)
	cfg.JobDelay = time.Duration(*jobMs) * time.Millisecond
	cfg.CheckPointStore = cps
	cfg.CheckpointID = *tenant + "|" + *scene
	switch *execName {
	case "opencode":
		cfg.Executor = tools.OpenCodeExecutor{Session: &opencode.Session{WorkDir: *ocDir, Model: *ocModel}, Scene: *scene}
	default:
		cfg.Executor = tools.MockExecutor{}
	}
	cfg.OnOutbound = func(ctx context.Context, kind, text string) {
		if kind == "ack_latency" || kind == "user_in" || kind == "suppressed_ack_dup" {
			return
		}
		// Skip stay_quiet / internal-only kinds that should not hit the group.
		if kind == "stay_quiet" {
			return
		}
		if err := prov.Send(ctx, *cid, text); err != nil {
			fmt.Fprintf(os.Stderr, "dws send: %v\n", err)
		}
	}

	rt, err := core.NewRuntime(ctx, cfg, sq, sq, store.NewMemKV(), conv, statsOrNil())
	must(err)
	defer rt.Stop()

	fmt.Printf("dws-consumer starting\n  cid=%s kind=%s profile=%q\n  db=%s checkpoint=%s\n",
		*cid, *kind, *profile, *db, cfg.CheckpointID)

	ch, err := prov.Subscribe(ctx, *cid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "hint: dws auth may be expired — run:")
		fmt.Fprintln(os.Stderr, "  python3 $HOME/.agents/skills/dws-env/scripts/dws_env.py as <identity> -- auth login --device")
		fmt.Fprintln(os.Stderr, "  or: dws --profile <corpId:userId> auth login --device")
		os.Exit(1)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("shutting down")
			return
		case in, ok := <-ch:
			if !ok {
				fmt.Println("inbound closed")
				return
			}
			fmt.Printf("← %s: %s\n", in.Speaker, trunc(in.Text, 120))
			rt.UserMessage(ctx, in.Speaker, in.Text, in.AtMs)
		}
	}
}

func statsOrNil() *core.Stats { return core.NewStats() }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func sanitizeScene(cid string) string {
	var b strings.Builder
	for _, r := range cid {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		return "scene"
	}
	return s
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
