# file: scripts/whisper_mlx_server.py
# version: 1.1.0
# guid: 3f8c21d4-7b6e-4a52-9c18-2d5e7a9b4c60
# last-edited: 2026-08-30
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

import logging
import os
import shutil
import sys
import tempfile
import threading
from typing import List

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_mlx_server")

try:
    from fastapi import FastAPI, File, UploadFile
except ImportError as e:  # pragma: no cover - dependency probe
    print(f"Missing dependency: {e}")
    print('Install with: uv run scripts/whisper_mlx_server.py')
    sys.exit(1)

DEFAULT_MODEL = "mlx-community/whisper-small.en-mlx"
MODEL_REPO = (
    sys.argv[1]
    if len(sys.argv) > 1 and not sys.argv[1].startswith("-")
    else os.environ.get("WHISPER_MLX_MODEL", DEFAULT_MODEL)
)

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


FFMPEG_PATH = _require_ffmpeg()

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
            language="en",
            task="transcribe",
            # Audiobook intros are short and often start with music or a
            # publisher jingle. Conditioning on previous text makes Whisper
            # loop on those, emitting the same phrase repeatedly.
            condition_on_previous_text=False,
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
            try:
                os.unlink(tmp_path)
            except OSError:
                pass


@app.post("/transcribe")
async def transcribe(file: UploadFile = File(...)):
    """Single-file endpoint, used by the Go client's per-file fallback path."""
    data = await file.read()
    return _do_transcribe(data, file.filename)


@app.post("/transcribe-batch")
async def transcribe_batch(files: List[UploadFile] = File(...)):
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
        # Reported because "can this worker decode audio" is a different
        # question from "is the model loaded", and only the first one was
        # ever wrong. The process refuses to start without it, so this is
        # documentation of a guarantee rather than a live check.
        "ffmpeg": FFMPEG_PATH,
    }


if __name__ == "__main__":
    import uvicorn

    bind = os.environ.get("WHISPER_BIND", "127.0.0.1")
    port = int(os.environ.get("WHISPER_PORT", "19848"))
    log.info(f"Ready — model={MODEL_REPO} backend=mlx bind={bind}:{port}")
    if bind != "127.0.0.1":
        log.warning(f"binding {bind} — this exposes the worker beyond loopback")
    uvicorn.run(app, host=bind, port=port, log_level="info")
