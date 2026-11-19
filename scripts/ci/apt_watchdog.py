#!/usr/bin/env python3
# file: scripts/ci/apt_watchdog.py
# version: 1.1.0
# guid: 5e1b9c47-2a83-4d6f-8b0e-91c7f3a4d256
# last-edited: 2026-11-18

"""Run a command and kill it when a directory stops growing fast enough.

Usage: apt_watchdog.py [--dir D] [--floor-kib N] [--window S] [--grace S]
                       [--poll S] [--report S] -- <command> [args...]

apt_install.sh runs ``apt-get install --download-only`` under this. A mirror
that trickles (a few hundred KiB/s) never trips apt's own Acquire timeout, which
only fires on a full stall, and a flat wall-clock kill cannot tell a slow mirror
from a big download. This kills on throughput instead: once ``--grace`` seconds
have passed, if the directory grew by less than ``--floor-kib`` KiB/s averaged
over the last ``--window`` seconds, the command's process group is killed and
the exit status is 124 (the same status timeout(1) uses). The caller then
rotates to another mirror; the partial downloads stay in the directory, and
apt resumes them.

Where apt writes: with ``-o Dir::Cache::archives=D`` apt downloads each .deb
into ``D/partial/`` and renames it to ``D/`` when it is complete (``D/lock`` is
an empty lock file). Measured on a hosted ubuntu-26.04 runner, a 67 MB set grew
``D`` from 0 to 67 MB with the bytes first under ``D/partial`` and then in
``D``. So the watched number is the recursive size of ``D`` (partial/ included),
sampled with partial/ scanned before ``D`` itself and kept monotone, so a file
renamed between the two scans can neither be missed nor look like shrinkage.

Besides the kill message, the watchdog prints a progress line every
``--report`` seconds (size and the rate over the last window) and a summary when
the command exits, so a CI log shows the real download timing of a healthy run.

Runs as root under apt_install.sh (sudo), because the command's process group
is root's and an unprivileged parent cannot signal it. If the directory cannot
be measured the watchdog does not kill: the wall-clock cap around it still
applies.

Exit status: the command's, or 124 for a throughput kill, or 128+signal when
the watchdog itself was told to stop (the command is killed first).
"""

from __future__ import annotations

import argparse
import os
import signal
import subprocess
import sys
import time

SLOW_EXIT = 124


def dir_bytes(path: str) -> int | None:
    """Total size of every file under ``path``; None when it cannot be read."""
    total = 0
    try:
        # topdown=False yields partial/ before the directory itself (see the
        # module docstring).
        for root, _dirs, files in os.walk(path, topdown=False, onerror=_raise):
            for name in files:
                try:
                    total += os.lstat(os.path.join(root, name)).st_size
                except FileNotFoundError:
                    continue  # renamed out of partial/ between listing and stat
    except OSError:
        return None
    return total


def _raise(err: OSError) -> None:
    raise err


class Meter:
    """High-water mark of ``dir_bytes``: a download only ever grows the directory.

    A rename from partial/ into the directory racing the scan, or apt removing a
    corrupt partial, must not read as a drop (which would turn the next
    differences into a false "too slow").
    """

    def __init__(self, directory: str) -> None:
        self.directory = directory
        self.high = 0

    def read(self) -> int | None:
        size = dir_bytes(self.directory)
        if size is None:
            return None
        self.high = max(self.high, size)
        return self.high


def window_rate(samples: list[tuple[float, int]], now: float, window: float) -> float | None:
    """Bytes/s over the last ``window`` seconds, or None without a full window of history."""
    if not samples:
        return None
    old = [s for s in samples if s[0] <= now - window]
    if not old:
        return None
    t0, b0 = old[-1]
    t1, b1 = samples[-1]
    if t1 - t0 <= 0:
        return None
    return (b1 - b0) / (t1 - t0)


def too_slow(samples: list[tuple[float, int]], now: float, started: float,
             floor_bps: float, window: float, grace: float) -> bool:
    """True when the growth over the last ``window`` seconds is under the floor."""
    if now - started < grace:
        return False
    rate = window_rate(samples, now, window)
    return rate is not None and rate < floor_bps


def fmt_rate(rate: float | None) -> str:
    return "n/a" if rate is None else f"{rate / 1024.0:.0f} KiB/s"


def kill_group(proc: subprocess.Popen[bytes]) -> None:
    for sig, wait in ((signal.SIGTERM, 5.0), (signal.SIGKILL, 5.0)):
        try:
            os.killpg(proc.pid, sig)
        except ProcessLookupError:
            return
        deadline = time.monotonic() + wait
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                return
            time.sleep(0.1)


def run(argv: list[str], directory: str, floor_kib: float, window: float,
        grace: float, poll: float, report: float = 30.0) -> int:
    proc = subprocess.Popen(argv, start_new_session=True)
    stopped: list[int] = []

    def on_signal(signum: int, _frame: object) -> None:
        stopped.append(signum)

    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGINT, on_signal)
    started = time.monotonic()
    samples: list[tuple[float, int]] = []
    meter = Meter(directory)
    next_report = started + report

    def summary(verdict: str) -> None:
        elapsed = time.monotonic() - started
        size = samples[-1][1] if samples else 0
        avg = size / elapsed if elapsed > 0 else None
        print(
            f"apt_watchdog: {verdict}: {directory} holds {size / 1048576.0:.1f} MiB after "
            f"{elapsed:.0f}s (average {fmt_rate(avg)}, floor {floor_kib:g} KiB/s)",
            file=sys.stderr,
        )

    while True:
        try:
            rc = proc.wait(timeout=poll)
            size = meter.read()
            if size is not None:
                samples.append((time.monotonic(), size))
            summary(f"command exited {rc}")
            return rc
        except subprocess.TimeoutExpired:
            pass
        if stopped:
            kill_group(proc)
            summary("stopped by signal")
            return 128 + stopped[0]
        now = time.monotonic()
        size = meter.read()
        if size is None:
            continue
        samples.append((now, size))
        samples[:] = [s for s in samples if s[0] >= now - 2 * window - poll]
        if report > 0 and now >= next_report:
            next_report = now + report
            print(
                f"apt_watchdog: {size / 1048576.0:.1f} MiB so far, "
                f"{fmt_rate(window_rate(samples, now, window))} over the last {window:g}s",
                file=sys.stderr,
            )
        if too_slow(samples, now, started, floor_kib * 1024.0, window, grace):
            print(
                f"apt_watchdog: {directory} grew at {fmt_rate(window_rate(samples, now, window))}, "
                f"under the {floor_kib:g} KiB/s floor, over the last {window:g}s "
                f"({size / 1048576.0:.1f} MiB held); killing the download so the caller can try another mirror",
                file=sys.stderr,
            )
            kill_group(proc)
            return SLOW_EXIT


def main(args: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawTextHelpFormatter)
    ap.add_argument("--dir", default="/var/cache/apt/archives")
    ap.add_argument("--floor-kib", type=float, default=400.0)
    ap.add_argument("--window", type=float, default=20.0)
    ap.add_argument("--grace", type=float, default=20.0)
    ap.add_argument("--poll", type=float, default=2.0)
    ap.add_argument("--report", type=float, default=30.0, help="progress line interval in seconds; 0 disables")
    ap.add_argument("command", nargs=argparse.REMAINDER)
    ns = ap.parse_args(args)
    cmd = ns.command[1:] if ns.command[:1] == ["--"] else ns.command
    if not cmd:
        ap.error("no command given")
    return run(cmd, ns.dir, ns.floor_kib, ns.window, ns.grace, ns.poll, ns.report)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
