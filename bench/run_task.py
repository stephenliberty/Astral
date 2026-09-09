#!/usr/bin/env python3
"""Astral benchmark runner: drive an LLM coding agent against a target repo
with and without astral's MCP tools, and report token usage.

Methodology: the baseline and astral arms are identical EXCEPT the astral arm
registers the astral MCP server (adding the astral_* tools). They share the
same prompt, tool surface, backend, and model. Pass --tools and/or
--instruction to BOTH arms if you vary them, so the comparison isolates the
effect of the astral tools themselves.

Usage:
  python bench/run_task.py <task-file> <label> [options]

Reads the task prompt from <task-file>, runs `claude -p` with stream-json
output, and sums the input tokens consumed across all requests (ground-truth
usage from the backend).

Options:
  --astral                register the astral MCP server (adds astral_* tools;
                          generated config sets alwaysLoad so tools load
                          upfront rather than behind ToolSearch)
  --tools <list>           comma-separated tool surface for BOTH arms
                           (default: Claude Code's default tool list). Pass the
                           same value to baseline and astral arms so they
                           differ only by the astral MCP tools.
  --instruction           prepend the "use astral" instruction to the user
                           message (default: off; for a clean isolate of the
                           MCP effect, set it on both arms)
  --system-prompt         send the astral instruction via --append-system-prompt
                           (system channel, higher authority than the user
                           message). Mutually exclusive with --instruction.
  --steer <text>          prepend a one-line instruction to steer behavior
                           (same as --instruction but with custom text).
                           Bash is NOT blocked — deprioritizing code-hunting
                           is done through the instruction, since scoped
                           deny rules break legit grep on logs/build output.
  --preload <sym,...>     run `astral locate`+`astral callers` against each
                           symbol BEFORE the run and inject the answers into
                           the prompt. Makes grep/Read for those symbols pure
                           redundancy, so a token-greedy model skips them.
                           E.g. --preload transformMediaTypeObject
  --iterations N           run the task N times (fresh repo reset each time)
  --backend <lmstudio|ollama>     model backend (default: lmstudio)
  --model <name>           model id passed to claude --model (ollama default:
                           deepseek-v4-flash:cloud)
  --language <go|ts|...>   target repo language; selects the default verifier
                           and toolchain (default: go)
  --verify <pkg...>        packages/targets to verify after each run (Go:
                           `go build ./<pkg>/`; for the default TS verifier
                           these are passed as tsconfig/dir hints)
  --verify-cmd <command>   fully custom verification command (runs in the
                           repo with the toolchain on PATH). Overrides the
                           language default.
  --repo <path>            target repository (default: ./bench-repo)
  --verify-cwd <subdir>    run the verifier in this repo-relative subdir
                           (e.g. packages/openapi-typescript for a workspace)
  --log-dir <path>         transcript output dir (default: ./bench-out)
  --mcp <path>             MCP config JSON (default: generated)
  --claude <path>          claude binary (default: $CLAUDE_BIN or 'claude')

Environment:
  ASTRAL_BIN               astral CLI binary path
  ASTRAL_MCP_BIN           astral MCP server binary path
"""
import json
import os
import shutil
import subprocess
import sys
import time

LOG_DIR = os.environ.get("ASTRAL_BENCH_OUT", "bench-out")
# Steering instruction: astral owns code exploration and relationship discovery
# (where a symbol lives, who references it, what a module contains); Bash is for
# executing the code (build/typecheck/test) and for inspecting runtime output.
# A strong prior, not a tool block (Bash remains fully available).
ADVISOR_INSTRUCTION = (
    "Tool discipline: astral is the authoritative, pre-indexed map of this "
    "codebase's structure and relationships.\n"
    "  - To locate a symbol, use astral_locate.\n"
    "  - To understand a module's contents and conventions, use "
    "astral_module.\n"
    "  - To find what depends on, imports, or references a symbol — its "
    "relationship graph, blast radius, and impact — use astral_callers.\n"
    "  - To know which tests exercise code you are changing, use "
    "astral_affected.\n"
    "Never use grep/ripgrep/find/ls to explore the code or discover these "
    "relationships; astral already knows and answers instantly.\n"
    "Reserve Bash for executing the code: build, typecheck, run tests, and "
    "inspect runtime output.\n"
    "Before reading any source file, consult astral first.\n"
)

