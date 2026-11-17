# file: scripts/tests/test_whisper_mlx_pins.py
# version: 1.3.0
# guid: 0e5b7d3a-4c19-4f82-b6a0-91d3c7e5f284
# last-edited: 2026-11-17
#
# Characterization pins for scripts/whisper_mlx_server.py.
#
# These were written and run against the Mac worker BEFORE its path pipeline
# was extracted into the modules it shares with scripts/whisper_server.py, and
# they stayed green, unchanged, across that extraction: that was the evidence that
# the extraction did not change what the Mac worker answers. Since then they have
# been edited on purpose in exactly three places, each commented where it sits:
# the ffmpeg argv gained -protocol_whitelist (round 2), /health's path_map_error
# stopped quoting entries (round 2), and the error text a job answer carries became
# a code plus one word from a closed set per code (rounds 5 and 6, below).
#
# They drive ONLY seams that survive a refactor: HTTP, environment variables,
# the PATH lookup of ffmpeg, and WHISPER_BACKEND_FACTORY (a real inference
# child process running tests/fake_whisper_backend.py). They never patch a
# function inside the module, so a refactor cannot make them pass vacuously.
#
# Rounds 5 and 6 (R4-m3, R5-m2). The error text a job answer carries is now a stable
# code and ONE word from a closed set for that code ("path_unmapped: root_unreadable",
# "ffmpeg_error: invalid_data"), or one fixed sentence for a code with a single reason,
# never what the failure said: the worker has no authentication, and an exception, an
# ffmpeg stderr line or a mapped path can each name a profile directory or a host. The
# codes are unchanged (the Go client matches on nothing else), the texts are pinned
# literally below so reading one here shows what a caller sees, and /health's "ffmpeg"
# is whether it was found, not where. The detail that used to be in the answer is in
# the worker log.
#
# Literal values below (the two fingerprints, the ffmpeg argv) are pinned on
# purpose: both fingerprints are folded into the server's result-journal keys,
# so a changed hex silently orphans every journalled transcript.

import importlib.util
import json
import os
import pathlib
import stat
import sys
import threading
import time
import unittest

try:
    import pytest
    from fastapi.testclient import TestClient
except ImportError as exc:  # pragma: no cover - dependency-gated
    raise unittest.SkipTest(f"whisper pin tests need fastapi+httpx+pytest ({exc})")

_SCRIPTS = pathlib.Path(__file__).resolve().parents[1]
_SERVER = _SCRIPTS / "whisper_mlx_server.py"

# What a caller is told, per code and category (whisper_path_pipeline.CODE_CATEGORIES
# and PUBLIC_SENTENCES, pinned here as literals on purpose: changing a word is a
# deliberate edit of this file).
UNMAPPED_NOT_ABSOLUTE = "path_unmapped: not_absolute"
UNMAPPED_DOT_DOT = "path_unmapped: dot_dot"
UNMAPPED_NOT_UNDER_PREFIX = "path_unmapped: not_under_prefix"
UNMAPPED_ROOT_UNREADABLE = "path_unmapped: root_unreadable"
UNMAPPED_SYMLINK_ESCAPE = "path_unmapped: symlink_escape"
UNMAPPED_MAP_UNSET = "path_unmapped: path_map_unset"
BAD_REQUEST_JOB = "bad_request: job_not_object"
BAD_REQUEST_PATH = "bad_request: path_invalid"
BAD_REQUEST_SECONDS = "bad_request: seconds_invalid"
NOT_FOUND = "not_found: the file was not found on this worker"
FFMPEG_ERROR_INVALID_DATA = "ffmpeg_error: invalid_data"
FFMPEG_ERROR_EMPTY_CLIP = "ffmpeg_error: empty_clip"
FFMPEG_TIMEOUT_IO_FAULT = "ffmpeg_timeout: io_fault"
CONTAINER = "container_rejected: this worker does not accept the file's container"
WHISPER_ERROR = "whisper_error: transcription failed on this worker"
FFMPEG_MISSING = "worker_misconfigured: ffmpeg_missing"

EXTRACT_FINGERPRINT = "c797688b945fe1e6"
DECODE_FINGERPRINT_DEFAULT_MODEL = "884d4a3eb40a7486"
# sha256("override:gotham-gpu-1")[:16]
MACHINE_ID_FOR_OVERRIDE = "ed1652312bf434ca"

