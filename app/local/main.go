// Command el opens the persistent local Employee Loop Entry. Its replay input
// and Markdown outbox are local simulation, never real DingTalk IM transport.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/D1-2004/eino-employee-loop/app/local/providers/channel"

	modelproviders "github.com/D1-2004/eino-employee-loop/app/local/providers/models"
	"github.com/D1-2004/eino-employee-loop/employee/contract"
	"github.com/D1-2004/eino-employee-loop/employee/entry"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "el:", err)
		os.Exit(1)
	}
}

func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 4*1024*1024 {
		return errors.New("configuration exceeds 4 MiB")
	}
	if err = contract.ValidateJSON(raw, 32); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (resultErr error) {
	flags := flag.NewFlagSet("el", flag.ContinueOnError)
	flags.SetOutput(errOut)
	checkProfile := flags.Bool("check-profile", false, "validate configuration and print provenance only; no model, state or transport initialization")
	sourceMode := flags.String("source-mode", "", "source provenance mode: dev or acceptance")
	sourceRevision := flags.String("source-revision", "", "exact clean 40-character source revision required for acceptance")
	profilePath := flags.String("profile", "", "immutable local-el-profile/1 JSON file (required)")
	promptBundlePath := flags.String("prompt-bundle", "", "external compiled native-role prompt bundle JSON (optional; no template rendering)")
	directory := flags.String("dir", "", "private SQLite/Markdown state directory (required)")
	scriptPath := flags.String("script", "", "stateless role/rule model JSON, instead of an LLM")
	timeout := flags.Duration("timeout", 2*time.Minute, "total bounded command duration")
	limit := flags.Int("limit", 256, "maximum synchronous drain steps")
	listPlugins := flags.Bool("list-plugins", false, "list trusted compiled plugin name/version pairs and exit")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: el -profile profile.json -dir state [-script rules.json] replay|submit|answer|signal|schedule|drain|status|owner-status|retry|serve [command flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listPlugins {
		if *checkProfile {
			return errors.New("check-profile and list-plugins are exclusive")
		}
		return json.NewEncoder(out).Encode(compiledPlugins)
	}
	if *checkProfile && flags.NArg() != 0 {
		return errors.New("check-profile takes no command")
	}
	if *profilePath == "" || (!*checkProfile && (*directory == "" || flags.NArg() < 1)) {
		flags.Usage()
		return errors.New("profile, dir and command required")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	var profile entry.Profile
	if err := readJSON(*profilePath, &profile); err != nil {
		if *checkProfile {
			return errors.New("profile: unreadable or invalid configuration")
		}
		return fmt.Errorf("profile: %w", err)
	}
	if err := loadPromptBundle(*promptBundlePath, &profile); err != nil {
		if *checkProfile {
			return errors.New("prompt bundle: unreadable or invalid compiled artifact")
		}
		return fmt.Errorf("prompt bundle: %w", err)
	}
	if err := profile.Validate(); err != nil {
		if *checkProfile {
			return errors.New("profile: invalid immutable configuration or prompt artifact")
		}
		return err
	}
	var preflight any
	if *checkProfile || *sourceMode != "" || *sourceRevision != "" {
		if profile.Backend != "standalone" || profile.PromptBundle == nil {
			return errors.New("preflight requires standalone profile and a compiled prompt bundle")
		}
		// This CLI supplies only builtin module factories. Use the same pure
		// selection validator as Entry.Open before constructing any resources.
		if err := entry.NewModuleRegistry().Validate(profile.Modules); err != nil {
			return errors.New("profile: local module is not registered")
		}
		for _, selected := range profile.Plugins {
			found := false
			for _, compiled := range compiledPlugins {
				if selected.Name == compiled.Name && selected.Version == compiled.Version {
					found = true
				}
			}
			if !found {
				return errors.New("profile: unregistered compiled plugin")
			}
		}
		source, err := localSourceManifest(*sourceMode, *sourceRevision)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(profile)
		preflight = map[string]any{"type": "profile_preflight", "profile_sha256": contract.Digest(raw), "prompt_provenance": promptProvenance(profile), "source_manifest": source, "runtime_checked": false}
		if *checkProfile {
			return json.NewEncoder(out).Encode(preflight)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	models, err := modelFactory(*scriptPath)
	if err != nil {
		return err
	}
	plugins, err := pluginRegistry(errOut)
	if err != nil {
		return err
	}
	e, err := Open(ctx, Options{Directory: *directory, Profile: profile, Models: models, Plugins: plugins})
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, e.Close()) }()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if preflight != nil {
		if err = enc.Encode(preflight); err != nil {
			return err
		}
	}
	if provenance := promptProvenance(profile); provenance != nil {
		if err = enc.Encode(map[string]any{"type": "prompt_bundle", "provenance": provenance}); err != nil {
			return err
		}
	}
	command := flags.Arg(0)
	cf := flag.NewFlagSet(command, flag.ContinueOnError)
	cf.SetOutput(errOut)
	scene := cf.String("scene", "", "configured scene (required when multiple scenes)")
	inputPath := cf.String("in", "-", "NDJSON input path, or - for stdin")
	id := cf.String("id", "", "stable original message ID")
	text := cf.String("text", "", "exact original human text")
	job := cf.String("job", "", "pending ChatLoop job ID")
	target := cf.String("target", "", "exact native pending target ID")
	nativeOwner := cf.String("native-owner", "", "DWS capture sender ID explicitly mapped to the profile owner")
	sceneMapJSON := cf.String("scene-map", "", "JSON object mapping DWS capture conversation IDs to configured scenes")
	eventFile := cf.String("event", "", "one source-bound replay message JSON file (signal)")
	condition := cf.String("condition", "", "exact existing schedule/wait condition (signal)")
	scheduleFile := cf.String("schedule", "", "local-el-schedule/1 JSON file (schedule)")
	if err = cf.Parse(flags.Args()[1:]); err != nil {
		return err
	}
	if cf.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *scene == "" && len(profile.Scenes) == 1 {
		*scene = profile.Scenes[0]
	}
	validScene := func() error {
		for _, s := range profile.Scenes {
			if s == *scene {
				return nil
			}
		}
		return errors.New("select a configured scene with -scene")
	}
	maps := map[string]string{}
	if *sceneMapJSON != "" {
		if err = contract.ValidateJSON([]byte(*sceneMapJSON), 4); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(*sceneMapJSON), &maps); err != nil {
			return err
		}
	}
	replayOptions := channel.ReplayOptions{Owner: profile.Owner, Scenes: profile.Scenes, NativeOwner: *nativeOwner, NativeScenes: maps}
	snapshot := func() error {
		dir, _ := filepath.Abs(*directory)
		for _, s := range profile.Scenes {
			if *scene != "" && s != *scene {
				continue
			}
			tasks, e1 := e.Tasks(ctx, s)
			pending, e2 := e.Pending(ctx, s)
			deliveries, e3 := e.Deliveries(ctx, s)
			failures, e4 := e.Failures(ctx, s)
			if err = errors.Join(e1, e2, e3, e4); err != nil {
				return err
			}
			tasks, err = displayTasks(tasks)
			if err != nil {
				return err
			}
			if err = enc.Encode(map[string]any{"type": "local_state", "transport": "local-replay/markdown", "directory": dir, "scene": s, "tasks": tasks, "pending": pending, "deliveries": deliveries, "failures": failures}); err != nil {
				return err
			}
		}
		return nil
	}
	replay := func() error {
		r := in
		if *inputPath != "-" {
			f, e := os.Open(*inputPath)
			if e != nil {
				return e
			}
			defer f.Close()
			r = f
		}
		count, e1 := channel.Replay(r, replayOptions, func(input entry.Input) error {
			a, e2 := e.Submit(ctx, input)
			if e2 != nil {
				return e2
			}
			return enc.Encode(map[string]any{"type": "admission", "message_id": input.ID, "admission": a})
		})
		if e1 != nil {
			return e1
		}
		return enc.Encode(map[string]any{"type": "replay_loaded", "count": count, "real_im": false})
	}
	switch command {
	case "replay":
		if err = replay(); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "submit":
		if err = validScene(); err != nil {
			return err
		}
		if *id == "" || strings.TrimSpace(*text) == "" {
			return errors.New("submit requires -id and -text")
		}
		a, e1 := e.Submit(ctx, entry.Input{ID: *id, Scene: *scene, Actor: profile.Owner, Text: *text, Origin: "local-cli"})
		if e1 != nil {
			return e1
		}
		if err = enc.Encode(map[string]any{"type": "admission", "admission": a}); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "answer":
		if err = validScene(); err != nil {
			return err
		}
		if *id == "" || strings.TrimSpace(*text) == "" || *job == "" || *target == "" {
			return errors.New("answer requires -id, -text, -job and -target")
		}
		if err = e.Resume(ctx, *job, entry.Input{ID: *id, Scene: *scene, Actor: profile.Owner, Text: *text, Origin: "local-cli"}, *target); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "retry":
		if *job == "" {
			return errors.New("retry requires -job (Task or admission ID)")
		}
		if err = e.Retry(ctx, *job); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "drain":
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "signal":
		if *eventFile == "" || strings.TrimSpace(*condition) == "" {
			return errors.New("signal requires -event and -condition")
		}
		raw, e1 := os.ReadFile(*eventFile)
		if e1 != nil {
			return e1
		}
		if len(raw) > 4*1024*1024 {
			return errors.New("signal event exceeds 4 MiB")
		}
		input, ok, e1 := channel.DecodeReplay(raw, replayOptions)
		if e1 != nil {
			return e1
		}
		if !ok {
			return errors.New("signal requires an original text message")
		}
		if err = e.Signal(ctx, input, *condition); err != nil {
			return err
		}
		if err = enc.Encode(map[string]any{"type": "signal_recorded", "id": input.ID, "scene": input.Scene, "condition": *condition, "real_im": false}); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "schedule":
		if err = validScene(); err != nil {
			return err
		}
		if *scheduleFile == "" {
			return errors.New("schedule requires -schedule")
		}
		var doc scheduleDocument
		if err = readJSON(*scheduleFile, &doc); err != nil {
			return err
		}
		proposal, e1 := doc.schedule()
		if e1 != nil {
			return e1
		}
		saved, e1 := e.Schedule(ctx, *scene, proposal)
		if e1 != nil {
			return e1
		}
		if err = enc.Encode(map[string]any{"type": "schedule_recorded", "scene": *scene, "schedule": saved, "real_im": false}); err != nil {
			return err
		}
		if err = e.Drain(ctx, *limit); err != nil {
			return err
		}
		return snapshot()
	case "owner-status":
		if *scene != "" {
			if err = validScene(); err != nil {
				return err
			}
		}
		for _, s := range profile.Scenes {
			if *scene != "" && *scene != s {
				continue
			}
			docs, e1 := e.OwnerDocuments(ctx, s)
			if e1 != nil {
				return e1
			}
			if err = enc.Encode(map[string]any{"type": "owner_documents", "scene": s, "documents": docs, "real_im": false}); err != nil {
				return err
			}
		}
		return nil
	case "status":
		if *scene != "" {
			if err = validScene(); err != nil {
				return err
			}
		}
		return snapshot()
	case "serve":
		if err = e.Start(ctx); err != nil {
			return err
		}
		if err = enc.Encode(map[string]any{"type": "started", "real_im": false, "input": "existing durable queue only", "timeout": timeout.String()}); err != nil {
			return err
		}
		return serveWorkerErrors(ctx, e.Errors(), enc)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

type scheduleDocument struct {
	Version      string      `json:"version"`
	ID           string      `json:"id"`
	TaskID       string      `json:"taskId"`
	GoalRevision uint64      `json:"goalRevision"`
	Source       work.Source `json:"source"`
	Condition    string      `json:"condition,omitempty"`
	Due          time.Time   `json:"due,omitempty"`
	Every        string      `json:"every"`
	Limit        int         `json:"limit"`
}

func (d scheduleDocument) schedule() (work.Schedule, error) {
	if d.Version != "local-el-schedule/1" || d.ID == "" || d.TaskID == "" || d.GoalRevision == 0 || d.Limit < 1 || d.Every == "" {
		return work.Schedule{}, errors.New("schedule: version, identity, goalRevision, every and explicit positive limit required")
	}
	every, err := time.ParseDuration(d.Every)
	if err != nil {
		return work.Schedule{}, err
	}
	return work.Schedule{ID: d.ID, TaskID: d.TaskID, GoalRevision: d.GoalRevision, Source: d.Source, Condition: d.Condition, Due: d.Due, Every: every, Limit: d.Limit}, nil
}

func modelFactory(script string) (entry.Models, error) {
	if script != "" {
		return loadRuleModels(script)
	}
	// Credentials are read by the selected Provider only when a model is used.
	return modelproviders.OpenAIEnvironment().Models, nil
}

// serveWorkerErrors keeps one failed job from terminating unrelated workers.
// Diagnostics are bounded even when a durable fault is reported every poll.
func serveWorkerErrors(ctx context.Context, workerErrors <-chan error, enc *json.Encoder) (resultErr error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var pending error
	count := 0
	var nextReport time.Time
	flush := func() error {
		if pending == nil {
			return nil
		}
		err := enc.Encode(map[string]any{"type": "worker_error", "error": pending.Error(), "occurrences": count, "real_im": false})
		pending, count = nil, 0
		nextReport = time.Now().Add(time.Second)
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, flush()) }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().Before(nextReport) {
				continue
			}
			if err := flush(); err != nil {
				return err
			}
		case workerErr, open := <-workerErrors:
			if !open {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("serve: worker error stream closed unexpectedly")
			}
			if workerErr == nil {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pending, count = workerErr, count+1
			if fatalWorkerError(workerErr) {
				return fmt.Errorf("serve: worker resource unavailable: %w", workerErr)
			}
			if !time.Now().Before(nextReport) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
}

func fatalWorkerError(err error) bool {
	// Inspect each joined branch: a transient first error must not hide a fatal
	// settlement failure from a different worker operation.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if fatalWorkerError(child) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if fatalWorkerError(wrapped.Unwrap()) {
			return true
		}
	}
	for _, fatal := range []error{sql.ErrConnDone, syscall.ENOSPC, syscall.EIO, syscall.EROFS, syscall.EBADF} {
		if errors.Is(err, fatal) {
			return true
		}
	}
	// SQLite extended result codes retain the primary code in the low byte.
	// Busy/locked/constraint and individual missing records remain recoverable.
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		switch sqliteError.Code() & 0xff {
		case 8, 10, 11, 13, 14, 21, 26: // READONLY, IOERR, CORRUPT, FULL, CANTOPEN, MISUSE, NOTADB
			return true
		}
	}
	return false
}

