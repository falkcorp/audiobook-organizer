# file: scripts/tests/test_whisper_mlx_server.py
# version: 1.9.0
# guid: 8b4d19f2-6c30-4e71-a5d9-1f7c3e8a2b45
# last-edited: 2026-10-26
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

import contextlib
import importlib.util
import os
import pathlib
import shutil
import subprocess
import sys
import threading
import time
import unittest

# ci.yml's Repo Guards job runs `python3 -m unittest discover -s scripts`, and
# every OTHER test under scripts/ is stdlib-only. This one needs fastapi and
# httpx to exercise a real HTTP surface, so under bare unittest its import
# would be an ERROR and fail that job. Raising SkipTest at import time makes
# unittest report it as SKIPPED -- visible, not silently dropped -- while the
# uv-driven step in ci.yml (and the command in this file's header) runs it for
# real with the dependencies present. Do not "fix" this by deleting the guard;
# fix it by making sure the uv step still runs.
try:
    import pytest
    from fastapi.testclient import TestClient
except ImportError as exc:  # pragma: no cover - dependency-gated
    raise unittest.SkipTest(
        f"whisper_mlx_server contract tests need fastapi+httpx+pytest ({exc}); "
        "run: uv run --with fastapi --with httpx --with pytest --with python-multipart "
        "pytest scripts/tests/test_whisper_mlx_server.py"
    )

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
    # The fixtures map plain tmp dirs, which are not mount points; the mount
    # check has its own tests that switch it back on.
    monkeypatch.setenv("WHISPER_PATH_REQUIRE_MOUNT", "0")

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


def test_health_reports_decode_fingerprint(srv):
    """The Go result journal keys transcripts by decode_fingerprint, so a
    retuned worker never serves transcripts from its old configuration. It
    must be present, and must change when a decode setting changes."""
    mod, client = srv
    fp = client.get("/health").json().get("decode_fingerprint")
    assert isinstance(fp, str) and len(fp) == 16
    import hashlib
    import json

    changed = dict(mod.DECODE_SETTINGS, language="de")
    other = hashlib.sha256(json.dumps(changed, sort_keys=True).encode()).hexdigest()[:16]
    assert other != fp


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

    batch = client.post("/transcribe-batch", files=[("files", ("b", b"1", "audio/wav"))]).json()[
        "results"
    ]["b"]
    assert set(batch.keys()) == {"text", "error"}


def test_inference_is_serialized(srv):
    """The module holds a lock around inference. MLX shares the Mac's unified
    memory with the desktop; concurrent transcriptions contend for memory
    bandwidth rather than going faster. Pinned so nobody 'optimises' it into a
    thread pool without reading why."""
    mod, _ = srv
    assert isinstance(mod._inference_lock, type(__import__("threading").Lock()))


class FfmpegPreflightTest(unittest.TestCase):
    """The worker must refuse to start without ffmpeg.

    Without this the failure is invisible in the worst possible way: mlx_whisper
    raises per file, _do_transcribe catches it and returns
    {"text": "", "error": "... 'ffmpeg'"}, and the batch endpoint answers 200.
    The transport is healthy, /health says ok, and the caller writes one
    whisper_error per book. Fail-closed at startup instead.
    """

    def test_missing_ffmpeg_exits_rather_than_serving(self):
        import shutil as _shutil

        mod = _load_module()
        real_which = _shutil.which
        _shutil.which = lambda name, *a, **k: (
            None if name == "ffmpeg" else real_which(name, *a, **k)
        )
        try:
            with self.assertRaises(SystemExit) as ctx:
                mod._require_ffmpeg()
            self.assertEqual(ctx.exception.code, 1)
        finally:
            _shutil.which = real_which

    def test_present_ffmpeg_returns_its_path(self):
        # Skipped rather than asserted when the host genuinely lacks ffmpeg:
        # CI runners may not have it, and this test is about the happy path,
        # not about the runner's package list. The refusal case above is the
        # one that must run everywhere.
        import shutil as _shutil

        if _shutil.which("ffmpeg") is None:
            self.skipTest("ffmpeg not installed on this host")
        self.assertTrue(_load_module()._require_ffmpeg())


# ── Path pipeline: /transcribe-path and /transcribe-path-batch ───────────────
#
# Contract source: internal/transcribe/path.go (the Go client) and the status
# mapping in internal/plugins/maintenance/intro_transcribe.go. The error CODE
# prefixes asserted here are what the Go side switches on.


@pytest.fixture
def pathsrv(srv, tmp_path, monkeypatch):
    """srv plus a mapped share: server /lib/Gotham -> tmp_path/share."""
    mod, client = srv
    share = tmp_path / "share"
    (share / "books" / "A").mkdir(parents=True)
    (share / "books" / "A" / "one.m4b").write_bytes(b"audio")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")

    def fake_extract(src, seconds):
        # Echo what was asked for, so the tests prove which file and how many
        # seconds reached extraction. Returns a real temp dir like the real one.
        import tempfile

        d = tempfile.mkdtemp(prefix="test-clip-")
        wav = pathlib.Path(d) / "clip.wav"
        wav.write_bytes(f"{pathlib.Path(src).name}|{seconds}".encode())
        return d, str(wav)

    monkeypatch.setattr(mod, "_extract_clip", fake_extract)
    monkeypatch.setattr(
        mod, "_transcribe_file", lambda path: "heard:" + pathlib.Path(path).read_text()
    )
    return mod, client, share


