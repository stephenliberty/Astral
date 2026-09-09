#!/usr/bin/env bash
# Run the astral benchmark suite against a Go repository.
#
# Usage:
#   run_suite.sh <repo> [--backend lmstudio|ollama] [--model M]
#                [--iterations N] [--astral-only]
set -euo pipefail

REPO="${1:?usage: run_suite.sh <repo> [--backend ...] [--model ...] [--iterations N] [--astral-only]}"
shift

BACKEND="${ASTRA_BENCH_BACKEND:-lmstudio}"
MODEL="${ASTRA_BENCH_MODEL:-}"
ITERATIONS="${ASTRA_BENCH_ITERATIONS:-3}"
MODE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --backend) BACKEND="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --astral-only) MODE="--astral-only"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNNER="$BENCH_DIR/run_task.py"
OUT="${ASTRA_BENCH_OUT:-bench-results-go.jsonl}"

#   name                          task file                              verify pkg
TASKS=(
  "task1:$BENCH_DIR/go/task1-endpoint.txt:endpoint"
  "task2:$BENCH_DIR/go/task2-log.txt:log"
  "task3:$BENCH_DIR/go/task3-metrics.txt:metrics"
  "task4:$BENCH_DIR/go/task4-transport.txt:transport/http"
  "task5:$BENCH_DIR/go/task5-refactor.txt:log"
)

: > "$OUT"

for entry in "${TASKS[@]}"; do
  IFS=: read -r name taskfile pkg <<< "$entry"
  echo "=== $name ==="
  COMMON=(--repo "$REPO" --backend "$BACKEND" --model "$MODEL"
          --iterations "$ITERATIONS" --language go --verify "$pkg")
  if [ "$MODE" != "--astral-only" ]; then
    echo "  baseline..."
    python3 "$RUNNER" "$taskfile" "$name-baseline" "${COMMON[@]}" >> "$OUT" 2>&1
  fi
  echo "  astral..."
  python3 "$RUNNER" "$taskfile" "$name-astral" --astral "${COMMON[@]}" >> "$OUT" 2>&1
done

echo "=== SUITE COMPLETE ==="
echo "Results in $OUT"
