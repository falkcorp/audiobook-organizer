# file: scripts/whisper_mlx_server.py
# version: 1.12.0
# guid: 3f8c21d4-7b6e-4a52-9c18-2d5e7a9b4c60
# last-edited: 2026-10-26
#
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "mlx-whisper>=0.4.0",
#   "fastapi>=0.111",
#   "uvicorn[standard]>=0.29",
#   "python-multipart>=0.0.9",
# ]
# ///
#
# Mac-only Whisper transcription worker using Apple MLX (Metal).
#
# WHY THIS EXISTS AS A SEPARATE SERVER, and not a device flag on
# scripts/whisper_server.py: that server runs faster-whisper, which runs on
# ctranslate2, which has NO Metal/MPS backend. Its own _resolve_device docstring
# says so. Pointing it at Apple Silicon does not fail -- it silently runs on
# CPU, which is the worst outcome because it looks like it worked. MLX is a
# different inference stack entirely, so it gets a different server.
#
# It speaks the EXACT protocol internal/transcribe/remote.go expects:
#   GET  /health           -> {"batch_pipeline": true, ...}
#   POST /transcribe       -> multipart field "file",  {"text","error"}
#   POST /transcribe-batch -> multipart field "files", {"results":{...}}
# and, for the Mac-side extraction path (docs/whisper-remote-extract.md):
#   POST /transcribe-path       -> JSON {"path","seconds"}, {"text","error"}
#   POST /transcribe-path-batch -> JSON {"jobs":{key:{"path","seconds"}}},
#                                  {"results":{key:{"text","error"}}}
# Those take a SERVER path, map it to this Mac's NFS mount (WHISPER_PATH_MAP),
# and extract the clip HERE with ffmpeg, so the production server never decodes
# audio. The upload endpoints above are unchanged.
# The batch result key is the PART'S FILENAME echoed verbatim -- the Go client
# sends the book ID as the filename and looks the result up by it (see
# sendBatch's comment). Do not basename, sanitize or normalise it.
#
# CAPACITY ONLY. When this worker is offline the Go service treats it as absent
# capacity, not as permission to fall back; queued work stays durable. Do not
# enable AI parsing on account of this server.
#
# Run:
#   uv run scripts/whisper_mlx_server.py [model-repo]
#   uv run scripts/whisper_mlx_server.py mlx-community/whisper-small.en-mlx
#
# Binds LOOPBACK by default, unlike the CUDA server which binds 0.0.0.0.
# Exposing this to the LAN is a deliberate, separate decision -- set
# WHISPER_BIND=0.0.0.0 only alongside a firewall rule.
#
# Environment:
#   WHISPER_MLX_MODEL   model repo (default mlx-community/whisper-small.en-mlx)
#   WHISPER_BIND        bind address (default 127.0.0.1)
#   WHISPER_PORT        port (default 19848 -- one above the CUDA server's
#                       19847, so both can run on one host without colliding)
#   WHISPER_PATH_MAP    server-path=local-path pairs, comma separated, e.g.
#                       /mnt/bigdata=/Users/<you>/mnt/bigdata . REQUIRED for the
#                       /transcribe-path* endpoints and deliberately has NO
#                       default: unset means path_pipeline is false and every
#                       path job is refused.
#   WHISPER_EXTRACT_TIMEOUT  per-clip ffmpeg timeout, seconds (default 120)
#   WHISPER_EXTRACT_WORKERS  parallel ffmpeg extractions (default 4)
#   WHISPER_TRANSCRIBE_MIN_TIMEOUT_S  floor of the per-clip inference time limit,
#                       seconds (default 60)
#   WHISPER_TRANSCRIBE_TIMEOUT_FACTOR  per-clip limit = max(floor, factor x the
#                       clip's audio seconds) (default 4, so 360 s for a 90 s clip)
#   WHISPER_CHILD_READY_TIMEOUT_S  how long a freshly started inference child may
#                       take to load the model before it counts as failed
#                       (default 300). Model load is NOT part of the clip's limit.
#   WHISPER_CHILD_FAULT_TTL_S  how long /health keeps reporting child_start_error
#                       after the inference child failed to start (default 300).
#                       A successful start clears it at once; the expiry is what
#                       lets a worker the client refused be sent a request again.
#   WHISPER_BACKEND_FACTORY  "module:callable" returning a transcribe(path)->str
#                       function for the inference child to use INSTEAD of
#                       mlx_whisper. A test seam: lets the contract tests run a
#                       real child process (and kill it) without Apple silicon.
#
# INFERENCE RUNS IN A KILLABLE CHILD PROCESS. mlx_whisper can enter an endless
# decode loop on a particular clip (its seek keeps rewinding and never reaches
# the end), and an in-process call cannot be interrupted: the old design held
# the process-wide lock forever and every later request parked behind it. The
# model now lives in a child process; the parent puts a per-clip deadline on
# each call, and on expiry SIGKILLs the child and returns the per-file error
# "transcribe_timeout: ...". The next request starts a fresh child. A child that
# DIES on a clip is reported as "decoder_crashed: ..." and restarted the same way,
# unless it was killed from OUTSIDE (SIGKILL/SIGTERM, or never reaped): that is
# "decoder_killed: ...". One kill says nothing certain about the clip (macOS
# jetsam, an operator), but a clip whose decode grows until it is killed does so
# on every worker, so the Go side counts a kill as a timeout strike that turns
# permanent only when a second, DIFFERENT machine also fails on the clip.
#
# A batch request may carry X-Whisper-Request-Timeout-S: the ceiling after which
# the client gives up on it. Once that passes the worker stops starting clips for
# the request (a client that left gets no answer, and decoding on would hold the
# inference lock against everyone queued behind it).

import asyncio
import atexit
import concurrent.futures
import contextlib
import errno
import hashlib
import importlib
import json
import logging
import os
import posixpath
import select
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import wave
from typing import Annotated, Any, List

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_mlx_server")

try:
    from fastapi import Body, FastAPI, File, HTTPException, Request, UploadFile
except ImportError as e:  # pragma: no cover - dependency probe
    print(f"Missing dependency: {e}")
    print("Install with: uv run scripts/whisper_mlx_server.py")
    sys.exit(1)

DEFAULT_MODEL = "mlx-community/whisper-small.en-mlx"
MODEL_REPO = (
    sys.argv[1]
    if len(sys.argv) > 1 and not sys.argv[1].startswith("-")
    else os.environ.get("WHISPER_MLX_MODEL", DEFAULT_MODEL)
)

LANGUAGE = "en"
CONDITION_ON_PREVIOUS_TEXT = False

# DECODE_FINGERPRINT is a short hash of every setting that changes the text
# this server returns for the same WAV bytes. The Go client folds it into the
# whisper result-journal key (internal/transcribe/journal.go), so retuning any
# of these -- a VAD threshold, the model, the decode path -- makes previously
# journalled transcripts unreachable instead of being served for the new
# configuration. Add a setting here whenever one is added to the decode call.
DECODE_SETTINGS = {
    "backend": "mlx",
    "model": MODEL_REPO,
    "language": LANGUAGE,
    "condition_on_previous_text": CONDITION_ON_PREVIOUS_TEXT,
}
DECODE_FINGERPRINT = hashlib.sha256(
    json.dumps(DECODE_SETTINGS, sort_keys=True).encode()
).hexdigest()[:16]

