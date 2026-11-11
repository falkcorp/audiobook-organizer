#!/usr/bin/env python3
# file: scripts/r2corpus/record_r0_answers.py
# version: 1.3.1
# guid: 53d96275-057a-4a73-ae6a-625b8f810a3a
# last-edited: 2026-11-10
"""Record the R0 binary's answers over R2's stage-1 fixture corpus (F2-12).

09 §9.3 item 1: after the corpus is written, the answers every R2 PR compares
against are recorded **by the R0 binary** booted on it: effective config,
schedules, search results and ABS responses (reach is F4-07's). This script:

1. copies the committed store directory to the writer's fixed work directory
   (never boots on the committed bytes: Pebble rewrites MANIFEST/OPTIONS on open),
2. boots R0 there with exactly the writer's boot spec (``write_r0_corpus.py``),
3. mints its own API key through R0's bootstrap exchange (the replay rule: a
   replay re-mints on its copy and substitutes it for ``<api-key:NAME>``),
4. sends a fixed request list per answer and writes one JSON file per answer under
   ``internal/books/testdata/r2corpus/answers/``, each holding its requests,
5. records the answer list in ``manifest.json``.

Secrets never reach an answer file: an API key's secret becomes
``<api-key:NAME>``, every other credential (ABS tokens, passwords) ``<redacted>``.

What a replay cannot reproduce is taken out and listed in ``manifest.json``'s
``volatile_fields`` (format coined here): values R0 computes from the clock
(``"<volatile>"`` in the answer) and the recorder's own artefacts (its API key
row, its ABS login session), which are dropped. The recorder waits for the
runs R0 dispatches at boot to finish before it sends its first request, and
sends its ABS playback requests last so no answer reads its own progress.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import pathlib
import re
import shutil
import sys
import time
from typing import Any
from urllib.parse import quote

_HERE = pathlib.Path(__file__).resolve().parent


def _load_writer():
    """Import write_r0_corpus.py by path (registered before exec, for dataclasses)."""
    name = "write_r0_corpus"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, _HERE / "write_r0_corpus.py")
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


w = _load_writer()

ANSWER_KEY = "r2-answers"  # the fixture name of the recorder's own key
REDACTED = "<redacted>"
VOLATILE = "<volatile>"
RECORDER_UA = "r2corpus-recorder/1.0"
RECORDER_DEVICE = {"clientName": "r2corpus-recorder", "deviceId": "recorder-001"}
# x-return-tokens is kept: it changes the ABS /login response (the tokens are in
# the body only when it is set), so a replay must send it too.
REQUEST_HEADERS_TO_KEEP = ("accept", "content-type", "user-agent", "prefer", "x-return-tokens")
RESPONSE_HEADERS_TO_KEEP = ("content-type", "accept-ranges", "etag", "cache-control")
SECRET_KEYS = frozenset(
    {
        "accesstoken",
        "refreshtoken",
        "token",
        "password",
        "apikey",
        "api_key",
        "secret",
        "jwtsecret",
        "session_id",
        "generated_password",
    }
)

# The search oracle's fixed query list (RE2 syntax, 2026-10-06 decision): titles,
# an author, a series-free miss, an anchored pattern, and the unfiltered list.
SEARCH_QUERIES = (
    "Knightfall",
    "Halloween",
    "Year One",
    "Selina Kyle",
    "Montoya",
    "Joker",
    "^The ",
    "",
)

# ABS identities the writer created (users accepted through invites).
ABS_USER = "dick.grayson"

# What a replay cannot reproduce (manifest.json "volatile_fields"). Found by
# recording the same corpus twice and diffing the answers, plus the fields R0
# computes from the clock at day granularity (a short diff cannot show them).
#
# A rule names an answer, a request ("METHOD /path", "*" = one path or query
# segment) and a path into the exchange's response body. Paths: dotted keys,
# [*] every element, [?field==JSON] the elements whose field equals JSON,
# [?field^=JSON] the elements whose string field starts with JSON. Actions:
# "normalize" writes "<volatile>" in place of the value; "drop" removes the
# matched list element (the recorder's own artefacts); "mask" (with target
# "request.path") writes "<volatile>" over each "*" segment of the recorded
# request path. Every rule must match at least once, or the recording fails:
# a rule that matches nothing is stale and proves nothing.
#
# Stored timestamps (created_at, a key's expires_at, the pause marker's since,
# ABS item times, root_dir books' updated_at) come from the store and are NOT
# volatile: a replay on a copy of the store reads the same values.
IMPORT_PREFIX = str(w.Layout(pathlib.Path(w.DEFAULT_WORKDIR)).import_dir) + "/"
_IMPORT_ROWS = f"data.items[?file_path^={json.dumps(IMPORT_PREFIX)}]"
_BOOT_RESCAN = (
    "the corpus' queued library scan runs at every boot (runs.standdown) and rescans the "
    "import root (a scan without force_update leaves root_dir out), stamping the import "
    "root's books with the boot's clock"
)
VOLATILE_FIELDS: tuple[dict[str, str], ...] = (
    {"answer": "schedules", "request": "GET /api/v1/tasks",
     "path": "data[?run_on_startup==true].last_run", "action": "normalize",
     "why": "a run_on_startup task runs at every boot, so its last run is the boot's clock"},
    {"answer": "schedules", "request": "GET /api/v1/maintenance-window/status",
     "path": "data.next_run_estimate", "action": "normalize",
     "why": "the next window start computed from the current date"},
    {"answer": "search", "request": "GET /api/v1/audiobooks?*",
     "path": f"{_IMPORT_ROWS}.updated_at", "action": "normalize", "why": _BOOT_RESCAN},
    {"answer": "search", "request": "GET /api/v1/audiobooks?*",
     "path": f"{_IMPORT_ROWS}.last_scan_mtime", "action": "normalize",
     "why": "the boot-time rescan stores each import-root file's mtime, which a checkout sets"},
    {"answer": "library", "request": "GET /api/v1/audiobooks?*",
     "path": f"{_IMPORT_ROWS}.updated_at", "action": "normalize", "why": _BOOT_RESCAN},
    {"answer": "identity", "request": "GET /api/v1/auth/api-keys?all=true",
     "path": "data.api_keys[?name==\"r2-answers\"]", "action": "drop",
     "why": "the recorder's own key, minted on the copy by the bootstrap exchange"},
    {"answer": "identity", "request": "GET /api/v1/auth/api-keys?all=true",
     "path": "data.count", "action": "normalize",
     "why": "counts the recorder's own key"},
    {"answer": "identity", "request": "GET /api/v1/auth/api-keys?all=true",
     "path": "data.api_keys[*].days_since_last_use", "action": "normalize",
     "why": "days between the key's stored last use and the current date"},
    {"answer": "abs", "request": "GET /api/me/sessions",
     "path": "sessions[?current==true]", "action": "drop",
     "why": "the recorder's own ABS login session (the one making the request)"},
    {"answer": "abs", "request": "GET /api/me/sessions",
     "path": "total", "action": "normalize",
     "why": "counts the recorder's own ABS login session"},
    {"answer": "abs", "request": "GET /api/libraries",
     "path": "libraries[*].createdAt", "action": "normalize",
     "why": "R0's ABS library view stamps the request time, not a stored time"},
    {"answer": "abs", "request": "GET /api/libraries",
     "path": "libraries[*].lastScan", "action": "normalize",
     "why": "R0's ABS library view stamps the request time, not a stored time"},
    {"answer": "abs", "request": "GET /api/libraries",
     "path": "libraries[*].lastUpdate", "action": "normalize",
     "why": "R0's ABS library view stamps the request time, not a stored time"},
    {"answer": "abs", "request": "GET /api/libraries",
     "path": "libraries[*].folders[*].addedAt", "action": "normalize",
     "why": "R0's ABS library view stamps the request time, not a stored time"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "id", "action": "normalize",
     "why": "the playback session the recorder opens gets a fresh ID"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "deviceInfo.id", "action": "normalize",
     "why": "the playback session the recorder opens gets a fresh ID"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "startedAt", "action": "normalize",
     "why": "the time the recorder opened the playback session"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "updatedAt", "action": "normalize",
     "why": "the time the recorder opened the playback session"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "date", "action": "normalize",
     "why": "the session's start date (abs/play.go: s.StartedAt at request time)"},
    {"answer": "abs", "request": "POST /api/items/*/play",
     "path": "dayOfWeek", "action": "normalize",
     "why": "the session's start weekday (abs/play.go: s.StartedAt at request time)"},
    {"answer": "abs", "request": "POST /api/session/*/sync",
     "target": "request.path", "path": "", "action": "mask",
     "why": "the playback session ID the recorder's /play just minted"},
    {"answer": "abs", "request": "POST /api/session/*/close",
     "target": "request.path", "path": "", "action": "mask",
     "why": "the playback session ID the recorder's /play just minted"},
)  # fmt: skip

_PATH_TOKEN = re.compile(r"\.?([A-Za-z0-9_]+)|\[\*\]|\[\?([A-Za-z0-9_]+)(==|\^=)(.+?)\]")


def parse_path(path: str) -> list[tuple[str, Any]]:
    """Tokens of a volatile-field path.

    ("key", k), ("all", None), ("where", (k, v)) for [?k==v], and
    ("prefix", (k, v)) for [?k^=v] (v must be a JSON string).
    """
    out: list[tuple[str, Any]] = []
    pos = 0
    while pos < len(path):
        m = _PATH_TOKEN.match(path, pos)
        if not m:
            raise ValueError(f"bad volatile-field path {path!r} at {pos}")
        if m.group(1):
            out.append(("key", m.group(1)))
        elif m.group(2):
            value = json.loads(m.group(4))
            if m.group(3) == "^=":
                if not isinstance(value, str):
                    raise ValueError(f"[?{m.group(2)}^=...] needs a JSON string in {path!r}")
                out.append(("prefix", (m.group(2), value)))
            else:
                out.append(("where", (m.group(2), value)))
        else:
            out.append(("all", None))
        pos = m.end()
    return out


def apply_rule(body: Any, tokens: list[tuple[str, Any]], action: str) -> int:
    """Normalize or drop what the tokens match in body (in place); returns hits."""
    if not tokens:
        return 0
    (kind, arg), rest = tokens[0], tokens[1:]
    hits = 0
    if kind == "key":
        if not isinstance(body, dict) or arg not in body:
            return 0
        if rest:
            return apply_rule(body[arg], rest, action)
        if action == "drop":
            del body[arg]
        else:
            body[arg] = VOLATILE
        return 1
    if not isinstance(body, list):
        return 0
    if kind == "all":
        idx = list(range(len(body)))
    elif kind == "prefix":
        field, want = arg
        idx = [
            i
            for i, e in enumerate(body)
            if isinstance(e, dict) and isinstance(e.get(field), str) and e[field].startswith(want)
        ]
    else:
        field, want = arg
        idx = [i for i, e in enumerate(body) if isinstance(e, dict) and e.get(field) == want]
    if rest:
        return sum(apply_rule(body[i], rest, action) for i in idx)
    for i in reversed(idx):
        if action == "drop":
            del body[i]
        else:
            body[i] = VOLATILE
        hits += 1
    return hits


def _request_regex(pattern: str) -> tuple[str, re.Pattern[str]]:
    want_method, want_path = pattern.split(" ", 1)
    regex = "^" + re.escape(want_path).replace(r"\*", "([^/?]+)") + "$"
    return want_method, re.compile(regex)


def _request_matches(pattern: str, method: str, path: str) -> bool:
    want_method, regex = _request_regex(pattern)
    return method == want_method and regex.match(path) is not None


def mask_path(pattern: str, path: str) -> str:
    """The request path with each segment the pattern's "*" matched as <volatile>."""
    _, regex = _request_regex(pattern)
    m = regex.match(path)
    if not m:
        return path
    out, last = [], 0
    for i in range(1, (m.lastindex or 0) + 1):
        out += [path[last : m.start(i)], VOLATILE]
        last = m.end(i)
    return "".join(out) + path[last:]


