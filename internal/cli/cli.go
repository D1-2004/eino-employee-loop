// Package cli holds the small helpers shared by the service commands:
// locale resolution, localized output and flag parsing helpers.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/D1-2004/eino-employee-loop/i18n"
)

// Env names read by every command.
const (
	EnvLocale = "EMPLOYEE_LOCALE"
	EnvToken  = "EEL_TOKEN"
)

// Console prints localized lines.
type Console struct {
	Messages *i18n.Bundle
	Locale   string
	Out, Err io.Writer
}

// NewConsole returns a console on stdout/stderr for the locale named by
// $EMPLOYEE_LOCALE (used for flag help, before flags are parsed). Its
// bundle is Messages() plus the packs the loaders merge (for example
// catalogdoc.LoadMessages).
func NewConsole(packs ...func(*i18n.Bundle) error) *Console {
	b := Messages()
	if len(packs) > 0 {
		b = b.Clone()
		for _, load := range packs {
			if err := load(b); err != nil {
				panic(err)
			}
		}
	}
	return &Console{Messages: b, Locale: os.Getenv(EnvLocale), Out: os.Stdout, Err: os.Stderr}
}

// T returns a localized text.
func (c *Console) T(key string, args ...string) string {
	return c.Messages.Text(c.Locale, key, args...)
}

// Say prints a localized line to Out.
func (c *Console) Say(key string, args ...string) { fmt.Fprintln(c.Out, c.T(key, args...)) }

// Warn prints a localized line to Err.
func (c *Console) Warn(key string, args ...string) { fmt.Fprintln(c.Err, c.T(key, args...)) }

// Fail prints the error line and exits with code 1.
func (c *Console) Fail(err error) {
	c.Warn("cmd.error", "error", err.Error())
	os.Exit(1)
}

// Usage prints a localized usage error and exits with code 2.
func (c *Console) Usage(key string, args ...string) {
	c.Warn(key, args...)
	os.Exit(2)
}

// SetLocale switches the console locale when tag is not empty.
func (c *Console) SetLocale(tag string) {
	if strings.TrimSpace(tag) != "" {
		c.Locale = tag
	}
}

// List splits a comma separated flag value, dropping empty items.
func List(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Serve runs an HTTP server until ctx ends, then shuts it down within 5s.
// It returns once the listener is bound or failed, reporting later errors
// on errc.
func Serve(ctx context.Context, addr string, h http.Handler, errc chan<- error) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && errc != nil {
			errc <- err
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return ln.Addr().String(), nil
}