# WORKER IDENTITY. The Go client makes a file's decoder-timeout result
# permanent only when two DIFFERENT machines fail on it, so one sick worker can
# never condemn a file. Several endpoints can be one machine (two server
# processes on two ports, or two SSH tunnels to the same box), so the client
# cannot tell machines apart by URL; /health reports machine_id instead. It is a
# short hash, never the raw name: the Go side stores it on the file row and
# shows it in API responses, and a hostname or machine UUID has no business
# there. Every process on one machine reports the same value. Sources, first
# that answers: WHISPER_MACHINE_ID (an operator override, hashed like the rest),
# /etc/machine-id (Linux), the macOS IOPlatformUUID, then the hostname (last,
# because a Mac's hostname can change with the network it joins).


def _machine_source() -> str:
    override = os.environ.get("WHISPER_MACHINE_ID", "").strip()
    if override:
        return "override:" + override
    with contextlib.suppress(OSError), open("/etc/machine-id", encoding="ascii") as f:
        mid = f.read().strip()
        if mid:
            return "machine-id:" + mid
    if sys.platform == "darwin":
        with contextlib.suppress(OSError, subprocess.SubprocessError):
            out = subprocess.run(
                ["ioreg", "-rd1", "-c", "IOPlatformExpertDevice"],
                capture_output=True,
                text=True,
                timeout=5,
                check=False,
            ).stdout
            for line in out.splitlines():
                if "IOPlatformUUID" in line and "=" in line:
                    uuid = line.split("=", 1)[1].strip().strip('"')
                    if uuid:
                        return "platform-uuid:" + uuid
    return "hostname:" + socket.gethostname()


def machine_id() -> str:
    """The opaque identity of the machine this worker runs on (see above)."""
    return hashlib.sha256(_machine_source().encode()).hexdigest()[:16]


MACHINE_ID = machine_id()

# Inference is serialized. MLX shares the Mac's unified memory with everything
# else the desktop is doing, so two concurrent transcriptions do not run twice
# as fast -- they contend for the same memory bandwidth and make the machine
# unusable. The Go client's batch path already sends files in one request and
# expects sequential server-side processing, so this costs no throughput.
_inference_lock = threading.Lock()


def _require_ffmpeg() -> str:
    """Refuse to start without ffmpeg. Returns its resolved path.

    mlx_whisper shells out to `ffmpeg` to decode audio. Without it EVERY
    transcription fails -- but it fails *inside* a 200 response, as
    {"text": "", "error": "[Errno 2] ... 'ffmpeg'"}, because the batch protocol
    carries per-file errors in the body. /health stays "ok", the transport looks
    perfectly healthy, and the caller records a whisper_error per book.

    That is exactly what happened on 2026-08-31: 2,472 batch requests, all 200,
    21,443 books marked whisper_error, zero transcripts. Under launchd the PATH
    is the minimal default (no /opt/homebrew/bin), so a worker that worked when
    started from a shell failed as an agent.

    Exiting here is deliberate and fail-closed: no process means no /health,
    which means the dispatcher's capability gate refuses this endpoint and
    defers the page, instead of burning through the library writing errors.
    """
    found = shutil.which("ffmpeg")
    if found:
        return found
    log.error(
        "ffmpeg not found on PATH (%s) — mlx_whisper cannot decode audio and every "
        "transcription would fail inside a 200 response. Refusing to start. "
        "Add ffmpeg's directory to PATH; under launchd set it in EnvironmentVariables.",
        os.environ.get("PATH", ""),
    )
    raise SystemExit(1)


# ── Killable inference child ─────────────────────────────────────────────────

TRANSCRIBE_MIN_TIMEOUT_S = float(os.environ.get("WHISPER_TRANSCRIBE_MIN_TIMEOUT_S", "60"))
TRANSCRIBE_TIMEOUT_FACTOR = float(os.environ.get("WHISPER_TRANSCRIBE_TIMEOUT_FACTOR", "4"))
CHILD_READY_TIMEOUT_S = float(os.environ.get("WHISPER_CHILD_READY_TIMEOUT_S", "300"))
CHILD_FAULT_TTL_S = float(os.environ.get("WHISPER_CHILD_FAULT_TTL_S", "300"))
CHILD_FLAG = "--inference-child"
BACKEND_FACTORY_ENV = "WHISPER_BACKEND_FACTORY"

# Bytes per second of the 16 kHz mono 16-bit PCM this worker extracts; only used
# to estimate the length of an upload whose header cannot be read.
_PCM_BYTES_PER_S = 32000


def _audio_seconds(path: str) -> float:
    """Length of the clip in seconds, from the WAV header (frames / rate), not
    from the length the caller asked for: the clip can be shorter than asked
    (a file shorter than the clip length), capped by what the file's size can
    hold. Falls back to a size estimate for a file that is not a parseable WAV."""
    try:
        by_size = os.path.getsize(path) / _PCM_BYTES_PER_S
    except OSError:
        return 0.0
    try:
        with wave.open(path, "rb") as w:
            rate = w.getframerate()
            if rate > 0:
                # The header is the caller's claim, the size is the file's. A
                # header that claims more frames than the file can hold would
                # stretch the clip's deadline (a forged or truncated WAV), so the
                # deadline is never longer than the bytes on disk allow. The
                # clips this worker cuts are 16 kHz mono 16-bit, where the two
                # agree exactly.
                return min(w.getnframes() / rate, by_size)
    except (wave.Error, EOFError, OSError):
        pass
    return by_size


def _clip_timeout(path: str) -> float:
    """The per-clip inference time limit: max(floor, factor x audio seconds)."""
    return max(TRANSCRIBE_MIN_TIMEOUT_S, TRANSCRIBE_TIMEOUT_FACTOR * _audio_seconds(path))


class ChildFaultError(RuntimeError):
    """The inference child could not START: a fault of THIS WORKER, never a
    statement about the clip being decoded (no clip was ever sent to it).
    `starting` is True for exactly that case, and it is the case where every later
    clip in the same request would hit the same wall, so callers stop sending it
    clips. A child that DIES while decoding a clip is not this: that is a verdict
    about the clip (PathJobError, decoder_crashed), because a clip that
    deterministically kills the child must be able to settle. (A child killed from
    outside is PathJobError decoder_killed, a strike that needs a second machine, not a start fault.) The flag stays so
    a handler can tell the two apart if a start-less fault is ever raised again."""

    def __init__(self, message: str, *, starting: bool):
        super().__init__(message)
        self.starting = starting


