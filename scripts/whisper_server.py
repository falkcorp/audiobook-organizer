# file: scripts/whisper_server.py
# version: 1.0.0
# guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
# last-edited: 2026-06-26
#
# Remote Whisper transcription server for use with audiobook-organizer.
# Run on a machine with a fast GPU to offload the bulk transcription op.
#
# Install:
#   pip install faster-whisper fastapi "uvicorn[standard]"
#
# Run (defaults to base.en):
#   python scripts/whisper_server.py [model]
#   python scripts/whisper_server.py small.en
#
# Configure the Go service to use it:
#   Add to deploy/local.conf:  Environment=WHISPER_REMOTE_URL=http://<this-machine-ip>:8000
#   Then: make deploy
#
# Windows firewall: allow inbound TCP 8000 from your LAN subnet.

import io
import sys
import logging

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_server")

try:
    import faster_whisper
    from fastapi import FastAPI, File, UploadFile
    from fastapi.responses import JSONResponse
    import uvicorn
except ImportError as e:
    print(f"Missing dependency: {e}")
    print("Install with: pip install faster-whisper fastapi \"uvicorn[standard]\"")
    sys.exit(1)

model_name = sys.argv[1] if len(sys.argv) > 1 else "base.en"
log.info(f"Loading {model_name} (first run downloads ~150MB)...")

model = faster_whisper.WhisperModel(
    model_name,
    device="cuda",
    compute_type="float16",  # tensor cores on Turing+ (RTX series)
)
log.info(f"Ready — model={model_name} device=cuda compute=float16")

app = FastAPI()


@app.post("/transcribe")
async def transcribe(file: UploadFile = File(...)):
    data = await file.read()
    try:
        segments, info = model.transcribe(
            io.BytesIO(data),
            language="en",
            task="transcribe",
            beam_size=5,
            vad_filter=True,       # skip silent regions, speeds up short clips
        )
        text = " ".join(s.text for s in segments).strip()
        log.info(f"transcribed {file.filename}: {len(text)} chars in {info.duration:.1f}s audio")
        return {"text": text, "error": None}
    except Exception as e:
        log.error(f"transcription failed for {file.filename}: {e}")
        return JSONResponse({"text": "", "error": str(e)}, status_code=200)


@app.get("/health")
async def health():
    return {"status": "ok", "model": model_name}


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000, log_level="info")
