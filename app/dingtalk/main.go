// Command eel runs one employee locally on the Eino Employee Loop, connected
// to DingTalk through a dws-logged-in account or a Stream Mode robot, or to
// this terminal.
//
//	eel -channel dws -profile <corpId:userId>
//	eel -channel robot -robot-client-id <id> -robot-client-secret <secret>
//	eel -channel console [-model-script <file.json>] [-v [-bodies]]
//
// The model is any OpenAI-compatible endpoint (LLM_BASE_URL, LLM_API_KEY,
// LLM_MODEL), or a scripted model (-model-script) for runs without an LLM.
// Langfuse tracing is on when LANGFUSE_* keys are set.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	local "github.com/D1-2004/eino-employee-loop/app/dingtalk/runner"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "eel:", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func run() error {
	channel := flag.String("channel", env("EEL_CHANNEL", "dws"), "dws (an account through the dws CLI), robot (a Stream Mode robot) or console (this terminal)")
	profile := flag.String("profile", env("EEL_DWS_PROFILE", ""), "dws account profile corpId:userId (channel dws)")
	clientID := flag.String("robot-client-id", env("EEL_ROBOT_CLIENT_ID", ""), "robot app client id (channel robot)")
	clientSecret := flag.String("robot-client-secret", env("EEL_ROBOT_CLIENT_SECRET", ""), "robot app client secret (channel robot)")
	user := flag.String("user", env("EEL_CONSOLE_USER", env("USER", "user")), "your name as the sender (channel console)")
	id := flag.String("id", env("EEL_EMPLOYEE_ID", ""), "employee id in traces (default local-<channel>)")
	name := flag.String("name", env("EEL_EMPLOYEE_NAME", ""), "employee name in the prompt")
	instructions := flag.String("instructions", env("EEL_INSTRUCTIONS", ""), "role instructions, or @file")
	locale := flag.String("locale", env("EEL_LOCALE", "en"), "prompt locale (en, zh-CN)")
	baseURL := flag.String("llm-base-url", env("LLM_BASE_URL", ""), "OpenAI-compatible base URL")
	modelName := flag.String("llm-model", env("LLM_MODEL", ""), "model name")
	script := flag.String("model-script", "", "JSON file of model responses to use instead of an LLM")
	verbose := flag.Bool("v", false, "debug log on stderr: per wake the routed event, model requests, tool calls and the decision")
	bodies := flag.Bool("bodies", false, "with -v, also log message texts, model requests and responses and tool arguments")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose || *bodies {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if strings.HasPrefix(*instructions, "@") {
		raw, err := os.ReadFile(strings.TrimPrefix(*instructions, "@"))
		if err != nil {
			return err
		}
		*instructions = string(raw)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var chat model.ToolCallingChatModel
	var permanent func(error) bool
	if *script != "" {
		s, err := local.LoadScript(*script)
		if err != nil {
			return err
		}
		chat, permanent, *modelName = s, s.Permanent, "script:"+*script
	} else {
		if *baseURL == "" || *modelName == "" || os.Getenv("LLM_API_KEY") == "" {
			return fmt.Errorf("set LLM_BASE_URL, LLM_MODEL and LLM_API_KEY, or use -model-script")
		}
		openai, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{BaseURL: *baseURL, APIKey: os.Getenv("LLM_API_KEY"), Model: *modelName})
		if err != nil {
			return err
		}
		chat = openai
	}
	if *id == "" {
		*id = "local-" + *channel
	}
	var ch local.Channel
	switch *channel {
	case "dws":
		ch = &local.DWS{Profile: *profile, Log: log}
	case "robot":
		ch = &local.Robot{ClientID: *clientID, ClientSecret: *clientSecret}
	case "console":
		display := *name
		if display == "" {
			display = *id
		}
		stat, err := os.Stdin.Stat()
		interactive := err == nil && stat.Mode()&os.ModeCharDevice != 0
		if interactive {
			fmt.Fprintln(os.Stderr, local.ConsoleUsage)
		}
		ch = &local.Console{In: os.Stdin, Out: os.Stdout, User: *user, Employee: display, Echo: !interactive}
	default:
		return fmt.Errorf("unknown channel %q", *channel)
	}
	tracer := local.TracerFromEnv()
	log.Info("eel.start", "channel", *channel, "employee", *id, "model", *modelName, "langfuse", tracer != nil)
	employee := &local.Employee{ID: *id, Name: *name, Instructions: *instructions, Locale: *locale, Model: chat, Permanent: permanent, ModelName: *modelName, Channel: ch, Source: *channel, Tracer: tracer, Log: log, LogBodies: *bodies}
	if err := employee.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
