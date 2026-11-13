# file: scripts/tests/test_record_r0_reach.py
# version: 1.5.0
# guid: 2f6c9d84-1b37-4a5e-8d02-7e3a5b9c1f60
# last-edited: 2026-11-13
"""Tests for scripts/r2corpus/record_r0_reach.py and its committed data (F4-07).

The recorder is driven against a fake R0: a stdlib HTTP server that answers from
a fixture router (each route needs one atom) and a fixture principal model, so
the recording code, the verdict rule, the sentinels and ``--check`` run with no
R0 binary. What the fake cannot show - that the real R0 answers the probes the
way the rule assumes - is proven by the recorder's own run on a GitHub runner.

Run: python3 -m pytest scripts/tests/test_record_r0_reach.py -q
"""

from __future__ import annotations

import http.server
import importlib.util
import io
import json
import pathlib
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock
from urllib.parse import urlparse

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "r2corpus" / "record_r0_reach.py"
REACH = REPO_ROOT / "internal" / "books" / "rbacreach"
ABK_RE = re.compile(r"abk_[A-Za-z0-9]{16,}")


def load():
    name = "record_r0_reach"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


# The fake listens on loopback: skip the platform proxy lookup (slow on macOS) per request.
urllib.request.getproxies = lambda: {}

rec = load()
w = rec.w

ROLE_ATOMS = {
    "admin": list(w.ALL_ATOMS),
    "editor": ["library.view", "library.edit_metadata", "playlists.create"],
    "viewer": ["library.view", "playlists.create"],
    "fp-worker": ["fingerprint.worker"],
    **{k: v["permissions"] for k, v in rec.CUSTOM_ROLES.items()},
}

FIXTURE_ROUTES = [
    {"method": "GET", "path": "/api/v1/health", "abs_only": False, "atom": None},
    {"method": "GET", "path": "/api/v1/audiobooks/:id", "abs_only": False, "atom": "library.view"},
    {
        "method": "PUT",
        "path": "/api/v1/audiobooks/:id",
        "abs_only": False,
        "atom": "library.edit_metadata",
    },
    {"method": "GET", "path": "/api/v1/users", "abs_only": False, "atom": "users.manage"},
    {
        "method": "POST",
        "path": "/api/v1/system/reset",
        "abs_only": False,
        "atom": "settings.manage",
    },
    {"method": "POST", "path": "/api/v1/auth/logout", "abs_only": False, "atom": "@authenticated"},
    {"method": "GET", "path": "/api/v1/files/*filepath", "abs_only": False, "atom": "library.view"},
    {"method": "POST", "path": "/auth/refresh", "abs_only": True, "atom": "@never"},
]


