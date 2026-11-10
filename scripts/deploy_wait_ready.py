#!/usr/bin/env python3
# file: scripts/deploy_wait_ready.py
# version: 1.2.0
# guid: 8cc44fdd-c37e-4cc9-b12b-e209be212cee
# last-edited: 2026-11-09

"""Wait for a restarted server to report "ready":true on /health.

Usage: deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT [PREVIOUS_INVOCATION]]
(scripts/deploy-wait-ready.sh runs this file; 09 §9.4 R1 "Content: deploy
tooling", 09 §9.2.2, R1-27.)

With SSH_HOST and UNIT it reads the systemd unit on the host each poll,
`ssh SSH_HOST systemctl show -p
ActiveState,SubState,NRestarts,ExecMainStatus,InvocationID UNIT` (no sudo:
systemctl show needs none):

- PREVIOUS_INVOCATION is the unit's InvocationID read before the restart or
  the stop (empty for a unit that was not running). When it is given, the
  wait never reports ready while the unit still carries that InvocationID:
  a restart that never ran (sudo refused it, ssh dropped before it reached
  systemd) leaves the OLD process active and ready, and /health alone would
  call the rollback done. An unchanged InvocationID on an active unit for
  DEPLOY_RESTART_GRACE seconds (default 30) fails with "not restarted".

- `activating` is progress. A first boot that runs migrations stays
  activating for as long as its steps keep extending the start timeout
  (EXTEND_TIMEOUT_USEC; systemd bounds it), so TIMEOUT counts from the last
  poll that saw the unit activating, not from the start of the wait.
- `failed` or `inactive`, or NRestarts rising above its value at the first
  poll (a crash and an automatic restart), fails at once. A rise whose
  ExecMainStatus is 75 and that is exactly one more restart is not a crash:
  75 is the restart the service asks for after staging a restore (05-backup
  §5B.3.4), so the wait goes on. Two or more at once are a crash loop.
- ExecMainStatus 78 on a failed, inactive or restarted unit is a refusal, not
  a crash: the binary will not open this store (the level guard or the
  upgrade floor, 05-storage §5S.3.7, 09 §9.2.2) and its last log line names
  the release to install. It is reported by name, with exit 78. The status
  is not read while the unit is starting, because systemd keeps the previous
  run's status until the new main process exits.
- Once the unit is `active` it polls BASE_URL/health until the body's
  "ready" is true (read inside the {"data": ...} envelope), and keeps reading the unit meanwhile, so a crash during
  the warmup is caught by NRestarts instead of waiting out TIMEOUT.

Without SSH_HOST (Docker, launchd) only /health can be read, and a long
start looks the same as a hang. The wait then uses the larger of TIMEOUT and
DEPLOY_READY_URL_TIMEOUT (default 1800 s, enough for a first boot that
migrates the store); set that variable to change it.

The server wraps /health's answer in the envelope every API route uses,
{"data": {"ready": ..., ...}} (httputil.RespondWithOK), so "ready" is read
from "data"; a bare body with a top-level "ready" is read as well. Only the
JSON value true is ready. A /health answer that is not 200, or no answer,
keeps polling; a 200 whose body has no "ready" field in either place fails at
once with exit 2 (that binary predates readiness reporting). DEPLOY_WAIT_INTERVAL sets the poll interval in seconds
(default 5).

Exit status: 0 ready; 1 not ready (TIMEOUT passed, or the unit failed or
crashed); 2 a bad answer or bad arguments; 78 the binary refused the store.

Text read from the host is printed with its control characters removed.

Standard library only. Tests: scripts/tests/test_deploy_wait_ready.py.
"""

from __future__ import annotations

import json
import os
import re
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

USAGE = "usage: deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT [PREVIOUS_INVOCATION]]"
PROPERTIES = "ActiveState,SubState,NRestarts,ExecMainStatus,InvocationID"
SSH_OPTS = [
    "-o",
    "BatchMode=yes",
    "-o",
    "ConnectTimeout=10",
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=4",
]
_CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f-\x9f]")


def clean(text: str) -> str:
    """Text from the host with control characters (an ESC sequence a hostile
    or broken host prints) removed; newlines and tabs are kept."""
    return _CONTROL.sub("", text)


EX_TEMPFAIL = 75  # a restart the service requested
EX_CONFIG = 78  # the binary refused the store


def _env_seconds(name: str, default: float) -> float:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError:
        print(f"ERROR: {name}={raw!r} is not a number of seconds", file=sys.stderr)
        sys.exit(2)
    if value < 0:
        print(f"ERROR: {name}={raw!r} is negative", file=sys.stderr)
        sys.exit(2)
    return value


