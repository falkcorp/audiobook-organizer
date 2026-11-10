#!/usr/bin/env python3
# file: scripts/r2corpus/record_r0_answers.py
# version: 1.0.1
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
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
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
REQUEST_HEADERS_TO_KEEP = ("accept", "content-type", "user-agent", "prefer")
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
    hdrs = {"accept": "application/json", "user-agent": "r2corpus-recorder/1.0"}
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
    ("library", "books, playlists and recent runs as R0 lists them"),
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
    login_body = {"username": ABS_USER, "password": w.OTHER_PASSWORD}
    r = rec.send(a, "POST", "/login", login_body, auth="none", headers={"x-return-tokens": "true"})
    user = (r.body or {}).get("user", {}) if isinstance(r.body, dict) else {}
    token = user.get("accessToken") or user.get("token")
    if r.status != 200 or not token:
        raise w.CorpusError(f"ABS login on the copy failed: {r.status}")
    client.bearers["robin"] = token
    auth = "abs:robin"
    for path in ("/api/me", "/api/me/progress", "/api/me/sessions", "/api/libraries"):
        rec.send(a, "GET", path, auth=auth)
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
                for res in rr.body.get("results", []):
                    if (res.get("media") or {}).get("numAudioFiles", 0) > 1:
                        item_id = res["id"]
                        break
                if item_id is None and rr.body.get("results"):
                    item_id = rr.body["results"][0]["id"]
    if item_id is None:
        raise w.CorpusError("the ABS library on the copy has no item")
    rec.send(a, "GET", f"/api/items/{item_id}?expanded=1&include=progress", auth=auth)
    rec.send(a, "GET", f"/api/me/progress/{item_id}", auth=auth)
    rec.send(a, "GET", f"/api/me/bookmarks/{item_id}", auth=auth)
    play_body = {
        "deviceInfo": {"clientName": "r2corpus-recorder", "deviceId": "recorder-001"},
        "mediaPlayer": "unknown",
        "forceDirectPlay": True,
    }
    play = rec.send(a, "POST", f"/api/items/{item_id}/play", play_body, auth=auth)
    if play.status == 200 and isinstance(play.body, dict) and play.body.get("id"):
        sid = play.body["id"]
        sync = {"currentTime": 0.1, "timeListened": 0.1, "duration": 0.3}
        rec.send(a, "POST", f"/api/session/{sid}/sync", sync, auth=auth)
        rec.send(a, "POST", f"/api/session/{sid}/close", None, auth=auth)
    rec.send(a, "GET", "/api/me", auth=auth)


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
    tag = args.tag or w.read_r0_tag()
    if manifest["r0_tag"] != tag:
        raise w.CorpusError(f"manifest r0_tag {manifest['r0_tag']} != {tag}")
    if args.binary:
        binary = pathlib.Path(args.binary).resolve()
    else:
        binary = w.fetch_binary(tag, pathlib.Path(args.cache).expanduser())
    if not args.docker:
        w.check_native_binary(binary, args.allow_foreign_binary)
    if not args.allow_foreign_binary and w.sha256_file(binary) != manifest["r0_binary_sha256"]:
        raise w.CorpusError("this binary is not the one that wrote the corpus")
    answers_dir = pathlib.Path(args.out).resolve() if args.out else w.ANSWERS_DIR
    if args.allow_foreign_binary and answers_dir == w.ANSWERS_DIR:
        raise w.CorpusError("--allow-foreign-binary is for development: give --out")

    layout = w.Layout(pathlib.Path(args.workdir))
    if layout.workdir.exists():
        shutil.rmtree(layout.workdir)
    shutil.copytree(corpus_store, layout.store)  # a copy: the committed bytes stay
    layout.home.mkdir(parents=True, exist_ok=True)
    layout.viper_yaml.write_text(w._yaml_dump(w.VIPER_YAML), encoding="utf-8")

    proc = w.R0Process(binary, layout, args.port, docker=args.docker)
    ctx = w.Ctx(client=w.Client(proc.base), proc=proc, layout=layout)
    rec = Recorder(ctx.client)
    proc.start()
    try:
        mint_answer_key(ctx)
        record_all(rec, ctx.client)
    finally:
        proc.stop()

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