class FakeR0:
    """A fixture R0: every behaviour a test needs is a switch on the instance."""

    def __init__(self, auth_off: bool = False) -> None:
        self.auth_off = auth_off
        self.users = {
            u: {"id": f"id-{i}", "username": u, "roles": [label], "status": "active"}
            for i, (u, (_, label)) in enumerate(rec.USERS.items())
        }
        self.users["bystander"] = {
            "id": "id-bystander",
            "username": "bystander",
            "roles": ["viewer"],
            "status": "active",
        }
        self.sessions: dict[str, str] = {}
        self.keys: dict[str, tuple[str, list[str]]] = {}
        self.fail_status: dict[str, int] = {}  # "METHOD path-prefix" -> status
        # the same, but only for a request that passed authorization (a handler's own failure)
        self.handler_status: dict[str, int] = {}
        self.me_status: dict[
            str, int
        ] = {}  # username -> status of GET /auth/me (a confined worker)
        self.delete_user_on: str | None = None  # a route whose probe drops a user
        self.delete_book_on: str | None = None  # a route whose probe drops a book
        self.count_reply: dict = {"data": {"count": 3}}
        self.defs = [
            {"id": "scan.trigger", "permissions": ["scan.trigger"], "approver": "requests.approve"},
            {"id": "books.noop", "permissions": ["library.view"], "approver": ""},
        ]
        self.defs_start_status = 400
        self.awaiting = ["run-1"]
        self.dead = False
        self.peers: list[str] = []  # the client address of every request
        self.stream_path: str | None = None  # an event stream: headers, then heartbeats forever
        self.lock = threading.Lock()
        outer = self

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):  # noqa: D401
                pass

            def _do(self):
                outer.handle(self)

        for verb in ("GET", "POST", "PUT", "PATCH", "DELETE"):
            setattr(H, f"do_{verb}", H._do)
        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.port = self.httpd.server_address[1]
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    @property
    def base(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def stop(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()

    def atoms_of(self, user: str) -> list[str]:
        out: list[str] = []
        for r in self.users[user]["roles"]:
            out += ROLE_ATOMS[r]
        return out

    def principal(self, h) -> tuple[str | None, list[str] | None]:
        """(user, effective atoms) or (None, None) when unauthenticated."""
        auth = h.headers.get("Authorization", "")
        if auth.startswith("Bearer "):
            owner_scopes = self.keys.get(auth[7:])
            if owner_scopes is None:
                return None, None
            owner, scopes = owner_scopes
            held = self.atoms_of(owner)
            return owner, held if not scopes else [a for a in scopes if a in held]
        m = re.search(r"sid=([^;]+)", h.headers.get("Cookie", ""))
        if m and m.group(1) in self.sessions:
            user = self.sessions[m.group(1)]
            return user, self.atoms_of(user)
        return None, None

    def reply(self, h, status: int, body=None, cookie: str | None = None) -> None:
        raw = json.dumps(body if body is not None else {}).encode()
        h.send_response(status)
        h.send_header("Content-Type", "application/json")
        h.send_header("Content-Length", str(len(raw)))
        if cookie:
            h.send_header("Set-Cookie", cookie)
        h.end_headers()
        h.wfile.write(raw)

    def handle(self, h) -> None:
        self.peers.append(h.client_address[0])
        if self.stream_path and urlparse(h.path).path == self.stream_path:
            return self.stream(h)
        with self.lock:
            self._handle(h)

    def stream(self, h) -> None:
        """An SSE route: 401 without a principal, else 200 and a heartbeat that
        never stops (a client that reads the body to its end never returns)."""
        with self.lock:
            user, _ = self.principal(h)
        if user is None and not self.auth_off:
            return self.reply(h, 401)
        h.send_response(200)
        h.send_header("Content-Type", "text/event-stream")
        h.end_headers()
        for _ in range(400):  # 20 s at most, so a failing test still ends
            try:
                h.wfile.write(b": heartbeat\n\n")
                h.wfile.flush()
            except OSError:
                return
            time.sleep(0.05)

    def _handle(self, h) -> None:
        if self.dead:
            h.connection.close()
            return
        n = int(h.headers.get("Content-Length") or 0)
        body = json.loads(h.rfile.read(n)) if n else None
        path = urlparse(h.path).path
        method = h.command
        for prefix, status in self.fail_status.items():
            pm, pp = prefix.split(" ", 1)
            if pm == method and path.startswith(pp):
                return self.reply(h, status)
        if self.delete_user_on and path == self.delete_user_on:
            self.users.pop("bystander", None)
        if self.delete_book_on and path == self.delete_book_on:
            self.count_reply = {"data": {"count": self.count_reply["data"]["count"] - 1}}
        user, atoms = self.principal(h)
        if path == "/api/v1/auth/login":
            u = (body or {}).get("username")
            if u in self.users and rec.USERS[u][0] == (body or {}).get("password"):
                sid = f"s{len(self.sessions)}-{u}"
                self.sessions[sid] = u
                return self.reply(h, 200, {}, f"sid={sid}; Path=/")
            return self.reply(h, 401)
        if path == "/api/v1/auth/status":
            return self.reply(h, 200)
        if path == "/api/v1/auth/me":
            if user in self.me_status:
                return self.reply(h, self.me_status[user])
            return self.reply(h, 200 if (user or self.auth_off) else 401)
        if path == "/api/v1/auth/api-keys" and method == "POST":
            if user is None or "users.manage" not in (atoms or []):
                return self.reply(h, 403)
            owner = next(u for u in self.users.values() if u["id"] == body["user_id"])["username"]
            secret = "abk_" + (re.sub("[^A-Za-z0-9]", "", body["name"]) + "x" * 20)[:24]
            self.keys[secret] = (owner, body["scopes"])
            return self.reply(h, 201, {"data": {"id": f"k-{len(self.keys)}", "api_key": secret}})
        if path == "/api/v1/users" and method == "GET" and (user and "users.manage" in atoms):
            return self.reply(h, 200, {"data": {"users": list(self.users.values())}})
        if path == "/api/v1/audiobooks/count" and method == "GET":
            return self.reply(h, 200, self.count_reply)
        if path == "/api/v3/ops/defs" and method == "GET":
            return self.reply(h, 200 if user or self.auth_off else 401, {"items": self.defs})
        if path == "/api/v3/ops/runs" and method == "GET":
            items = [
                {"id": r, "state": "awaiting_decision", "def_id": "scan.trigger", "secret": "x"}
                for r in self.awaiting
            ]
            return self.reply(h, 200 if user or self.auth_off else 401, {"items": items})
        m = re.fullmatch(r"/api/v3/ops/defs/([^/]+)/runs", path)
        if m and method == "POST":
            d = next((x for x in self.defs if x["id"] == m.group(1)), None)
            if not (user or self.auth_off):
                return self.reply(h, 401)
            need = d["permissions"] if d else []
            if not self.auth_off and any(a not in (atoms or []) for a in need):
                return self.reply(h, 403)
            return self.reply(h, self.defs_start_status)
        m = re.fullmatch(r"/api/v3/ops/runs/([^/]+)/approve", path)
        if m and method == "POST":
            if not (user or self.auth_off):
                return self.reply(h, 401)
            if not self.auth_off and "requests.approve" not in (atoms or []):
                return self.reply(h, 403)
            return self.reply(h, 422)
        for r in FIXTURE_ROUTES:
            pat = re.sub(r":[A-Za-z_]+", "[^/]+", r["path"])
            pat = re.sub(r"\*[A-Za-z_]+", ".+", pat)
            if r["method"] != method or not re.fullmatch(pat, path):
                continue
            a = r["atom"]
            if a is None:
                return self.reply(h, 404)
            if a == "@never":
                return self.reply(h, 401)
            if self.auth_off:
                return self.reply(h, 400)
            if user is None:
                return self.reply(h, 401)
            if path == "/api/v1/auth/logout":
                for sid, u in list(self.sessions.items()):
                    if u == user and f"sid={sid}" in h.headers.get("Cookie", ""):
                        del self.sessions[sid]
                return self.reply(h, 200)
            if a != "@authenticated" and a not in (atoms or []):
                return self.reply(h, 403)
            for prefix, status in self.handler_status.items():
                pm, pp = prefix.split(" ", 1)
                if pm == method and path.startswith(pp):
                    return self.reply(h, status)
            return self.reply(h, 400 if method in ("POST", "PUT", "PATCH") else 404)
        return self.reply(h, 404)


SMALL_TABLE = [
    p
    for p in rec.principal_table()
    if p["name"]
    in {
        "session:admin",
        "session:editor",
        "session:viewer",
        "key:admin:settings",
        "key:admin:view",
        "key:admin:empty",
        "key:editor:every",
        "key:viewer:view",
        "anonymous",
    }
]


class Rig:
    """A fake R0 plus the recorder's own client and probes for the full table."""

    def __init__(self, fake: FakeR0 | None = None, principals=None) -> None:
        self.fake = fake or FakeR0()
        self.client = w.Client(self.fake.base, user_agent="test")
        self.principals = [p for p in (principals or SMALL_TABLE) if p["kind"] != "authoff"]
        self.probes = rec.prepare_credentials(self.client, self.principals)
        self.routes = [{k: v for k, v in r.items() if k != "atom"} for r in FIXTURE_ROUTES]

    def run(self, do_tasks: bool = True):
        return rec.record_pass(
            lambda p: self.probes[p["name"]],
            self.principals,
            self.routes,
            self.probes["session:admin"],
            4,
            do_tasks,
        )

    def close(self) -> None:
        self.fake.stop()


class PrincipalTableTest(unittest.TestCase):
    def setUp(self):
        self.rows = rec.principal_table()
        self.names = [r["name"] for r in self.rows]

    def test_names_are_unique(self):
        self.assertEqual(len(self.names), len(set(self.names)))

    def test_05_rbac_section_12_shapes_are_all_present(self):
        for label in ("admin", "editor", "viewer", "fp-worker", "custom-all", "custom-subset"):
            self.assertIn(f"session:{label}", self.names)
        for owner in ("admin", "editor", "viewer", "fp-worker"):
            for s in ("view", "settings", "users", "worker", "every", "empty"):
                self.assertIn(f"key:{owner}:{s}", self.names)
        self.assertIn("key:custom-subset:outside", self.names)  # atoms outside its owner's roles
        self.assertIn("key:custom-all:every", self.names)
        self.assertIn("anonymous", self.names)
        self.assertEqual(self.names[-1], "authoff")

    def test_keys_name_their_scopes_and_owner(self):
        by = {r["name"]: r for r in self.rows}
        self.assertEqual(by["key:editor:every"]["scopes"], list(w.ALL_ATOMS))
        self.assertEqual(by["key:viewer:empty"]["scopes"], [])
        self.assertEqual(by["key:fp-worker:worker"]["scopes"], ["fingerprint.worker"])
        outside = by["key:custom-subset:outside"]["scopes"]
        held = set(rec.CUSTOM_ROLES["custom-subset"]["permissions"])
        self.assertTrue(set(outside) - held)

    def test_custom_roles_match_the_overlay_writer(self):
        src = (REPO_ROOT / "scripts" / "r2corpus" / "overlay" / "identity.go").read_text()
        for spec in rec.CUSTOM_ROLES.values():
            self.assertIn(f'"{spec["name"]}"', src)
            for atom in spec["permissions"]:
                self.assertIn(f'"{atom}"', src)


class ProbeShapeTest(unittest.TestCase):
    def test_path_params_and_wildcards_get_dummy_values(self):
        self.assertEqual(
            rec.probe_path("/api/v1/a/:id/b/:jid"), "/api/v1/a/r2reach-missing/b/r2reach-missing"
        )
        self.assertEqual(rec.probe_path("/files/*filepath"), "/files/r2reach/missing")

    def test_mutating_verbs_get_an_empty_object(self):
        self.assertEqual(rec.probe_body("POST"), {})
        self.assertIsNone(rec.probe_body("GET"))
        self.assertIsNone(rec.probe_body("DELETE"))

    def test_the_invite_routes_get_a_token_of_each_principals_own(self):
        a = rec.probe_request("GET", "/api/v1/auth/invite/:token", "key:admin:view")
        b = rec.probe_request("GET", "/api/v1/auth/invite/:token", "session:viewer")
        self.assertNotEqual(a, b)
        self.assertTrue(a[0].startswith("/api/v1/auth/invite/r2reach-missing-"))
        self.assertIsNone(a[1])
        c = rec.probe_request("POST", "/api/v1/auth/accept-invite", "key:admin:view")
        d = rec.probe_request("POST", "/api/v1/auth/accept-invite", "session:viewer")
        self.assertNotEqual(c[1], d[1])
        self.assertEqual(set(c[1]), {"token"})
        # a token is a path segment: nothing in it needs escaping
        self.assertRegex(c[1]["token"], r"^[A-Za-z0-9-]+$")
        # every other route is the static probe
        self.assertEqual(
            rec.probe_request("PUT", "/api/v1/a/:id", "key:x"), ("/api/v1/a/r2reach-missing", {})
        )
        self.assertEqual(rec.probe_request("GET", "/api/v1/a", "key:x"), ("/api/v1/a", None))

    def test_the_invite_routes_are_routes_of_the_committed_dump(self):
        dump = json.loads(
            (REPO_ROOT / "internal/books/rbacreach/routes.json").read_text(encoding="utf-8")
        )
        known = {(r["method"], r["path"]) for r in dump["routes"]}
        self.assertIn(rec.INVITE_GET, known)
        self.assertIn(rec.INVITE_ACCEPT, known)

    def test_verdict_rule(self):
        self.assertEqual(rec.verdict_of(401, "k"), "D")
        self.assertEqual(rec.verdict_of(403, "k"), "D")
        self.assertEqual(rec.verdict_of(404, "k"), "A")
        self.assertEqual(rec.verdict_of(422, "k"), "A")
        for bad in (429, 500, 503, 0):
            with self.assertRaises(rec.RecorderError):
                rec.verdict_of(bad, "k")
        # a listed route's 5xx is an allow, whichever principal asked
        self.assertEqual(rec.verdict_of(500, "key:x POST /api/v1/ai/scans"), "A")
        self.assertEqual(rec.verdict_of(502, "session:y GET /api/v1/deluge/torrents"), "A")
        with self.assertRaises(rec.RecorderError):
            rec.verdict_of(500, "key:x POST /api/v1/ai/not-listed")


class CanaryTest(unittest.TestCase):
    def setUp(self):
        self.rig = Rig()

    def tearDown(self):
        self.rig.close()

    def test_a_worker_confined_credentials_403_is_its_baseline(self):
        self.rig.fake.me_status["barbara.gordon"] = 403
        out = self.rig.run()
        self.assertEqual(len(out["columns"]), len(self.rig.principals))

    def test_a_canary_that_answers_differently_from_its_baseline_fails(self):
        probe = self.rig.probes["session:viewer"]
        rec.canary(probe)
        self.assertEqual(probe.canary_status, 200)
        self.rig.fake.me_status["barbara.gordon"] = 403
        with self.assertRaises(rec.RecorderError) as cm:
            rec.canary(probe)
        self.assertIn("session:viewer: 403, want one of [200]", str(cm.exception))

    def test_a_first_canary_that_is_not_a_live_credential_fails(self):
        self.rig.fake.me_status["barbara.gordon"] = 401
        with self.assertRaises(rec.RecorderError):
            rec.canary(self.rig.probes["session:viewer"])


class StopThrowawayTest(unittest.TestCase):
    class Hung:
        docker = False

        def __init__(self, exc):
            self.exc, self.killed = exc, False
            self.proc = type("P", (), {})()
            self.proc.kill = lambda: setattr(self, "killed", True)
            self.proc.wait = lambda: 0

        def stop(self, timeout):
            raise self.exc

    def test_a_shutdown_that_hangs_is_killed_not_raised(self):
        for exc in (subprocess.TimeoutExpired("r0", 60), w.CorpusError("exit 2")):
            h = self.Hung(exc)
            rec.stop_throwaway(h)
            self.assertTrue(h.killed)
            self.assertIsNone(h.proc)


class RecordPassTest(unittest.TestCase):
    def setUp(self):
        self.rig = Rig()
        self.addCleanup(self.rig.close)

    def test_every_route_and_principal_gets_one_verdict(self):
        out = self.rig.run()
        self.assertEqual(len(out["columns"]), len(self.rig.principals))
        for col in out["columns"]:
            self.assertEqual(len(col), len(self.rig.routes))
            self.assertTrue(all(isinstance(s, int) and 0 < s < 500 for s in col))
        doc = rec.build_verdicts(
            {"r0_tag": "v0.0.0"}, self.rig.routes, self.rig.principals, out["columns"], {}
        )
        for r in doc["routes"]:
            self.assertEqual(len(r["verdict"]), len(self.rig.principals))
            self.assertTrue(set(r["verdict"]) <= {"A", "D"})

    def test_the_verdicts_follow_the_fixture_model(self):
        out = self.rig.run()
        doc = rec.build_verdicts(
            {"r0_tag": "t"}, self.rig.routes, self.rig.principals, out["columns"], {}
        )
        idx = {n: i for i, n in enumerate(doc["principals"])}
        row = {(r["method"], r["path"]): r["verdict"] for r in doc["routes"]}
        reset = row[("POST", "/api/v1/system/reset")]
        self.assertEqual(reset[idx["session:admin"]], "A")
        self.assertEqual(reset[idx["session:editor"]], "D")
        self.assertEqual(reset[idx["key:admin:settings"]], "A")
        self.assertEqual(reset[idx["key:admin:view"]], "D")
        self.assertEqual(reset[idx["key:admin:empty"]], "A")  # empty list = all the owner's atoms
        self.assertEqual(reset[idx["key:editor:every"]], "D")  # the intersection with the owner
        self.assertEqual(reset[idx["anonymous"]], "D")
        self.assertEqual(row[("GET", "/api/v1/health")][idx["anonymous"]], "A")
        edit = row[("PUT", "/api/v1/audiobooks/:id")]
        self.assertEqual(edit[idx["session:viewer"]], "D")
        self.assertEqual(edit[idx["key:editor:every"]], "A")

    def test_an_event_stream_does_not_hang_the_probe(self):
        # The first runner pass hung 37 minutes in GET /api/events: an authorized
        # probe of a stream whose heartbeat keeps the socket alive. The verdict is
        # the status line; the body is never read.
        self.rig.fake.stream_path = "/api/events"
        self.rig.routes.append({"method": "GET", "path": "/api/events", "abs_only": False})
        started = time.monotonic()
        out = self.rig.run(do_tasks=False)
        self.assertLess(time.monotonic() - started, 15, "the probe waited for the stream to end")
        names = [p["name"] for p in self.rig.principals]
        last = len(self.rig.routes) - 1
        self.assertEqual(out["columns"][names.index("session:admin")][last], 200)
        self.assertEqual(out["columns"][names.index("anonymous")][last], 401)

    def test_a_probe_can_come_from_its_own_source_address(self):
        # R0 rate-limits each peer address and the limit is a stored setting, not
        # an environment one: the recorder spreads its probes over 127/8 instead.
        self.rig.fake.peers.clear()
        status = rec.status_only(
            self.rig.client, "GET", "/api/v1/health", None, "none", 10.0, source="127.0.0.1"
        )
        self.assertEqual(status, 404)
        self.assertEqual(self.rig.fake.peers, ["127.0.0.1"])

    def test_source_addresses_are_distinct_loopback_addresses(self):
        src = rec.SourceIPs()
        seen = [src.next() for _ in range(70000)]
        self.assertEqual(len(set(seen)), len(seen))
        for ip in seen[:300] + seen[-300:]:
            parts = [int(x) for x in ip.split(".")]
            self.assertEqual(parts[0], 127)
            self.assertTrue(all(0 <= x <= 255 for x in parts))
            self.assertNotIn(ip, ("127.0.0.0", "127.255.255.255"))

    def test_logout_ends_the_session_and_the_recorder_logs_in_again(self):
        out = self.rig.run()
        names = [p["name"] for p in self.rig.principals]
        logout = next(
            i for i, r in enumerate(self.rig.routes) if r["path"] == "/api/v1/auth/logout"
        )
        # the logout probe was authorized for every session (a verdict, not a dead session),
        self.assertEqual(out["columns"][names.index("session:viewer")][logout], 200)
        # and the session works again afterwards (the recorder's canary ran after it).
        admin = self.rig.probes["session:admin"]
        self.assertEqual(admin.send("GET", "/api/v1/auth/me", None), 200)

    def test_a_route_other_than_logout_never_runs_before_it(self):
        order = rec.order_routes(self.rig.routes)
        self.assertEqual((order[-1]["method"], order[-1]["path"]), ("POST", "/api/v1/auth/logout"))

    def test_a_5xx_is_a_recorder_failure_that_names_the_route(self):
        self.assertNotIn("GET /api/v1/health", rec.OK_5XX)
        self.rig.fake.fail_status["GET /api/v1/health"] = 500
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("GET /api/v1/health", str(cm.exception))
        self.assertIn("500", str(cm.exception))

    def test_every_principals_failures_are_reported_together(self):
        self.rig.fake.fail_status["GET /api/v1/health"] = 500
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        for who in ("session:admin", "session:viewer", "key:admin:view"):
            self.assertIn(f"{who} GET /api/v1/health", str(cm.exception))

    def test_a_listed_5xx_after_authorization_is_an_allow(self):
        route = "PUT /api/v1/audiobooks/:id"
        self.assertIn(route, rec.OK_5XX)
        self.rig.fake.handler_status["PUT /api/v1/audiobooks"] = 500
        out = self.rig.run()
        names = [p["name"] for p in self.rig.principals]
        i = next(i for i, r in enumerate(self.rig.routes) if f"{r['method']} {r['path']}" == route)
        self.assertEqual(out["columns"][names.index("session:admin")][i], 500)
        self.assertEqual(out["columns"][names.index("anonymous")][i], 401)
        verdicts = rec.build_verdicts({}, self.rig.routes, self.rig.principals, out["columns"], {})
        row = verdicts["routes"][i]
        self.assertEqual(row["verdict"][names.index("session:admin")], "A")
        self.assertEqual(row["verdict"][names.index("anonymous")], "D")

    def test_a_listed_5xx_the_anonymous_caller_also_gets_is_not_a_verdict(self):
        # a 5xx before the decision (a crash in the middleware) is not an allow.
        self.rig.fake.fail_status["PUT /api/v1/audiobooks"] = 500
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("anonymous got 500", str(cm.exception))

    def test_every_listed_5xx_names_a_route_of_the_committed_dump(self):
        dump = json.loads(
            (REPO_ROOT / "internal/books/rbacreach/routes.json").read_text(encoding="utf-8")
        )
        known = {f"{r['method']} {r['path']}" for r in dump["routes"]}
        self.assertEqual(sorted(set(rec.OK_5XX) - known), [])

    def test_a_429_is_a_recorder_failure_not_a_deny(self):
        self.rig.fake.fail_status["GET /api/v1/health"] = 429
        with self.assertRaises(rec.RecorderError):
            self.rig.run()

    def test_a_dead_server_is_a_recorder_failure(self):
        self.rig.fake.dead = True
        with self.assertRaises(rec.RecorderError):
            self.rig.run()

    def test_store_drift_fails_the_run(self):
        self.rig.fake.delete_user_on = "/api/v1/system/reset"
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("drift", str(cm.exception))

    def test_a_dropped_book_fails_the_run(self):
        self.rig.fake.delete_book_on = "/api/v1/system/reset"
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("drift", str(cm.exception))
        self.assertIn("books", str(cm.exception))

    def test_a_census_that_cannot_read_the_book_count_fails_the_run(self):
        for reply in ({"count": 3}, {"data": {}}, {"data": {"count": "3"}}):
            with self.subTest(reply=reply):
                self.rig.fake.count_reply = reply
                with self.assertRaises(rec.RecorderError) as cm:
                    self.rig.run()
                self.assertIn("book count", str(cm.exception))

    def test_a_task_start_that_is_not_refused_after_authorization_fails(self):
        self.rig.fake.defs_start_status = 202  # a run started
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("may have started a run", str(cm.exception))

    def test_no_awaiting_run_fails(self):
        self.rig.fake.awaiting = []
        with self.assertRaises(rec.RecorderError):
            self.rig.run()

    def test_tasks_and_approvals_are_recorded_per_principal(self):
        out = self.rig.run()
        names = [p["name"] for p in self.rig.principals]
        tasks = {t["id"]: t for t in out["tasks"]}
        self.assertEqual(set(tasks), {"scan.trigger", "books.noop"})
        i = names.index("key:admin:empty")
        self.assertEqual(tasks["scan.trigger"]["verdict"][i], "A")
        self.assertEqual(tasks["scan.trigger"]["permissions"], ["scan.trigger"])
        ap = out["approvals"][0]
        self.assertEqual(ap["run_id"], "run-1")
        self.assertEqual(
            ap["run"], {"id": "run-1", "def_id": "scan.trigger", "state": "awaiting_decision"}
        )
        self.assertEqual(ap["verdict"][i], "A")
        self.assertEqual(ap["verdict"][names.index("session:viewer")], "D")


class AuthOffTest(unittest.TestCase):
    def test_the_authoff_principal_is_probed_without_a_credential(self):
        fake = FakeR0(auth_off=True)
        self.addCleanup(fake.stop)
        client = w.Client(fake.base, user_agent="test")
        p = [x for x in rec.principal_table() if x["kind"] == "authoff"]
        probe = rec.Probe(client, p[0], None)
        out = rec.record_pass(
            lambda _: probe,
            p,
            [{k: v for k, v in r.items() if k != "atom"} for r in FIXTURE_ROUTES],
            probe,
            1,
            False,
        )
        statuses = out["columns"][0]
        self.assertNotIn(401, statuses[:-1])  # no route but the never-allowed one answers 401

    def test_a_5xx_only_the_authoff_list_names_is_an_allow_in_the_authoff_pass(self):
        fake = FakeR0(auth_off=True)
        self.addCleanup(fake.stop)
        client = w.Client(fake.base, user_agent="test")
        p = [x for x in rec.principal_table() if x["kind"] == "authoff"]
        probe = rec.Probe(client, p[0], None)
        routes = [{k: v for k, v in r.items() if k != "atom"} for r in FIXTURE_ROUTES]
        fake.fail_status["GET /api/v1/health"] = 500

        def run():
            return rec.record_pass(lambda _: probe, p, routes, probe, 1, False)

        with self.assertRaises(rec.RecorderError):
            run()
        with mock.patch.dict(rec.OK_5XX_AUTHOFF, {"GET /api/v1/health": "a test"}):
            self.assertIn(500, run()["columns"][0])
        # the same entry does not loosen the auth-on pass
        listed = mock.patch.dict(rec.OK_5XX_AUTHOFF, {"GET /api/v1/health": "a test"})
        with listed, self.assertRaises(rec.RecorderError):
            rec.verdict_of(500, "key:x GET /api/v1/health")


class AuthFlipTest(unittest.TestCase):
    class Client:
        def __init__(self, status):
            self.status = status

        def request(self, method, path, auth=None, **kw):
            return type("R", (), {"status": self.status, "body": {}})()

    def test_a_boot_that_still_asks_for_a_credential_is_not_auth_off(self):
        with self.assertRaises(w.CorpusError) as cm:
            rec.check_auth_is_off(self.Client(401))
        self.assertIn("authentication is still on", str(cm.exception))
        rec.check_auth_is_off(self.Client(200))

    def test_the_edit_runs_the_helper_from_the_repo_root_on_the_copys_store(self):
        layout = mock.Mock()
        layout.db = pathlib.Path("/x/db/audiobooks.pebble")
        done = subprocess.CompletedProcess([], 0, stdout="enable_auth: true -> false\n", stderr="")
        with mock.patch.object(rec.subprocess, "run", return_value=done) as run:
            rec.edit_config(layout, [rec.AUTH_OFF_EDIT])
        argv = run.call_args.args[0]
        self.assertEqual(argv[:2], ["go", "run"])
        self.assertTrue(argv[2].endswith("edit_config.go"))
        self.assertEqual(argv[3], "/x/db/audiobooks.pebble")
        self.assertEqual(argv[4:], ["enable_auth=true>false"])
        self.assertEqual(run.call_args.kwargs["cwd"], w.REPO_ROOT)

    def test_a_failed_edit_stops_the_run(self):
        layout = mock.Mock()
        layout.db = pathlib.Path("/x/db/audiobooks.pebble")
        bad = subprocess.CompletedProcess([], 1, stdout="", stderr="no config_blob")
        failing = mock.patch.object(rec.subprocess, "run", return_value=bad)
        with failing, self.assertRaises(w.CorpusError) as cm:
            rec.edit_config(layout, [rec.WORKER_FLAG])
        self.assertIn("no config_blob", str(cm.exception))

    def test_each_boot_turns_the_worker_flag_on_and_only_the_auth_off_boot_turns_auth_off(self):
        seen = []
        stop = RuntimeError("stop after the edit")

        def record_edits(layout, edits):
            seen.append(edits)
            raise stop

        args = rec.build_parser().parse_args(["--check"])
        for auth_off, want in (
            (False, [rec.WORKER_FLAG, rec.DISCOVERY_FLAG]),
            (True, [rec.WORKER_FLAG, rec.DISCOVERY_FLAG, rec.AUTH_OFF_EDIT]),
        ):
            staged = mock.patch.object(rec, "stage_copy")
            edited = mock.patch.object(rec, "edit_config", side_effect=record_edits)
            with staged, edited, self.assertRaises(RuntimeError):
                rec.boot_and_record(args, pathlib.Path("r0"), [], [], auth_off)
            self.assertEqual(seen[-1], want)

    def test_the_helper_source_is_a_txt_file_that_checks_what_it_replaces(self):
        src = rec.EDIT_CONFIG_SRC.read_text(encoding="utf-8")
        self.assertEqual(rec.EDIT_CONFIG_SRC.suffix, ".txt")
        self.assertIn("pebble.Open(path, &pebble.Options{})", src)
        self.assertIn('e.from != "*" && string(was) != e.from', src)
        self.assertEqual(rec.AUTH_OFF_EDIT, "enable_auth=true>false")
        self.assertEqual(rec.WORKER_FLAG, "fingerprint_remote_workers_enabled=*>true")
        self.assertEqual(rec.DISCOVERY_FLAG, "deluge_discovery_enabled=*>true")

    def test_the_auth_on_pass_is_saved_and_read_back(self):
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / "sub" / rec.PARTIAL_ON
            doc = {"columns": [[200, 403]], "canary": {"anonymous": 401}}
            rec.save_partial(path, doc)
            self.assertEqual(json.loads(path.read_text()), doc)
        ns = rec.build_parser().parse_args(["--check", "--reuse-auth-on", "x.json"])
        self.assertEqual(ns.reuse_auth_on, "x.json")


class FilesAndCheckTest(unittest.TestCase):
    def doc(self, flip: bool = False):
        rig = Rig()
        self.addCleanup(rig.close)
        out = rig.run()
        extra = {k: out[k] for k in ("tasks", "approvals", "census")}
        meta = {"r0_tag": "v9.9.9", "r0_binary_sha256": "0" * 64}
        doc = rec.build_verdicts(meta, rig.routes, rig.principals, out["columns"], extra)
        if flip:
            v = doc["routes"][1]["verdict"]
            doc["routes"][1]["verdict"] = ("D" if v[0] == "A" else "A") + v[1:]
        return doc, rig

    def test_the_output_names_the_r0_tag_and_the_route_count(self):
        doc, rig = self.doc()
        self.assertEqual(doc["r0_tag"], "v9.9.9")
        self.assertEqual(doc["route_count"], len(rig.routes))
        self.assertEqual(doc["principals"], [p["name"] for p in rig.principals])

    def test_a_one_cell_diff_is_reported_and_check_exits_nonzero(self):
        old, rig = self.doc()
        new, _ = self.doc(flip=True)
        diffs = rec.diff_verdicts(old, new)
        self.assertEqual(len(diffs), 1)
        self.assertIn(new["routes"][1]["path"], diffs[0])
        self.assertEqual(rec.diff_verdicts(old, old), [])

    def test_main_check_exit_codes(self):
        import tempfile

        old, _ = self.doc()
        new, _ = self.doc(flip=True)
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp)
            rec.write_json(out / "verdicts.json", old)
            orig = rec.record
            try:
                rec.record = lambda args: ({}, new)
                err, so = io.StringIO(), io.StringIO()
                with redirect_stderr(err), redirect_stdout(so):
                    rc = rec.main(["--check", "--out", str(out)])
                self.assertEqual(rc, 1)
                self.assertIn("1 differing cell", err.getvalue())
                rec.record = lambda args: ({}, old)
                with redirect_stderr(io.StringIO()), redirect_stdout(io.StringIO()):
                    self.assertEqual(rec.main(["--check", "--out", str(out)]), 0)
            finally:
                rec.record = orig

    def test_no_credential_reaches_the_output(self):
        import tempfile

        doc, rig = self.doc()
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp)
            rec.write_json(out / "verdicts.json", doc)
            rec.write_json(out / "principals.json", rec.principals_doc(rig.principals))
            text = (out / "verdicts.json").read_text() + (out / "principals.json").read_text()
        self.assertIsNone(ABK_RE.search(text))
        for secret in list(rig.client.keys.values()) + [pw for pw, _ in rec.USERS.values()]:
            self.assertNotIn(secret, text)

    def test_written_files_carry_a_header(self):
        import tempfile

        doc, _ = self.doc()
        with tempfile.TemporaryDirectory() as tmp:
            rec.write_json(pathlib.Path(tmp) / "verdicts.json", doc)
            head = json.loads((pathlib.Path(tmp) / "verdicts.json").read_text())["_header"]
        self.assertEqual(head["file"], "internal/books/rbacreach/verdicts.json")
        self.assertEqual(set(head), {"file", "version", "guid", "last-edited"})


