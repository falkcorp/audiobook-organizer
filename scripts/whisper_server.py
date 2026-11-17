# file: scripts/whisper_server.py
# version: 5.2.0
# guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
# last-edited: 2026-11-17
#
# /// script
# requires-python = ">=3.14,<3.15"
# # Upper bound is deliberate: the default python3 on the Mac is 3.15, and
# # uvicorn[standard] has no cp315 wheels yet, so an open ">=3.14" makes uv
# # pick 3.15 and compile from source. Lift the cap once 3.15 wheels exist.
# dependencies = [
#   "faster-whisper>=1.0.0",
#   # faster-whisper 1.2.1 calls av.open(..., metadata_errors=...), which PyAV 19
#   # removed, and faster-whisper only requires av>=11: unpinned, uv resolves av 19
#   # and every decode fails with "open() got an unexpected keyword argument
#   # 'metadata_errors'". Lift the cap once faster-whisper handles it.
#   "av>=11,<19",
#   "fastapi>=0.111",
#   "uvicorn[standard]>=0.29",
#   "python-multipart>=0.0.9",
# ]
# ///
#
# Remote Whisper transcription worker for audiobook-organizer, for machines that
# are not a Mac (the Mac worker is scripts/whisper_mlx_server.py). It runs on a
# CUDA GPU when there is one and on the CPU when there is not, chosen by
# --device auto|cpu|cuda (WHISPER_DEVICE); the default, auto, uses CUDA when a
# device is present and loads, and otherwise says so in one log line and runs on
# CPU. ctranslate2 has no Metal backend, so Apple Silicon is CPU here too.
#
# It serves the same two ways the Mac worker does:
#   * the PATH pipeline (/transcribe-path, /transcribe-path-batch): the server
#     sends a file PATH, this worker maps it through WHISPER_PATH_MAP, cuts the
#     first seconds with ffmpeg and transcribes the clip. The pipeline, its path
#     safety and /health fields are shared with the Mac worker and live in
#     scripts/whisper_path_pipeline.py and scripts/whisper_machine_id.py, and the
#     killable decode child is scripts/whisper_decode_child.py: copy all four
#     files together.
#   * the older WAV UPLOAD endpoints (/transcribe, /transcribe-batch).
#
# Run (uv handles all deps -- no pip install needed):
#   uv run scripts/whisper_server.py                      # auto device, 127.0.0.1 only
#   uv run scripts/whisper_server.py --device cpu         # force CPU
#   uv run scripts/whisper_server.py small.en --device cpu --cpu-threads 6
#
# Settings (flag, then environment, then default):
#   --device         WHISPER_DEVICE        auto | cpu | cuda          auto
#   --model          WHISPER_MODEL         faster-whisper model name  large-v2 on cuda,
#                    (or the first positional argument)                   base.en on cpu
#   --compute-type   WHISPER_COMPUTE_TYPE  float16 on cuda, int8 on cpu
#   --cpu-threads    WHISPER_CPU_THREADS   logical cores - 2, 1..8
#   --num-workers    WHISPER_NUM_WORKERS   decode processes (each holds
#                                          its own copy of the model)    1
#   --batch-size     WHISPER_BATCH_SIZE    batched-pipeline batch size    8
#   --host           WHISPER_HOST          127.0.0.1 (loopback only; see below)
#   --port           WHISPER_PORT          19847
#
# CPU guidance (starting points, NOT benchmarked on a specific machine; measure
# a dry-run dispatch and adjust): int8 is the right CPU compute type and the
# only sensible default (float16 is not implemented on CPU). A desktop CPU does
# the 90 s intro clips this worker is sent at several times real time with
# base.en and noticeably slower with small.en, so keep base.en unless the
# transcripts are not good enough and the machine has cores to spare. One decode
# at a time with every thread on it finishes each clip soonest; raise
# --num-workers only if the machine has many more cores than --cpu-threads
# (workers x threads is the number of cores kept busy).
#
# BINDING: there is no authentication on any route, and the path routes read files
# under WHISPER_PATH_MAP, so anything that can reach the port can obtain transcripts
# of files under the mapped roots. The default is therefore loopback, like the Mac
# worker: the server reaches workers through an SSH tunnel. A worker meant to be
# reached over the LAN must opt in with --host 0.0.0.0 (or an interface address),
# must be firewalled to the server, and logs a warning when it is.
#
# DECODING happens in a child process (scripts/whisper_decode_child.py): the model
# loads there, the parent enforces a per-clip deadline max(floor, factor x audio
# seconds) and kills a child that misses it, and the next clip starts a fresh one.
# A hung or crashed decode answers transcribe_timeout / decoder_crashed /
# decoder_killed exactly like the Mac worker and the worker recovers by itself.
# The deadline constants are generous and NOT benchmarked (a slow CPU must not be
# killed for being slow): WHISPER_TRANSCRIBE_MIN_TIMEOUT_S (300) and
# WHISPER_TRANSCRIBE_TIMEOUT_FACTOR (8), and the computed deadline is capped at
# WHISPER_TRANSCRIBE_MAX_TIMEOUT_S (3600), so an enormous upload cannot earn an
# unbounded one. Upload bodies are capped at MAX_UPLOAD_BYTES (256 MiB).
#
# The FIRST child to start fixes the model, device and compute type this worker
# serves; every later child and restart is started with those values explicitly
# and refused if it reports anything else (no silent GPU-to-CPU fallback after the
# first resolution, and with --num-workers > 1 every child serves the same model).
#
# Windows: see docs/whisper-remote-extract.md. Needs ffmpeg on PATH for the path
# pipeline. WHISPER_PATH_MAP local sides are drive paths or UNC shares.

