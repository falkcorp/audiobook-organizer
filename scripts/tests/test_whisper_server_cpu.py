# file: scripts/tests/test_whisper_server_cpu.py
# version: 2.0.1
# guid: c3e91a60-2f5d-4b87-a4c2-7d0b6e18f953
# last-edited: 2026-11-17
#
# Tests for scripts/whisper_server.py, the non-Mac Whisper worker: device
# selection (auto | cpu | cuda and the CPU fallback), configuration parsing, the
# /health contract, and the path pipeline and upload endpoints end to end with a
# fake model. No real model is loaded and nothing here needs a GPU: the
# Transcriber is a stub, run through ws.InProcessRunner (the production engine
# decodes in a killable child process; tests/test_whisper_decode_child.py covers
# that with real child processes). Real-ffmpeg tests skip when ffmpeg is not
# installed, unless WHISPER_TESTS_REQUIRE_FFMPEG=1 (CI), where a missing ffmpeg FAILS.

import asyncio
import logging
import math
import os
import pathlib
import shutil
import struct
import sys
import threading
import time
import unittest
import wave

try:
    import pytest
    from fastapi.testclient import TestClient
except ImportError as exc:  # pragma: no cover - dependency-gated
    raise unittest.SkipTest(f"whisper server tests need fastapi+httpx+pytest ({exc})")

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import whisper_machine_id as wmi  # noqa: E402
import whisper_path_pipeline as wpp  # noqa: E402
import whisper_server as ws  # noqa: E402

HAS_FFMPEG = shutil.which("ffmpeg") is not None


def test_ci_has_ffmpeg_when_it_says_it_does():
    # CI sets WHISPER_TESTS_REQUIRE_FFMPEG=1: the real-ffmpeg tests below must RUN
    # there, not skip, or the extraction half of the pipeline is untested while
    # the job is green.
    if os.environ.get("WHISPER_TESTS_REQUIRE_FFMPEG") == "1":
        assert HAS_FFMPEG, "WHISPER_TESTS_REQUIRE_FFMPEG=1 but ffmpeg is not on PATH"


class FakeTranscriber:
    """Stands in for faster-whisper: reports a device, returns heard-<size>."""

    def __init__(self, device="cpu", compute_type="int8", batched=True, source="model"):
        self.device = device
        self.compute_type = compute_type
        self.batched = batched
        self.device_source = source
        self.calls = []
        self.gate = None  # a threading.Event the decode waits on
        self.entered = threading.Event()
        self.boom = None
        self.model = "base.en"

    def transcribe(self, audio):
        self.entered.set()
        if self.gate is not None:
            assert self.gate.wait(10), "the test never released the decode"
        if self.boom:
            raise RuntimeError(self.boom)
        size = (
            pathlib.Path(audio).stat().st_size if isinstance(audio, str) else len(audio.getvalue())
        )
        self.calls.append(audio if isinstance(audio, str) else "<bytes>")
        return f"heard-{size}"


def _sine_wav(path, seconds=3.0, rate=16000):
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        frames = b"".join(
            struct.pack("<h", int(8000 * math.sin(2 * math.pi * 440 * i / rate)))
            for i in range(int(seconds * rate))
        )
        w.writeframes(frames)


def _engine(fake, n=1, meta=None):
    runners = [ws.InProcessRunner(fake) for _ in range(n)]
    if meta:
        for r in runners:
            base = r.meta
            r.meta = lambda base=base: {**base(), **meta}
    return ws.Engine(runners)


def _app(monkeypatch, tmp_path, fake=None, cfg=None, meta=None):
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-cpu-1")
    monkeypatch.setenv(wpp.REQUIRE_MOUNT_ENV, "0")
    share = tmp_path / "share"
    share.mkdir(exist_ok=True)
    monkeypatch.setenv(wpp.PATH_MAP_ENV, f"/lib/Gotham={share}")
    fake = fake or FakeTranscriber()
    cfg = cfg or ws.ServerConfig()
    app = ws.create_app(cfg, _engine(fake, cfg.num_workers, meta))
    return TestClient(app), fake, share, app