class InferenceChild:
    """One long-lived child process that holds the model, with a deadline per call.

    Protocol: JSON lines. The parent writes {"path": ...} to the child's request
    pipe; the child answers {"text": ...} or {"error": ...} on its response pipe,
    and writes {"ready": true} once after the model is loaded. The pipes are
    dedicated file descriptors, NOT stdin/stdout: mlx and its download progress
    write to stdout/stderr, and mixing that into the protocol would corrupt it.

    Callers serialise on _inference_lock; this class is not re-entrant. The
    busy fields are the one thing read from OTHER threads (/health), under a
    tiny separate mutex that is never held across a call.
    """

    def __init__(self) -> None:
        self._proc: subprocess.Popen | None = None
        self._req_w: int | None = None
        self._resp_r: int | None = None
        self._buf = b""
        self._busy_mu = threading.Lock()
        self._busy_since: float | None = None
        self._busy_limit: float | None = None
        # The last failure to START the child, for /health. Its own tiny mutex,
        # never held across a call and never _inference_lock: a wedged job holds
        # that lock and /health must still be able to say the worker is faulted.
        self._start_mu = threading.Lock()
        self._start_error: str | None = None
        self._start_error_at = 0.0
        atexit.register(self.shutdown)

    # -- liveness, for /health ------------------------------------------------

    def busy(self) -> tuple[float, float | None]:
        """(seconds the current clip has been running, its limit). (0.0, None)
        when idle. Never blocks on inference."""
        with self._busy_mu:
            if self._busy_since is None:
                return 0.0, None
            return time.monotonic() - self._busy_since, self._busy_limit

    def _set_busy(self, limit: float | None) -> None:
        with self._busy_mu:
            self._busy_since = None if limit is None else time.monotonic()
            self._busy_limit = limit

    def start_error(self) -> str | None:
        """The message of a child start failure within the last CHILD_FAULT_TTL_S
        seconds with no start succeeding since, else None. Never blocks on
        inference. The expiry matters: the client refuses a worker that reports
        this, so without it a refused worker would never be sent the request that
        starts the child again."""
        with self._start_mu:
            if (
                self._start_error is not None
                and time.monotonic() - self._start_error_at < CHILD_FAULT_TTL_S
            ):
                return self._start_error
            return None

    def _note_start(self, error: str | None) -> None:
        with self._start_mu:
            self._start_error = error
            self._start_error_at = time.monotonic()

    # -- child lifecycle ------------------------------------------------------

    def _spawn(self) -> None:
        req_r, req_w = os.pipe()
        resp_r, resp_w = os.pipe()
        env = dict(os.environ)
        env["WHISPER_MLX_MODEL"] = MODEL_REPO
        try:
            proc = subprocess.Popen(
                [sys.executable, os.path.abspath(__file__), CHILD_FLAG, str(req_r), str(resp_w)],
                pass_fds=(req_r, resp_w),
                stdin=subprocess.DEVNULL,
                env=env,
                # Own session/process group so a kill takes the child's own
                # subprocesses (mlx_whisper shells out to ffmpeg) with it.
                start_new_session=True,
            )
        except BaseException:
            for fd in (req_r, req_w, resp_r, resp_w):
                os.close(fd)
            raise
        # The child's ends are the child's now.
        os.close(req_r)
        os.close(resp_w)
        self._proc, self._req_w, self._resp_r, self._buf = proc, req_w, resp_r, b""

    def _kill(self) -> None:
        """SIGKILL the child's whole group, reap it, drop the pipes. SIGKILL, not
        SIGTERM: a Metal kernel in flight can sit through a polite signal."""
        proc = self._proc
        if proc is not None:
            with contextlib.suppress(ProcessLookupError, PermissionError, OSError):
                os.killpg(proc.pid, signal.SIGKILL)
            with contextlib.suppress(ProcessLookupError, OSError):
                proc.kill()
            with contextlib.suppress(Exception):
                proc.wait(timeout=30)
        for fd in (self._req_w, self._resp_r):
            if fd is not None:
                with contextlib.suppress(OSError):
                    os.close(fd)
        self._proc = self._req_w = self._resp_r = None
        self._buf = b""

    def shutdown(self) -> None:
        self._kill()

    def _exit_code(self) -> int | None:
        """The child's exit code after its pipe closed. The pipe closes a moment
        before the process can be reaped, so a bare poll() usually says None."""
        proc = self._proc
        if proc is None:
            return None
        with contextlib.suppress(subprocess.TimeoutExpired):
            return proc.wait(timeout=2)
        return None

    def _readline(self, deadline: float) -> bytes | None:
        """One protocol line, or None when `deadline` (monotonic) passes first.
        Raises EOFError when the child closed its end (it died)."""
        while b"\n" not in self._buf:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return None
            ready, _, _ = select.select([self._resp_r], [], [], remaining)
            if not ready:
                return None
            chunk = os.read(self._resp_r, 65536)
            if not chunk:
                raise EOFError
            self._buf += chunk
        line, _, self._buf = self._buf.partition(b"\n")
        return line

    def _ensure_child(self) -> None:
        if self._proc is not None and self._proc.poll() is None:
            return
        try:
            self._start()
        except ChildFaultError as e:
            self._note_start(str(e))
            raise
        self._note_start(None)

    def _start(self) -> None:
        """Start a fresh child and wait for its ready line, or raise
        ChildFaultError(starting=True)."""
        self._kill()
        try:
            self._spawn()
        except OSError as e:
            raise ChildFaultError(f"inference process could not be started: {e}", starting=True)
        # Model load happens HERE, before the clip's clock starts: at a high
        # machine load it can eat most of a 60 s floor, and a clip must not time
        # out because the model was slow to load.
        try:
            line = self._readline(time.monotonic() + CHILD_READY_TIMEOUT_S)
        except EOFError:
            code = self._exit_code()
            self._kill()
            raise ChildFaultError(
                f"inference process exited while starting (exit code {code})", starting=True
            )
        if line is None:
            self._kill()
            raise ChildFaultError(
                f"inference process not ready after {CHILD_READY_TIMEOUT_S:.0f}s", starting=True
            )
        if not json.loads(line).get("ready"):
            self._kill()
            raise ChildFaultError(
                "inference process sent an unexpected startup message", starting=True
            )

    def run(self, path: str, timeout_s: float) -> str:
        """Transcribe `path`. Raises PathJobError(transcribe_timeout) when it does
        not answer in `timeout_s` (the child is killed; the next call starts a
        fresh one), PathJobError(decoder_crashed) when the child dies on the clip
        by its own fault (a crash signal or a non-zero exit; also restarted by the
        next call), PathJobError(decoder_killed) when it was killed from outside
        (SIGKILL/SIGTERM) or could not be reaped, ChildFaultError when the child
        cannot start (a worker fault), and RuntimeError when the backend reports an error
        for the clip."""
        self._ensure_child()
        self._set_busy(timeout_s)
        try:
            try:
                os.write(self._req_w, json.dumps({"path": path}).encode() + b"\n")
                line = self._readline(time.monotonic() + timeout_s)
            except (EOFError, BrokenPipeError):
                code = self._exit_code()
                self._kill()
                # The pipe closed while this clip was in flight. A SIGKILL or
                # SIGTERM comes from OUTSIDE the decoder (macOS jetsam under
                # memory pressure from another job on this box, an operator),
                # and a child that could not be reaped (code None) is not
                # evidence the clip killed it either. But a decode that grows
                # until jetsam kills it does so on every machine, so the Go side
                # counts this as a timeout strike that only a second, DIFFERENT
                # machine can make permanent (one sick worker never can).
                if code is None or code in (-signal.SIGKILL, -signal.SIGTERM):
                    raise PathJobError(
                        ERR_DECODER_KILLED,
                        f"the decode process was killed from outside (exit code {code}) "
                        "while on this clip; it was restarted",
                    )
                # Anything else (a crash signal such as SIGABRT/SIGSEGV/SIGBUS,
                # or a non-zero exit) is the decoder dying on the clip: a verdict
                # about the CLIP. A clip that does so every time must settle as
                # a strike on the Go side, like transcribe_timeout, not be
                # deferred as a worker fault forever.
                raise PathJobError(
                    ERR_DECODER_CRASHED,
                    f"the decode process died (exit code {code}) on this clip; it was restarted",
                )
            if line is None:
                self._kill()
                log.error(
                    f"inference exceeded {timeout_s:.0f}s on {os.path.basename(path)}; child killed"
                )
                raise PathJobError(
                    ERR_TRANSCRIBE_TIMEOUT,
                    f"no result within {timeout_s:.0f}s; the decode process was killed and restarted",
                )
            msg = json.loads(line)
            if msg.get("error"):
                raise RuntimeError(str(msg["error"]))
            return (msg.get("text") or "").strip()
        finally:
            self._set_busy(None)


