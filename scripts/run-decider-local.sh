#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AGENTAI_DECIDER_DIR="${AGENTAI_DECIDER_DIR:-${TMPDIR:-/tmp}/agentai-decider-2b-$(id -u)}"
if [ ! -x "$AGENTAI_DECIDER_DIR/venv/bin/python" ]; then
  echo 'Run bash scripts/setup-decider-local.sh first.' >&2
  exit 1
fi
export HF_HOME="$AGENTAI_DECIDER_DIR/hf"
export HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 PYTHONDONTWRITEBYTECODE=1
exec "$AGENTAI_DECIDER_DIR/venv/bin/python" "$repo_dir/scripts/decider-local-server.py" "$@"