# ── configuration ────────────────────────────────────────────────────────────


def test_defaults_are_auto_device_no_model_and_loopback():
    cfg = ws.parse_config([], {})
    assert (cfg.device, cfg.model, cfg.compute_type, cfg.cpu_threads) == (
        "auto",
        None,  # the model follows the resolved device (see resolve_model)
        None,
        None,
    )
    # Loopback, like the Mac worker: the path routes read files and have no auth.
    assert (cfg.num_workers, cfg.batch_size, cfg.port, cfg.host) == (1, 8, 19847, "127.0.0.1")
    assert ws.ServerConfig().host == "127.0.0.1"


def test_the_bind_host_is_opt_in_by_flag_or_environment():
    assert ws.parse_config(["--host", "0.0.0.0"], {}).host == "0.0.0.0"
    assert ws.parse_config([], {"WHISPER_HOST": "192.0.2.7"}).host == "192.0.2.7"
    assert ws.parse_config(["--host", "::1"], {"WHISPER_HOST": "0.0.0.0"}).host == "::1"


def test_environment_variables_set_every_option():
    cfg = ws.parse_config(
        [],
        {
            "WHISPER_DEVICE": "CPU",
            "WHISPER_MODEL": "small.en",
            "WHISPER_COMPUTE_TYPE": "int8_float32",
            "WHISPER_CPU_THREADS": "6",
            "WHISPER_NUM_WORKERS": "2",
            "WHISPER_BATCH_SIZE": "4",
            "WHISPER_HOST": "127.0.0.1",
            "WHISPER_PORT": "9000",
        },
    )
    assert cfg == ws.ServerConfig("small.en", "cpu", "int8_float32", 6, 2, 4, "127.0.0.1", 9000)


def test_a_flag_beats_its_environment_variable():
    cfg = ws.parse_config(
        ["--device", "cuda", "--cpu-threads", "3", "--port", "1"],
        {"WHISPER_DEVICE": "cpu", "WHISPER_CPU_THREADS": "9", "WHISPER_PORT": "2"},
    )
    assert (cfg.device, cfg.cpu_threads, cfg.port) == ("cuda", 3, 1)


def test_the_model_can_still_be_the_first_positional_argument():
    assert ws.parse_config(["small.en"], {}).model == "small.en"
    assert ws.parse_config(["--model", "tiny.en"], {"WHISPER_MODEL": "x"}).model == "tiny.en"


@pytest.mark.parametrize(
    "argv,env",
    [
        (["--device", "gpu"], {}),
        ([], {"WHISPER_DEVICE": "cuda:0"}),
        (["--cpu-threads", "0"], {}),
        (["--num-workers", "-1"], {}),
        (["--port", "x"], {}),
        ([], {"WHISPER_CPU_THREADS": "many"}),
        ([], {"WHISPER_NUM_WORKERS": "0"}),
        (["--device", "cpu", "--compute-type", "float16"], {}),
        (["--device", "cpu"], {"WHISPER_COMPUTE_TYPE": "int8_float16"}),
    ],
)
def test_a_bad_option_is_a_usage_error(argv, env):
    with pytest.raises(SystemExit) as e:
        ws.parse_config(argv, env)
    assert e.value.code == 2


def test_a_gpu_only_compute_type_is_allowed_with_auto_and_replaced_on_cpu(caplog):
    cfg = ws.parse_config(["--compute-type", "float16"], {})  # device auto
    assert ws.effective_compute_type(cfg, "cuda") == "float16"
    with caplog.at_level(logging.WARNING, logger="whisper_server"):
        assert ws.effective_compute_type(cfg, "cpu") == "int8"
    assert "cannot run on the CPU" in caplog.text


@pytest.mark.parametrize(
    "cores,threads", [(1, 1), (2, 1), (4, 2), (6, 4), (8, 6), (12, 8), (32, 8), (64, 8)]
)
def test_default_cpu_threads_leaves_two_cores_and_caps_at_eight(cores, threads):
    assert ws.default_cpu_threads(cores) == threads


# ── device resolution ────────────────────────────────────────────────────────


