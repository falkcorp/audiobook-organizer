# file: scripts/whisper_mlx_server.py
# version: 1.18.0
# guid: 3f8c21d4-7b6e-4a52-9c18-2d5e7a9b4c60
# last-edited: 2026-11-17
#
# /// script
# requires-python = ">=3.14,<3.15"
# # Upper bound is deliberate: the default python3 on the Mac is 3.15, and
# # uvicorn[standard] has no cp315 wheels yet, so an open ">=3.14" makes uv
# # pick 3.15 and compile from source. Lift the cap once 3.15 wheels exist.
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
# The path pipeline and the machine identity this worker serves are SHARED with
# scripts/whisper_server.py: see scripts/whisper_path_pipeline.py and
# scripts/whisper_machine_id.py. Keep both next to this file when deploying it.
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

import atexit
import contextlib
import hashlib
import importlib
import json
import logging
import os
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Annotated, List

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_mlx_server")

try:
    from fastapi import FastAPI, File, Request, UploadFile
except ImportError as e:  # pragma: no cover - dependency probe
    print(f"Missing dependency: {e}")
    print("Install with: uv run scripts/whisper_mlx_server.py")
    sys.exit(1)

# The shared worker modules live next to this file. Put that directory first on
# sys.path so they import however this file is started: `uv run`, a launchd
# plist, the inference child (which re-executes this file), or a test that loads
# it by path.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from whisper_machine_id import MachineIdentity  # noqa: E402
from whisper_path_pipeline import (  # noqa: E402
    CAT_DIED_ON_CLIP,
    CAT_EXITED_DURING_START,
    CAT_KILLED_BY_SIGNAL,
    CAT_NOT_REAPED,
    CAT_PROCESS_START_FAILED,
    CAT_PROTOCOL_ERROR,
    CAT_START_TIMEOUT,
    ERR_DECODER_CRASHED,
    ERR_DECODER_KILLED,
    ERR_TRANSCRIBE_TIMEOUT,
    ERR_WORKER_MISCONFIGURED,
    ChildFaultError,
    PathJobError,
    PathPipeline,
    abandon_deadline,
    abandoned_result,
    audio_seconds,
    error_result,
    install_path_routes,
    misconfigured_error,
    past,
    public_model_name,
    wire_error,
)

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

# WORKER IDENTITY lives in scripts/whisper_machine_id.py (shared with
# whisper_server.py): /health reports machine_id, an opaque hash of a stable
# per-machine source, so the Go side can tell machines apart even when two ports
# are one box. See that module's header for why the hostname is never one.
identity = MachineIdentity()

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
if CHILD_FLAG not in sys.argv:
    identity.start()
BACKEND_FACTORY_ENV = "WHISPER_BACKEND_FACTORY"


