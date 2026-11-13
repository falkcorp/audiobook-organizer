#!/usr/bin/env python3
# file: scripts/r2corpus/record_r0_reach.py
# version: 1.3.1
# guid: 8e5b1d7c-4a92-4f36-b0c8-2d7a9e3f6b14
# last-edited: 2026-11-13
"""Record the R0 binary's reach over the R2 overlay corpus (F4-07).

05-rbac §6.3 / 09 §9.2.6: the reach oracle compares, for every (route, principal)
cell, what the R0 release binary allowed with what R2 allows after M3 migrated
the same corpus. This script records the R0 half: it boots the R0 binary on a
copy of the overlay store (``write_r0_corpus.py``'s boot spec, F2-13's rows),
requests every route of R0's router dump (``routes.json``, written by
``dump_r0_routes.py`` from the tag's source) as every principal of
``principal_table()`` and writes, under ``internal/books/rbacreach/``:

- ``principals.json``: the table itself (the Go test seeds the same rows),
- ``verdicts.json``: per route a verdict string, one letter per principal
  (``A`` allowed past the authorization decision, ``D`` denied by it), plus the
  books-task start and run-approval probes.

A probe is a request that fails *after* authorization when it is authorized: a
dummy path id (404) or an empty ``{}`` body (400/422). So a 401/403 is a
denial and any other status below 500 is an allow. Nothing else is a verdict:
a 429, a 5xx and a connection error are recorder failures, never recorded, and
a drift sentinel (a census of users, books and task definitions, plus a
per-principal canary) fails the run when a probe changed the store or killed the
server.

Two boots: one with auth on (every principal but ``authoff``) and one on a copy
whose stored config has ``enable_auth`` turned off (``authoff``; R0 reads the
stored config after the environment, so ``ENABLE_AUTH=false`` does not reach a
store that has one: ``r0authoff/flip_auth.go.txt``). Secrets never reach the
output: keys are minted on the copy and held in memory only.

``--check`` re-derives the data and exits 1 on any differing verdict cell.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import datetime
import functools
import http.client
import http.cookiejar
import importlib.util
import json
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.error
import urllib.request
from typing import Any, Callable

_HERE = pathlib.Path(__file__).resolve().parent


def _load(name: str, filename: str):
    """Import a sibling script by path (registered before exec, for dataclasses)."""
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, _HERE / filename)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


w = _load("write_r0_corpus", "write_r0_corpus.py")
ra = _load("record_r0_answers", "record_r0_answers.py")

REACH_REL = "internal/books/rbacreach"
REACH_DIR = w.REPO_ROOT / REACH_REL
OVERLAY_STORE = w.CORPUS_DIR / "overlay"
HEADER_GUIDS = {
    "principals.json": "b3c2f6e1-7d48-4a15-9e0b-5c1a8d2f4e67",
    "routes.json": "d41a8b7f-2c65-4e93-8f10-6b9e3a7c5d28",
    "verdicts.json": "e92f5c3a-8b17-4d64-a0c9-1f6d7b2e8a43",
}
DATA_VERSION = "1.0.0"

RECORDER_UA = "r2corpus-reach/1.0"
KEY_PREFIX = "r2reach-"
ALLOW, DENY = "A", "D"
# What each principal sees as a denial. R0's middleware answers 401 for no or an
# unusable credential and 403 for an authenticated principal lacking the atom.
DENY_STATUSES = frozenset({401, 403})
# A route whose authorized probe answers 5xx, by "METHOD /path", with the reason.
# Authorization runs before the handler, so a 5xx is an allow: the request was let
# through and the handler then failed on the recorder's dummy id or empty body, or
# on a service the throwaway store has not configured. Any 5xx not listed here is
# a recorder failure (the probe did not reach a verdict), and a listed route must
# answer 401 to the anonymous caller (check_5xx_follows_authorization), which is
# what shows the 5xx came after the decision. Each reason is R0's own message in
# the serve log of the run that observed it (run 43391356590).
OK_5XX: dict[str, str] = {
    "POST /api/v1/ai/scans": "500 AI scan pipeline not configured",
    "POST /api/v1/ai/scans/:id/cancel": "500 AI scan pipeline not configured",
    "POST /api/v1/ai/test-connection": "500 connection test failed (the OpenAI call is refused, no key)",
    "PUT /api/v1/audiobooks/:id": "500 failed to update audiobook (no updates provided)",
    "POST /api/v1/audiobooks/bulk-write-back": (
        "500 enqueue failed while an earlier probe's run holds the op's concurrency key"
        " (202 otherwise)"
    ),
    "POST /api/v1/audiobooks/:id/apply-metadata": "500 failed to apply metadata (audiobook not found)",
    "POST /api/v1/audiobooks/:id/mark-no-match": "500 failed to mark no match (audiobook not found)",
    "POST /api/v1/audiobooks/:id/parse-with-ai": "500 a recovered nil-pointer panic: no AI parser is configured",
    "POST /api/v1/audiobooks/:id/quarantine": "500 quarantine failed (book not found)",
    "DELETE /api/v1/audiobooks/:id/quarantine": "500 unquarantine failed (book not found)",
    "POST /api/v1/audiobooks/:id/retired-versions/:vid/restore": (
        "501 restoring a retired version is not available yet"
    ),
    "DELETE /api/v1/backup/:filename": "500 failed to delete backup (no such file or backups dir)",
    "POST /api/v1/dedup/fingerprint-rescan": "503 no fingerprint backend (fpcalc / ffmpeg) found",
    "GET /api/v1/deluge/labels": "502 the Deluge host does not resolve (an unconfigured client)",
    "POST /api/v1/deluge/test-connection": "502 the Deluge host does not resolve",
    "GET /api/v1/deluge/torrents": "502 the Deluge host does not resolve",
    "POST /api/v1/operations/:id/revert": "500 failed to revert operation (no changes for the operation)",
    "POST /api/v1/update/apply": "500 failed to apply update (no release asset to apply)",
}

# The same for the auth-disabled boot only: a handler that assumes a signed-in user
# and fails without one. With authentication off no decision stands before the
# handler, so these are allows too; they are kept apart so the auth-on pass does
# not take them. Each reason is R0's own message in the auth-off boot's serve log.
OK_5XX_AUTHOFF: dict[str, str] = {}

ADMIN_USER = "lucius.fox"  # the corpus' admin-role user, whose session mints keys
USERS: dict[str, tuple[str, str]] = {
    # username -> (password, role label)
    "lucius.fox": (w.OTHER_PASSWORD, "admin"),
    "dick.grayson": (w.OTHER_PASSWORD, "editor"),
    "barbara.gordon": (w.OTHER_PASSWORD, "viewer"),
    "fp.worker": (w.OTHER_PASSWORD, "fp-worker"),
    "james.gordon": ("fixture-password-arkham-asylum-1974", "custom-all"),
    "harleen.quinzel": ("fixture-password-arkham-asylum-1974", "custom-subset"),
    "lucius.fox.jr": ("fixture-password-arkham-asylum-1974", "custom-unknown"),
}
# The overlay's custom roles (scripts/r2corpus/overlay/identity.go): the Go test
# seeds the same permission lists.
CUSTOM_ROLES = {
    "custom-all": {"name": "GCPD Command", "permissions": list(w.ALL_ATOMS)},
    "custom-subset": {
        "name": "Arkham Staff",
        "permissions": [
            "library.view",
            "library.edit_metadata",
            "collections.manage",
            "requests.create",
        ],
    },
    "custom-unknown": {
        "name": "Wayne Enterprises",
        "permissions": ["library.view", "playlists.create", "batcave.access"],
    },
}
SCOPE_SETS: dict[str, list[str]] = {
    "view": ["library.view"],
    "settings": ["settings.manage"],
    "users": ["users.manage"],
    "worker": ["fingerprint.worker"],
    "every": list(w.ALL_ATOMS),
    "empty": [],
}
KEY_OWNERS = ("admin", "editor", "viewer", "fp-worker")
CUSTOM_KEYS = {
    "custom-all": ("every", "empty"),
    "custom-subset": ("outside", "empty"),
}
OUTSIDE_SCOPES = ["settings.manage", "users.manage"]  # outside Arkham Staff's roles


def _owner_user(label: str) -> str:
    for u, (_, lab) in USERS.items():
        if lab == label:
            return u
    raise KeyError(label)


def principal_table() -> list[dict[str, Any]]:
    """05-rbac §12's principal table: sessions, scoped keys, anonymous, auth off."""
    rows: list[dict[str, Any]] = []
    for user, (_, label) in USERS.items():
        rows.append({"name": f"session:{label}", "kind": "session", "user": user, "role": label})
    for label in KEY_OWNERS:
        for set_name, scopes in SCOPE_SETS.items():
            rows.append(
                {
                    "name": f"key:{label}:{set_name}",
                    "kind": "key",
                    "user": _owner_user(label),
                    "role": label,
                    "scopes": list(scopes),
                }
            )
    for label, sets in CUSTOM_KEYS.items():
        for set_name in sets:
            scopes = OUTSIDE_SCOPES if set_name == "outside" else SCOPE_SETS[set_name]
            rows.append(
                {
                    "name": f"key:{label}:{set_name}",
                    "kind": "key",
                    "user": _owner_user(label),
                    "role": label,
                    "scopes": list(scopes),
                }
            )
    rows.append({"name": "anonymous", "kind": "anonymous"})
    rows.append({"name": "authoff", "kind": "authoff"})
    return rows