def _count(n, why=""):
    return lambda: (n, why)


def test_cpu_is_never_probed_for_cuda():
    def forbidden():
        raise AssertionError("an explicit cpu must not probe CUDA")

    assert ws.resolve_device("cpu", forbidden)[0] == "cpu"


def test_cuda_is_taken_at_its_word():
    assert ws.resolve_device("cuda", _count(0))[0] == "cuda"


def test_auto_uses_cuda_only_when_a_device_is_found():
    assert ws.resolve_device("auto", _count(2)) == ("cuda", "auto: 2 CUDA device(s) found")
    assert ws.resolve_device("auto", _count(0)) == ("cpu", "auto: no CUDA device found")


def test_auto_says_why_when_the_probe_itself_failed():
    device, reason = ws.resolve_device("auto", _count(0, "OSError: no libcuda"))
    assert device == "cpu" and "probe failed" in reason and "libcuda" in reason


# ── loading, and the fallback ────────────────────────────────────────────────


def _maker(log, fail_on=()):
    def make(cfg, device, compute):
        log.append((device, compute))
        if device in fail_on:
            raise RuntimeError(f"{device} load boom")
        return FakeTranscriber(device=device, compute_type=compute)

    return make


def test_auto_with_no_gpu_loads_int8_on_cpu_with_one_clear_line(caplog):
    calls = []
    with caplog.at_level(logging.INFO, logger="whisper_server"):
        loaded = ws.load_transcriber(ws.ServerConfig(), _maker(calls), _count(0))
    assert calls == [("cpu", "int8")]
    lines = [r.getMessage() for r in caplog.records if "whisper device" in r.getMessage()]
    assert len(lines) == 1
    assert "cpu" in lines[0] and "int8" in lines[0] and "no CUDA device found" in lines[0]
    assert loaded.attempted_device == "cpu" and loaded.fallback is False


def test_auto_falls_back_to_cpu_when_the_cuda_load_fails(caplog):
    calls = []
    with caplog.at_level(logging.INFO, logger="whisper_server"):
        loaded = ws.load_transcriber(
            ws.parse_config(["--compute-type", "float16"], {}),
            _maker(calls, fail_on=("cuda",)),
            _count(1),
        )
    assert calls == [("cuda", "float16"), ("cpu", "int8")]
    assert loaded.transcriber.device == "cpu" and loaded.transcriber.compute_type == "int8"
    lines = [r.getMessage() for r in caplog.records if "whisper device" in r.getMessage()]
    assert len(lines) == 1, "one line, not one per attempt"
    assert "cuda load boom" in lines[0] and "using cpu" in lines[0]
    # A GPU that was there and failed is a WARNING, not an INFO line (review m7),
    # and the state is machine-readable: attempted cuda, ran cpu, fallback true.
    warned = [
        r for r in caplog.records if r.levelno == logging.WARNING and "FALLBACK" in r.getMessage()
    ]
    assert len(warned) == 1 and "cuda load boom" in warned[0].getMessage()
    assert (loaded.attempted_device, loaded.fallback) == ("cuda", True)
    assert loaded.attempted_compute_type == "float16"


def test_the_model_defaults_by_the_device_that_resolves():
    cuda = ws.load_transcriber(ws.ServerConfig(), _maker([]), _count(1))
    assert cuda.model == "large-v2", "a GPU install has always run large-v2"
    cpu = ws.load_transcriber(ws.ServerConfig(), _maker([]), _count(0))
    assert cpu.model == "base.en"
    named = ws.load_transcriber(ws.ServerConfig(model="small.en"), _maker([]), _count(1))
    assert named.model == "small.en", "an explicit model always wins"


def test_a_fallback_to_cpu_takes_the_cpu_default_model_and_says_so(caplog):
    seen = []

    def make(cfg, device, compute):
        seen.append((cfg.model, device))
        if device == "cuda":
            raise RuntimeError("cuda load boom")
        return FakeTranscriber(device=device, compute_type=compute)

    with caplog.at_level(logging.WARNING, logger="whisper_server"):
        loaded = ws.load_transcriber(ws.ServerConfig(), make, _count(1))
    assert seen == [("large-v2", "cuda"), ("base.en", "cpu")]
    assert loaded.model == "base.en"
    assert "model base.en" in caplog.text and "large-v2" in caplog.text