EXPECTED_HEALTH_KEYS = {
    "backend",
    "batch_pipeline",
    "busy_limit_seconds",
    "busy_seconds",
    "child_ready_timeout_s",
    "clip_timeout_factor",
    "clip_timeout_floor_s",
    "decode_fingerprint",
    "device",
    "extract_fingerprint",
    "extract_timeout_s",
    "ffmpeg",
    "machine_id",
    "model",
    "path_pipeline",
    "path_prefixes",
    "path_roots",
    "status",
}

FFMPEG_ARGV_TEMPLATE = [
    "-nostdin",
    "-y",
    "-v",
    "error",
    "-format_whitelist",
    "mov,mp4,m4a,3gp,3g2,mj2,mp3,flac,ogg,wav,aac,matroska,webm,asf",
    "-ss",
    "0",
    "-t",
    "{seconds}",
    # Added in whisper-cpu round 2 (defence in depth: local files only). The one
    # deliberate change to this pin; EXTRACT_FINGERPRINT is unchanged.
    "-protocol_whitelist",
    "file",
    "-i",
    "{src}",
    "-vn",
    "-ac",
    "1",
    "-ar",
    "16000",
    "-f",
    "wav",
    "{wav}",
]


def _load():
    spec = importlib.util.spec_from_file_location("whisper_mlx_server", _SERVER)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["whisper_mlx_server"] = mod
    spec.loader.exec_module(mod)
    return mod


_FAKE_FFMPEG = """#!{python}
import json, os, sys, wave
argv = sys.argv[1:]
with open(os.environ["FAKE_FFMPEG_LOG"], "a") as f:
    f.write(json.dumps(argv) + "\\n")
mode = os.environ.get("FAKE_FFMPEG_MODE", "ok")
if mode == "fail":
    sys.stderr.write(os.environ.get("FAKE_FFMPEG_STDERR", "boom") + "\\n")
    sys.exit(1)
if mode == "empty":
    open(argv[-1], "wb").close()
    sys.exit(0)
with wave.open(argv[-1], "wb") as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(16000)
    w.writeframes(b"\\x00\\x00" * 16000)
"""


@pytest.fixture
def pin(monkeypatch, tmp_path):
    """The Mac worker with a fake ffmpeg on PATH and the fake inference backend
    in a real child process. Returns (client, share, ffmpeg_log)."""
    bindir = tmp_path / "bin"
    bindir.mkdir()
    ffmpeg = bindir / "ffmpeg"
    ffmpeg.write_text(_FAKE_FFMPEG.format(python=sys.executable))
    ffmpeg.chmod(ffmpeg.stat().st_mode | stat.S_IXUSR)
    log = tmp_path / "ffmpeg.log"
    monkeypatch.setenv("FAKE_FFMPEG_LOG", str(log))
    monkeypatch.setenv("PATH", f"{bindir}{os.pathsep}{os.environ['PATH']}")
    monkeypatch.setenv("PYTHONPATH", str(pathlib.Path(__file__).resolve().parent))
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "fake_whisper_backend:factory")
    monkeypatch.setenv("WHISPER_PATH_REQUIRE_MOUNT", "0")
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-gpu-1")
    monkeypatch.delenv("WHISPER_MLX_MODEL", raising=False)
    share = tmp_path / "share"
    (share / "Arkham" / "Book").mkdir(parents=True)
    (share / "Arkham" / "Book" / "01.m4b").write_bytes(b"audio")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py"])
    mod = _load()
    try:
        yield TestClient(mod.app), share, log
    finally:
        mod._inference.shutdown()


def _post(client, jobs, **kw):
    return client.post("/transcribe-path-batch", json={"jobs": jobs}, **kw)


def _ffmpeg_calls(log):
    if not log.exists():
        return []
    return [json.loads(line) for line in log.read_text().splitlines()]


# ── identity of the journal keys ─────────────────────────────────────────────


def test_fingerprints_are_pinned(pin):
    client, _, _ = pin
    body = client.get("/health").json()
    assert body["extract_fingerprint"] == EXTRACT_FINGERPRINT
    assert body["decode_fingerprint"] == DECODE_FINGERPRINT_DEFAULT_MODEL