_inference = InferenceChild()


def _mlx_backend():
    """The real backend: mlx_whisper, loaded in the child only."""
    import mlx_whisper

    # Best-effort preload so the model is in memory before the first clip's
    # clock starts. ModelHolder is an mlx_whisper internal, so any failure here
    # just means the first clip pays the load inside its own limit.
    try:
        import mlx.core as mx
        from mlx_whisper.transcribe import ModelHolder

        ModelHolder.get_model(MODEL_REPO, mx.float16)
    except Exception as e:  # pragma: no cover - Apple-silicon only
        print(f"model preload skipped: {e}", file=sys.stderr, flush=True)

    def transcribe(path: str) -> str:
        result = mlx_whisper.transcribe(
            path,
            path_or_hf_repo=MODEL_REPO,
            language=LANGUAGE,
            task="transcribe",
            # Audiobook intros are short and often start with music or a
            # publisher jingle. Conditioning on previous text makes Whisper
            # loop on those, emitting the same phrase repeatedly.
            condition_on_previous_text=CONDITION_ON_PREVIOUS_TEXT,
        )
        return (result.get("text") or "").strip()

    return transcribe


def _load_backend():
    spec = os.environ.get(BACKEND_FACTORY_ENV, "")
    if not spec:
        return _mlx_backend()
    module, _, attr = spec.partition(":")
    return getattr(importlib.import_module(module), attr)()


def _child_main(req_fd: int, resp_fd: int) -> None:
    """The inference child: load the model, say ready, then answer one request
    line at a time until the parent closes the pipe or goes away."""
    # Anything the model code prints to stdout must not reach the protocol.
    os.dup2(2, 1)
    parent = os.getppid()

    def orphan_watch() -> None:
        # A child stuck in a decode loop never reads the pipe, so EOF alone would
        # leave it spinning forever if the parent was killed.
        while True:
            time.sleep(2)
            if os.getppid() != parent:
                os._exit(0)

    threading.Thread(target=orphan_watch, daemon=True).start()
    req = os.fdopen(req_fd, "rb")
    resp = os.fdopen(resp_fd, "wb")

    def send(obj: dict) -> None:
        resp.write(json.dumps(obj).encode() + b"\n")
        resp.flush()

    try:
        transcribe = _load_backend()
    except Exception as e:
        print(f"inference child could not load its backend: {e}", file=sys.stderr, flush=True)
        raise SystemExit(3)
    send({"ready": True})
    for raw in req:
        try:
            path = json.loads(raw)["path"]
            send({"text": transcribe(path)})
        except Exception as e:
            send({"error": f"{type(e).__name__}: {e}"})


app = FastAPI()


def _transcribe_file(path: str) -> str:
    """Transcribe a WAV at `path` and return the text, under a per-clip deadline.

    The inference itself runs in the killable child (see InferenceChild). A clip
    that does not finish inside _clip_timeout(path) raises
    PathJobError("transcribe_timeout", ...) after the child has been killed.

    mlx_whisper is imported only in the CHILD, never here, on purpose: it is an
    Apple-Silicon-only dependency, and importing it in this module would make it
    unimportable on Linux -- taking the contract tests down with it. The tests
    replace this function wholesale (or point the child at a fake backend with
    WHISPER_BACKEND_FACTORY), so they exercise the real HTTP surface on any
    platform and only the inference call itself is Mac-gated.
    """
    limit = _clip_timeout(path)
    with _inference_lock:
        return _inference.run(path, limit)


def _do_transcribe(data: bytes, filename: str, on_start_fault=None) -> dict:
    """Run one file and return the {"text","error"} shape the Go client decodes.

    Never raises: a failure here must degrade to an error entry for THIS file
    so a single bad WAV cannot fail a 64-file batch. The Go side reads
    error != nil per result and keeps the rest.

    An inference child that cannot START is reported with the
    worker_misconfigured code (a fault of this worker, which the Go side defers
    without striking the file) and passed to `on_start_fault` so the caller can
    stop sending it clips. A child that DIES on this clip comes back from
    _transcribe_file as PathJobError(decoder_crashed), which the generic branch
    below renders as "decoder_crashed: ..." like any other coded failure.
    """
    tmp_path = None
    try:
        # mlx_whisper loads audio through ffmpeg from a PATH; it does not take
        # a file object, so the uploaded bytes have to land on disk first.
        with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tf:
            tf.write(data)
            tmp_path = tf.name
        text = _transcribe_file(tmp_path)
        log.info(f"transcribed {filename}: {len(text)} chars")
        return {"text": text, "error": None}
    except ChildFaultError as e:
        log.error(f"inference child fault for {filename}: {e}")
        if e.starting and on_start_fault is not None:
            on_start_fault(e)
        return _error_result(PathJobError(ERR_WORKER_MISCONFIGURED, str(e)))
    except Exception as e:
        log.error(f"transcription failed for {filename}: {e}")
        return {"text": "", "error": str(e)}
    finally:
        if tmp_path:
            with contextlib.suppress(OSError):
                os.unlink(tmp_path)


# Plain `def`, not `async def`, for both upload handlers: _do_transcribe blocks
# for as long as inference runs (it waits on _inference_lock and then on the
# child), and a blocking call inside an async handler freezes the event loop --
# /health and every other endpoint with it. As plain defs FastAPI runs them in
# its threadpool, and the file is read with the sync file object.
@app.post("/transcribe")
def transcribe(file: Annotated[UploadFile, File()]):
    """Single-file endpoint, used by the Go client's per-file fallback path."""
    data = file.file.read()
    return _do_transcribe(data, file.filename)


ABANDONED_DETAIL = "the client's request timeout passed before this clip started"
REQUEST_TIMEOUT_HEADER = "X-Whisper-Request-Timeout-S"
# Larger than any real ceiling; keeps time.monotonic() + n finite for a header
# of absurd length instead of raising OverflowError.
_MAX_REQUEST_TIMEOUT_S = 10**9


def _abandon_deadline(request: Request) -> float | None:
    """The monotonic time after which the client has given up on this request, from
    X-Whisper-Request-Timeout-S (whole seconds, counted from now: the handler's
    entry). A missing, empty, non-numeric or non-positive header is no deadline,
    which is today's behaviour: only ASCII digits are read, so "1.5", "-5" and
    "1_0" are ignored rather than half-understood."""
    raw = (request.headers.get(REQUEST_TIMEOUT_HEADER) or "").strip()
    if not (raw.isascii() and raw.isdigit()):
        return None
    seconds = min(int(raw), _MAX_REQUEST_TIMEOUT_S)
    if seconds <= 0:
        return None
    return time.monotonic() + seconds


def _past(deadline: float | None) -> bool:
    return deadline is not None and time.monotonic() >= deadline


def _abandoned_result() -> dict:
    return _error_result(PathJobError(ERR_REQUEST_ABANDONED, ABANDONED_DETAIL))