import argparse
import contextlib
import dataclasses
import hashlib
import importlib
import json
import logging
import os
import queue
import sys
import tempfile
import threading
import time
from typing import Annotated, Any, Callable, List, Optional, Protocol

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_server")

try:
    from fastapi import FastAPI, File, Request, UploadFile
except ImportError as e:  # pragma: no cover - dependency probe
    print(f"Missing dependency: {e}")
    print('Install with: pip install "faster-whisper>=1.0.0" fastapi "uvicorn[standard]"')
    sys.exit(1)

# The shared worker modules live next to this file: put that directory first on
# sys.path so they import however this file is started (uv run, the Windows
# supervisor, a test that loads it by path).
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from whisper_decode_child import DecodeChild, StartPin  # noqa: E402
from whisper_machine_id import MachineIdentity  # noqa: E402
from whisper_path_pipeline import (  # noqa: E402
    EXTRACT_FINGERPRINT,
    BodyLimitMiddleware,
    ChildFaultError,
    PathPipeline,
    abandon_deadline,
    abandoned_result,
    audio_seconds,
    error_result,
    install_path_routes,
    misconfigured_error,
    past,
)

DEVICES = ("auto", "cpu", "cuda")
# The model follows the device that RESOLVES, not a single default: a GPU install
# has always run large-v2 (which is what the Windows supervisor passed), and
# silently dropping it to base.en would change every transcript and orphan every
# journalled one. A CPU worker defaults to the small model.
DEFAULT_CUDA_MODEL = "large-v2"
DEFAULT_CPU_MODEL = "base.en"
LOOPBACK_HOSTS = ("127.0.0.1", "::1", "localhost")
BACKEND_FACTORY = "whisper_server:child_backend"
DEFAULT_PORT = 19847
DEFAULT_BATCH_SIZE = 8
BEAM_SIZE = 5
LANGUAGE = "en"
# ctranslate2 compute types that exist on each device. float16 is not
# implemented on CPU, and int8_float16 needs a GPU.
CPU_COMPUTE_TYPES = ("int8", "int8_float32", "float32", "int16")
CUDA_ONLY_COMPUTE_TYPES = ("float16", "int8_float16", "bfloat16")


# The per-clip deadline: max(floor, factor x audio seconds). A decode past it is
# killed (the clip answers transcribe_timeout, which the Go side counts as a
# strike), so it is generous on purpose: a slow CPU must not be killed for being
# slow. NOT benchmarked on a particular machine; tune with the environment.
# /health reports both so the server sizes its own HTTP ceiling from them.
def _env_seconds(name: str, default: float, *, maximum: float = 7 * 86400.0) -> float:
    """A duration from the environment: finite, positive, and small enough for the
    pipe poll (about 24 days at most). nan, inf, zero, negative or text exits at
    startup with the variable's name rather than reaching a decode as an overflow
    that would desynchronise the child's channel."""
    raw = os.environ.get(name)
    if raw is None or not raw.strip():
        return default
    try:
        value = float(raw)
    except ValueError:
        value = float("nan")
    if not (0.0 < value <= maximum):  # False for nan and inf as well
        raise SystemExit(f"{name}={raw!r} must be a number greater than 0 and at most {maximum:g}")
    return value


CLIP_TIMEOUT_FLOOR_S = _env_seconds("WHISPER_TRANSCRIBE_MIN_TIMEOUT_S", 300.0)
CLIP_TIMEOUT_FACTOR = _env_seconds("WHISPER_TRANSCRIBE_TIMEOUT_FACTOR", 8.0, maximum=1e4)
# Hard ceiling on the COMPUTED deadline, max(floor, factor x audio length). At the
# default factor of 8 the cap binds for audio longer than 450 s: a 10-minute clip
# is cut to 3600 s (6x its length, not 8x), and so is anything longer. An intro
# clip is far shorter than 450 s, so this does not touch the clips this worker is
# sent; it is there for what a huge upload would otherwise earn (a 1 GB upload read
# as 31,250 s of audio would get 69 hours, and busy_limit_seconds would keep the
# server's stuck rule from ever firing). The Go client sizes its own ceiling from
# the uncapped formula, so it can only wait longer than this worker does.
CLIP_TIMEOUT_CAP_S = _env_seconds("WHISPER_TRANSCRIBE_MAX_TIMEOUT_S", 3600.0)
# Upload routes (/transcribe, /transcribe-batch) take whole WAVs, which the path
# routes' 8 MiB limit would refuse. 256 MiB is about 2.3 hours of 16 kHz MONO 16-bit
# PCM (32,000 bytes a second), or about 1.2 hours of stereo; either is far more than
# an intro clip. The whole body is read into memory.
MAX_UPLOAD_BYTES = 256 * 1024 * 1024
# How long a RESTARTED child may take to load the model (cached by then). Reported
# on /health: the server adds it to its HTTP ceiling.
CHILD_READY_TIMEOUT_S = _env_seconds("WHISPER_CHILD_READY_TIMEOUT_S", 300.0)
CHILD_FAULT_TTL_S = _env_seconds("WHISPER_CHILD_FAULT_TTL_S", 300.0)
# The FIRST start may download the model (large-v2 is about 3 GB), so it waits far
# longer than a restart. Startup is before the worker listens, not inside a request.
STARTUP_TIMEOUT_S = _env_seconds("WHISPER_STARTUP_TIMEOUT_S", 3600.0)