def test_explicit_cuda_that_cannot_load_is_loud_not_a_silent_cpu():
    calls = []
    with pytest.raises(RuntimeError, match="cuda load boom"):
        ws.load_transcriber(
            ws.parse_config(["--device", "cuda"], {}), _maker(calls, fail_on=("cuda",)), _count(0)
        )
    assert calls == [("cuda", "float16")], "no cpu attempt"


def test_explicit_cpu_that_cannot_load_is_loud():
    with pytest.raises(RuntimeError, match="cpu load boom"):
        ws.load_transcriber(
            ws.parse_config(["--device", "cpu"], {}), _maker([], fail_on=("cpu",)), _count(1)
        )


def test_auto_with_a_gpu_that_loads_stays_on_cuda_with_float16():
    calls = []
    loaded = ws.load_transcriber(ws.ServerConfig(), _maker(calls), _count(1))
    assert calls == [("cuda", "float16")] and loaded.transcriber.device == "cuda"


def test_a_model_that_loads_somewhere_else_is_reported_as_loaded(caplog):
    def make(cfg, device, compute):
        return FakeTranscriber(device="cpu", compute_type="int8", source="model")

    with caplog.at_level(logging.WARNING, logger="whisper_server"):
        loaded = ws.load_transcriber(ws.parse_config(["--device", "cuda"], {}), make, _count(1))
    assert loaded.transcriber.device == "cpu" and loaded.attempted_device == "cuda"
    assert "loaded device=cpu" in caplog.text


# ── the decode fingerprint is journal-key material ───────────────────────────


def test_an_existing_gpu_install_keeps_the_fingerprint_it_had_before_this_change():
    # Literals computed from the 2.x formula for large-v2, float16, batched, batch 8:
    # an existing GPU box (the Windows supervisor passed large-v2) must not move,
    # or every journalled transcript of that worker is orphaned.
    assert ws.decode_fingerprint("large-v2", 8, "float16", True) == "850881e4bf06fae4"


def test_an_unconfigured_gpu_worker_resolves_to_exactly_that_fingerprint():
    # End to end through the resolution path with no model, no compute type and
    # no flags, as an existing install runs it: device auto finding a CUDA device.
    cfg = ws.parse_config([], {})
    loaded = ws.load_transcriber(cfg, _maker([]), _count(1))
    t = loaded.transcriber
    assert (loaded.model, t.device, t.compute_type, t.batched) == (
        "large-v2",
        "cuda",
        "float16",
        True,
    )
    assert ws.decode_fingerprint(loaded.model, cfg.batch_size, t.compute_type, t.batched) == (
        "850881e4bf06fae4"
    )


def test_base_en_float16_is_the_other_fingerprint_the_old_default_had():
    assert ws.decode_fingerprint("base.en", 8, "float16", True) == "9eb83fa070875d52"


def test_a_cpu_int8_worker_has_its_own_fingerprint():
    assert ws.decode_fingerprint("base.en", 8, "int8", True) == "47f40c4def7eeab6"


def test_every_decode_setting_moves_the_fingerprint():
    base = ws.decode_fingerprint("base.en", 8, "int8", True)
    for other in (
        ws.decode_fingerprint("small.en", 8, "int8", True),
        ws.decode_fingerprint("base.en", 4, "int8", True),
        ws.decode_fingerprint("base.en", 8, "float32", True),
        ws.decode_fingerprint("base.en", 8, "int8", False),
    ):
        assert other != base


def test_threads_and_workers_are_not_inputs_to_the_fingerprint():
    # They change speed, not text, so they must not orphan the journal: the
    # function does not even take them.
    import inspect

    params = set(inspect.signature(ws.decode_fingerprint).parameters)
    assert params == {"model", "batch_size", "compute_type", "batched"}


# ── /health ──────────────────────────────────────────────────────────────────