# ---------------------------------------------------------------- probes

_PARAM = re.compile(r":[A-Za-z_][A-Za-z0-9_]*")
_WILD = re.compile(r"\*[A-Za-z_][A-Za-z0-9_]*")
DUMMY_ID = "r2reach-missing"
MUTATING = frozenset({"POST", "PUT", "PATCH"})
# Routes that end the caller's session: probed last per principal, then the
# session principal logs in again.
SESSION_KILLERS = frozenset({("POST", "/api/v1/auth/logout")})


def probe_path(path: str) -> str:
    return _WILD.sub("r2reach/missing", _PARAM.sub(DUMMY_ID, path))


def probe_body(method: str) -> Any:
    return {} if method in MUTATING else None


# R0 bounds the two public invite routes with a per-token bucket (burst 8, 6 a
# minute) beside the per-peer one, and a request with no token or a body that
# does not parse shares one fallback bucket. The peer-address spread does not
# help: thirty-odd principals probing one dummy token get a 429 after the eighth.
# Each principal therefore probes with a token of its own.
INVITE_GET = ("GET", "/api/v1/auth/invite/:token")
INVITE_ACCEPT = ("POST", "/api/v1/auth/accept-invite")


def probe_token(principal_name: str) -> str:
    """A dummy invite token no other principal uses."""
    return f"{DUMMY_ID}-{re.sub(r'[^A-Za-z0-9]+', '-', principal_name)}"


