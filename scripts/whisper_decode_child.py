# file: scripts/whisper_decode_child.py
# version: 2.3.0
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
# Every request carries an id the child echoes and the parent checks, and ANY
# exception between send and receive kills the child (see run): a stale reply must
# never be read as the next clip's answer, which would hand one book another book's
# transcript.
#
# Startup pin: the first child that becomes ready fixes the model, device and compute
# type this worker serves (StartPin). Every later child and every restart is started
# with those values EXPLICITLY and is refused if it reports anything else, so a
# restart can never quietly fall from large-v2 on CUDA to base.en on the CPU and file
# base.en text under large-v2's journal key.
#
# Shutdown: the child ignores SIGINT and (POSIX) lives in its own session, like the
# Mac child, so Ctrl-C stops the worker without the terminal reaching the decode. The
# child is killed by Engine.shutdown(), which runs after the web server has returned;
# the server's default graceful stop waits for requests in flight, so a stop during a
# hung decode can take up to that clip's deadline unless the worker is stopped a
# second time. A stop does not answer a clip decoder_killed, and a worker killed
# outright gives its caller a dropped connection.
#
# Parent death: the child exits when its pipe closes or the orphan watch sees the
# parent gone; on Windows every child is also put in a Job Object with
# KILL_ON_JOB_CLOSE, so it dies with the parent even while stuck in a native call
# that holds the GIL (which would stop the orphan watch from running).
#
# Exit codes: POSIX reports a signal death as a negative code, so SIGKILL/SIGTERM
# (an operator, an OOM killer) read as decoder_killed and anything else as
# decoder_crashed. Windows has no signals: an externally terminated process has an
# arbitrary code there, so on Windows a death on a clip is always decoder_crashed.

import contextlib
import importlib
import logging
import multiprocessing
import os
import signal
import sys
import threading
import time
from typing import Any, Callable, Optional

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
    # Ctrl-C belongs to the parent, which decides what the worker does about it.
    with contextlib.suppress(Exception):
        signal.signal(signal.SIGINT, signal.SIG_IGN)
    if sys.platform != "win32":
        # Its own session, so a terminal's SIGINT/SIGHUP to the worker's process
        # group never reaches a decode that is mid-clip.
        with contextlib.suppress(OSError):
            os.setsid()
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
    meta = dict(getattr(backend, "meta", {}) or {})
    meta.setdefault("pid", os.getpid())  # the REAL interpreter, not a launcher
    conn.send({"ready": True, "meta": meta})
    while True:
        try:
            msg = conn.recv()
        except (EOFError, OSError):
            return
        try:
            conn.send({"id": msg.get("id"), "text": backend.transcribe(msg["path"])})
        except Exception as e:  # noqa: BLE001 - becomes this clip's error
            conn.send({"id": msg.get("id"), "error": f"{type(e).__name__}: {e}"})


# ── Windows: children die with the parent ────────────────────────────────────

_JOB: Any = None
_JOB_LOCK = threading.Lock()


def _kill_on_close_job() -> Any:
    """One Job Object for the life of this process, set to kill every process in it
    when its last handle closes, which is when this process exits however it exits.
    ctypes only, no dependency. Raises OSError if Windows refuses."""
    global _JOB
    import ctypes
    from ctypes import wintypes

    with _JOB_LOCK:
        if _JOB is not None:
            return _JOB
        k32 = ctypes.WinDLL("kernel32", use_last_error=True)
        k32.CreateJobObjectW.restype = wintypes.HANDLE
        k32.CreateJobObjectW.argtypes = [wintypes.LPVOID, wintypes.LPCWSTR]
        k32.SetInformationJobObject.argtypes = [
            wintypes.HANDLE,
            ctypes.c_int,
            wintypes.LPVOID,
            wintypes.DWORD,
        ]
        k32.CloseHandle.argtypes = [wintypes.HANDLE]
        job = k32.CreateJobObjectW(None, None)
        if not job:
            raise ctypes.WinError(ctypes.get_last_error())

        class BasicLimits(ctypes.Structure):
            _fields_ = [
                ("PerProcessUserTimeLimit", wintypes.LARGE_INTEGER),
                ("PerJobUserTimeLimit", wintypes.LARGE_INTEGER),
                ("LimitFlags", wintypes.DWORD),
                ("MinimumWorkingSetSize", ctypes.c_size_t),
                ("MaximumWorkingSetSize", ctypes.c_size_t),
                ("ActiveProcessLimit", wintypes.DWORD),
                ("Affinity", ctypes.c_size_t),
                ("PriorityClass", wintypes.DWORD),
                ("SchedulingClass", wintypes.DWORD),
            ]

        class IoCounters(ctypes.Structure):
            _fields_ = [(n, ctypes.c_ulonglong) for n in (
                "ReadOperationCount", "WriteOperationCount", "OtherOperationCount",
                "ReadTransferCount", "WriteTransferCount", "OtherTransferCount",
            )]  # fmt: skip

        class ExtendedLimits(ctypes.Structure):
            _fields_ = [
                ("BasicLimitInformation", BasicLimits),
                ("IoInfo", IoCounters),
                ("ProcessMemoryLimit", ctypes.c_size_t),
                ("JobMemoryLimit", ctypes.c_size_t),
                ("PeakProcessMemoryUsed", ctypes.c_size_t),
                ("PeakJobMemoryUsed", ctypes.c_size_t),
            ]

        info = ExtendedLimits()
        info.BasicLimitInformation.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        # 9 = JobObjectExtendedLimitInformation
        if not k32.SetInformationJobObject(job, 9, ctypes.byref(info), ctypes.sizeof(info)):
            # The error is read first (CloseHandle may overwrite it), and the handle is
            # closed: _JOB is still None, so the next start would otherwise create and
            # leak another.
            err = ctypes.WinError(ctypes.get_last_error())
            k32.CloseHandle(job)
            raise err
        _JOB = job
        return job


