#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v go >/dev/null 2>&1; then
  echo "[run-local] go is not installed or not in PATH"
  exit 1
fi

load_dotenv() {
  local env_file="$1"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ""|\#*)
        continue
        ;;
    esac

    line="${line#export }"
    if [[ "$line" != *"="* ]]; then
      continue
    fi

    local key="${line%%=*}"
    local value="${line#*=}"
    key="${key//[[:space:]]/}"

    case "$value" in
      \"*\")
        value="${value:1:${#value}-2}"
        ;;
      \'*\')
        value="${value:1:${#value}-2}"
        ;;
    esac

    if [ -n "$key" ]; then
      export "$key=$value"
    fi
  done < "$env_file"
}

if [ -f ".env" ]; then
  load_dotenv ".env"
else
  echo "[run-local] .env not found, defaults will be used"
fi

export APP_ENV="${APP_ENV:-development}"
export PORT="${PORT:-8080}"
export SQLITE_PATH="${SQLITE_PATH:-./backend.sqlite}"

echo "[run-local] starting backend on http://localhost:${PORT}"
exec go run ./cmd/server