def test_decode_fingerprint_follows_the_model(pin, monkeypatch):
    monkeypatch.setenv("WHISPER_MLX_MODEL", "mlx-community/whisper-tiny-mlx")
    other = _load()
    try:
        fp = TestClient(other.app).get("/health").json()["decode_fingerprint"]
    finally:
        other._inference.shutdown()
    assert fp != DECODE_FINGERPRINT_DEFAULT_MODEL


def test_a_local_model_directory_is_a_label_on_health_and_a_hub_id_is_not(
    pin, monkeypatch, tmp_path
):
    import hashlib

    local = tmp_path / "models" / "whisper-batcave"
    local.mkdir(parents=True)
    seen = {}
    for raw in (str(local), "mlx-community/whisper-tiny-mlx"):
        monkeypatch.setenv("WHISPER_MLX_MODEL", raw)
        other = _load()
        try:
            seen[raw] = TestClient(other.app).get("/health").json()
            fingerprint = other.DECODE_FINGERPRINT
        finally:
            other._inference.shutdown()
        # the fingerprint is of the raw value in both cases
        assert seen[raw]["decode_fingerprint"] == fingerprint
    want = "local:" + hashlib.sha256(str(local).encode()).hexdigest()[:16]
    assert seen[str(local)]["model"] == want
    assert str(tmp_path) not in json.dumps(seen[str(local)])
    assert seen["mlx-community/whisper-tiny-mlx"]["model"] == "mlx-community/whisper-tiny-mlx"


# ── /health ──────────────────────────────────────────────────────────────────


def _health_with_machine_id(client):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        body = client.get("/health").json()
        if "machine_id" in body:
            return body
        time.sleep(0.02)
    raise AssertionError("machine_id never appeared")


def test_health_has_exactly_these_keys_and_values(pin):
    client, share, _ = pin
    body = _health_with_machine_id(client)
    assert set(body) == EXPECTED_HEALTH_KEYS
    assert body["status"] == "ok"
    assert body["batch_pipeline"] is True
    assert body["device"] == "metal"
    assert body["backend"] == "mlx"
    assert body["model"] == "mlx-community/whisper-small.en-mlx"
    assert body["machine_id"] == MACHINE_ID_FOR_OVERRIDE
    assert body["busy_seconds"] == 0.0 and body["busy_limit_seconds"] is None
    assert body["clip_timeout_floor_s"] == 60.0
    assert body["clip_timeout_factor"] == 4.0
    assert body["child_ready_timeout_s"] == 300.0
    assert body["extract_timeout_s"] == 120
    assert body["ffmpeg"] is True, "whether it was found, never where"
    assert body["path_pipeline"] is True
    assert body["path_prefixes"] == ["/lib/Gotham"]
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": True}]


def test_health_without_a_path_map(pin, monkeypatch):
    client, _, _ = pin
    monkeypatch.delenv("WHISPER_PATH_MAP")
    body = client.get("/health").json()
    assert body["path_pipeline"] is False
    assert body["path_prefixes"] == [] and body["path_roots"] == []
    assert "path_map_error" not in body


def test_health_reports_an_invalid_map(pin, monkeypatch):
    client, _, _ = pin
    monkeypatch.setenv("WHISPER_PATH_MAP", "/lib/Gotham")
    body = client.get("/health").json()
    assert body["path_pipeline"] is False
    assert body["path_map_error"] == (
        # Deliberate change (whisper-cpu round 2): the entry is named by number, not
        # quoted, because /health shows this to any caller and a local side can name
        # a host and share.
        "WHISPER_PATH_MAP invalid: entry 1 is not server-path=local-path"
    )


def test_health_marks_a_missing_root_unreadable(pin, monkeypatch, tmp_path):
    client, _, _ = pin
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={tmp_path / 'gone'}")
    body = client.get("/health").json()
    assert body["path_pipeline"] is True
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": False}]


def test_health_lists_the_longest_prefix_first(pin, monkeypatch, tmp_path):
    client, share, _ = pin
    other = tmp_path / "other"
    other.mkdir()
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib={other},/lib/Gotham/Annex={share}")
    body = client.get("/health").json()
    assert body["path_prefixes"] == ["/lib/Gotham/Annex", "/lib"]


def test_a_child_process_starts_no_machine_id_thread(monkeypatch):
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-gpu-1")
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py", "--inference-child", "3", "4"])
    # Only threads this import starts count: another test's server module may
    # still have a machine-id thread of its own alive.
    before = set(threading.enumerate())
    _load()
    started = set(threading.enumerate()) - before
    assert "machine-id" not in {t.name for t in started}


