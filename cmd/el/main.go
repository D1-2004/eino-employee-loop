// Command el opens the persistent local Employee Loop Entry. Its replay input
// and Markdown outbox are local simulation, never real DingTalk IM transport.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/D1-2004/eino-employee-loop/channel"
	"github.com/D1-2004/eino-employee-loop/entry"
	"github.com/D1-2004/eino-employee-loop/internal/jsoncheck"
	"github.com/D1-2004/eino-employee-loop/work"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
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
	if err = jsoncheck.Unique(raw, 32); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (resultErr error) {
	flags := flag.NewFlagSet("el", flag.ContinueOnError)
	flags.SetOutput(errOut)
	profilePath := flags.String("profile", "", "immutable local-el-profile/1 JSON file (required)")
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
		return json.NewEncoder(out).Encode(compiledPlugins)
	}
	if *profilePath == "" || *directory == "" || flags.NArg() < 1 {
		flags.Usage()
		return errors.New("profile, dir and command required")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	var profile entry.Profile
	if err := readJSON(*profilePath, &profile); err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if err := profile.Validate(); err != nil {
		return err
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
	e, err := entry.Open(ctx, entry.Options{Directory: *directory, Profile: profile, Models: models, Plugins: plugins})
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, e.Close()) }()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
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
		if err = jsoncheck.Unique([]byte(*sceneMapJSON), 4); err != nil {
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
		workerErrors := e.Errors()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case workerErr, open := <-workerErrors:
				if !open {
					workerErrors = nil
					continue
				}
				if workerErr != nil {
					return fmt.Errorf("owned worker failed: %w", workerErr)
				}
			}
		}
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
	// Opening and reading persisted state do not need network credentials. A
	// model-consuming command reports missing credentials at the actual call.
	return func(ctx context.Context, request entry.ModelRequest) (model.ToolCallingChatModel, error) {
		base, key, name := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
		if base == "" || key == "" || name == "" {
			return nil, errors.New("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL, or use -script")
		}
		if name != request.ModelName {
			return nil, errors.New("LLM_MODEL must equal frozen profile.modelName")
		}
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: base, APIKey: key, Model: name})
	}, nil
}
