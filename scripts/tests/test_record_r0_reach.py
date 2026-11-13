# file: scripts/tests/test_record_r0_reach.py
# version: 1.0.0
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
import sys
import threading
import unittest
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
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
        self.delete_user_on: str | None = None  # a route whose probe drops a user
        self.defs = [
            {"id": "scan.trigger", "permissions": ["scan.trigger"], "approver": "requests.approve"},
            {"id": "books.noop", "permissions": ["library.view"], "approver": ""},
        ]
        self.defs_start_status = 400
        self.awaiting = ["run-1"]
        self.dead = False
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
        with self.lock:
            self._handle(h)

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
        if path == "/api/v1/audiobooks" and method == "GET":
            return self.reply(h, 200, {"count": 3})
        if path == "/api/v3/ops/defs" and method == "GET":
            return self.reply(h, 200 if user or self.auth_off else 401, {"items": self.defs})
        if path == "/api/v3/ops/runs" and method == "GET":
            items = [{"id": r, "state": "awaiting_decision"} for r in self.awaiting]
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

    def test_verdict_rule(self):
        self.assertEqual(rec.verdict_of(401, "k"), "D")
        self.assertEqual(rec.verdict_of(403, "k"), "D")
        self.assertEqual(rec.verdict_of(404, "k"), "A")
        self.assertEqual(rec.verdict_of(422, "k"), "A")
        for bad in (429, 500, 503, 0):
            with self.assertRaises(rec.RecorderError):
                rec.verdict_of(bad, "k")


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
        self.rig.fake.fail_status["PUT /api/v1/audiobooks"] = 500
        with self.assertRaises(rec.RecorderError) as cm:
            self.rig.run()
        self.assertIn("PUT /api/v1/audiobooks/:id", str(cm.exception))
        self.assertIn("500", str(cm.exception))

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