def _job(path, seconds=90):
    return {"path": path, "seconds": seconds}


def test_health_advertises_path_pipeline_and_roots(pathsrv):
    _, client, share = pathsrv
    body = client.get("/health").json()
    assert body["path_pipeline"] is True
    assert body["path_prefixes"] == ["/lib/Gotham"]
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": True}]
    assert len(body["extract_fingerprint"]) == 16
    # The upload-path contract is untouched.
    assert body["batch_pipeline"] is True and len(body["decode_fingerprint"]) == 16


def test_health_without_a_map_is_not_a_path_worker(srv, monkeypatch):
    """No default map: unset means the capability is OFF, never a guess."""
    _, client = srv
    monkeypatch.delenv("WHISPER_PATH_MAP", raising=False)
    body = client.get("/health").json()
    assert body["path_pipeline"] is False
    assert body["path_prefixes"] == [] and body["path_roots"] == []


def test_health_reports_a_lost_mount_per_root(pathsrv, monkeypatch, tmp_path):
    mod, client, _ = pathsrv
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={tmp_path / 'gone'}")
    body = client.get("/health").json()
    assert body["path_pipeline"] is True
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": False}]


def test_health_malformed_map_fails_closed(srv, monkeypatch):
    _, client = srv
    monkeypatch.setenv("WHISPER_PATH_MAP", "/lib/Gotham")  # no '='
    body = client.get("/health").json()
    assert body["path_pipeline"] is False
    assert "path_map_error" in body


def test_path_batch_maps_and_keys_verbatim(pathsrv):
    _, client, _ = pathsrv
    key = "Jules Verne — 20,000 Leagues"
    r = client.post(
        "/transcribe-path-batch",
        json={"jobs": {key: _job("/lib/Gotham/books/A/one.m4b", 300)}},
    )
    assert r.status_code == 200
    assert r.json() == {"results": {key: {"text": "heard:one.m4b|300", "error": None}}}


def test_single_path_endpoint(pathsrv):
    _, client, _ = pathsrv
    r = client.post("/transcribe-path", json=_job("/lib/Gotham/books/A/one.m4b"))
    assert r.json() == {"text": "heard:one.m4b|90", "error": None}


@pytest.mark.parametrize(
    "path",
    [
        "/lib/Gotham/../etc/passwd",
        "/lib/Gotham/books/../../etc/passwd",
        "/lib/GothamX/books/A/one.m4b",  # string prefix, not a component prefix
        "/mnt/other/books/A/one.m4b",
        "books/A/one.m4b",  # relative
        "/etc/passwd",
        "/",
    ],
)
def test_unmapped_or_traversing_paths_are_refused(pathsrv, path):
    _, client, _ = pathsrv
    res = client.post("/transcribe-path-batch", json={"jobs": {"k": _job(path)}}).json()["results"][
        "k"
    ]
    assert res["text"] == ""
    assert res["error"].startswith("path_unmapped:")


def test_symlink_escaping_the_root_is_refused(pathsrv, tmp_path):
    _, client, share = pathsrv
    outside = tmp_path / "outside.m4b"
    outside.write_bytes(b"secret")
    (share / "books" / "link.m4b").symlink_to(outside)
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/link.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:")


def test_lost_mount_fails_each_job_closed_as_path_unmapped(pathsrv, monkeypatch, tmp_path):
    _, client, _ = pathsrv
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={tmp_path / 'gone'}")
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:") and "mount" in res["error"]


def test_no_map_refuses_every_job(pathsrv, monkeypatch):
    _, client, _ = pathsrv
    monkeypatch.delenv("WHISPER_PATH_MAP")
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:")


def test_missing_file_under_a_readable_root_is_not_found(pathsrv, monkeypatch):
    mod, client, _ = pathsrv

    def real_extract_missing(src, seconds):
        raise mod.PathJobError(mod.ERR_NOT_FOUND, "file not found under the mapped root")

    monkeypatch.setattr(mod, "_extract_clip", real_extract_missing)
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/nope.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("not_found:")


@pytest.mark.parametrize("seconds", [0, -1, 601, 1.5, "90", True, None])
def test_bad_seconds_is_a_per_job_error(pathsrv, seconds):
    _, client, _ = pathsrv
    jobs = {
        "bad": {"path": "/lib/Gotham/books/A/one.m4b", "seconds": seconds},
        "good": _job("/lib/Gotham/books/A/one.m4b"),
    }
    results = client.post("/transcribe-path-batch", json={"jobs": jobs}).json()["results"]
    assert results["bad"]["error"].startswith("bad_request:")
    assert results["good"]["error"] is None  # one bad job never fails the batch


def test_path_batch_rejects_a_non_object_jobs(pathsrv):
    _, client, _ = pathsrv
    assert client.post("/transcribe-path-batch", json={"jobs": []}).status_code == 422


