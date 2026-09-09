#!/usr/bin/env python3
"""Anthropic-compat shim: forwards Claude Code requests to LM Studio.

Fixes the qwen chat-template incompatibility where an inline
`role: "system"` message after a user message is rejected. The shim hoists
inline system messages into the top-level `system` field (prepended), which
qwen's template requires.

Also translates thinking/reasoning fields. Claude Code sends adaptive thinking;
LM Studio and Ollama both accept it natively and qwen reasons with it. Thinking
is env-gated so the old "no reasoning" harness behavior can be reproduced.
"""
import http.server
import json
import os
import sys
import urllib.request

UPSTREAM = "http://192.168.50.94:1234"
PORT = 1235
TARGET_MODEL = os.environ.get("SHIM_MODEL", "")  # e.g. "qwen/qwen3.6-27b"
# Thinking stays ON by default; set ASTRAL_SHIM_THINKING=off to disable.
# A per-request `X-Astral-Thinking: off` header overrides it for that call.
DEFAULT_STRIP_THINKING = os.environ.get("ASTRAL_SHIM_THINKING", "on") == "off"


def strip_thinking(headers):
    val = headers.get("X-Astral-Thinking")
    if val is not None:
        return val.strip().lower() == "off"
    return DEFAULT_STRIP_THINKING


def fix_body(data, s_thinking):
    if not isinstance(data, dict):
        return data
    if TARGET_MODEL:
        data["model"] = TARGET_MODEL
    messages = data.get("messages", [])
    inline_system = [m for m in messages if m.get("role") == "system"]
    if inline_system:
        messages = [m for m in messages if m.get("role") != "system"]
        # Prepend inline system content to the top-level system field.
        top = data.get("system", [])
        if isinstance(top, str):
            top = [{"type": "text", "text": top}]
        elif not isinstance(top, list):
            top = []
        for m in inline_system:
            content = m.get("content", [])
            if isinstance(content, str):
                content = [{"type": "text", "text": content}]
            top = content + top
        data["system"] = top
        data["messages"] = messages
    # Thinking: keep Claude Code's adaptive thinking block (qwen reasons with
    # it) unless disabled via ASTRAL_SHIM_THINKING=off or the per-request
    # X-Astral-Thinking: off header. Only drop fields the Anthropic layer
    # genuinely rejects.
    if s_thinking:
        for key in ("thinking", "context_management", "output_config"):
            data.pop(key, None)
    else:
        data.pop("context_management", None)
        data.pop("output_config", None)
    return data


class Handler(http.server.BaseHTTPRequestHandler):
    def _forward(self, body):
        req = urllib.request.Request(UPSTREAM + self.path, data=body, method="POST")
        for k, v in self.headers.items():
            if k.lower() not in ("host", "content-length"):
                req.add_header(k, v)
        try:
            resp = urllib.request.urlopen(req)
            out = resp.read()
            self.send_response(resp.status)
            for k, v in resp.headers.items():
                self.send_header(k, v)
            self.end_headers()
            self.wfile.write(out)
        except urllib.error.HTTPError as e:
            out = e.read()
            self.send_response(e.code)
            self.end_headers()
            self.wfile.write(out)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            data = json.loads(body)
            # Log tool definitions the model sees (first request only).
            if "tools" in data:
                with open("/tmp/opencode/shim-tools.json", "w") as f:
                    json.dump(data["tools"], f, indent=2)
            s_thinking = strip_thinking(self.headers)
            fixed = fix_body(data, s_thinking)
            if fixed != data:
                print("=== FIXED REQUEST ===", flush=True)
                print(json.dumps(fixed, indent=2)[:2000], flush=True)
            body = json.dumps(fixed).encode()
        except Exception as e:
            print("parse err:", e, flush=True)
        self._forward(body)

    def do_GET(self):
        req = urllib.request.Request(UPSTREAM + self.path, method="GET")
        for k, v in self.headers.items():
            if k.lower() not in ("host", "content-length"):
                req.add_header(k, v)
        try:
            resp = urllib.request.urlopen(req)
            out = resp.read()
            self.send_response(resp.status)
            for k, v in resp.headers.items():
                self.send_header(k, v)
            self.end_headers()
            self.wfile.write(out)
        except urllib.error.HTTPError as e:
            out = e.read()
            self.send_response(e.code)
            self.end_headers()
            self.wfile.write(out)

    def log_message(self, *a):
        pass

    # Suppress the stack-trace noise on client disconnects (common when the
    # model is slow, e.g. 27b at ~2.5 t/s, and the client times out).
    def handle_error(self, request, client_address):
        import traceback
        exc = traceback.format_exc()
        if "ConnectionResetError" in exc or "BrokenPipeError" in exc:
            return
        print(exc, file=sys.stderr)


def make_server():
    from http.server import ThreadingHTTPServer
    return ThreadingHTTPServer(("127.0.0.1", PORT), Handler)


if __name__ == "__main__":
    make_server().serve_forever()