# VAD parameters tuned for audiobook intros: lower threshold so music/quiet speech
# isn't stripped; shorter silence gap so publisher jingles don't eat the whole clip.
VAD_PARAMS = {
    "threshold": 0.3,  # default 0.5 — lower = more permissive (catches quiet speech)
    "min_silence_duration_ms": 500,  # default 2000 — split on shorter silences
    "min_speech_duration_ms": 200,  # default 250 — keep shorter speech fragments
}


# ── configuration ────────────────────────────────────────────────────────────


@dataclasses.dataclass(frozen=True)
class ServerConfig:
    model: Optional[str] = None  # None = the resolved device's default
    device: str = "auto"  # what was ASKED: auto | cpu | cuda
    compute_type: Optional[str] = None  # None = the device's default
    cpu_threads: Optional[int] = None  # None = default_cpu_threads()
    num_workers: int = 1  # decode processes
    batch_size: int = DEFAULT_BATCH_SIZE
    host: str = "127.0.0.1"
    port: int = DEFAULT_PORT


def default_cpu_threads(logical_cores: Optional[int] = None) -> int:
    """Threads for one CPU decode: every logical core but two, 1..8.

    Two are left for the OS, ffmpeg (the path pipeline cuts clips alongside the
    decode) and the /health poll -- this is usually a desktop someone also uses.
    More than 8 threads buys little for one Whisper decode (ctranslate2 scales
    sub-linearly), so the cap leaves the rest of a big CPU to --num-workers."""
    cores = logical_cores if logical_cores is not None else (os.cpu_count() or 2)
    return max(1, min(8, cores - 2))


def _positive_int(text: str) -> int:
    try:
        value = int(text)
    except ValueError:
        raise argparse.ArgumentTypeError(f"{text!r} is not an integer")
    if value < 1:
        raise argparse.ArgumentTypeError(f"{value} must be at least 1")
    return value


def build_parser(env: dict) -> argparse.ArgumentParser:
    def from_env(name: str, default: Any = None) -> Any:
        return env.get(name, "").strip() or default

    parser = argparse.ArgumentParser(
        prog="whisper_server.py",
        description="Whisper transcription worker (CUDA or CPU) for audiobook-organizer.",
    )
    parser.add_argument("model_pos", nargs="?", metavar="model", help="same as --model")
    parser.add_argument("--model", default=None)
    parser.add_argument(
        "--device",
        choices=DEVICES,
        type=str.lower,
        default=from_env("WHISPER_DEVICE", "auto").lower(),
        help="auto uses CUDA when present and loadable, else CPU (default: auto)",
    )
    parser.add_argument("--compute-type", default=from_env("WHISPER_COMPUTE_TYPE"))
    parser.add_argument(
        "--cpu-threads",
        type=_positive_int,
        default=from_env("WHISPER_CPU_THREADS"),
        help="threads per CPU decode (default: logical cores - 2, 1..8)",
    )
    parser.add_argument(
        "--num-workers",
        type=_positive_int,
        default=from_env("WHISPER_NUM_WORKERS", 1),
        help="decode processes, each with its own copy of the model (default: 1)",
    )
    parser.add_argument(
        "--batch-size",
        type=_positive_int,
        default=from_env("WHISPER_BATCH_SIZE", DEFAULT_BATCH_SIZE),
    )
    parser.add_argument(
        "--host",
        default=from_env("WHISPER_HOST", "127.0.0.1"),
        help="bind address (default: 127.0.0.1; anything wider exposes the file-read "
        "routes to the network and has no authentication)",
    )
    parser.add_argument(
        "--port", type=_positive_int, default=from_env("WHISPER_PORT", DEFAULT_PORT)
    )
    return parser


