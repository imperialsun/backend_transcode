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
  local line key value
  local quoted_double_re='^(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*"(.*)"[[:space:]]*(#.*)?$'
  local quoted_single_re="^(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*'(.*)'[[:space:]]*(#.*)?$"
  local plain_re='^(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*(.*)$'

  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"

    case "$line" in
      ""|\#*)
        continue
        ;;
    esac

    if [[ "$line" =~ $quoted_double_re ]]; then
      key="${BASH_REMATCH[2]}"
      value="${BASH_REMATCH[3]}"
    elif [[ "$line" =~ $quoted_single_re ]]; then
      key="${BASH_REMATCH[2]}"
      value="${BASH_REMATCH[3]}"
    elif [[ "$line" =~ $plain_re ]]; then
      key="${BASH_REMATCH[2]}"
      value="${BASH_REMATCH[3]}"
      value="${value%%[[:space:]]#*}"
      value="${value%"${value##*[![:space:]]}"}"
    else
      echo "[run-local] skipping malformed line in ${env_file}"
      continue
    fi

    # Keep preexisting environment values so Compose or the caller can override .env.
    if [[ -z "${!key+x}" ]]; then
      export "$key=$value"
    fi
  done < "$env_file"
}

if [ -f ".env" ]; then
  load_dotenv ".env"
  echo "[run-local] loaded .env"
else
  echo "[run-local] .env not found, defaults will be used"
fi

export APP_ENV="${APP_ENV:-development}"
export PORT="${PORT:-8080}"
export SQLITE_PATH="${SQLITE_PATH:-./backend.sqlite}"

echo "[run-local] starting backend on http://localhost:${PORT}"
exec go run ./cmd/server
