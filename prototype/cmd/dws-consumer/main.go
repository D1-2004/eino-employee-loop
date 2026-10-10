// DWS conversation consumer — DingTalk adapter + local stack + core.Runtime.
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

	"github.com/D1-2004/eino-employee-loop/prototype/adapters/dws"
	"github.com/D1-2004/eino-employee-loop/prototype/core"
	"github.com/D1-2004/eino-employee-loop/prototype/local"
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
	selfID := flag.String("self-id", os.Getenv("DWS_SELF_ID"), "skip inbound from these senderId (comma-separated)")
	tenant := flag.String("tenant", "dws", "Field.Provider / storage tenant")
	scene := flag.String("scene", "", "Field.ID (default = sanitized cid)")
	jobMs := flag.Int("job-ms", 4000, "Host pacing before JobExecutor")
	execName := flag.String("executor", envOr("EXECUTOR", "mock"), "mock | opencode")
	ocDir := flag.String("opencode-dir", os.Getenv("OPENCODE_DIR"), "OpenCode working dir")
	ocModel := flag.String("opencode-model", os.Getenv("OPENCODE_MODEL"), "optional OpenCode model")
	instr := flag.String("instruction", "", "override system instruction")
	seedHist := flag.Int("seed-history", 40, "seed recent DWS messages on first run (0=off)")
	transport := flag.String("transport", envOr("DWS_TRANSPORT", "stream"), "stream (dws event +listen-im) | poll (emergency only)")
	localeFlag := flag.String("locale", "", "reply/prompt locale (default $EMPLOYEE_LOCALE, else en)")
	allowPoll := flag.Bool("allow-poll-fallback", envOr("DWS_ALLOW_POLL_FALLBACK", "") == "1", "if stream keeps failing, eventually fall back to poll (default: never)")
	flag.Parse()
	_ = godotenv.Load(*dotenv)

	if strings.TrimSpace(*cid) == "" {
		fmt.Fprintln(os.Stderr, "required: --cid or DWS_CID")
		os.Exit(2)
	}
	lcfg, err := local.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *scene == "" {
		*scene = sanitizeScene(*cid)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	_ = os.MkdirAll(filepath.Dir(*db), 0o755)

	stack, err := local.Open(ctx, local.StackOpts{
		DBPath: *db, LLM: lcfg, Executor: *execName,
		OpenCodeDir: *ocDir, OpenCodeModel: *ocModel, Scene: *scene,
		Locale: *localeFlag,
	})
	must(err)
	defer stack.Close()

	var selfNames, selfIDs []string
	for _, n := range strings.Split(*self, ",") {
		if n = strings.TrimSpace(n); n != "" {
			selfNames = append(selfNames, n)
		}
	}
	for _, n := range strings.Split(*selfID, ",") {
		if n = strings.TrimSpace(n); n != "" {
			selfIDs = append(selfIDs, n)
		}
	}

	prov := dws.NewDWSProvider(dws.DWSConfig{
		Profile: *profile, PollEvery: *poll, Kind: *kind,
		SelfNames: selfNames, SelfIDs: selfIDs,
		Transport: *transport, AllowPollFallback: *allowPoll,
	})
	defer prov.Close()

	field := core.Field{
		ID: *scene, Kind: "conversation", Locator: *cid,
		Provider: *tenant, Title: "dws:" + *kind,
	}
	rt, err := stack.WireRuntime(ctx, local.RuntimeOpts{
		Field: field, Instruction: *instr,
		JobDelay: time.Duration(*jobMs) * time.Millisecond,
		OnOutbound: func(ctx context.Context, kind, text string) {
			if kind == "ack_latency" || kind == "user_in" || kind == "reaction_in" || kind == "suppressed_ack_dup" || kind == "stay_quiet" {
				return
			}
			if err := prov.Send(ctx, *cid, text); err != nil {
				fmt.Fprintf(os.Stderr, "dws send: %v\n", err)
			}
		},
		Events: logEvents{},
	})
	must(err)
	defer rt.Stop()

	fmt.Printf("dws-consumer starting\n  field=%s locator=%s kind=%s profile=%q\n  db=%s checkpoint=%s|%s\n  markdown_send=true ai_tag=false ack_mode=auto transport=%s allow_poll_fallback=%v locale=%s\n",
		field.ID, trunc(*cid, 40), *kind, *profile, *db, *tenant, *scene, *transport, *allowPoll, stack.Locale)

	if *seedHist > 0 {
		existing, _ := stack.Conv.Recent(ctx, *tenant, *scene, 0, 20)
		if len(existing) >= 8 {
			fmt.Printf("skip history seed: local conv already has %d msgs\n", len(existing))
		} else if hist, err := prov.FetchHistory(ctx, *cid, *seedHist); err != nil {
			fmt.Fprintf(os.Stderr, "seed history: %v\n", err)
		} else if n, err := rt.SeedFromHistory(ctx, hist, selfNames, selfIDs); err != nil {
			fmt.Fprintf(os.Stderr, "seed history append: %v\n", err)
		} else {
			fmt.Printf("seeded %d history msgs from DWS (fetched %d)\n", n, len(hist))
		}
	}

	ch, err := prov.Subscribe(ctx, *cid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe failed: %v\n", err)
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
			if in.Kind == "reaction" {
				fmt.Printf("← reaction %s by %s on %s\n", in.Reaction, in.Speaker, trunc(in.TargetMsgID, 24))
				if dws.NegativeReaction(in.Reaction, in.Text) {
					rt.ReactionFeedback(ctx, in)
				} else {
					fmt.Printf("  (ignored non-negative reaction)\n")
				}
				continue
			}
			fmt.Printf("← %s: %s\n", in.Speaker, trunc(in.Text, 120))
			rt.UserMessage(ctx, in.Speaker, in.Text, in.AtMs)
			go markHandled(prov, *cid, in.MsgID)
		}
	}
}

type logEvents struct{}

func (logEvents) Emit(_ context.Context, ev core.ProcessEvent) error {
	fmt.Printf("event kind=%s task=%s receipt=%s text=%s\n", ev.Kind, ev.TaskID, ev.Receipt, trunc(ev.Text, 80))
	return nil
}

func markHandled(prov *dws.DWSProvider, cid, msgID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := prov.MarkRead(ctx, cid, msgID); err != nil {
		fmt.Fprintf(os.Stderr, "mark-read: %v\n", err)
	}
	if err := prov.ClearRedPoint(ctx, cid); err != nil {
		fmt.Fprintf(os.Stderr, "clear-red-point: %v\n", err)
	}
}

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
