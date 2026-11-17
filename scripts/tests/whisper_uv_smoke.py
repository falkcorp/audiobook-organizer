# file: scripts/tests/whisper_uv_smoke.py
# version: 1.1.0
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
decode is killed there and the RESTARTED child serves the next request. One decode
process only (num_workers=1): with two, the next request would be taken by the child
that never hung and never restarted, and the replacement would go unexercised.

Exits 0 and prints SMOKE OK, or exits non-zero with the reason."""

import os
import pathlib
import struct
import subprocess
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


def pid_alive(pid):
    """Whether process `pid` exists. Not os.kill(pid, 0): on Windows that terminates it."""
    if sys.platform == "win32":
        out = subprocess.run(
            ["tasklist", "/FI", f"PID eq {pid}", "/NH"], capture_output=True, text=True
        ).stdout
        return str(pid) in out
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def main():
    print("python", sys.version.split()[0], sys.executable)
    check(sys.version_info[:2] == (3, 14), f"expected Python 3.14, got {sys.version}")
    cfg = ws.ServerConfig(device="cpu", num_workers=1)
    engine = ws.build_engine(cfg, transcriber_factory="whisper_fake_backend:FakeTranscriber")
    try:
        print("meta", engine.meta())
        client = TestClient(ws.create_app(cfg, engine))
        health = client.get("/health").json()
        check(health["device"] == "cpu" and health["model"] == "base.en", health)
        first_pid = engine.runners[0].meta()["pid"]
        check(first_pid != os.getpid(), f"the decode ran in the server process ({first_pid})")
        with tempfile.TemporaryDirectory() as d:
            body = pathlib.Path(wav(d, "a.wav")).read_bytes()
            r = client.post("/transcribe", files={"file": ("a.wav", body, "audio/wav")}).json()
            check(r["error"] is None and r["text"].startswith("heard:"), r)
            hung = client.post(
                "/transcribe", files={"file": ("hang.wav", body + b"HANGME", "audio/wav")}
            ).json()
            check((hung["error"] or "").startswith("transcribe_timeout"), hung)
            check(not pid_alive(first_pid), f"the hung decode process {first_pid} is still alive")
            # The only decode process is the one just killed, so this request can only be
            # served by its replacement.
            again = client.post("/transcribe", files={"file": ("b.wav", body, "audio/wav")}).json()
            check(again["error"] is None and again["text"].startswith("heard:"), again)
            new_pid = engine.runners[0].meta()["pid"]
            check(
                new_pid not in (first_pid, os.getpid()), f"no restart: pid {first_pid}->{new_pid}"
            )
            check(pid_alive(new_pid), f"the replacement decode process {new_pid} is not running")
        print("SMOKE OK")
    finally:
        engine.shutdown()


if __name__ == "__main__":
    main()