def test_one_failing_path_job_does_not_fail_the_batch(pathsrv, monkeypatch):
    mod, client, share = pathsrv
    (share / "books" / "A" / "bad.m4b").write_bytes(b"x")
    real = mod._extract_clip

    def selective(src, seconds):
        if src.endswith("bad.m4b"):
            raise mod.PathJobError(mod.ERR_FFMPEG, "Invalid data found when processing input")
        return real(src, seconds)

    monkeypatch.setattr(mod, "_extract_clip", selective)
    results = client.post(
        "/transcribe-path-batch",
        json={
            "jobs": {
                "good": _job("/lib/Gotham/books/A/one.m4b"),
                "bad": _job("/lib/Gotham/books/A/bad.m4b"),
            }
        },
    ).json()["results"]
    assert results["good"]["error"] is None
    assert results["bad"]["error"].startswith("ffmpeg_error:")


def test_a_whisper_failure_is_prefixed_and_isolated(pathsrv, monkeypatch):
    mod, client, _ = pathsrv

    def boom(path):
        raise RuntimeError("model fell over")

    monkeypatch.setattr(mod, "_transcribe_file", boom)
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("whisper_error:") and "model fell over" in res["error"]


def test_temp_clips_are_removed(pathsrv, monkeypatch):
    mod, client, _ = pathsrv
    seen = []
    real = mod._extract_clip

    def tracking(src, seconds):
        d, w = real(src, seconds)
        seen.append(d)
        return d, w

    monkeypatch.setattr(mod, "_extract_clip", tracking)
    client.post("/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}})
    assert seen and not any(pathlib.Path(d).exists() for d in seen)


def test_path_handlers_are_sync_so_health_stays_live(srv):
    """async def would run the whole batch on the event loop and /health would
    stop answering; the Go pool treats an unreadable /health as a refused
    endpoint. Pinned so nobody 'tidies' them to async."""
    import inspect

    mod, _ = srv
    assert not inspect.iscoroutinefunction(mod.transcribe_path)
    assert not inspect.iscoroutinefunction(mod.transcribe_path_batch)


@pytest.mark.parametrize(
    "raw",
    ["/mnt/x", "=/a", "/mnt/x=", "mnt/x=/a", "/mnt/x=a", "/=/a", "/a=/", "/a/../b=/c"],
)
def test_parse_path_map_rejects_malformed_entries(srv, raw):
    mod, _ = srv
    with pytest.raises(ValueError):
        mod.parse_path_map(raw)


def test_parse_path_map_longest_prefix_first(srv):
    mod, _ = srv
    pairs = mod.parse_path_map("/lib/Gotham=/a, /lib/Gotham/books=/b ,")
    assert pairs[0][0] == "/lib/Gotham/books"


# ── ffmpeg failure classification (the real _extract_clip, subprocess faked) ──
#
# ffmpeg_error is a DURABLE verdict on the Go side: it hides the book until its
# mtime changes. So a nonzero exit is only that verdict when the file is still
# readable and the stderr is not an I/O fault.


@pytest.fixture
def extract_env(srv, tmp_path, monkeypatch):
    mod, client = srv
    share = tmp_path / "share"
    share.mkdir()
    src = share / "book.m4b"
    src.write_bytes(b"audio")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    monkeypatch.setattr(mod.shutil, "which", lambda name: "/fake/bin/ffmpeg")
    return mod, share, str(src)


def _fake_run(monkeypatch, mod, stderr, before=None):
    calls = []

    def run(argv, **kw):
        calls.append(argv)
        if before:
            before()
        return subprocess.CompletedProcess(argv, 1, b"", stderr)

    monkeypatch.setattr(mod.subprocess, "run", run)
    return calls


def _extract_code(mod, src):
    with pytest.raises(mod.PathJobError) as ei:
        mod._extract_clip(src, 90)
    return ei.value.code, str(ei.value)


@pytest.mark.parametrize(
    "stderr",
    [
        b"book.m4b: Input/output error\n",
        b"read failed: Operation timed out",
        b"Stale file handle",
        b"Connection timed out",
        b"Device not configured",
    ],
)
def test_ffmpeg_io_fault_text_is_a_retryable_timeout_not_a_verdict(
    extract_env, monkeypatch, stderr
):
    mod, _, src = extract_env
    _fake_run(monkeypatch, mod, stderr)
    code, _ = _extract_code(mod, src)
    assert code == mod.ERR_FFMPEG_TIMEOUT


def test_ffmpeg_decode_failure_on_a_readable_file_stays_the_verdict(extract_env, monkeypatch):
    mod, _, src = extract_env
    _fake_run(monkeypatch, mod, b"book.m4b: Invalid data found when processing input\n")
    code, _ = _extract_code(mod, src)
    assert code == mod.ERR_FFMPEG


def test_ffmpeg_failure_after_the_mount_dropped_is_path_unmapped(extract_env, monkeypatch):
    """The decode error text looks like a corrupt file, but the root can no
    longer be listed by the time ffmpeg exits: the evidence says mount fault."""
    mod, share, src = extract_env
    gone = share.with_name("share-gone")
    _fake_run(
        monkeypatch,
        mod,
        b"Invalid data found when processing input",
        before=lambda: share.rename(gone),
    )
    code, _ = _extract_code(mod, src)
    assert code == mod.ERR_PATH_UNMAPPED


def test_ffmpeg_failure_when_the_file_vanished_is_path_unmapped(extract_env, monkeypatch):
    mod, _, src = extract_env
    _fake_run(
        monkeypatch,
        mod,
        b"Invalid data found when processing input",
        before=lambda: pathlib.Path(src).unlink(),
    )
    code, _ = _extract_code(mod, src)
    assert code == mod.ERR_PATH_UNMAPPED


def test_missing_ffmpeg_is_a_worker_fault_not_a_book_verdict(extract_env, monkeypatch):
    mod, _, src = extract_env
    monkeypatch.setattr(mod.shutil, "which", lambda name: None)
    code, text = _extract_code(mod, src)
    assert code == mod.ERR_WORKER_MISCONFIGURED
    assert text.startswith("worker_misconfigured: ")


def test_ffmpeg_argv_restricts_the_demuxers_before_the_input(extract_env, monkeypatch):
    """A planted .m3u8 must not let ffmpeg's HLS demuxer open other paths."""
    mod, _, src = extract_env
    calls = _fake_run(monkeypatch, mod, b"x")
    _extract_code(mod, src)
    argv = calls[0]
    i = argv.index("-format_whitelist")
    assert argv[i + 1] == mod.FORMAT_WHITELIST
    assert i < argv.index("-i")
    names = mod.FORMAT_WHITELIST.split(",")
    assert "hls" not in names and "concat" not in names and "sdp" not in names


def test_extract_fingerprint_covers_the_format_whitelist(srv):
    mod, _ = srv
    assert "-format_whitelist" in mod.EXTRACT_SETTINGS["args"]


def test_worker_misconfigured_reaches_the_client_as_a_prefixed_error(pathsrv, monkeypatch):
    mod, client, _ = pathsrv

    def no_ffmpeg(src, seconds):
        raise mod.PathJobError(mod.ERR_WORKER_MISCONFIGURED, "ffmpeg is not on PATH on this worker")

    monkeypatch.setattr(mod, "_extract_clip", no_ffmpeg)
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("worker_misconfigured:")


# A soft, read-only mount that stays dead past deadtimeout is forcibly
# unmounted, leaving an EMPTY mountpoint directory. It lists fine, so only the
# mount-point check can tell it from a healthy share.
def test_an_unmounted_empty_mountpoint_is_not_a_readable_root(srv, monkeypatch, tmp_path):
    mod, _ = srv
    monkeypatch.setenv("WHISPER_PATH_REQUIRE_MOUNT", "1")
    mountpoint = tmp_path / "bigdata"
    mountpoint.mkdir()
    assert mod._root_readable(str(mountpoint)) is False  # plain dir: not a mount
    monkeypatch.setattr(mod.os.path, "ismount", lambda p: True)
    assert mod._root_readable(str(mountpoint)) is True  # the same dir, as a mount


def test_a_forcibly_unmounted_root_fails_closed_per_job_and_in_health(srv, monkeypatch, tmp_path):
    mod, client = srv
    monkeypatch.setenv("WHISPER_PATH_REQUIRE_MOUNT", "1")
    share = tmp_path / "share"
    share.mkdir()  # empty leftover directory, not a mount
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    body = client.get("/health").json()
    assert body["path_roots"] == [{"prefix": "/lib/Gotham", "readable": False}]
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/lib/Gotham/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:")


def test_the_mount_check_defaults_on(srv, monkeypatch, tmp_path):
    mod, _ = srv
    monkeypatch.delenv("WHISPER_PATH_REQUIRE_MOUNT", raising=False)
    assert mod._root_readable(str(tmp_path)) is False


def test_ffmpeg_whitelist_rejection_is_container_rejected_not_a_verdict(extract_env, monkeypatch):
    mod, _, src = extract_env
    _fake_run(
        monkeypatch,
        mod,
        b"[in#0] Format not on whitelist 'mov,mp4'\n[in#0] Error opening input: Invalid argument\n",
    )
    code, _ = _extract_code(mod, src)
    assert code == mod.ERR_CONTAINER_REJECTED


def test_format_whitelist_names_only_real_demuxers(srv):
    mod, _ = srv
    assert "opus" not in mod.FORMAT_WHITELIST.split(",")  # Ogg Opus is read by "ogg"


@pytest.mark.skipif(shutil.which("ffmpeg") is None, reason="ffmpeg not installed on this host")
def test_real_ffmpeg_renamed_aiff_is_container_rejected(srv, tmp_path, monkeypatch):
    """Real ffmpeg, real whitelist: an AIFF renamed .m4b is refused by the
    whitelist and reported as container_rejected (non-durable on the Go side),
    where a corrupt .m4b is still the durable ffmpeg_error."""
    mod, _ = srv
    share = tmp_path / "share"
    share.mkdir()
    subprocess.run(
        [
            "ffmpeg",
            "-nostdin",
            "-y",
            "-v",
            "error",
            "-f",
            "lavfi",
            "-i",
            "sine=frequency=440:duration=2",
            "-f",
            "aiff",
            str(share / "renamed.m4b"),
        ],
        check=True,
    )
    (share / "corrupt.m4b").write_bytes(b"not audio at all")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    for name, want in (
        ("renamed.m4b", mod.ERR_CONTAINER_REJECTED),
        ("corrupt.m4b", mod.ERR_FFMPEG),
    ):
        with pytest.raises(mod.PathJobError) as ei:
            mod._extract_clip(str(share / name), 5)
        assert ei.value.code == want, name


# ── /health liveness with a hung mount ───────────────────────────────────────


def test_health_is_async_and_never_waits_for_the_shared_threadpool(childsrv, monkeypatch):
    """The sync upload handlers each park a threadpool thread while they queue on
    the inference lock; a sync /health would need a thread too and starve behind
    them, so the Go liveness probe would read a busy worker as a dead one. With
    the default 40-thread pool saturated by uploads, /health must still answer."""
    import asyncio
    import inspect

    import httpx

    mod, _, _, _ = childsrv
    assert inspect.iscoroutinefunction(mod.health)
    release = threading.Event()
    monkeypatch.setattr(mod, "_transcribe_file", lambda p: (release.wait(30), "x")[1])

    async def scenario():
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=mod.app), base_url="http://192.0.2.10"
        ) as c:
            uploads = [
                asyncio.create_task(
                    c.post("/transcribe", files={"file": ("p.wav", b"abc", "audio/wav")})
                )
                for _ in range(48)
            ]
            await asyncio.sleep(1.0)
            try:
                return await asyncio.wait_for(c.get("/health"), timeout=5)
            finally:
                release.set()
                await asyncio.gather(*uploads, return_exceptions=True)

    resp = asyncio.run(scenario())
    assert resp.status_code == 200
    assert resp.json()["status"] == "ok"


