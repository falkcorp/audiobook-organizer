# file: scripts/whisper_server.py
# version: 2.9.0
# guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
# last-edited: 2026-08-31
#
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "faster-whisper>=1.0.0",
#   "fastapi>=0.111",
#   "uvicorn[standard]>=0.29",
#   "python-multipart>=0.0.9",
# ]
# ///
#
# Remote Whisper transcription server for use with audiobook-organizer.
# Runs on a machine with a fast GPU when one is available, and on CPU otherwise
# -- the device is resolved at startup (see _resolve_device) rather than
# hardcoded. A GPU is strongly preferred for bulk transcription; CPU works and
# is roughly an order of magnitude slower.
#
# WHISPER_DEVICE forces the device ("cuda" or "cpu"); "auto" and an unset value
# both mean "probe for CUDA, fall back to CPU". ctranslate2 has no Metal/MPS
# backend, so Apple Silicon runs on CPU.
#
# Run (uv handles all deps automatically — no pip install needed):
#   uv run scripts/whisper_server.py [model]
#   uv run scripts/whisper_server.py small.en
#
# On Windows (GPU machine), uv is available at:
#   https://docs.astral.sh/uv/getting-started/installation/
#
# Configure the Go service to use it:
#   Add to deploy/local.conf:  Environment=WHISPER_REMOTE_URL=http://<ip>:8000
#   Then: make deploy
#
# Windows firewall: allow inbound TCP 8000 from your LAN subnet.
#
# NOTE: faster-whisper uses ctranslate2 for inference — no separate torch
# install needed. ctranslate2 bundles its own CUDA runtime.
# If you see CUDA errors, ensure CUDA 11.x or 12.x drivers are installed.
# A host that reports device=cpu on /health when it has a GPU means the CUDA
# probe found no devices at startup; restart it once the driver is healthy.
#
# v2: adds /transcribe-batch endpoint.  BatchedInferencePipeline (faster-whisper
# >=1.0.0) processes 16 audio chunks per file simultaneously on the GPU —
# typically 2-3x faster than single-chunk sequential transcription on Turing+.
# Falls back to standard WhisperModel if the pipeline is unavailable.

import io
import sys
import logging
from typing import List

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_server")

try:
    import faster_whisper
    from fastapi import FastAPI, File, UploadFile
    import uvicorn
except ImportError as e:
    print(f"Missing dependency: {e}")
    print('Install with: pip install "faster-whisper>=1.0.0" fastapi "uvicorn[standard]"')
    sys.exit(1)

import os

model_name = sys.argv[1] if len(sys.argv) > 1 else "base.en"


def _resolve_device() -> str:
    """Pick the inference device, preferring CUDA when it is actually usable.

    device= was hardcoded to "cuda", which made this server GPU-only: on any
    machine without CUDA it died at model load. That is why the Mac had no
    Whisper server at all while the Go service was configured to call one.

    ctranslate2 (what faster-whisper runs on) has no Metal/MPS backend, so
    Apple Silicon runs on CPU -- but CPU with int8 is perfectly usable, and a
    working CPU server beats no server. Set WHISPER_DEVICE to override.
    """
    forced = os.environ.get("WHISPER_DEVICE", "").strip().lower()
    # "auto" is a legal ctranslate2 device string, but passing it through
    # unresolved would defeat both things below that branch on the device: the
    # compute_type default would pick int8 on a working GPU, and the startup
    # banner would print "auto" instead of what is actually in use -- the exact
    # defect this function exists to fix. Treat it as "probe", not as a device.
    if forced and forced != "auto":
        return forced
    try:
        import ctranslate2

        if ctranslate2.get_cuda_device_count() > 0:
            return "cuda"
    except Exception as exc:  # pragma: no cover - depends on host
        log.warning(f"CUDA probe failed ({exc}); falling back to CPU")
    return "cpu"


device = _resolve_device()

# WHISPER_COMPUTE_TYPE: float16 for Turing+ (RTX series), int8 for Pascal
# (GTX 10-series). The default must follow the device -- ctranslate2's CPU
# backend does not implement float16, so keeping the old unconditional
# "float16" default would trade the hardcoded-cuda crash for a compute-type
# crash and leave CPU hosts exactly as broken.
default_compute = "float16" if device == "cuda" else "int8"
compute_type = os.environ.get("WHISPER_COMPUTE_TYPE", default_compute)
log.info(f"Loading {model_name} device={device} compute={compute_type} (first run downloads model)...")

model = faster_whisper.WhisperModel(
    model_name,
    device=device,
    compute_type=compute_type,
)

# BatchedInferencePipeline was introduced in faster-whisper 1.0.0.
# It processes multiple audio chunks of a single file in parallel on the GPU,
# giving 2-3x speedup on Turing+ GPUs for clips longer than ~10 seconds.
try:
    from faster_whisper import BatchedInferencePipeline
    batched_model = BatchedInferencePipeline(model=model)
    log.info(f"BatchedInferencePipeline available — batch_size={int(os.environ.get('WHISPER_BATCH_SIZE', 8))}")
except (ImportError, Exception) as e:
    batched_model = None
    log.warning(f"BatchedInferencePipeline unavailable ({e}), falling back to standard model")

