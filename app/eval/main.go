// Command eval verifies replay and recovery through the local reference app.
// It owns no business loop: each action runs the same app/local executable.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type acceptance struct {
	root, fixtures, output, binary string
	timeout                        time.Duration
	env                            []string
	summary                        object
}

func (a *acceptance) command(name string, args []string, success bool) []any {
	stdout := filepath.Join(a.output, name+".stdout.ndjson")
	stderr := filepath.Join(a.output, name+".stderr.txt")
	record := object{"name": name, "argv": args, "stdout": filepath.Base(stdout), "stderr": filepath.Base(stderr), "expected_success": success}
	a.summary["commands"] = append(a.summary["commands"].([]any), record)
	out, err := os.Create(stdout)
	must(err)
	defer out.Close()
	eout, err := os.Create(stderr)
	must(err)
	defer eout.Close()
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = a.root
	cmd.Env = a.env
	cmd.Stdout = out
	cmd.Stderr = eout
	start := time.Now()
	err = cmd.Run()
	record["duration_seconds"] = float64(time.Since(start).Milliseconds()) / 1000
	code := 0
	if err != nil {
		code = -1
		record["process_error"] = err.Error()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	record["returncode"] = code
	if ctx.Err() != nil {
		record["timed_out"] = true
		panic(fmt.Errorf("%s exceeded %s", name, a.timeout))
	}
	// Failure expectations must be a command's own nonzero exit, not a start error.
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			panic(fmt.Errorf("%s could not execute: %w", name, err))
		}
	}
	if (code == 0) != success || (!success && code < 1) {
		panic(fmt.Errorf("%s: unexpected exit %d (%v); see %s", name, code, err, stderr))
	}
	if !success || name == "build" {
		return nil
	}
	b, err := os.ReadFile(stdout)
	must(err)
	rows := []any{}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		v := object{}
		must(json.Unmarshal([]byte(line), &v))
		rows = append(rows, v)
	}
	return rows
}
func (a *acceptance) cli(name, caseName, profile, script string, success bool, args ...string) []any {
	argv := []string{a.binary, "-profile", filepath.Join(a.fixtures, profile), "-dir", filepath.Join(a.output, caseName), "-script", filepath.Join(a.fixtures, script)}
	return a.command(name, append(argv, args...), success)
}
func discoverRoot() string {
	path, err := os.Getwd()
	must(err)
	for {
		if info, err := os.Stat(filepath.Join(path, "evals", "cases", "local")); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	path, err = os.Getwd()
	must(err)
	return path
}
func absolute(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		must(err)
		path = filepath.Join(home, path[2:])
	}
	value, err := filepath.Abs(path)
	must(err)
	return value
}
func prepareOutput(path string) (string, error) {
	if path == "" {
		var err error
		path, err = os.MkdirTemp("", "eel-local-acceptance-")
		if err != nil {
			return "", err
		}
	}
	path = absolute(path)
	if entries, err := os.ReadDir(path); err == nil {
		if len(entries) > 0 {
			return "", fmt.Errorf("--output must be empty; prior evidence will not be overwritten")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	// Retain the claim with the evidence. An empty-directory precheck alone
	// races with another evaluator preparing this same directory.
	claim, err := os.OpenFile(filepath.Join(path, ".eval-owner"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("--output already claimed or unavailable: %w", err)
	}
	if err := claim.Close(); err != nil {
		return "", err
	}
	return path, nil
}
func (a *acceptance) run() (code int) {
	code = 1
	defer func() {
		if err := recover(); err != nil {
			a.summary["error"] = fmt.Sprint(err)
		}
		jsonFile(filepath.Join(a.output, "summary.json"), a.summary)
		b, err := json.Marshal(object{"passed": a.summary["passed"], "summary": filepath.Join(a.output, "summary.json"), "error": a.summary["error"]})
		must(err)
		fmt.Println(string(b))
	}()
	entries, err := os.ReadDir(a.fixtures)
	must(err)
	for _, entry := range entries {
		if !entry.IsDir() {
			obj(a.summary["fixtures"])[entry.Name()] = digest(filepath.Join(a.fixtures, entry.Name()))
		}
	}
	if a.binary == "" {
		a.binary = filepath.Join(a.output, "local-employee")
		build := []string{"go", "build", "-o", a.binary, "./app/local"}
		a.summary["binary"] = object{"path": a.binary, "origin": "built-by-runner", "build_command": build}
		a.command("build", build, true)
	} else {
		a.summary["binary"] = object{"path": a.binary, "origin": "provided", "source_provenance": "unknown"}
	}
	obj(a.summary["binary"])["sha256"] = digest(a.binary)
	a.allLoops()
	a.interruption()
	a.summary["passed"] = true
	return 0
}
func mainCode(args []string) int {
	flags := flag.NewFlagSet("employee-eval", flag.ContinueOnError)
	root := flags.String("root", "", "checkout root (default: discover from working directory)")
	output := flags.String("output", "", "empty evidence directory (default: retained temporary directory)")
	binary := flags.String("binary", "", "existing local reference CLI (source provenance unknown)")
	fixtures := flags.String("fixtures", "", "fixture bundle (default: ROOT/evals/cases/local)")
	timeout := flags.Int("command-timeout", 180, "per-process timeout in seconds")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments or nonpositive --command-timeout")
		return 2
	}
	if *root == "" {
		*root = discoverRoot()
	}
	*root = absolute(*root)
	if *fixtures == "" {
		*fixtures = filepath.Join(*root, "evals", "cases", "local")
	}
	*fixtures = absolute(*fixtures)
	dir, err := prepareOutput(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	env := []string{}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if key != "LLM_BASE_URL" && key != "LLM_API_KEY" && key != "OPENAI_API_KEY" {
			env = append(env, kv)
		}
	}
	a := &acceptance{root: *root, fixtures: *fixtures, output: dir, timeout: time.Duration(*timeout) * time.Second, env: env,
		summary: object{"schema": "local-acceptance/1", "passed": false, "scope": object{"model": "scripted", "execution": "local-native", "state": "sqlite", "artifacts": "markdown", "transport": "local-file", "real_model": false, "real_im": false, "online_parity": "not established"}, "commands": []any{}, "checks": []any{}, "cases": object{}, "fixtures": object{}}}
	if *binary != "" {
		a.binary = absolute(*binary)
	}
	return a.run()
}
func main() { os.Exit(mainCode(os.Args[1:])) }
