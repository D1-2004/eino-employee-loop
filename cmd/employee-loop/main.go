// Command employee-loop runs the Employee Loop service with in-memory
// storage. Its boundaries are chosen by flags:
//
//	-serve addr    HTTP event ingress (POST /v1/events) for gateways and the
//	               Task Manager
//	-tasks URL     remote Task Manager; default an in-process task manager
//	               with the echo executor, publishing into this loop
//	-deliver URL   remote deliverer (an Event Gateway); default printing
//	-stdin         a local scene fed by stdin lines
//
// The chat model comes from the environment (see providers/llm).
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/employeeloop"
	"github.com/D1-2004/eino-employee-loop/gateway/stdin"
	"github.com/D1-2004/eino-employee-loop/internal/cli"
	"github.com/D1-2004/eino-employee-loop/providers/llm"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
	"github.com/D1-2004/eino-employee-loop/taskmgr/memtask"
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

// localScene is the scene id of the stdin conversation.
const localScene = "local"

func main() {
	con := cli.NewConsole()
	locale := flag.String("locale", os.Getenv(cli.EnvLocale), con.T("cmd.flag.locale"))
	personaFile := flag.String("persona", "", con.T("cmd.employee_loop.flag.persona"))
	serve := flag.String("serve", "", con.T("cmd.employee_loop.flag.serve"))
	tasksURL := flag.String("tasks", "", con.T("cmd.employee_loop.flag.tasks"))
	deliverURL := flag.String("deliver", "", con.T("cmd.employee_loop.flag.deliver"))
	useStdin := flag.Bool("stdin", false, con.T("cmd.employee_loop.flag.stdin"))
	speaker := flag.String("speaker", "", con.T("cmd.employee_loop.flag.speaker"))
	employee := flag.String("employee", "employee", con.T("cmd.flag.employee"))
	tenant := flag.String("tenant", "local", con.T("cmd.flag.tenant"))
	token := flag.String("token", os.Getenv(cli.EnvToken), con.T("cmd.flag.token"))
	dotenv := flag.String("dotenv", ".env", con.T("cmd.flag.dotenv"))
	flag.Parse()
	_ = godotenv.Load(*dotenv)
	con.SetLocale(*locale)

	cfg, err := llm.FromEnv()
	if err != nil {
		con.Usage("cmd.employee_loop.model_config", "error", err.Error())
	}
	persona := ""
	if *personaFile != "" {
		raw, err := os.ReadFile(*personaFile)
		if err != nil {
			con.Usage("cmd.employee_loop.persona_read", "error", err.Error())
		}
		persona = string(raw)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	chat, err := llm.NewChatModel(ctx, cfg)
	if err != nil {
		con.Fail(err)
	}

	var opts []httpapi.Option
	if *token != "" {
		opts = append(opts, httpapi.WithToken(*token))
	}

	// The in-process task manager publishes into the loop, which needs the
	// task manager to exist first: the sink is bound once the loop exists.
	ingress := &boundSink{}
	var tasks contract.TaskManager
	var local *memtask.Manager
	tasksLabel := con.T("cmd.employee_loop.tasks_local")
	if *tasksURL != "" {
		tasks, tasksLabel = httpapi.NewTaskManagerClient(*tasksURL, opts...), *tasksURL
	} else {
		local = memtask.New(executors.EchoExecutor{Delay: time.Second}, ingress)
		tasks = local
	}

	loop, err := employeeloop.New(employeeloop.Config{
		Employee: *employee, Tenant: *tenant, Locale: *locale, Persona: persona, Model: chat, Tasks: tasks,
	})
	if err != nil {
		con.Fail(err)
	}
	ingress.sink = loop

	if *speaker == "" {
		*speaker = con.T("cmd.employee_loop.default_speaker")
	}
	term := stdin.New(stdin.Config{
		In: os.Stdin, Out: os.Stdout, Locale: *locale, Prompt: true,
		Scene: contract.SceneRef{ID: localScene, Locator: localScene},
		Actor: contract.Actor{ID: "local-user", DisplayName: *speaker}, Employee: *employee,
	})
	var deliverer contract.Deliverer = term
	deliverLabel := con.T("cmd.employee_loop.deliver_local")
	if *deliverURL != "" {
		deliverer, deliverLabel = httpapi.NewDelivererClient(*deliverURL, opts...), *deliverURL
	}

	errc := make(chan error, 4)
	serveLabel := con.T("cmd.employee_loop.serve_none")
	if *serve != "" {
		addr, err := cli.Serve(ctx, *serve, httpapi.NewEventHandler(loop, opts...), errc)
		if err != nil {
			con.Fail(err)
		}
		serveLabel = addr
	}

	worker := loop.Worker(deliverer)
	worker.OnError = func(err error) { con.Warn("cmd.error", "error", err.Error()) }
	done := make(chan struct{})
	go func() { _ = worker.Run(ctx); close(done) }()

	con.Say("cmd.employee_loop.started", "employee", *employee, "locale", con.Locale,
		"tasks", tasksLabel, "deliver", deliverLabel, "serve", serveLabel)
	if *useStdin {
		con.Say("cmd.employee_loop.stdin_hint")
		go func() {
			_ = term.Run(ctx, loop)
			stop()
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		con.Warn("cmd.error", "error", err.Error())
		stop()
	}
	<-done
	if local != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = local.Close(cctx)
		cancel()
	}
	con.Say("cmd.stopped", "service", "employee-loop")
}

// boundSink forwards to a sink bound after construction.
type boundSink struct{ sink contract.EventSink }

func (b *boundSink) Publish(ctx context.Context, e contract.Event) (contract.EventAck, error) {
	return b.sink.Publish(ctx, e)
}
