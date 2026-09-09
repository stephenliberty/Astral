#!/usr/bin/env python3
"""Parse LM Studio server logs to audit a benchmark run.

Every inference request LM Studio serves is logged with:
  - the full request body (model, tools array, messages/system)
  - `prompt processing, n_tokens = <N>, progress = 1.00` — the engine's actual
    prefill size for the request (ground truth, independent of the client)
  - `stop processing: n_tokens = <N>, truncated = <0|1>`

This lets the harness confirm two things the stream-json transcript can't:
  1. Were the astral MCP tool definitions actually present in the model's
     context for a given run (i.e. was "tool visibility" the problem)?
  2. What were the engine-level prompt sizes and was anything truncated?

Usage:
  python3 bench/parse_lms_log.py <logfile> [--start HH:MM:SS] [--end HH:MM:SS]
                                  [--astral-only]

Output (JSON): window, stats over completed prefill sizes, truncation count,
and whether astral_* tool defs appeared in any request body in the window.
"""
import argparse
import glob
import json
import os
import re
import sys

PREFILL_RE = re.compile(
    r"\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}).*?"
    r"prompt processing, n_tokens =\s+(\d+),\s*progress = 1\.00")
STOP_RE = re.compile(
    r"\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}).*?"
    r"stop processing: n_tokens =\s+(\d+),\s*truncated =\s+(\d+)")
ASTRAL_TOOL_RE = re.compile(r'"name":\s*"mcp__astral__[\w]+"')
TIME_RE = re.compile(r"\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}")


def in_window(ts, start, end):
    t = ts[11:]  # HH:MM:SS
    return (not start or t >= start) and (not end or t <= end)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("log", help="LM Studio server log file (or glob)")
    ap.add_argument("--start", help="window start HH:MM:SS")
    ap.add_argument("--end", help="window end HH:MM:SS")
    ap.add_argument("--astral-only", action="store_true",
                    help="report only requests whose body mentioned astral tools")
    args = ap.parse_args()

    files = sorted(glob.glob(args.log))
    if not files:
        print(json.dumps({"error": f"no files matched {args.log}"}))
        sys.exit(1)

    prefills, stops, astral_lines = [], [], 0
    window_hits = 0
    for path in files:
        with open(path, errors="replace") as f:
            for line in f:
                m = TIME_RE.search(line)
                ts = m.group(0) if m else None
                if not ts or not in_window(ts, args.start, args.end):
                    continue
                window_hits += 1
                pm = PREFILL_RE.search(line)
                if pm:
                    prefills.append(int(pm.group(2)))
                sm = STOP_RE.search(line)
                if sm:
                    stops.append((int(sm.group(2)), int(sm.group(3))))
                if ASTRAL_TOOL_RE.search(line):
                    astral_lines += 1

    if not window_hits:
        print(json.dumps({
            "window": f"{args.start or '*'}-{args.end or '*'}",
            "error": "no matching lines in window (log may predate window, or "
                     "server-logs/ is on the Windows side)",
        }))
        sys.exit(0)

    astral_tools_visible = astral_lines > 0
    n_truncated = sum(1 for _, t in stops if t)
    def stats(vals):
        if not vals:
            return {"n": 0}
        vals = sorted(vals)
        return {
            "count": len(vals),
            "min": vals[0],
            "max": vals[-1],
            "sum": sum(vals),
            "mean": round(sum(vals) / len(vals)),
            "p90": vals[int(0.9 * (len(vals) - 1))],
        }

    print(json.dumps({
        "window": f"{args.start or '*'}-{args.end or '*'}",
        "files": files,
        "log_lines_in_window": window_hits,
        "requests_with_prefill_tokens": {
            "count": len(prefills),
            **{k: v for k, v in stats(prefills).items()},
        },
        "requests_stopped": {
            "count": len(stops),
            "max_n_tokens": max((n for n, _ in stops), default=None),
            "truncated_count": n_truncated,
        },
        "astral_tools_in_context_visible": astral_tools_visible,
        "astral_tool_def_lines": astral_lines,
        "note": ("astral_tools_in_context_visible=True means the model's "
                 "requests contained astral_* tool definitions, i.e. they "
                 "were available but unused — a behavioral miss, not a "
                 "visibility miss" if astral_tools_visible else
                 "no astral tool definitions appeared in the window"),
    }, indent=2))


if __name__ == "__main__":
    main()
