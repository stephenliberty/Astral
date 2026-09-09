#!/usr/bin/env bash
# Astral benchmark launcher.
#
# A single entry point so you never have to remember the run_task.py flags.
# It prompts for the bits that vary and fills in sensible defaults for the rest.
#
#   bench/run.sh                     interactive menu
#   bench/run.sh go task1            run one Go task (baseline + advisor)
#   bench/run.sh ts pluralize        run one TS task
#   bench/run.sh go all              run the full Go suite
#   bench/run.sh ts all              run the full TS suite
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_GO="${ASTRA_REPO_GO:-/tmp/opencode/go-kit}"
REPO_TS="${ASTRA_REPO_TS:-/tmp/opencode/openapi-typescript}"
VERIFY_CWD_TS="packages/openapi-typescript"
BACKEND="${ASTRA_BENCH_BACKEND:-lmstudio}"
MODEL="${ASTRA_BENCH_MODEL:-}"
ITERATIONS="${ASTRA_BENCH_ITERATIONS:-3}"

export PATH="$PATH:/home/stephen/.local/go/bin"
ASTRAL_BIN="${ASTRAL_BIN:-$BENCH_DIR/../bin/astral}"

# ---- build astral if needed ----
ensure_astral() {
  if [ ! -x "$ASTRAL_BIN" ]; then
    echo "building astral..."
    (cd "$BENCH_DIR/.." && go build -o "$ASTRAL_BIN" ./cmd/astral)
  fi
  export ASTRAL_BIN
}

# ---- list available tasks ----
go_tasks() {
  for f in "$BENCH_DIR"/go/task*.txt; do
    basename "$f" .txt | sed 's/^task[0-9]*-//'
  done
}
ts_tasks() {
  for f in "$BENCH_DIR"/ts/ts-*.txt; do
    basename "$f" .txt | sed 's/^ts-//'
  done
}

# ---- prompt for backend / model if not already set ----
pick_backend() {
  if [ -n "$BACKEND" ] && [ "$BACKEND" != "ask" ]; then
    if [ "$BACKEND" = "ollama" ] && [ -z "$MODEL" ]; then MODEL="deepseek-v4-flash:cloud"; fi
    return
  fi
  echo "Which backend?"
  echo "  1) lmstudio  (local model in LM Studio, e.g. qwen3.5-9b)"
  echo "  2) ollama    (local or cloud model via Ollama)"
  echo "  3) ask       (prompt for ANTHROPIC_BASE_URL/TOKEN)"
  read -rp "choose [1]: " choice || true
  case "${choice:-1}" in
    2) BACKEND=ollama; MODEL="${ASTRA_BENCH_MODEL:-deepseek-v4-flash:cloud}" ;;
    3) BACKEND=custom ;;
    *) BACKEND=lmstudio ;;
  esac
}

go_task_file() { # name -> path, allowing "endpoint", "1", "task1", "task1-endpoint"
  local name="$1" cand
  for cand in "task-${name}.txt" "${name}.txt" "task${name}.txt"; do
    if [ -f "$BENCH_DIR/go/$cand" ]; then echo "$BENCH_DIR/go/$cand"; return 0; fi
  done
  # match by the trailing keyword (e.g. "endpoint" -> task1-endpoint.txt)
  local f
  for f in "$BENCH_DIR"/go/task*.txt; do
    if [[ "$(basename "$f" .txt)" == *"${name}" ]]; then echo "$f"; return 0; fi
  done
  echo ""
}
run_one() { # lang task advisor
  local lang="$1" task="$2" advisor="$3"
  ensure_astral
  pick_backend
  local taskfile repo verify_pkg=""
  case "$lang" in
    go)
      taskfile=$(go_task_file "$task")
      repo="$REPO_GO"
      # default verify package by the task's keyword suffix
      local kw
      kw=$(basename "$taskfile" .txt)
      kw="${kw##*-}"
      case "$kw" in
        endpoint) verify_pkg=endpoint ;;
        log) verify_pkg=log ;;
        metrics) verify_pkg=metrics ;;
        transport) verify_pkg=transport/http ;;
        refactor) verify_pkg=log ;;
        *) verify_pkg="$kw" ;;
      esac
    ;;
    ts)
      taskfile="$BENCH_DIR/ts/ts-${task}.txt"
      repo="$REPO_TS"
    ;;
  esac
  if [ -z "$taskfile" ] || [ ! -f "$taskfile" ]; then
    echo "task not found: $task (looked for $taskfile)" >&2
    exit 1
  fi

  local label="${lang}-${task}"
  local args=(--repo "$repo" --backend "$BACKEND" --model "$MODEL"
              --iterations "$ITERATIONS" --language "$lang")
  if [ "$lang" = go ]; then
    args+=(--verify "$verify_pkg")
  else
    args+=(--verify-cwd "$VERIFY_CWD_TS")
  fi
  if [ "$advisor" = "1" ]; then
    args+=(--astral)
  fi

  local arm
  arm=$([ "$advisor" = 1 ] && echo advisor || echo baseline)
  echo "== running [$lang/$task $arm]: $taskfile =="
  python3 "$BENCH_DIR/run_task.py" "$taskfile" "$label-$arm" "${args[@]}"
}

usage() {
  printf 'Usage:\n'
  printf '  bench/run.sh                      interactive menu\n'
  printf '  bench/run.sh go <task>            run one Go task (baseline + advisor)\n'
  printf '  bench/run.sh ts <task>            run one TS task (baseline + advisor)\n'
  printf '  bench/run.sh go all               full Go suite\n'
  printf '  bench/run.sh ts all               full TS suite\n'
  printf '\nGo tasks:   %s\n' "$(go_tasks | tr '\n' ' ')"
  printf 'TS tasks:   %s\n' "$(ts_tasks | tr '\n' ' ')"
  printf '\nEnvironment:\n'
  printf '  ASTRA_BENCH_BACKEND   lmstudio | ollama | ask  (default lmstudio)\n'
  printf '  ASTRA_BENCH_MODEL     model id (ollama default: deepseek-v4-flash:cloud)\n'
  printf '  ASTRA_BENCH_ITERATIONS  iterations per task (default 3)\n'
  printf '  ASTRA_REPO_GO / ASTRA_REPO_TS\n'
  printf '  ASTRA_BIN\n'
}

main() {
  local lang="${1:-}" task="${2:-}"
  if [ -z "$lang" ]; then
    usage
    echo
    echo "which suite?"; read -rp "go/ts [go]: " lang || true; lang="${lang:-go}"
    echo "which task? ($( [ "$lang" = ts ] && ts_tasks || go_tasks ) | all)"; read -rp "task: " task || true
  fi

  if [ "$lang" = "go" ] && [ "$task" = "all" ]; then
    bash "$BENCH_DIR/go/run_suite.sh" "$REPO_GO" --backend "$BACKEND" --model "$MODEL" --iterations "$ITERATIONS"
    return
  fi
  if [ "$lang" = "ts" ] && [ "$task" = "all" ]; then
    bash "$BENCH_DIR/ts/run_suite.sh" "$REPO_TS" --verify-cwd "$VERIFY_CWD_TS" --backend "$BACKEND" --model "$MODEL" --iterations "$ITERATIONS"
    return
  fi

  case "$lang" in
    go|ts)
      run_one "$lang" "$task" 0
      run_one "$lang" "$task" 1
      ;;
    *)
      echo "unknown suite: $lang" >&2; usage; exit 1 ;;
  esac
}

main "$@"