def parse_config(argv: List[str], env: Optional[dict] = None) -> ServerConfig:
    """argv (without the program name) and the environment to a ServerConfig.
    A flag beats its environment variable. Exits with a usage error on a bad value,
    including a compute type that cannot run on an explicitly chosen device."""
    env = os.environ if env is None else env
    parser = build_parser(env)
    args = parser.parse_args(argv)
    # type= is not applied to string defaults taken from the environment in every
    # argparse version, so normalise them the same way the flags are.
    for name in ("cpu_threads", "num_workers", "batch_size", "port"):
        value = getattr(args, name)
        if isinstance(value, str):
            try:
                setattr(args, name, _positive_int(value))
            except argparse.ArgumentTypeError as e:
                parser.error(f"{name.replace('_', '-')}: {e}")
    # choices= is not checked against a default taken from the environment.
    if args.device not in DEVICES:
        parser.error(f"WHISPER_DEVICE {args.device!r} is not one of: {', '.join(DEVICES)}")
    model = args.model or args.model_pos or env.get("WHISPER_MODEL", "").strip() or None
    compute = args.compute_type.strip().lower() if args.compute_type else None
    if compute and args.device == "cpu" and compute not in CPU_COMPUTE_TYPES:
        parser.error(
            f"--compute-type {compute} cannot run on the CPU "
            f"(use one of: {', '.join(CPU_COMPUTE_TYPES)})"
        )
    return ServerConfig(
        model=model,
        device=args.device,
        compute_type=compute,
        cpu_threads=args.cpu_threads,
        num_workers=args.num_workers,
        batch_size=args.batch_size,
        host=args.host,
        port=args.port,
    )


# ── device resolution ────────────────────────────────────────────────────────


def cuda_device_count() -> "tuple[int, str]":
    """(CUDA devices ctranslate2 can see, "" or why it could not look). Never
    raises: not having a usable GPU is an answer, not an error.

    ctranslate2's get_cuda_device_count() returns 0 rather than raising when the
    driver call fails, so a GPU host that comes up mid driver reset reads as "no
    GPU" here: /health reports the device actually in use for exactly that case."""
    try:
        import ctranslate2

        return int(ctranslate2.get_cuda_device_count()), ""
    except Exception as exc:  # pragma: no cover - depends on host
        return 0, f"{type(exc).__name__}: {exc}"


def resolve_device(
    requested: str, count_cuda: Callable[[], "tuple[int, str]"] = cuda_device_count
) -> "tuple[str, str]":
    """(device, reason) for a requested device of auto, cpu or cuda.

    cpu and cuda are taken at their word (cuda is not probed: if it cannot load,
    that is an error to see, not something to paper over). auto probes."""
    if requested == "cpu":
        return "cpu", "requested cpu"
    if requested == "cuda":
        return "cuda", "requested cuda"
    count, why = count_cuda()
    if count > 0:
        return "cuda", f"auto: {count} CUDA device(s) found"
    if why:
        # The reason is on the unauthenticated /health (device_reason), so it is a
        # fixed phrase; the probe's own error text can name a library path.
        log.warning("CUDA probe failed: %s", why)
        return "cpu", "auto: CUDA probe failed"
    return "cpu", "auto: no CUDA device found"


def effective_compute_type(cfg: ServerConfig, device: str) -> str:
    """The compute type to load with. Unset means the device's default (float16 on
    cuda, int8 on cpu). A GPU-only type asked for on a CPU only gets here through
    --device auto (an explicit cpu was refused at parse time), where the same
    configuration has to work on both kinds of machine, so it is replaced by int8
    with a warning rather than crashing the load."""
    if device == "cuda":
        return cfg.compute_type or "float16"
    if cfg.compute_type is None:
        return "int8"
    if cfg.compute_type in CPU_COMPUTE_TYPES:
        return cfg.compute_type
    log.warning(
        "compute type %s cannot run on the CPU; using int8 (set --compute-type int8 "
        "to silence this)",
        cfg.compute_type,
    )
    return "int8"


# ── the model behind a small interface ───────────────────────────────────────


def resolve_model(cfg: ServerConfig, device: str) -> str:
    """The model to load on `device`: the configured one, else large-v2 on cuda (what
    a GPU install has always run) and base.en on the CPU."""
    return cfg.model or (DEFAULT_CUDA_MODEL if device == "cuda" else DEFAULT_CPU_MODEL)


class Transcriber(Protocol):
    """What the child needs from a speech model. `audio` is a path to a WAV."""

    device: str  # what actually loaded, not what was asked for
    compute_type: str
    batched: bool
    device_source: str  # "model" if read back off the loaded model, else "requested"

    def transcribe(self, audio: str) -> str: ...


class FasterWhisperTranscriber:
    """faster-whisper (ctranslate2), imported only when one is built so the
    rest of this module loads and tests without it."""

    def __init__(self, cfg: ServerConfig, device: str, compute_type: str) -> None:
        import faster_whisper

        # One decode per process: concurrency is --num-workers PROCESSES, so a hung
        # decode can be killed on its own. ctranslate2's own num_workers stays 1.
        kwargs: dict = {"device": device, "compute_type": compute_type, "num_workers": 1}
        if device == "cpu":
            kwargs["cpu_threads"] = cfg.cpu_threads or default_cpu_threads()
        self._model = faster_whisper.WhisperModel(cfg.model, **kwargs)
        self._batch_size = cfg.batch_size
        # BatchedInferencePipeline (faster-whisper >= 1.0.0) decodes several chunks
        # of one file at once; falls back to the plain model when unavailable.
        try:
            from faster_whisper import BatchedInferencePipeline

            self._batched = BatchedInferencePipeline(model=self._model)
        except Exception as e:
            self._batched = None
            log.warning(f"BatchedInferencePipeline unavailable ({e}), using the standard model")
        self.batched = self._batched is not None
        # The device/compute type above are what we ASKED ctranslate2 for; what it
        # loaded can differ, and the difference is what the server's require_gpu gate
        # exists to catch. Read them back off the model so /health measures what is
        # running rather than echoing this process's configuration.
        self.device, self.device_source = self._read_loaded("device", device)
        self.compute_type, _ = self._read_loaded("compute_type", compute_type)

    def _read_loaded(self, attr: str, requested: str) -> "tuple[str, str]":
        value = getattr(getattr(self._model, "model", None), attr, None)
        if isinstance(value, str) and value.strip():
            return value.strip().lower(), "model"
        return requested, "requested"

    def transcribe(self, audio: str) -> str:
        if self._batched is not None:
            segments, _info = self._batched.transcribe(
                audio,
                language=LANGUAGE,
                task="transcribe",
                batch_size=self._batch_size,
                vad_filter=True,
                vad_parameters=VAD_PARAMS,
            )
        else:
            segments, _info = self._model.transcribe(
                audio,
                language=LANGUAGE,
                task="transcribe",
                beam_size=BEAM_SIZE,
                vad_filter=True,
                vad_parameters=VAD_PARAMS,
            )
        return " ".join(s.text for s in segments).strip()


