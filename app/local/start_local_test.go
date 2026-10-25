package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type launcherFixture struct {
	script, profile, bundle, state, tools, tmp, args, served, goLog string
	env                                                             []string
}

func newLauncherFixture(t *testing.T) launcherFixture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := t.TempDir()
	f := launcherFixture{script: filepath.Join(filepath.Dir(file), "start-local.sh"), profile: filepath.Join(root, "private profile.json"), bundle: filepath.Join(root, "compiled prompt.json"), state: filepath.Join(root, "state with spaces"), tools: filepath.Join(root, "tools"), tmp: filepath.Join(root, "build with spaces"), args: filepath.Join(root, "arguments"), served: filepath.Join(root, "served"), goLog: filepath.Join(root, "compiler-called")}
	for _, path := range []string{f.tools, f.tmp} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{f.profile, f.bundle} {
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "test employee")
	executable := `#!/bin/sh
for value do printf '%s\000' "$value" >> "$START_TEST_ARGS"; done
printf 'END\000' >> "$START_TEST_ARGS"
for value do
 if [ "$value" = '-check-profile' ]; then
  [ "${START_TEST_FAILCHECK:-0}" = 0 ] || exit 7
  echo '{"type":"profile_preflight","runtime_checked":false}'
  exit 0
 fi
done
[ "$value" = serve ] || exit 8
printf '%s' "$0" > "$START_TEST_SERVED"
exit 23
`
	if err := os.WriteFile(binary, []byte(executable), 0700); err != nil {
		t.Fatal(err)
	}
	compiler := `#!/bin/sh
: > "$START_TEST_GOLOG"
[ "$1" = build ] || exit 10
shift
output=
while [ "$#" -gt 0 ]; do
 if [ "$1" = -o ]; then output=$2; shift 2; else shift; fi
done
[ -n "$output" ] || exit 11
cp "$START_TEST_BINARY" "$output"
chmod 700 "$output"
`
	if err := os.WriteFile(filepath.Join(f.tools, "go"), []byte(compiler), 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(os.Environ(), "PATH="+f.tools+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+f.tmp, "START_TEST_BINARY="+binary, "START_TEST_ARGS="+f.args, "START_TEST_SERVED="+f.served, "START_TEST_GOLOG="+f.goLog)
	return f
}
func (f launcherFixture) invoke(extra ...string) (string, error) {
	args := []string{f.script, "--profile", f.profile, "--prompt-bundle", f.bundle, "--state", f.state}
	cmd := exec.Command("sh", append(args, extra...)...)
	cmd.Env = f.env
	raw, err := cmd.CombinedOutput()
	return string(raw), err
}
func TestLauncherPreservesSpacedArgumentsAndExecsCheckedBinary(t *testing.T) {
	f := newLauncherFixture(t)
	out, err := f.invoke("--timeout", "13h")
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("serve exit not preserved: %s %v", out, err)
	}
	raw, err := os.ReadFile(f.args)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(string(raw), "END\x00")
	if len(calls) != 3 {
		t.Fatalf("expected one check then one serve: %q", raw)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(f.state))
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"-profile", f.profile, "-prompt-bundle", f.bundle, "-dir", filepath.Join(parent, filepath.Base(f.state)), "-source-mode", "dev", "-source-revision", "", "-timeout", "13h"}
	for i, suffix := range []string{"-check-profile", "serve"} {
		want := strings.Join(append(append([]string{}, expected...), suffix), "\x00") + "\x00"
		if calls[i] != want {
			t.Fatalf("shell changed actual argv: got%q want%q", calls[i], want)
		}
	}
	served, err := os.ReadFile(f.served)
	if err != nil || !strings.HasPrefix(string(served), f.tmp+string(os.PathSeparator)) {
		t.Fatal("did not execute the owned checked build", err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(string(served)), "source-manifest.json")); err != nil {
		t.Fatal("actual build provenance not retained", err)
	}
}
func TestLauncherMissingInputRefusesBeforeBuildOrState(t *testing.T) {
	for _, missing := range []string{"profile", "bundle"} {
		t.Run(missing, func(t *testing.T) {
			f := newLauncherFixture(t)
			path := f.profile
			if missing == "bundle" {
				path = f.bundle
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := f.invoke(); err == nil {
				t.Fatal("missing input accepted")
			}
			for _, sideEffect := range []string{f.goLog, f.state, f.served} {
				if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
					t.Fatal("missing input caused a side effect", sideEffect)
				}
			}
		})
	}
}
func TestLauncherFailedManifestPreflightNeverServesOrOpensState(t *testing.T) {
	f := newLauncherFixture(t)
	f.env = append(f.env, "START_TEST_FAILCHECK=1")
	if _, err := f.invoke(); err == nil {
		t.Fatal("failed preflight ignored")
	}
	for _, path := range []string{f.served, f.state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid manifest reached serve/state")
		}
	}
	entries, err := os.ReadDir(f.tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed build was not cleaned up", err)
	}
}
func TestLauncherRejectsRepositoryStateIncludingSymlink(t *testing.T) {
	f := newLauncherFixture(t)
	repository := filepath.Clean(filepath.Join(filepath.Dir(f.script), "../.."))
	f.state = filepath.Join(repository, "must-not-be-created")
	if _, err := f.invoke(); err == nil {
		t.Fatal("state inside source accepted")
	}
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	f.state = filepath.Join(link, "must-not-be-created")
	if _, err := f.invoke(); err == nil {
		t.Fatal("symlink into repository accepted")
	}
	if _, err := os.Stat(f.goLog); !os.IsNotExist(err) {
		t.Fatal("repository state reached compiler")
	}
}

func TestLauncherCannotOverrideFrozenIdentityOrInventAcceptanceRevision(t *testing.T) {
	for _, arguments := range [][]string{{"--owner", "another-owner"}, {"--scene", "daily-room"}, {"--mode", "acceptance"}} {
		f := newLauncherFixture(t)
		if _, err := f.invoke(arguments...); err == nil {
			t.Fatal("unsupported identity or unstamped acceptance accepted")
		}
		for _, path := range []string{f.goLog, f.served, f.state} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid selection reached build/serve/state")
			}
		}
	}
}