def probe_request(method: str, path: str, principal_name: str) -> tuple[str, Any]:
    """The request a principal sends for a route: probe_path and probe_body, with
    the invite routes' token made the principal's own."""
    if (method, path) == INVITE_GET:
        return path.replace(":token", probe_token(principal_name)), None
    if (method, path) == INVITE_ACCEPT:
        return path, {"token": probe_token(principal_name)}
    return probe_path(path), probe_body(method)


class RecorderError(RuntimeError):
    """A probe that did not reach a verdict, or a store that drifted."""


def verdict_of(status: int, key: str, ok_5xx: dict[str, str] | None = None) -> str:
    """401/403 -> D; any other status below 500 -> A. Else the run is wrong."""
    listed = OK_5XX if ok_5xx is None else ok_5xx
    if status == 429:
        raise RecorderError(f"{key}: 429 (the rate limiter is on): not a verdict")
    route = key.split(" ", 1)[1] if " " in key else key  # "<principal> METHOD /path"
    if status >= 500 and route not in listed:
        raise RecorderError(
            f"{key}: {status}: a probe that fails after authorization must answer below 500"
        )
    if status <= 0:
        raise RecorderError(f"{key}: no response")
    return DENY if status in DENY_STATUSES else ALLOW


class SourceIPs:
    """A fresh loopback source address per request (127.0.0.0/8, all local on Linux).

    R0 limits each peer address to a token bucket (100 a minute, burst 20) and
    the setting is read from the store, not the environment, so ENABLE_RATE_LIMIT
    cannot turn it off. One address per probe never meets another's bucket: the
    limiter stays on, as R0 ships it, and no probe waits for it.
    """

    SPAN = 1 << 24

    def __init__(self) -> None:
        self._n = 1
        self._lock = threading.Lock()

    def next(self) -> str:
        with self._lock:
            self._n = self._n % (self.SPAN - 2) + 1
            n = self._n
        return f"127.{(n >> 16) & 255}.{(n >> 8) & 255}.{n & 255}"


# Set by main() from --spread-source-ips; None sends from the default address.
SOURCES: SourceIPs | None = None


class _FromAddress(urllib.request.HTTPHandler):
    def __init__(self, address: str) -> None:
        super().__init__()
        self.address = address

    def http_open(self, req: urllib.request.Request):
        conn = functools.partial(http.client.HTTPConnection, source_address=(self.address, 0))
        return self.do_open(conn, req)


def status_only(
    client: Any,
    method: str,
    path: str,
    body: Any,
    auth: str,
    timeout: float,
    source: str | None = None,
) -> int:
    """Send one request and return its status without reading the body.

    The writer's client reads the whole body, which never ends for an event
    stream whose heartbeat keeps resetting the socket timeout (a probe that was
    authorized for ``GET /api/events`` hung the first runner pass for 37 minutes).
    A 429 is not retried: it means the rate limiter is on, a recorder failure.
    """
    hdrs = {"Accept": "application/json", "User-Agent": RECORDER_UA}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    handlers: list[Any] = []
    if auth.startswith("key:"):
        hdrs["Authorization"] = f"Bearer {client.keys[auth[4:]]}"
    elif auth != "none":
        jar = client.jars.setdefault(auth, http.cookiejar.CookieJar())
        handlers.append(urllib.request.HTTPCookieProcessor(jar))
    req = urllib.request.Request(client.base + path, data=data, method=method, headers=hdrs)
    if source:
        handlers.append(_FromAddress(source))
    opener = urllib.request.build_opener(*handlers)
    try:
        with opener.open(req, timeout=timeout) as r:
            return int(r.status)  # leaving the block closes the connection
    except urllib.error.HTTPError as e:
        try:
            return int(e.code)
        finally:
            e.close()


class Probe:
    """One principal's way of sending a request (a cookie jar, a key or none)."""

    def __init__(self, client: Any, principal: dict[str, Any], login: Callable[[], None] | None):
        self.client = client
        self.principal = principal
        self._login = login
        self.auth = _auth_name(principal)
        self.canary_status: int | None = None  # what the first canary answered

    def send(self, method: str, path: str, body: Any) -> int:
        """The status line of one request, with the body left unread: a probe that
        is authorized can reach a stream (SSE) that never ends, and the verdict
        is the status, which the headers already carry."""
        try:
            source = SOURCES.next() if SOURCES else None
            return status_only(self.client, method, path, body, self.auth, 60.0, source)
        except OSError as e:  # connection refused/reset, timeout: the server died
            raise RecorderError(f"{self.principal['name']} {method} {path}: {e}") from e

    def fetch(self, path: str) -> Any:
        """A GET whose body the recorder reads (retries a 429, like the writer's client)."""
        try:
            return self.client.request("GET", path, auth=self.auth, timeout=60.0)
        except OSError as e:
            raise RecorderError(f"{self.principal['name']} GET {path}: {e}") from e

    def relogin(self) -> None:
        if self._login:
            self._login()


