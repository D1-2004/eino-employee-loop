// Local demo: local.Stack (SQLite + checkpoint + stdin + mock) wired into core.Runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/prototype/core"
	"github.com/D1-2004/eino-employee-loop/prototype/local"
	"github.com/joho/godotenv"
)

func main() {
	script := flag.Bool("script", false, "run the built-in multi-message script instead of interactive stdin")
	db := flag.String("db", filepath.Join(os.TempDir(), "eino-employee-loop-demo.db"), "SQLite path")
	dotenv := flag.String("dotenv", ".env", "optional dotenv (gitignored)")
	jobMs := flag.Int("job-ms", 4000, "Host pacing before JobExecutor runs")
	cpID := flag.String("checkpoint-id", "local|demo", "TurnLoop CheckpointID")
	localeFlag := flag.String("locale", "", "reply/prompt locale (default $EMPLOYEE_LOCALE, else en)")
	speakerFlag := flag.String("speaker", "", "demo speaker name (default $DEMO_SPEAKER, else the locale's demo.speaker)")
	flag.Parse()
	_ = godotenv.Load(*dotenv)
	locale := local.ResolveLocale(*localeFlag)
	speaker := strings.TrimSpace(*speakerFlag)
	if speaker == "" {
		speaker = strings.TrimSpace(os.Getenv("DEMO_SPEAKER"))
	}
	if speaker == "" {
		speaker = core.Messages().Text(locale, "demo.speaker")
	}
	lcfg, err := local.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	stack, err := local.Open(ctx, local.StackOpts{DBPath: *db, LLM: lcfg, Executor: "mock", StdinSpeaker: speaker, Scene: "demo", Locale: locale})
	must(err)
	defer stack.Close()

	rt, err := stack.WireRuntime(ctx, local.RuntimeOpts{
		Field:        core.Field{ID: "demo", Kind: "conversation", Title: "local demo", Provider: "local"},
		JobDelay:     time.Duration(*jobMs) * time.Millisecond,
		CheckpointID: *cpID,
		OnOutbound: func(_ context.Context, kind, text string) {
			if kind == "ack_latency" || kind == "user_in" {
				return
			}
			_ = stack.IM.Send(context.Background(), "stdin", fmt.Sprintf("(%s) %s", kind, text))
		},
	})
	must(err)
	defer rt.Stop()

	fmt.Printf("Eino Employee Loop demo\n  db=%s\n  checkpoint=%s\n", *db, *cpID)
	if *script {
		runScript(ctx, rt, speaker, core.Messages().List(locale, "demo.script"))
		return
	}
	fmt.Println("Type a message, or /quit /tasks /memory /stats /checkpoint")
	ch, err := stack.IM.Subscribe(ctx, "stdin")
	must(err)
	defer stack.IM.Close()
	for in := range ch {
		line := strings.TrimSpace(in.Text)
		switch {
		case line == "/quit" || line == "/exit":
			return
		case line == "/tasks":
			ts, _ := rt.ListTasks(ctx)
			for _, t := range ts {
				fmt.Printf("  %s [%s] %s\n", t.ID, t.Status, trunc(t.Goal, 80))
			}
		case line == "/memory":
			fs, _ := rt.ListMemory(ctx, speaker, "me")
			fs2, _ := rt.ListMemory(ctx, "", "scene")
			for _, f := range append(fs, fs2...) {
				fmt.Printf("  [%s/%s] %s\n", f.Audience, f.Kind, trunc(f.Text, 100))
			}
		case line == "/stats":
			fmt.Printf("  counters=%v\n  store=%v\n", rt.Counters(), rt.StoreStats())
		case line == "/checkpoint":
			blob, ok, err := stack.Checkpoint.Get(ctx, *cpID)
			fmt.Printf("  id=%s ok=%v bytes=%d err=%v\n", *cpID, ok, len(blob), err)
		default:
			rt.UserMessage(ctx, in.Speaker, line, in.AtMs)
			rt.WaitIdle(true, 120*time.Second)
		}
	}
}

func runScript(ctx context.Context, rt *core.Runtime, speaker string, lines []string) {
	for _, text := range lines {
		fmt.Printf("\nuser(%s): %s\n", speaker, text)
		rt.UserMessage(ctx, speaker, text, 0)
		rt.WaitIdle(false, 90*time.Second)
		time.Sleep(200 * time.Millisecond)
	}
	rt.WaitIdle(true, 60*time.Second)
	fmt.Println("\n--- registry ---")
	ts, _ := rt.ListTasks(ctx)
	for _, t := range ts {
		fmt.Printf("%s [%s] %s → %s\n", t.ID, t.Status, trunc(t.Goal, 60), trunc(t.Result, 60))
	}
	fmt.Println("--- memory ---")
	fs, _ := rt.ListMemory(ctx, speaker, "me")
	for _, f := range fs {
		fmt.Printf("[%s] %s\n", f.Kind, f.Text)
	}
	fmt.Println("--- counters ---")
	fmt.Println(rt.Counters())
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
