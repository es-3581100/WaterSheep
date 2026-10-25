#!/usr/bin/env python3
"""Dev launcher and end-to-end smoke for the WaterSheep Go/Rust boundary.

Normal launch:
  python tools/pybundle/watersheep_bundle.py --launch

Dev smoke (uses an internal fake WaterSheep server; no model download):
  python tools/pybundle/watersheep_bundle.py --smoke --dev

The dev toggle never grants execution authority. It only enables explicitly approved,
read-only access to raw WaterSheep output/logs through the Go supervisor.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

APPROVAL_HEADER = "X-WaterSheep-Debug-Approval"
APPROVAL_PHRASE = "I-APPROVE-READONLY-DIRECT-OUTPUT"


def repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def run(cmd: list[str], *, cwd: Path | None = None, capture: bool = False) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        cwd=str(cwd) if cwd else None,
        check=True,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def build_go(root: Path) -> Path:
    out = root / ".dev" / "bin" / "watersheep-supervisor"
    out.parent.mkdir(parents=True, exist_ok=True)
    run(["go", "test", "./..."], cwd=root / "go-bridge")
    run(["go", "build", "-o", str(out), "./cmd/watersheep-supervisor"], cwd=root / "go-bridge")
    return out


def build_rust(root: Path) -> None:
    run(["cargo", "test", "--manifest-path", str(root / "rust-harness" / "Cargo.toml")])


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def http_json(method: str, url: str, body: dict | None = None, headers: dict[str, str] | None = None):
    data = None if body is None else json.dumps(body).encode("utf-8")
    req_headers = {"Accept": "application/json"}
    if body is not None:
        req_headers["Content-Type"] = "application/json"
    if headers:
        req_headers.update(headers)
    req = Request(url, data=data, headers=req_headers, method=method)
    with urlopen(req, timeout=5) as resp:
        return resp.status, json.loads(resp.read().decode("utf-8"))


def wait_status(base: str, timeout: float = 10.0) -> dict:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            _, status = http_json("GET", f"{base}/v1/status")
            last = status
            if status.get("state") == "ready":
                return status
            if status.get("state") == "failed":
                raise RuntimeError(f"supervisor failed: {status}")
        except Exception as exc:  # startup is expected to race the first few polls
            last = exc
        time.sleep(0.1)
    raise RuntimeError(f"supervisor did not become ready: {last}")


def fixture_request() -> dict:
    return {
        "state": {"node": "build.health", "tests": "pass", "compile": "pass"},
        "questions": {
            "routing": {
                "type": "choice",
                "instructions": "What should happen next?",
                "criteria": {
                    "continue": "continue through normal runtime",
                    "inspect": "request deeper inspection",
                    "stop": "do not continue",
                },
            },
            "escalate": {
                "type": "noul",
                "instructions": "Should the expensive planner inspect this now?",
            },
        },
    }


def fake_answer(request: dict) -> dict:
    answers = {}
    for name, spec in (request.get("questions") or {}).items():
        kind = spec.get("type")
        criteria = spec.get("criteria") or {}
        labels = list(criteria.keys()) if isinstance(criteria, dict) else list(criteria)
        if kind == "choice":
            labels = labels or ["continue", "inspect"]
            selected = labels[0]
            remaining = max(0, len(labels) - 1)
            tail = (0.09 / remaining) if remaining else 0.0
            probs = {label: (0.91 if label == selected else tail) for label in labels}
            answers[name] = {
                "type": "choice",
                "choice": selected,
                "confidence": 0.91,
                "probabilities": probs,
            }
        elif kind == "noul":
            answers[name] = {"type": "noul", "noul": 0.17}
        elif kind == "multi":
            labels = labels or ["a", "b"]
            answers[name] = {
                "type": "multi",
                "choices": [labels[0]],
                "confidence": 0.88,
                "probabilities": {label: (0.88 if i == 0 else 0.12) for i, label in enumerate(labels)},
            }
        elif kind == "score":
            labels = labels or ["low", "high"]
            probs = {str(i): (0.2 if i == 0 else 0.8 / max(1, len(labels) - 1)) for i in range(len(labels))}
            answers[name] = {
                "type": "score",
                "score": 1.0,
                "confidence": 0.8,
                "legend": {str(i): label for i, label in enumerate(labels)},
                "probabilities": probs,
            }
        else:
            answers[name] = {"type": str(kind or "?"), "error": "unsupported fake question type"}
    return {
        "model": "watersheep-smoke-fixture",
        "answers": answers,
        "usage": {"input_tokens": 37, "output_tokens": 0},
    }


class FakeWaterSheep(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def do_POST(self):
        if self.path.rstrip("/") not in ("/v1/systemone", "/v1/decisions"):
            self.send_error(404)
            return
        size = int(self.headers.get("Content-Length") or 0)
        request = json.loads(self.rfile.read(size) or b"{}")
        payload = json.dumps(fake_answer(request)).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def fake_server(port: int) -> int:
    print(f"fake-watersheep ready on 127.0.0.1:{port}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), FakeWaterSheep).serve_forever()
    return 0


def smoke(root: Path, dev: bool, skip_rust: bool) -> int:
    go_bin = build_go(root)
    if not skip_rust:
        build_rust(root)

    upstream_port = free_port()
    supervisor_port = free_port()
    base = f"http://127.0.0.1:{supervisor_port}"
    upstream = f"http://127.0.0.1:{upstream_port}"
    child = [sys.executable, str(Path(__file__).resolve()), "--_fake-server", str(upstream_port)]

    cmd = [
        str(go_bin), "serve",
        "--listen", f"127.0.0.1:{supervisor_port}",
        "--upstream", upstream,
        "--startup-timeout", "10s",
        "--child-command-json", json.dumps(child),
    ]
    if dev:
        cmd.append("--dev")

    smoke_env = os.environ.copy()
    smoke_env["WATERSHEEP_SMOKE"] = "1"
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=smoke_env)
    try:
        status = wait_status(base)
        assert status["owner"] == "go-bridge"
        assert status["authority"] == "observation-only"
        assert status["go_module"] == "github.com/SamratDuttaOfficial/WaterSheep/go-bridge"

        req_obj = fixture_request()
        _, proxied = http_json("POST", f"{base}/v1/systemone", req_obj)
        assert proxied["model"] == "watersheep-smoke-fixture"

        denied = False
        try:
            http_json("POST", f"{base}/v1/debug/direct", req_obj)
        except HTTPError as exc:
            denied = exc.code == 403
        assert denied, "direct output unexpectedly worked without explicit approval"

        approved = None
        if dev:
            _, approved = http_json(
                "POST",
                f"{base}/v1/debug/direct",
                req_obj,
                {APPROVAL_HEADER: APPROVAL_PHRASE},
            )
            assert approved["model"] == "watersheep-smoke-fixture"

        receipt = None
        if not skip_rust:
            with tempfile.TemporaryDirectory() as td:
                request_path = Path(td) / "request.json"
                request_path.write_text(json.dumps(req_obj), encoding="utf-8")
                cp = run(
                    [
                        "cargo", "run", "--quiet",
                        "--manifest-path", str(root / "rust-harness" / "Cargo.toml"),
                        "--", "ask",
                        "--base-url", base,
                        "--request", str(request_path),
                    ],
                    capture=True,
                )
                receipt = json.loads(cp.stdout)
                assert receipt["authority"] == "evidence-only"
                assert receipt["model"] == "watersheep-smoke-fixture"
                assert {d["name"] for d in receipt["decisions"]} == set(req_obj["questions"])

        summary = {
            "schema": "watersheep.pybundle.smoke/v1",
            "status": "PASS",
            "dev_toggle": dev,
            "go_status": status,
            "normal_proxy_model": proxied["model"],
            "debug_denied_without_approval": denied,
            "debug_direct_model": approved and approved["model"],
            "rust_receipt": receipt,
        }
        print(json.dumps(summary, indent=2, sort_keys=True))
        return 0
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=2)
        if proc.returncode not in (0, -15):
            stderr = proc.stderr.read() if proc.stderr else ""
            if stderr:
                print(stderr, file=sys.stderr)


def launch(root: Path, dev: bool, model: str, skip_rust: bool) -> int:
    go_bin = build_go(root)
    if not skip_rust:
        build_rust(root)
    cmd = [str(go_bin), "serve", "--model", model]
    if dev:
        cmd.append("--dev")
        print(
            "DEV MODE: direct WaterSheep output is still locked; each debug call must include "
            "--approve-readonly-direct-output.",
            file=sys.stderr,
        )
    os.execv(cmd[0], cmd)
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--smoke", action="store_true", help="run fake-model end-to-end smoke")
    mode.add_argument("--launch", action="store_true", help="build and launch the Go-owned WaterSheep supervisor")
    ap.add_argument("--dev", action="store_true", help="enable dev-only debug surface; per-call approval is still required")
    ap.add_argument("--model", default=os.environ.get("WATERSHEEP_MODEL", "samratduttaofficial/WaterSheep"))
    ap.add_argument("--skip-rust", action="store_true", help=argparse.SUPPRESS)
    ap.add_argument("--_fake-server", type=int, help=argparse.SUPPRESS)
    args = ap.parse_args(argv)

    if args._fake_server:
        return fake_server(args._fake_server)
    root = repo_root()
    if args.launch:
        return launch(root, args.dev, args.model, args.skip_rust)
    return smoke(root, args.dev, args.skip_rust)


if __name__ == "__main__":
    raise SystemExit(main())