def test_a_server_process_starts_the_machine_id_thread(pin):
    client, _, _ = pin
    # Started at import by the server process; /health then carries the id.
    assert _health_with_machine_id(client)["machine_id"] == MACHINE_ID_FOR_OVERRIDE


# ── extraction: the exact ffmpeg invocation ──────────────────────────────────


def test_the_ffmpeg_argv_is_pinned(pin):
    client, share, log = pin
    r = _post(client, {"k": {"path": "/lib/Gotham/Arkham/Book/01.m4b", "seconds": 45}})
    assert r.status_code == 200
    calls = _ffmpeg_calls(log)
    assert len(calls) == 1
    argv = calls[0]
    src = os.path.realpath(share / "Arkham" / "Book" / "01.m4b")
    wav = argv[-1]
    assert os.path.basename(wav) == "clip.wav"
    assert os.path.basename(os.path.dirname(wav)).startswith("whisper-clip-")
    expected = [a.format(seconds=45, src=src, wav=wav) for a in FFMPEG_ARGV_TEMPLATE]
    assert argv == expected


def test_a_successful_job_returns_the_backend_text(pin):
    client, _, _ = pin
    r = _post(client, {"k": {"path": "/lib/Gotham/Arkham/Book/01.m4b", "seconds": 5}})
    res = r.json()["results"]
    assert set(res) == {"k"}
    assert res["k"]["error"] is None
    assert res["k"]["text"].startswith("heard-")


def test_the_default_clip_length_is_90_seconds(pin):
    client, _, log = pin
    _post(client, {"k": {"path": "/lib/Gotham/Arkham/Book/01.m4b"}})
    argv = _ffmpeg_calls(log)[0]
    assert argv[argv.index("-t") + 1] == "90"


def test_the_single_path_endpoint(pin):
    client, _, _ = pin
    ok = client.post("/transcribe-path", json={"path": "/lib/Gotham/Arkham/Book/01.m4b"}).json()
    assert ok["error"] is None and ok["text"].startswith("heard-")
    bad = client.post("/transcribe-path", json={"path": "/elsewhere/x.m4b"}).json()
    assert bad == {"text": "", "error": UNMAPPED_NOT_UNDER_PREFIX}


# ── per-job request validation: the exact error text per input ───────────────

GOOD = "/lib/Gotham/Arkham/Book/01.m4b"

REFUSALS = [
    ("not-a-dict", "oops", BAD_REQUEST_JOB),
    ("no-path", {"seconds": 5}, BAD_REQUEST_PATH),
    ("empty-path", {"path": ""}, BAD_REQUEST_PATH),
    ("int-path", {"path": 7}, BAD_REQUEST_PATH),
    ("nul", {"path": GOOD + "\x00"}, BAD_REQUEST_PATH),
    ("relative", {"path": "lib/Gotham/x"}, UNMAPPED_NOT_ABSOLUTE),
    ("dotdot", {"path": "/lib/Gotham/../../etc/passwd"}, UNMAPPED_DOT_DOT),
    (
        "dotdot-inside",
        {"path": "/lib/Gotham/Arkham/../Arkham/Book/01.m4b"},
        UNMAPPED_DOT_DOT,
    ),
    ("unmapped", {"path": "/etc/passwd"}, UNMAPPED_NOT_UNDER_PREFIX),
    (
        "component-lookalike",
        {"path": "/lib/GothamX/Arkham/Book/01.m4b"},
        UNMAPPED_NOT_UNDER_PREFIX,
    ),
    ("seconds-zero", {"path": GOOD, "seconds": 0}, BAD_REQUEST_SECONDS),
    ("seconds-big", {"path": GOOD, "seconds": 601}, BAD_REQUEST_SECONDS),
    ("seconds-bool", {"path": GOOD, "seconds": True}, BAD_REQUEST_SECONDS),
    ("seconds-str", {"path": GOOD, "seconds": "90"}, BAD_REQUEST_SECONDS),
    ("seconds-float", {"path": GOOD, "seconds": 1.5}, BAD_REQUEST_SECONDS),
    (
        "missing",
        {"path": "/lib/Gotham/Arkham/Book/02.m4b"},
        NOT_FOUND,
    ),
    (
        "a-directory",
        {"path": "/lib/Gotham/Arkham/Book"},
        NOT_FOUND,
    ),
]