@dataclasses.dataclass
class LoadedModel:
    transcriber: Transcriber
    model: str  # the model that loaded (the device's default when none was given)
    attempted_device: str  # the first device tried; differs from the loaded one after a fallback
    attempted_compute_type: str
    device_reason: str
    fallback: bool = False


def load_transcriber(
    cfg: ServerConfig,
    make: Callable[[ServerConfig, str, str], Transcriber] = FasterWhisperTranscriber,
    count_cuda: Callable[[], "tuple[int, str]"] = cuda_device_count,
) -> LoadedModel:
    """Resolve the device, load the model on it, and log ONE line saying what
    is running and why.

    --device auto that finds a CUDA device but cannot load the model on it (a
    driver or CUDA runtime problem) falls back to the CPU, once, at WARNING level
    with the reason in the line, and /health shows it (requested_device auto,
    device cpu, fallback true). It lasts the life of the child. An explicit
    --device cuda that cannot load is NOT papered over: it raises, so a worker an
    operator asked to be a GPU worker never quietly serves from the CPU."""
    device, reason = resolve_device(cfg.device, count_cuda)
    compute = effective_compute_type(cfg, device)
    attempted_device, attempted_compute = device, compute
    model = resolve_model(cfg, device)
    fallback = False
    try:
        transcriber = make(dataclasses.replace(cfg, model=model), device, compute)
    except Exception as exc:
        if not (cfg.device == "auto" and device == "cuda"):
            raise
        fallback = True
        # A fixed phrase for /health's device_reason; the exception is in the log line
        # below (an exception from a model load can name a cache path under a user
        # profile, and /health has no authentication).
        reason = "auto: CUDA found but the model failed to load on it; using cpu"
        device = "cpu"
        compute = effective_compute_type(dataclasses.replace(cfg, compute_type=None), "cpu")
        model = resolve_model(cfg, "cpu")
        log.warning(
            "whisper GPU FALLBACK: a CUDA device was found but model %s failed to load on "
            "it (%s); this worker now runs on the CPU with model %s until it is restarted. "
            "Set require_gpu on endpoints that must be GPU.",
            resolve_model(cfg, "cuda"),
            exc,
            model,
        )
        transcriber = make(dataclasses.replace(cfg, model=model), device, compute)
    log.info(
        "whisper device: %s compute=%s model=%s threads=%s workers=%s (%s)",
        transcriber.device,
        transcriber.compute_type,
        model,
        (cfg.cpu_threads or default_cpu_threads()) if transcriber.device == "cpu" else "n/a",
        cfg.num_workers,
        reason,
    )
    if transcriber.device != device:
        log.warning(
            "asked ctranslate2 for device=%s but it loaded device=%s; /health reports the "
            "loaded device",
            device,
            transcriber.device,
        )
    return LoadedModel(transcriber, model, attempted_device, attempted_compute, reason, fallback)


# DECODE_FINGERPRINT is a short hash of every setting that changes the text this
# server returns for the same WAV bytes. The server folds it into the whisper
# result-journal key, so retuning any of these (a VAD threshold, the model, the
# compute type, the decode path) makes previously journalled transcripts
# unreachable instead of serving them for the new configuration. The settings
# and their order are unchanged from 2.x, so an existing GPU install (large-v2,
# float16, batched, batch size 8) keeps its fingerprint; a CPU worker's differs by
# model and compute type, as it should.
def decode_fingerprint(model: str, batch_size: int, compute_type: str, batched: bool) -> str:
    settings = {
        "backend": "faster-whisper",
        "model": model,
        "compute_type": compute_type,
        # The batched pipeline and the beam-search fallback are different decoders.
        "path": "batched" if batched else "beam",
        "batch_size": batch_size if batched else None,
        "beam_size": BEAM_SIZE if not batched else None,
        "language": LANGUAGE,
        "vad_filter": True,
        "vad_parameters": VAD_PARAMS,
    }
    return hashlib.sha256(json.dumps(settings, sort_keys=True).encode()).hexdigest()[:16]


