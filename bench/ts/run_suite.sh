#!/usr/bin/env bash
# Run the astral benchmark suite against a TypeScript monorepo.
#
# The agent works from the repo root (so astral's per-repo index resolves),
# while the typecheck verifier runs inside the workspace package directory.
#
# Usage:
#   run_suite.sh <repo> --verify-cwd <pkg-dir> [--backend lmstudio|ollama]
#                [--model M] [--iterations N] [--astral-only]
set -euo pipefail

REPO="${1:?usage: run_suite.sh <repo> --verify-cwd <pkg-dir> [--backend ...] [--model ...] [--iterations N] [--astral-only]}"
shift

BACKEND="${ASTRA_BENCH_BACKEND:-lmstudio}"
MODEL="${ASTRA_BENCH_MODEL:-}"
ITERATIONS="${ASTRA_BENCH_ITERATIONS:-3}"
VERIFY_CWD=""
MODE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --backend) BACKEND="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --verify-cwd) VERIFY_CWD="$2"; shift 2 ;;
    --astral-only) MODE="--astral-only"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$VERIFY_CWD" ]; then
  echo "--verify-cwd <pkg-dir> is required (workspace package with tsconfig.json)" >&2
  exit 1
fi

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNNER="$BENCH_DIR/run_task.py"
OUT="${ASTRA_BENCH_OUT:-bench-results-ts.jsonl}"

#   name                          task file
TASKS=(
  "ts-pluralize:$BENCH_DIR/ts/ts-pluralize.txt"
  "ts-add-link:$BENCH_DIR/ts/ts-link-object.txt"
  "ts-add-example:$BENCH_DIR/ts/ts-example-object.txt"
  "ts-refactor:$BENCH_DIR/ts/ts-refactor-transform.txt"
)

: > "$OUT"

for entry in "${TASKS[@]}"; do
  IFS=: read -r name taskfile <<< "$entry"
  echo "=== $name ==="
  COMMON=(--repo "$REPO" --backend "$BACKEND" --model "$MODEL"
          --iterations "$ITERATIONS" --language ts --verify-cwd "$VERIFY_CWD")
  if [ "$MODE" != "--astral-only" ]; then
    echo "  baseline..."
    python3 "$RUNNER" "$taskfile" "$name-baseline" "${COMMON[@]}" >> "$OUT" 2>&1
  fi
  echo "  astral..."
  python3 "$RUNNER" "$taskfile" "$name-astral" --astral "${COMMON[@]}" >> "$OUT" 2>&1
done

echo "=== SUITE COMPLETE ==="
echo "Results in $OUT"