# Default verifier commands per language. {targets} is replaced with the
# space-joined --verify arguments (empty if none). The command runs in the
# repo with the language toolchain prepended to PATH.
VERIFIERS = {
    "go": "go build {targets}",
    "ts": "tsc --noEmit -p tsconfig.json",
    "tsnocheck": "tsc --noEmit --skipLibCheck -p tsconfig.json",
}


def go_targets(targets):
    """Normalize Go package targets: 'metrics' -> './metrics/' (a path, not
    an import)."""
    out = []
    for t in targets:
        t = t.strip()
        if not t:
            continue
        if not t.startswith("./") and not t.startswith("."):
            t = "./" + t
        if not t.endswith("/"):
            t += "/"
        out.append(t)
    return " ".join(out)


def parse_args():
    argv = sys.argv[1:]
    task_file, label = argv[0], argv[1]
    use_astral = "--astral" in argv
    use_instruction = "--instruction" in argv
    use_system_prompt = "--system-prompt" in argv
    steer = None
    tools = None
    backend, model, iterations = "lmstudio", None, 1
    language, verify_cmd = "go", None
    verify_targets = []
    preload = []
    repo, log_dir, claude, mcp, verify_cwd = None, None, None, None, None
    i = 2
    while i < len(argv):
        a = argv[i]

        def need_flag():
            nonlocal i
            if i + 1 >= len(argv):
                raise SystemExit(f"missing value for {a}")
            i += 1
            return argv[i]

        if a == "--iterations":
            iterations = int(need_flag())
        elif a == "--backend":
            backend = need_flag()
        elif a == "--model":
            model = need_flag()
        elif a == "--tools":
            tools = need_flag()
        elif a == "--steer":
            steer = need_flag()
        elif a == "--preload":
            # comma-separated: symbol or symbol:kind; each is answered by
            # `astral locate` / `astral callers` and injected into the prompt.
            for raw in need_flag().split(","):
                raw = raw.strip()
                if raw:
                    preload.append(raw)
        elif a == "--verify-cwd":
            verify_cwd = need_flag()
        elif a == "--language":
            language = need_flag()
        elif a == "--verify-cmd":
            verify_cmd = need_flag()
        elif a == "--verify":
            i += 1
            while i < len(argv) and not argv[i].startswith("--"):
                verify_targets.append(argv[i])
                i += 1
            continue
        elif a == "--verify-cwd":
            verify_cwd = need_flag()
        elif a == "--repo":
            repo = need_flag()
        elif a == "--log-dir":
            log_dir = need_flag()
        elif a == "--mcp":
            mcp = need_flag()
        elif a == "--claude":
            claude = need_flag()
        i += 1
    return dict(task_file=task_file, label=label, use_astral=use_astral,
                use_instruction=use_instruction,
                use_system_prompt=use_system_prompt, steer=steer, tools=tools,
                preload=preload, iterations=iterations, backend=backend,
                model=model, language=language, verify_cmd=verify_cmd,
                verify_targets=verify_targets, repo=repo, log_dir=log_dir,
                mcp=mcp, claude=claude, verify_cwd=verify_cwd)


def toolchain_dirs(language):
    """Return directories to prepend to PATH for the language toolchain."""
    dirs = []
    for tool in ("go", "node", "npm", "npx"):
        path = shutil.which(tool)
        if path:
            d = os.path.dirname(os.path.abspath(path))
            if d not in dirs:
                dirs.append(d)
    return dirs


def build_verifier(cfg):
    """Return the verification command, or None if nothing should verify.

    The verifier runs when the caller passes --verify-cmd (custom), --verify
    targets (used with the language default), or simply sets --language to a
    language with a default verifier (e.g. TS typechecks even with no packages
    listed).
    """
    if cfg["verify_cmd"]:
        return cfg["verify_cmd"]
    if not cfg["verify_targets"] and cfg["language"] == "go":
        # Go requires explicit package targets; no targets means no default.
        return None
    key = cfg["language"]
    template = VERIFIERS.get(key)
    if not template:
        raise SystemExit(
            f"no default verifier for language '{key}'; pass --verify-cmd")
    targets = go_targets(cfg["verify_targets"]) if key == "go" else " ".join(cfg["verify_targets"])
    return template.format(targets=targets)


