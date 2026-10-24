package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOutputHasOneConcurrentOwnerAndPreservesEvidence(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing-empty"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "evidence")
			if existing {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 16)
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; _, err := prepareOutput(dir); results <- err }()
			}
			close(start)
			wg.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				}
			}
			if winners != 1 {
				t.Fatalf("expected one evidence owner, got %d", winners)
			}
			summary := filepath.Join(dir, "summary.json")
			original := []byte(`{"passed":false,"original":true}`)
			if err := os.WriteFile(summary, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareOutput(dir); err == nil {
				t.Fatal("reused evidence directory accepted")
			}
			after, err := os.ReadFile(summary)
			if err != nil || string(after) != string(original) {
				t.Fatal("prior evidence changed", err)
			}
		})
	}
}
