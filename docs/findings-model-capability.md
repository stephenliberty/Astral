# Findings: model capability and astral tool adoption

Date: 2026-09-09

## Question

Does astral's value depend on the model's ability to understand and trust
its tools? We tested across models, harnesses, thinking modes, and context
sizes.

## The core finding

**qwen3.5 does not internalize tool semantics.** Across every configuration
it called astral tools, then **grepped for the same symbol to verify the
answer** — treating astral as a hint and grep as the source of truth.

The transcript pattern (identical across all qwen runs):

```
astral_astral_locate    {"symbol": "transformMediaTypeObject"}
astral_astral_callers   {"symbol": "transformMediaTypeObject"}
grep                    {"pattern": "transformMediaTypeObject", ...}
```

The model calls astral, gets the answer, then re-searches for the same symbol
anyway. That is a **trust** failure, not a discoverability, thinking, context,
or harness failure.

## Evidence: astral calls vs total calls (refactor task)

| config | astral calls | total calls | notes |
|---|---|---|---|
| qwen3.5-9b, thinking OFF | 0–2 | ~20 | never used astral_callers |
| qwen3.5-9b, thinking ON | 1 | ~15 | used astral_callers once |
| qwen3.6-27b, thinking ON | 3 | ~10 | locate+callers+module, then grep |
| qwen3.5-397b cloud, opencode | 2 | 17 | locate+callers, then grep |
| **deepseek-v4-flash** | self-selected | — | 69–75% token reduction, 100% compile |

## What did NOT move the ratio

- **Thinking on/off** — qwen is a hybrid-thinking model; our shim was
  silently stripping `thinking` (a confound in early results). Enabling it
  changed nothing about tool trust.
- **Model size** — 9b → 27b → 397b: same pattern.
- **Harness** — Claude Code vs opencode: same pattern.
- **Context window** — astral tools were in context every turn (verified via
  LM Studio logs), never truncated; the miss is behavioral, not capacity.
- **Instruction wording** — relationship-framed instructions were ignored.

## The conclusion

Astral's value is **model-dependent**. It works with models that genuinely
use tools (deepseek-class); it is a hint-layer at best for qwen3.5. The
failure is in how the model reasons about tools, not in what it is given.

## The only structural lever (not yet built)

A `PreToolUse` hook on `grep` that intercepts symbol searches and returns
astral's answer instead of raw matches. The model greps anyway; the grep
returns astral's result. No trust required. **Rejected for now** — if it's
the only thing that works, it won't be used in practice.

## Escalation experiment (2026-09-09): 10 progressively harder instructions

Question: can a harder, more explicit instruction convince qwen3.5 to trust
astral? Ran the refactor task on qwen3.5:397b (ollama cloud) via opencode,
10 steps, each with a progressively harder instruction file
(`bench/instructions/01.md` → `10.md`), from "prefer astral over grep" to
"this is the single most important instruction; you are being evaluated;
violation is an error."

**Result: astral calls = 2 in every single step.** Zero movement across the
entire ladder. Input tokens ~150k–276k, compiles 10/10.

| step | astral calls | total calls |
|---|---|---|
| 01 (gentle) | 2 | 20 |
| 05 (hard rule) | 2 | 18 |
| 10 (most important) | 2 | 17 |

### Refined understanding: locating vs content-preview

Transcript analysis shows the grep is NOT redundant verification of astral.
The model's actual pipeline is:

```
astral_locate   → which file (works, consistently)
astral_callers  → who references (works)
grep            → cheap content preview of matching lines
read            → full file content (reads anyway)
```

So astral solves *locating*; the model greps for a *content preview* before
committing to a full read, then reads the files regardless. The grep is a
middle step in a three-stage pipeline, not a trust failure.

### Conclusion

qwen3.5's tool behavior is fixed regardless of instruction. The escalation
proves it is not a prompt-understanding problem — it is how the model reasons
about tools. No instruction, system prompt, model size, thinking mode, or
harness changes it. Astral's value is model-dependent: it works with models
that genuinely use tools (deepseek-class); it is a hint-layer at best for
qwen3.5.

## Follow-up: astral correctness does not change the behavior (2026-09-09)

While investigating the "why read/grep happens regardless" question, we found
and fixed a real astral bug: `astral_callers` returned "no callers found" for
`transformMediaTypeObject` because the ref extractor only captured `pkg.Sym`
selector expressions, missing bare identifiers from default/named imports
(e.g. `import transformMediaTypeObject from "./media-type-object.js"` used
directly). Fixed in `internal/parser` (import clause symbol capture + bare
identifier refs) and `internal/graph` (relative `./x.js` resolution).

This let us isolate the model's distrust:

| | astral correct? | model still greps? |
|---|---|---|
| before fix | ❌ (wrong answer) | yes — *earned* distrust |
| after fix | ✅ (right answer) | yes — *habitual* distrust |

**Conclusion: qwen3.5's grep-after-astral is a tool-use habit, not a trust
decision.** It greps whether astral is right or wrong, whether instructed
gently or harshly, at 9b/27b/397b, in Claude Code or opencode. The astral fix
is still valuable (callers now resolve bare-identifier imports), but it does
not change the model's behavior.