def _assign_to_job(pid: int) -> bool:
    """Put process `pid` in the kill-on-close job; True when it is in. Failure is
    logged and returned, not fatal: the orphan watch and pipe EOF still cover every
    case but a native call that holds the GIL. The caller reports it (DecodeChild
    .unprotected, shown on /health), because a worker running without the job looks
    exactly like one with it until the day its parent dies."""
    import ctypes
    from ctypes import wintypes

    try:
        job = _kill_on_close_job()
        k32 = ctypes.WinDLL("kernel32", use_last_error=True)
        k32.OpenProcess.restype = wintypes.HANDLE
        k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        k32.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        k32.CloseHandle.argtypes = [wintypes.HANDLE]
        # PROCESS_SET_QUOTA | PROCESS_TERMINATE
        handle = k32.OpenProcess(0x0100 | 0x0001, False, pid)
        if not handle:
            raise ctypes.WinError(ctypes.get_last_error())
        try:
            if not k32.AssignProcessToJobObject(job, handle):
                raise ctypes.WinError(ctypes.get_last_error())
        finally:
            k32.CloseHandle(handle)
        return True
    except Exception as e:  # noqa: BLE001
        log.warning("could not put decode process %s in a kill-on-close job: %s", pid, e)
        return False


class StartPin:
    """The model, device and compute type this worker serves, fixed by the first
    child that becomes ready. Shared by every DecodeChild of one worker."""

    # `batched` is here because the decode fingerprint folds it in: a restart whose
    # batched pipeline failed to build would decode by beam search under the batched
    # worker's fingerprint, and its text would be filed as the batched decoder's.
    KEYS = ("model", "device", "compute_type", "batched")

    def __init__(self) -> None:
        self._mu = threading.Lock()
        self._meta: Optional[dict] = None

    def values(self) -> Optional[dict]:
        with self._mu:
            return None if self._meta is None else {k: self._meta.get(k) for k in self.KEYS}

    def meta(self) -> Optional[dict]:
        """The FULL meta of the first successful start (what /health reports)."""
        with self._mu:
            return None if self._meta is None else dict(self._meta)

    def check_and_record(self, meta: dict) -> Optional[str]:
        """None if `meta` is acceptable (and recorded when it is the first), else the
        reason it is not."""
        with self._mu:
            if self._meta is None:
                self._meta = dict(meta)
                return None
            want = {k: self._meta.get(k) for k in self.KEYS}
            got = {k: meta.get(k) for k in self.KEYS}
            if want != got:
                return (
                    f"it loaded {got} but this worker is pinned to {want} (the first start's "
                    "resolution); a restart never changes the model silently"
                )
            return None


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
        pin: Optional[StartPin] = None,
        pin_arg: Optional[Callable[[dict, dict], dict]] = None,
    ) -> None:
        self._spec = factory_spec
        self._arg = factory_arg
        # With a pin, every start after the first resolution gets
        # pin_arg(base_arg, pinned_values) as its factory argument and must report
        # the pinned values back.
        self._pin = pin
        self._pin_arg = pin_arg
        self._req_id = 0
        # True while the most recent child started could NOT be put in the Windows
        # kill-on-close job (see _protect); shown on /health as child_unprotected.
        self._unprotected = False
        self.ready_timeout_s = ready_timeout_s
        self.fault_ttl_s = fault_ttl_s
        self._ctx = multiprocessing.get_context("spawn")
        self._proc: Any = None
        self._conn: Any = None
        self._mu = threading.Lock()
        self._busy_since: Optional[float] = None
        self._busy_limit: Optional[float] = None
        self._start_error: Optional[str] = None  # a short category, safe for /health
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
        """A short category of the last failure to START the child (for the
        unauthenticated /health: never the exception text, which is in the log), within
        fault_ttl_s with no start succeeding since, else None. It expires so a worker the client refused for
        it is sent the request that starts the child again."""
        with self._mu:
            if (
                self._start_error is not None
                and time.monotonic() - self._start_error_at < self.fault_ttl_s
            ):
                return self._start_error
            return None

    def unprotected(self) -> bool:
        """True when the last child started is running WITHOUT the Windows job object
        that kills it with this process (the assignment failed; the warning is in the
        log). It then relies on the orphan watch alone, which a decode that holds the
        GIL defeats. Always False off Windows, where there is no job to lack."""
        with self._mu:
            return self._unprotected

    @staticmethod
    def _protect(pid: Optional[int]) -> bool:
        """Tie a started child's life to this process; False when that failed."""
        if sys.platform != "win32" or pid is None:
            return True
        return _assign_to_job(pid)

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
            log.warning("decode process failed to start (%s): %s", e.category, e)
            self._note_start(f"worker_misconfigured: {e.category}")
            raise
        self._note_start(None)
        return self.meta()

    def _fault_free_start(self, wait: float) -> None:
        parent_conn, child_conn = self._ctx.Pipe(duplex=True)
        arg = self._arg
        if self._pin is not None and self._pin_arg is not None:
            pinned = self._pin.values()
            if pinned is not None:
                arg = self._pin_arg(self._arg, pinned)
        proc = self._ctx.Process(
            target=_child_main, args=(child_conn, self._spec, arg), daemon=True
        )
        try:
            proc.start()
        except Exception as e:
            parent_conn.close()
            child_conn.close()
            raise ChildFaultError(
                f"decode process could not be started: {e}",
                starting=True,
                category="process_start_failed",
            )
        child_conn.close()  # the child's end is the child's now (EOF needs this)
        protected = self._protect(proc.pid)
        with self._mu:
            self._unprotected = not protected
        self._proc, self._conn = proc, parent_conn
        self.starts += 1
        try:
            if not parent_conn.poll(wait):
                self._kill()
                raise ChildFaultError(
                    f"decode process not ready after {wait:.0f}s",
                    starting=True,
                    category="start_timeout",
                )
            msg = parent_conn.recv()
        except (EOFError, OSError):
            code = self._exit_code(proc)
            self._kill()
            raise ChildFaultError(
                f"decode process exited while starting (exit code {code})",
                starting=True,
                category="exited_during_start",
            )
        if msg.get("fatal"):
            self._kill()
            raise ChildFaultError(
                f"decode process could not load its model: {msg['fatal']}",
                starting=True,
                category="model_load_failed",
            )
        if not msg.get("ready"):
            self._kill()
            raise ChildFaultError(
                "decode process sent an unexpected startup message",
                starting=True,
                category="protocol_error",
            )
        meta = dict(msg.get("meta") or {})
        if self._pin is not None:
            why = self._pin.check_and_record(meta)
            if why:
                self._kill()
                raise ChildFaultError(
                    f"decode process refused: {why}",
                    starting=True,
                    category="pinned_model_mismatch",
                )
        log.info(
            "decode process %s ready: model=%s device=%s compute_type=%s fallback=%s (%s)",
            meta.get("pid"),
            meta.get("model"),
            meta.get("device"),
            meta.get("compute_type"),
            meta.get("fallback"),
            meta.get("device_reason"),
        )
        with self._mu:
            self._meta = meta

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
        # Busy from the moment the request arrives, INCLUDING while a child is
        # (re)started: /health must never say idle while a model load is in progress
        # for a request. The limit covers the load, then the decode gets its own.
        self._set_busy(self.ready_timeout_s + timeout_s)
        try:
            self._ensure_child()
        except BaseException:
            self._set_busy(None)
            raise
        proc, conn = self._proc, self._conn
        self._req_id += 1
        req_id = self._req_id
        self._set_busy(timeout_s)
        try:
            try:
                conn.send({"id": req_id, "path": wav_path})
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
            except PathJobError:
                raise
            except BaseException:
                # Anything else between send and receive (an overflowing timeout, an
                # unpickling error, a KeyboardInterrupt) leaves the child working on a
                # request nobody will read; its answer would then be taken for the
                # NEXT clip's. Kill it so the next clip starts a clean child.
                self._kill()
                raise
            if msg.get("id") != req_id:
                # Second line of defence: an answer to some other request.
                self._kill()
                raise PathJobError(
                    ERR_DECODER_CRASHED,
                    "the decode process answered a different request; it was restarted",
                )
            if msg.get("error"):
                raise RuntimeError(str(msg["error"]))
            return (msg.get("text") or "").strip()
        finally:
            self._set_busy(None)