def normalize_answers(
    files: dict[str, dict[str, Any]], rules: tuple[dict[str, str], ...] = VOLATILE_FIELDS
) -> None:
    """Apply the volatile-field rules to the recorded exchanges; every rule must hit.

    Response rules run first and request-path masks last, so every response
    rule matches against the path as it was sent.
    """
    ordered = sorted(rules, key=lambda r: r.get("target") == "request.path")
    for rule in ordered:
        hits = 0
        for ex in files[rule["answer"]]["exchanges"]:
            req = ex["request"]
            if not _request_matches(rule["request"], req["method"], req["path"]):
                continue
            if rule.get("target") == "request.path":
                if rule["action"] != "mask":
                    raise w.CorpusError(f"a request.path rule must mask: {rule}")
                req["path"] = mask_path(rule["request"], req["path"])
                hits += 1
            else:
                hits += apply_rule(ex["response"]["body"], parse_path(rule["path"]), rule["action"])
        if hits == 0:
            raise w.CorpusError(f"volatile-field rule matched nothing (stale rule): {rule}")


def placeholder(name: str) -> str:
    return f"<api-key:{name}>"


def redact(value: Any, keys: dict[str, str]) -> Any:
    """Replace every credential in a JSON value, preserving types.

    keys maps a raw abk_ secret to its fixture name; any abk_ string becomes the
    placeholder (``<api-key:unknown>`` if the name is not known).
    """
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if isinstance(v, str) and w.ABK_RE.search(v):
                out[k] = _sub_keys(v, keys)
            elif isinstance(v, str) and k.lower() in SECRET_KEYS and v:
                out[k] = REDACTED
            else:
                out[k] = redact(v, keys)
        return out
    if isinstance(value, list):
        return [redact(v, keys) for v in value]
    if isinstance(value, str):
        return _sub_keys(value, keys)
    return value


