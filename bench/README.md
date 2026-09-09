# Astral Benchmark Harness

Measures the token and correctness impact of astral's MCP tools on an
LLM coding agent, by running identical tasks with and without the tools and
comparing the prompt-token usage reported by the backend.

This is the harness behind the numbers in the main README's
[Benchmarks](../README.md#benchmarks) section.

## Layout

```
bench/
  run.sh             # friendly launcher: `bench/run.sh go task1` (see below)
  run_task.py        # shared runner: one task run (or N iterations), with/without advisor
  reset_repo.sh      # reset target repo to pristine + re-index with astral
  go/
    run_suite.sh     # 5 Go tasks x baseline/advisor x N iterations
    task*.txt        # Go task prompts (go-kit flavored)
  ts/
    run_suite.sh     # TypeScript tasks x baseline/advisor x N iterations
    ts-*.txt         # TS task prompts (openapi-typescript flavored)
```

## The easy way: `run.sh`

You don't need to remember the flags. `run.sh` fills in the defaults and asks
only for the parts that vary:

```sh
bench/run.sh go task1            # one Go task, baseline + advisor
bench/run.sh ts pluralize        # one TS task, baseline + advisor
bench/run.sh go all              # the whole Go suite
bench/run.sh ts all              # the whole TS suite
bench/run.sh                     # interactive menu
```

Default repos are `/tmp/opencode/go-kit` and `/tmp/opencode/openapi-typescript`;
override via `ASTRA_REPO_GO` / `ASTRA_REPO_TS`. Backend defaults to LM Studio
at `127.0.0.1:1235`; set `ASTRA_BENCH_BACKEND=ollama` (with
`ASTRA_BENCH_MODEL=deepseek-v4-flash:cloud`) to use Ollama. It builds and
points `astral` at `$ASTRA_BIN` (or `bin/astral` in the repo root) when
needed.

## Prerequisites

- **Go 1.27+** — to build astral and to verify agent-produced code compiles.
- **Claude Code** CLI on `PATH` (or set `CLAUDE_BIN=/path/to/claude`).
- A backend that speaks the Anthropic Messages API and reports `usage`:
  - **LM Studio** — set `ASTRAL_ANTHROPIC_BASE_URL` and
    `ASTRAL_ANTHROPIC_TOKEN` (default `http://127.0.0.1:1235` / `lmstudio`).
  - **Ollama** — default `http://localhost:11434`; use `--model` to pick a
    cloud or local model (e.g. `deepseek-v4-flash:cloud`, `qwen3.5:9b`).
- The **claude-code compatibility shim** if your LM Studio backend rejects
  Claude Code's system-message placement. See
  [`scripts/shim.py`](../scripts/shim.py).
- A target repository to benchmark against (e.g. go-kit).

## Build astral first

```sh
go build -o /tmp/astral ./cmd/astral
go build -o /tmp/astral-mcp ./cmd/astral-mcp
export ASTRAL_BIN=/tmp/astral
export ASTRAL_MCP_BIN=/tmp/astral-mcp
```

## Run a single task

```sh
# Go baseline (no astral tools)
python3 bench/run_task.py bench/go/task1-endpoint.txt task1-base \
  --repo /path/to/go-kit --verify endpoint

# Go with astral MCP tools
python3 bench/run_task.py bench/go/task1-endpoint.txt task1-adv \
  --astral --repo /path/to/go-kit --verify endpoint

# TypeScript (monorepo): agent cwd is repo root; verifier runs in the package
python3 bench/run_task.py bench/ts/ts-pluralize.txt ts-base \
  --repo /path/to/openapi-typescript --language ts \
  --verify-cwd packages/openapi-typescript

# 3 iterations to damp run-to-run variance
python3 bench/run_task.py bench/go/task5-refactor.txt task5-adv \
  --astral --repo /path/to/go-kit --verify log --iterations 3
```

Each iteration resets the repo (via `reset_repo.sh`, preserving notes and
`node_modules` for TS repos), re-indexes with astral, runs the task, then runs
the verifier (`go build` for Go; `tsc --noEmit` for TS) and records whether
the agent's changes compile.

## Run the full suite

```sh
# Go suite — LM Studio (local qwen)
bash bench/go/run_suite.sh /path/to/go-kit --backend lmstudio --iterations 3

# Go suite — Ollama (deepseek cloud)
bash bench/go/run_suite.sh /path/to/go-kit --backend ollama \
  --model deepseek-v4-flash:cloud --iterations 3

# TypeScript suite (workspace monorepo)
bash bench/ts/run_suite.sh /path/to/openapi-typescript \
  --verify-cwd packages/openapi-typescript --backend ollama \
  --model deepseek-v4-flash:cloud --iterations 2

# astral-only runs (baseline numbers already collected)
bash bench/go/run_suite.sh /path/to/go-kit --astral-only
```

Results (one JSON aggregate per task×arm) append to `bench-results-go.jsonl`
or `bench-results-ts.jsonl`, and every run's full `stream-json` transcript,
stderr, prompt, and compile error are saved under `bench-out/` for auditing.
Set `ASTRA_BENCH_OUT` to relocate.

## Reading the results

Each aggregate row contains:

- `mean_input_tokens` / `min` / `max` — ground-truth prompt tokens across
  iterations (lower is better).
- `mean_elapsed_seconds` — wall clock.
- `mean_advisor_calls` — how many `astral_*` MCP tools the model invoked.
- `compile_count` / `compile_total` — how many runs left the verify package
  build-green. This is the correctness axis.

Compare `*-baseline` vs `*-advisor` for the same task. The refactor tasks
(`task5` in Go, `ts-refactor` in TS) are the strongest signal because they
exercise `astral_callers` (blast radius) and `astral_affected` (test impact).

## Using the runner from opencode

`run_task.py` and `run_suite.sh` drive the Claude Code CLI directly and are
backend-agnostic, so they work identically whether you invoke them from a
shell, CI, or through opencode's `Bash` tool. To benchmark with opencode as
the *agent* (rather than Claude Code), register the astral MCP server in
opencode and give it the same task prompts; see the main
[README](../README.md#mcp).

## Known confounds (read before trusting the numbers)

- **Arm equivalence** — a correct baseline/astral comparison differs ONLY by
  the astral MCP server being attached. Both arms share the same prompt,
  tool surface, backend, and model. Vary `--tools`/`--instruction`/`--steer`
  on BOTH arms or the difference is not attributable to astral.
- **Tool visibility** — astral's generated MCP config sets `alwaysLoad: true`,
  and a non-first-party `ANTHROPIC_BASE_URL` disables tool search anyway, so
  the `astral_*` tools are in the model's upfront context for the astral arm.
- **Steering vs enforcement** — instructions cannot hard-force tool choice.
  Claude Code only removes a tool from context via `--disallowedTools` /
  `permissions.deny`, and scoped deny rules (e.g. `Bash(grep *)`) break
  legitimate uses (searching logs, build output, fixtures). Astral keeps Bash
  fully available and frames it as "reserve for build/test" in the steering
  instruction; the model weighs that as a prior.
- **Self-verification** — the agent's PATH must include the Go toolchain;
  without it the model ships unverified (broken) code and the compile metric
  understates both arms.
- **Run-to-run variance** — a single run of a small model can swing input
  tokens by 10x. Use `--iterations 3+`.
- **Backend token accounting differs** — Ollama and LM Studio report usage in
  different units and models emit different amounts of `thinking` tokens.
  Compare arms within one backend/model, not across them.