def _run_upload_batch(files, deadline: float | None = None) -> dict[str, dict]:
    """Run an upload batch sequentially; never raises. `files` are objects with
    .filename and a readable .file. Once `deadline` (monotonic) has passed, the
    clips not yet started are answered as abandoned without being run: the check is
    before STARTING a clip, never mid-clip."""
    results: dict[str, dict] = {}
    start_fault: list[ChildFaultError] = []
    for f in files:
        if _past(deadline):
            # The client gave up on this request and will never read the answer.
            results[f.filename] = _abandoned_result()
            continue
        if start_fault:
            # The child never became ready: every further clip would wait out the
            # same CHILD_READY_TIMEOUT_S. Answer the rest with the same fault.
            results[f.filename] = _error_result(
                PathJobError(ERR_WORKER_MISCONFIGURED, str(start_fault[0]))
            )
            continue
        results[f.filename] = _do_transcribe(f.file.read(), f.filename, start_fault.append)
    return results


@app.post("/transcribe-batch")
def transcribe_batch(files: Annotated[List[UploadFile], File()], request: Request):
    """Multi-file endpoint. Returns {"results": {"<filename>": {...}}}.

    The key is each part's filename VERBATIM -- the Go client sends the book ID
    there and looks the result up by it. Processing is sequential; the win over
    N single requests is reduced HTTP overhead, not parallelism.

    X-Whisper-Request-Timeout-S, when sent, is the client's HTTP ceiling: clips not
    yet started when it passes are answered as abandoned (see _run_upload_batch).
    """
    return {"results": _run_upload_batch(files, _abandon_deadline(request))}


# ── Path pipeline: extract the clip on THIS machine from a server path ───────
#
# The production server may not decode audio (standing rule), so it sends the
# PATH of a book's first audio file and this worker reads it through its own
# NFS mount, extracts the first N seconds, and transcribes that. Everything
# below fails CLOSED: an unmapped, traversing or unreadable path is a per-job
# error, never a guess.

PATH_MAP_ENV = "WHISPER_PATH_MAP"
DEFAULT_CLIP_SECONDS = 90
MAX_CLIP_SECONDS = 600
EXTRACT_TIMEOUT_S = int(os.environ.get("WHISPER_EXTRACT_TIMEOUT", "120"))
EXTRACT_WORKERS = max(1, int(os.environ.get("WHISPER_EXTRACT_WORKERS", "4")))

# Stable error CODES. internal/plugins/maintenance/intro_transcribe.go maps the
# prefix before the first ": " to a per-book status; free-form ffmpeg stderr
# is never parsed on the Go side. Keep the two lists in step.
#   path_unmapped   path refused, or its mapped root is not readable (lost mount)
#   not_found       mapped root readable but the file is not there
#   ffmpeg_error    ffmpeg failed or produced a WAV that does not parse: a DECODE
#                   verdict about the file (durable on the Go side)
#   ffmpeg_timeout  ffmpeg exceeded the timeout: NOT a verdict (NFS stall), retryable
#   bad_request     malformed job (bad seconds, missing path)
#   whisper_error   inference raised
#   container_rejected  ffmpeg refused the container (not on FORMAT_WHITELIST): a
#                   policy of this worker, not a verdict about the file; non-durable
#   transcribe_timeout  inference did not finish inside the per-clip limit (see
#                   InferenceChild) and its child process was killed. A verdict
#                   about the CLIP, not the worker: mlx_whisper loops forever on
#                   some clips. The Go side counts strikes per file and makes the
#                   result permanent at 2.
#   decoder_crashed  the inference child process DIED while decoding this clip
#                   (a crash or OOM kill; the next clip gets a fresh child). A
#                   verdict about the CLIP, like transcribe_timeout: a clip that
#                   kills the child every time must settle, so the Go side counts
#                   it on the same per-file strike counter.
#   decoder_killed  the inference child process was killed from OUTSIDE while on
#                   this clip (SIGKILL or SIGTERM: macOS jetsam under memory
#                   pressure from another job, an operator), or could not be
#                   reaped to tell. Not a verdict on its own: the Go side counts it
#                   as a timeout strike, and the file turns permanent only when a
#                   second, DIFFERENT machine (see machine_id in /health) also
#                   times out or is killed on it.
#   request_abandoned  the client's request timeout (X-Whisper-Request-Timeout-S)
#                   passed before this clip started, so it was never run. Not a
#                   verdict about the book: the client has already left, and the
#                   Go side treats it as no answer.
#   worker_misconfigured  this worker cannot do the job at all (ffmpeg not on
#                   PATH, or the inference process cannot START): a fault of the
#                   worker, never a verdict about a book; the Go side defers the
#                   book and writes nothing
ERR_PATH_UNMAPPED = "path_unmapped"
ERR_NOT_FOUND = "not_found"
ERR_FFMPEG = "ffmpeg_error"
ERR_FFMPEG_TIMEOUT = "ffmpeg_timeout"
ERR_BAD_REQUEST = "bad_request"
ERR_WHISPER = "whisper_error"
ERR_WORKER_MISCONFIGURED = "worker_misconfigured"
ERR_CONTAINER_REJECTED = "container_rejected"
ERR_TRANSCRIBE_TIMEOUT = "transcribe_timeout"
ERR_DECODER_CRASHED = "decoder_crashed"
ERR_DECODER_KILLED = "decoder_killed"
ERR_REQUEST_ABANDONED = "request_abandoned"

# Demuxers ffmpeg may use on a library file. An allow-list, not a deny-list: the
# share is writable by anyone who can drop a file in it, and a planted .m3u8 /
# .sdp / .concat "audio file" would otherwise make ffmpeg's playlist demuxers
# open arbitrary paths on this Mac. Names are ffmpeg demuxer names, covering
# every container in intro_transcribe.go's transcribableExtSet (.wma is asf).
# There is no separate "opus" demuxer: Ogg Opus is read by "ogg".
FORMAT_WHITELIST = "mov,mp4,m4a,3gp,3g2,mj2,mp3,flac,ogg,wav,aac,matroska,webm,asf"

# Substrings of ffmpeg stderr that mean the READ failed (a dropped or stalling
# NFS mount), not that the file cannot be decoded. Matched case-insensitively.
IO_FAULT_MARKERS = (
    "input/output error",
    "operation timed out",
    "connection timed out",
    "stale file handle",
    "device not configured",
    "no such device",
    "transport endpoint",
)

# ffmpeg's text when -format_whitelist refuses the input's container.
WHITELIST_REJECTION_MARKER = "not on whitelist"

# WHISPER_PATH_REQUIRE_MOUNT=0 turns off the "mapped root must be a mount point"
# check in _root_readable (tests, or a map that deliberately points at a plain
# directory). Read live, default on.
REQUIRE_MOUNT_ENV = "WHISPER_PATH_REQUIRE_MOUNT"

# Every argument that changes the extracted WAV. Hashed into /health's
# extract_fingerprint, which the Go client folds into path-job journal keys
# only (DECODE_FINGERPRINT is untouched, so upload-path entries stay valid).
EXTRACT_SETTINGS = {
    "args": [
        "-format_whitelist",
        FORMAT_WHITELIST,
        "-ss",
        "0",
        "-vn",
        "-ac",
        "1",
        "-ar",
        "16000",
        "-f",
        "wav",
    ],
    "max_seconds": MAX_CLIP_SECONDS,
}
EXTRACT_FINGERPRINT = hashlib.sha256(
    json.dumps(EXTRACT_SETTINGS, sort_keys=True).encode()
).hexdigest()[:16]