def _auth_name(p: dict[str, Any]) -> str:
    if p["kind"] == "session":
        return f"sess:{p['name']}"
    if p["kind"] == "key":
        return f"key:{KEY_PREFIX}{p['name']}"
    return "none"  # anonymous, and authoff (the auth-off boot has no credential)


def canary(probe: Probe) -> None:
    """The principal's credential still behaves as it did at the start.

    The first call records the status of a harmless request and requires a live
    credential's answer: 200, or 403 for a worker-confined credential ("this
    credential is limited to the fingerprint worker API": R0 refuses `me` to it
    by design), 401 for the anonymous caller, 200 on the auth-disabled status
    route. Every later call must answer the same, so a probe that revoked a
    session, stripped a role or killed the server is caught, whichever status
    that principal's baseline is."""
    kind = probe.principal["kind"]
    if kind == "authoff":
        status = probe.send("GET", "/api/v1/auth/status", None)
        first = {200}
    elif kind == "anonymous":
        status = probe.send("GET", "/api/v1/auth/me", None)
        first = {401}
    else:
        status = probe.send("GET", "/api/v1/auth/me", None)
        first = {200, 403}
    want = first if probe.canary_status is None else {probe.canary_status}
    if status not in want:
        raise RecorderError(
            f"canary for {probe.principal['name']}: {status}, want one of {sorted(want)}"
        )
    probe.canary_status = status