def test_a_hung_root_listing_answers_503_instead_of_hanging_health(srv, monkeypatch):
    """The NFS listing is the one call that can block; it runs on a dedicated
    pool under a deadline, and a miss is a 503 (which the Go pool refuses)."""
    mod, client = srv
    gate = threading.Event()
    monkeypatch.setattr(mod, "HEALTH_PATH_TIMEOUT_S", 0.3)
    monkeypatch.setattr(mod, "_path_health", lambda: (gate.wait(10), {})[1])
    try:
        t0 = time.monotonic()
        r = client.get("/health")
        assert r.status_code == 503
        assert time.monotonic() - t0 < 3
    finally:
        gate.set()


def test_root_readable_reads_one_entry_not_the_whole_listing(srv, monkeypatch, tmp_path):
    mod, _ = srv
    (tmp_path / "a").write_text("x")
    monkeypatch.setattr(
        mod.os, "listdir", lambda *a, **k: pytest.fail("listdir builds the whole library listing")
    )
    assert mod._root_readable(str(tmp_path)) is True
    assert mod._root_readable(str(tmp_path / "missing")) is False


def test_mapped_path_returned_is_the_resolved_real_path(srv, monkeypatch, tmp_path):
    mod, _ = srv
    real_share = tmp_path / "real"
    real_share.mkdir()
    (real_share / "f.m4b").write_bytes(b"x")
    link = tmp_path / "link"
    link.symlink_to(real_share)
    got = mod.map_server_path("/lib/Gotham/f.m4b", [("/lib/Gotham", str(link))])
    assert got == str((real_share / "f.m4b").resolve())