class PathJobError(Exception):
    """A per-job failure carrying one of the stable ERR_* codes."""

    def __init__(self, code: str, detail: str):
        super().__init__(f"{code}: {detail}")
        self.code = code


def parse_path_map(raw: str) -> list[tuple[str, str]]:
    """Parse WHISPER_PATH_MAP into [(server_prefix, local_root), ...].

    Raises ValueError on ANY malformed entry: a half-understood map is a map
    that sends some jobs to the wrong place, so the whole thing is refused.
    Both sides must be absolute and neither may be "/" (mapping the whole
    filesystem is never what an operator meant). Longest server prefix first.
    """
    pairs: list[tuple[str, str]] = []
    for entry in raw.split(","):
        entry = entry.strip()
        if not entry:
            continue
        server, sep, local = entry.partition("=")
        server, local = server.strip(), local.strip()
        if not sep or not server or not local:
            raise ValueError(f"entry {entry!r} is not server-path=local-path")
        for side in (server, local):
            if not side.startswith("/") or ".." in side.split("/"):
                raise ValueError(f"entry {entry!r}: {side!r} must be absolute with no '..'")
        server, local = posixpath.normpath(server), posixpath.normpath(local)
        if server == "/" or local == "/":
            raise ValueError(f"entry {entry!r} maps the filesystem root")
        pairs.append((server, local))
    pairs.sort(key=lambda p: len(p[0]), reverse=True)
    return pairs


def _path_map() -> tuple[list[tuple[str, str]], str]:
    """The live (pairs, error) from the environment. Read per call, not cached:
    health must describe what a job would do right now."""
    raw = os.environ.get(PATH_MAP_ENV, "")
    try:
        return parse_path_map(raw), ""
    except ValueError as e:
        return [], f"{PATH_MAP_ENV} invalid: {e}"


def _root_readable(root: str) -> bool:
    """True when `root` can be listed right now. A directory read, not isdir: a
    lost NFS mount leaves the mountpoint directory behind, so isdir stays true
    while every read fails. scandir stopping at the first entry, not listdir:
    the root holds the whole library, and building that list on every /health
    poll is wasted work on a mount that is already slow.

    The root must also still be a MOUNT POINT. A soft, read-only NFS mount that
    stays unresponsive past its deadtimeout is forcibly unmounted (man
    mount_nfs), which leaves the empty mountpoint directory behind: scandir on
    it succeeds, and every job would then report not_found. ismount is False
    there, so the root is correctly reported unreadable."""
    if os.environ.get(REQUIRE_MOUNT_ENV, "1") != "0" and not os.path.ismount(root):
        return False
    try:
        with os.scandir(root) as entries:
            next(entries, None)
        return True
    except OSError:
        return False


def _mapped_root_of(local_path: str) -> str | None:
    """The mapped local root that `local_path` lives under, or None."""
    pairs, _ = _path_map()
    for _server, local in pairs:
        # Both spellings: map_server_path hands ffmpeg the REALPATH, which
        # differs from the configured root when the mountpoint sits under a symlink.
        for base in (local.rstrip("/"), os.path.realpath(local).rstrip("/")):
            if local_path == base or local_path.startswith(base + "/"):
                return local
    return None


def map_server_path(path: Any, pairs: list[tuple[str, str]]) -> str:
    """Translate a server path to its local path, or raise PathJobError.

    Matching is by path COMPONENT: /mnt/bigdataX/a must not match /mnt/bigdata.
    ".." components, non-absolute paths and anything not under a mapped prefix
    are refused, as is a mapped path whose realpath leaves its root (a symlink
    out of the share). A mapped root that cannot be listed is a lost mount:
    path_unmapped, so the caller keeps the book retryable rather than burying it.
    """
    if not isinstance(path, str) or not path or "\x00" in path:
        raise PathJobError(ERR_BAD_REQUEST, "path must be a non-empty string")
    if not path.startswith("/"):
        raise PathJobError(ERR_PATH_UNMAPPED, "path is not absolute")
    parts = [p for p in path.split("/") if p not in ("", ".")]
    if ".." in parts:
        raise PathJobError(ERR_PATH_UNMAPPED, "path contains '..'")
    for server, local in pairs:
        sparts = server.strip("/").split("/")
        if parts[: len(sparts)] != sparts:
            continue
        if not _root_readable(local):
            raise PathJobError(
                ERR_PATH_UNMAPPED, f"mapped root for {server} is not readable (mount lost?)"
            )
        mapped = os.path.join(local, *parts[len(sparts) :])
        real_root = os.path.realpath(local)
        real = os.path.realpath(mapped)
        if os.path.commonpath([real_root, real]) != real_root:
            raise PathJobError(ERR_PATH_UNMAPPED, "path resolves outside its mapped root")
        # The resolved path, not `mapped`: ffmpeg then opens exactly the file
        # that was just checked, with no symlink left to swap between the check
        # and the open.
        return real
    raise PathJobError(ERR_PATH_UNMAPPED, "path is not under a mapped prefix")


