#!/usr/bin/env bash
# Build dws-consumer binary. Runtime config (cid, profile, self-*, keys) stays in env / local files — never commit those.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$HOME/.local/bin/eino-dws-consumer}"
mkdir -p "$(dirname "$OUT")"
cd "$ROOT"
GOTOOLCHAIN=auto go build -o "$OUT" ./cmd/dws-consumer
echo "installed: $OUT"
echo
echo "Example (placeholders — use your env / dotenv, not committed values):"
echo "  export LLM_API_KEY=..."
echo "  export DWS_PROFILE='''corpId:userId'''   # optional"
echo "  export DWS_SELF_NAME='''YourDisplayName'''"
echo "  $OUT --cid '''openConversationId...''' --kind group --db ./data/employee.db --executor mock"
echo
echo "Auth: use your local dws login flow for the chosen profile."
