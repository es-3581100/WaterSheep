#!/usr/bin/env python3
"""Private DonCombo GET/POST compatibility shim -> Go WaterSheep supervisor.

Host-only, shadow-only, no KoL credentials and no game actions. Supports both
GET /v1/kolmafia?request=<urlencoded-json> (observed local runtime) and
POST /v1/kolmafia with form request=... (published DonCombo adapter).
"""
from __future__ import annotations

import argparse
import ipaddress
import json
import signal
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import Request, urlopen

MAX_REQUEST = 262_144
MAX_REPLY = 262_144


def private_ip(raw: str) -> str:
    addr = ipaddress.IPv4Address(raw)
    if raw != str(addr):
        raise ValueError("listen host must be a canonical literal IPv4")
    allowed = addr == ipaddress.IPv4Address("127.0.0.1") or any(addr in ipaddress.IPv4Network(block) for block in
          ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))
    if not allowed:
        raise ValueError("listen host must be 127.0.0.1 or explicit RFC1918 IPv4")
    return raw


def validate_payload(obj: object) -> dict:
    if not isinstance(obj, dict):
        raise ValueError("request must be an object")
    state = obj.get("state")
    if not isinstance(state, dict) or state.get("mode") != "shadow-only; no action authorized":
        raise ValueError("shadow-only state required")
    questions = obj.get("questions")
    if not isinstance(questions, dict) or set(questions) != {"tile_class"}:
        raise ValueError("exactly one tile_class question required")
    item = questions["tile_class"]
    if not isinstance(item, dict) or item.get("type") != "choice":
        raise ValueError("choice question required")
    criteria = item.get("criteria")
    if not isinstance(criteria, dict) or set(criteria) != set("ABCDEF"):
        raise ValueError("A-F criteria required")
    return obj


def request_from_query(query: str) -> dict:
    if len(query.encode("utf8")) > MAX_REQUEST:
        raise ValueError("request URL too large")
    form = parse_qs(query, keep_blank_values=True, strict_parsing=True)
    if set(form) != {"request"} or len(form["request"]) != 1:
        raise ValueError("exactly one request field required")
    raw = form["request"][0]
    if len(raw.encode("utf8")) > MAX_REQUEST:
        raise ValueError("decoded request too large")
    return validate_payload(json.loads(raw))


def handler(upstream: int):
    class H(BaseHTTPRequestHandler):
        server_version = "DonComboWaterSheepDualTransport/1.0"
        def log_message(self, *_args):
            # Avoid printing query-string evidence even on rejected requests.
            return
        def write_json(self, status: int, obj: dict):
            b = json.dumps(obj, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(b)))
            self.end_headers()
            try:
                self.wfile.write(b)
            except (BrokenPipeError, ConnectionResetError):
                pass
        def do_GET(self):
            if self.path == "/health":
                try:
                    with socket.create_connection(("127.0.0.1", upstream), timeout=0.5):
                        state = "listening"
                except OSError:
                    state = "unreachable"
                return self.write_json(200, {"adapter": "ready", "watersheep_tcp": state,
                    "game_actions": False, "request_modes": ["GET-query", "POST-form"]})
            u = urlsplit(self.path)
            if u.path != "/v1/kolmafia" or not u.query:
                return self.write_json(404, {"error": "not_found"})
            try:
                obj = request_from_query(u.query)
            except (ValueError, TypeError, json.JSONDecodeError):
                return self.write_json(400, {"error": "invalid_shadow_request"})
            return self.forward(obj)
        def do_POST(self):
            u = urlsplit(self.path)
            if u.path != "/v1/kolmafia" or u.query:
                return self.write_json(404, {"error": "not_found"})
            if self.headers.get("Content-Type", "").split(";", 1)[0].strip().lower() != "application/x-www-form-urlencoded":
                return self.write_json(415, {"error": "expected_form_body"})
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if length < 1 or length > MAX_REQUEST:
                    return self.write_json(413, {"error": "invalid_body_length"})
                obj = request_from_query(self.rfile.read(length).decode("utf8"))
            except (ValueError, TypeError, UnicodeDecodeError, json.JSONDecodeError):
                return self.write_json(400, {"error": "invalid_shadow_request"})
            return self.forward(obj)
        def forward(self, obj: dict):
            body = json.dumps(obj, separators=(",", ":")).encode("utf8")
            if len(body) > MAX_REQUEST:
                return self.write_json(413, {"error": "request_too_large"})
            request = Request(f"http://127.0.0.1:{upstream}/v1/systemone", data=body,
                              headers={"Content-Type": "application/json"}, method="POST")
            try:
                with urlopen(request, timeout=180) as response:
                    raw = response.read(MAX_REPLY + 1)
                if len(raw) > MAX_REPLY:
                    return self.write_json(502, {"error": "upstream_reply_too_large"})
                result = json.loads(raw)
                if not isinstance(result, dict):
                    return self.write_json(502, {"error": "invalid_upstream_reply"})
                return self.write_json(200, result)
            except HTTPError as exc:
                return self.write_json(502, {"error": f"upstream_http_{exc.code}"})
            except (URLError, OSError, ValueError, json.JSONDecodeError):
                return self.write_json(502, {"error": "upstream_unavailable"})
    return H


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--listen-host", action="append", default=[])
    parser.add_argument("--listen-port", type=int, default=8768)
    parser.add_argument("--watersheep-port", type=int, default=8777)
    a = parser.parse_args()
    if a.listen_port == a.watersheep_port or not 1024 <= a.listen_port <= 65535 or not 1024 <= a.watersheep_port <= 65535:
        parser.error("invalid listen/upstream ports")
    hosts = a.listen_host or ["127.0.0.1"]
    try:
        hosts = [private_ip(h) for h in hosts]
    except ValueError as exc:
        parser.error(str(exc))
    if len(set(hosts)) != len(hosts):
        parser.error("duplicate listen address")
    servers = []
    threads = []
    try:
        # Bind all endpoints first; avoid half-ready listener on gateway errors.
        for host in hosts:
            srv = ThreadingHTTPServer((host, a.listen_port), handler(a.watersheep_port))
            srv.daemon_threads = True
            servers.append(srv)
        for srv in servers[1:]:
            th = threading.Thread(target=srv.serve_forever, daemon=True)
            th.start()
            threads.append((srv, th))
        for host in hosts:
            print(f"DonCombo compatibility bridge: {host}:{a.listen_port} -> 127.0.0.1:{a.watersheep_port}", flush=True)
        def terminate(_signum, _frame):
            raise KeyboardInterrupt
        signal.signal(signal.SIGTERM, terminate)
        try:
            servers[0].serve_forever()
        except KeyboardInterrupt:
            pass
    finally:
        for srv, th in threads:
            srv.shutdown()
            th.join(timeout=3)
        for srv in servers:
            srv.server_close()

if __name__ == "__main__":
    main()