def _sub_keys(s: str, keys: dict[str, str]) -> str:
    return w.ABK_RE.sub(lambda m: placeholder(keys.get(m.group(0), "unknown")), s)


def recorded_request_headers(auth: str, extra: dict[str, str] | None) -> dict[str, str]:
    hdrs = {"accept": "application/json", "user-agent": RECORDER_UA}
    for k, v in (extra or {}).items():
        if k.lower() in REQUEST_HEADERS_TO_KEEP:
            hdrs[k.lower()] = v
    if auth.startswith("key:"):
        hdrs["authorization"] = f"Bearer {placeholder(auth[4:])}"
    elif auth.startswith("abs:"):
        hdrs["authorization"] = f"Bearer {REDACTED}"
    return hdrs


class Recorder:
    def __init__(self, client: Any) -> None:
        self.client = client
        self.files: dict[str, dict[str, Any]] = {}

    def keymap(self) -> dict[str, str]:
        return {raw: name for name, raw in self.client.keys.items()}

    def send(
        self,
        answer: str,
        method: str,
        path: str,
        body: Any = None,
        auth: str = f"key:{ANSWER_KEY}",
        headers: dict[str, str] | None = None,
    ) -> Any:
        r = self.client.request(method, path, body, auth=auth, headers=headers)
        keys = self.keymap()
        self.files[answer]["exchanges"].append(
            {
                "request": {
                    "method": method,
                    "path": path,
                    "headers": recorded_request_headers(auth, headers),
                    "body": redact(body, keys),
                },
                "response": {
                    "status": r.status,
                    "headers": {
                        k: v for k, v in r.headers.items() if k in RESPONSE_HEADERS_TO_KEEP
                    },
                    "body": redact(r.body, keys),
                },
            }
        )
        return r

    def start(self, name: str, what: str) -> None:
        self.files[name] = {"name": name, "what": what, "exchanges": []}