def preload_answers(astral_bin, repo, preload):
    """Run `astral locate <sym>` / `astral callers <sym>` from repo root for each
    --preload entry and return the combined text. This makes astral's answers
    part of the prompt itself, so grep+Read would only duplicate what the model
    already has — a token-greedy model skips the redundancy."""
    if not preload:
        return ""
    parts = ["[Pre-loaded from astral; the answers below are already known. "
             "If you still grep/search for them you are wasting tokens.]"]
    env = dict(os.environ)
    for item in preload:
        sym = item.split(":", 1)[0].strip()
        for cmd, label in (("locate", "locate "), ("callers", "callers ")):
            proc = subprocess.run([astral_bin, cmd, sym], cwd=repo, env=env,
                                  capture_output=True, text=True, timeout=30)
            out = proc.stdout.strip()
            parts.append(f"--- astral {label}{sym} ---\n{out}")
    return "\n\n".join(parts) + "\n\n"


def run_once(cfg, iter_idx):
    with open(cfg["task_file"]) as f:
        prompt = f.read().strip()
    # The steering instruction can go in the user message (--instruction /
    # --steer) or in the system prompt (--system-prompt). The system channel
    # carries more authority; the experiment is whether that changes qwen's
    # tool trust.
    system_instruction = None
    if cfg["use_system_prompt"]:
        system_instruction = ADVISOR_INSTRUCTION
    elif cfg["use_instruction"]:
        prompt = ADVISOR_INSTRUCTION + "\n" + prompt
    elif cfg["steer"]:
        prompt = f"Instruction: {cfg['steer']}\n" + prompt
    if cfg["preload"]:
        # Preload needs the astral CLI; run it before constructing the prompt.
        astral_bin = os.environ.get("ASTRAL_BIN", "astral")
        prompt = preload_answers(astral_bin, cfg["repo"], cfg["preload"]) + prompt

    repo = cfg["repo"]
    claude = cfg["claude"]
    # The agent works from the repo root so astral (whose MCP server derives
    # its index root from cwd) resolves the correct .astral index. The
    # verifier may still run in a workspace subdir via --verify-cwd.
    agent_cwd = repo

    env = dict(os.environ)
    env["CLAUDE_CODE_ATTRIBUTION_HEADER"] = "0"
    # Put astral's binary dir and the language toolchain on the agent's PATH
    # so it can self-verify (e.g. `go build`, `npx tsc`). Without it the model
    # wastes tokens hunting for the binary and ships unverified (broken) code.
    extra = []
    astral_bin = os.environ.get("ASTRAL_BIN")
    if astral_bin:
        extra.append(os.path.dirname(os.path.abspath(astral_bin)))
    extra += toolchain_dirs(cfg["language"])
    # For TS/JS repos the compiler lives in node_modules/.bin (hoisted to the
    # workspace root or in the package dir); expose both so the agent can run
    # `npx tsc` / `tsc` to self-verify.
    if cfg["language"] in ("ts", "tsnocheck", "js"):
        if cfg.get("verify_cwd"):
            extra.append(os.path.join(repo, cfg["verify_cwd"], "node_modules", ".bin"))
        extra.append(os.path.join(repo, "node_modules", ".bin"))
    env["PATH"] = ":".join(extra + [env.get("PATH", "")])

    # Base cmd.
    cmd = [claude, "-p", "--output-format", "stream-json", "--verbose",
           "--dangerously-skip-permissions"]
    if cfg["backend"] == "ollama":
        env["ANTHROPIC_BASE_URL"] = os.environ.get(
            "ANTHROPIC_BASE_URL", "http://localhost:11434")
        env["ANTHROPIC_AUTH_TOKEN"] = "ollama"
        env["ANTHROPIC_API_KEY"] = ""
        cmd += ["--model", cfg["model"] or "deepseek-v4-flash:cloud"]
    else:  # lmstudio via shim
        env["ANTHROPIC_BASE_URL"] = os.environ.get(
            "ASTRAL_ANTHROPIC_BASE_URL", "http://127.0.0.1:1235")
        env["ANTHROPIC_AUTH_TOKEN"] = os.environ.get(
            "ASTRAL_ANTHROPIC_TOKEN", "lmstudio")
    cmd.append(prompt)
    # System-prompt channel: higher authority than the user message. The
    # experiment is whether qwen trusts astral more when the instruction is
    # in the system prompt rather than the task text.
    if system_instruction:
        cmd += ["--append-system-prompt", system_instruction]
    # Both arms use the default tool list (Claude Code's full surface). The
    # astral arm additionally registers the astral MCP server; that is the ONLY
    # difference. --tools can restrict the surface, but must be identical for
    # both arms to avoid a confound. Bash is deliberately NOT blocked here:
    # scoped deny rules on grep/find break legitimate uses (searching build
    # output, test fixtures, logs), and blocking all Bash kills self-verify.
    # Deprioritizing code-hunting is done via the steering instruction, which
    # the model weighs as a prior, not via a hard tool block.
    if cfg["tools"]:
        cmd += ["--tools", cfg["tools"]]
    if cfg["use_astral"] and cfg["mcp"]:
        cmd += ["--mcp-config", cfg["mcp"]]

    log_dir = cfg["log_dir"]
    os.makedirs(log_dir, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    suffix = f"{cfg['label']}-it{iter_idx}-{stamp}" if iter_idx else f"{cfg['label']}-{stamp}"
    base = os.path.join(log_dir, suffix)
    with open(base + ".prompt.txt", "w") as f:
        f.write(prompt)
    start = time.monotonic()
    with open(base + ".stream.jsonl", "w") as tf, open(base + ".stderr.log", "w") as ef:
        proc = subprocess.Popen(cmd, cwd=agent_cwd, env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                text=True)
        stdout_lines = []
        for line in proc.stdout:
            tf.write(line)
            tf.flush()
            stdout_lines.append(line)
        stderr_text = proc.stderr.read()
        ef.write(stderr_text)
        ef.flush()
        try:
            proc.wait(timeout=900)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    elapsed = time.monotonic() - start
    proc_stdout = "".join(stdout_lines)

    total_input = total_output = total_cache_read = total_cache_write = 0
    thinking_blocks = 0
    thinking_chars = 0
    tool_calls = 0
    tool_names = []
    result = None
    for line in proc_stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        t = ev.get("type")
        if t == "assistant":
            u = ev.get("message", {}).get("usage", {})
            total_input += u.get("input_tokens", 0)
            total_output += u.get("output_tokens", 0)
            total_cache_read += u.get("cache_read_input_tokens", 0)
            total_cache_write += u.get("cache_creation_input_tokens", 0)
            for c in ev.get("message", {}).get("content", []):
                if c.get("type") == "tool_use":
                    tool_calls += 1
                    tool_names.append(c.get("name", "?"))
                elif c.get("type") == "thinking":
                    # qwen is a hybrid-thinking model; the reasoning effort
                    # shows up as thinking content blocks. Claude Code reports
                    # thinking_tokens=0 on this proxy path, so count blocks
                    # and characters as the reasoning-effort measure.
                    thinking_blocks += 1
                    thinking_chars += len(c.get("thinking", ""))
        elif t == "result":
            result = ev

    return {
        "label": cfg["label"],
        "advisor": cfg["use_astral"],
        "iteration": iter_idx,
        "elapsed_seconds": round(elapsed, 1),
        "total_input_tokens": total_input,
        "total_output_tokens": total_output,
        "total_cache_read_tokens": total_cache_read,
        "total_cache_write_tokens": total_cache_write,
        # qwen reasoning effort (hybrid thinking model). Claude's proxy path
        # reports thinking_tokens=0, so block count + chars are the measure.
        "thinking_blocks": thinking_blocks,
        "thinking_chars": thinking_chars,
        "tool_calls": tool_calls,
        "tool_names": tool_names,
        "advisor_calls": sum(1 for n in tool_names if "astral" in n),
        "terminal_reason": (result or {}).get("terminal_reason"),
        "result_text": (result or {}).get("result", "")[:200],
        "compiles": None,
        "_transcript_base": base,
        "stderr_tail": stderr_text[-300:] if stderr_text else "",
    }


def verify_compiles(d, cmd, repo, language, verify_cwd=None):
    env = dict(os.environ)
    extra = toolchain_dirs(language)
    # For TS/JS repos the compiler lives in node_modules/.bin, which may be
    # hoisted to the workspace root or live in the package dir. Add both.
    if language in ("ts", "tsnocheck", "js"):
        if verify_cwd:
            extra.append(os.path.join(repo, verify_cwd, "node_modules", ".bin"))
        extra.append(os.path.join(repo, "node_modules", ".bin"))
    env["PATH"] = ":".join(extra + [env.get("PATH", "")])
    # The verifier runs wherever the agent worked (verify_cwd if set), since
    # workspace sub-packages compile relative to their own tsconfig/package.
    verify_cwd = os.path.join(repo, verify_cwd) if verify_cwd else repo
    if isinstance(cmd, str):
        proc = subprocess.run(cmd, cwd=verify_cwd, env=env, shell=True,
                              capture_output=True, text=True, timeout=240)
    else:
        proc = subprocess.run(cmd, cwd=verify_cwd, env=env,
                              capture_output=True, text=True, timeout=240)
    d["compiles"] = (proc.returncode == 0)
    if not d["compiles"]:
        d["compile_error"] = proc.stderr.strip()[:500]
        base = d.get("_transcript_base")
        if base:
            with open(base + ".compile_error.txt", "w") as f:
                f.write((proc.stderr + "\n" + proc.stdout).strip())
    return d


def reset_repo(repo, astral_bin):
    """Reset the target repo to a pristine checkout and re-index with astral."""
    if not os.path.exists(repo):
        raise SystemExit(f"repo {repo} does not exist")
    script = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                          "reset_repo.sh")
    subprocess.run(["bash", script, repo, astral_bin],
                   capture_output=True, check=False)


