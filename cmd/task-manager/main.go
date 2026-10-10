// Command task-manager runs the Task Manager service: an in-memory task
// manager with the echo executor, serving the Task Manager API over HTTP
// and reporting every task only by publishing execution events to the
// event ingress of the task's employee: -loops maps employees to their
// loops, and -loop receives the events of every other employee.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/D1-2004/eino-employee-loop/contract"
	"github.com/D1-2004/eino-employee-loop/internal/cli"
	"github.com/D1-2004/eino-employee-loop/taskmgr/executors"
	"github.com/D1-2004/eino-employee-loop/taskmgr/memtask"
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

func main() {
	con := cli.NewConsole()
	locale := flag.String("locale", os.Getenv(cli.EnvLocale), con.T("cmd.flag.locale"))
	serve := flag.String("serve", ":8082", con.T("cmd.task_manager.flag.serve"))
	loopURL := flag.String("loop", "", con.T("cmd.flag.loop"))
	loopsFlag := flag.String("loops", "", con.T("cmd.task_manager.flag.loops"))
	delay := flag.Duration("delay", time.Second, con.T("cmd.task_manager.flag.delay"))
	token := flag.String("token", os.Getenv(cli.EnvToken), con.T("cmd.flag.token"))
	dotenv := flag.String("dotenv", ".env", con.T("cmd.flag.dotenv"))
	flag.Parse()
	_ = godotenv.Load(*dotenv)
	con.SetLocale(*locale)
	loops := map[string]string{}
	for _, item := range cli.List(*loopsFlag) {
		emp, url, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(emp) == "" || strings.TrimSpace(url) == "" {
			con.Usage("cmd.task_manager.bad_loops", "item", item)
		}
		loops[strings.TrimSpace(emp)] = strings.TrimSpace(url)
	}
	if *loopURL == "" && len(loops) == 0 {
		con.Usage("cmd.missing_flag", "flag", "loop")
	}

	var opts []httpapi.Option
	if *token != "" {
		opts = append(opts, httpapi.WithToken(*token))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks := map[string]contract.EventSink{}
	for emp, url := range loops {
		sinks[emp] = httpapi.NewEventSinkClient(url, opts...)
	}
	var fallback contract.EventSink
	if *loopURL != "" {
		fallback = httpapi.NewEventSinkClient(*loopURL, opts...)
	}
	// Every event names its employee; route it to that employee's loop.
	route := contract.SinkResolver(func(employee string) (contract.EventSink, error) {
		if s, ok := sinks[employee]; ok {
			return s, nil
		}
		if fallback != nil {
			return fallback, nil
		}
		return nil, contract.ErrUnknownEmployee
	})
	tm := memtask.New(executors.EchoExecutor{Delay: *delay}, route,
		memtask.WithRejectHandler(func(e contract.Event, err error) {
			con.Warn("cmd.error", "error", e.ID+": "+err.Error())
		}))
	errc := make(chan error, 1)
	addr, err := cli.Serve(ctx, *serve, httpapi.NewTaskManagerHandler(tm, opts...), errc)
	if err != nil {
		con.Fail(err)
	}
	con.Say("cmd.task_manager.started", "addr", addr, "loop", strings.TrimSpace(*loopURL+" "+*loopsFlag))
	select {
	case <-ctx.Done():
	case err := <-errc:
		con.Warn("cmd.error", "error", err.Error())
	}
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tm.Close(cctx); err != nil {
		con.Warn("cmd.error", "error", err.Error())
	}
	con.Say("cmd.stopped", "service", "task-manager")
}