ANSWERS = (
    ("effective_config", "R0's own view of every config key: GET /api/v1/config (stored, "
     "YAML and env layers applied; env_locked and setting_locks name the operator layers)"),
    ("schedules", "R0's task listing, maintenance-window status and executor pause state"),
    ("search", "GET /api/v1/audiobooks?search= over a fixed query list (the search oracle)"),
    ("abs", "the ABS conformance requests of scripts/abs_capture_fixtures.py on the corpus"),
    ("identity", "users, invites and API keys as R0 lists them (input of the M3 checks)"),
    ("library", "books, playlists and import roots as R0 lists them. No run listing: "
     "booting the copy re-queues and dispatches the corpus' quiesced and queued scans, "
     "so runs listed on the copy are boot-time transitions under fresh run IDs"),
)  # fmt: skip


def record_all(rec: Recorder, client: Any) -> None:
    what = dict(ANSWERS)
    rec.start("effective_config", what["effective_config"])
    rec.send("effective_config", "GET", "/api/v1/config")

    rec.start("schedules", what["schedules"])
    rec.send("schedules", "GET", "/api/v1/tasks")
    rec.send("schedules", "GET", "/api/v1/maintenance-window/status")
    rec.send("schedules", "GET", "/api/v1/operations/pause")

    rec.start("search", what["search"])
    for q in SEARCH_QUERIES:
        rec.send(
            "search",
            "GET",
            f"/api/v1/audiobooks?search={quote(q)}&limit=100&sort_by=title&sort_order=asc",
        )

    rec.start("identity", what["identity"])
    rec.send("identity", "GET", "/api/v1/users")
    rec.send("identity", "GET", "/api/v1/users/invites")
    rec.send("identity", "GET", "/api/v1/auth/api-keys?all=true")

    rec.start("library", what["library"])
    rec.send("library", "GET", "/api/v1/audiobooks?limit=100&sort_by=title&sort_order=asc")
    rec.send("library", "GET", "/api/v1/playlists")
    rec.send("library", "GET", "/api/v1/import-paths")

    record_abs(rec, client)