class ChildBackend:
    """What the decode child builds: the model plus the meta the parent reports."""

    def __init__(self, cfg: ServerConfig, loaded: LoadedModel) -> None:
        self._transcriber = loaded.transcriber
        t = loaded.transcriber
        self.meta = {
            "model": loaded.model,
            "device": t.device,
            "compute_type": t.compute_type,
            "batched": bool(t.batched),
            "batch_size": cfg.batch_size,
            "device_source": t.device_source,
            "attempted_device": loaded.attempted_device,
            "attempted_compute_type": loaded.attempted_compute_type,
            "device_reason": loaded.device_reason,
            "fallback": loaded.fallback,
        }

    def transcribe(self, wav_path: str) -> str:
        return self._transcriber.transcribe(wav_path)


def child_backend(arg: dict) -> ChildBackend:
    """The factory the decode child calls (named by BACKEND_FACTORY): rebuild the
    config from plain data, resolve the device and load the model, IN THE CHILD.

    arg["transcriber_factory"] ("module:attr") replaces the faster-whisper model
    class: a test seam so the production wiring (build_engine, the pickled config,
    this factory, load_transcriber) can run in CI with a fake model."""
    cfg = ServerConfig(**arg["cfg"])
    spec = arg.get("transcriber_factory")
    if spec:
        module, _, attr = spec.partition(":")
        make = getattr(importlib.import_module(module), attr)
        return ChildBackend(cfg, load_transcriber(cfg, make=make))
    return ChildBackend(cfg, load_transcriber(cfg))


# ── running a decode ─────────────────────────────────────────────────────────


class InProcessRunner:
    """A runner that decodes in THIS process, with no deadline and no kill. Production
    never uses it (a hung decode would wedge the worker; see DecodeChild); it exists so
    the HTTP surface can be tested without spawning a process per test."""

    def __init__(self, transcriber: Transcriber) -> None:
        self._t = transcriber
        self._mu = threading.Lock()
        self._since: Optional[float] = None
        self._limit: Optional[float] = None

    def run(self, wav_path: str, timeout_s: float) -> str:
        with self._mu:
            self._since, self._limit = time.monotonic(), timeout_s
        try:
            return self._t.transcribe(wav_path)
        finally:
            with self._mu:
                self._since = self._limit = None

    def busy(self) -> "tuple[float, Optional[float]]":
        with self._mu:
            if self._since is None:
                return 0.0, None
            return time.monotonic() - self._since, self._limit

    def start_error(self) -> Optional[str]:
        return None

    def meta(self) -> dict:
        t = self._t
        return {
            "model": getattr(t, "model", None) or DEFAULT_CPU_MODEL,
            "device": t.device,
            "compute_type": t.compute_type,
            "batched": bool(t.batched),
            "batch_size": DEFAULT_BATCH_SIZE,
            "device_source": t.device_source,
            "attempted_device": t.device,
            "attempted_compute_type": t.compute_type,
            "device_reason": "in-process test runner",
            "fallback": False,
        }

    def shutdown(self) -> None:
        pass


class Engine:
    """Runs decodes for the HTTP handlers on a pool of runners (one decode process
    each), and aggregates what /health reports: the most overdue running clip, the
    last start failure, and the loaded model's meta."""

    def __init__(self, runners: list, pin: Optional[StartPin] = None) -> None:
        self.runners = list(runners)
        self._pin = pin
        self._free: queue.Queue = queue.Queue()
        for r in self.runners:
            self._free.put(r)

    @staticmethod
    def clip_limit_s(audio_s: Optional[float]) -> float:
        return max(
            CLIP_TIMEOUT_FLOOR_S, min(CLIP_TIMEOUT_CAP_S, CLIP_TIMEOUT_FACTOR * (audio_s or 0.0))
        )

    def run(self, wav_path: str, audio_s: Optional[float]) -> str:
        limit = self.clip_limit_s(audio_s)
        runner = self._free.get()
        try:
            return runner.run(wav_path, limit)
        finally:
            self._free.put(runner)

    def busy(self) -> "tuple[float, Optional[float]]":
        """(seconds the most overdue running decode has run, its limit), or (0, None)
        when idle. Never waits on a decode slot, so a stuck decode cannot hide."""
        now_busy = [b for b in (r.busy() for r in self.runners) if b[1] is not None]
        if not now_busy:
            return 0.0, None
        return max(now_busy, key=lambda b: b[0] - (b[1] or 0.0))

    def start_error(self) -> Optional[str]:
        for r in self.runners:
            err = r.start_error()
            if err:
                return err
        return None

    def unprotected(self) -> bool:
        """True when any decode child runs without the Windows job object that kills
        it with this process (see DecodeChild.unprotected)."""
        return any(getattr(r, "unprotected", lambda: False)() for r in self.runners)

    def meta(self) -> dict:
        """What /health reports: the PINNED resolution (the first start's model,
        device and compute type, which every child is held to), else the first live
        runner's."""
        if self._pin is not None:
            pinned = self._pin.meta()
            if pinned:
                return pinned
        for r in self.runners:
            m = r.meta()
            if m:
                return m
        return {}

    def shutdown(self) -> None:
        for r in self.runners:
            with contextlib.suppress(Exception):
                r.shutdown()


