#!/usr/bin/env bash
# Build dws-consumer and install beside a local data dir.
# Intended for machines that already have `dws` authenticated (e.g. dxxh).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$HOME/.local/bin/eino-dws-consumer}"
mkdir -p "$(dirname "$OUT")" "$HOME/.local/share/eino-employee-loop"
cd "$ROOT"
GOTOOLCHAIN=auto go build -o "$OUT" ./cmd/dws-consumer
echo "installed: $OUT"
echo
echo "Run on a host with dws logged in:"
echo "  export DEEPSEEK_API_KEY=..."
echo "  export DWS_PROFILE='corpId:userId'   # optional"
echo "  $OUT --cid 'cid...' --db \$HOME/.local/share/eino-employee-loop/employee.db"
echo
echo "If dws auth expired:"
echo "  python3 \$HOME/.agents/skills/dws-env/scripts/dws_env.py as <identity> -- auth login --device"
echo "  # or: dws --profile <corpId:userId> auth login --device"
