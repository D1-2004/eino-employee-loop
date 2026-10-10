package im

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression: Subscribe used to hold p.mu across pollOnce, which also Lock()'s — deadlock.
func TestSubscribeDoesNotDeadlockOnSeed(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-dws.sh")
	script := "#!/bin/sh\necho '{\"messages\":[{\"messageId\":\"m1\",\"sender\":\"u\",\"senderId\":\"s\",\"text\":\"hi\",\"createTime\":\"2026-10-10 12:00:00\"}]}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewDWSProvider(DWSConfig{
		DWSBin:    fake,
		Kind:      "group",
		SeenPath:  filepath.Join(dir, "seen.json"),
		PollEvery: time.Hour,
		CatchUp:   time.Minute,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	var ch <-chan Inbound
	var err error
	go func() {
		ch, err = p.Subscribe(ctx, "cidTEST")
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if ch == nil {
			t.Fatal("nil channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe deadlocked (did not return within 2s)")
	}
	_ = p.Close()
}