def _pin_arg(base: dict, pinned: dict) -> dict:
    """The factory argument for every child after the first resolution: the same
    config with the model, device and compute type the first child reported, now
    EXPLICIT (an explicit cuda is strict, so a restart that cannot load it is a
    start fault, never a quiet CPU run)."""
    cfg = dict(base["cfg"])
    cfg.update(model=pinned["model"], device=pinned["device"], compute_type=pinned["compute_type"])
    return {**base, "cfg": cfg}


def build_engine(cfg: ServerConfig, transcriber_factory: Optional[str] = None) -> Engine:
    """The production engine: --num-workers decode processes, started now (the model
    is loaded, or the load error raised, before the worker listens).

    The first child resolves `auto`; the rest (and every restart) are pinned to what
    it resolved. A child that cannot load exactly that fails the start, so the
    worker refuses to serve rather than serve two different models.
    `transcriber_factory` ("module:attr") is a test seam, see child_backend."""
    pin = StartPin()
    arg: dict = {"cfg": dataclasses.asdict(cfg)}
    if transcriber_factory:
        arg["transcriber_factory"] = transcriber_factory
    children = [
        DecodeChild(
            BACKEND_FACTORY,
            arg,
            ready_timeout_s=CHILD_READY_TIMEOUT_S,
            fault_ttl_s=CHILD_FAULT_TTL_S,
            pin=pin,
            pin_arg=_pin_arg,
        )
        for _ in range(cfg.num_workers)
    ]
    engine = Engine(children, pin)
    try:
        for c in children:
            c.start(STARTUP_TIMEOUT_S)
    except BaseException:
        engine.shutdown()
        raise
    return engine


# ── the app ──────────────────────────────────────────────────────────────────