@pytest.mark.parametrize(("name", "job", "expected"), REFUSALS, ids=[r[0] for r in REFUSALS])
def test_refusals_are_pinned(pin, name, job, expected):
    client, _, log = pin
    res = _post(client, {name: job}).json()["results"][name]
    assert res["text"] == ""
    assert res["error"] == expected
    if not expected.startswith("not_found"):
        assert _ffmpeg_calls(log) == [], "a refused job must never reach ffmpeg"


@pytest.mark.parametrize("seconds", [1, 600])
def test_the_clip_length_bounds_are_inclusive(pin, seconds):
    client, _, _ = pin
    res = _post(client, {"k": {"path": GOOD, "seconds": seconds}}).json()["results"]["k"]
    assert res["error"] is None


def test_a_symlink_out_of_the_share_is_refused(pin, tmp_path):
    client, share, log = pin
    secret = tmp_path / "secret"
    secret.mkdir()
    (secret / "stolen.m4b").write_bytes(b"x")
    (share / "Arkham" / "leak").symlink_to(secret)
    (share / "Arkham" / "direct.m4b").symlink_to(secret / "stolen.m4b")
    res = _post(
        client,
        {
            "dir": {"path": "/lib/Gotham/Arkham/leak/stolen.m4b"},
            "file": {"path": "/lib/Gotham/Arkham/direct.m4b"},
        },
    ).json()["results"]
    for key in ("dir", "file"):
        assert res[key]["error"] == UNMAPPED_SYMLINK_ESCAPE
    assert _ffmpeg_calls(log) == []


def test_a_symlink_inside_the_share_is_followed(pin):
    client, share, log = pin
    (share / "Arkham" / "alias.m4b").symlink_to(share / "Arkham" / "Book" / "01.m4b")
    res = _post(client, {"k": {"path": "/lib/Gotham/Arkham/alias.m4b"}}).json()["results"]["k"]
    assert res["error"] is None
    # ffmpeg is handed the resolved file, not the link.
    assert _ffmpeg_calls(log)[0][_ffmpeg_calls(log)[0].index("-i") + 1] == os.path.realpath(
        share / "Arkham" / "Book" / "01.m4b"
    )


def test_a_lost_root_is_path_unmapped(pin, monkeypatch, tmp_path):
    client, _, log = pin
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={tmp_path / 'gone'}")
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res["error"] == UNMAPPED_ROOT_UNREADABLE
    assert _ffmpeg_calls(log) == []


def test_a_root_that_is_not_a_mount_is_unreadable_by_default(pin, monkeypatch):
    client, _, _ = pin
    monkeypatch.delenv("WHISPER_PATH_REQUIRE_MOUNT")
    body = client.get("/health").json()
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": False}]
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res["error"] == UNMAPPED_ROOT_UNREADABLE


def test_no_map_refuses_every_job(pin, monkeypatch):
    client, _, _ = pin
    monkeypatch.delenv("WHISPER_PATH_MAP")
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res["error"] == UNMAPPED_MAP_UNSET


# ── the request envelope ─────────────────────────────────────────────────────


def test_keys_are_echoed_verbatim_and_one_bad_job_does_not_fail_the_batch(pin):
    client, _, _ = pin
    jobs = {
        "  odd key / with ünïcode  ": {"path": GOOD, "seconds": 3},
        "bad": {"path": "/etc/passwd"},
        "7": {"path": GOOD, "seconds": 3},
    }
    res = _post(client, jobs).json()["results"]
    assert set(res) == set(jobs)
    assert res["  odd key / with ünïcode  "]["error"] is None
    assert res["7"]["error"] is None
    assert res["bad"]["error"].startswith("path_unmapped:")


def test_an_empty_batch_is_an_empty_result(pin):
    client, _, _ = pin
    assert _post(client, {}).json() == {"results": {}}


@pytest.mark.parametrize("body", [{"jobs": []}, {"jobs": "x"}, {}, {"jobs": None}])
def test_jobs_must_be_an_object(pin, body):
    client, _, _ = pin
    r = client.post("/transcribe-path-batch", json=body)
    assert r.status_code == 422
    assert r.json() == {"detail": '"jobs" must be an object'}


def test_a_non_object_body_is_rejected_by_the_framework(pin):
    client, _, _ = pin
    assert client.post("/transcribe-path-batch", json=[1, 2]).status_code == 422