# The device/compute_type above are what we ASKED ctranslate2 for. What it
# actually loaded can differ, and the difference is the whole point of the
# require_gpu gate downstream: WHISPER_DEVICE is returned verbatim by
# _resolve_device, so a host still carrying an old "WHISPER_DEVICE=cuda"
# launch script advertises cuda regardless of what silicon it has. Reading the
# values back off the loaded model turns /health from an echo of this
# process's configuration into a measurement of what is running. Reported
# separately from the requested values so a mismatch is diagnosable rather
# than merely corrected.
def _read_loaded(attr: str) -> str | None:
    """Read an attribute off the loaded ctranslate2 model, or None.

    None means "this ctranslate2 build does not expose it", which is a
    different fact from "it says cpu" and must stay distinguishable: the
    caller falls back to the requested value but records that it did so, so a
    consumer can tell a measurement from an assumption.
    """
    inner = getattr(model, "model", None)
    value = getattr(inner, attr, None)
    if isinstance(value, str) and value.strip():
        return value.strip().lower()
    return None


_loaded_device = _read_loaded("device")
_loaded_compute_type = _read_loaded("compute_type")
resolved_device = _loaded_device if _loaded_device is not None else device
resolved_compute_type = _loaded_compute_type if _loaded_compute_type is not None else compute_type
resolved_device_source = "model" if _loaded_device is not None else "requested"

if resolved_device != device:
    log.warning(
        f"requested device={device} but ctranslate2 loaded device={resolved_device} — "
        "reporting the loaded device to /health"
    )

log.info(
    f"Ready — model={model_name} device={resolved_device} compute={resolved_compute_type} "
    f"(requested device={device} compute={compute_type})"
)

# VAD parameters tuned for audiobook intros: lower threshold so music/quiet speech
# isn't stripped; shorter silence gap so publisher jingles don't eat the whole clip.
VAD_PARAMS = {
    "threshold": 0.3,            # default 0.5 — lower = more permissive (catches quiet speech)
    "min_silence_duration_ms": 500,   # default 2000 — split on shorter silences
    "min_speech_duration_ms": 200,    # default 250 — keep shorter speech fragments
}

app = FastAPI()


def _do_transcribe(data: bytes, filename: str) -> dict:
    """Transcribe raw WAV bytes. Uses BatchedInferencePipeline when available."""
    try:
        if batched_model is not None:
            segments, info = batched_model.transcribe(
                io.BytesIO(data),
                language="en",
                task="transcribe",
                batch_size=int(os.environ.get("WHISPER_BATCH_SIZE", 8)),
                vad_filter=True,
                vad_parameters=VAD_PARAMS,
            )
        else:
            segments, info = model.transcribe(
                io.BytesIO(data),
                language="en",
                task="transcribe",
                beam_size=5,
                vad_filter=True,
                vad_parameters=VAD_PARAMS,
            )
        text = " ".join(s.text for s in segments).strip()
        log.info(f"transcribed {filename}: {len(text)} chars, {info.duration:.1f}s audio")
        return {"text": text, "error": None}
    except Exception as e:
        log.error(f"transcription failed for {filename}: {e}")
        return {"text": "", "error": str(e)}


@app.post("/transcribe")
async def transcribe(file: UploadFile = File(...)):
    """Single-file endpoint — kept for backward compatibility."""
    data = await file.read()
    return _do_transcribe(data, file.filename)


@app.post("/transcribe-batch")
async def transcribe_batch(files: List[UploadFile] = File(...)):
    """
    Multi-file endpoint. Accepts up to 64 WAV files in one multipart request.
    The filename of each part is used as the result key (Go sends book IDs).
    Processing is sequential on the GPU — the gain over N single requests is
    reduced HTTP overhead and tighter back-to-back GPU scheduling.
    Returns: {"results": {"<filename>": {"text": "...", "error": null}, ...}}
    """
    results = {}
    for f in files:
        data = await f.read()
        results[f.filename] = _do_transcribe(data, f.filename)
    return {"results": results}


@app.get("/health")
async def health():
    """Health probe.

    device/compute_type are reported because the CUDA probe in _resolve_device
    cannot distinguish "no GPU installed" from "GPU transiently unavailable":
    ctranslate2's get_cuda_device_count() returns 0 rather than raising when
    cudaGetDeviceCount fails. A GPU host that comes up during a driver reset
    therefore starts on CPU and stays pinned there for the life of the process,
    serving healthy 200s roughly ten times slower than expected. Surfacing the
    resolved device makes that visible to a health check instead of leaving it
    in a log line. Adding fields is backward compatible -- the Go client
    ignores unknown fields. It no longer decodes ONLY batch_pipeline: since
    the require_gpu gate landed it also reads device, and a require_gpu
    endpoint that omits device is refused rather than assumed healthy.
    """
    batch_available = batched_model is not None
    return {
        "status": "ok",
        "model": model_name,
        "batch_pipeline": batch_available,
        # device is the RESOLVED device read back off the loaded ctranslate2
        # model, not the requested one -- the Go require_gpu gate refuses an
        # endpoint whose device is not in its GPU allow-list, and gating on a
        # requested value would make the gate inert on exactly the host it
        # exists to catch.
        "device": resolved_device,
        "compute_type": resolved_compute_type,
        "requested_device": device,
        "requested_compute_type": compute_type,
        "device_source": resolved_device_source,
    }


if __name__ == "__main__":
    import os
    port = int(os.environ.get("WHISPER_PORT", "19847"))
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="info")
