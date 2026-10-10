// Command dws-gateway runs the DingTalk Event Gateway for one conversation:
// it listens through the dws CLI, normalizes messages into events for the
// Employee Loop's ingress (-loop) and serves the delivery API (-serve) that
// sends the loop's replies back through dws.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/D1-2004/eino-employee-loop/gateway/dws"
	"github.com/D1-2004/eino-employee-loop/internal/cli"
	"github.com/D1-2004/eino-employee-loop/transport/httpapi"
)

func main() {
	con := cli.NewConsole()
	locale := flag.String("locale", os.Getenv(cli.EnvLocale), con.T("cmd.flag.locale"))
	cid := flag.String("cid", os.Getenv("DWS_CID"), con.T("cmd.dws_gateway.flag.cid"))
	kind := flag.String("kind", envOr("DWS_KIND", dws.KindGroup), con.T("cmd.dws_gateway.flag.kind"))
	profile := flag.String("profile", os.Getenv("DWS_PROFILE"), con.T("cmd.dws_gateway.flag.profile"))
	selfID := flag.String("self-id", os.Getenv("DWS_SELF_ID"), con.T("cmd.dws_gateway.flag.self_id"))
	selfName := flag.String("self-name", os.Getenv("DWS_SELF_NAME"), con.T("cmd.dws_gateway.flag.self_name"))
	transport := flag.String("transport", envOr("DWS_TRANSPORT", dws.TransportStream), con.T("cmd.dws_gateway.flag.transport"))
	loopURL := flag.String("loop", "", con.T("cmd.flag.loop"))
	serve := flag.String("serve", ":8083", con.T("cmd.dws_gateway.flag.serve"))
	namespace := flag.String("namespace", "dws", con.T("cmd.dws_gateway.flag.namespace"))
	tenant := flag.String("tenant", "", con.T("cmd.flag.tenant"))
	employee := flag.String("employee", os.Getenv("EMPLOYEE_ID"), con.T("cmd.flag.employee"))
	reactions := flag.String("reactions", os.Getenv("DWS_REACTIONS"), con.T("cmd.dws_gateway.flag.reactions"))
	token := flag.String("token", os.Getenv(cli.EnvToken), con.T("cmd.flag.token"))
	dotenv := flag.String("dotenv", ".env", con.T("cmd.flag.dotenv"))
	flag.Parse()
	_ = godotenv.Load(*dotenv)
	con.SetLocale(*locale)
	if *cid == "" {
		con.Usage("cmd.missing_flag", "flag", "cid")
	}
	if *loopURL == "" {
		con.Usage("cmd.missing_flag", "flag", "loop")
	}

	var opts []httpapi.Option
	if *token != "" {
		opts = append(opts, httpapi.WithToken(*token))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	negative := dws.DefaultLexicon().Negative
	if *reactions != "" {
		raw, err := os.ReadFile(*reactions)
		if err != nil {
			con.Fail(err)
		}
		var cfg dws.LexiconConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			con.Fail(err)
		}
		negative = dws.NewLexicon(cfg).Negative
	}
	self := dws.Identity{IDs: cli.List(*selfID), Names: cli.List(*selfName)}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := dws.NewClient(dws.Config{
		Profile: *profile, Kind: *kind, Transport: *transport,
		SelfIDs: self.IDs, SelfNames: self.Names, Logger: logger, Negative: negative,
	})
	defer client.Close()
	own := dws.NewRecentIDs(0)

	errc := make(chan error, 2)
	addr, err := cli.Serve(ctx, *serve, httpapi.NewDeliveryHandler(&dws.Deliverer{Sender: client, Own: own}, opts...), errc)
	if err != nil {
		con.Fail(err)
	}
	listener := &dws.Listener{
		Source: client, Sink: httpapi.NewEventSinkClient(*loopURL, opts...), Marker: client, Logger: logger,
		Normalizer: &dws.Normalizer{
			Scenes: dws.HashSceneResolver{Namespace: *namespace, Tenant: *tenant},
			Self:   self, Own: own, Locale: *locale, Employee: *employee, Negative: negative,
		},
	}
	go func() { errc <- listener.Run(ctx, *cid) }()
	con.Say("cmd.dws_gateway.started", "conversation", *cid, "kind", *kind, "loop", *loopURL, "addr", addr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil && ctx.Err() == nil {
			con.Warn("cmd.error", "error", err.Error())
		}
	}
	stop()
	con.Say("cmd.stopped", "service", "dws-gateway")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