def _clip_timeout(path: str) -> float:
    """The per-clip inference time limit: max(floor, factor x audio seconds)."""
    return max(TRANSCRIBE_MIN_TIMEOUT_S, TRANSCRIBE_TIMEOUT_FACTOR * audio_seconds(path))


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
            # The fixed category word, as the decode child does: /health is
            # unauthenticated and the message can carry a path or an OSError's text.
            self._note_start(wire_error(ERR_WORKER_MISCONFIGURED, e.category))
            raise
        self._note_start(None)

    def _start(self) -> None:
        """Start a fresh child and wait for its ready line, or raise
        ChildFaultError(starting=True)."""
        self._kill()
        try:
            self._spawn()
        except OSError as e:
            raise ChildFaultError(
                f"inference process could not be started: {e}",
                starting=True,
                category=CAT_PROCESS_START_FAILED,
            )
        # Model load happens HERE, before the clip's clock starts: at a high
        # machine load it can eat most of a 60 s floor, and a clip must not time
        # out because the model was slow to load.
        try:
            line = self._readline(time.monotonic() + CHILD_READY_TIMEOUT_S)
        except EOFError:
            code = self._exit_code()
            self._kill()
            raise ChildFaultError(
                f"inference process exited while starting (exit code {code})",
                starting=True,
                category=CAT_EXITED_DURING_START,
            )
        if line is None:
            self._kill()
            raise ChildFaultError(
                f"inference process not ready after {CHILD_READY_TIMEOUT_S:.0f}s",
                starting=True,
                category=CAT_START_TIMEOUT,
            )
        if not json.loads(line).get("ready"):
            self._kill()
            raise ChildFaultError(
                "inference process sent an unexpected startup message",
                starting=True,
                category=CAT_PROTOCOL_ERROR,
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
                        category=CAT_NOT_REAPED if code is None else CAT_KILLED_BY_SIGNAL,
                    )
                # Anything else (a crash signal such as SIGABRT/SIGSEGV/SIGBUS,
                # or a non-zero exit) is the decoder dying on the clip: a verdict
                # about the CLIP. A clip that does so every time must settle as
                # a strike on the Go side, like transcribe_timeout, not be
                # deferred as a worker fault forever.
                raise PathJobError(
                    ERR_DECODER_CRASHED,
                    f"the decode process died (exit code {code}) on this clip; it was restarted",
                    category=CAT_DIED_ON_CLIP,
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
        return misconfigured_error(e)
    except Exception as e:
        # A PathJobError from the child (a timeout, a crash) answers with its own code;
        # anything else is whisper_error. Both are fixed text, the detail is logged.
        return error_result(e, f"transcription failed for {filename}")
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


def _run_upload_batch(files, deadline: float | None = None) -> dict[str, dict]:
    """Run an upload batch sequentially; never raises. `files` are objects with
    .filename and a readable .file. Once `deadline` (monotonic) has passed, the
    clips not yet started are answered as abandoned without being run: the check is
    before STARTING a clip, never mid-clip."""
    results: dict[str, dict] = {}
    start_fault: list[ChildFaultError] = []
    for f in files:
        if past(deadline):
            # The client gave up on this request and will never read the answer.
            results[f.filename] = abandoned_result()
            continue
        if start_fault:
            # The child never became ready: every further clip would wait out the
            # same CHILD_READY_TIMEOUT_S. Answer the rest with the same fault.
            results[f.filename] = misconfigured_error(start_fault[0])
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
    return {"results": _run_upload_batch(files, abandon_deadline(request))}


# ── Path pipeline: extract the clip on THIS machine from a server path ───────
#
# The path pipeline itself (map, safety boundary, extraction, batch handling and
# the path /health fields) is scripts/whisper_path_pipeline.py, shared with
# whisper_server.py. This worker supplies only the model: _transcribe_file, the
# killable MLX child under its per-clip deadline.

pipeline = PathPipeline()


def _run_path_jobs(jobs: dict, deadline: float | None = None) -> dict[str, dict]:
    """Run every path job through the shared pipeline with this worker's model.
    _transcribe_file is looked up per call, so it stays replaceable."""
    return pipeline.run_path_jobs(jobs, deadline, _transcribe_file)


transcribe_path, transcribe_path_batch = install_path_routes(app, _run_path_jobs)


@app.get("/health")
async def health():
    """Health probe.

    An async def so it never waits for a threadpool thread (see PathPipeline's
    health pool), with the filesystem reads, which can block on a hung NFS mount,
    run on that pool under a deadline. When the deadline passes the answer is
    still a 200 with every field that does not need the filesystem, plus
    path_health_error.

    child_start_error is present ONLY while the inference child's last start
    failed within WHISPER_CHILD_FAULT_TTL_S and no start has succeeded since.

    batch_pipeline MUST be present for the Go client to use /transcribe-batch:
    supportsRemoteBatch decodes it as *bool and enables batching when the
    pointer is non-nil. Reporting the model and backend makes a misconfigured
    worker visible to a health check rather than only to a log line.
    """
    return await pipeline.health_payload(
        identity=identity,
        model=public_model_name(MODEL_REPO),
        backend="mlx",
        device="metal",
        decode_fingerprint=DECODE_FINGERPRINT,
        busy=_inference.busy,
        start_error=_inference.start_error,
        clip_timeout_floor_s=TRANSCRIBE_MIN_TIMEOUT_S,
        clip_timeout_factor=TRANSCRIBE_TIMEOUT_FACTOR,
        child_ready_timeout_s=CHILD_READY_TIMEOUT_S,
    )


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
    # The raw value, once, here in the log; /health carries public_model_name's label.
    log.info(
        f"Ready — model={MODEL_REPO} (reported as {public_model_name(MODEL_REPO)}) "
        f"backend=mlx bind={bind}:{port}"
    )
    if bind != "127.0.0.1":
        log.warning(f"binding {bind} — this exposes the worker beyond loopback")
        # log_config=None is required, not cosmetic. uvicorn.run() otherwise
        # applies its own dictConfig with disable_existing_loggers=True, which
        # silences the module logger created above -- so uvicorn's access lines
        # appear while every log.info/log.error from this file vanishes. That is
        # how an ffmpeg failure on every single request left no trace here on
        # 2026-08-31: 2,472 requests logged as "200 OK" and not one error line.
    uvicorn.run(app, host=bind, port=port, log_level="info", log_config=None)
