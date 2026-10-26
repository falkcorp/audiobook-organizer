# file: scripts/tests/fake_whisper_backend.py
# version: 1.1.0
# guid: 5d2e8b17-93a4-4c60-b1f8-7a3c0e94d256
# last-edited: 2026-10-26
#
# A stand-in inference backend for the contract tests of
# scripts/whisper_mlx_server.py. The worker's inference child loads whatever
# WHISPER_BACKEND_FACTORY names ("fake_whisper_backend:factory"), so the tests
# run a REAL child process -- and really kill it -- without Apple silicon or
# mlx_whisper. What the "audio" contains decides what the fake does:
#
#   contains HANGHANG  never returns (the endless decode loop)
#   contains BOOMBOOM  raises
#   contains DIEDIE    the process exits abruptly (a crash, not a hang)
#   contains KILLKILL  the process SIGKILLs itself (what jetsam or an operator does
#                      from outside; the parent sees exit code -9)
#   contains ABRTABRT  the process aborts (SIGABRT, a decoder crash signal)
#   anything else      returns "heard-<byte length>"

import os
import signal
import time


def factory():
    def transcribe(path: str) -> str:
        with open(path, "rb") as f:
            data = f.read()
        if b"HANGHANG" in data:
            while True:
                time.sleep(60)
        if b"BOOMBOOM" in data:
            raise ValueError("fake backend exploded")
        if b"DIEDIE" in data:
            os._exit(9)
        if b"KILLKILL" in data:
            os.kill(os.getpid(), signal.SIGKILL)
            time.sleep(60)
        if b"ABRTABRT" in data:
            os.abort()
        return f"heard-{len(data)}"

    return transcribe