def create_app(
    cfg: ServerConfig,
    engine: Engine,
    pipeline: Optional[PathPipeline] = None,
    identity: Optional[MachineIdentity] = None,
) -> FastAPI:
    app = FastAPI()
    app.add_middleware(
        BodyLimitMiddleware, paths=("/transcribe", "/transcribe-batch"), limit=MAX_UPLOAD_BYTES
    )
    pipeline = pipeline or PathPipeline()
    identity = identity or MachineIdentity()
    identity.start()
    app.state.engine = engine
    app.state.pipeline = pipeline
    app.state.identity = identity

    def identity_of(meta: dict) -> dict:
        """What this worker's output is filed under: the model and backend it decodes
        with, and the fingerprints of its decode and extraction settings. ONE function
        for /health and for every transcription response, so the two cannot disagree
        about what the worker is. It is read from the engine's pin (the first start's
        resolution, which every decode child is held to), so it is the identity of the
        process that decoded, whichever child process that was."""
        model = meta.get("model", cfg.model or DEFAULT_CPU_MODEL)
        return {
            "model": model,
            "backend": "faster-whisper",
            "decode_fingerprint": decode_fingerprint(
                model,
                int(meta.get("batch_size", cfg.batch_size)),
                meta.get("compute_type", ""),
                bool(meta.get("batched", False)),
            ),
            "extract_fingerprint": EXTRACT_FINGERPRINT,
        }

    def response_identity() -> dict:
        return identity_of(engine.meta())

    def transcribe_clip(wav_path: str) -> str:
        return engine.run(wav_path, audio_seconds(wav_path))

    def run_path_jobs(jobs: dict, deadline: Optional[float] = None) -> dict:
        return pipeline.run_path_jobs(jobs, deadline, transcribe_clip)

    install_path_routes(app, run_path_jobs, response_identity)

    def do_transcribe(data: bytes, filename: str, on_start_fault=None) -> dict:
        """One upload to the {"text","error"} shape. Never raises: one bad WAV must
        not fail a batch. The deadline is computed from the upload's own audio
        length, like a path clip's, not a flat limit."""
        tmp_path = None
        try:
            with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tf:
                tf.write(data)
                tmp_path = tf.name
            text = engine.run(tmp_path, audio_seconds(tmp_path))
            log.info(f"transcribed {filename}: {len(text)} chars")
            return {"text": text, "error": None}
        except ChildFaultError as e:
            log.error(f"decode child fault for {filename}: {e}")
            if e.starting and on_start_fault is not None:
                on_start_fault(e)
            return misconfigured_error(e)
        except Exception as e:
            # whisper_error and a fixed sentence; the exception is in the log, which
            # error_result writes.
            return error_result(e, f"transcription failed for {filename}")
        finally:
            if tmp_path:
                with contextlib.suppress(OSError):
                    os.unlink(tmp_path)

    def run_upload_batch(files, deadline: Optional[float] = None) -> dict:
        """Sequential. Once `deadline` (monotonic) has passed, clips not yet started
        are answered as abandoned: the client gave up and will never read them, and
        decoding them would hold a slot. The check is before STARTING a clip."""
        results: dict = {}
        start_fault: list = []
        for f in files:
            if past(deadline):
                results[f.filename] = abandoned_result()
            elif start_fault:
                results[f.filename] = misconfigured_error(start_fault[0])
            else:
                results[f.filename] = do_transcribe(f.file.read(), f.filename, start_fault.append)
        return results

    # Plain `def`, not `async def`: FastAPI runs these in its threadpool, so a
    # decode does not freeze the event loop and /health keeps answering while the
    # worker is busy (the server reads a silent worker as a dead one).
    @app.post("/transcribe")
    def transcribe(file: Annotated[UploadFile, File()]):
        """Single-file endpoint — kept for backward compatibility. The answer carries
        the worker's identity (see identity_of), read after the decode."""
        result = do_transcribe(file.file.read(), file.filename)
        return {**result, "identity": response_identity()}

    @app.post("/transcribe-batch")
    def transcribe_batch(files: Annotated[List[UploadFile], File()], request: Request):
        """Multi-file endpoint: WAV files in one multipart request; each part's
        filename is its result key. Returns {"results": {name: {"text","error"}}}.
        X-Whisper-Request-Timeout-S is honoured like the Mac worker does. The answer
        carries the worker's identity (see identity_of), read after the decodes."""
        results = run_upload_batch(files, abandon_deadline(request))
        return {"results": results, "identity": response_identity()}

    @app.get("/health")
    async def health():
        """Health probe. Every field the Mac worker reports, plus compute_type,
        requested_device, attempted_device, fallback and device_source (additive: the
        server ignores fields it does not know). `device` is the RESOLVED device
        read back off the loaded model, never the requested one, so the server's
        require_gpu gate measures what is running. Everything model-related comes
        from the engine's pin: the first start's resolution, to which every decode
        child, restarts included, is held (a restart that cannot load exactly that
        is a start fault, shown as child_start_error, never a different model)."""
        meta = engine.meta()
        device = meta.get("device", "unknown")
        ident = identity_of(meta)
        compute = meta.get("compute_type", "")
        batched = bool(meta.get("batched", False))
        return await pipeline.health_payload(
            identity=identity,
            model=ident["model"],
            backend=ident["backend"],
            device=device,
            decode_fingerprint=ident["decode_fingerprint"],
            busy=engine.busy,
            start_error=engine.start_error,
            clip_timeout_floor_s=CLIP_TIMEOUT_FLOOR_S,
            clip_timeout_factor=CLIP_TIMEOUT_FACTOR,
            child_ready_timeout_s=CHILD_READY_TIMEOUT_S,
            extra={
                # The shared payload says True; this worker knows whether the
                # batched pipeline actually loaded.
                "batch_pipeline": batched,
                "compute_type": compute,
                # What was ASKED (auto|cpu|cuda), what the first attempt tried, and
                # whether the load fell back, so a GPU that vanished is visible.
                "requested_device": cfg.device,
                "attempted_device": meta.get("attempted_device", device),
                "requested_compute_type": meta.get("attempted_compute_type", compute),
                "fallback": bool(meta.get("fallback", False)),
                "device_source": meta.get("device_source", ""),
                "device_reason": meta.get("device_reason", ""),
                "num_workers": cfg.num_workers,
                # Present (true) ONLY while a decode child runs outside the Windows
                # job object that kills it with this worker: it would then outlive
                # a killed worker if a decode holds the GIL. Absent otherwise.
                **({"child_unprotected": True} if engine.unprotected() else {}),
                # "nt" or "posix": a Windows worker cannot represent every title
                # (path_unrepresentable), so a dispatcher can keep those off it.
                "path_flavor": "nt"
                if getattr(pipeline.pathmod, "__name__", "") == "ntpath"
                else "posix",
                **(
                    {"cpu_threads": cfg.cpu_threads or default_cpu_threads()}
                    if device == "cpu"
                    else {}
                ),
            },
        )

    return app


def main(argv: Optional[List[str]] = None) -> None:
    cfg = parse_config(sys.argv[1:] if argv is None else argv)
    log.info(
        f"Loading model (device={cfg.device}, model={cfg.model or 'by device'}; "
        "the first run downloads the model)..."
    )
    try:
        engine = build_engine(cfg)
    except ChildFaultError as e:
        # Loud and fail-closed: no process means no /health, so the dispatcher
        # refuses this endpoint instead of this worker serving something other than
        # what it was asked to (an explicit cuda that cannot load is never papered
        # over by a CPU run).
        log.error(f"cannot start: {e}")
        raise SystemExit(1)
    app = create_app(cfg, engine)
    import uvicorn

    if cfg.host not in LOOPBACK_HOSTS:
        log.warning(
            f"binding {cfg.host} — this exposes the worker beyond loopback: there is no "
            "authentication, and any host that can reach the port can obtain transcripts "
            "of files under WHISPER_PATH_MAP"
        )
    # log_config=None is required, not cosmetic. uvicorn.run() otherwise applies
    # its own dictConfig with disable_existing_loggers=True, which silences the
    # module logger above -- uvicorn's access lines appear while every
    # log.info/log.error from this file vanishes (an ffmpeg failure on every
    # request once left no trace here: 2,472 requests logged as "200 OK").
    try:
        uvicorn.run(app, host=cfg.host, port=cfg.port, log_level="info", log_config=None)
    finally:
        engine.shutdown()


if __name__ == "__main__":
    main()
