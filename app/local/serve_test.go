package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/employee/entry"
)

func TestServeContinuesDurableQueueAfterBadMessage(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "rules.json")
	raw := `{"version":"local-el-script/1","modelName":"scripted-local-demo","rules":[{"role":"chat","receipt":"bad-message","error":"bad message model failure"},{"role":"chat","receipt":"good-message","tool_calls":[{"id":"reply-good","name":"reply","arguments":{"source_ref":"${source}","text":"good message delivered"}}]}]}`
	if err := os.WriteFile(script, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var profile entry.Profile
	if err := readJSON(example("profile.json"), &profile); err != nil {
		t.Fatal(err)
	}
	models, err := modelFactory(script)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Open(context.Background(), Options{Directory: dir, Profile: profile, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bad-message", "good-message"} {
		if _, err = e.Submit(context.Background(), entry.Input{ID: id, Scene: profile.Scenes[0], Actor: profile.Owner, Text: id, Origin: "local-cli"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	err = run(ctx, []string{"-profile", example("profile.json"), "-dir", dir, "-script", script, "serve"}, strings.NewReader(""), &out, &errOut)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serve exited before cancellation: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"type":"worker_error"`) || !strings.Contains(out.String(), "bad message model failure") {
		t.Fatalf("missing diagnostic: %s", out.String())
	}
	status, err := invoke(t, dir, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	state := readState(t, status)
	if len(state.Deliveries) != 1 || state.Deliveries[0].State != "applied" || state.Deliveries[0].Intent.Body != "good message delivered" {
		t.Fatalf("valid queued message did not complete: %+v", state)
	}
}

type serveSQLiteError int

func (e serveSQLiteError) Error() string { return fmt.Sprintf("sqlite code %d", e) }
func (e serveSQLiteError) Code() int     { return int(e) }

func TestServeWorkerErrorClassification(t *testing.T) {
	for _, test := range []struct {
		err   error
		fatal bool
	}{
		{errors.New("incompatible task state"), false},
		{serveSQLiteError(5), false}, // SQLITE_BUSY can retry.
		{fmt.Errorf("write checkpoint: %w", serveSQLiteError(10|(3<<8))), true},
		{fmt.Errorf("write: %w", syscall.ENOSPC), true},
		{fmt.Errorf("single task file: %w", os.ErrPermission), false},
		{errors.Join(serveSQLiteError(5), serveSQLiteError(11)), true},
	} {
		if got := fatalWorkerError(test.err); got != test.fatal {
			t.Fatalf("%v: fatal=%v", test.err, got)
		}
	}
}

func TestServeWorkerErrorsAreBoundedAndFatalIsExplicit(t *testing.T) {
	events := make(chan error, 100)
	for range 100 {
		events <- errors.New("persistent tool failure")
	}
	close(events)
	var out bytes.Buffer
	err := serveWorkerErrors(context.Background(), events, json.NewEncoder(&out))
	if err == nil || !strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("unexpected err %v", err)
	}
	dec := json.NewDecoder(&out)
	count, total := 0, 0
	for {
		var event struct {
			Type        string
			Occurrences int
		}
		err := dec.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		total += event.Occurrences
		if event.Type != "worker_error" {
			t.Fatal(event)
		}
	}
	if count != 2 || total != 100 {
		t.Fatalf("unbounded or dropped diagnostics: count=%d total=%d", count, total)
	}
	events = make(chan error, 1)
	events <- fmt.Errorf("corrupt state: %w", serveSQLiteError(11))
	out.Reset()
	err = serveWorkerErrors(context.Background(), events, json.NewEncoder(&out))
	if err == nil || !strings.Contains(err.Error(), "resource unavailable") || !strings.Contains(out.String(), "corrupt state") {
		t.Fatalf("fatal fault hidden: %v %s", err, out.String())
	}
}

type serveBrokenWriter struct{}

func (serveBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestServeWorkerDiagnosticWriteFailureIsExplicit(t *testing.T) {
	events := make(chan error, 1)
	events <- errors.New("tool failure")
	if err := serveWorkerErrors(context.Background(), events, json.NewEncoder(serveBrokenWriter{})); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write failure hidden: %v", err)
	}
}
