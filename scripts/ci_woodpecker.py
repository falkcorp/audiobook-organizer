#!/usr/bin/env python3
# file: scripts/ci_woodpecker.py
# version: 1.0.0
# guid: 7d41c2e8-5a93-4f16-b0e7-2c8a9f3d6b51
# last-edited: 2026-09-26
"""Run the current commit through Woodpecker CI and wait for the verdict.

This is how `make ci` work is offloaded from the developer Mac onto the CI
agents (the prod host's docker agent, llm1, and the Mac's local agent; see
docs/ci/woodpecker.md). It:

1. pushes HEAD to its branch on origin (explicit SHA, lease-checked),
2. starts a manual pipeline for that branch through the Woodpecker API, which
   does not depend on the GitHub webhook getting through Cloudflare Access,
3. polls until the pipeline finishes and checks the pipeline ran the SHA that
   was pushed, and
4. prints every workflow and step, with the tail of each failed step's log,
   and exits 0 only if the pipeline succeeded.

The server URL and API token are never in the repo. They come from
WOODPECKER_URL / WOODPECKER_TOKEN, or from the gitignored file
.claude/.credentials/woodpecker-api.env (KEY=VALUE lines) in the primary
checkout. Use the server's LAN address: the public hostname sits behind
Cloudflare Access, which the API client cannot pass.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

CRED_REL = Path(".claude/.credentials/woodpecker-api.env")
TERMINAL = {"success", "failure", "error", "killed", "declined", "blocked", "skipped"}


def git(*args: str) -> str:
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout.strip()


def load_config() -> tuple[str, str]:
    url, token = os.environ.get("WOODPECKER_URL", ""), os.environ.get("WOODPECKER_TOKEN", "")
    if not (url and token):
        # The credentials file lives in the primary checkout, shared by worktrees.
        common = Path(git("rev-parse", "--path-format=absolute", "--git-common-dir"))
        for base in (Path(git("rev-parse", "--show-toplevel")), common.parent):
            f = base / CRED_REL
            if f.is_file():
                for line in f.read_text().splitlines():
                    k, _, v = line.strip().partition("=")
                    if k == "WOODPECKER_URL" and not url:
                        url = v
                    elif k == "WOODPECKER_TOKEN" and not token:
                        token = v
                break
    if not (url and token):
        sys.exit(f"ci-woodpecker: set WOODPECKER_URL and WOODPECKER_TOKEN, or create {CRED_REL}")
    return url.rstrip("/"), token


class API:
    def __init__(self, url: str, token: str):
        self.url, self.token = url, token

    def call(self, method: str, path: str, body: dict | None = None) -> Any:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.url + "/api" + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + self.token)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                raw = r.read()
        except urllib.error.HTTPError as e:
            sys.exit(f"ci-woodpecker: {method} {path}: HTTP {e.code}: {e.read()[:300]!r}")
        return json.loads(raw) if raw else None


def repo_slug() -> str:
    remote = git("remote", "get-url", "origin")
    slug = remote.split("github.com")[-1].lstrip(":/")
    return slug[:-4] if slug.endswith(".git") else slug


def step_log_tail(api: API, repo_id: int, number: int, step_id: int, n: int) -> list[str]:
    entries = api.call("GET", f"/repos/{repo_id}/logs/{number}/{step_id}") or []
    lines = [base64.b64decode(e.get("data") or b"").decode(errors="replace").rstrip() for e in entries]
    return lines[-n:]


def report(api: API, repo_id: int, p: dict, tail: int) -> None:
    print(f"pipeline #{p['number']} {p['status']}  {api.url.split('//')[-1]}  commit {p['commit'][:9]}")
    for w in p.get("workflows") or []:
        print(f"  {w['name']:22} {w['state']}")
        for c in w.get("children") or []:
            dur = (c.get("finished") or 0) - (c.get("started") or 0)
            extra = f" {c['error']}" if c.get("error") else ""
            print(f"    {c['name']:22} {c['state']:8} {str(dur) + 's' if dur > 0 else ''}{extra}")
            if c["state"] in ("failure", "error", "killed"):
                for line in step_log_tail(api, repo_id, p["number"], c["id"], tail):
                    print("      | " + line[:240])


def main() -> int:
    ap = argparse.ArgumentParser(description="Run HEAD through Woodpecker CI and wait for the verdict.")
    ap.add_argument("--no-push", action="store_true", help="do not push HEAD first (branch already on origin)")
    ap.add_argument("--tail", type=int, default=40, help="log lines to show per failed step")
    ap.add_argument("--poll", type=int, default=20, help="seconds between status polls")
    args = ap.parse_args()

    url, token = load_config()
    api = API(url, token)
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    if branch == "HEAD":
        sys.exit("ci-woodpecker: detached HEAD; check out a branch first")
    sha = git("rev-parse", "HEAD")

    if not args.no_push:
        remote_sha = git("ls-remote", "origin", f"refs/heads/{branch}").split("\t")[0]
        lease = f"--force-with-lease=refs/heads/{branch}:{remote_sha}" if remote_sha else f"--force-with-lease=refs/heads/{branch}:"
        subprocess.run(["git", "push", "-q", lease, "origin", f"+{sha}:refs/heads/{branch}"], check=True)

    repo = api.call("GET", f"/repos/lookup/{repo_slug()}")
    p = api.call("POST", f"/repos/{repo['id']}/pipelines", {"branch": branch})
    if p["commit"] != sha:
        sys.exit(f"ci-woodpecker: pipeline #{p['number']} took commit {p['commit'][:9]}, not HEAD {sha[:9]}; is the push on origin?")
    print(f"ci-woodpecker: pipeline #{p['number']} started for {branch} @ {sha[:9]}", flush=True)

    start = time.monotonic()
    while p["status"] not in TERMINAL:
        time.sleep(args.poll)
        p = api.call("GET", f"/repos/{repo['id']}/pipelines/{p['number']}")
    report(api, repo["id"], p, args.tail)
    print(f"ci-woodpecker: {p['status']} after {int(time.monotonic() - start)}s")
    return 0 if p["status"] == "success" else 1


if __name__ == "__main__":
    sys.exit(main())