def order_routes(routes: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Session-killing routes last (a stable order otherwise)."""

    def key(r: dict[str, Any]) -> tuple[bool, str, str]:
        return ((r["method"], r["path"]) in SESSION_KILLERS, r["path"], r["method"])

    return sorted(routes, key=key)


def probe_principal(probe: Probe, routes: list[dict[str, Any]]) -> list[int]:
    """One status per route. A probe with no verdict (429, 5xx) is collected, not
    recorded, so one run names every such route; a dead server stops the pass."""
    canary(probe)
    out: dict[tuple[str, str], int] = {}
    problems: list[str] = []
    listed = {**OK_5XX, **OK_5XX_AUTHOFF} if probe.principal["kind"] == "authoff" else OK_5XX
    for r in order_routes(routes):
        m, p = r["method"], r["path"]
        req_path, req_body = probe_request(m, p, probe.principal["name"])
        status = probe.send(m, req_path, req_body)
        try:
            verdict_of(status, f"{probe.principal['name']} {m} {p}", listed)
        except RecorderError as e:
            problems.append(str(e))
        out[(m, p)] = status
        if (m, p) in SESSION_KILLERS:
            probe.relogin()
    if problems:
        raise RecorderError(f"{len(problems)} probe(s) without a verdict:\n" + "\n".join(problems))
    canary(probe)
    print(f"recorded {probe.principal['name']}: {len(routes)} routes", file=sys.stderr, flush=True)
    return [out[(r["method"], r["path"])] for r in routes]


# ---------------------------------------------------------------- tasks, approvals


def task_defs(admin: Probe) -> list[dict[str, Any]]:
    r = admin.fetch("/api/v3/ops/defs")
    body = r.body if isinstance(r.body, dict) else {}
    items = body.get("items") or body.get("defs") or (body.get("data") or {}).get("items") or []
    if r.status != 200 or not items:
        raise RecorderError(f"GET /api/v3/ops/defs -> {r.status}, {len(items)} defs")
    return sorted(items, key=lambda d: str(d["id"]))


RUN_FIELDS = ("id", "def_id", "def", "kind", "state", "owner", "created_by")


def awaiting_runs(admin: Probe) -> list[dict[str, Any]]:
    """The runs in state awaiting_decision, each with the fields that name its definition."""
    r = admin.fetch("/api/v3/ops/runs?limit=200")
    body = r.body if isinstance(r.body, dict) else {}
    items = body.get("items") or []
    runs = [
        {k: x[k] for k in RUN_FIELDS if k in x}
        for x in sorted(items, key=lambda x: str(x["id"]))
        if x.get("state") == "awaiting_decision"
    ]
    if not runs:
        raise RecorderError(
            "the corpus holds no run in state awaiting_decision to probe approvals on"
        )
    return runs


# A task start that is authorized fails validation: a run must never start.
TASK_OK_AFTER_AUTH = frozenset({400, 404, 409, 422})
# An approval probe sends decision "maybe": 422 once authorized; 404 is the run
# not being readable by the caller (mayReadRun), which is before the decision.
APPROVE_REACHED = frozenset({422})


def probe_tasks(probes: list[Probe], defs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows = []
    for d in defs:
        path = f"/api/v3/ops/defs/{d['id']}/runs"
        statuses = []
        for p in probes:
            s = p.send("POST", path, {"params": []})
            verdict_of(s, f"{p.principal['name']} POST {path}")
            if s not in DENY_STATUSES and s not in TASK_OK_AFTER_AUTH:
                raise RecorderError(
                    f"{p.principal['name']} POST {path} -> {s}: an authorized probe must fail "
                    "validation, and this one may have started a run"
                )
            statuses.append(s)
        rows.append(
            {
                "id": d["id"],
                "permissions": d.get("permissions") or [],
                "approver": d.get("approver") or "",
                "status": statuses,
                "verdict": "".join(DENY if s in DENY_STATUSES else ALLOW for s in statuses),
            }
        )
    return rows


def probe_approvals(probes: list[Probe], runs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows = []
    for run in runs:
        rid = str(run["id"])
        path = f"/api/v3/ops/runs/{rid}/approve"
        statuses, cells = [], []
        for p in probes:
            s = p.send("POST", path, {"decision": "maybe"})
            verdict_of(s, f"{p.principal['name']} POST {path}")
            statuses.append(s)
            # N: the probe did not reach the approver check (the run is not
            # readable by this caller); it carries no reach information.
            cells.append(DENY if s in DENY_STATUSES else ALLOW if s in APPROVE_REACHED else "N")
        rows.append({"run_id": rid, "run": run, "status": statuses, "verdict": "".join(cells)})
    return rows


# ---------------------------------------------------------------- sentinel


def census(admin: Probe) -> dict[str, Any]:
    """What probing must not change: users (id, name, roles, status), books, task defs."""
    users = admin.fetch("/api/v1/users")
    if users.status != 200:
        raise RecorderError(f"census: GET /api/v1/users -> {users.status}")
    rows = sorted(
        (
            u["id"],
            u["username"],
            sorted(u.get("roles") or []),
            u.get("status", ""),
        )
        for u in (users.body.get("data") or {}).get("users", [])
    )
    books = admin.fetch("/api/v1/audiobooks/count")
    if books.status != 200:
        raise RecorderError(f"census: GET /api/v1/audiobooks/count -> {books.status}")
    total = ((books.body or {}).get("data") or {}).get("count")
    if not isinstance(total, int) or isinstance(total, bool):
        # A census that reads nothing counts nothing: the sentinel would pass on any drift.
        raise RecorderError(f"census: the book count is {total!r}, not a number")
    return {
        "users": [list(r) for r in rows],
        "books": total,
        "task_defs": [d["id"] for d in task_defs(admin)],
    }


def check_no_drift(before: dict[str, Any], after: dict[str, Any]) -> None:
    for k in before:
        if before[k] != after[k]:
            raise RecorderError(
                f"the store drifted while probing ({k}): {before[k]!r} -> {after[k]!r}"
            )


# ---------------------------------------------------------------- the pass


def check_5xx_follows_authorization(
    routes: list[dict[str, Any]],
    principals: list[dict[str, Any]],
    columns: list[list[int]],
    ok_5xx: dict[str, str] | None = None,
) -> None:
    """A route listed in OK_5XX is an allow only because its 5xx came after the
    decision: the anonymous caller, who never passes it, must be refused."""
    listed = OK_5XX if ok_5xx is None else ok_5xx
    names = [p["name"] for p in principals]
    if "anonymous" not in names:
        return
    anon = columns[names.index("anonymous")]
    bad = [
        f"{r['method']} {r['path']}: anonymous got {anon[i]}, not a refusal"
        for i, r in enumerate(routes)
        if f"{r['method']} {r['path']}" in listed and anon[i] not in DENY_STATUSES
    ]
    if bad:
        raise RecorderError(
            "OK_5XX route(s) whose 5xx is not shown to follow authorization:\n" + "\n".join(bad)
        )


def record_pass(
    make_probe: Callable[[dict[str, Any]], Probe],
    principals: list[dict[str, Any]],
    routes: list[dict[str, Any]],
    admin: Probe,
    workers: int,
    do_tasks: bool,
) -> dict[str, Any]:
    probes = [make_probe(p) for p in principals]
    before = census(admin) if do_tasks else None
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
        futures = [pool.submit(probe_principal, p, routes) for p in probes]
        columns: list[list[int]] = []
        failures: list[str] = []
        for f in futures:  # every principal's failure is reported, not the first one's
            try:
                columns.append(f.result())
            except RecorderError as e:
                failures.append(str(e))
    if failures:
        raise RecorderError("\n".join(failures))
    check_5xx_follows_authorization(routes, principals, columns)
    out: dict[str, Any] = {"columns": columns}
    out["canary"] = {p.principal["name"]: p.canary_status for p in probes}
    if do_tasks:
        out["tasks"] = probe_tasks(probes, task_defs(admin))
        out["approvals"] = probe_approvals(probes, awaiting_runs(admin))
        for p in probes:
            canary(p)
        after = census(admin)
        check_no_drift(before, after)
        out["census"] = {
            "users": len(after["users"]),
            "books": after["books"],
            "task_defs": len(after["task_defs"]),
        }
    return out


def build_verdicts(
    meta: dict[str, Any],
    routes: list[dict[str, Any]],
    principals: list[dict[str, Any]],
    columns: list[list[int]],
    extra: dict[str, Any],
) -> dict[str, Any]:
    """Assemble verdicts.json from one status column per principal."""
    rows = []
    for i, r in enumerate(routes):
        st = [col[i] for col in columns]
        rows.append(
            {
                "method": r["method"],
                "path": r["path"],
                "probe_path": probe_path(r["path"]),
                "abs_only": bool(r.get("abs_only")),
                "status": st,
                "verdict": "".join(DENY if s in DENY_STATUSES else ALLOW for s in st),
            }
        )
    uniform = sorted(f"{r['method']} {r['path']}" for r in rows if set(r["status"]) == {404})
    return {
        **meta,
        "route_count": len(routes),
        "principals": [p["name"] for p in principals],
        "routes": rows,
        "uniform_not_found": uniform,
        **extra,
    }


# ---------------------------------------------------------------- files


def header(name: str) -> dict[str, str]:
    return {
        "file": f"{REACH_REL}/{name}",
        "version": DATA_VERSION,
        "guid": HEADER_GUIDS[name],
        "last-edited": datetime.date.today().isoformat(),
    }


def write_json(path: pathlib.Path, data: dict[str, Any]) -> None:
    body = {"_header": header(path.name), **data}
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(body, indent=1, sort_keys=False) + "\n", encoding="utf-8")


def principals_doc(principals: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "what": "the principal table of 05-rbac section 12; the Go oracle seeds the same rows",
        "users": {u: label for u, (_, label) in USERS.items()},
        "custom_roles": CUSTOM_ROLES,
        "principals": principals,
    }


def diff_verdicts(old: dict[str, Any], new: dict[str, Any]) -> list[str]:
    """One line per differing verdict cell (statuses are not compared)."""
    out: list[str] = []
    for k in ("r0_tag", "r0_binary_sha256", "route_count", "principals"):
        if old.get(k) != new.get(k):
            out.append(f"{k}: {old.get(k)!r} -> {new.get(k)!r}")
    if old.get("canary") != new.get("canary"):
        out.append(f"canary: {old.get('canary')!r} -> {new.get('canary')!r}")
    names = new["principals"]
    o = {(r["method"], r["path"]): r["verdict"] for r in old["routes"]}
    n = {(r["method"], r["path"]): r["verdict"] for r in new["routes"]}
    for key in sorted(set(o) | set(n)):
        if key not in o or key not in n:
            out.append(f"{key[0]} {key[1]}: route {'added' if key not in o else 'removed'}")
            continue
        for i, (a, b) in enumerate(zip(o[key], n[key])):
            if a != b:
                out.append(f"{key[0]} {key[1]} / {names[i]}: {a} -> {b}")
    for sect in ("tasks", "approvals"):
        o2 = {r.get("id", r.get("run_id")): r["verdict"] for r in old.get(sect, [])}
        n2 = {r.get("id", r.get("run_id")): r["verdict"] for r in new.get(sect, [])}
        for key in sorted(set(o2) | set(n2)):
            if o2.get(key) != n2.get(key):
                out.append(f"{sect} {key}: {o2.get(key)} -> {n2.get(key)}")
    return out


# ---------------------------------------------------------------- the real R0


def _login(client: Any, ident: str, username: str, password: str) -> None:
    client.jars.pop(ident, None)
    r = client.request(
        "POST",
        "/api/v1/auth/login",
        {"username": username, "password": password, "remember_me": True},
        auth=ident,
    )
    if r.status != 200:
        raise RecorderError(f"login {username} -> {r.status}")


def _user_ids(client: Any, auth: str) -> dict[str, str]:
    r = client.request("GET", "/api/v1/users", auth=auth)
    if r.status != 200:
        raise RecorderError(f"GET /api/v1/users -> {r.status}")
    return {u["username"]: u["id"] for u in (r.body.get("data") or {}).get("users", [])}


def prepare_credentials(client: Any, principals: list[dict[str, Any]]) -> dict[str, Probe]:
    """Log the session principals in and mint the key principals' keys (memory only)."""
    admin_ident = f"sess:session:{USERS[ADMIN_USER][1]}"
    _login(client, admin_ident, ADMIN_USER, USERS[ADMIN_USER][0])
    ids = _user_ids(client, admin_ident)
    probes: dict[str, Probe] = {}
    for p in principals:
        if p["kind"] == "session":
            pw = USERS[p["user"]][0]
            ident = _auth_name(p)
            _login(client, ident, p["user"], pw)
            probes[p["name"]] = Probe(
                client, p, lambda i=ident, u=p["user"], pw=pw: _login(client, i, u, pw)
            )
        elif p["kind"] == "key":
            r = client.request(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": f"{KEY_PREFIX}{p['name']}",
                    "scopes": p["scopes"],
                    "user_id": ids[p["user"]],
                    "expires_in_days": w.KEY_TTL_DAYS,
                },
                auth=admin_ident,
            )
            secret = _find_secret(r.body)
            if r.status not in (200, 201) or not secret:
                raise RecorderError(
                    f"minting {p['name']} -> {r.status}: {_without_secrets(r.body)}"
                )
            client.keys[f"{KEY_PREFIX}{p['name']}"] = secret
            probes[p["name"]] = Probe(client, p, None)
        else:
            probes[p["name"]] = Probe(client, p, None)
    return probes


