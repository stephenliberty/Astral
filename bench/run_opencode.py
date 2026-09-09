#!/usr/bin/env python3
"""Astral benchmark runner for opencode (headless) against Ollama Cloud.

Drives `opencode run --format json` with the astral MCP server registered in
opencode's config, and reports token usage from the step_finish events.

Usage:
  python3 bench/run_opencode.py <task-file> <label> [options]

Options:
  --astral                (accepted for parity; astral MCP is always available
                           via opencode config — this flag is a no-op marker)
  --instruction           prepend the astral steering instruction
  --steer <text>          prepend a custom steering instruction
  --iterations N          run the task N times (fresh repo reset each time)
  --model <provider/model> opencode model (default: ollama-cloud/qwen3.5:397b)
  --repo <path>           target repository (default: ./bench-repo)
  --verify-cwd <subdir>   run the verifier in this repo-relative subdir
  --language <go|ts>      selects the default verifier (default: go)
  --verify <pkg...>       Go packages to build after each run
  --log-dir <path>        transcript output dir (default: ./bench-out)
  --claude <path>         ignored (opencode binary from PATH)

Environment:
  ASTRAL_BIN               astral CLI binary path
  OPENCODE_BIN             opencode binary (default: 'opencode')
"""
import json
import os
import shutil
import subprocess
import sys
import time

LOG_DIR = os.environ.get("ASTRAL_BENCH_OUT", "bench-out")
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
VERIFIERS = {
    "go": "go build {targets}",
    "ts": "tsc --noEmit -p tsconfig.json",
}


def parse_args():
    argv = sys.argv[1:]
    task_file, label = argv[0], argv[1]
    use_instruction = "--instruction" in argv
    steer = None
    instruction_file = None
    backend, model, iterations = "ollama", "ollama-cloud/qwen3.5:397b", 1
    language, verify_cmd = "go", None
    verify_targets = []
    repo, log_dir, verify_cwd = None, None, None
    agent = None
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
        elif a == "--model":
            model = need_flag()
        elif a == "--agent":
            agent = need_flag()
        elif a == "--instruction-file":
            instruction_file = need_flag()
        elif a == "--steer":
            steer = need_flag()
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
        elif a == "--repo":
            repo = need_flag()
        elif a == "--log-dir":
            log_dir = need_flag()
        i += 1
    return dict(task_file=task_file, label=label,
                use_instruction=use_instruction, steer=steer,
                instruction_file=instruction_file,
                model=model, agent=agent, iterations=iterations,
                language=language, verify_cmd=verify_cmd,
                verify_targets=verify_targets, repo=repo, log_dir=log_dir,
                verify_cwd=verify_cwd)


def go_targets(targets):
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


def build_verifier(cfg):
    if cfg["verify_cmd"]:
        return cfg["verify_cmd"]
    if not cfg["verify_targets"] and cfg["language"] == "go":
        return None
    template = VERIFIERS.get(cfg["language"])
    if not template:
        raise SystemExit(
            f"no default verifier for language '{cfg['language']}'; pass --verify-cmd")
    targets = go_targets(cfg["verify_targets"]) if cfg["language"] == "go" else " ".join(cfg["verify_targets"])
    return template.format(targets=targets)


def toolchain_dirs(language):
    dirs = []
    for tool in ("go", "node", "npm", "npx"):
        path = shutil.which(tool)
        if path:
            d = os.path.dirname(os.path.abspath(path))
            if d not in dirs:
                dirs.append(d)
    # Known Go install locations not on the current PATH.
    for cand in ("/home/stephen/.local/go/bin", "/usr/local/go/bin"):
        if os.path.isdir(cand) and cand not in dirs:
            dirs.append(cand)
    return dirs