def ensure_mcp_config(cfg):
    """If the advisor arm has no explicit --mcp config, generate one from
    ASTRAL_MCP_BIN (or a sibling of ASTRAL_BIN). The generated config sets
    alwaysLoad: true so the astral tools are loaded into context upfront
    rather than deferred behind ToolSearch. Returns the config path or None
    if no astral MCP binary can be found (warns but proceeds without it)."""
    if not cfg["use_astral"] or cfg["mcp"]:
        return cfg["mcp"]
    mcp_bin = os.environ.get("ASTRAL_MCP_BIN")
    if not mcp_bin:
        astral_bin = os.environ.get("ASTRAL_BIN")
        if astral_bin:
            cand = os.path.join(os.path.dirname(os.path.abspath(astral_bin)),
                                "astral-mcp")
            if os.path.exists(cand):
                mcp_bin = cand
    if not mcp_bin or not os.path.exists(mcp_bin):
        print(f"WARN: astral arm requested but no astral MCP binary found "
              f"(set ASTRAL_MCP_BIN); running without astral tools",
              file=sys.stderr)
        return None
    if cfg["log_dir"]:
        os.makedirs(cfg["log_dir"], exist_ok=True)
    cfg_path = os.path.abspath(os.path.join(cfg["log_dir"], "mcp-config.json"))
    with open(cfg_path, "w") as f:
        json.dump({
            "mcpServers": {
                "astral": {"command": mcp_bin, "args": [], "alwaysLoad": True}
            }
        }, f)
    return cfg_path