class CommittedDataTest(unittest.TestCase):
    """The recorded data, once it is committed, is whole (skipped before the first run)."""

    @classmethod
    def setUpClass(cls):
        p = REACH / "verdicts.json"
        if not p.is_file():
            raise unittest.SkipTest("verdicts.json not recorded yet")
        cls.v = json.loads(p.read_text())
        cls.routes = json.loads((REACH / "routes.json").read_text())
        cls.principals = json.loads((REACH / "principals.json").read_text())

    def test_every_dumped_route_has_a_verdict_per_principal(self):
        names = [p["name"] for p in self.principals["principals"]]
        self.assertEqual(self.v["principals"], names)
        self.assertEqual(self.v["route_count"], self.routes["route_count"])
        self.assertEqual(len(self.v["routes"]), self.routes["route_count"])
        for r in self.v["routes"]:
            self.assertEqual(len(r["verdict"]), len(names), r["path"])

    def test_the_r0_tag_is_the_manifest_tag(self):
        m = json.loads((REPO_ROOT / "internal/books/testdata/r2corpus/manifest.json").read_text())
        self.assertEqual(self.v["r0_tag"], m["r0_tag"])
        self.assertEqual(self.v["r0_binary_sha256"], m["r0_binary_sha256"])

    def test_no_secret_and_no_go_file_in_the_data_directory(self):
        for p in REACH.iterdir():
            self.assertNotEqual(p.suffix, ".go")
            self.assertIsNone(ABK_RE.search(p.read_text()), p.name)


if __name__ == "__main__":
    unittest.main()
