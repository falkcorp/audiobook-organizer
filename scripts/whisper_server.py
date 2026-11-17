# file: scripts/whisper_server.py
# version: 3.0.0
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
#     scripts/whisper_path_pipeline.py and scripts/whisper_machine_id.py: copy
#     all three files together.
#   * the older WAV UPLOAD endpoints (/transcribe, /transcribe-batch).
#
# Run (uv handles all deps -- no pip install needed):
#   uv run scripts/whisper_server.py                      # auto device, base.en
#   uv run scripts/whisper_server.py --device cpu         # force CPU
#   uv run scripts/whisper_server.py small.en --device cpu --cpu-threads 6
#
# Settings (flag, then environment, then default):
#   --device         WHISPER_DEVICE        auto | cpu | cuda          auto
#   --model          WHISPER_MODEL         faster-whisper model name  base.en
#                    (or the first positional argument)
#   --compute-type   WHISPER_COMPUTE_TYPE  float16 on cuda, int8 on cpu
#   --cpu-threads    WHISPER_CPU_THREADS   logical cores - 2, 1..8
#   --num-workers    WHISPER_NUM_WORKERS   concurrent decodes             1
#   --batch-size     WHISPER_BATCH_SIZE    batched-pipeline batch size    8
#   --host           WHISPER_HOST          0.0.0.0
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
# This worker decodes IN-PROCESS: it cannot kill a decode that hangs, unlike the
# Mac worker, whose inference runs in a child process it can kill. It therefore
# never answers transcribe_timeout or decoder_crashed. It does report, on
# /health, how long the current clip has been running and its generous limit
# (busy_seconds / busy_limit_seconds), so the server's stuck detection still
# refuses a worker that has wedged.
#
# Windows: see docs/whisper-remote-extract.md. Needs ffmpeg on PATH for the path
# pipeline. WHISPER_PATH_MAP local sides are drive paths or UNC shares.

import argparse
import dataclasses
import hashlib
import io
import json
import logging
import os
import sys
import threading
import time
from typing import Annotated, Any, Callable, List, Optional, Protocol

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("whisper_server")

try:
    from fastapi import FastAPI, File, UploadFile
except ImportError as e:  # pragma: no cover - dependency probe
    print(f"Missing dependency: {e}")
    print('Install with: pip install "faster-whisper>=1.0.0" fastapi "uvicorn[standard]"')
    sys.exit(1)

# The shared worker modules live next to this file: put that directory first on
# sys.path so they import however this file is started (uv run, the Windows
# supervisor, a test that loads it by path).
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from whisper_machine_id import MachineIdentity  # noqa: E402
from whisper_path_pipeline import (  # noqa: E402
    PathPipeline,
    audio_seconds,
    install_path_routes,
)

DEVICES = ("auto", "cpu", "cuda")
DEFAULT_MODEL = "base.en"
DEFAULT_PORT = 19847
DEFAULT_BATCH_SIZE = 8
BEAM_SIZE = 5
LANGUAGE = "en"
# ctranslate2 compute types that exist on each device. float16 is not
# implemented on CPU, and int8_float16 needs a GPU.
CPU_COMPUTE_TYPES = ("int8", "int8_float32", "float32", "int16")
CUDA_ONLY_COMPUTE_TYPES = ("float16", "int8_float16", "bfloat16")

# The in-process worker cannot kill a clip, so these are NOT deadlines it
# enforces: they are what /health reports so the server can size its own HTTP
# ceiling (max(floor, factor x audio seconds) per clip) and spot a wedged
# worker. Generous on purpose: a slow CPU must not look stuck.
CLIP_TIMEOUT_FLOOR_S = 300.0
CLIP_TIMEOUT_FACTOR = 3.0
# The model is loaded before the server listens, so a request never waits on a
# decoder start; reported as a token second, not zero (zero reads as "predates
# the field" and gets the server's 300 s default allowance added).
CHILD_READY_TIMEOUT_S = 1.0

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
    model: str = DEFAULT_MODEL
    device: str = "auto"  # what was ASKED: auto | cpu | cuda
    compute_type: Optional[str] = None  # None = the device's default
    cpu_threads: Optional[int] = None  # None = default_cpu_threads()
    num_workers: int = 1
    batch_size: int = DEFAULT_BATCH_SIZE
    host: str = "0.0.0.0"
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
        help="decodes that may run at once (default: 1)",
    )
    parser.add_argument(
        "--batch-size",
        type=_positive_int,
        default=from_env("WHISPER_BATCH_SIZE", DEFAULT_BATCH_SIZE),
    )
    parser.add_argument("--host", default=from_env("WHISPER_HOST", "0.0.0.0"))
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
    model = args.model or args.model_pos or env.get("WHISPER_MODEL", "").strip() or DEFAULT_MODEL
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
    return "cpu", "auto: " + (f"CUDA probe failed ({why})" if why else "no CUDA device found")


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


