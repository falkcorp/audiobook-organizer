# file: scripts/tests/test_whisper_mlx_server.py
# version: 1.0.0
# guid: 8b4d19f2-6c30-4e71-a5d9-1f7c3e8a2b45
# last-edited: 2026-08-30
#
# Contract tests for scripts/whisper_mlx_server.py.
#
# These are written from internal/transcribe/remote.go -- the CLIENT -- not
# from the server. That direction matters: a test written from the server can
# only prove the server is self-consistent, which is exactly the failure mode
# where a worker returns 200 with keys nobody reads and transcripts silently
# never land. Every assertion below traces to a specific line of remote.go.
#
# They run on ANY platform: the module imports mlx_whisper lazily inside
# _transcribe_file, and these tests replace that function. So CI (Linux, no
# Apple Silicon) still exercises multipart parsing, result keying and per-file
# error isolation -- everything except the inference call itself.
#
# Run:
#   uv run --with fastapi --with httpx --with pytest --with python-multipart \
#     pytest scripts/tests/test_whisper_mlx_server.py -v

import importlib.util
import pathlib
import sys

import pytest
from fastapi.testclient import TestClient

_SERVER = pathlib.Path(__file__).resolve().parents[1] / "whisper_mlx_server.py"


def _load_module():
    spec = importlib.util.spec_from_file_location("whisper_mlx_server", _SERVER)
    mod = importlib.util.module_from_spec(spec)
    sys.modules["whisper_mlx_server"] = mod
    spec.loader.exec_module(mod)
    return mod


@pytest.fixture
def srv(monkeypatch):
    """The server with inference stubbed to echo the temp file's byte length.

    Returning something derived from the actual bytes -- rather than a constant
    -- is what makes the "did the right file reach the right key" assertions
    below meaningful. A constant would pass even if the server mixed up which
    upload went to which result.
    """
    mod = _load_module()

    def fake(path: str) -> str:
        return f"text-for-{pathlib.Path(path).stat().st_size}-bytes"

    monkeypatch.setattr(mod, "_transcribe_file", fake)
    return mod, TestClient(mod.app)


# ── /health: the batch capability probe ──────────────────────────────────────


def test_health_advertises_batch_pipeline(srv):
    """remote.go supportsRemoteBatch decodes {"batch_pipeline": *bool} and
    enables the batch path when the pointer is NON-NIL. The key must therefore
    be present and a real JSON bool -- omitting it silently demotes this worker
    to the slow per-file path, with no error anywhere."""
    _, client = srv
    r = client.get("/health")
    assert r.status_code == 200
    body = r.json()
    assert "batch_pipeline" in body, "absent key demotes the worker to per-file"
    assert body["batch_pipeline"] is True
    assert body["status"] == "ok"


# ── /transcribe: the single-file fallback path ───────────────────────────────


def test_transcribe_uses_form_field_named_file(srv):
    """remote.go transcribeOneRemote calls CreateFormFile("file", ...)."""
    _, client = srv
    r = client.post("/transcribe", files={"file": ("book-1.wav", b"1234567890", "audio/wav")})
    assert r.status_code == 200
    assert r.json() == {"text": "text-for-10-bytes", "error": None}


def test_transcribe_rejects_a_differently_named_field(srv):
    """Pins the field NAME, not just that some upload works. If the server
    accepted any field name this test would pass while the real client's
    "file" part went unread."""
    _, client = srv
    r = client.post("/transcribe", files={"upload": ("book-1.wav", b"x", "audio/wav")})
    assert r.status_code == 422


# ── /transcribe-batch: the path production actually uses ─────────────────────


def test_batch_uses_form_field_named_files(srv):
    """remote.go sendBatch calls CreateFormFile("files", e.id) for every file."""
    _, client = srv
    r = client.post(
        "/transcribe-batch",
        files=[
            ("files", ("book-a", b"11111", "audio/wav")),
            ("files", ("book-b", b"1111111111", "audio/wav")),
        ],
    )
    assert r.status_code == 200
    assert r.json() == {
        "results": {
            "book-a": {"text": "text-for-5-bytes", "error": None},
            "book-b": {"text": "text-for-10-bytes", "error": None},
        }
    }


def test_batch_rejects_a_differently_named_field(srv):
    _, client = srv
    r = client.post("/transcribe-batch", files=[("file", ("book-a", b"x", "audio/wav"))])
    assert r.status_code == 422


def test_batch_keys_results_by_filename_verbatim(srv):
    """THE load-bearing assertion. sendBatch sets each part's filename to the
    BOOK ID and looks the result up by that exact string; anything the server
    does to it -- basename, lowercase, strip an extension, URL-decode -- makes
    the transcript unfindable and the book silently stays untranscribed.

    The ids below are shaped like real ones: a ULID, something with dots and
    spaces, and a unicode title."""
    _, client = srv
    ids = [
        "01JAV9X2K7QF3B8N4M6P0RSTUV",
        "book.with.dots and spaces.wav",
        "Jules Verne — 20,000 Leagues",
    ]
    r = client.post(
        "/transcribe-batch",
        files=[("files", (i, b"1234", "audio/wav")) for i in ids],
    )
    assert r.status_code == 200
    assert sorted(r.json()["results"].keys()) == sorted(ids)


def test_one_failing_file_does_not_fail_the_batch(srv):
    """A 64-file batch must not be lost to one bad WAV. The failing entry gets
    a non-null error and an empty text; every other entry still transcribes.
    remote.go copies error into BatchResult.Error per id and keeps the rest."""
    mod, client = srv

    def selective(path: str) -> str:
        if pathlib.Path(path).stat().st_size == 3:
            raise RuntimeError("ffmpeg: Invalid data found when processing input")
        return "ok"

    mod._transcribe_file = selective

    r = client.post(
        "/transcribe-batch",
        files=[
            ("files", ("good-1", b"12345", "audio/wav")),
            ("files", ("bad-1", b"123", "audio/wav")),
            ("files", ("good-2", b"12345", "audio/wav")),
        ],
    )
    assert r.status_code == 200
    results = r.json()["results"]
    assert results["good-1"] == {"text": "ok", "error": None}
    assert results["good-2"] == {"text": "ok", "error": None}
    assert results["bad-1"]["text"] == ""
    assert "Invalid data" in results["bad-1"]["error"]


def test_every_result_carries_exactly_text_and_error(srv):
    """remote.go decodes into a struct of {Text string; Error *string}. Extra
    keys are ignored by Go, but a MISSING error key would decode as nil and
    read as success -- so pin both, on both endpoints."""
    _, client = srv
    single = client.post("/transcribe", files={"file": ("b", b"1", "audio/wav")}).json()
    assert set(single.keys()) == {"text", "error"}

    batch = client.post(
        "/transcribe-batch", files=[("files", ("b", b"1", "audio/wav"))]
    ).json()["results"]["b"]
    assert set(batch.keys()) == {"text", "error"}


def test_inference_is_serialized(srv):
    """The module holds a lock around inference. MLX shares the Mac's unified
    memory with the desktop; concurrent transcriptions contend for memory
    bandwidth rather than going faster. Pinned so nobody 'optimises' it into a
    thread pool without reading why."""
    mod, _ = srv
    assert isinstance(mod._inference_lock, type(__import__("threading").Lock()))