# What the Go side decodes from /health (internal/transcribe/remote.go), plus the
# additive fields this worker reports.
GO_READ_FIELDS = {
    "batch_pipeline",
    "device",
    "compute_type",
    "model",
    "backend",
    "decode_fingerprint",
    "path_pipeline",
    "path_roots",
    "extract_fingerprint",
    "busy_seconds",
    "busy_limit_seconds",
    "clip_timeout_floor_s",
    "clip_timeout_factor",
    "child_ready_timeout_s",
    "extract_timeout_s",
    "machine_id",
}


def test_health_carries_every_field_the_server_reads(monkeypatch, tmp_path):
    client, *_ = _app(monkeypatch, tmp_path)
    body = client.get("/health").json()
    assert set(body) >= GO_READ_FIELDS, GO_READ_FIELDS - set(body)
    assert body["status"] == "ok" and body["backend"] == "faster-whisper"
    assert body["device"] == "cpu" and body["compute_type"] == "int8"
    assert body["path_pipeline"] is True
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": True}]
    assert body["busy_seconds"] == 0 and body["busy_limit_seconds"] is None
    assert "child_start_error" not in body, "absent, not null, when there is none"
    assert body["clip_timeout_floor_s"] == ws.CLIP_TIMEOUT_FLOOR_S
    assert body["clip_timeout_factor"] == ws.CLIP_TIMEOUT_FACTOR


def test_health_reports_the_device_that_loaded_and_the_one_asked_for(monkeypatch, tmp_path):
    fake = FakeTranscriber(device="cpu", compute_type="int8", source="model")
    meta = {
        "attempted_device": "cuda",
        "attempted_compute_type": "float16",
        "device_reason": "auto: CUDA found but the model failed to load on it (boom); using cpu",
        "fallback": True,
    }
    client, *_ = _app(monkeypatch, tmp_path, fake, cfg=ws.ServerConfig(device="auto"), meta=meta)
    body = client.get("/health").json()
    # Consistent after a GPU-to-CPU fallback (review m7 / nit 7): what was asked,
    # what the first attempt tried, what ran, and that it fell back.
    assert body["requested_device"] == "auto" and body["attempted_device"] == "cuda"
    assert body["requested_compute_type"] == "float16" and body["device"] == "cpu"
    assert body["compute_type"] == "int8" and body["fallback"] is True
    assert body["device_source"] == "model" and "using cpu" in body["device_reason"]
    plain, *_ = _app(monkeypatch, tmp_path)
    assert plain.get("/health").json()["fallback"] is False


def test_health_names_the_path_flavor_and_the_model_in_use(monkeypatch, tmp_path):
    client, *_ = _app(monkeypatch, tmp_path)
    body = client.get("/health").json()
    assert body["path_flavor"] == ("nt" if os.name == "nt" else "posix")
    assert body["model"] == "base.en"
    assert body["decode_fingerprint"] == ws.decode_fingerprint("base.en", 8, "int8", True)


def test_health_reports_threads_and_workers_on_cpu_only(monkeypatch, tmp_path):
    cfg = ws.ServerConfig(cpu_threads=5, num_workers=2)
    cpu, *_ = _app(monkeypatch, tmp_path, cfg=cfg)
    body = cpu.get("/health").json()
    assert body["cpu_threads"] == 5 and body["num_workers"] == 2
    gpu, *_ = _app(monkeypatch, tmp_path, FakeTranscriber("cuda", "float16"), cfg=cfg)
    assert "cpu_threads" not in gpu.get("/health").json()


def test_health_reports_whether_the_batched_pipeline_loaded(monkeypatch, tmp_path):
    client, *_ = _app(monkeypatch, tmp_path, FakeTranscriber(batched=False))
    assert client.get("/health").json()["batch_pipeline"] is False


def test_health_machine_id_is_the_hash_of_the_override(monkeypatch, tmp_path):
    import hashlib

    client, *_ = _app(monkeypatch, tmp_path)
    body = client.get("/health").json()
    assert body["machine_id"] == hashlib.sha256(b"override:gotham-cpu-1").hexdigest()[:16]


