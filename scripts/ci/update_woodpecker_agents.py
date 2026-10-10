#!/usr/bin/env python3
# file: scripts/ci/update_woodpecker_agents.py
# version: 1.0.0
# guid: 2de0d1a6-b52b-44d0-b941-2e05d9911c94
# last-edited: 2026-10-10
"""Upgrade the native woodpecker-agent binaries to the server's version.

U1 and llm1 run the agent as a plain binary with the local backend (U1 under
systemd, llm1 as a LaunchDaemon), not as a container, so an upgrade means
swapping a binary. U0's agent is part of the server's swarm stack and is
upgraded with that stack, not here (docs/ci/woodpecker.md, section 4).

For each host, in order:

1. Skip it if the server already reports the target version for that agent.
2. Pause it (`no_schedule`) so the server hands it no new workflow, then wait
   until its running tasks have finished. Restarting mid-workflow kills them.
3. Copy the release binary over (checked against the release's
   checksums.txt), keep the old one beside it as `woodpecker-agent.<old>`, and
   swap the new one in with an atomic rename.
4. Restart: `systemctl restart` on U1; on llm1 kill the process and launchd's
   KeepAlive starts the new binary (no sudo needed).
5. Pass only when the SERVER reports the new version with a fresh
   last_contact; a local `--version` does not prove the agent reconnected.
   On failure the old binary is restored and restarted. On llm1 a failure
   is usually macOS Local Network privacy: the binary is ad-hoc signed, so a
   new build is a new identity and may need the toggle in System Settings ->
   Privacy & Security -> Local Network again.
6. Unpause, whatever happened.

The Woodpecker API is called through curl, not urllib: macOS Local Network
privacy can block Python's own sockets to the LAN while curl and ssh work.
URL and token come from WOODPECKER_URL / WOODPECKER_TOKEN or the gitignored
.claude/.credentials/woodpecker-api.env, as for scripts/ci_woodpecker.py.
Nothing here prints the agent's env file, plist or agent.conf: they hold the
agent secret.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import subprocess
import sys
import tarfile
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

CRED_REL = Path(".claude/.credentials/woodpecker-api.env")
RELEASE_URL = "https://github.com/woodpecker-ci/woodpecker/releases/download/v{version}/{asset}"


@dataclass(frozen=True)
class Host:
    agent: str  # the agent's name on the server (WOODPECKER_HOSTNAME)
    ssh: str  # ssh destination; an alias from ~/.ssh/config or host name
    platform: str  # release asset suffix, e.g. linux_amd64
    binary: str  # absolute path, or relative to the remote user's HOME
    restart: str  # "systemd" or "launchd-keepalive"


HOSTS = {
    "u1": Host("u1", "u1-root", "linux_amd64", "/tank/ci/woodpecker/bin/woodpecker-agent", "systemd"),
    "llm1": Host("llm1", "llm1.local", "darwin_arm64", "ci/woodpecker/bin/woodpecker-agent", "launchd-keepalive"),
}


def log(msg: str) -> None:
    print(time.strftime("%H:%M:%S"), msg, flush=True)


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
        sys.exit(f"update-woodpecker-agents: set WOODPECKER_URL and WOODPECKER_TOKEN, or create {CRED_REL}")
    return url.rstrip("/"), token


class API:
    def __init__(self, url: str, token: str):
        self.url, self.token = url, token

    def call(self, method: str, path: str, body: dict | None = None) -> Any:
        # The token goes in through stdin (-H @-), so it never shows in `ps`.
        cmd = ["curl", "-sS", "--fail-with-body", "--max-time", "30", "-X", method, "-H", "@-"]
        headers = f"Authorization: Bearer {self.token}\n"
        if body is not None:
            headers += "Content-Type: application/json\n"
            cmd += ["--data-binary", json.dumps(body)]
        r = subprocess.run(cmd + [self.url + "/api" + path], input=headers, capture_output=True, text=True)
        if r.returncode != 0:
            raise RuntimeError(f"{method} {path}: curl rc={r.returncode}: {(r.stdout + r.stderr)[:300]}")
        return json.loads(r.stdout) if r.stdout.strip() else None

    def agent(self, name: str) -> dict:
        for a in self.call("GET", "/agents?per_page=100") or []:
            if a["name"] == name:
                return a
        raise RuntimeError(f"agent {name!r} is not registered on the server")

    def set_paused(self, agent: dict, paused: bool) -> None:
        # PatchAgent copies name, filters AND no_schedule from the body, so a
        # body with only no_schedule would blank the agent's name and filters.
        body = {"name": agent["name"], "filters": agent.get("filters"), "no_schedule": paused}
        self.call("PATCH", f"/agents/{agent['id']}", body)


def ssh(host: Host, script: str, check: bool = True) -> str:
    r = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host.ssh, script],
                       capture_output=True, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(f"ssh {host.ssh}: rc={r.returncode}: {(r.stdout + r.stderr).strip()[:400]}")
    return r.stdout.strip()


def fetch_binary(version: str, platform: str, workdir: Path) -> Path:
    """Download the release tarball, check it against checksums.txt, extract the agent."""
    asset = f"woodpecker-agent_{platform}.tar.gz"
    tarball, sums = workdir / asset, workdir / f"checksums-{version}.txt"
    for url, dest in ((RELEASE_URL.format(version=version, asset=asset), tarball),
                      (RELEASE_URL.format(version=version, asset="checksums.txt"), sums)):
        if not dest.exists():
            subprocess.run(["curl", "-fsSL", "--max-time", "300", "-o", str(dest), url], check=True)
    want = next((ln.split()[0] for ln in sums.read_text().splitlines()
                 if ln.split() and ln.split()[-1] == asset), None)
    got = hashlib.sha256(tarball.read_bytes()).hexdigest()
    if want != got:
        raise RuntimeError(f"{asset}: sha256 {got} does not match checksums.txt ({want})")
    out = workdir / platform
    out.mkdir(exist_ok=True)
    with tarfile.open(tarball) as tf:
        member = tf.getmember("woodpecker-agent")
        if not member.isfile():
            raise RuntimeError(f"{asset}: woodpecker-agent is not a regular file")
        tf.extract(member, out, filter="data")
    return out / "woodpecker-agent"


def wait_idle(api: API, agent_id: int, timeout: int) -> None:
    deadline = time.time() + timeout
    while True:
        tasks = api.call("GET", f"/agents/{agent_id}/tasks") or []
        if not tasks:
            return
        if time.time() > deadline:
            raise RuntimeError(f"still running {len(tasks)} task(s) after {timeout}s; not restarting")
        log(f"  waiting: {len(tasks)} task(s) running")
        time.sleep(15)


def restart(host: Host, binary: str) -> None:
    if host.restart == "systemd":
        ssh(host, "systemctl restart woodpecker-agent")
    else:
        # The LaunchDaemon has KeepAlive: launchd starts the new binary once
        # the old process exits, so the CI user can do this without sudo.
        ssh(host, f"pkill -TERM -f {shlex.quote('^' + binary + '$')}")


def wait_reconnected(api: API, name: str, version: str, since: float, timeout: int) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        time.sleep(5)
        try:
            a = api.agent(name)
        except RuntimeError:
            continue
        if a.get("version") == version and a.get("last_contact", 0) >= since:
            return True
    return False


def upgrade(api: API, host: Host, version: str, workdir: Path, args: argparse.Namespace) -> str:
    agent = api.agent(host.agent)
    if agent.get("version") == version and not args.force:
        return f"already {version}"
    binary = ssh(host, f'cd && realpath {shlex.quote(host.binary)}')
    current = ssh(host, f"{shlex.quote(binary)} --version").split()[-1]
    log(f"{host.agent}: server says {agent.get('version')}, binary says {current}, target {version}")
    if args.dry_run:
        return f"dry run: would replace {current} with {version}"

    new = fetch_binary(version, host.platform, workdir)
    b = shlex.quote(binary)
    backup = shlex.quote(f"{binary}.{current}")
    staged = shlex.quote(binary + ".new")
    subprocess.run(["scp", "-q", "-o", "BatchMode=yes", str(new), f"{host.ssh}:{binary}.new"], check=True)
    got = ssh(host, f"{staged} --version").split()[-1]
    if got != version:
        raise RuntimeError(f"staged binary reports {got}, expected {version}")

    api.set_paused(agent, True)
    try:
        wait_idle(api, agent["id"], args.idle_timeout)
        # Keep the old binary for rollback, then swap with a rename (same
        # directory, so atomic). The copy takes the old file's owner and mode.
        ssh(host, f"set -e; cp -p {b} {backup}; cp -p {b} {b}.tmp; cat {staged} > {b}.tmp; "
                  f"chmod 755 {b}.tmp; mv -f {b}.tmp {b}; rm -f {staged}")
        since = time.time() - 1
        restart(host, binary)
        if wait_reconnected(api, host.agent, version, since, args.verify_timeout):
            return f"upgraded {current} -> {version}"
        log(f"{host.agent}: no check-in on {version} within {args.verify_timeout}s; rolling back to {current}")
        ssh(host, f"set -e; cp -p {backup} {b}.tmp; mv -f {b}.tmp {b}")
        since = time.time() - 1
        restart(host, binary)
        back = wait_reconnected(api, host.agent, current, since, args.verify_timeout)
        hint = (" On macOS this is usually Local Network privacy: allow woodpecker-agent under System Settings"
                " -> Privacy & Security -> Local Network, then rerun.") if host.platform.startswith("darwin") else ""
        raise RuntimeError(f"new binary never checked in; rolled back to {current} "
                           f"({'reconnected' if back else 'ALSO NOT RECONNECTED'}).{hint}")
    finally:
        try:
            api.set_paused(api.agent(host.agent), False)
        except RuntimeError as e:
            log(f"{host.agent}: COULD NOT UNPAUSE, it gets no work until no_schedule is cleared: {e}")


def main() -> int:
    ap = argparse.ArgumentParser(description="Upgrade the native woodpecker-agent binaries to the server's version.")
    ap.add_argument("hosts", nargs="*", default=list(HOSTS), help=f"agents to upgrade (default: {' '.join(HOSTS)})")
    ap.add_argument("--version", help="target version (default: the server's own version)")
    ap.add_argument("--dry-run", action="store_true", help="report versions, change nothing")
    ap.add_argument("--force", action="store_true", help="reinstall even if the server reports the target version")
    ap.add_argument("--idle-timeout", type=int, default=3600, help="seconds to wait for running tasks to finish")
    ap.add_argument("--verify-timeout", type=int, default=120, help="seconds to wait for the agent to check in")
    args = ap.parse_args()

    unknown = [h for h in args.hosts if h not in HOSTS]
    if unknown:
        sys.exit(f"unknown host(s) {unknown}; known: {', '.join(HOSTS)}")
    api = API(*load_config())
    version = (args.version or api.call("GET", "/version")["version"]).removeprefix("v")
    log(f"target woodpecker-agent {version}")

    failed = 0
    with tempfile.TemporaryDirectory(prefix="wp-agent-") as tmp:
        for name in args.hosts:
            try:
                log(f"{name}: {upgrade(api, HOSTS[name], version, Path(tmp), args)}")
            except (RuntimeError, subprocess.CalledProcessError) as e:
                failed += 1
                log(f"{name}: FAILED: {e}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
