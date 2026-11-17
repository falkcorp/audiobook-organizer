# file: scripts/tests/whisper_fake_backend.py
# version: 1.1.1
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
#   gilhang.wav sleeps in a native call that HOLDS THE GIL, so no Python thread (the
#               orphan watch) can run: only the OS can reap it
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
        if name.startswith("gilhang"):
            _gil_hang()
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


def _gil_hang():
    """A native sleep that keeps the GIL: ctypes.PyDLL does not release it."""
    import ctypes

    if sys.platform == "win32":
        ctypes.PyDLL("kernel32").Sleep(3_600_000)
    else:
        ctypes.PyDLL(None).sleep(3600)


class FakeTranscriber:
    """A stand-in for FasterWhisperTranscriber (the transcriber_factory test seam of
    whisper_server.child_backend): honours the device it is asked for."""

    batched = True
    device_source = "model"

    def __init__(self, cfg, device, compute_type):
        self.model = cfg.model
        self.device = device
        self.compute_type = compute_type
        self.pid = os.getpid()

    def transcribe(self, path):
        return FakeBackend({}).transcribe(path)


def make_pinned(arg):
    """A backend whose `auto` resolution is DIFFERENT on its second load, like a CUDA
    load that fails on a restart: load 1 resolves auto to cuda/large-v2/float16, every
    later auto load falls back to cpu/base.en/int8. An explicit device is honoured,
    and an explicit cuda raises once `cuda_gone` exists (what load_transcriber does:
    an explicit cuda is never papered over). `ignore_pin` makes it fall back even when
    asked explicitly (a factory that disregards the pin: the parent must refuse it)."""
    cfg = arg["cfg"]
    counter = arg["counter"]
    try:
        with open(counter) as f:
            n = int(f.read() or "0")
    except FileNotFoundError:
        n = 0
    with open(counter, "w") as f:
        f.write(str(n + 1))
    device = cfg.get("device", "auto")
    if device == "auto" or arg.get("ignore_pin"):
        device, model, compute = (
            ("cuda", "large-v2", "float16") if n == 0 else ("cpu", "base.en", "int8")
        )
    else:
        model = cfg["model"]
        compute = cfg["compute_type"]
        if device == "cuda" and os.path.exists(arg.get("cuda_gone", "\0")):
            raise RuntimeError("CUDA out of memory")
    be = FakeBackend(arg)
    be.meta.update(model=model, device=device, compute_type=compute, loads=n + 1)
    return be
