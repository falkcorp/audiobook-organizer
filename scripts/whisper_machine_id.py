# file: scripts/whisper_machine_id.py
# version: 1.0.0
# guid: 6a2f9c41-8d07-4b35-a1e6-5c9d3b7f0e82
# last-edited: 2026-11-17
#
# Worker machine identity, shared by scripts/whisper_mlx_server.py and
# scripts/whisper_server.py (moved out of the former verbatim).
#
# The Go client makes a file's decoder-timeout result permanent only when two
# DIFFERENT machines fail on it, so one sick worker can never condemn a file.
# Several endpoints can be one machine (two server processes on two ports, or
# two SSH tunnels to the same box), so the client cannot tell machines apart by
# URL; /health reports machine_id instead. It is a short hash, never the raw
# name: the Go side stores it on the file row and shows it in API responses,
# and a machine UUID has no business there. Every process on one machine
# reports the same value, and the Go side treats it as a KNOWN machine, so only
# a source that is the same for every process on the box and across restarts
# may produce it. The hostname is not one (a Mac's hostname can change with the
# network it joins, and a process that fell back to it while its siblings did
# not would make one box two known machines). With no stable source yet,
# /health leaves machine_id out and the Go side names the worker by a hash of
# its URL, an UNKNOWN machine whose strikes never count after a file's first;
# the background thread keeps trying (ioreg is slowest exactly when a worker
# restarts under memory pressure).
#
# One instance per server process (MachineIdentity), so a test that loads a
# server module twice gets two independent identities.

import asyncio
import concurrent.futures
import contextlib
import hashlib
import logging
import os
import subprocess
import sys
import threading
import time

log = logging.getLogger("whisper_machine_id")

MACHINE_ID_FILE = "/etc/machine-id"
MACHINE_ID_IOREG_TIMEOUT_S = 2.0
# How long a /health waits for the FIRST attempt to finish (the attempt starts
# at import, so this matters only for a /health in the first seconds of the
# process). It runs alongside the filesystem checks, so it must stay within the
# health pool's own timeout, and both under the Go probe's 3 s.
MACHINE_ID_WAIT_S = 2.5
MACHINE_ID_RETRY_S = 30.0


class MachineIdentity:
    """The opaque identity of the machine a worker runs on (see the header).

    The attributes are plain instance values so a test can shorten the waits.
    """

    def __init__(self) -> None:
        self.id_file = MACHINE_ID_FILE
        self.ioreg_timeout_s = MACHINE_ID_IOREG_TIMEOUT_S
        self.wait_s = MACHINE_ID_WAIT_S
        self.retry_s = MACHINE_ID_RETRY_S
        self._lock = threading.Lock()
        self._value: str | None = None
        # Resolved when the machine-id thread's FIRST attempt ends, whatever it
        # found.
        self.first: concurrent.futures.Future | None = None

    def stable_source(self) -> str | None:
        """The first stable identity source that answers, or None."""
        override = os.environ.get("WHISPER_MACHINE_ID", "").strip()
        if override:
            return "override:" + override
        with contextlib.suppress(OSError), open(self.id_file, encoding="ascii") as f:
            mid = f.read().strip()
            if mid:
                return "machine-id:" + mid
        if sys.platform == "darwin":
            with contextlib.suppress(OSError, subprocess.SubprocessError):
                out = subprocess.run(
                    ["ioreg", "-rd1", "-c", "IOPlatformExpertDevice"],
                    capture_output=True,
                    text=True,
                    timeout=self.ioreg_timeout_s,
                    check=False,
                ).stdout
                for line in out.splitlines():
                    if "IOPlatformUUID" in line and "=" in line:
                        uuid = line.split("=", 1)[1].strip().strip('"')
                        if uuid:
                            return "platform-uuid:" + uuid
        return None

    def compute(self) -> str | None:
        """The hashed id, or None when no stable source answers."""
        source = self.stable_source()
        if source is None:
            return None
        return hashlib.sha256(source.encode()).hexdigest()[:16]

    def _publish(self, mid: str) -> str:
        """Record the process's id unless one is already recorded, and return
        the one that stands. The first stable id wins for the life of the
        process."""
        with self._lock:
            if self._value is None:
                self._value = mid
            return self._value

    def current(self) -> str | None:
        with self._lock:
            return self._value

    def start(self) -> concurrent.futures.Future:
        """Start the machine-id thread once per server process and return the
        future of its first attempt. Called at import by the server process
        (never by an inference child, which re-imports its server module on
        every restart and has no use for it). Off the event loop, so /health
        never runs ioreg itself. The thread tries the stable sources until one
        answers, every retry_s, then publishes that id and stops: the id
        appears once and never changes."""
        if self.first is None:
            first: concurrent.futures.Future = concurrent.futures.Future()

            def run() -> None:
                warned = False
                while True:
                    try:
                        mid = self.compute()
                    except Exception:  # a source that raises is a source that did not answer
                        log.exception("machine_id source failed")
                        mid = None
                    if mid:
                        self._publish(mid)
                    if not first.done():
                        first.set_result(None)
                    if mid:
                        return
                    if not warned:
                        log.warning(
                            "no stable machine id yet; /health leaves machine_id out (the server "
                            "treats this worker as an unknown machine) and it is retried every "
                            "%.0fs",
                            self.retry_s,
                        )
                        warned = True
                    time.sleep(self.retry_s)

            threading.Thread(target=run, name="machine-id", daemon=True).start()
            self.first = first
        return self.first

    async def reported(self) -> str | None:
        """The process's machine id for /health, or None (the field is left
        out). Right after start it waits for the first attempt without blocking
        the event loop, at most wait_s; after that it never waits."""
        first = self.start()
        if not first.done():
            try:
                await asyncio.wait_for(asyncio.shield(asyncio.wrap_future(first)), self.wait_s)
            except asyncio.TimeoutError:
                log.warning(
                    "machine_id not ready after %.1fs; /health leaves it out this time",
                    self.wait_s,
                )
        return self.current()