def _find_secret(body: Any) -> str | None:
    """The raw key secret in a mint response: any string value starting abk_."""
    if isinstance(body, str):
        return body if body.startswith("abk_") and len(body) > 20 else None
    if isinstance(body, dict):
        body = list(body.values())
    if isinstance(body, list):
        for v in body:
            found = _find_secret(v)
            if found:
                return found
    return None


def _without_secrets(body: Any) -> str:
    """A response body for an error message, with every abk_ value cut."""
    return re.sub(r"abk_[^\"\s,}]+", "abk_<cut>", json.dumps(body))[:400]


FLIP_AUTH_SRC = pathlib.Path(__file__).with_name("r0authoff") / "flip_auth.go.txt"


def flip_auth_off(layout: Any) -> None:
    """Turn enable_auth off in the staged copy's stored config (flip_auth.go.txt)."""
    with tempfile.TemporaryDirectory() as tmp:
        src = pathlib.Path(tmp) / "flip_auth.go"
        shutil.copy(FLIP_AUTH_SRC, src)
        r = subprocess.run(  # noqa: S603 - fixed argv
            ["go", "run", str(src), str(layout.db)],
            cwd=w.REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=1200,
            check=False,
        )
    if r.returncode != 0:
        raise w.CorpusError(
            f"could not turn enable_auth off in the copy: {r.stderr.strip()[-800:]}"
        )
    print(r.stdout.strip(), file=sys.stderr, flush=True)