// localSourceManifest describes this executable, never a guessed checkout HEAD.
// Dirty/unstamped developer builds remain explicitly ineligible for acceptance.
type sourceManifest struct {
	Version            string `json:"version"`
	Mode               string `json:"mode"`
	BaseRevision       string `json:"base_revision,omitempty"`
	VerifiedRevision   string `json:"verified_source_revision,omitempty"`
	Modified           *bool  `json:"modified,omitempty"`
	BinarySHA256       string `json:"binary_sha256"`
	AcceptanceEligible bool   `json:"acceptance_eligible"`
}

func checkSourceSelection(mode, expected, revision string, modified *bool) error {
	if mode != "dev" && mode != "acceptance" {
		return errors.New("source-mode must be dev or acceptance")
	}
	validRevision := func(s string) bool {
		if len(s) != 40 {
			return false
		}
		for _, r := range s {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	if expected != "" && (!validRevision(expected) || expected != revision) {
		return errors.New("executable source revision does not match requested exact revision")
	}
	if mode == "acceptance" && (expected == "" || !validRevision(revision) || modified == nil || *modified) {
		return errors.New("acceptance requires an exact clean, VCS-stamped executable revision")
	}
	return nil
}
func localSourceManifest(mode, expected string) (sourceManifest, error) {
	if mode == "" {
		mode = "dev"
	}
	manifest := sourceManifest{Version: "local-el-source/1", Mode: mode}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				manifest.BaseRevision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" || setting.Value == "false" {
					value := setting.Value == "true"
					manifest.Modified = &value
				}
			}
		}
	}
	if err := checkSourceSelection(mode, expected, manifest.BaseRevision, manifest.Modified); err != nil {
		return manifest, err
	}
	file, err := os.Executable()
	if err != nil {
		return manifest, errors.New("source manifest: executable unavailable")
	}
	f, err := os.Open(file)
	if err != nil {
		return manifest, errors.New("source manifest: executable unreadable")
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return manifest, errors.New("source manifest: executable digest failed")
	}
	manifest.BinarySHA256 = hex.EncodeToString(hash.Sum(nil))
	if mode == "acceptance" {
		manifest.AcceptanceEligible = true
		manifest.VerifiedRevision = manifest.BaseRevision
	}
	return manifest, nil
}
