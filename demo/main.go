// Demo: Grok-Bot-style employee loop — instant ack, background tasks, registry + memory.
//
//	DEEPSEEK_API_KEY=... go run ./demo
//	DEEPSEEK_API_KEY=... go run ./demo -script   # scripted multi-message simulation
//
// Never commit a .env with a real key. Load from the environment or a local-only file.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/loop"
	"github.com/D1-2004/eino-employee-loop/store"
	"github.com/joho/godotenv"
)

func main() {
	script := flag.Bool("script", false, "run the built-in multi-message script instead of interactive stdin")
	db := flag.String("db", filepath.Join(os.TempDir(), "eino-employee-loop-demo.db"), "SQLite path")
	dotenv := flag.String("dotenv", ".env", "optional dotenv (gitignored); DEEPSEEK_API_KEY also accepted from the environment")
	jobMs := flag.Int("job-ms", 4000, "simulated background job duration")
	flag.Parse()
	_ = godotenv.Load(*dotenv)
	key, base := os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("DEEPSEEK_BASE_URL")
	if key == "" {
		fmt.Fprintln(os.Stderr, "set DEEPSEEK_API_KEY (and optionally DEEPSEEK_BASE_URL)")
		os.Exit(2)
	}
	ctx := context.Background()
	sq, err := store.OpenSQLite(*db)
	must(err)
	defer sq.Close()
	conv, err := loop.NewConvStore(sq, false)
	must(err)
	stats := loop.NewStats()
	cd := loop.DemoCase("")
	cfg := loop.DefaultConfig("demo", "scene-demo", key, base, cd)
	cfg.JobDelay = time.Duration(*jobMs) * time.Millisecond
	rt, err := loop.NewRuntime(ctx, cfg, sq, sq, store.NewMemKV(), conv, stats)
	must(err)
	defer rt.Stop()

	print := func(prefix string, evs []loop.OutEv, since float64) {
		for _, e := range evs {
			if e.TMs < since {
				continue
			}
			if e.Kind == "user_in" || e.Kind == "ack_latency" {
				continue
			}
			fmt.Printf("  [%s +%.0fms] %s\n", prefix, e.TMs, trunc(e.Text, 160))
		}
	}

	if *script {
		runScript(ctx, rt, print)
		return
	}
	fmt.Println("Eino Employee Loop demo (SQLite + MemKV). Type a message, or /quit /tasks /memory /stats")
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !in.Scan() {
			break
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "/quit":
			return
		case line == "/tasks":
			ts, _ := rt.ListTasks(ctx)
			for _, t := range ts {
				fmt.Printf("  %s [%s] %s\n", t.ID, t.Status, trunc(t.Goal, 80))
			}
		case line == "/memory":
			fs, _ := rt.ListMemory(ctx, "you", "me")
			fs2, _ := rt.ListMemory(ctx, "", "scene")
			for _, f := range append(fs, fs2...) {
				fmt.Printf("  [%s/%s] %s\n", f.Audience, f.Kind, trunc(f.Text, 100))
			}
		case line == "/stats":
			fmt.Printf("  counters=%v\n  store=%v\n", rt.Counters(), rt.StoreStats())
		default:
			since := float64(time.Since(time.Time{}).Milliseconds()) // unused; use outbox watermark
			before := len(rt.Outbox())
			rt.UserMessage(ctx, "you", line, 0)
			rt.WaitIdle(true, 120*time.Second)
			print("bot", rt.Outbox()[before:], 0)
			_ = since
		}
	}
}

func runScript(ctx context.Context, rt *loop.Runtime, printFn func(string, []loop.OutEv, float64)) {
	steps := []struct {
		who, text, wait string
	}{
		{"冬翔", "记住：以后查天气都用摄氏度，而且只要一句话", "turn"},
		{"冬翔", "查一下今天杭州的天气", "turn"},
		{"冬翔", "还在查吗", "turn"},
		{"冬翔", "查一下今天杭州的天气", "turn"}, // should dedup if prior still active/recent
		{"冬翔", "谢谢", "turn"},
	}
	for _, s := range steps {
		fmt.Printf("\nuser(%s): %s\n", s.who, s.text)
		before := len(rt.Outbox())
		rt.UserMessage(ctx, s.who, s.text, 0)
		if s.wait == "turn" {
			rt.WaitIdle(false, 90*time.Second)
		} else {
			rt.WaitIdle(true, 120*time.Second)
		}
		printFn("bot", rt.Outbox()[before:], 0)
		time.Sleep(200 * time.Millisecond)
	}
	rt.WaitIdle(true, 60*time.Second)
	fmt.Println("\n--- registry ---")
	ts, _ := rt.ListTasks(ctx)
	for _, t := range ts {
		fmt.Printf("%s [%s] %s → %s\n", t.ID, t.Status, trunc(t.Goal, 60), trunc(t.Result, 60))
	}
	fmt.Println("--- memory ---")
	fs, _ := rt.ListMemory(ctx, "冬翔", "me")
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