def record_abs(rec: Recorder, client: Any) -> None:
    """The request list of scripts/abs_capture_fixtures.py, on the corpus."""
    a = "abs"
    rec.start(a, dict(ANSWERS)[a])
    for path in ("/ping", "/status"):
        rec.send(a, "GET", path, auth="none")
    login_body = {"username": ABS_USER, "password": w.OTHER_PASSWORD, "deviceInfo": RECORDER_DEVICE}
    r = rec.send(a, "POST", "/login", login_body, auth="none", headers={"x-return-tokens": "true"})
    user = (r.body or {}).get("user", {}) if isinstance(r.body, dict) else {}
    token = user.get("accessToken") or user.get("token")
    if r.status != 200 or not token:
        raise w.CorpusError(f"ABS login on the copy failed: {r.status}")
    client.bearers["robin"] = token
    auth = "abs:robin"
    progressed: set[str] = set()
    for path in ("/api/me", "/api/me/progress", "/api/me/sessions", "/api/libraries"):
        rr = rec.send(a, "GET", path, auth=auth)
        if path == "/api/me" and isinstance(rr.body, dict):
            progressed = {str(mp.get("libraryItemId")) for mp in rr.body.get("mediaProgress") or []}
    libs = client.request("GET", "/api/libraries", auth=auth).body.get("libraries", [])
    item_id = None
    for lib in libs:
        lid = lib["id"]
        for suffix in (
            "items?limit=10&page=0",
            "personalized",
            "series",
            "authors",
            "narrators",
            "search?q=Knightfall",
            "filterdata",
        ):
            rr = rec.send(a, "GET", f"/api/libraries/{lid}/{suffix}", auth=auth)
            if suffix.startswith("items") and item_id is None and rr.status == 200:
                item_id = pick_abs_item(rr.body.get("results", []), progressed)
    if item_id is None:
        raise w.CorpusError("the ABS library on the copy has no item")
    rec.send(a, "GET", f"/api/items/{item_id}?expanded=1&include=progress", auth=auth)
    rec.send(a, "GET", f"/api/me/progress/{item_id}", auth=auth)
    rec.send(a, "GET", f"/api/me/bookmarks/{item_id}", auth=auth)
    # Last, so no recorded answer reads the recorder's own progress or session.
    play_body = {"deviceInfo": RECORDER_DEVICE, "mediaPlayer": "unknown", "forceDirectPlay": True}
    play = rec.send(a, "POST", f"/api/items/{item_id}/play", play_body, auth=auth)
    if play.status != 200 or not isinstance(play.body, dict) or not play.body.get("id"):
        raise w.CorpusError(f"ABS play on the copy failed: {play.status}")
    sid = play.body["id"]
    sync = {"currentTime": 0.1, "timeListened": 0.1, "duration": 0.3}
    rec.send(a, "POST", f"/api/session/{sid}/sync", sync, auth=auth)
    rec.send(a, "POST", f"/api/session/{sid}/close", None, auth=auth)


