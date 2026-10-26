# file: scripts/whisper_mlx_server.py
# version: 1.8.0
# guid: 3f8c21d4-7b6e-4a52-9c18-2d5e7a9b4c60
# last-edited: 2026-10-25
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
# "transcribe_timeout: ...". The next request starts a fresh child.

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
    from fastapi import Body, FastAPI, File, HTTPException, UploadFile
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
CHILD_FLAG = "--inference-child"
BACKEND_FACTORY_ENV = "WHISPER_BACKEND_FACTORY"

# Bytes per second of the 16 kHz mono 16-bit PCM this worker extracts; only used
# to estimate the length of an upload whose header cannot be read.
_PCM_BYTES_PER_S = 32000


def _audio_seconds(path: str) -> float:
    """Length of the clip in seconds, from the WAV header (frames / rate), not
    from the length the caller asked for: the clip can be shorter than asked
    (a file shorter than the clip length). Falls back to a size estimate for a
    file that is not a parseable WAV."""
    try:
        with wave.open(path, "rb") as w:
            rate = w.getframerate()
            if rate > 0:
                return w.getnframes() / rate
    except (wave.Error, EOFError, OSError):
        pass
    try:
        return os.path.getsize(path) / _PCM_BYTES_PER_S
    except OSError:
        return 0.0


def _clip_timeout(path: str) -> float:
    """The per-clip inference time limit: max(floor, factor x audio seconds)."""
    return max(TRANSCRIBE_MIN_TIMEOUT_S, TRANSCRIBE_TIMEOUT_FACTOR * _audio_seconds(path))


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
        self._kill()
        self._spawn()
        # Model load happens HERE, before the clip's clock starts: at a high
        # machine load it can eat most of a 60 s floor, and a clip must not time
        # out because the model was slow to load.
        try:
            line = self._readline(time.monotonic() + CHILD_READY_TIMEOUT_S)
        except EOFError:
            code = self._proc.poll() if self._proc else None
            self._kill()
            raise RuntimeError(f"inference process exited while starting (exit code {code})")
        if line is None:
            self._kill()
            raise RuntimeError(f"inference process not ready after {CHILD_READY_TIMEOUT_S:.0f}s")
        if not json.loads(line).get("ready"):
            self._kill()
            raise RuntimeError("inference process sent an unexpected startup message")

    def run(self, path: str, timeout_s: float) -> str:
        """Transcribe `path`. Raises PathJobError(transcribe_timeout) when it does
        not answer in `timeout_s` (the child is killed; the next call starts a
        fresh one) and RuntimeError when the child dies or reports an error."""
        self._ensure_child()
        self._set_busy(timeout_s)
        try:
            try:
                os.write(self._req_w, json.dumps({"path": path}).encode() + b"\n")
                line = self._readline(time.monotonic() + timeout_s)
            except (EOFError, BrokenPipeError):
                code = self._proc.poll() if self._proc else None
                self._kill()
                # A dead child is a worker fault, NOT a statement about the clip.
                raise RuntimeError(f"inference process died (exit code {code})")
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