def read_unit(host: str, unit: str) -> tuple[dict[str, str] | None, str]:
    """Return (properties, "") or (None, the ssh error) for one poll."""
    try:
        proc = subprocess.run(
            ["ssh", *SSH_OPTS, host, f"systemctl show -p {PROPERTIES} {unit}"],
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return None, "ssh did not answer within 30s"
    if proc.returncode != 0:
        err = " | ".join(ln.strip() for ln in clean(proc.stderr).splitlines() if ln.strip())
        return None, f"ssh exit {proc.returncode}: {err or 'no error output'}"
    props = {}
    for line in clean(proc.stdout).splitlines():
        key, sep, value = line.partition("=")
        if sep:
            props[key.strip()] = value.strip()
    return props, clean(proc.stdout)


def read_health(url: str) -> tuple[str, str]:
    """Return ("ready" | "waiting" | "bad", detail) for one GET of url."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = (
        ssl.CERT_NONE
    )  # the deploy host's certificate may be self-signed (curl -k before)
    try:
        with urllib.request.urlopen(url, timeout=5, context=ctx) as resp:  # noqa: S310 (operator-given URL)
            if resp.status != 200:
                return "waiting", f"HTTP {resp.status}"
            body = clean(resp.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        return "waiting", f"HTTP {e.code}"
    except (urllib.error.URLError, OSError, TimeoutError) as e:
        return "waiting", str(getattr(e, "reason", e))
    try:
        doc = json.loads(body)
    except ValueError:
        doc = None
    if isinstance(doc, dict) and "ready" not in doc and isinstance(doc.get("data"), dict):
        doc = doc["data"]  # the API envelope: {"data": {"ready": true, ...}}
    if not isinstance(doc, dict) or "ready" not in doc:
        return "bad", body.strip()[:200]
    return ("ready" if doc["ready"] is True else "waiting"), "ready=" + json.dumps(doc["ready"])


def _int(props: dict[str, str], key: str) -> int | None:
    try:
        return int(props.get(key, ""))
    except ValueError:
        return None


def wait(url: str, timeout: float, host: str, unit: str, previous: str | None = None) -> int:
    """previous: the unit's InvocationID before the restart ("" for a unit
    that was not running), or None to not check it."""
    interval = _env_seconds("DEPLOY_WAIT_INTERVAL", 5.0)
    grace = _env_seconds("DEPLOY_RESTART_GRACE", 30.0)
    health = url.rstrip("/") + "/health"
    if not host:
        timeout = max(timeout, _env_seconds("DEPLOY_READY_URL_TIMEOUT", 1800.0))
    start = time.monotonic()
    clock_from = (
        start  # TIMEOUT counts from here: the start, or the last poll that saw `activating`
    )
    baseline: int | None = None
    last = "no answer yet"
    while True:
        now = time.monotonic()
        poll_health = not host
        if host:
            props, raw = read_unit(host, unit)
            if props is None:
                last = raw
            else:
                state = props.get("ActiveState", "")
                if not state:
                    print(
                        f"ERROR: `systemctl show` for {unit} on {host} gave no ActiveState: {raw.strip()[:200]!r}",
                        file=sys.stderr,
                    )
                    return 2
                sub = props.get("SubState", "")
                restarts = _int(props, "NRestarts")
                status = _int(props, "ExecMainStatus")
                if baseline is None:
                    baseline = restarts
                rose = restarts is not None and baseline is not None and restarts > baseline
                if rose and status == EX_TEMPFAIL and restarts == baseline + 1:
                    print(
                        f"   {unit} restarted on request (exit 75, NRestarts {baseline} -> {restarts}); still waiting"
                    )
                    baseline, rose = restarts, False
                    clock_from = now
                desc = f"ActiveState={state} SubState={sub} NRestarts={restarts} ExecMainStatus={status}"
                if state in ("failed", "inactive") or rose:
                    if status == EX_CONFIG:
                        print(
                            f"ERROR: {unit} on {host} exited with status 78: the binary refused this store; "
                            "install the release the log names first "
                            f"(`journalctl -u {unit} -n 20` ends with `install <release> first`). ({desc})",
                            file=sys.stderr,
                        )
                        return EX_CONFIG
                    why = (
                        f"NRestarts rose from {baseline} to {restarts} (it crashed and was restarted)"
                        if rose
                        else f"it is {state}"
                    )
                    print(
                        f"ERROR: {unit} on {host} is not running: {why}. ({desc}) "
                        f"See `journalctl -u {unit} -n 50`.",
                        file=sys.stderr,
                    )
                    return 1
                if state == "activating":
                    clock_from = now
                    last = desc
                elif state == "active":
                    invocation = props.get("InvocationID", "")
                    if previous is not None and (not invocation or invocation == previous):
                        last = f"{desc} InvocationID={invocation or '(none)'}, unchanged since before the restart"
                        if now - start >= grace:
                            print(
                                f"ERROR: {unit} on {host} was not restarted: it is still the process that was "
                                f"running before the restart (InvocationID {invocation or '(none)'} unchanged for "
                                f"{int(now - start)}s), so the old binary is still the one serving. Restart it "
                                f"(sudo systemctl restart {unit}) and check the error the restart reported above.",
                                file=sys.stderr,
                            )
                            return 1
                    else:
                        poll_health = True
                        last = desc
                else:
                    last = desc  # deactivating, reloading: not progress, keep reading
        if poll_health:
            verdict, detail = read_health(health)
            if verdict == "ready":
                print(f"   ready after {int(now - start)}s")
                return 0
            if verdict == "bad":
                print(
                    f"ERROR: {health} answered with no ready field after {int(now - start)}s "
                    f"(binary predates readiness reporting?): {detail!r}",
                    file=sys.stderr,
                )
                return 2
            last = f"{last}; /health: {detail}" if host else f"/health: {detail}"
        if now - clock_from >= timeout:
            print(
                f"ERROR: {health} not ready after {int(now - start)}s "
                f"(limit {timeout:g}s{' after the unit left activating' if host else ''}); last: {last}",
                file=sys.stderr,
            )
            return 1
        time.sleep(interval)


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 4, 5):
        print(USAGE, file=sys.stderr)
        return 2
    url, raw_timeout = argv[0], argv[1]
    try:
        timeout = float(raw_timeout)
    except ValueError:
        timeout = -1
    if not url or timeout < 0:
        print(f"{USAGE}\n(TIMEOUT {raw_timeout!r} is not a number of seconds)", file=sys.stderr)
        return 2
    host, unit = (argv[2], argv[3]) if len(argv) >= 4 else ("", "")
    if len(argv) >= 4 and (not host or not unit):
        print(USAGE, file=sys.stderr)
        return 2
    previous = argv[4].strip() if len(argv) == 5 else None
    return wait(url, timeout, host, unit, previous)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