class Transcriber(Protocol):
    """What the server needs from a speech model. `audio` is a path to a WAV
    file or a BytesIO of WAV bytes."""

    device: str  # what actually loaded, not what was asked for
    compute_type: str
    batched: bool
    device_source: str  # "model" if read back off the loaded model, else "requested"

    def transcribe(self, audio: "str | io.BytesIO") -> str: ...


class FasterWhisperTranscriber:
    """faster-whisper (ctranslate2), imported only when one is built so the
    rest of this module loads and tests without it."""

    def __init__(self, cfg: ServerConfig, device: str, compute_type: str) -> None:
        import faster_whisper

        kwargs: dict = {"device": device, "compute_type": compute_type}
        if device == "cpu":
            kwargs["cpu_threads"] = cfg.cpu_threads or default_cpu_threads()
            kwargs["num_workers"] = cfg.num_workers
        else:
            kwargs["num_workers"] = cfg.num_workers
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

    def transcribe(self, audio: "str | io.BytesIO") -> str:
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
    requested_device: str
    requested_compute_type: str
    device_reason: str


def load_transcriber(
    cfg: ServerConfig,
    make: Callable[[ServerConfig, str, str], Transcriber] = FasterWhisperTranscriber,
    count_cuda: Callable[[], "tuple[int, str]"] = cuda_device_count,
) -> LoadedModel:
    """Resolve the device, load the model on it, and log ONE line saying what
    is running and why.

    --device auto that finds a CUDA device but cannot load the model on it (a
    driver or CUDA runtime problem) falls back to the CPU, once, with the reason
    in that line. An explicit --device cuda that cannot load is NOT papered over:
    it raises, so a worker an operator asked to be a GPU worker never
    quietly serves from the CPU."""
    device, reason = resolve_device(cfg.device, count_cuda)
    compute = effective_compute_type(cfg, device)
    requested_compute = compute
    try:
        transcriber = make(cfg, device, compute)
    except Exception as exc:
        if not (cfg.device == "auto" and device == "cuda"):
            raise
        reason = f"auto: CUDA found but the model failed to load on it ({exc}); using cpu"
        device = "cpu"
        compute = effective_compute_type(dataclasses.replace(cfg, compute_type=None), "cpu")
        transcriber = make(cfg, device, compute)
    log.info(
        "whisper device: %s compute=%s model=%s threads=%s workers=%s (%s)",
        transcriber.device,
        transcriber.compute_type,
        cfg.model,
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
    return LoadedModel(transcriber, device, requested_compute, reason)


# DECODE_FINGERPRINT is a short hash of every setting that changes the text this
# server returns for the same WAV bytes. The server folds it into the whisper
# result-journal key, so retuning any of these (a VAD threshold, the model, the
# compute type, the decode path) makes previously journalled transcripts
# unreachable instead of serving them for the new configuration. The settings
# and their order are unchanged from 2.x, so a GPU float16 worker keeps its
# fingerprint; a CPU worker's differs by compute type, as it should.
def decode_fingerprint(cfg: ServerConfig, compute_type: str, batched: bool) -> str:
    settings = {
        "backend": "faster-whisper",
        "model": cfg.model,
        "compute_type": compute_type,
        # The batched pipeline and the beam-search fallback are different decoders.
        "path": "batched" if batched else "beam",
        "batch_size": cfg.batch_size if batched else None,
        "beam_size": BEAM_SIZE if not batched else None,
        "language": LANGUAGE,
        "vad_filter": True,
        "vad_parameters": VAD_PARAMS,
    }
    return hashlib.sha256(json.dumps(settings, sort_keys=True).encode()).hexdigest()[:16]


# ── running a decode ─────────────────────────────────────────────────────────


class Engine:
    """Runs decodes for the HTTP handlers: at most `num_workers` at once, and a
    record of what is running so /health can report busy_seconds."""

    def __init__(self, transcriber: Transcriber, num_workers: int) -> None:
        self.transcriber = transcriber
        self._slots = threading.BoundedSemaphore(num_workers)
        self._lock = threading.Lock()
        self._active: dict[int, tuple[float, float]] = {}
        self._next = 0

    @staticmethod
    def clip_limit_s(audio_s: Optional[float]) -> float:
        return max(CLIP_TIMEOUT_FLOOR_S, CLIP_TIMEOUT_FACTOR * (audio_s or 0.0))

    def run(self, audio: "str | io.BytesIO", audio_s: Optional[float]) -> str:
        limit = self.clip_limit_s(audio_s)
        with self._slots:
            with self._lock:
                token = self._next
                self._next += 1
                self._active[token] = (time.monotonic(), limit)
            try:
                return self.transcriber.transcribe(audio)
            finally:
                with self._lock:
                    self._active.pop(token, None)

    def busy(self) -> "tuple[float, Optional[float]]":
        """(seconds the most overdue running decode has run, its limit), or
        (0, None) when idle. Read without holding a decode slot, so a wedged
        decode cannot hide itself."""
        with self._lock:
            running = list(self._active.values())
        if not running:
            return 0.0, None
        now = time.monotonic()
        started, limit = max(running, key=lambda r: (now - r[0]) - r[1])
        return now - started, limit


# ── the app ──────────────────────────────────────────────────────────────────


def create_app(
    cfg: ServerConfig,
    loaded: LoadedModel,
    pipeline: Optional[PathPipeline] = None,
    identity: Optional[MachineIdentity] = None,
) -> FastAPI:
    app = FastAPI()
    pipeline = pipeline or PathPipeline()
    identity = identity or MachineIdentity()
    identity.start()
    transcriber = loaded.transcriber
    engine = Engine(transcriber, cfg.num_workers)
    fingerprint = decode_fingerprint(cfg, transcriber.compute_type, transcriber.batched)
    app.state.engine = engine
    app.state.pipeline = pipeline
    app.state.identity = identity

    def transcribe_clip(wav_path: str) -> str:
        return engine.run(wav_path, audio_seconds(wav_path))

    def run_path_jobs(jobs: dict, deadline: Optional[float] = None) -> dict:
        return pipeline.run_path_jobs(jobs, deadline, transcribe_clip)

    install_path_routes(app, run_path_jobs)

    def do_transcribe(data: bytes, filename: str) -> dict:
        try:
            text = engine.run(io.BytesIO(data), None)
            log.info(f"transcribed {filename}: {len(text)} chars")
            return {"text": text, "error": None}
        except Exception as e:
            log.error(f"transcription failed for {filename}: {e}")
            return {"text": "", "error": str(e)}

    # Plain `def`, not `async def`: FastAPI runs these in its threadpool, so a
    # decode does not freeze the event loop and /health keeps answering while the
    # worker is busy (the server reads a silent worker as a dead one).
    @app.post("/transcribe")
    def transcribe(file: Annotated[UploadFile, File()]):
        """Single-file endpoint — kept for backward compatibility."""
        return do_transcribe(file.file.read(), file.filename)

    @app.post("/transcribe-batch")
    def transcribe_batch(files: Annotated[List[UploadFile], File()]):
        """Multi-file endpoint: WAV files in one multipart request; each part's
        filename is its result key. Returns {"results": {name: {"text","error"}}}."""
        return {"results": {f.filename: do_transcribe(f.file.read(), f.filename) for f in files}}

    @app.get("/health")
    async def health():
        """Health probe. Every field the Mac worker reports, plus compute_type,
        requested_device/requested_compute_type and device_source (additive: the
        server ignores fields it does not know). `device` is the RESOLVED device
        read back off the loaded model, never the requested one, so the server's
        require_gpu gate measures what is running."""
        return await pipeline.health_payload(
            identity=identity,
            model=cfg.model,
            backend="faster-whisper",
            device=transcriber.device,
            decode_fingerprint=fingerprint,
            busy=engine.busy,
            start_error=lambda: None,
            clip_timeout_floor_s=CLIP_TIMEOUT_FLOOR_S,
            clip_timeout_factor=CLIP_TIMEOUT_FACTOR,
            child_ready_timeout_s=CHILD_READY_TIMEOUT_S,
            extra={
                # The shared payload says True; this worker knows whether the
                # batched pipeline actually loaded.
                "batch_pipeline": transcriber.batched,
                "compute_type": transcriber.compute_type,
                "requested_device": loaded.requested_device,
                "requested_compute_type": loaded.requested_compute_type,
                "device_source": transcriber.device_source,
                "device_reason": loaded.device_reason,
                "num_workers": cfg.num_workers,
                **(
                    {"cpu_threads": cfg.cpu_threads or default_cpu_threads()}
                    if transcriber.device == "cpu"
                    else {}
                ),
            },
        )

    return app


def main(argv: Optional[List[str]] = None) -> None:
    cfg = parse_config(sys.argv[1:] if argv is None else argv)
    log.info(f"Loading {cfg.model} (device={cfg.device}; the first run downloads the model)...")
    loaded = load_transcriber(cfg)
    app = create_app(cfg, loaded)
    import uvicorn

    # log_config=None is required, not cosmetic. uvicorn.run() otherwise applies
    # its own dictConfig with disable_existing_loggers=True, which silences the
    # module logger above -- uvicorn's access lines appear while every
    # log.info/log.error from this file vanishes (an ffmpeg failure on every
    # request once left no trace here: 2,472 requests logged as "200 OK").
    uvicorn.run(app, host=cfg.host, port=cfg.port, log_level="info", log_config=None)


if __name__ == "__main__":
    main()