# Run states that are not finished (operations v3 view and v2 status names).
_ACTIVE_STATES = frozenset({"queued", "pending", "claimed", "running", "stopping", "cancelling"})


DEFAULT_SETTLE_TIMEOUT = 300.0


def wait_for_boot_runs(
    client: Any, auth: str, timeout: float = DEFAULT_SETTLE_TIMEOUT
) -> list[dict[str, Any]]:
    """Wait until no run is active; returns the runs as listed then.

    Booting the copy re-queues the corpus' quiesced scan (the resume sweep) and
    dispatches its queued scan, and run_on_startup tasks run; an answer recorded
    while they run would race them. Settled = two listings 2 s apart agree and
    neither shows an active run. A run waiting on its dependencies (F2-13's
    overlay) is dispatched only by the registry's deps sweep, a fixed 5 minutes
    after boot at the R0 tag, so a corpus holding one needs a timeout above
    that (``--settle-timeout``).
    """
    deadline = time.monotonic() + timeout
    prev = None
    while True:
        r = client.request("GET", "/api/v3/ops/runs?limit=200", auth=auth)
        if r.status != 200 or not isinstance(r.body, dict):
            raise w.CorpusError(f"GET /api/v3/ops/runs on the copy -> {r.status}")
        runs = r.body.get("items") or []
        snap = sorted((str(x.get("id")), str(x.get("state"))) for x in runs)
        active = [s for s in snap if s[1] in _ACTIVE_STATES]
        if not active and snap == prev:
            return runs
        if time.monotonic() > deadline:
            changing = "" if snap == prev else " (the run listing was still changing)"
            raise w.CorpusError(
                f"boot-time runs did not settle in {timeout:.0f} s: {active}{changing}"
            )
        prev = snap
        time.sleep(2.0)


def pick_abs_item(results: list[dict[str, Any]], progressed: set[str]) -> str | None:
    """The item the per-item requests read: one the user has progress on.

    The ABS items list serves root_dir only (one Knightfall, the organized
    copy) and reports ``media.numAudioFiles`` as 0 for every item, so the old
    "first multi-file item" choice never matched and fell back to the first
    item (The Long Halloween), on which the user has no progress: the progress
    request answered 404. The writer stored progress and a bookmark on
    Knightfall, which is in the user's mediaProgress, so that is the pick. With
    no progressed item in the list, the first item is used.
    """
    for res in results:
        if str(res.get("id")) in progressed:
            return str(res["id"])
    return str(results[0]["id"]) if results else None


def mint_answer_key(ctx: Any) -> None:
    """Re-mint the recorder's key on the copy through R0's bootstrap exchange."""
    token_file = ctx.layout.state / ".bootstrap-token"
    deadline = time.monotonic() + 60.0
    while not token_file.exists():
        if time.monotonic() > deadline:
            raise w.CorpusError("R0 wrote no .bootstrap-token on the copy")
        time.sleep(0.5)
    time.sleep(0.5)
    r = ctx.client.request(
        "POST",
        "/api/v1/auth/bootstrap",
        {"token": token_file.read_text(encoding="utf-8").strip(), "key_name": ANSWER_KEY},
        auth="none",
    )
    if r.status != 200:
        raise w.CorpusError(f"bootstrap on the copy -> {r.status}: {r.body}")
    ctx.client.keys[ANSWER_KEY] = r.body["data"]["api_key"]