def run_once(cfg, iter_idx):
    with open(cfg["task_file"]) as f:
        prompt = f.read().strip()
    if cfg["instruction_file"]:
        with open(cfg["instruction_file"]) as f:
            prompt = f.read().strip() + "\n\n" + prompt
    elif cfg["use_instruction"]:
        prompt = ADVISOR_INSTRUCTION + "\n" + prompt
    elif cfg["steer"]:
        prompt = f"Instruction: {cfg['steer']}\n" + prompt

    repo = cfg["repo"]
    opencode_bin = os.environ.get("OPENCODE_BIN", "opencode")

    env = dict(os.environ)
    extra = []
    astral_bin = os.environ.get("ASTRAL_BIN")
    if astral_bin:
        extra.append(os.path.dirname(os.path.abspath(astral_bin)))
    extra += toolchain_dirs(cfg["language"])
    if cfg["language"] in ("ts", "tsnocheck", "js"):
        if cfg.get("verify_cwd"):
            extra.append(os.path.join(repo, cfg["verify_cwd"], "node_modules", ".bin"))
        extra.append(os.path.join(repo, "node_modules", ".bin"))
    env["PATH"] = ":".join(extra + [env.get("PATH", "")])

    cmd = [opencode_bin, "run", "--model", cfg["model"],
           "--format", "json", "--auto", "--dir", repo]
    if cfg["agent"]:
        cmd += ["--agent", cfg["agent"]]
    cmd.append(prompt)

    log_dir = cfg["log_dir"]
    os.makedirs(log_dir, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    suffix = f"{cfg['label']}-it{iter_idx}-{stamp}" if iter_idx else f"{cfg['label']}-{stamp}"
    base = os.path.join(log_dir, suffix)
    with open(base + ".prompt.txt", "w") as f:
        f.write(prompt)
    start = time.monotonic()
    with open(base + ".stream.jsonl", "w") as tf, open(base + ".stderr.log", "w") as ef:
        proc = subprocess.Popen(cmd, cwd=repo, env=env,
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
            proc.wait(timeout=1800)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    elapsed = time.monotonic() - start
    proc_stdout = "".join(stdout_lines)

    total_input = total_output = total_reasoning = 0
    tool_calls = 0
    tool_names = []
    for line in proc_stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        t = ev.get("type")
        if t == "step_finish":
            toks = ev.get("part", {}).get("tokens", {})
            total_input += toks.get("input", 0)
            total_output += toks.get("output", 0)
            total_reasoning += toks.get("reasoning", 0)
        elif t == "tool_use":
            tool_calls += 1
            tool_names.append(ev.get("part", {}).get("tool", "?"))

    return {
        "label": cfg["label"],
        "harness": "opencode",
        "model": cfg["model"],
        "iteration": iter_idx,
        "elapsed_seconds": round(elapsed, 1),
        "total_input_tokens": total_input,
        "total_output_tokens": total_output,
        "total_reasoning_tokens": total_reasoning,
        "tool_calls": tool_calls,
        "tool_names": tool_names,
        "astral_calls": sum(1 for n in tool_names if "astral" in n),
        "compiles": None,
        "_transcript_base": base,
        "stderr_tail": stderr_text[-300:] if stderr_text else "",
    }


def verify_compiles(d, cmd, repo, language, verify_cwd=None):
    env = dict(os.environ)
    extra = toolchain_dirs(language)
    if language in ("ts", "tsnocheck", "js"):
        if verify_cwd:
            extra.append(os.path.join(repo, verify_cwd, "node_modules", ".bin"))
        extra.append(os.path.join(repo, "node_modules", ".bin"))
    env["PATH"] = ":".join(extra + [env.get("PATH", "")])
    verify_cwd = os.path.join(repo, verify_cwd) if verify_cwd else repo
    proc = subprocess.run(cmd, cwd=verify_cwd, env=env, shell=True,
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
    if not os.path.exists(repo):
        raise SystemExit(f"repo {repo} does not exist")
    script = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                          "reset_repo.sh")
    subprocess.run(["bash", script, repo, astral_bin],
                   capture_output=True, check=False)


def main():
    cfg = parse_args()
    if cfg["repo"] is None:
        cfg["repo"] = "bench-repo"
    if cfg["log_dir"] is None:
        cfg["log_dir"] = LOG_DIR
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
              f"calls={d['tool_calls']} astral_calls={d.get('astral_calls', 0)} "
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
        "harness": "opencode",
        "model": cfg["model"],
        "n": cfg["iterations"],
        "mean_input_tokens": round(mean(lambda r: r["total_input_tokens"])),
        "min_input_tokens": min((r["total_input_tokens"] for r in runs)),
        "max_input_tokens": max((r["total_input_tokens"] for r in runs)),
        "mean_elapsed_seconds": round(mean(lambda r: r["elapsed_seconds"]), 1),
        "mean_tool_calls": round(mean(lambda r: r["tool_calls"]), 1),
        "mean_astral_calls": round(mean(lambda r: r.get("astral_calls", 0)), 1),
        "compile_count": sum(1 for r in runs if r["compiles"] is True),
        "compile_total": sum(1 for r in runs if r["compiles"] is not None),
    }
    print(json.dumps(agg))


if __name__ == "__main__":
    main()