def _do_transcribe(data: bytes, filename: str) -> dict:
    """Run one file and return the {"text","error"} shape the Go client decodes.

    Never raises: a failure here must degrade to an error entry for THIS file
    so a single bad WAV cannot fail a 64-file batch. The Go side reads
    error != nil per result and keeps the rest.
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


@app.post("/transcribe-batch")
def transcribe_batch(files: Annotated[List[UploadFile], File()]):
    """Multi-file endpoint. Returns {"results": {"<filename>": {...}}}.

    The key is each part's filename VERBATIM -- the Go client sends the book ID
    there and looks the result up by it. Processing is sequential; the win over
    N single requests is reduced HTTP overhead, not parallelism.
    """
    results = {}
    for f in files:
        data = f.file.read()
        results[f.filename] = _do_transcribe(data, f.filename)
    return {"results": results}


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
#   worker_misconfigured  this worker cannot do the job at all (ffmpeg not on
#                   PATH): a fault of the worker, never a verdict about a book;
#                   the Go side defers the book and writes nothing
ERR_PATH_UNMAPPED = "path_unmapped"
ERR_NOT_FOUND = "not_found"
ERR_FFMPEG = "ffmpeg_error"
ERR_FFMPEG_TIMEOUT = "ffmpeg_timeout"
ERR_BAD_REQUEST = "bad_request"
ERR_WHISPER = "whisper_error"
ERR_WORKER_MISCONFIGURED = "worker_misconfigured"
ERR_CONTAINER_REJECTED = "container_rejected"
ERR_TRANSCRIBE_TIMEOUT = "transcribe_timeout"

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


def _run_path_jobs(jobs: dict[str, Any]) -> dict[str, dict]:
    """Run every job; never raises. One result per key, errors isolated."""
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
    try:
        for key, fut in pending.items():
            tmpdir = None
            try:
                tmpdir, wav = fut.result()
                text = _transcribe_file(wav)
                log.info(f"transcribed path job {key}: {len(text)} chars")
                results[key] = {"text": text, "error": None}
            except PathJobError as e:
                log.error(f"path job {key} failed: {e}")
                results[key] = _error_result(e)
            except Exception as e:
                log.error(f"path job {key} failed: {e}")
                results[key] = _error_result(PathJobError(ERR_WHISPER, str(e)))
            finally:
                if tmpdir:
                    shutil.rmtree(tmpdir, ignore_errors=True)
    finally:
        # A cancelled/aborted loop must not leak the clips still being cut.
        for key, fut in pending.items():
            if key in results:
                continue
            fut.cancel()
            with contextlib.suppress(Exception):
                tmpdir, _ = fut.result()
                shutil.rmtree(tmpdir, ignore_errors=True)
    return results


# Plain `def`, not `async def`: FastAPI runs these in its threadpool, so a long
# batch does not freeze the event loop and /health keeps answering. The Go
# pool probes /health with a 3 s timeout and refuses an endpoint it cannot read.
@app.post("/transcribe-path")
def transcribe_path(body: Annotated[dict, Body()]):
    """Single path job: {"path": ..., "seconds": 90} -> {"text","error"}."""
    return _run_path_jobs({"job": body})["job"]


@app.post("/transcribe-path-batch")
def transcribe_path_batch(body: Annotated[dict, Body()]):
    """{"jobs": {key: {"path","seconds"}}} -> {"results": {key: {"text","error"}}}.

    Keys are echoed verbatim. Jobs are validated one by one, not through one
    request model, so a single malformed job cannot 422 the whole batch.
    """
    jobs = body.get("jobs")
    if not isinstance(jobs, dict):
        raise HTTPException(status_code=422, detail='"jobs" must be an object')
    return {"results": _run_path_jobs(jobs)}


def _path_health() -> dict:
    """The path pipeline's /health fields, computed live.

    path_pipeline means "a valid WHISPER_PATH_MAP is configured"; whether each
    mapped root is readable RIGHT NOW is path_roots. They are separate on
    purpose: configuration is the capability, a lost NFS mount is a fault, and
    the Go client defers pages (writes nothing) while any root is unreadable
    instead of recording every book as failed.
    """
    pairs, err = _path_map()
    out: dict = {
        "path_pipeline": bool(pairs),
        "path_prefixes": [server for server, _ in pairs],
        "path_roots": [
            {"prefix": server, "readable": _root_readable(local)} for server, local in pairs
        ],
        "extract_fingerprint": EXTRACT_FINGERPRINT,
    }
    if err:
        out["path_map_error"] = err
    return out


@app.get("/health")
def health():
    """Health probe.

    A plain def, not async: it reads the NFS root, and on a hung mount that
    call blocks. A sync handler blocks one threadpool thread; an async one would
    freeze the event loop and with it the upload endpoints.

    batch_pipeline MUST be present for the Go client to use /transcribe-batch:
    supportsRemoteBatch decodes it as *bool and enables batching when the
    pointer is non-nil. Reporting the model and backend makes a misconfigured
    worker visible to a health check rather than only to a log line.
    """
    busy_seconds, busy_limit = _inference.busy()
    return {
        "status": "ok",
        "model": MODEL_REPO,
        "batch_pipeline": True,
        "device": "metal",
        "backend": "mlx",
        "decode_fingerprint": DECODE_FINGERPRINT,
        # Reported because "can this worker decode audio" is a different
        # question from "is the model loaded", and only the first was ever
        # wrong. Looked up LIVE rather than frozen at import: the startup
        # preflight proves ffmpeg was present when we booted, not that it
        # still is.
        "ffmpeg": shutil.which("ffmpeg"),
        # Inference liveness, read WITHOUT touching _inference_lock (a wedged
        # job holds it). busy_seconds is how long the current clip has been
        # running (0 when idle); busy_limit_seconds is that clip's own limit. A
        # busy_seconds past its limit means the deadline did not fire, and the Go
        # client then refuses this endpoint. The two clip_timeout_* fields let
        # the client size its HTTP timeout to the worst case of a batch.
        "busy_seconds": round(busy_seconds, 1),
        "busy_limit_seconds": busy_limit,
        "clip_timeout_floor_s": TRANSCRIBE_MIN_TIMEOUT_S,
        "clip_timeout_factor": TRANSCRIBE_TIMEOUT_FACTOR,
        **_path_health(),
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
