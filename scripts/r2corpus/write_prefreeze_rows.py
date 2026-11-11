#!/usr/bin/env python3
# file: scripts/r2corpus/write_prefreeze_rows.py
# version: 1.1.0
# guid: 5e8b2d14-9a7c-4f03-b6e1-2c4d8f0a7b39
# last-edited: 2026-11-10
"""Write a few rows into a corpus copy with the pre-freeze release's binary (F2-15).

Design rule 10 of F2-15: production upgrades to R2 from a store the pre-freeze
release has booted and written to, never from a raw R0 store. The cutover
suite's ``prefreezeWrittenCorpus`` helper (internal/books/cutover_suite_test.go)
calls this script to make such a store. It:

1. copies the corpus store (F2-12's stage 1 with F2-13's overlay) into the
   writer's fixed work directory, as the recorder does (every path the store
   holds points inside it), never touching the committed bytes;
2. boots the given binary there with the writer's boot spec, the maintenance
   window moved twelve hours from now, or to ``--window-start`` (``write_r0_corpus.shifted_window``), so
   no window task runs while it is driven, on the given port;
3. writes, **only through the binary's API**, after re-minting a key through
   the bootstrap exchange (the replay rule: never a recorded secret):
   one user (an invite accepted), one API key, one setting change through
   ``PUT /api/v1/config``, and one library scan of the corpus' import root,
   started and waited for to finish (the corpus leaves the executor paused, so
   the scan runs between a resume and a pause with the corpus' own reason);
4. stops it cleanly and copies the store directory to ``--out``.

What it wrote is printed as JSON on stdout (names and IDs, never secrets), so
the Go helper can check those rows survive every test.

Usage:
    write_prefreeze_rows.py --binary <path> --corpus <store dir> --out <dir> [--port N]
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

_HERE = pathlib.Path(__file__).resolve().parent


def _load_writer():
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

WRITER_KEY = "prefreeze-writer"
# Batman fixtures only (public repository).
NEW_USER = "harvey.dent"
NEW_USER_EMAIL = "two-face@example.com"
NEW_USER_ROLE = "viewer"
NEW_KEY_NAME = "prefreeze-gcpd-key"
NEW_KEY_SCOPES = ["library.view"]
# One plain stored setting, changed from the corpus' value through the config route.
SETTING = "auto_scan_debounce_seconds"
PAUSE_REASON = "r2corpus: paused executor"  # the corpus' own (write_r0_corpus.py)
SCAN_TIMEOUT = 180.0


def mint_key(ctx: Any) -> None:
    token_file = ctx.layout.state / ".bootstrap-token"
    deadline = time.monotonic() + 60.0
    while not token_file.exists():
        if time.monotonic() > deadline:
            raise w.CorpusError("the binary wrote no .bootstrap-token on the copy")
        time.sleep(0.5)
    time.sleep(0.5)
    r = ctx.client.request(
        "POST", "/api/v1/auth/bootstrap",
        {"token": token_file.read_text(encoding="utf-8").strip(), "key_name": WRITER_KEY},
        auth="none",
    )  # fmt: skip
    if r.status != 200:
        raise w.CorpusError(f"bootstrap on the copy -> {r.status}: {r.body}")
    ctx.client.keys[WRITER_KEY] = r.body["data"]["api_key"]
    ctx.ids["writer_key_id"] = r.body["data"].get("key_id", "")


def call(
    ctx: Any, method: str, path: str, body: Any = None, expect=(200, 201, 204), auth: str = ""
) -> Any:
    r = ctx.client.request(method, path, body, auth=auth or f"key:{WRITER_KEY}")
    if r.status not in expect:
        raise w.CorpusError(f"{method} {path} -> {r.status}: {json.dumps(r.body)[:400]}")
    return r.body


def write_rows(ctx: Any) -> dict[str, Any]:
    out: dict[str, Any] = {}
    # The binary refuses user and key management to an API key ("sign in to do
    # this"), so those two go through a web session of the corpus' admin, whose
    # password is write_r0_corpus.py's published fixture value (a generated
    # fixture, not a recorded secret). The rest uses the re-minted key.
    w.login(ctx, "admin", "admin", w.FIXTURE_PASSWORD)
    inv = call(ctx, "POST", "/api/v1/users/invite", {
        "username": NEW_USER, "role_id": NEW_USER_ROLE, "email": NEW_USER_EMAIL, "expires_in": 24,
    }, auth="admin")  # fmt: skip
    acc = call(
        ctx,
        "POST",
        "/api/v1/auth/accept-invite",
        {"token": inv["data"]["token"], "password": w.FIXTURE_PASSWORD},
        auth="harvey",
    )
    out["user"] = {"username": NEW_USER, "id": acc["data"]["user"]["id"]}

    key = call(
        ctx,
        "POST",
        "/api/v1/auth/api-keys",
        {"name": NEW_KEY_NAME, "scopes": NEW_KEY_SCOPES, "expires_in_days": 30},
        auth="admin",
    )
    out["api_key"] = {"name": NEW_KEY_NAME, "id": w.dig(key, "data.id")}

    cfg = call(ctx, "GET", "/api/v1/config")
    before = (
        w.dig(cfg, f"data.config.{SETTING}")
        if "config" in cfg.get("data", {})
        else cfg["data"].get(SETTING)
    )
    value = int(before or 0) + 1
    call(ctx, "PUT", "/api/v1/config", {SETTING: value})
    out["setting"] = {"name": SETTING, "before": before, "after": value}

    call(ctx, "POST", "/api/v1/operations/resume", {})
    try:
        r = call(
            ctx,
            "POST",
            "/api/v1/operations/v2",
            {"def_id": "library.scan", "params": {"folder_path": str(ctx.layout.import_dir)}},
            expect=(200, 201, 202),
        )
        op = str(w.dig(r, "op_id"))
        deadline = time.monotonic() + SCAN_TIMEOUT
        while True:
            st = w._status(call(ctx, "GET", f"/api/v1/operations/v2/{op}"))
            if st in w.TERMINAL:
                break
            if time.monotonic() > deadline:
                raise w.CorpusError(
                    f"scan {op} did not finish within {SCAN_TIMEOUT:.0f}s (last {st!r})"
                )
            time.sleep(0.5)
        if st not in ("completed", "succeeded"):
            raise w.CorpusError(f"scan {op} ended {st!r}")
        out["scan"] = {"op_id": op, "status": st, "folder": str(ctx.layout.import_dir)}
    finally:
        call(ctx, "POST", "/api/v1/operations/pause", {"reason": PAUSE_REASON})
    return out


def run(args: argparse.Namespace) -> dict[str, Any]:
    corpus = pathlib.Path(args.corpus).resolve()
    out_dir = pathlib.Path(args.out).resolve()
    if out_dir.exists():
        raise w.CorpusError(f"{out_dir} exists: --out must be a new directory")
    binary = pathlib.Path(args.binary).resolve()
    layout = w.Layout(pathlib.Path(args.workdir))
    if layout.workdir.exists():
        shutil.rmtree(layout.workdir)
    shutil.copytree(corpus, layout.store)  # a copy: the committed bytes stay
    layout.home.mkdir(parents=True, exist_ok=True)
    layout.viper_yaml.write_text(w._yaml_dump(w.VIPER_YAML), encoding="utf-8")
    window = w.shifted_window(start=args.window_start)
    w.check_outside_maintenance_window(window=window)
    proc = w.R0Process(binary, layout, args.port, extra_env=w.shifted_window_env(window))
    ctx = w.Ctx(
        client=w.Client(proc.base, user_agent="r2corpus-prefreeze/1.0"), proc=proc, layout=layout
    )
    proc.start()
    try:
        mint_key(ctx)
        wrote = write_rows(ctx)
    finally:
        proc.stop()
    shutil.copytree(layout.store, out_dir)
    return wrote


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--binary", required=True, help="the pre-freeze release's binary for this host")
    ap.add_argument("--corpus", required=True, help="the corpus store directory to copy")
    ap.add_argument("--out", required=True, help="a new directory for the written store")
    ap.add_argument("--workdir", default=w.DEFAULT_WORKDIR)
    ap.add_argument(
        "--window-start",
        type=int,
        help="the maintenance window's UTC start hour for the boot (default: twelve hours from now)",
    )
    ap.add_argument("--port", type=int, default=w.DEFAULT_PORT)
    args = ap.parse_args(argv)
    try:
        wrote = run(args)
    except w.CorpusError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1
    print(json.dumps(wrote, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
