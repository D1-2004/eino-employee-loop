package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// A failed first compiler must still exclude another architecture writing the
// same directory. The subprocess stub stops before any compilation or artifact.
func TestBundleBuildOutputHasOneConcurrentOwner(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "build-bundle.sh")
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing-empty"
		}
		t.Run(name, func(t *testing.T) {
			temp := t.TempDir()
			tools := filepath.Join(temp, "tools")
			output := filepath.Join(temp, "bundle")
			trace := filepath.Join(temp, "trace")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.Mkdir(output, 0700); err != nil {
					t.Fatal(err)
				}
			}
			stub := "#!/bin/sh\nprintf 'entered\\n' >> \"$BUNDLE_TEST_TRACE\"\nsleep 0.1\nexit 9\n"
			if err := os.WriteFile(filepath.Join(tools, "go"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			codes := make(chan int, 8)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					arch := "amd64"
					if i%2 != 0 {
						arch = "arm64"
					}
					cmd := exec.Command("sh", script, output, arch)
					cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "BUNDLE_TEST_TRACE="+trace)
					err := cmd.Run()
					if e, ok := err.(*exec.ExitError); ok {
						codes <- e.ExitCode()
					} else {
						codes <- -1
					}
				}(i)
			}
			close(start)
			wg.Wait()
			close(codes)
			compilers := 0
			for code := range codes {
				if code == 9 {
					compilers++
				} else if code != 2 {
					t.Fatalf("unexpected exit %d", code)
				}
			}
			raw, err := os.ReadFile(trace)
			if err != nil || compilers != 1 || strings.Count(string(raw), "entered") != 1 {
				t.Fatalf("mixed build output: compilers=%d trace=%s err=%v", compilers, raw, err)
			}
			if _, err = os.Stat(filepath.Join(output, ".bundle-owner")); err != nil {
				t.Fatal("failed build lost ownership claim", err)
			}
		})
	}
}