def check_auth_is_off(client: Any) -> None:
    """R0 booted from the flipped store must answer a credential-less request to a
    route that needs one: a 401 here means the recording would not be auth-off."""
    r = client.request("GET", "/api/v3/ops/runs?limit=1", auth="none")
    if r.status == 401:
        raise w.CorpusError(
            "the auth-off boot answers 401 to a request without a credential: "
            "authentication is still on, so the auth-off column would be a lie"
        )


def stage_copy(args: argparse.Namespace, layout: Any) -> None:
    corpus = pathlib.Path(args.corpus).resolve()
    if layout.workdir.exists():
        shutil.rmtree(layout.workdir)
    shutil.copytree(corpus, layout.store)
    ra.touch_like_a_checkout(layout.store)
    layout.home.mkdir(parents=True, exist_ok=True)
    layout.viper_yaml.write_text(w._yaml_dump(w.VIPER_YAML), encoding="utf-8")


def boot_and_record(
    args: argparse.Namespace,
    binary: pathlib.Path,
    routes: list[dict[str, Any]],
    principals: list[dict[str, Any]],
    auth_off: bool,
) -> dict[str, Any]:
    layout = w.Layout(pathlib.Path(args.workdir))
    stage_copy(args, layout)
    # R0 builds its per-peer rate limiter from the stored config, which the
    # environment does not reach: the recorder spreads its probes over loopback
    # source addresses instead (SourceIPs), and sets no ENABLE_RATE_LIMIT.
    env: dict[str, str] = {}
    if auth_off:
        # ENABLE_AUTH=false does not reach a store that has a config_blob either:
        # rewrite enable_auth in the throwaway copy's blob.
        flip_auth_off(layout)
    proc = w.R0Process(binary, layout, args.port, docker=args.docker, extra_env=env)
    client = w.Client(proc.base, user_agent=RECORDER_UA)
    proc.start()
    try:
        if auth_off:
            check_auth_is_off(client)
            ra.wait_for_boot_runs(client, "none", timeout=args.settle_timeout)
            mine = [p for p in principals if p["kind"] == "authoff"]
            probes = {p["name"]: Probe(client, p, None) for p in mine}
            admin = probes["authoff"]
            return record_pass(lambda p: probes[p["name"]], mine, routes, admin, 1, False)
        ra.mint_answer_key(_Ctx(client, proc, layout))
        ra.wait_for_boot_runs(client, f"key:{ra.ANSWER_KEY}", timeout=args.settle_timeout)
        mine = [p for p in principals if p["kind"] != "authoff"]
        probes = prepare_credentials(client, mine)
        admin = probes["session:admin"]
        return record_pass(lambda p: probes[p["name"]], mine, routes, admin, args.workers, True)
    finally:
        stop_throwaway(proc)
        keep_log(layout, f"{args.workdir.rstrip('/')}-logs", "off" if auth_off else "on")


def stop_throwaway(proc: Any) -> None:
    """Stop R0 and discard its store: the recording is already in memory, so a
    shutdown that hangs or exits non-zero is logged and the process killed."""
    try:
        proc.stop(timeout=10.0)
    except (subprocess.TimeoutExpired, w.CorpusError) as e:
        print(f"R0 did not stop cleanly ({type(e).__name__}); killing it", file=sys.stderr)
        if proc.proc is not None and not proc.docker:
            proc.proc.kill()
            proc.proc.wait()
            proc.proc = None


def keep_log(layout: Any, dest: str, boot: str) -> None:
    """R0's output of this boot, kept for a failed run (the next boot rewrites workdir)."""
    if layout.serve_log.is_file():
        pathlib.Path(dest).mkdir(parents=True, exist_ok=True)
        shutil.copy(layout.serve_log, pathlib.Path(dest) / f"serve-auth-{boot}.out")


class _Ctx:
    def __init__(self, client: Any, proc: Any, layout: Any) -> None:
        self.client, self.proc, self.layout = client, proc, layout


def load_routes(path: pathlib.Path) -> dict[str, Any]:
    doc = json.loads(path.read_text(encoding="utf-8"))
    if not doc.get("routes"):
        raise w.CorpusError(f"{path}: no routes")
    return doc


