#!/usr/bin/env python3
# file: scripts/deploy_rollback.py
# version: 1.0.0
# guid: 0d519d12-853d-4902-8896-8d5744a32cbd
# last-edited: 2026-11-09

"""`make rollback` and `make rollback-restore` (R1-27).

05-storage §5S.3.7 "The binary-swap guard", §5S.3.12 row `make
rollback-restore`, 09 §9.2.1 "Rollback", 09 §9.2.2.

Usage (the Makefile passes every value):
    deploy_rollback.py rollback --guard G --host H --bin B --db D --url U --unit N --timeout S
    deploy_rollback.py restore  --guard G --host H --bin B --db D --url U --unit N --timeout S [--archive NAME]

The decisions are scripts/storage_format_guard.py's, loaded from --guard so
the recipe tests can run a mutated copy; every remote command is in its
`commands()`. Both flows first read the rollback helpers' config on the host
and refuse unless it names DEPLOY_BIN, DEPLOY_DB and DEPLOY_UNIT.

rollback:
  1. `<bin>.prev` exists; the guard passes (the in-flight probe, the store's
     levels against `.prev`'s).
  2. Match the unit type to `.prev` (unit-type helper), read the unit's
     InvocationID, install `.prev` atomically (swap-prev helper), restart.
  3. Wait (scripts/deploy_wait_ready.py) with that InvocationID, so the old
     process still serving after a restart that never ran is never called
     ready. A restart refused by sudo fails at once.

restore (make rollback-restore):
  1. `<bin>.prev` exists; the guard's read-only checks pass while the service
     still runs, so a refusal leaves it running: the probe, the source (the
     latest restorable checkpoint, or ROLLBACK_ARCHIVE, a file name in the
     helpers' archive_dir), `.prev`'s levels EQUAL to the source's.
  2. Match the unit type to `.prev` and read the InvocationID, still before
     the stop.
  3. Stop the unit; check it stays stopped; probe again.
  4. The CURRENT binary's `storage restore-offline` through the
     restore-offline helper (as the service user, in a transient unit that
     outlives a dropped connection). Its swap lock refuses while a service is
     running or starting; nothing retries it.
  5. Check the unit stayed stopped; install `.prev` (swap-prev helper);
     start; wait as above.

Every failure after the first change says what state the host is in (the
store, the binary, the unit, the unit type) and prints the exact commands
that finish or undo the rollback. Host text is printed with control
characters removed.

Exit status: 0 done; 1 refused or failed; 2 bad arguments; 78 the binary
refused the store (from the wait). Standard library only. Tests:
scripts/tests/test_make_rollback.py.
"""

from __future__ import annotations

import argparse
import importlib.util
import os
import sys
from pathlib import Path
from types import ModuleType

sys.path.insert(0, str(Path(__file__).resolve().parent))
import deploy_wait_ready  # noqa: E402


