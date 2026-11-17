# file: scripts/tests/whisper_fake_backend.py
# version: 1.0.1
# guid: 8a2f6c41-9d37-4e05-b1c8-3f7e5a09d264
# last-edited: 2026-11-17
#
# A fake decode backend for tests/test_whisper_decode_child.py. The decode child
# imports this module BY NAME in a spawned process (whisper_decode_child's backend
# factory "module:attr"), so it lives next to the tests and must stay importable
# without pytest or fastapi. What a clip does is decided by its file name, so a
# test picks the failure by naming the WAV:
#
#   hang.wav    sleeps for an hour (never answers)
#   crash.wav   exits the process with code 7
#   killed.wav  SIGKILLs itself (POSIX only)
#   boom.wav    raises ValueError (a backend error for that clip, child stays up)
#   slow.wav    answers after 0.4 s
#   anything    answers "heard:<name>"
#   any file whose bytes end in HANGME hangs (uploads choose their own file name)
#
# The factory argument picks how LOADING behaves: {"load": "ok" | "fatal" | "exit" |
# "hang"}, and {"fatal_if_exists": "<path>"} makes the load fatal while that file
# exists (a restart that cannot load its model).

import os
import signal
import sys
import time


class FakeBackend:
    def __init__(self, arg):
        self.meta = {
            "model": "fake",
            "device": "cpu",
            "compute_type": "int8",
            "batched": True,
            "batch_size": 8,
            "device_source": "model",
            "attempted_device": "cpu",
            "attempted_compute_type": "int8",
            "device_reason": "fake backend",
            "fallback": False,
            "pid": os.getpid(),
        }

    def transcribe(self, path):
        name = os.path.basename(path)
        with open(path, "rb") as f:
            if f.read().endswith(b"HANGME"):  # an upload names its own temp file
                time.sleep(3600)
        if name.startswith("hang"):
            time.sleep(3600)
        if name.startswith("crash"):
            os._exit(7)
        if name.startswith("killed"):
            os.kill(os.getpid(), signal.SIGKILL)
        if name.startswith("boom"):
            raise ValueError("model exploded")
        if name.startswith("slow"):
            time.sleep(0.4)
        return f"heard:{name}"


def make(arg):
    mode = (arg or {}).get("load", "ok")
    marker = (arg or {}).get("fatal_if_exists")
    if marker and os.path.exists(marker):
        raise RuntimeError("model file missing")
    if mode == "fatal":
        raise RuntimeError("could not load the model")
    if mode == "exit":
        os._exit(5)
    if mode == "hang":
        time.sleep(3600)
    print("fake backend loaded", file=sys.stderr, flush=True)
    return FakeBackend(arg)
