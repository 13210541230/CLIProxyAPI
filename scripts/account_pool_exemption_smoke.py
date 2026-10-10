"""Isolated Manager -> CPA -> synthetic OAuth upstream exemption smoke test.

Requires fresh build/cli-proxy-api-e2e.exe and build/enterprise-access-audit.dll,
Python bcrypt, and the sibling Manager binary. No production credentials are read.
Use --keep-alive for browser validation, then POST /finish on the stub port.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import threading
import time
import sys
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

import bcrypt

ROOT = Path(__file__).resolve().parents[1]
SCRATCH = ROOT / "build_tmp" / "account-pool-exemption"
LOGS = ROOT / "logs"
CPA_PORT, MANAGER_PORT, STUB_PORT = 18399, 18318, 18400
CPA = f"http://127.0.0.1:{CPA_PORT}"
MANAGER = f"http://127.0.0.1:{MANAGER_PORT}"
STUB = f"http://127.0.0.1:{STUB_PORT}"
API = "/v0/management/enterprise-access-audit/account-pool"
# Synthetic, local-only credentials; never valid outside this fixture.
KEY = "account-pool-exemption-smoke-management"
NORMAL = "account-pool-exemption-smoke-normal"
EXEMPT = "account-pool-exemption-smoke-exempt"
GATE = threading.Event()
FINISH = threading.Event()
LOCK = threading.Lock()
CALLS: list[dict] = []
WAITING = {"a": 0, "b": 0}


def request(base, path, method="GET", body=None, key=KEY, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    hdr = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
    hdr.update(headers or {})
    req = Request(base + path, data=data, method=method, headers=hdr)
    try:
        with urlopen(req, timeout=120) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else {}
    except HTTPError as error:
        raw = error.read()
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = {"error": raw.decode(errors="replace")}
        return error.code, payload


def checked(base, path, method="GET", body=None, **kwargs):
    status, payload = request(base, path, method, body, **kwargs)
    assert 200 <= status < 300, (path, status, payload)
    return payload


def wait_until(predicate, timeout=30, label="condition"):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = predicate()
            if value:
                return value
        except (URLError, ConnectionError, TimeoutError):
            pass
        time.sleep(0.1)
    raise AssertionError("Timed out: " + label)


def key_hash(key):
    return hashlib.sha256(key.encode()).hexdigest()[:8]


class Stub(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path != "/state":
            self.send_error(404)
            return
        with LOCK:
            encoded = json.dumps({"calls": list(CALLS), "waiting": dict(WAITING)}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_POST(self):
        data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path in ("/block", "/release"):
            GATE.clear() if self.path == "/block" else GATE.set()
            self.send_response(200)
            self.send_header("Content-Length", "2")
            self.end_headers()
            self.wfile.write(b"{}")
            return
        if self.path == "/finish":
            GATE.set()
            FINISH.set()
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"{}")
            return
        payload = json.loads(data or b"{}")
        text = json.dumps(payload.get("input", ""))
        account = "a" if self.headers.get("Authorization") == "Bearer synthetic-a" else "b"
        entry = {"account": account, "hold": "hold-" in text, "fail": "fail-" in text, "session": self.headers.get("Session_id", self.headers.get("X-Session-Id", "")),
                 "reservationHeaderPresent": bool(self.headers.get("X-CPA-Account-Pool-Request-Id"))}
        with LOCK:
            CALLS.append(entry)
        if entry["hold"]:
            with LOCK:
                WAITING[account] += 1
            try:
                GATE.wait(120)
            finally:
                with LOCK:
                    WAITING[account] -= 1
        if entry["fail"]:
            encoded = json.dumps({"error": {"message": "synthetic failure", "type": "server_error", "code": "synthetic_failure"}}).encode()
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
        else:
            response = {"id": "resp-smoke", "object": "response", "status": "completed", "model": payload.get("model"),
                        "output": [{"id": "msg-smoke", "type": "message", "role": "assistant", "status": "completed",
                                    "content": [{"type": "output_text", "text": "handled-by-" + account, "annotations": []}]}],
                        "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
            encoded = ("data: " + json.dumps({"type": "response.completed", "response": response}) + "\n\n").encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        try:
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            pass


def prepare():
    for port in (CPA_PORT, MANAGER_PORT, STUB_PORT):
        with socket.socket() as sock:
            assert sock.connect_ex(("127.0.0.1", port)) != 0, f"Port {port} occupied; refusing to affect another process"
    assert not SCRATCH.exists(), f"Scratch already exists: {SCRATCH}; inspect/clean owned fixture before retry"
    (SCRATCH / "auths").mkdir(parents=True)
    (SCRATCH / "plugins" / "windows" / "amd64").mkdir(parents=True)
    (SCRATCH / "static").mkdir()
    LOGS.mkdir(exist_ok=True)
    shutil.copy2(ROOT / "build" / "cli-proxy-api-e2e.exe", SCRATCH / "cli-proxy-api.exe")
    shutil.copy2(ROOT / "build" / "enterprise-access-audit.dll", SCRATCH / "plugins" / "windows" / "amd64" / "enterprise-access-audit.dll")
    shutil.copy2(ROOT.parent / "Cli-Proxy-API-Management-Center" / "bin" / "cpa-manager.exe", SCRATCH / "cpa-manager.exe")
    digest = bcrypt.hashpw(KEY.encode(), bcrypt.gensalt(prefix=b"2a")).decode()
    (SCRATCH / "config.yaml").write_text(f'''host: "127.0.0.1"
port: {CPA_PORT}
auth-dir: "auths"
api-keys: ["{NORMAL}", "{EXEMPT}"]
remote-management:
  allow-remote: false
  secret-key: "{digest}"
  disable-control-panel: true
logging-to-file: false
request-log: false
request-retry: 0
disable-cooling: true
proxy-url: "http://127.0.0.1:{STUB_PORT}"
usage-statistics-enabled: false
plugins:
  enabled: true
  dir: "plugins"
  configs:
    enterprise-access-audit:
      enabled: true
      data_dir: "plugin-data"
      default_audit_enabled: false
      audit_enabled: false
      exclusive-scheduler-providers: [codex]
      account_pool:
        enabled: true
        data_dir: "pool-data"
        max_wait_seconds: 1
        reserve_seconds: 10
''', encoding="utf-8")
    for account in ("a", "b"):
        (SCRATCH / "auths" / f"smoke-{account}.json").write_text(json.dumps({
            "type": "codex", "access_token": "synthetic-" + account,
            "account_id": "synthetic-account-" + account, "email": account + "@smoke.invalid",
            "base_url": f"http://127.0.0.1:{STUB_PORT}",
        }), encoding="utf-8")
    (SCRATCH / "management.key").write_text(KEY, encoding="utf-8")
    (SCRATCH / "config.json").write_text(json.dumps({"httpAddr": f"127.0.0.1:{MANAGER_PORT}", "dataDir": "manager-data",
        "cpaUpstreamUrl": CPA, "managementKeyFile": "management.key"}), encoding="utf-8")


def state(auth):
    return checked(MANAGER, API + "/state?authId=" + auth)["state"]


def run(keep_alive=False):
    prepare()
    upstream = ThreadingHTTPServer(("127.0.0.1", STUB_PORT), Stub)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    report = {"checks": [], "ports": {"cpa": CPA_PORT, "manager": MANAGER_PORT, "stub": STUB_PORT}}
    manager = None
    pool = concurrent.futures.ThreadPoolExecutor(max_workers=8)
    log_handle = (LOGS / "account-pool-exemption-manager.log").open("w", encoding="utf-8")
    env = os.environ.copy()
    for name in list(env):
        if name.startswith(("CPA_", "USAGE_")) or name in ("HTTP_ADDR", "PANEL_PATH", "MANAGEMENT_STATIC_PATH", "WRITABLE_PATH"):
            env.pop(name, None)
    env["CPA_MANAGER_CONFIG"] = str(SCRATCH / "config.json")
    env["MANAGEMENT_STATIC_PATH"] = str(SCRATCH / "static")
    env["WRITABLE_PATH"] = str(SCRATCH)
    try:
        manager = subprocess.Popen([str(SCRATCH / "cpa-manager.exe")], cwd=SCRATCH, env=env, stdout=log_handle, stderr=subprocess.STDOUT)
        wait_until(lambda: request(MANAGER, "/health")[0] == 200, label="Manager health")
        runtime = wait_until(lambda: (lambda r: r if r.get("managed") and r.get("healthy") and not r.get("external") else None)(checked(MANAGER, "/runtime")), label="managed CPA health")
        report["runtime"] = {key: runtime.get(key) for key in ("managed", "healthy", "external", "pid")}
        print("MANAGER_READY", json.dumps(report["runtime"]), flush=True)
        files = checked(MANAGER, "/v0/management/auth-files")["files"]
        ids = {item.get("email"): item["id"] for item in files}
        a, b = ids["a@smoke.invalid"], ids["b@smoke.invalid"]
        policy = {"version": 1, "provider": "codex", "pools": [{"id": "home", "name": "Home", "enabled": True}, {"id": "other", "name": "Other", "enabled": True}],
            "members": [{"poolId": "home", "authId": a, "enabled": True}, {"poolId": "other", "authId": b, "enabled": True}],
            "bindings": [{"apiKeyHash": key_hash(NORMAL), "poolId": "home"}, {"apiKeyHash": key_hash(EXEMPT), "poolId": "home", "crossPoolExempt": True}]}
        checked(MANAGER, API + "/policy", "PUT", {"policy": policy})
        checked(MANAGER, API + "/concurrency-limits", "PUT", {"items": [{"authId": a, "limit": 5}, {"authId": b, "limit": 5}]})
        models = checked(MANAGER, "/v0/management/auth-files/models?name=" + files[0]["name"])
        entries = models.get("models", [])
        model = next((entry["id"] for entry in entries if "codex" in entry.get("id", "")), None)
        if model is None:
            model = next(entry["id"] for entry in entries)
        report["model"] = model
        report["authIds"] = {"a": a, "b": b}

        def chat(key, session, text="probe"):
            return request(CPA, "/v1/responses", "POST", {"model": model, "input": [{"role": "user", "content": text}], "stream": False}, key=key, headers={"X-Session-Id": session})

        status, payload = chat(EXEMPT, "primary-first")
        assert status == 200 and "handled-by-a" in json.dumps(payload), (status, payload)
        report["checks"].append("exempt primary-first")
        wait_until(lambda: state(a)["active"] == 0, label="primary completion")
        assert state(a)["reserved"] == 0, state(a)
        report["checks"].append("admitted pick has no residual reservation")

        wait_until(lambda: state(b)["borrowReady"], timeout=75, label="60-second telemetry warmup")
        report["checks"].append("sustained foreign capacity observed")
        holders = [pool.submit(chat, NORMAL, f"hold-{i}", f"hold-{i}") for i in range(5)]
        wait_until(lambda: state(a)["active"] == 5, label="five held primary requests")
        status, payload = chat(NORMAL, "normal-busy")
        assert status == 503 and "account_busy" in json.dumps(payload), (status, payload)
        report["checks"].append("ordinary caller remains isolated and returns account_busy")
        status, payload = chat(EXEMPT, "borrowed-session")
        assert status == 200 and "handled-by-b" in json.dumps(payload), (status, payload)
        report["checks"].append("exempt new session borrows other department account")
        wait_until(lambda: state(b)["active"] == 0, label="borrowed completion")
        assert state(b)["reserved"] == 0, state(b)
        GATE.set()
        for future in holders:
            status, payload = future.result(timeout=15)
            assert status == 200, (status, payload)
        wait_until(lambda: state(a)["active"] == 0, label="held requests drain")
        status, payload = chat(EXEMPT, "borrowed-session")
        assert status == 200 and "handled-by-b" in json.dumps(payload), (status, payload)
        report["checks"].append("borrowed session stays foreign after primary recovers")
        current = checked(MANAGER, API)["policy"]
        current["version"] += 1
        current.pop("hash", None)
        for binding in current["bindings"]:
            if binding["apiKeyHash"] == key_hash(EXEMPT):
                binding.pop("crossPoolExempt", None)
        checked(MANAGER, API + "/policy", "PUT", {"policy": current})
        status, payload = chat(EXEMPT, "borrowed-session")
        assert status == 200 and "handled-by-b" in json.dumps(payload), (status, payload)
        status, payload = chat(EXEMPT, "after-revocation")
        assert status == 200 and "handled-by-a" in json.dumps(payload), (status, payload)
        report["checks"].append("revocation preserves old binding and isolates new session")
        with LOCK:
            before = len(CALLS)
        status, payload = chat(EXEMPT, "borrowed-session", "fail-synthetic")
        assert status >= 400, (status, payload)
        with LOCK:
            failure_accounts = [call["account"] for call in CALLS[before:]]
        assert failure_accounts and set(failure_accounts) == {"b"}, failure_accounts
        status, payload = chat(EXEMPT, "borrowed-session")
        assert status == 200 and "handled-by-b" in json.dumps(payload), (status, payload)
        report["checks"].append("upstream failure never migrates active session")
        report["state"] = {"a": state(a), "b": state(b)}
        with LOCK:
            report["upstreamCalls"] = list(CALLS)
        assert all(not call["reservationHeaderPresent"] for call in report["upstreamCalls"]), "internal selection identity leaked upstream"
        report["checks"].append("host selection identity is not forwarded upstream")
        (LOGS / "account-pool-exemption-smoke.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
        print("SMOKE_PASS", json.dumps(report["checks"]), flush=True)
        if keep_alive:
            print("BROWSER_READY " + MANAGER + "/management.html", flush=True)
            if not FINISH.wait(1200):
                raise AssertionError("Browser validation did not finish within fixture lifetime")
    finally:
        GATE.set()
        pool.shutdown(wait=True, cancel_futures=True)
        if manager:
            try:
                request(MANAGER, "/runtime/stop", "POST", {})
            except Exception:
                pass
            manager.terminate()
            try:
                manager.wait(timeout=10)
            except subprocess.TimeoutExpired:
                manager.kill()
                manager.wait()
        upstream.shutdown()
        upstream.server_close()
        log_handle.close()
        # Only this owned, synthetic profile is removed, never another workspace.
        shutil.rmtree(SCRATCH)
        if SCRATCH.parent.exists() and not any(SCRATCH.parent.iterdir()):
            SCRATCH.parent.rmdir()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--keep-alive", action="store_true")
    try:
        run(parser.parse_args().keep_alive)
    except Exception:
        traceback.print_exc(file=sys.stdout)
        sys.exit(1)
