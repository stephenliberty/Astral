# Edit-scoping research (2026-09-09)

Question: can we scope the `Edit` tool to a region (e.g. "edit `foo()` = lines 100–150")?

## Finding: no native region-scoping on Edit

The built-in `Edit` tool schema is only `file_path`, `old_string`, `new_string`,
(+ `replace_all`). It does exact string replacement — no `offset`/`limit`/
`line` parameters. There is no built-in way to say "only touch lines X–Y."

Reference: code.claude.com/docs/en/tools → "Edit tool behavior":
- Three checks to apply: read-before-edit, exact `old_string` match, uniqueness.
- Viewing a file with Bash `sed -n '100,150p'` satisfies the read-before-edit
  requirement (also `cat`, `nl`, `bat`, `head`, `tail`, `grep` on a single
  file, no pipes). So a `sed` region-view + Edit is a legitimate workflow.

## What CAN scope edits, in increasing strictness

1. **Instruction (soft prior)** — "Modify only the body of `foo` (lines
   100–150); change nothing else." Steering, not enforcement.
2. **Read scoping (context)** — `Read` takes `offset`/`limit`; showing only the
   region keeps the rest out of context. Works for the read-before-edit check.
3. **PreToolUse hook (hard, runtime-enforced)** — block `Edit` calls whose
   span falls outside an allowed region, by inspecting `old_string`/`new_string`
   and resolving them to lines. Requires the region to be known a priori.
4. **Subagent allowlist** — `tools: Read, Edit, Write` (no Glob/Grep/Bash)
   narrows tools, not regions.

## Relevance to astral

`astral_locate` already returns each symbol's full span (`line`, `end_line`).
So the harness/tooling could:
- pass "edit only within `foo` lines X–Y" as the instruction, and/or
- feed X–Y to a `PreToolUse` hook that denies out-of-region edits.

This is a distinct "editing discipline" axis in the benchmark, separate from
the "symbol hunting" axis (which tools find things). Not yet implemented.

## Decision

Recorded for later. The immediate benchmark focus stays on the hunting/
relationship axis (astral_locate/module/callers/affected vs grep).
