# file: scripts/whisper_server.py
# version: 2.4.0
# guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
# last-edited: 2026-06-30
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
# Run on a machine with a fast GPU to offload bulk transcription.
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
# NOTE: faster-whisper uses ctranslate2 for CUDA inference — no separate
# torch install needed. ctranslate2 bundles its own CUDA runtime.
# If you see CUDA errors, ensure CUDA 11.x or 12.x drivers are installed.
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

model_name = sys.argv[1] if len(sys.argv) > 1 else "base.en"
log.info(f"Loading {model_name} (first run downloads ~150MB)...")

model = faster_whisper.WhisperModel(
    model_name,
    device="cuda",
    compute_type="int8",  # int8 works on Pascal+ (GTX 10-series and up); float16 needs Turing+
)

# BatchedInferencePipeline was introduced in faster-whisper 1.0.0.
# It processes multiple audio chunks of a single file in parallel on the GPU,
# giving 2-3x speedup on Turing+ GPUs for clips longer than ~10 seconds.
try:
    from faster_whisper import BatchedInferencePipeline
    batched_model = BatchedInferencePipeline(model=model)
    log.info("BatchedInferencePipeline available — batch endpoint uses batch_size=16")
except (ImportError, Exception) as e:
    batched_model = None
    log.warning(f"BatchedInferencePipeline unavailable ({e}), falling back to standard model")

log.info(f"Ready — model={model_name} device=cuda compute=int8")

app = FastAPI()


def _do_transcribe(data: bytes, filename: str) -> dict:
    """Transcribe raw WAV bytes. Uses BatchedInferencePipeline when available."""
    try:
        if batched_model is not None:
            segments, info = batched_model.transcribe(
                io.BytesIO(data),
                language="en",
                task="transcribe",
                batch_size=16,
                vad_filter=False,
            )
        else:
            segments, info = model.transcribe(
                io.BytesIO(data),
                language="en",
                task="transcribe",
                beam_size=5,
                vad_filter=False,
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
    batch_available = batched_model is not None
    return {"status": "ok", "model": model_name, "batch_pipeline": batch_available}


if __name__ == "__main__":
    import os
    port = int(os.environ.get("WHISPER_PORT", "19847"))
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="info")
