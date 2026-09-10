# Astral

A local, deterministic "dumb advisor" for LLM coding agents. It gives a model a
compact, trustworthy view of a codebase — where symbols live, what conventions
govern a module, and what invariants must be respected when writing there — so
the model can answer "where do I write this" and "how do I write here" with a
single cheap query instead of burning tokens reading files.

The goal is to convert expensive inference (searching, reading examples to
learn idioms) into cheap lookup, reducing token usage — especially for small
models that burn context fast.

## Why

Coding agents waste tokens in three structural ways:

1. **Blind navigation** — the model greps and reads whole files to find where a
   symbol lives.
2. **Re-learning conventions** — it reads example after example to infer a
   module's idioms, error style, and invariants.
3. **Over-broad edits** — it can't see the blast radius of a refactor or which
   tests a change touches, so it reads (and re-reads) more than it needs.

`astral` attacks all three with deterministic static analysis: a
content-addressed symbol index, human-authored conventions notes, and a
cross-package relationship graph for blast-radius and test-impact queries.

## Design principles

1. **Dumb, not smart.** Deterministic static analysis. No LLM calls, no SDKs,
   no API keys. It never invents content — it surfaces and maintains
   human-authored notes.
2. **Advisory, not authoritative.** Notes are a strong prior, not ground truth.
   The code is the source of truth. When they conflict, the code wins.
3. **Trust via freshness.** Every derived artifact is content-addressed. A hash
   match means "trust this, skip the read." A mismatch means "stale,
   re-verify."
4. **Never blocks, only flags.** The model always gets a note (draft or
   reviewed). Review is a pull activity, never a push.
5. **Token-friendliness is the product.** Narrow question in, one-line answer
   out. No JSON dumps, no noise.

## Features

- **`locate`** — find a symbol (`file:line`) *and* the scoped conventions note
  in one round-trip. The token-friendly workhorse.
- **`module`** — per-file summaries plus the merged note for a directory.
- **Notes** — human-authored claims about a directory (purpose, conventions,
  invariants) with a lifecycle: `draft → reviewed → stale → conflict`. Notes
  merge root-to-leaf at query time; invariants are additive.
- **`callers`** — blast radius: every file that references a symbol across
  packages. Check before refactoring or removing anything.
- **`affected`** — test impact analysis: which tests to run after editing a set
  of files, computed over the transitive import graph.
- **MCP server** — the same queries exposed as MCP tools
  (`find_symbol`, `module_info`, `review_notes`, `find_references`,
  `affected_tests`) so a model can call them directly.
- **Lazy freshness** — correctness never depends on a watcher; stale files are
  re-parsed on access. The optional `watch` daemon is a warm-up optimization.

## Supported languages

Parsed with tree-sitter: **Python, TypeScript, JavaScript, Go, HCL (Terraform)**.

## Install

Requires Go 1.27+.

```sh
go build ./cmd/astral
go build ./cmd/astral-mcp
```

## Usage

```sh
astral init [path]              # build index + draft notes
astral watch                    # optional warm-up daemon (lazy is default)
astral locate <symbol>          # file:line + scoped note (with state)
astral module <path>             # per-file summaries + module note
astral note <module>            # emit prompt / --set <json> / --approve
astral review                   # batched: new, unclear, stale, conflict
astral status                   # freshness, coverage, staleness summary
astral gc                       # prune orphans
astral callers <symbol>         # files referencing a symbol across packages
astral affected <file...>       # tests impacted by changes to the files
```

### Notes: the curation loop

The advisor does not generate note content — it orchestrates. The model writing
the code fills in the note; the advisor manages the lifecycle.

```sh
astral note services/auth            # emits a prompt: module structure + schema
astral note services/auth --set '{"purpose":"...","conventions":{...},"invariants":[...]}'
astral approve services/auth         # draft → reviewed
```

A note is a JSON claim about a directory:

```json
{
  "fingerprint": "sha256:...",
  "state": "reviewed",
  "purpose": "Authentication: token issuance, refresh, revocation",
  "conventions": { "naming": "snake_case", "errors": "Result types" },
  "invariants": ["All errors must reject with SomeSpecialError"]
}
```

- **`invariants`** = hard constraints. The model must respect them.
- **`conventions`** = soft guidance. The model should follow them.
- **`state`** = one of `draft`, `reviewed`, `stale`, `conflict`.

Notes are per-directory and merge at query time, root → leaf: purpose and
conventions are most-specific-wins, invariants are an additive union, and state
is the weakest link in the chain.

### MCP

```sh
astral-mcp
```

Serves over stdio. Register it with your agent (e.g. Claude Code, Cursor,
opencode) as an MCP server. The model can then call `find_symbol` before
reading a file, `find_references` before a refactor, and `affected_tests`
after an edit to know exactly which tests to run.

## Benchmarks

Measured in a Claude Code harness driving a real model against the
[go-kit](https://github.com/go-kit/kit) codebase, with and without astral's
MCP tools available. Each task is a code-generation or refactor request; the
"advisor" arm has `find_symbol`, `module_info`, `find_references`, and
`affected_tests` in its tool list. **Input tokens** are the ground-truth
prompt tokens reported by the model backend, summed across all requests in
the run. **Compiles** is how many of the runs produced a build-green package.

### deepseek-v4-flash (cloud), 2 runs per arm

`deepseek-v4-flash` self-selects the astral tools even when Bash is available,
so the savings are clean.

| task | baseline (tokens) | advisor (tokens) | reduction | wall-clock | compiles |
|---|---|---|---|---|---|
| 1 — add `WithRetry` to `endpoint` | 684,518 | 211,980 | **-69%** | 2.2x faster | 2/2 |
| 5 — rename `NewJSONLogger`→`NewStructuredLogger` (refactor) | 1,817,002 | 441,412 | **-75%** | 1.7x faster | 2/2 |

### qwen3.5-9b (local LM Studio), 3 runs per arm

The 9b model needs the tool surface reduced to make astral discoverable; even
then, run-to-run variance is high (the model often falls back to grep). These
numbers were collected after fixing a harness bug (see note below) where the
agent's PATH did not include the Go toolchain, so it could not self-verify
with `go build` and shipped unverified code.

| task | baseline (tokens) | advisor (tokens) | reduction | compiles |
|---|---|---|---|---|
| 1 — add `WithRetry` to `endpoint` | 344,707 | 116,694 | **-66%** | 3/3 vs 2/3 |
| 2 — add `NewPrefixLogger` to `log` | 250,358 | 150,675 | **-40%** | 3/3 vs 2/3 |
| 3 — add `NewMultiCounter` to `metrics` | 243,449 | 162,881 | **-33%** | 3/3 vs 3/3 |
| 4 — add `NewLoggingErrorEncoder` to `transport/http` | 173,184 | 256,940 | **+48%** | 3/3 vs 2/3 |
| 5 — rename `NewJSONLogger`→`NewStructuredLogger` (refactor) | 3,026,962 | 1,364,615 | **-55%** | 3/3 |

The advisor arms of tasks 2-4 were re-run on the fixed harness (3 iterations
each) after the toolchain-PATH fix; their token numbers replace the earlier
runs that used a broken PATH. Task 4's advisor arm spent more tokens because
the fixed harness let the model actually run the verification loop; its
compile rate went from 1/3 to 3/3. Run-to-run variance on the 9b model is
large (input tokens swing by ~2.5x), so single-task deltas should be read as
directional. Notably, the advisor compile rate is now at parity or better on
every task — the earlier 1/3 compile rates were an artifact of the harness
blocking the model from running `go build`, not an astral shortcoming.

### Takeaways

- **Capable models get the full benefit.** deepseek cut tokens ~70-75% while
  keeping a 100% compile rate, and used the astral tools of its own accord.
- **Small models are the harder case.** The 9b model only reaches for astral
  when its tool list is trimmed, and its variance can swamp the signal.
  Restricting the tool surface to a focused set
  (`Read,Edit,Write,Bash,astral_*`) makes it converge.
- **The refactor task is the standout.** `find_references` (blast radius) and
  `affected_tests` (test impact) convert a multi-file refactor from a
  grep-and-hope exercise into targeted, verified edits.
- **Self-verification matters for correctness.** The agent must be able to run
  the build itself; when the harness blocked it from doing so, both arms
  shipped broken code and the compile metric was meaningless. Fixing the
  agent's PATH (Go toolchain) brought the advisor-arm compile rate to parity
  on every task.

The harness is bundled in [`bench/`](bench/README.md) — `bench/run_suite.sh`
drives Claude Code against any Anthropic-compatible backend (LM Studio or
Ollama) on the same five tasks, and `bench/run_task.py` accepts per-language
verifiers (`--language ts` runs `tsc --noEmit`, `--verify-cmd` for custom
checks), so the suite portably targets Go, TypeScript, or any repository the
agent can validate.

## How it works

```
.astral/
  index.json                  # derived, gitignored
  files/<file_hash>.json      # derived, gitignored — symbols extracted from source
  notes/<dir_path>.json       # human-authored, COMMITTABLE — stable name
```

- **File fingerprint** = `sha256(content)`.
- **Directory fingerprint** = `sha256(join(sorted(file_hashes)))` — changes
  when any file in the directory changes.
- **`index.json`** maps `path → {file_hash, index_ref, note_ref, state}` and
  caches directory fingerprints. Rewritten atomically (temp + rename).
- **Freshness** — at query time the file is hashed and compared. Match → trust,
  skip the read. Mismatch → re-parse on the spot (lazy).
- **GC** — `astral gc` deletes derived artifacts not referenced by `index.json`.

The relationship graph is a query-time layer over the index, not a separate
store. The parser extracts imports and cross-package references alongside
symbols; `callers` and `affected` resolve those against the index on demand.

## Correctness model

- **Correctness does not depend on the watcher.** `locate` checks the hash at
  query time and lazily re-parses stale files. The watcher is a warm-up
  optimization, not a correctness requirement.
- **The stale flag is a "please re-verify" signal, not a "this note is wrong"
  verdict.** The fingerprint detects *change*, not *violation*.
- **The graph is static and import-graph-only.** It cannot see dynamic
  dispatch, reflection, or config-driven behavior. Treat `affected` as a "must
  run" subset, not an exhaustive one — pair it with a smoke-test safety net.
- **Conservative by design.** Same-name symbols across packages are resolved by
  package match; ambiguous cases over-approximate, which is the safe direction.

## Testing

```sh
go test ./...
```

The advisor indexes its own Go codebase as the primary test harness, and a
fixture repo with Python, TypeScript, JavaScript, and HCL files exercises the
parser layer. Tests cover parser extraction correctness, content-addressing
round-trips, note lifecycle transitions, merge semantics, and staleness
detection.

## Architecture

```
cmd/astral/main.go       # CLI entry
cmd/astral-mcp/main.go   # MCP server (locate/module/review/callers/affected)
internal/watcher/         # fsnotify + debounce + batch re-parse
internal/parser/          # tree-sitter (py, ts, js, go, hcl) + refs/imports
internal/index/           # content-addressed symbol index
internal/notes/           # lifecycle, drift diff, LLM-handoff flow
internal/graph/           # callers + test impact over the index
internal/query/           # CLI query commands
internal/store/           # content-addressed store + index.json
```

## Roadmap

- **v3** — per-note file sets (fixes the drift cascade where a change in a
  leaf directory flags every ancestor note stale); cross-compile build matrix
  (tree-sitter's Go binding requires cgo, so cross-compilation needs a
  per-target build matrix).
- **Possible** — heuristic note generation as an alternative to the LLM-handoff
  flow; invariant override/negation (currently additive-only).

## License

Apache-2.0 / MIT (see `LICENSE` and `LICENSE-MIT`).
