# advisor

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

`advisor` attacks all three with deterministic static analysis: a
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
  (`advisor_locate`, `advisor_module`, `advisor_review`, `advisor_callers`,
  `advisor_affected`) so a model can call them directly.
- **Lazy freshness** — correctness never depends on a watcher; stale files are
  re-parsed on access. The optional `watch` daemon is a warm-up optimization.

## Supported languages

Parsed with tree-sitter: **Python, TypeScript, JavaScript, Go, HCL (Terraform)**.

## Install

Requires Go 1.27+.

```sh
go build ./cmd/advisor
go build ./cmd/advisor-mcp
```

## Usage

```sh
advisor init [path]              # build index + draft notes
advisor watch                    # optional warm-up daemon (lazy is default)
advisor locate <symbol>          # file:line + scoped note (with state)
advisor module <path>             # per-file summaries + module note
advisor note <module>            # emit prompt / --set <json> / --approve
advisor review                   # batched: new, unclear, stale, conflict
advisor status                   # freshness, coverage, staleness summary
advisor gc                       # prune orphans
advisor callers <symbol>         # files referencing a symbol across packages
advisor affected <file...>       # tests impacted by changes to the files
```

### Notes: the curation loop

The advisor does not generate note content — it orchestrates. The model writing
the code fills in the note; the advisor manages the lifecycle.

```sh
advisor note services/auth            # emits a prompt: module structure + schema
advisor note services/auth --set '{"purpose":"...","conventions":{...},"invariants":[...]}'
advisor approve services/auth         # draft → reviewed
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
advisor-mcp
```

Serves over stdio. Register it with your agent (e.g. Claude Code, Cursor,
opencode) as an MCP server. The model can then call `advisor_locate` before
reading a file, `advisor_callers` before a refactor, and `advisor_affected`
after an edit to know exactly which tests to run.

## How it works

```
.advisor/
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
- **GC** — `advisor gc` deletes derived artifacts not referenced by `index.json`.

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
cmd/advisor/main.go       # CLI entry
cmd/advisor-mcp/main.go   # MCP server (locate/module/review/callers/affected)
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