def load_guard(path: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location("storage_format_guard", path)
    if spec is None or spec.loader is None:
        raise SystemExit(f"deploy_rollback: cannot load the guard {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class Run:
    """One recipe run: the guard, the options, the host reads."""

    def __init__(self, guard: ModuleType, a: argparse.Namespace):
        self.g = guard
        self.a = a
        self.o = guard.Opts(host=a.host, db=a.db, bin=a.bin, unit=a.unit, archive=getattr(a, "archive", "") or "")
        self.get = guard.SSH(self.o)

    def say(self, *lines: str, err: bool = False) -> None:
        for line in lines:
            print(self.g.clean(line), file=sys.stderr if err else sys.stdout, flush=True)

    def refuse(self, *lines: str) -> int:
        self.say(*lines, err=True)
        return 1

    def cmd(self, key: str, name: str = "") -> str:
        return self.g.commands(self.o, name)[key]

    def ssh_line(self, key: str, name: str = "") -> str:
        """The command an operator types to run key by hand."""
        return f"ssh {self.o.host} '{self.cmd(key, name)}'"

    def act(self, key: str, stream: bool = False, timeout: float | None = 120):
        """Run a changing command (never cached)."""
        r = self.g.run_remote(self.o.host, self.cmd(key), timeout=timeout, stream=stream)
        if not stream:
            out = self.g.clean(r.stdout).rstrip()
            if out:
                self.say(*("   " + ln for ln in out.splitlines()))
        return r

    def start_current_lines(self) -> list[str]:
        """How to bring the service back on the CURRENT binary after the unit
        type was matched to .prev."""
        return [
            f"To start the service again on the current binary {self.o.bin}: "
            f"bash scripts/deploy-match-unit-type.sh {self.o.host} {self.o.bin} && {self.ssh_line('start')}"
        ]

    # --- steps shared by both flows

    def preflight(self) -> int:
        refusal = self.g.load_host_config(self.o, self.get)
        if refusal:
            return self.refuse(*refusal)
        if self.get("prev_exists").rc != 0:
            return self.refuse(
                f"REFUSING: no {self.o.bin}.prev on {self.o.host}: install the release to roll back to as "
                f"{self.o.bin}.prev first. Nothing was changed."
            )
        return 0

    def match_unit_type(self) -> int:
        caps = self.get("prev_caps")
        notify = caps.rc == 0 and "sd_notify" in self.g.clean(caps.stdout).split()
        kind = "notify" if notify else "simple"
        self.say(
            f"→ Matching the service type to {self.o.bin}.prev ("
            + ("it reports sd_notify: Type=notify" if notify else "no sd_notify: Type=simple drop-in")
            + ")..."
        )
        r = self.act(f"unit_type_{kind}")
        if r.rc != 0:
            return self.refuse(
                f"REFUSING: matching the unit type failed ({self.g._cause(r)}). Nothing was stopped, restored "
                "or swapped.",
                f"It runs `{self.cmd('unit_type_' + kind)}`: check the unit-type helper and its sudoers line "
                f"({self.g.DOCS}).",
            )
        return 0

    def invocation(self) -> tuple[str | None, str]:
        r = self.get("invocation")
        if r.rc != 0:
            return None, self.g._cause(r)
        return self.g.clean(r.stdout).strip(), ""

    def wait(self, previous: str) -> int:
        return deploy_wait_ready.wait(self.a.url, self.a.timeout, self.o.host, self.o.unit, previous)


def rollback(run: Run) -> int:
    o = run.o
    run.say(f"→ Rolling back {o.bin} on {o.host}...")
    if run.preflight():
        return 1
    ignore = os.environ.get("ROLLBACK_IGNORE_FORMAT", "").strip() == "1"
    code, lines = run.g.decide_rollback(o, run.get, ignore)
    run.say(*lines, err=code != 0)
    if code:
        return 1
    if run.match_unit_type():
        return 1
    previous, err = run.invocation()
    if previous is None:
        return run.refuse(
            f"REFUSING: cannot read {o.unit}'s InvocationID ({err}), which the wait needs to tell the restarted "
            f"process from the old one. Nothing was swapped; the unit type now matches {o.bin}.prev.",
            *run.start_current_lines(),
        )
    swap = run.act("swap")
    if swap.rc != 0:
        return run.refuse(
            f"ERROR: installing {o.bin}.prev failed ({run.g._cause(swap)}). {o.bin} is unchanged and "
            f"{o.unit} was not restarted; the unit type now matches {o.bin}.prev, so match it back before the "
            f"next restart: bash scripts/deploy-match-unit-type.sh {o.host} {o.bin}"
        )
    restart = run.act("restart")
    if restart.rc != 0:
        if restart.sudo_refused:
            return run.refuse(
                f"ERROR: {o.bin} is now the previous binary, but {o.unit} was not restarted: sudo refused "
                f"`{run.cmd('restart')}` ({run.g._cause(restart)}). The OLD process is still the one serving.",
                f"Restart it by hand: {run.ssh_line('restart')} (and add its sudoers line, {run.g.DOCS}).",
            )
        run.say(
            f"   systemctl restart reported a failure ({run.g._cause(restart)}); the wait reads the unit and "
            "names the cause."
        )
    run.say(
        f"→ Restarted on the previous binary; waiting for /health to report ready ({run.a.timeout:g}s after "
        "the unit leaves activating)..."
    )
    rc = run.wait(previous)
    if rc != 0:
        run.refuse(
            f"ERROR: the swap is done ({o.bin} is the previous binary; the replaced one is {o.bin}.rolled-back), "
            "but the server did not report ready (the reason is above). A previous binary older than readiness "
            "reporting has no ready field; check it by hand."
        )
        return rc
    run.say(f"✅ Rolled back {o.bin} to the previous version and restarted.")
    return 0


def restore(run: Run) -> int:
    o = run.o
    run.say(f"→ Rollback-restore of {o.db} on {o.host}{' from ' + o.archive if o.archive else ''}...")
    if run.preflight():
        return 1
    code, lines, restore_cmd = run.g.decide_restore(o, run.get)
    run.say(*lines, err=code != 0)
    if code:
        return 1
    if run.match_unit_type():
        return 1
    previous, err = run.invocation()
    if previous is None:
        return run.refuse(
            f"REFUSING: cannot read {o.unit}'s InvocationID ({err}). Nothing was stopped or restored; the unit "
            f"type now matches {o.bin}.prev.",
            *run.start_current_lines(),
        )
    unit_type_note = f"the unit type matches {o.bin}.prev"
    run.say(f"→ Stopping {o.unit}...")
    stop = run.act("stop")
    if stop.rc != 0:
        state, seen = run.g.unit_state(o, run.g.SSH(o))
        return run.refuse(
            f"ERROR: could not stop {o.unit} ({run.g._cause(stop)}); it now reads {seen}. Nothing was restored "
            f"and {o.bin} is still the current binary; {unit_type_note}.",
            *run.start_current_lines(),
        )
    stopped = run.g.check_stopped(o, run.g.SSH(o))
    if stopped:
        return run.refuse(
            *stopped,
            f"Nothing was restored and {o.bin} is still the current binary; {unit_type_note}. Find what started "
            f"it (systemctl status {o.unit}), stop it, and run make rollback-restore again.",
            *run.start_current_lines(),
        )
    probe = run.g.check_inflight(o, run.g.SSH(o))
    if probe:
        return run.refuse(
            *probe,
            f"{o.unit} is stopped and nothing was restored; {unit_type_note}.",
            *run.start_current_lines(),
        )
    run.say(f"→ Restoring with the current binary: {restore_cmd}")
    r = run.g.run_remote(o.host, restore_cmd, timeout=None, stream=True)
    if r.rc == 255:
        return run.refuse(
            f"ERROR: the connection to {o.host} dropped during the restore. The restore keeps running on the host "
            f"as {run.g.TRANSIENT_UNIT}.service: do NOT start {o.unit} until it has finished "
            f"(ssh {o.host} systemctl status {run.g.TRANSIENT_UNIT}; its output is in {o.transient_log}).",
            f"If it succeeded, finish with: {run.ssh_line('swap')} && {run.ssh_line('start')}",
            f"If it failed, {o.bin} is still the current binary and {unit_type_note}.",
            *run.start_current_lines(),
        )
    if r.rc != 0:
        return run.refuse(
            f"ERROR: storage restore-offline failed (exit {r.rc}; its message is above) and was not retried. "
            f"{o.unit} is stopped, {o.bin} is still the current binary, and {unit_type_note}. A swap the restore "
            "had begun is finished or undone by its journal at the next start.",
            f"If it says the service is running or starting, something started {o.unit} during the rollback: "
            f"find it (systemctl status {o.unit}), stop it, and run make rollback-restore again.",
            *run.start_current_lines(),
        )
    after = run.g.check_stopped(o, run.g.SSH(o))
    if after:
        return run.refuse(
            *after,
            f"The store is already restored from the {'archive' if o.archive else 'checkpoint'}, but {o.bin} is "
            f"still the current binary: if it is running, it opens the restored store and migrates it again.",
            f"Stop it ({run.ssh_line('stop')}) and run make rollback: it swaps {o.bin}.prev in while the store is "
            "still at .prev's levels, and refuses if the current binary already migrated it (then run make "
            "rollback-restore again).",
        )
    swap = run.act("swap")
    if swap.rc != 0:
        return run.refuse(
            f"ERROR: the store is restored, but installing {o.bin}.prev failed ({run.g._cause(swap)}). {o.unit} is "
            f"stopped and {o.bin} is still the current binary: do not start {o.unit} now, the current binary "
            "would migrate the restored store again.",
            f"Fix the cause, then finish with: {run.ssh_line('swap')} && {run.ssh_line('start')}",
        )
    start = run.act("start")
    if start.rc != 0:
        if start.sudo_refused:
            return run.refuse(
                f"ERROR: the store is restored and {o.bin} is the previous binary, but sudo refused the start "
                f"({run.g._cause(start)}); {o.unit} is stopped.",
                f"Start it: {run.ssh_line('start')} (and add its sudoers line, {run.g.DOCS}).",
            )
        run.say(
            f"   systemctl start reported a failure ({run.g._cause(start)}); the wait reads the unit and names "
            "the cause."
        )
    run.say(
        f"→ Started the previous binary on the restored store; waiting for /health to report ready "
        f"({run.a.timeout:g}s after the unit leaves activating)..."
    )
    rc = run.wait(previous)
    if rc != 0:
        run.refuse(
            f"ERROR: the restore and the swap are done ({o.bin} is the previous binary; the replaced one is "
            f"{o.bin}.rolled-back), but the server did not report ready (the reason is above)."
        )
        return rc
    run.say(
        f"✅ Restored {o.db} and rolled {o.bin} back to the previous binary. Repeat any credential revocations "
        "made since the restored point."
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="mode", required=True)
    for mode in ("rollback", "restore"):
        p = sub.add_parser(mode)
        p.add_argument("--guard", required=True)
        p.add_argument("--host", required=True)
        p.add_argument("--bin", required=True)
        p.add_argument("--db", required=True)
        p.add_argument("--url", required=True)
        p.add_argument("--unit", default="audiobook-organizer.service")
        p.add_argument("--timeout", type=float, default=300.0)
        if mode == "restore":
            p.add_argument("--archive", default="")
    a = parser.parse_args(argv)
    guard = load_guard(a.guard)
    for k in ("host", "bin", "db", "unit", "url"):
        setattr(a, k, getattr(a, k).strip())
    if a.mode == "restore":
        a.archive = a.archive.strip()
    run = Run(guard, a)
    bad = guard._validate(run.o)
    if bad or a.timeout < 0:
        print(f"deploy_rollback: {bad or 'timeout must not be negative'}", file=sys.stderr)
        return 2
    return rollback(run) if a.mode == "rollback" else restore(run)


if __name__ == "__main__":
    sys.exit(main())