@pytest.mark.skipif(shutil.which("ffmpeg") is None, reason="ffmpeg not installed on this host")
def test_real_ffmpeg_extracts_a_valid_16k_mono_clip(srv, tmp_path, monkeypatch):
    """The only test that runs the REAL extraction: a lavfi tone, cut to 2 s."""
    mod, client = srv
    share = tmp_path / "share"
    share.mkdir()
    src = share / "tone.m4a"
    subprocess.run(
        [
            "ffmpeg",
            "-nostdin",
            "-y",
            "-v",
            "error",
            "-f",
            "lavfi",
            "-i",
            "sine=frequency=440:duration=5",
            str(src),
        ],
        check=True,
    )
    (share / "broken.m4a").write_bytes(b"not audio at all")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    import wave

    seen = {}

    def inspect_wav(path):
        with wave.open(path, "rb") as w:
            seen["rate"], seen["ch"] = w.getframerate(), w.getnchannels()
            seen["secs"] = w.getnframes() / w.getframerate()
        return "tone"

    monkeypatch.setattr(mod, "_transcribe_file", inspect_wav)
    results = client.post(
        "/transcribe-path-batch",
        json={
            "jobs": {
                "tone": _job("/lib/Gotham/tone.m4a", 2),
                "broken": _job("/lib/Gotham/broken.m4a", 2),
                "absent": _job("/lib/Gotham/absent.m4a", 2),
            }
        },
    ).json()["results"]
    assert results["tone"] == {"text": "tone", "error": None}
    assert (seen["rate"], seen["ch"]) == (16000, 1) and 1.9 < seen["secs"] < 2.1
    assert results["broken"]["error"].startswith("ffmpeg_error:")
    assert results["absent"]["error"].startswith("not_found:")


# ── Killable inference child: per-clip deadline ──────────────────────────────
#
# These run a REAL child process (scripts/tests/fake_whisper_backend.py stands
# in for mlx_whisper), so the kill, the respawn and the pipe protocol are
# exercised for real on any platform. The 2026-10-23 incident: one clip sent
# mlx_whisper into an endless decode loop while the worker held its inference
# lock, so every later request parked behind it and all four workers wedged.