@pytest.mark.parametrize("header", ["abc", "", "-5", "1.5", "0", "1_0"])
def test_an_unreadable_deadline_header_means_no_deadline(pin, header):
    client, _, _ = pin
    r = _post(
        client, {"k": {"path": GOOD, "seconds": 2}}, headers={"X-Whisper-Request-Timeout-S": header}
    )
    assert r.json()["results"]["k"]["error"] is None


def test_an_absurd_deadline_header_is_clamped_not_an_error(pin):
    client, _, _ = pin
    r = _post(
        client,
        {"k": {"path": GOOD, "seconds": 2}},
        headers={"X-Whisper-Request-Timeout-S": "9" * 40},
    )
    assert r.status_code == 200
    assert r.json()["results"]["k"]["error"] is None


# ── ffmpeg failure classification at the HTTP seam ───────────────────────────


@pytest.mark.parametrize(
    ("stderr", "expected"),
    [
        ("Input/output error", FFMPEG_TIMEOUT_IO_FAULT),
        ("Stale file handle", FFMPEG_TIMEOUT_IO_FAULT),
        ("x not on whitelist y", CONTAINER),
        ("Invalid data found when processing input", FFMPEG_ERROR_INVALID_DATA),
        ("moov atom not found", "ffmpeg_error: moov_missing"),
        ("Output file #0 does not contain any stream", "ffmpeg_error: no_audio_stream"),
        ("Unknown decoder 'batcave'", "ffmpeg_error: unsupported_codec"),
        ("av_read_frame: partial file", "ffmpeg_error: truncated_file"),
        ("Permission denied", "ffmpeg_error: permission_denied"),
        # Stderr the table has never seen is "other", never the stderr itself.
        ("the Joker rewired the batcave-keypad-ledger", "ffmpeg_error: other"),
    ],
)
def test_ffmpeg_failures_are_classified(pin, monkeypatch, stderr, expected):
    client, _, _ = pin
    monkeypatch.setenv("FAKE_FFMPEG_MODE", "fail")
    monkeypatch.setenv("FAKE_FFMPEG_STDERR", stderr)
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res == {"text": "", "error": expected}


def test_an_empty_clip_is_an_ffmpeg_error(pin, monkeypatch):
    client, _, _ = pin
    monkeypatch.setenv("FAKE_FFMPEG_MODE", "empty")
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res == {"text": "", "error": FFMPEG_ERROR_EMPTY_CLIP}


def test_a_missing_ffmpeg_is_worker_misconfigured(pin, monkeypatch, tmp_path):
    client, _, _ = pin
    monkeypatch.setenv("PATH", str(tmp_path / "nothing-here"))
    res = _post(client, {"k": {"path": GOOD}}).json()["results"]["k"]
    assert res == {"text": "", "error": FFMPEG_MISSING}
    assert client.get("/health").json()["ffmpeg"] is False


# ── the upload endpoints are untouched ───────────────────────────────────────


def test_upload_batch_keys_and_results(pin, caplog):
    client, _, _ = pin
    files = [
        ("files", ("book-1", b"abcd", "audio/wav")),
        ("files", ("book-2", b"BOOMBOOM", "audio/wav")),
    ]
    with caplog.at_level("ERROR"):
        res = client.post("/transcribe-batch", files=files).json()["results"]
    assert res["book-1"] == {"text": "heard-4", "error": None}
    assert res["book-2"] == {"text": "", "error": WHISPER_ERROR}
    assert "fake backend exploded" in caplog.text, "what the answer withholds is in the log"


def test_upload_single(pin):
    client, _, _ = pin
    r = client.post("/transcribe", files={"file": ("x.wav", b"abc", "audio/wav")})
    assert r.json() == {"text": "heard-3", "error": None}


# ── inference failures keep their codes ──────────────────────────────────────


def test_a_backend_exception_is_a_whisper_error(pin, caplog):
    # The fake ffmpeg writes silence, so drive the backend through the upload
    # route, whose bytes reach the fake backend as-is.
    client, _, _ = pin
    with caplog.at_level("ERROR"):
        res = client.post(
            "/transcribe-batch", files=[("files", ("a", b"BOOMBOOM", "audio/wav"))]
        ).json()["results"]["a"]
    assert res == {"text": "", "error": WHISPER_ERROR}
    assert "ValueError" in caplog.text, "the exception's type and text are in the log"