def touch_like_a_checkout(root: pathlib.Path) -> None:
    """Give every copied file a fresh mtime, as a git checkout of the corpus does.

    A replay boots on a checkout, where no file keeps the mtime it had when the
    writer ran; copytree keeps them. The corpus' queued library scan runs at
    every boot and reads file mtimes into book rows, so recording on preserved
    mtimes would hide fields a replay cannot reproduce. With fresh mtimes those
    fields differ between two recordings, which is how VOLATILE_FIELDS finds them.
    """
    now = time.time()
    for p in root.rglob("*"):
        if p.is_file() and not p.is_symlink():
            os.utime(p, (now, now))


def check_answers_still_valid(manifest: dict[str, Any], now: Any = None) -> None:
    """Refuse to record once an answer-visible row of the corpus has lapsed.

    Past ``lapses.answers_valid_until`` the store's API keys (or its open
    invite, or its ABS sessions) have expired, so R0 would answer differently
    from the day the corpus was written: regenerate the corpus instead.
    """
    until = (manifest.get("lapses") or {}).get("answers_valid_until")
    if not until:
        raise w.CorpusError(
            "manifest.json has no lapses.answers_valid_until: regenerate the corpus"
        )
    now = now or w._dt.datetime.now(w._dt.timezone.utc)
    limit = w._dt.datetime.strptime(until, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=w._dt.timezone.utc)
    if now >= limit:
        raise w.CorpusError(
            f"the corpus' rows lapsed at {until} (manifest lapses): regenerate the corpus "
            "with write_r0_corpus.py --regenerate before recording answers"
        )


def scan_answers(answers_dir: pathlib.Path) -> list[str]:
    bad = []
    for p in sorted(answers_dir.glob("*.json")):
        if w.ABK_RE.search(p.read_text(encoding="utf-8")):
            bad.append(p.name)
    return bad


