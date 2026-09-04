#!/usr/bin/env python3
"""Minimal MCP stdio client that drives `cairn mcp` and calls the blast tool twice.

Portable: CAIRN_ROOT is this file's parents[1] (the cairn repo root).
Optional --cairn-bin skips `go run` and uses a prebuilt binary.
The second blast reuses the in-RAM dependency graph, so it should be faster.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time
from pathlib import Path

CAIRN_ROOT = Path(__file__).resolve().parents[1]


class MCPClient:
    def __init__(self, repo: str, cairn_bin: str | None = None):
        self.next_id = 0
        if cairn_bin:
            cmd = [cairn_bin, "mcp", "--dir", repo]
            cwd = None
        else:
            cmd = ["go", "run", "./cmd/cairn", "mcp", "--dir", repo]
            cwd = str(CAIRN_ROOT)
        self.proc = subprocess.Popen(
            cmd,
            cwd=cwd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            bufsize=1,
        )

    def _send(self, payload):
        self.proc.stdin.write(json.dumps(payload) + "\n")
        self.proc.stdin.flush()

    def notify(self, method, params=None):
        msg = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            msg["params"] = params
        self._send(msg)

    def request(self, method, params=None):
        self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": self.next_id, "method": method}
        if params is not None:
            msg["params"] = params
        self._send(msg)

        line = self.proc.stdout.readline()
        if not line:
            raise RuntimeError("server closed stdout while waiting for %s" % method)
        resp = json.loads(line)
        if "error" in resp:
            raise RuntimeError("%s failed: %s" % (method, resp["error"]))
        return resp.get("result", {})

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()


def result_text(result):
    parts = []
    for block in result.get("content", []):
        if block.get("type") == "text":
            parts.append(block.get("text", ""))
    return "\n".join(parts)


def main():
    parser = argparse.ArgumentParser(description="cairn MCP blast demo client")
    parser.add_argument("--repo", required=True, help="repo directory to index")
    parser.add_argument("--file", required=True, help="repo-relative file to blast")
    parser.add_argument(
        "--cairn-bin",
        default=None,
        help="optional path to a prebuilt cairn binary (skips go run)",
    )
    args = parser.parse_args()

    started = time.monotonic()
    client = MCPClient(args.repo, cairn_bin=args.cairn_bin)
    try:
        client.request(
            "initialize",
            {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "blast-demo", "version": "0.1.0"},
            },
        )
        client.notify("notifications/initialized")
        startup_ms = (time.monotonic() - started) * 1000
        print("startup: %.0f ms" % startup_ms)

        tools = client.request("tools/list").get("tools", [])
        print(
            "tools (%d): %s"
            % (len(tools), ", ".join(t.get("name", "?") for t in tools))
        )

        for label in ("blast1", "blast2"):
            t0 = time.monotonic()
            result = client.request(
                "tools/call", {"name": "blast", "arguments": {"file": args.file}}
            )
            elapsed_ms = (time.monotonic() - t0) * 1000
            text = result_text(result)
            print("%s: %.0f ms, ~%d tokens" % (label, elapsed_ms, len(text) // 4))
    finally:
        client.close()

    return 0


if __name__ == "__main__":
    sys.exit(main())
