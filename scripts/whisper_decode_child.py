# file: scripts/whisper_decode_child.py
# version: 1.0.0
# guid: 5d0c8e7a-3b94-4a62-9f1e-b7a4c2d81e56
# last-edited: 2026-11-17
#
# A killable decode child for the non-Mac Whisper worker (scripts/whisper_server.py).
#
# WHY: faster-whisper decodes inside a native library call that cannot be
# interrupted from Python. A decode that hangs would hold its slot, and the worker,
# until a human restarted the process. The Mac worker solves this with a child
# process it can kill (InferenceChild in whisper_mlx_server.py); this is the same
# design for the non-Mac worker: the model loads in the CHILD, the parent enforces a
# per-clip deadline, kills a child that misses it, and starts a fresh one on the next
# clip. The worker recovers by itself.
#
# WHAT IS SHARED WITH THE MAC WORKER: the contract, not the code. Both emit
# transcribe_timeout / decoder_crashed / decoder_killed with the same meaning and
# the same error text shape, and report start failures the same way
# (ChildFaultError, which the shared path pipeline renders as worker_misconfigured).
# The Mac InferenceChild stays as it is on purpose: it is built on POSIX file
# descriptors, select() and process groups, which do not exist on Windows, and
# rewriting it onto this class would change a worker that is proven in production
# (the 60 Mac pin tests pin its behaviour).
#
# HOW: multiprocessing with the "spawn" start method, the only one Windows has, so
# this behaves the same everywhere and never forks a process that already has
# threads and a loaded model. A multiprocessing Pipe carries the protocol (not
# stdin/stdout: the model's own output must not be able to corrupt it). The
# backend is named by "module:attr" and built INSIDE the child from a
# JSON-serialisable argument, so nothing heavy is pickled and tests can point the
# child at a fake backend that hangs, crashes or exits.
#
# Protocol, child to parent: {"ready": True, "meta": {...}} once the backend is
# built, or {"fatal": "..."} followed by exit if it could not be; then one
# {"text": ...} or {"error": ...} per request {"path": ...}.
#
# Exit codes: POSIX reports a signal death as a negative code, so SIGKILL/SIGTERM
# (an operator, an OOM killer) read as decoder_killed and anything else as
# decoder_crashed. Windows has no signals: an externally terminated process has an
# arbitrary code there, so on Windows a death on a clip is always decoder_crashed.

import contextlib
import importlib
import json
import logging
import multiprocessing
import os
import signal
import sys
import threading
import time
from typing import Any, Optional

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from whisper_path_pipeline import (  # noqa: E402
    ERR_DECODER_CRASHED,
    ERR_DECODER_KILLED,
    ERR_TRANSCRIBE_TIMEOUT,
    ChildFaultError,
    PathJobError,
)

log = logging.getLogger("whisper_decode_child")

ORPHAN_POLL_S = 2.0


def _resolve_factory(spec: str):
    module, sep, attr = spec.partition(":")
    if not sep or not module or not attr:
        raise ValueError(f"backend factory {spec!r} is not module:attr")
    return getattr(importlib.import_module(module), attr)


def _child_main(conn, factory_spec: str, factory_arg: dict) -> None:
    """Entry point of the child process (picklable: a module-level function)."""
    parent = multiprocessing.parent_process()

    def orphan_watch() -> None:
        # A child stuck in a decode never reads the pipe, so EOF alone would leave
        # it spinning forever if the parent was killed.
        while True:
            time.sleep(ORPHAN_POLL_S)
            if parent is None or not parent.is_alive():
                os._exit(0)

    threading.Thread(target=orphan_watch, daemon=True).start()
    try:
        backend = _resolve_factory(factory_spec)(factory_arg)
    except BaseException as e:  # noqa: BLE001 - reported, then the child exits
        with contextlib.suppress(Exception):
            conn.send({"fatal": f"{type(e).__name__}: {e}"})
        raise SystemExit(3)
    conn.send({"ready": True, "meta": dict(getattr(backend, "meta", {}) or {})})
    while True:
        try:
            msg = conn.recv()
        except (EOFError, OSError):
            return
        try:
            conn.send({"text": backend.transcribe(msg["path"])})
        except Exception as e:  # noqa: BLE001 - becomes this clip's error
            conn.send({"error": f"{type(e).__name__}: {e}"})