def record(args: argparse.Namespace) -> int:
    corpus_store = pathlib.Path(args.corpus).resolve()
    manifest_path = corpus_store.parent / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    check_answers_still_valid(manifest)
    tag = args.tag or w.read_r0_tag()
    if manifest["r0_tag"] != tag:
        raise w.CorpusError(f"manifest r0_tag {manifest['r0_tag']} != {tag}")
    # --binary-tag: a replay by another release (F2-15's pre-freeze binary over
    # a corpus copy), whose answers are labelled with that tag, never R0's, and
    # never written over the committed answers/.
    foreign = args.allow_foreign_binary or bool(args.binary_tag)
    if args.binary_tag and not args.binary:
        raise w.CorpusError("--binary-tag needs --binary")
    if args.binary:
        binary = pathlib.Path(args.binary).resolve()
    else:
        binary = w.fetch_binary(tag, pathlib.Path(args.cache).expanduser())
    if not args.docker:
        w.check_native_binary(binary, foreign)
    if not foreign and w.sha256_file(binary) != manifest["r0_binary_sha256"]:
        raise w.CorpusError("this binary is not the one that wrote the corpus")
    answers_dir = pathlib.Path(args.out).resolve() if args.out else w.ANSWERS_DIR
    if foreign and answers_dir == w.ANSWERS_DIR:
        raise w.CorpusError("--allow-foreign-binary and --binary-tag record elsewhere: give --out")
    if args.binary_tag:
        tag = args.binary_tag

    layout = w.Layout(pathlib.Path(args.workdir))
    if layout.workdir.exists():
        shutil.rmtree(layout.workdir)
    shutil.copytree(corpus_store, layout.store)  # a copy: the committed bytes stay
    touch_like_a_checkout(layout.store)
    layout.home.mkdir(parents=True, exist_ok=True)
    layout.viper_yaml.write_text(w._yaml_dump(w.VIPER_YAML), encoding="utf-8")

    window, extra_env = w.MAINTENANCE_WINDOW_UTC, {}
    if args.shift_window or args.window_start is not None:
        window = w.shifted_window(start=args.window_start)
        extra_env = w.shifted_window_env(window)
    w.check_outside_maintenance_window(window=window)
    proc = w.R0Process(binary, layout, args.port, docker=args.docker, extra_env=extra_env)
    ctx = w.Ctx(client=w.Client(proc.base, user_agent=RECORDER_UA), proc=proc, layout=layout)
    rec = Recorder(ctx.client)
    proc.start()
    try:
        mint_answer_key(ctx)
        settled = wait_for_boot_runs(ctx.client, f"key:{ANSWER_KEY}", timeout=args.settle_timeout)
        print(f"boot-time runs settled ({len(settled)} runs listed)")
        record_all(rec, ctx.client)
    finally:
        proc.stop()
    normalize_answers(rec.files)

    if answers_dir.exists():
        shutil.rmtree(answers_dir)
    answers_dir.mkdir(parents=True)
    entries = []
    for name, what in ANSWERS:
        data = rec.files[name]
        data["r0_tag"] = tag
        (answers_dir / f"{name}.json").write_text(
            json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        rel = (
            (answers_dir / f"{name}.json").relative_to(w.REPO_ROOT).as_posix()
            if (answers_dir.is_relative_to(w.REPO_ROOT))
            else f"answers/{name}.json"
        )
        entries.append({"name": name, "file": rel, "what": what})
    bad = scan_answers(answers_dir)
    if bad:
        raise w.CorpusError(f"an API key secret reached an answer file: {bad}")
    manifest["answers"] = entries
    manifest["volatile_fields"] = [dict(r) for r in VOLATILE_FIELDS]
    if answers_dir == w.ANSWERS_DIR:
        w.write_manifest(manifest, manifest_path)
    print(f"wrote {len(entries)} answer files to {answers_dir}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="Record the R0 binary's answers over the stage-1 corpus (F2-12).",
    )
    ap.add_argument(
        "--regenerate",
        action="store_true",
        help="boot R0 on a copy of the corpus and (re)write answers/",
    )
    ap.add_argument("--list", action="store_true", help="list the answers and their requests")
    ap.add_argument("--corpus", default=str(w.STAGE1_DIR), help="the store directory to copy")
    ap.add_argument("--tag", help="override ci/r0_release.txt (a re-cut only)")
    ap.add_argument("--binary", help="use this R0 binary instead of downloading the release asset")
    ap.add_argument("--cache", default="~/.cache/r2corpus")
    ap.add_argument("--docker", action="store_true")
    ap.add_argument("--workdir", default=w.DEFAULT_WORKDIR)
    ap.add_argument("--port", type=int, default=w.DEFAULT_PORT)
    ap.add_argument("--out", help="write answers here instead of the committed answers/")
    ap.add_argument(
        "--settle-timeout",
        type=float,
        default=DEFAULT_SETTLE_TIMEOUT,
        help="seconds to wait for the boot-time runs to settle (a corpus with a run waiting on "
        "its dependencies needs more than R0's 5-minute deps sweep)",
    )
    ap.add_argument(
        "--binary-tag",
        help="label the answers with this release tag: a replay by another release's --binary "
        "(F2-15's pre-freeze binary); needs --out",
    )
    ap.add_argument(
        "--shift-window",
        action="store_true",
        help="boot in a time zone that puts the corpus' maintenance window twelve hours from now "
        "(write_r0_corpus.shifted_window_env), so the replay runs at any hour with no window task",
    )
    ap.add_argument(
        "--window-start",
        type=int,
        help="like --shift-window, with the window's UTC start hour given (a caller that boots "
        "several times picks one, so every boot shares one time zone)",
    )
    ap.add_argument(
        "--allow-foreign-binary",
        action="store_true",
        help="development only: a non-linux/amd64 R0 build; needs --out",
    )
    return ap  # fmt: skip


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.list:
        for name, what in ANSWERS:
            print(f"{name}: {what}")
        return 0
    if not args.regenerate:
        print("nothing to do: pass --list, or --regenerate to re-record", file=sys.stderr)
        return 2
    try:
        return record(args)
    except w.CorpusError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