def record(args: argparse.Namespace) -> tuple[dict[str, Any], dict[str, Any]]:
    manifest = json.loads(w.MANIFEST_PATH.read_text(encoding="utf-8"))
    ra.check_answers_still_valid(manifest)
    tag = w.read_r0_tag()
    if manifest["r0_tag"] != tag:
        raise w.CorpusError(f"manifest r0_tag {manifest['r0_tag']} != {tag}")
    routes_doc = load_routes(pathlib.Path(args.routes))
    if routes_doc.get("r0_tag") != tag:
        raise w.CorpusError(f"routes.json is for {routes_doc.get('r0_tag')}, not {tag}")
    binary = (
        pathlib.Path(args.binary).resolve()
        if args.binary
        else w.fetch_binary(tag, pathlib.Path(args.cache).expanduser())
    )
    if not args.docker:
        w.check_native_binary(binary, False)
    if w.sha256_file(binary) != manifest["r0_binary_sha256"]:
        raise w.CorpusError("this binary is not the one that wrote the corpus")
    w.check_outside_maintenance_window()
    principals = principal_table()
    routes = routes_doc["routes"]
    out = pathlib.Path(args.out)
    if args.reuse_auth_on:
        on = json.loads(pathlib.Path(args.reuse_auth_on).read_text(encoding="utf-8"))
    else:
        on = boot_and_record(args, binary, routes, principals, auth_off=False)
        if args.regenerate:  # kept, so a failure after this point does not lose the pass
            save_partial(out / PARTIAL_ON, on)
    off = boot_and_record(args, binary, routes, principals, auth_off=True)
    # Stitch the two boots' columns in principal order.
    cols_on = iter(on["columns"])
    cols_off = iter(off["columns"])
    columns = [next(cols_off) if p["kind"] == "authoff" else next(cols_on) for p in principals]
    meta = {
        "what": "R0's allow/deny per (route, principal); A = past the authorization decision, "
        "D = 401/403 (record_r0_reach.py)",
        "r0_tag": tag,
        "r0_binary_sha256": manifest["r0_binary_sha256"],
        "store": "internal/books/testdata/r2corpus/overlay",
    }
    extra = {k: on[k] for k in ("tasks", "approvals", "census")}
    extra["canary"] = {**on["canary"], **off["canary"]}
    return principals_doc(principals), build_verdicts(meta, routes, principals, columns, extra)


PARTIAL_ON = "auth-on.partial.json"


def save_partial(path: pathlib.Path, doc: dict[str, Any]) -> None:
    """The auth-on pass as recorded (not committed): --reuse-auth-on reads it."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(doc, indent=1, sort_keys=True) + "\n", encoding="utf-8")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description="Record the R0 binary's reach (F4-07).")
    ap.add_argument("--regenerate", action="store_true", help="boot R0 and (re)write rbacreach/")
    ap.add_argument("--check", action="store_true", help="re-derive and diff; exit 1 on a diff")
    ap.add_argument("--list", action="store_true", help="list the principals")
    ap.add_argument("--corpus", default=str(OVERLAY_STORE), help="the store directory to copy")
    ap.add_argument("--routes", default=str(REACH_DIR / "routes.json"))
    ap.add_argument("--binary", help="use this R0 binary instead of the release asset")
    ap.add_argument("--cache", default="~/.cache/r2corpus")
    ap.add_argument("--docker", action="store_true")
    ap.add_argument("--workdir", default=w.DEFAULT_WORKDIR)
    ap.add_argument("--port", type=int, default=w.DEFAULT_PORT)
    ap.add_argument("--out", default=str(REACH_DIR), help="write the data here")
    ap.add_argument(
        "--reuse-auth-on",
        metavar="PATH",
        help="use the auth-on pass saved in PATH (auth-on.partial.json) and record only "
        "the auth-off boot",
    )
    ap.add_argument("--workers", type=int, default=8, help="principals probed in parallel")
    ap.add_argument("--settle-timeout", type=float, default=360.0)
    ap.add_argument(
        "--spread-source-ips",
        action=argparse.BooleanOptionalAction,
        default=sys.platform.startswith("linux"),
        help="send each probe from its own 127.x.y.z address, so R0's per-address "
        "rate limit never answers 429 (needs a loopback that owns all of 127/8: Linux)",
    )
    return ap


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.list:
        for p in principal_table():
            print(p["name"])
        return 0
    if not (args.regenerate or args.check):
        print("nothing to do: pass --list, --regenerate or --check", file=sys.stderr)
        return 2
    global SOURCES
    SOURCES = SourceIPs() if args.spread_source_ips else None
    try:
        pdoc, vdoc = record(args)
    except (w.CorpusError, RecorderError) as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1
    out = pathlib.Path(args.out)
    if args.check:
        committed = json.loads((out / "verdicts.json").read_text(encoding="utf-8"))
        diffs = diff_verdicts(committed, vdoc)
        if diffs:
            print("\n".join(diffs[:200]), file=sys.stderr)
            print(f"{len(diffs)} differing cell(s)", file=sys.stderr)
            return 1
        print(f"no diff: {vdoc['route_count']} routes x {len(vdoc['principals'])} principals")
        return 0
    write_json(out / "principals.json", pdoc)
    write_json(out / "verdicts.json", vdoc)
    print(f"wrote {vdoc['route_count']} routes x {len(vdoc['principals'])} principals to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
