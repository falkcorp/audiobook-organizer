# file: scripts/whisper_mlx_server.py
# version: 1.5.0
# guid: 3f8c21d4-7b6e-4a52-9c18-2d5e7a9b4c60
# last-edited: 2026-10-22
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

import concurrent.futures
import contextlib
import errno
import hashlib
import json
import logging
import os
import posixpath
import shutil
import subprocess
import sys
import tempfile
import threading
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


app = FastAPI()


def _transcribe_file(path: str) -> str:
    """Transcribe a WAV at `path` with MLX Whisper and return the text.

    mlx_whisper is imported HERE rather than at module scope on purpose: it is
    an Apple-Silicon-only dependency, and importing it at the top would make
    this module unimportable on Linux -- taking the contract tests down with
    it. The tests replace this function wholesale, so they exercise the real
    HTTP surface (multipart parsing, result keying, per-file error isolation)
    on any platform, and only the inference call itself is Mac-gated.
    """
    import mlx_whisper

    with _inference_lock:
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


@app.post("/transcribe")
async def transcribe(file: Annotated[UploadFile, File()]):
    """Single-file endpoint, used by the Go client's per-file fallback path."""
    data = await file.read()
    return _do_transcribe(data, file.filename)


@app.post("/transcribe-batch")
async def transcribe_batch(files: Annotated[List[UploadFile], File()]):
    """Multi-file endpoint. Returns {"results": {"<filename>": {...}}}.

    The key is each part's filename VERBATIM -- the Go client sends the book ID
    there and looks the result up by it. Processing is sequential; the win over
    N single requests is reduced HTTP overhead, not parallelism.
    """
    results = {}
    for f in files:
        data = await f.read()
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
ERR_PATH_UNMAPPED = "path_unmapped"
ERR_NOT_FOUND = "not_found"
ERR_FFMPEG = "ffmpeg_error"
ERR_FFMPEG_TIMEOUT = "ffmpeg_timeout"
ERR_BAD_REQUEST = "bad_request"
ERR_WHISPER = "whisper_error"

# Every argument that changes the extracted WAV. Hashed into /health's
# extract_fingerprint, which the Go client folds into path-job journal keys
# only (DECODE_FINGERPRINT is untouched, so upload-path entries stay valid).
EXTRACT_SETTINGS = {
    "args": ["-ss", "0", "-vn", "-ac", "1", "-ar", "16000", "-f", "wav"],
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
    """True when `root` can be listed right now. os.listdir, not isdir: a lost
    NFS mount leaves the mountpoint directory behind, so isdir stays true while
    every read fails."""
    try:
        os.listdir(root)
        return True
    except OSError:
        return False


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
        return mapped
    raise PathJobError(ERR_PATH_UNMAPPED, "path is not under a mapped prefix")


def _check_seconds(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= MAX_CLIP_SECONDS:
        raise PathJobError(ERR_BAD_REQUEST, f"seconds must be an integer in 1..{MAX_CLIP_SECONDS}")
    return value


def _ffmpeg_tail(stderr: bytes) -> str:
    lines = [ln.strip() for ln in stderr.decode("utf-8", "replace").splitlines() if ln.strip()]
    return (lines[-1] if lines else "no output")[:200]


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
        raise PathJobError(ERR_FFMPEG, "ffmpeg is not on PATH")
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
            raise PathJobError(ERR_FFMPEG, _ffmpeg_tail(proc.stderr))
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
async def health():
    """Health probe.

    batch_pipeline MUST be present for the Go client to use /transcribe-batch:
    supportsRemoteBatch decodes it as *bool and enables batching when the
    pointer is non-nil. Reporting the model and backend makes a misconfigured
    worker visible to a health check rather than only to a log line.
    """
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
        **_path_health(),
    }


if __name__ == "__main__":
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
