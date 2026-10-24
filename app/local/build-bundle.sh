#!/bin/sh
# Build once, run the same reference app/evaluator on a Linux host or container.
set -eu
bundle_dir=${1:?Usage: build-bundle.sh EMPTY_OUTPUT_DIRECTORY [amd64|arm64]}
architecture=${2:-amd64}
case "$architecture" in amd64|arm64) ;; *) echo 'architecture must be amd64 or arm64' >&2; exit 2 ;; esac
repository=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mkdir -p "$bundle_dir"
if [ -n "$(ls -A "$bundle_dir")" ]; then
  echo 'output directory must be empty' >&2
  exit 2
fi
bundle_dir=$(CDPATH= cd -- "$bundle_dir" && pwd)
cd "$repository"
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -o "$bundle_dir/employee" ./app/local
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -o "$bundle_dir/employee-eval" ./app/eval
cp -R evals/cases/local "$bundle_dir/cases"
cp app/local/Dockerfile "$bundle_dir/Dockerfile"
mkdir "$bundle_dir/tmp"
chmod 1777 "$bundle_dir/tmp"
touch "$bundle_dir/tmp/.keep"
# Only public root certificates, never model credentials or a developer profile.
for certificate_file in /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem; do
  if [ -f "$certificate_file" ]; then
    cp "$certificate_file" "$bundle_dir/ca-certificates.crt"
    break
  fi
done
if [ ! -f "$bundle_dir/ca-certificates.crt" ]; then
  echo 'system CA bundle unavailable; no container build without trusted roots' >&2
  exit 1
fi
go version -m "$bundle_dir/employee" > "$bundle_dir/provenance.txt"
go version -m "$bundle_dir/employee-eval" >> "$bundle_dir/provenance.txt"
echo "Bundle: $bundle_dir (linux/$architecture)"