def _check_seconds(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= MAX_CLIP_SECONDS:
        raise PathJobError(ERR_BAD_REQUEST, f"seconds must be an integer in 1..{MAX_CLIP_SECONDS}")
    return value


def _ffmpeg_tail(stderr: bytes) -> str:
    lines = [ln.strip() for ln in stderr.decode("utf-8", "replace").splitlines() if ln.strip()]
    return (lines[-1] if lines else "no output")[:200]


def _raise_ffmpeg_failure(src: str, stderr: bytes) -> None:
    """Classify a nonzero ffmpeg exit. ffmpeg_error is a DURABLE verdict on the
    Go side (the book is hidden until its mtime changes), so it is reserved for
    a file that is still readable and still will not decode. A read fault
    mid-decode (the NFS mount dropped, the handle went stale) exits nonzero
    exactly like a corrupt file, so the evidence is re-checked after the fact:

      the mapped root is no longer listable, or the file is gone -> path_unmapped
      stderr names an I/O fault or timeout                       -> ffmpeg_timeout
      ffmpeg refused the container (format whitelist)            -> container_rejected
      otherwise                                                  -> ffmpeg_error
    """
    root = _mapped_root_of(src)
    if (root is not None and not _root_readable(root)) or not os.path.isfile(src):
        raise PathJobError(
            ERR_PATH_UNMAPPED, "source became unreadable while ffmpeg ran (mount lost?)"
        )
    text = stderr.decode("utf-8", "replace").lower()
    if any(marker in text for marker in IO_FAULT_MARKERS):
        raise PathJobError(ERR_FFMPEG_TIMEOUT, f"read fault during decode: {_ffmpeg_tail(stderr)}")
    if WHITELIST_REJECTION_MARKER in text:
        raise PathJobError(
            ERR_CONTAINER_REJECTED,
            "container is not on this worker's format whitelist (renamed or unusual file)",
        )
    raise PathJobError(ERR_FFMPEG, _ffmpeg_tail(stderr))


def _extract_clip(src: str, seconds: int) -> tuple[str, str]:
    """Extract the first `seconds` of `src` to a validated 16 kHz mono WAV.

    Returns (tmpdir, wav_path); the CALLER removes tmpdir. ffmpeg writes a name
    inside a fresh mkdtemp directory with -y, never a NamedTemporaryFile (which
    already exists, so ffmpeg without -y refuses to overwrite it).
    """
    if not os.path.isfile(src):
        raise PathJobError(ERR_NOT_FOUND, "file not found under the mapped root")
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        raise PathJobError(ERR_WORKER_MISCONFIGURED, "ffmpeg is not on PATH on this worker")
    tmpdir = tempfile.mkdtemp(prefix="whisper-clip-")
    wav = os.path.join(tmpdir, "clip.wav")
    try:
        try:
            proc = subprocess.run(
                [
                    ffmpeg,
                    "-nostdin",
                    "-y",
                    "-v",
                    "error",
                    "-format_whitelist",
                    FORMAT_WHITELIST,
                    "-ss",
                    "0",
                    "-t",
                    str(seconds),
                    "-i",
                    src,
                    "-vn",
                    "-ac",
                    "1",
                    "-ar",
                    "16000",
                    "-f",
                    "wav",
                    wav,
                ],
                stdin=subprocess.DEVNULL,
                capture_output=True,
                timeout=EXTRACT_TIMEOUT_S,
            )
        except subprocess.TimeoutExpired:
            raise PathJobError(ERR_FFMPEG_TIMEOUT, f"ffmpeg exceeded {EXTRACT_TIMEOUT_S}s")
        except OSError as e:
            raise PathJobError(
                ERR_FFMPEG_TIMEOUT if e.errno in (errno.EIO, errno.ETIMEDOUT) else ERR_FFMPEG,
                str(e),
            )
        if proc.returncode != 0:
            _raise_ffmpeg_failure(src, proc.stderr)
        _validate_wav(wav)
        return tmpdir, wav
    except BaseException:
        shutil.rmtree(tmpdir, ignore_errors=True)
        raise


def _validate_wav(path: str) -> None:
    """Non-empty and parses as the 16 kHz mono PCM we asked for."""
    try:
        if os.path.getsize(path) <= 44:
            raise PathJobError(ERR_FFMPEG, "ffmpeg produced an empty clip")
        with wave.open(path, "rb") as w:
            if w.getnframes() <= 0 or w.getframerate() != 16000 or w.getnchannels() != 1:
                raise PathJobError(ERR_FFMPEG, "ffmpeg produced an invalid WAV")
    except (wave.Error, EOFError, OSError) as e:
        raise PathJobError(ERR_FFMPEG, f"invalid WAV: {e}")


_extract_pool: concurrent.futures.ThreadPoolExecutor | None = None
_extract_pool_lock = threading.Lock()


def _get_extract_pool() -> concurrent.futures.ThreadPoolExecutor:
    """One process-wide bounded pool, so concurrent requests share the cap
    instead of each fanning out its own ffmpegs. Inference still serializes on
    _inference_lock; extraction of job N+1 overlaps inference of job N."""
    global _extract_pool
    with _extract_pool_lock:
        if _extract_pool is None:
            _extract_pool = concurrent.futures.ThreadPoolExecutor(
                max_workers=EXTRACT_WORKERS, thread_name_prefix="extract"
            )
        return _extract_pool


def _prepare_path_job(job: Any) -> tuple[str, int]:
    """Validate one job and return (local_path, seconds); raises PathJobError."""
    if not isinstance(job, dict):
        raise PathJobError(ERR_BAD_REQUEST, "job must be an object")
    seconds = _check_seconds(job.get("seconds", DEFAULT_CLIP_SECONDS))
    pairs, perr = _path_map()
    if not pairs:
        raise PathJobError(ERR_PATH_UNMAPPED, perr or f"{PATH_MAP_ENV} is not set on this worker")
    return map_server_path(job.get("path"), pairs), seconds


def _error_result(e: Exception) -> dict:
    return {"text": "", "error": str(e)}


def _discard_extraction(fut: concurrent.futures.Future) -> None:
    """Abandon an extraction without waiting for it, and without leaking its clip.
    A queued one is cancelled and never runs; one already running cannot be, so
    its tmpdir is removed by a callback when it finishes (add_done_callback runs
    the callback at once if it already has)."""
    if fut.cancel():
        return

    def remove_clip(done: concurrent.futures.Future) -> None:
        if done.cancelled() or done.exception() is not None:
            return  # _extract_clip cleans up after its own failure
        shutil.rmtree(done.result()[0], ignore_errors=True)

    fut.add_done_callback(remove_clip)


def _run_path_jobs(jobs: dict[str, Any], deadline: float | None = None) -> dict[str, dict]:
    """Run every job; never raises. One result per key, errors isolated.

    Once the child is known not to start, or `deadline` (monotonic, see
    _abandon_deadline) has passed, every remaining key is answered AT ONCE without
    waiting for its extraction, which can take up to EXTRACT_TIMEOUT_S on a slow
    mount: queued extractions are cancelled and running ones remove their own
    clip when they finish (_discard_extraction)."""
    results: dict[str, dict] = {}
    pending: dict[str, concurrent.futures.Future] = {}
    pool = _get_extract_pool()
    for key, job in jobs.items():
        try:
            src, seconds = _prepare_path_job(job)
        except PathJobError as e:
            results[key] = _error_result(e)
            continue
        except Exception as e:  # defensive: a mapping bug must not fail the batch
            results[key] = _error_result(PathJobError(ERR_PATH_UNMAPPED, str(e)))
            continue
        pending[key] = pool.submit(_extract_clip, src, seconds)
    start_fault: ChildFaultError | None = None
    try:
        for key, fut in pending.items():
            if start_fault is not None or _past(deadline):
                # The child never became ready (every further clip would wait out
                # the same CHILD_READY_TIMEOUT_S), or the client gave up: answer
                # without waiting for this clip's extraction.
                results[key] = (
                    _abandoned_result()
                    if start_fault is None
                    else _error_result(PathJobError(ERR_WORKER_MISCONFIGURED, str(start_fault)))
                )
                _discard_extraction(fut)
                continue
            tmpdir = None
            try:
                tmpdir, wav = fut.result()
                if _past(deadline):
                    # Extraction itself took past the deadline.
                    results[key] = _abandoned_result()
                    continue
                text = _transcribe_file(wav)
                log.info(f"transcribed path job {key}: {len(text)} chars")
                results[key] = {"text": text, "error": None}
            except PathJobError as e:
                log.error(f"path job {key} failed: {e}")
                results[key] = _error_result(e)
            except ChildFaultError as e:
                # A child that cannot start is this worker's fault, not a verdict
                # on the clip: the Go side defers worker_misconfigured and writes
                # nothing, where whisper_error would be recorded as failed.
                log.error(f"path job {key}: inference child fault: {e}")
                results[key] = _error_result(PathJobError(ERR_WORKER_MISCONFIGURED, str(e)))
                if e.starting:
                    start_fault = e
            except Exception as e:
                log.error(f"path job {key} failed: {e}")
                results[key] = _error_result(PathJobError(ERR_WHISPER, str(e)))
            finally:
                if tmpdir:
                    shutil.rmtree(tmpdir, ignore_errors=True)
    finally:
        # A cancelled/aborted loop must not leak the clips still being cut.
        for key, fut in pending.items():
            if key not in results:
                _discard_extraction(fut)
    return results


# Plain `def`, not `async def`: FastAPI runs these in its threadpool, so a long
# batch does not freeze the event loop and /health keeps answering. The Go
# pool probes /health with a 3 s timeout and refuses an endpoint it cannot read.
@app.post("/transcribe-path")
def transcribe_path(body: Annotated[dict, Body()]):
    """Single path job: {"path": ..., "seconds": 90} -> {"text","error"}."""
    return _run_path_jobs({"job": body})["job"]


@app.post("/transcribe-path-batch")
def transcribe_path_batch(body: Annotated[dict, Body()], request: Request):
    """{"jobs": {key: {"path","seconds"}}} -> {"results": {key: {"text","error"}}}.

    Keys are echoed verbatim. Jobs are validated one by one, not through one
    request model, so a single malformed job cannot 422 the whole batch.

    X-Whisper-Request-Timeout-S, when sent, is the client's HTTP ceiling: jobs not
    yet started when it passes are answered as abandoned (see _run_path_jobs).
    """
    jobs = body.get("jobs")
    if not isinstance(jobs, dict):
        raise HTTPException(status_code=422, detail='"jobs" must be an object')
    return {"results": _run_path_jobs(jobs, _abandon_deadline(request))}


def _path_health(probe: bool = True) -> dict:
    """The path pipeline's /health fields, computed live.

    path_pipeline means "a valid WHISPER_PATH_MAP is configured"; whether each
    mapped root is readable RIGHT NOW is path_roots. They are separate on
    purpose: configuration is the capability, a lost NFS mount is a fault, and
    the Go client defers pages (writes nothing) while any root is unreadable
    instead of recording every book as failed.

    probe=False does not touch the filesystem: it is the answer when the listing
    did not return in time, and reports every mapped root as NOT readable (fail
    closed: nothing was seen to be readable), where probe=True lists each root.
    """
    pairs, err = _path_map()
    out: dict = {
        "path_pipeline": bool(pairs),
        "path_prefixes": [server for server, _ in pairs],
        "path_roots": [
            {"prefix": server, "readable": probe and _root_readable(local)}
            for server, local in pairs
        ],
        "extract_fingerprint": EXTRACT_FINGERPRINT,
    }
    if err:
        out["path_map_error"] = err
    return out


# /health runs on the event loop, NOT in the anyio threadpool the sync upload
# handlers share (40 threads by default): a long batch queues handlers on
# _inference_lock, each parked in a threadpool thread, and a /health that needed
# a thread too would queue behind them -- the Go liveness probe would then read
# a busy worker as a dead one. Only the two calls that can block (the NFS root
# listing and the ffmpeg lookup) go to this dedicated pool, bounded by a
# deadline a little under the Go probe's 3 s. A hung mount strands at most these
# two threads. /health then still answers 200: the Go liveness poller and its
# stuck-detection read busy_seconds / busy_limit_seconds from this body, and a 503
# would hide them while the worker may be wedged. The reply says the filesystem
# could not be checked (path_health_error), reports every mapped root as not
# readable so path jobs are refused, and omits "ffmpeg", which is unknown.
HEALTH_PATH_TIMEOUT_S = 2.5
_health_pool = concurrent.futures.ThreadPoolExecutor(max_workers=2, thread_name_prefix="health")


def _blocking_health_fields() -> dict:
    return {"ffmpeg": shutil.which("ffmpeg"), **_path_health()}


@app.get("/health")
async def health():
    """Health probe.

    An async def so it never waits for a threadpool thread (see _health_pool),
    with the filesystem reads, which can block on a hung NFS mount, run on that
    pool under a deadline. When the deadline passes the answer is still a 200 with
    every field that does not need the filesystem, plus path_health_error; the
    roots are reported unreadable and "ffmpeg" is left out (see _health_pool).

    child_start_error is present ONLY while the inference child's last start
    failed within WHISPER_CHILD_FAULT_TTL_S and no start has succeeded since.

    batch_pipeline MUST be present for the Go client to use /transcribe-batch:
    supportsRemoteBatch decodes it as *bool and enables batching when the
    pointer is non-nil. Reporting the model and backend makes a misconfigured
    worker visible to a health check rather than only to a log line.
    """
    loop = asyncio.get_running_loop()
    try:
        blocking = await asyncio.wait_for(
            loop.run_in_executor(_health_pool, _blocking_health_fields), HEALTH_PATH_TIMEOUT_S
        )
    except asyncio.TimeoutError:
        blocking = {
            **_path_health(probe=False),
            "path_health_error": "filesystem checks did not answer in time",
        }
    busy_seconds, busy_limit = _inference.busy()
    child_start_error = _inference.start_error()
    return {
        "status": "ok",
        "model": MODEL_REPO,
        "batch_pipeline": True,
        "device": "metal",
        "backend": "mlx",
        "decode_fingerprint": DECODE_FINGERPRINT,
        # Which machine this is, as an opaque hash (see WORKER IDENTITY).
        "machine_id": MACHINE_ID,
        # Inference liveness, read WITHOUT touching _inference_lock (a wedged
        # job holds it). busy_seconds is how long the current clip has been
        # running (0 when idle); busy_limit_seconds is that clip's own limit. A
        # busy_seconds past its limit means the deadline did not fire, and the Go
        # client then refuses this endpoint. The clip_timeout_* fields let the
        # client size its HTTP timeout to the worst case of a batch, and
        # child_ready_timeout_s / extract_timeout_s are the two things a request
        # can wait on BEFORE a clip's own clock starts (a cold model load, and
        # ffmpeg cutting the clip), so the client's ceiling covers them too.
        "busy_seconds": round(busy_seconds, 1),
        "busy_limit_seconds": busy_limit,
        "clip_timeout_floor_s": TRANSCRIBE_MIN_TIMEOUT_S,
        "clip_timeout_factor": TRANSCRIBE_TIMEOUT_FACTOR,
        "child_ready_timeout_s": CHILD_READY_TIMEOUT_S,
        "extract_timeout_s": EXTRACT_TIMEOUT_S,
        # Absent, not null, when there is no recent start failure.
        **({"child_start_error": child_start_error} if child_start_error else {}),
        **blocking,
    }


if __name__ == "__main__":
    if CHILD_FLAG in sys.argv:
        # Re-entry as the inference child (see InferenceChild._spawn).
        _child_main(int(sys.argv[-2]), int(sys.argv[-1]))
        raise SystemExit(0)

    import uvicorn

    # Enforced here, not at import: a worker that cannot decode audio must not
    # serve, but the module must stay importable so the contract tests can load
    # it on a machine without ffmpeg. Enforcing at import made every test ERROR
    # on a CI runner that has no ffmpeg -- fail-closed in the wrong place.
    _require_ffmpeg()

    bind = os.environ.get("WHISPER_BIND", "127.0.0.1")
    port = int(os.environ.get("WHISPER_PORT", "19848"))
    log.info(f"Ready — model={MODEL_REPO} backend=mlx bind={bind}:{port}")
    if bind != "127.0.0.1":
        log.warning(f"binding {bind} — this exposes the worker beyond loopback")
        # log_config=None is required, not cosmetic. uvicorn.run() otherwise
        # applies its own dictConfig with disable_existing_loggers=True, which
        # silences the module logger created above -- so uvicorn's access lines
        # appear while every log.info/log.error from this file vanishes. That is
        # how an ffmpeg failure on every single request left no trace here on
        # 2026-08-31: 2,472 requests logged as "200 OK" and not one error line.
    uvicorn.run(app, host=bind, port=port, log_level="info", log_config=None)