@pytest.fixture
def childsrv(tmp_path, monkeypatch):
    """The server with the REAL _transcribe_file driving a fake-backend child,
    a 1.5 s per-clip floor and a mapped share. Extraction is faked: the clip is
    the source file's bytes, so a file's content steers the fake backend."""
    import tempfile

    mod = _load_module()
    here = str(pathlib.Path(__file__).resolve().parent)
    monkeypatch.setenv("PYTHONPATH", here + os.pathsep + os.environ.get("PYTHONPATH", ""))
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "fake_whisper_backend:factory")
    monkeypatch.setenv("WHISPER_PATH_REQUIRE_MOUNT", "0")
    monkeypatch.setattr(mod, "TRANSCRIBE_MIN_TIMEOUT_S", 1.5)
    monkeypatch.setattr(mod, "TRANSCRIBE_TIMEOUT_FACTOR", 0.0)
    share = tmp_path / "share"
    (share / "b").mkdir(parents=True)
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share}")
    made = []

    def fake_extract(src, seconds):
        d = tempfile.mkdtemp(prefix="test-clip-")
        made.append(d)
        wav = pathlib.Path(d) / "clip.wav"
        wav.write_bytes(pathlib.Path(src).read_bytes())
        return d, str(wav)

    monkeypatch.setattr(mod, "_extract_clip", fake_extract)

    def put(name, content):
        (share / "b" / name).write_bytes(content)
        return {"path": f"/lib/Gotham/b/{name}", "seconds": 90}

    yield mod, TestClient(mod.app), put, made
    mod._inference.shutdown()


def _alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    # A killed-but-unreaped child is a zombie; InferenceChild reaps it.
    return True


def test_hung_clip_times_out_and_child_is_killed_and_restarted(childsrv):
    mod, client, put, _ = childsrv
    jobs = {
        "good-1": put("one.m4b", b"abcd"),
        "poison": put("two.m4b", b"xxHANGHANGxx"),
        "good-2": put("three.m4b", b"abcdefgh"),
    }
    t0 = time.monotonic()
    first = client.post("/transcribe-path-batch", json={"jobs": {"good-1": jobs["good-1"]}})
    assert first.json()["results"]["good-1"] == {"text": "heard-4", "error": None}
    old_pid = mod._inference._proc.pid
    assert _alive(old_pid)

    res = client.post("/transcribe-path-batch", json={"jobs": jobs}).json()["results"]
    # Bounded: one 1.5 s limit plus a child start, never the hang.
    assert time.monotonic() - t0 < 30
    assert res["poison"]["text"] == ""
    assert res["poison"]["error"].startswith("transcribe_timeout:")
    # The batch kept going: the clip AFTER the poison one was served by a fresh child.
    assert res["good-1"]["error"] is None
    assert res["good-2"] == {"text": "heard-8", "error": None}
    assert not _alive(old_pid), "the hung child must be killed, not left spinning"
    assert mod._inference._proc.pid != old_pid


def test_health_answers_with_busy_seconds_while_a_clip_hangs(childsrv):
    mod, client, put, _ = childsrv
    monkey_job = put("hang.m4b", b"HANGHANG")
    box = {}

    def go():
        box["res"] = TestClient(mod.app).post("/transcribe-path", json=monkey_job).json()

    idle = client.get("/health").json()
    assert idle["busy_seconds"] == 0 and idle["busy_limit_seconds"] is None
    assert idle["clip_timeout_floor_s"] == 1.5 and idle["clip_timeout_factor"] == 0.0

    th = threading.Thread(target=go)
    th.start()
    seen = None
    deadline = time.monotonic() + 20
    while th.is_alive() and time.monotonic() < deadline:
        h = client.get("/health").json()
        if h["busy_limit_seconds"] is not None and h["busy_seconds"] > 0:
            seen = h
            break
        time.sleep(0.05)
    th.join(30)
    assert seen is not None, "/health must report the running clip while inference is stuck"
    assert seen["busy_limit_seconds"] == 1.5
    assert box["res"]["error"].startswith("transcribe_timeout:")
    after = client.get("/health").json()
    assert after["busy_seconds"] == 0 and after["busy_limit_seconds"] is None


def test_a_hung_upload_does_not_freeze_the_event_loop(childsrv):
    """/transcribe and /transcribe-batch used to be async def calling a blocking
    inference: one hung upload froze the loop and with it /health.

    Both requests go through ONE event loop (httpx's ASGI transport inside one
    asyncio.run): starlette's TestClient gives every request its own loop and
    so cannot show a frozen one."""
    import asyncio

    import httpx

    mod, _, _, _ = childsrv

    async def scenario():
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=mod.app), base_url="http://worker"
        ) as c:
            upload = asyncio.create_task(
                c.post("/transcribe", files={"file": ("p.wav", b"HANGHANG", "audio/wav")})
            )
            # Tick every 50 ms while the upload runs and record the worst gap
            # between ticks. A loop frozen by a blocking handler cannot tick
            # until the handler returns, so one gap is as long as the hang.
            worst, last = 0.0, time.monotonic()
            while not upload.done():
                await asyncio.sleep(0.05)
                now = time.monotonic()
                worst, last = max(worst, now - last), now
            health = await c.get("/health")
            return health.status_code, worst, upload.result().json()

    status, lag, upload = asyncio.run(scenario())
    assert status == 200
    assert lag < 1.0, f"the event loop was stalled for {lag:.1f}s by the upload"
    assert upload["text"] == "" and upload["error"].startswith("transcribe_timeout:")


def test_upload_batch_isolates_a_timeout_per_file(childsrv):
    _, client, _, _ = childsrv
    r = client.post(
        "/transcribe-batch",
        files=[
            ("files", ("a", b"1234", "audio/wav")),
            ("files", ("b", b"HANGHANG", "audio/wav")),
            ("files", ("c", b"123456", "audio/wav")),
        ],
    ).json()["results"]
    assert r["a"] == {"text": "heard-4", "error": None}
    assert r["b"]["error"].startswith("transcribe_timeout:")
    assert r["c"] == {"text": "heard-6", "error": None}