def test_health_answers_while_a_decode_is_running_and_reports_it_busy(monkeypatch, tmp_path):
    client, fake, share, app = _app(monkeypatch, tmp_path)
    clip = share / "Wayne" / "a.wav"
    clip.parent.mkdir()
    _sine_wav(clip, 1.0)
    fake.gate = threading.Event()
    app.state.pipeline.extract_clip = lambda src, seconds: (str(clip.parent), str(clip))
    result = {}

    def post():
        result["r"] = client.post(
            "/transcribe-path", json={"path": "/lib/Gotham/Wayne/a.wav", "seconds": 5}
        )

    t = threading.Thread(target=post)
    t.start()
    try:
        assert fake.entered.wait(10)
        time.sleep(0.05)
        started = time.monotonic()
        body = client.get("/health").json()
        assert time.monotonic() - started < 2.5, "health must not queue behind the decode"
        assert body["busy_seconds"] > 0
        assert body["busy_limit_seconds"] == ws.CLIP_TIMEOUT_FLOOR_S
    finally:
        fake.gate.set()
        t.join(10)
    assert result["r"].status_code == 200
    assert client.get("/health").json()["busy_seconds"] == 0


def test_the_handlers_are_plain_functions_so_health_stays_responsive(monkeypatch, tmp_path):
    *_, app = _app(monkeypatch, tmp_path)
    handlers = {r.path: r.endpoint for r in app.routes if hasattr(r, "endpoint")}
    for path in ("/transcribe", "/transcribe-batch", "/transcribe-path", "/transcribe-path-batch"):
        assert not asyncio.iscoroutinefunction(handlers[path]), path


# ── concurrency ──────────────────────────────────────────────────────────────


@pytest.mark.parametrize("workers,expected_peak", [(1, 1), (2, 2)])
def test_at_most_num_workers_decodes_run_at_once(workers, expected_peak):
    running = 0
    peak = 0
    lock = threading.Lock()

    class Slow(FakeTranscriber):
        def transcribe(self, audio):
            nonlocal running, peak
            with lock:
                running += 1
                peak = max(peak, running)
            time.sleep(0.05)
            with lock:
                running -= 1
            return "x"

    engine = ws.Engine([ws.InProcessRunner(Slow()) for _ in range(workers)])
    threads = [threading.Thread(target=engine.run, args=("p", 1.0)) for _ in range(6)]
    for t in threads:
        t.start()
    for t in threads:
        t.join(10)
    assert peak == expected_peak


def test_busy_reports_the_most_overdue_decode_and_clears_after_a_failure():
    fake = FakeTranscriber()
    engine = _engine(fake, 2)
    fake.boom = "wedged"
    with pytest.raises(RuntimeError):
        engine.run("p", 1.0)
    assert engine.busy() == (0.0, None), "a failed decode must not stay 'busy'"


def test_the_clip_limit_follows_the_audio_length_with_a_floor():
    assert ws.Engine.clip_limit_s(None) == ws.CLIP_TIMEOUT_FLOOR_S
    assert ws.Engine.clip_limit_s(10) == ws.CLIP_TIMEOUT_FLOOR_S
    assert ws.Engine.clip_limit_s(600) == 600 * ws.CLIP_TIMEOUT_FACTOR


# ── the path pipeline end to end, and the upload endpoints ───────────────────


def test_a_path_job_is_cut_then_transcribed_and_the_clip_is_removed(monkeypatch, tmp_path):
    client, fake, share, app = _app(monkeypatch, tmp_path)
    made = []

    def fake_extract(src, seconds):
        d = tmp_path / f"clip{len(made)}"
        d.mkdir()
        _sine_wav(d / "clip.wav", 1.0)
        made.append((src, seconds, d))
        return str(d), str(d / "clip.wav")

    app.state.pipeline.extract_clip = fake_extract
    (share / "Wayne").mkdir()
    (share / "Wayne" / "Year One.m4b").write_bytes(b"x")
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"b1": {"path": "/lib/Gotham/Wayne/Year One.m4b", "seconds": 45}}},
    ).json()["results"]
    assert res["b1"]["error"] is None and res["b1"]["text"].startswith("heard-")
    assert made[0][1] == 45 and made[0][0].endswith("Year One.m4b")
    assert not made[0][2].exists(), "the clip directory is removed"