def main():
    cfg = parse_args()
    if cfg["repo"] is None:
        cfg["repo"] = "bench-repo"
    if cfg["log_dir"] is None:
        cfg["log_dir"] = LOG_DIR
    if cfg["claude"] is None:
        cfg["claude"] = os.environ.get("CLAUDE_BIN", "claude")
    cfg["mcp"] = ensure_mcp_config(cfg)
    astral_bin = os.environ.get("ASTRAL_BIN", "astral")
    verifier_cmd = build_verifier(cfg)

    runs = []
    for i in range(cfg["iterations"]):
        reset_repo(cfg["repo"], astral_bin)
        d = run_once(cfg, i)
        if verifier_cmd:
            verify_compiles(d, verifier_cmd, cfg["repo"], cfg["language"], cfg.get("verify_cwd"))
        runs.append(d)
        print(f"[{cfg['label']} it{i}] input={d['total_input_tokens']:,} "
              f"calls={d['tool_calls']} advisor_calls={d.get('advisor_calls', 0)} "
              f"time={d['elapsed_seconds']}s compiles={d['compiles']}",
              file=sys.stderr, flush=True)

    if cfg["iterations"] == 1:
        d = dict(runs[0])
        d.pop("_transcript_base", None)
        print(json.dumps(d, indent=2))
        return

    def mean(fn):
        vals = [fn(r) for r in runs if fn(r) is not None]
        return sum(vals) / len(vals) if vals else 0

    agg = {
        "label": cfg["label"],
        "advisor": cfg["use_astral"],
        "language": cfg["language"],
        "n": cfg["iterations"],
        "mean_input_tokens": round(mean(lambda r: r["total_input_tokens"])),
        "min_input_tokens": min((r["total_input_tokens"] for r in runs)),
        "max_input_tokens": max((r["total_input_tokens"] for r in runs)),
        "mean_elapsed_seconds": round(mean(lambda r: r["elapsed_seconds"]), 1),
        "mean_tool_calls": round(mean(lambda r: r["tool_calls"]), 1),
        "mean_advisor_calls": round(mean(lambda r: r.get("advisor_calls", 0)), 1),
        "compile_count": sum(1 for r in runs if r["compiles"] is True),
        "compile_total": sum(1 for r in runs if r["compiles"] is not None),
    }
    print(json.dumps(agg))


if __name__ == "__main__":
    main()