def test_a_crashed_child_is_a_worker_fault_not_a_timeout_or_a_clip_verdict(childsrv):
    """A child that dies is a worker fault, not a statement about the clip: it
    must not be reported as transcribe_timeout (which the Go side strikes), and
    not as whisper_error (which the Go side records against the book as failed).
    worker_misconfigured is the code both ops defer without writing anything."""
    _, client, put, _ = childsrv
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"dies": put("d.m4b", b"DIEDIE"), "next": put("n.m4b", b"abc")}},
    ).json()["results"]
    assert res["dies"]["error"].startswith("worker_misconfigured:")
    assert "died" in res["dies"]["error"]
    # The next clip starts a fresh child and is served normally.
    assert res["next"] == {"text": "heard-3", "error": None}


def test_a_child_that_cannot_start_fails_the_whole_batch_once(childsrv, monkeypatch):
    """A child that never becomes ready is a worker fault, reported as
    worker_misconfigured for EVERY job, and the start is attempted ONCE: without
    the short-circuit each clip would wait out its own CHILD_READY_TIMEOUT_S."""
    mod, client, put, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    spawns = []
    real_spawn = mod._inference._spawn
    monkeypatch.setattr(mod._inference, "_spawn", lambda: (spawns.append(1), real_spawn())[1])
    t0 = time.monotonic()
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {k: put(f"{k}.m4b", b"abc") for k in ("a", "b", "c")}},
    ).json()["results"]
    assert time.monotonic() - t0 < 30
    assert len(spawns) == 1, "one failed start must short-circuit the rest of the batch"
    for k in ("a", "b", "c"):
        assert res[k]["text"] == ""
        assert res[k]["error"].startswith("worker_misconfigured:"), res[k]
        assert "while starting" in res[k]["error"]


def test_a_child_that_never_gets_ready_is_a_worker_fault(childsrv, monkeypatch, tmp_path):
    mod, client, put, _ = childsrv
    monkeypatch.setattr(mod, "CHILD_READY_TIMEOUT_S", 0.3)
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "slow_loading_backend:factory")
    # The backend factory sleeps past the ready deadline.
    slow = tmp_path / "slowdir"
    slow.mkdir()
    (slow / "slow_loading_backend.py").write_text(
        "import time\n\ndef factory():\n    time.sleep(30)\n"
    )
    monkeypatch.setenv("PYTHONPATH", str(slow) + os.pathsep + os.environ.get("PYTHONPATH", ""))
    res = client.post("/transcribe-path-batch", json={"jobs": {"a": put("a.m4b", b"abc")}}).json()[
        "results"
    ]
    assert res["a"]["error"].startswith("worker_misconfigured:")
    assert "not ready" in res["a"]["error"]


def test_upload_batch_short_circuits_after_a_failed_child_start(childsrv, monkeypatch):
    mod, client, _, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    spawns = []
    real_spawn = mod._inference._spawn
    monkeypatch.setattr(mod._inference, "_spawn", lambda: (spawns.append(1), real_spawn())[1])
    r = client.post(
        "/transcribe-batch",
        files=[("files", (n, b"abc", "audio/wav")) for n in ("a", "b", "c")],
    ).json()["results"]
    assert len(spawns) == 1
    assert all(r[n]["error"].startswith("worker_misconfigured:") for n in "abc")


def test_a_backend_exception_is_a_per_file_whisper_error(childsrv):
    _, client, put, _ = childsrv
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"boom": put("b.m4b", b"BOOMBOOM"), "ok": put("o.m4b", b"ab")}},
    ).json()["results"]
    assert res["boom"]["error"].startswith("whisper_error:")
    assert "fake backend exploded" in res["boom"]["error"]
    assert res["ok"] == {"text": "heard-2", "error": None}


def test_clip_tmpdirs_are_removed_on_timeout_and_error_paths(childsrv):
    _, client, put, made = childsrv
    client.post(
        "/transcribe-path-batch",
        json={
            "jobs": {
                "t": put("t.m4b", b"HANGHANG"),
                "e": put("e.m4b", b"BOOMBOOM"),
                "d": put("d.m4b", b"DIEDIE"),
                "k": put("k.m4b", b"fine"),
            }
        },
    )
    assert len(made) == 4
    assert not any(pathlib.Path(d).exists() for d in made)


def _write_wav(path, seconds, rate=16000):
    import wave as _wave

    with _wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(b"\x00\x00" * int(seconds * rate))


def test_clip_timeout_is_four_times_the_audio_with_a_floor(srv, tmp_path):
    mod, _ = srv
    ninety, five = tmp_path / "90.wav", tmp_path / "5.wav"
    _write_wav(ninety, 90)
    _write_wav(five, 5)
    assert mod._audio_seconds(str(ninety)) == pytest.approx(90)
    assert mod._clip_timeout(str(ninety)) == pytest.approx(360)  # 4 x 90
    assert mod._clip_timeout(str(five)) == 60  # 4 x 5 = 20, floored to 60