def test_a_refused_path_never_reaches_the_model(monkeypatch, tmp_path):
    client, fake, *_ = _app(monkeypatch, tmp_path)
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"k": {"path": "/etc/passwd", "seconds": 5}}},
    ).json()["results"]
    assert res["k"]["error"].startswith("path_unmapped:") and fake.calls == []


def test_a_model_failure_on_a_path_job_is_whisper_error_never_a_decoder_code(monkeypatch, tmp_path):
    client, fake, share, app = _app(monkeypatch, tmp_path)
    clip = share / "c.wav"
    _sine_wav(clip, 1.0)
    app.state.pipeline.extract_clip = lambda src, seconds: (str(share), str(clip))
    fake.boom = "model exploded"
    res = client.post("/transcribe-path", json={"path": "/lib/Gotham/x.m4b", "seconds": 5}).json()
    # NB the stub extractor skips the clip's removal of `share`: only the code matters here.
    assert res["error"].startswith("whisper_error:"), res
    assert not res["error"].startswith(("transcribe_timeout", "decoder_"))


@pytest.mark.skipif(not HAS_FFMPEG, reason="ffmpeg is not installed")
def test_real_ffmpeg_cuts_a_generated_clip_and_the_model_hears_it(monkeypatch, tmp_path):
    client, fake, share, _app_ = _app(monkeypatch, tmp_path)
    (share / "Wayne").mkdir()
    _sine_wav(share / "Wayne" / "Dark Knight.wav", 3.0, rate=44100)
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"k": {"path": "/lib/Gotham/Wayne/Dark Knight.wav", "seconds": 2}}},
    ).json()["results"]["k"]
    assert res["error"] is None, res
    # 2 s of 16 kHz mono 16-bit PCM is 64000 bytes of samples; the WAV header
    # ffmpeg writes varies by version (44 bytes, or more with a LIST chunk). A
    # clip of the whole 3 s source would be 96000+.
    heard = int(res["text"].removeprefix("heard-"))
    assert 64000 + 40 <= heard <= 64000 + 400, heard


@pytest.mark.skipif(not HAS_FFMPEG, reason="ffmpeg is not installed")
def test_real_ffmpeg_on_garbage_is_a_durable_ffmpeg_error_not_a_crash(monkeypatch, tmp_path):
    client, fake, share, _app_ = _app(monkeypatch, tmp_path)
    (share / "bad.m4b").write_bytes(b"this is not audio" * 100)
    res = client.post("/transcribe-path", json={"path": "/lib/Gotham/bad.m4b", "seconds": 5}).json()
    assert res["error"].split(":")[0] in {"ffmpeg_error", "container_rejected"}, res
    assert fake.calls == []


def test_the_single_upload_endpoint_still_works(monkeypatch, tmp_path):
    client, fake, *_ = _app(monkeypatch, tmp_path)
    r = client.post("/transcribe", files={"file": ("a.wav", b"RIFFxxxx", "audio/wav")})
    assert r.json() == {"text": "heard-8", "error": None}


def test_the_batch_upload_endpoint_honours_the_request_deadline_header(monkeypatch, tmp_path):
    # Review m6: like the Mac worker, clips not yet STARTED when the client's
    # ceiling has passed are answered request_abandoned and never decoded.
    client, fake, *_ = _app(monkeypatch, tmp_path)
    r = client.post(
        "/transcribe-batch",
        files=[("files", ("101", b"aa", "audio/wav"))],
        headers={wpp.REQUEST_TIMEOUT_HEADER: "30"},
    ).json()
    assert r["results"]["101"]["error"] is None, "inside the ceiling it is decoded"
    import whisper_path_pipeline as pp

    real_past = pp.past
    monkeypatch.setattr(ws, "past", lambda deadline: deadline is not None)
    r = client.post(
        "/transcribe-batch",
        files=[("files", ("101", b"aa", "audio/wav")), ("files", ("102", b"bb", "audio/wav"))],
        headers={wpp.REQUEST_TIMEOUT_HEADER: "30"},
    ).json()["results"]
    assert all(v["error"].startswith("request_abandoned:") for v in r.values()), r
    assert len(fake.calls) == 1, "an abandoned clip is never decoded"
    assert real_past(None) is False


