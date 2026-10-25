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
	script, profile, bundle, state, tools, tmp, args, served, goLog, order, signLog string
	env                                                                             []string
}

func newLauncherFixture(t *testing.T) launcherFixture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := t.TempDir()
	f := launcherFixture{script: filepath.Join(filepath.Dir(file), "start-local.sh"), profile: filepath.Join(root, "private profile.json"), bundle: filepath.Join(root, "compiled prompt.json"), state: filepath.Join(root, "state with spaces"), tools: filepath.Join(root, "tools"), tmp: filepath.Join(root, "build with spaces"), args: filepath.Join(root, "arguments"), served: filepath.Join(root, "served"), goLog: filepath.Join(root, "compiler-called"), order: filepath.Join(root, "order"), signLog: filepath.Join(root, "sign-arguments")}
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
  printf 'preflight\n' >> "$START_TEST_ORDER"
  [ "${START_TEST_FAILCHECK:-0}" = 0 ] || exit 7
  echo '{"type":"profile_preflight","runtime_checked":false}'
  exit 0
 fi
done
[ "$value" = serve ] || exit 8
printf 'serve\n' >> "$START_TEST_ORDER"
printf '%s' "$0" > "$START_TEST_SERVED"
exit 23
`
	if err := os.WriteFile(binary, []byte(executable), 0700); err != nil {
		t.Fatal(err)
	}
	compiler := `#!/bin/sh
: > "$START_TEST_GOLOG"
printf 'build\n' >> "$START_TEST_ORDER"
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
	for name, body := range map[string]string{
		"uname": "#!/bin/sh\nprintf '%s\\n' \"${START_TEST_OS:-Darwin}\"\n",
		"codesign": `#!/bin/sh
for argument do printf '%s\000' "$argument" >> "$START_TEST_SIGNLOG"; done
printf 'END\000' >> "$START_TEST_SIGNLOG"
case "$1" in
 --force)
  printf 'sign\n' >> "$START_TEST_ORDER"
  [ "${START_TEST_SIGN_FAIL:-}" != sign ] || exit 31
  for argument do target=$argument; done
  printf '\n# signed fixture\n' >> "$target"
  ;;
 --verify)
  printf 'verify\n' >> "$START_TEST_ORDER"
  [ "${START_TEST_SIGN_FAIL:-}" != verify ] || exit 32
  ;;
 *) exit 33 ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(f.tools, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = append(os.Environ(), "PATH="+f.tools+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+f.tmp, "START_TEST_BINARY="+binary, "START_TEST_ARGS="+f.args, "START_TEST_SERVED="+f.served, "START_TEST_GOLOG="+f.goLog, "START_TEST_ORDER="+f.order, "START_TEST_SIGNLOG="+f.signLog, "EL_LOCAL_CODESIGN_IDENTITY=")
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

func TestLauncherCodesignIsExplicitAndBeforePreflightOnSameBinary(t *testing.T) {
	f := newLauncherFixture(t)
	identity := strings.Repeat("AB", 20)
	f.env = append(f.env, "EL_LOCAL_CODESIGN_IDENTITY="+identity)
	out, err := f.invoke()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("signed launch did not exec serve: %s %v", out, err)
	}
	raw, err := os.ReadFile(f.order)
	if err != nil || string(raw) != "build\nsign\nverify\npreflight\nserve\n" {
		t.Fatalf("wrong actual process order: %q %v", raw, err)
	}
	target, err := os.ReadFile(f.served)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := os.ReadFile(string(target))
	if err != nil || !strings.Contains(string(signed), "# signed fixture") {
		t.Fatal("serve did not execute the signed artifact", err)
	}
	log, err := os.ReadFile(f.signLog)
	if err != nil {
		t.Fatal(err)
	}
	expected := "--force\x00--sign\x00" + identity + "\x00" + string(target) + "\x00END\x00--verify\x00--strict\x00" + string(target) + "\x00END\x00"
	if string(log) != expected {
		t.Fatalf("sign/verify did not use exact identity and checked binary: %q", log)
	}
}
func TestLauncherDefaultDoesNotCallCodesign(t *testing.T) {
	f := newLauncherFixture(t)
	_, _ = f.invoke()
	if _, err := os.Stat(f.signLog); !os.IsNotExist(err) {
		t.Fatal("default launcher called codesign")
	}
	order, err := os.ReadFile(f.order)
	if err != nil || string(order) != "build\npreflight\nserve\n" {
		t.Fatalf("default path changed: %q %v", order, err)
	}
}
func TestLauncherCodesignFailureStopsBeforePreflightAndCleansOwnedBuild(t *testing.T) {
	for _, stage := range []string{"sign", "verify"} {
		t.Run(stage, func(t *testing.T) {
			f := newLauncherFixture(t)
			f.env = append(f.env, "EL_LOCAL_CODESIGN_IDENTITY="+strings.Repeat("a", 40), "START_TEST_SIGN_FAIL="+stage)
			if _, err := f.invoke(); err == nil {
				t.Fatal("failed signature reached serve")
			}
			for _, sideEffect := range []string{f.args, f.served, f.state} {
				if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
					t.Fatal("failed signing reached app or business state", sideEffect)
				}
			}
			entries, err := os.ReadDir(f.tmp)
			if err != nil || len(entries) != 0 {
				t.Fatal("signature failure leaked owned build", err)
			}
			order, err := os.ReadFile(f.order)
			want := "build\nsign\n"
			if stage == "verify" {
				want += "verify\n"
			}
			if err != nil || string(order) != want {
				t.Fatalf("continued after signing failure: %q %v", order, err)
			}
		})
	}
}
func TestLauncherCodesignRejectsInvalidFingerprintOrNonDarwinBeforeBuild(t *testing.T) {
	for _, test := range []struct{ identity, system string }{{strings.Repeat("a", 39), "Darwin"}, {strings.Repeat("g", 40), "Darwin"}, {"certificate display name", "Darwin"}, {strings.Repeat("a", 40), "Linux"}} {
		f := newLauncherFixture(t)
		f.env = append(f.env, "EL_LOCAL_CODESIGN_IDENTITY="+test.identity, "START_TEST_OS="+test.system)
		if _, err := f.invoke(); err == nil {
			t.Fatal("invalid signing selection accepted")
		}
		for _, sideEffect := range []string{f.goLog, f.args, f.signLog, f.served, f.state} {
			if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
				t.Fatal("invalid signing selection caused effects", sideEffect)
			}
		}
	}
}