def test_clip_timeout_uses_the_header_not_the_requested_length(srv, tmp_path, monkeypatch):
    """A 20 s file that was asked for 90 s runs for 20 s of audio."""
    mod, _ = srv
    short = tmp_path / "20.wav"
    _write_wav(short, 20)
    monkeypatch.setattr(mod, "TRANSCRIBE_MIN_TIMEOUT_S", 10.0)
    assert mod._clip_timeout(str(short)) == pytest.approx(80)


def test_audio_seconds_never_exceeds_what_the_file_size_allows(srv, tmp_path):
    """A header that claims far more frames than the file holds (forged, or a
    truncated download) must not stretch the clip's deadline."""
    import struct

    mod, _ = srv
    lying = tmp_path / "lying.wav"
    # 100 bytes of audio but a header claiming an hour of 16 kHz mono.
    frames = 16000 * 3600
    data_len = frames * 2
    header = (
        b"RIFF"
        + struct.pack("<I", 36 + data_len)
        + b"WAVEfmt "
        + struct.pack("<IHHIIHH", 16, 1, 1, 16000, 32000, 2, 16)
        + b"data"
        + struct.pack("<I", data_len)
    )
    lying.write_bytes(header + b"\x00" * 100)
    size = lying.stat().st_size
    assert mod._audio_seconds(str(lying)) == pytest.approx(size / 32000)
    assert mod._audio_seconds(str(lying)) < 1


def test_clip_timeout_knobs_come_from_the_environment(monkeypatch):
    monkeypatch.setenv("WHISPER_TRANSCRIBE_MIN_TIMEOUT_S", "7")
    monkeypatch.setenv("WHISPER_TRANSCRIBE_TIMEOUT_FACTOR", "2.5")
    mod = _load_module()
    assert mod.TRANSCRIBE_MIN_TIMEOUT_S == 7 and mod.TRANSCRIBE_TIMEOUT_FACTOR == 2.5


def test_upload_handlers_are_sync_so_inference_cannot_freeze_the_loop(srv):
    import inspect

    mod, _ = srv
    assert not inspect.iscoroutinefunction(mod.transcribe)
    assert not inspect.iscoroutinefunction(mod.transcribe_batch)


def test_timeout_settings_do_not_change_the_decode_fingerprint(srv):
    """The limit says nothing about the text produced, so it must not be in
    DECODE_SETTINGS: that would make every journalled transcript unreachable."""
    mod, _ = srv
    assert set(mod.DECODE_SETTINGS) == {
        "backend",
        "model",
        "language",
        "condition_on_previous_text",
    }


def test_health_busy_fields_are_present_and_idle_by_default(srv):
    _, client = srv
    body = client.get("/health").json()
    assert body["busy_seconds"] == 0
    assert body["busy_limit_seconds"] is None
    assert body["clip_timeout_floor_s"] == 60 and body["clip_timeout_factor"] == 4
    # What a request can wait on before a clip's own clock starts: the client's
    # ceiling for a request that meets a cold worker is built from these.
    assert body["child_ready_timeout_s"] == 300
    assert body["extract_timeout_s"] == 120


def test_timeout_error_code_is_stable(srv):
    mod, _ = srv
    assert mod.ERR_TRANSCRIBE_TIMEOUT == "transcribe_timeout"


def test_the_orphaned_child_exits_when_its_parent_is_killed(tmp_path, monkeypatch):
    """A child stuck in a decode loop never reads its pipe, so EOF alone would
    leave it spinning forever after the parent is SIGKILLed. The child watches
    its parent and exits by itself; this runs a real child under a throwaway
    parent, kills the parent without any cleanup, and expects the child gone."""
    import signal as _signal

    here = str(pathlib.Path(__file__).resolve().parent)
    monkeypatch.setenv("PYTHONPATH", here + os.pathsep + os.environ.get("PYTHONPATH", ""))
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "fake_whisper_backend:factory")
    hang = tmp_path / "hang.wav"
    hang.write_bytes(b"HANGHANG")
    parent_src = (
        "import json, os, subprocess, sys, time\n"
        "r1, w1 = os.pipe()\n"
        "r2, w2 = os.pipe()\n"
        f"p = subprocess.Popen([sys.executable, {str(_SERVER)!r}, '--inference-child',"
        " str(r1), str(w2)], pass_fds=(r1, w2))\n"
        # Hand the child a clip that never decodes: it is now stuck in the
        # backend and will not read its pipe again, so the pipe closing when this
        # parent dies cannot be what ends it.
        f"os.write(w1, json.dumps({{'path': {str(hang)!r}}}).encode() + b'\\n')\n"
        "print(p.pid, flush=True)\n"
        "time.sleep(120)\n"
    )
    parent = subprocess.Popen(
        [sys.executable, "-c", parent_src], stdout=subprocess.PIPE, text=True, env=os.environ
    )
    child_pid = None
    try:
        child_pid = int(parent.stdout.readline())
        assert _alive(child_pid)
        # Let the child get past its backend load and start its watcher.
        time.sleep(1.0)
        os.kill(parent.pid, _signal.SIGKILL)
        parent.wait(timeout=10)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline and _alive(child_pid):
            time.sleep(0.25)
        assert not _alive(child_pid), "the orphaned inference child must exit on its own"
    finally:
        if parent.poll() is None:
            parent.kill()
        if child_pid is not None and _alive(child_pid):
            with contextlib.suppress(ProcessLookupError):
                os.kill(child_pid, _signal.SIGKILL)