class DecodeChild:
    """One long-lived child holding the model, with a deadline per call.

    Callers serialise on their own lock (the Engine gives each child to one
    request at a time); this class is not re-entrant. busy(), start_error() and
    meta() are read from OTHER threads (/health) under tiny mutexes that are never
    held across a call, so a wedged decode cannot hide itself."""

    def __init__(
        self,
        factory_spec: str,
        factory_arg: dict,
        *,
        ready_timeout_s: float = 300.0,
        fault_ttl_s: float = 300.0,
    ) -> None:
        self._spec = factory_spec
        self._arg = factory_arg
        self.ready_timeout_s = ready_timeout_s
        self.fault_ttl_s = fault_ttl_s
        self._ctx = multiprocessing.get_context("spawn")
        self._proc: Any = None
        self._conn: Any = None
        self._mu = threading.Lock()
        self._busy_since: Optional[float] = None
        self._busy_limit: Optional[float] = None
        self._start_error: Optional[str] = None
        self._start_error_at = 0.0
        self._meta: dict = {}
        self.starts = 0

    # ── liveness, for /health ────────────────────────────────────────────────

    def busy(self) -> "tuple[float, Optional[float]]":
        with self._mu:
            if self._busy_since is None:
                return 0.0, None
            return time.monotonic() - self._busy_since, self._busy_limit

    def _set_busy(self, limit: Optional[float]) -> None:
        with self._mu:
            self._busy_since = None if limit is None else time.monotonic()
            self._busy_limit = limit

    def start_error(self) -> Optional[str]:
        """The last failure to START the child within fault_ttl_s with no start
        succeeding since, else None. It expires so a worker the client refused for
        it is sent the request that starts the child again."""
        with self._mu:
            if (
                self._start_error is not None
                and time.monotonic() - self._start_error_at < self.fault_ttl_s
            ):
                return self._start_error
            return None

    def _note_start(self, error: Optional[str]) -> None:
        with self._mu:
            self._start_error = error
            self._start_error_at = time.monotonic()

    def meta(self) -> dict:
        """What the most recent ready child said about itself (device, compute
        type, ...). Empty before the first start."""
        with self._mu:
            return dict(self._meta)

    # ── lifecycle ────────────────────────────────────────────────────────────

    def alive(self) -> bool:
        return self._proc is not None and self._proc.is_alive()

    def _kill(self) -> None:
        proc, conn = self._proc, self._conn
        self._proc = self._conn = None
        if proc is not None:
            with contextlib.suppress(Exception):
                proc.kill()  # SIGKILL on POSIX, TerminateProcess on Windows
            with contextlib.suppress(Exception):
                proc.join(30)
        if conn is not None:
            with contextlib.suppress(Exception):
                conn.close()

    def shutdown(self) -> None:
        self._kill()

    def _exit_code(self, proc: Any) -> Optional[int]:
        """The exit code after the pipe closed. The pipe closes a moment before the
        process can be reaped, so join briefly."""
        with contextlib.suppress(Exception):
            proc.join(2)
        return proc.exitcode if proc is not None else None

    def start(self, ready_timeout_s: Optional[float] = None) -> dict:
        """Start a fresh child and wait for its ready message. Returns its meta, or
        raises ChildFaultError(starting=True). The model loads HERE, before any
        clip's clock starts: a clip must not time out because the model was slow
        to load."""
        wait = self.ready_timeout_s if ready_timeout_s is None else ready_timeout_s
        self._kill()
        try:
            self._fault_free_start(wait)
        except ChildFaultError as e:
            self._note_start(str(e))
            raise
        self._note_start(None)
        return self.meta()

    def _fault_free_start(self, wait: float) -> None:
        parent_conn, child_conn = self._ctx.Pipe(duplex=True)
        proc = self._ctx.Process(
            target=_child_main, args=(child_conn, self._spec, self._arg), daemon=True
        )
        try:
            proc.start()
        except Exception as e:
            parent_conn.close()
            child_conn.close()
            raise ChildFaultError(f"decode process could not be started: {e}", starting=True)
        child_conn.close()  # the child's end is the child's now (EOF needs this)
        self._proc, self._conn = proc, parent_conn
        self.starts += 1
        try:
            if not parent_conn.poll(wait):
                self._kill()
                raise ChildFaultError(f"decode process not ready after {wait:.0f}s", starting=True)
            msg = parent_conn.recv()
        except (EOFError, OSError):
            code = self._exit_code(proc)
            self._kill()
            raise ChildFaultError(
                f"decode process exited while starting (exit code {code})", starting=True
            )
        if msg.get("fatal"):
            self._kill()
            raise ChildFaultError(
                f"decode process could not load its model: {msg['fatal']}", starting=True
            )
        if not msg.get("ready"):
            self._kill()
            raise ChildFaultError(
                "decode process sent an unexpected startup message", starting=True
            )
        with self._mu:
            self._meta = dict(msg.get("meta") or {})

    def _ensure_child(self) -> None:
        if self.alive():
            return
        self.start()

    def run(self, wav_path: str, timeout_s: float) -> str:
        """Transcribe the WAV at `wav_path` under `timeout_s`.

        Raises PathJobError(transcribe_timeout) when the child does not answer in
        time (it is killed; the next call starts a fresh one),
        PathJobError(decoder_crashed) when it dies on the clip by its own fault,
        PathJobError(decoder_killed) when it was killed from outside or could not
        be reaped, ChildFaultError when a child cannot be started, and
        RuntimeError when the backend reports an error for the clip."""
        self._ensure_child()
        proc, conn = self._proc, self._conn
        self._set_busy(timeout_s)
        try:
            try:
                conn.send({"path": wav_path})
                if not conn.poll(timeout_s):
                    self._kill()
                    log.error(
                        "decode exceeded %.0fs on %s; child killed",
                        timeout_s,
                        os.path.basename(wav_path),
                    )
                    raise PathJobError(
                        ERR_TRANSCRIBE_TIMEOUT,
                        f"no result within {timeout_s:.0f}s; the decode process was killed "
                        "and restarted",
                    )
                msg = conn.recv()
            except (EOFError, OSError):
                code = self._exit_code(proc)
                self._kill()
                # Killed from OUTSIDE the decoder (an operator, an out-of-memory
                # killer), or not reapable: not evidence the clip did it.
                if code is None or (
                    sys.platform != "win32" and code in (-signal.SIGKILL, -signal.SIGTERM)
                ):
                    raise PathJobError(
                        ERR_DECODER_KILLED,
                        f"the decode process was killed from outside (exit code {code}) "
                        "while on this clip; it was restarted",
                    )
                raise PathJobError(
                    ERR_DECODER_CRASHED,
                    f"the decode process died (exit code {code}) on this clip; it was restarted",
                )
            if msg.get("error"):
                raise RuntimeError(str(msg["error"]))
            return (msg.get("text") or "").strip()
        finally:
            self._set_busy(None)


def encode_arg(obj: Any) -> dict:
    """Round-trip through JSON so a factory argument is plain data, never a live
    object (it is pickled to the child)."""
    return json.loads(json.dumps(obj))