def test_an_upload_limit_comes_from_its_audio_length_not_a_flat_300s(monkeypatch, tmp_path):
    seen = []
    client, fake, *_ = _app(monkeypatch, tmp_path)
    engine = client.app.state.engine
    real = engine.run
    engine.run = lambda path, audio_s: (seen.append(audio_s), real(path, audio_s))[1]
    wav = tmp_path / "long.wav"
    _sine_wav(wav, 5.0)
    client.post("/transcribe", files={"file": ("a.wav", wav.read_bytes(), "audio/wav")})
    assert seen and abs(seen[0] - 5.0) < 0.01, seen
    # And that length reaches the limit /health will report for it.
    assert ws.Engine.clip_limit_s(seen[0]) == max(
        ws.CLIP_TIMEOUT_FLOOR_S, ws.CLIP_TIMEOUT_FACTOR * seen[0]
    )


def test_the_batch_upload_endpoint_keys_results_by_filename(monkeypatch, tmp_path):
    client, fake, *_ = _app(monkeypatch, tmp_path)
    r = client.post(
        "/transcribe-batch",
        files=[("files", ("101", b"aa", "audio/wav")), ("files", ("102", b"bbbb", "audio/wav"))],
    ).json()
    assert r == {
        "results": {
            "101": {"text": "heard-2", "error": None},
            "102": {"text": "heard-4", "error": None},
        }
    }


def test_an_upload_failure_is_reported_in_the_body_as_it_always_was(monkeypatch, tmp_path):
    client, fake, *_ = _app(monkeypatch, tmp_path)
    fake.boom = "decode failed"
    r = client.post("/transcribe", files={"file": ("a.wav", b"x", "audio/wav")})
    assert r.status_code == 200 and r.json() == {"text": "", "error": "decode failed"}


def test_creating_the_app_starts_the_machine_id_thread_once(monkeypatch, tmp_path):
    ident = wmi.MachineIdentity()
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-cpu-2")
    ws.create_app(ws.ServerConfig(), _engine(FakeTranscriber()), identity=ident)
    assert ident.first is not None
    first = ident.first
    assert first.result(timeout=5) is None and ident.start() is first


def _run_main(monkeypatch, caplog, argv, shutdowns):
    """main() with the engine and uvicorn stubbed: returns what uvicorn was given."""
    import types

    served = {}
    engine = _engine(FakeTranscriber())
    monkeypatch.setattr(ws, "build_engine", lambda cfg: engine)
    monkeypatch.setattr(engine, "shutdown", lambda: shutdowns.append(1))
    monkeypatch.setitem(
        sys.modules,
        "uvicorn",
        types.SimpleNamespace(run=lambda app, **kw: served.update(kw)),
    )
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-cpu-main")
    with caplog.at_level(logging.WARNING, logger=ws.log.name):
        ws.main(argv)
    return served


def test_main_binds_loopback_quietly_by_default(monkeypatch, caplog):
    shutdowns = []
    served = _run_main(monkeypatch, caplog, [], shutdowns)
    assert served["host"] == "127.0.0.1" and shutdowns == [1]
    assert not [r for r in caplog.records if "beyond loopback" in r.getMessage()]


def test_main_warns_when_bound_beyond_loopback(monkeypatch, caplog):
    shutdowns = []
    served = _run_main(monkeypatch, caplog, ["--host", "0.0.0.0"], shutdowns)
    assert served["host"] == "0.0.0.0" and shutdowns == [1]
    warned = [r for r in caplog.records if "beyond loopback" in r.getMessage()]
    assert warned and warned[0].levelno == logging.WARNING
    assert "no authentication" in warned[0].getMessage()
