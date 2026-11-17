# file: scripts/tests/whisper_uv_smoke.py
# version: 1.0.0
# guid: 8e2a6d40-1c53-4f9b-a7d8-3b5c0e9f1a64
# last-edited: 2026-11-17

# /// script
# requires-python = ">=3.14,<3.15"
# dependencies = ["fastapi", "httpx", "python-multipart"]
# ///
"""Production launch path, with a fake model.

Run as `uv run scripts/tests/whisper_uv_smoke.py`: uv picks the SAME Python line the
supervisor's `uv run whisper_server.py` does (whisper_server.py's inline metadata
pins >=3.14,<3.15), in an environment built from inline metadata. It then goes
through the real build_engine -> spawn -> whisper_server:child_backend ->
load_transcriber factory with only the model class replaced (the transcriber_factory
seam), so no faster-whisper or CUDA is needed. It proves the things the pytest suites
(which run on whatever Python the runner has) cannot: the spawned child is uv's
interpreter, the pipe protocol and pickled config work on that interpreter, and a hung
decode is killed and replaced there.

Exits 0 and prints SMOKE OK, or exits non-zero with the reason."""

import os
import pathlib
import struct
import sys
import tempfile
import wave

os.environ["WHISPER_TRANSCRIBE_MIN_TIMEOUT_S"] = "2"
os.environ["WHISPER_TRANSCRIBE_TIMEOUT_FACTOR"] = "1"
os.environ["WHISPER_MACHINE_ID"] = "gotham-uv-smoke"

HERE = pathlib.Path(__file__).resolve().parent
sys.path[:0] = [str(HERE), str(HERE.parent)]

import whisper_server as ws  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402


def wav(directory, name):
    path = os.path.join(directory, name)
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(16000)
        w.writeframes(b"".join(struct.pack("<h", (i % 50) * 100) for i in range(16000)))
    return path


def check(cond, message):
    if not cond:
        print(f"SMOKE FAILED: {message}")
        sys.exit(1)


def main():
    print("python", sys.version.split()[0], sys.executable)
    check(sys.version_info[:2] == (3, 14), f"expected Python 3.14, got {sys.version}")
    cfg = ws.ServerConfig(device="cpu", num_workers=2)
    engine = ws.build_engine(cfg, transcriber_factory="whisper_fake_backend:FakeTranscriber")
    try:
        print("meta", engine.meta())
        client = TestClient(ws.create_app(cfg, engine))
        health = client.get("/health").json()
        check(health["device"] == "cpu" and health["model"] == "base.en", health)
        pids = {r.meta()["pid"] for r in engine.runners}
        check(os.getpid() not in pids and len(pids) == 2, f"decode pids {pids}")
        with tempfile.TemporaryDirectory() as d:
            body = pathlib.Path(wav(d, "a.wav")).read_bytes()
            r = client.post("/transcribe", files={"file": ("a.wav", body, "audio/wav")}).json()
            check(r["error"] is None and r["text"].startswith("heard:"), r)
            hung = client.post(
                "/transcribe", files={"file": ("hang.wav", body + b"HANGME", "audio/wav")}
            ).json()
            check((hung["error"] or "").startswith("transcribe_timeout"), hung)
            again = client.post("/transcribe", files={"file": ("b.wav", body, "audio/wav")}).json()
            check(again["error"] is None and again["text"].startswith("heard:"), again)
        print("SMOKE OK")
    finally:
        engine.shutdown()


if __name__ == "__main__":
    main()
