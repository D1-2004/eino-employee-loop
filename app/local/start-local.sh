#!/bin/sh
# Foreground launcher for the complete SQLite/Markdown/Eino local Entry.
# Profiles and compiled prompt artifacts stay external; no login or IM writes.
set -eu
umask 077
fail() { echo "start-local: $*" >&2; exit 2; }
profile= bundle= state= revision=
mode=dev
timeout=24h
while [ "$#" -gt 0 ]; do
  case "$1" in
    --profile|--prompt-bundle|--state|--mode|--revision|--timeout)
      [ "$#" -ge 2 ] && [ -n "$2" ] || fail "missing option value"
      case "$1" in
        --profile) profile=$2 ;; --prompt-bundle) bundle=$2 ;; --state) state=$2 ;;
        --mode) mode=$2 ;; --revision) revision=$2 ;; --timeout) timeout=$2 ;;
      esac
      shift 2 ;;
    --help) echo 'Usage: start-local.sh --profile /private/profile.json --prompt-bundle /private/compiled.json --state /private/state [--mode dev|acceptance] [--revision FULL_SHA] [--timeout 24h]'; exit 0 ;;
    *) fail "unknown option" ;;
  esac
done
[ -n "$profile" ] && [ -n "$bundle" ] && [ -n "$state" ] || fail "profile, compiled prompt bundle and state are required"
case "$mode" in dev) ;; acceptance) [ -n "$revision" ] || fail "acceptance requires --revision" ;; *) fail "mode must be dev or acceptance" ;; esac
case "$profile:$bundle" in /*:/*) ;; *) fail "profile and prompt bundle must use absolute paths" ;; esac
[ -f "$profile" ] && [ -r "$profile" ] || fail "profile file is missing or unreadable"
[ -f "$bundle" ] && [ -r "$bundle" ] || fail "compiled prompt bundle file is missing or unreadable"
case "$state" in /*) ;; *) fail "state must be an absolute external path" ;; esac
case "/$state/" in */../*|*/./*) fail "state path must not contain dot components" ;; esac
repository=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
# Resolve existing ancestors without creating a directory, including symlinks.
ancestor=$state
suffix=
while [ ! -d "$ancestor" ]; do
  [ ! -e "$ancestor" ] && [ ! -L "$ancestor" ] || fail "state path is not a directory"
  suffix="/$(basename -- "$ancestor")$suffix"
  ancestor=$(dirname -- "$ancestor")
done
ancestor=$(CDPATH= cd -- "$ancestor" && pwd -P)
state="$ancestor$suffix"
case "$state" in "$repository"|"$repository"/*|/) fail "state must be outside the repository and filesystem root" ;; esac
command -v go >/dev/null 2>&1 || fail "Go is required"
codesign_identity=${EL_LOCAL_CODESIGN_IDENTITY:-}
if [ -n "$codesign_identity" ]; then
  [ "${#codesign_identity}" -eq 40 ] || fail "codesign identity must be an exact 40-hex fingerprint"
  case "$codesign_identity" in *[!0123456789abcdefABCDEF]*) fail "codesign identity must be an exact 40-hex fingerprint" ;; esac
  [ "$(uname -s)" = Darwin ] || fail "explicit local codesigning is supported only on Darwin"
  command -v codesign >/dev/null 2>&1 || fail "codesign is required for the explicit identity"
fi
# Build exactly once. Preflight and serve run this same VCS-stamped executable.
build=$(mktemp -d "${TMPDIR:-/tmp}/employee-local.XXXXXXXX")
trap 'rm -rf -- "$build"' EXIT HUP INT TERM
cd "$repository"
go build -buildvcs=true -o "$build/employee" ./app/local
if [ -n "$codesign_identity" ]; then
  codesign --force --sign "$codesign_identity" "$build/employee"
  codesign --verify --strict "$build/employee"
fi
set -- -profile "$profile" -prompt-bundle "$bundle" -dir "$state" -source-mode "$mode" -source-revision "$revision" -timeout "$timeout"
"$build/employee" "$@" -check-profile > "$build/source-manifest.json"
cat "$build/source-manifest.json"
# Keep the owned binary and provenance for audit once it replaces this shell.
# No port probing, PID manager, process killing or default identity changes.
trap - EXIT HUP INT TERM
exec "$build/employee" "$@" serve
