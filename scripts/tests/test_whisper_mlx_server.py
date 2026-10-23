# file: scripts/tests/test_whisper_mlx_server.py
# version: 1.6.0
# guid: 8b4d19f2-6c30-4e71-a5d9-1f7c3e8a2b45
# last-edited: 2026-10-23
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
import shutil
import subprocess
import sys
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
    """srv plus a mapped share: server /mnt/bigdata -> tmp_path/share."""
    mod, client = srv
    share = tmp_path / "share"
    (share / "books" / "A").mkdir(parents=True)
    (share / "books" / "A" / "one.m4b").write_bytes(b"audio")
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/mnt/bigdata={share}")

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
    assert body["path_prefixes"] == ["/mnt/bigdata"]
    assert body["path_roots"] == [{"prefix": "/mnt/bigdata", "readable": True}]
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
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/mnt/bigdata={tmp_path / 'gone'}")
    body = client.get("/health").json()
    assert body["path_pipeline"] is True
    assert body["path_roots"] == [{"prefix": "/mnt/bigdata", "readable": False}]


def test_health_malformed_map_fails_closed(srv, monkeypatch):
    _, client = srv
    monkeypatch.setenv("WHISPER_PATH_MAP", "/mnt/bigdata")  # no '='
    body = client.get("/health").json()
    assert body["path_pipeline"] is False
    assert "path_map_error" in body


def test_path_batch_maps_and_keys_verbatim(pathsrv):
    _, client, _ = pathsrv
    key = "Jules Verne — 20,000 Leagues"
    r = client.post(
        "/transcribe-path-batch",
        json={"jobs": {key: _job("/mnt/bigdata/books/A/one.m4b", 300)}},
    )
    assert r.status_code == 200
    assert r.json() == {"results": {key: {"text": "heard:one.m4b|300", "error": None}}}


def test_single_path_endpoint(pathsrv):
    _, client, _ = pathsrv
    r = client.post("/transcribe-path", json=_job("/mnt/bigdata/books/A/one.m4b"))
    assert r.json() == {"text": "heard:one.m4b|90", "error": None}


@pytest.mark.parametrize(
    "path",
    [
        "/mnt/bigdata/../etc/passwd",
        "/mnt/bigdata/books/../../etc/passwd",
        "/mnt/bigdataX/books/A/one.m4b",  # string prefix, not a component prefix
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
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/link.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:")


def test_lost_mount_fails_each_job_closed_as_path_unmapped(pathsrv, monkeypatch, tmp_path):
    _, client, _ = pathsrv
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/mnt/bigdata={tmp_path / 'gone'}")
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:") and "mount" in res["error"]


def test_no_map_refuses_every_job(pathsrv, monkeypatch):
    _, client, _ = pathsrv
    monkeypatch.delenv("WHISPER_PATH_MAP")
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("path_unmapped:")


def test_missing_file_under_a_readable_root_is_not_found(pathsrv, monkeypatch):
    mod, client, _ = pathsrv

    def real_extract_missing(src, seconds):
        raise mod.PathJobError(mod.ERR_NOT_FOUND, "file not found under the mapped root")

    monkeypatch.setattr(mod, "_extract_clip", real_extract_missing)
    res = client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/nope.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("not_found:")


@pytest.mark.parametrize("seconds", [0, -1, 601, 1.5, "90", True, None])
def test_bad_seconds_is_a_per_job_error(pathsrv, seconds):
    _, client, _ = pathsrv
    jobs = {
        "bad": {"path": "/mnt/bigdata/books/A/one.m4b", "seconds": seconds},
        "good": _job("/mnt/bigdata/books/A/one.m4b"),
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
                "good": _job("/mnt/bigdata/books/A/one.m4b"),
                "bad": _job("/mnt/bigdata/books/A/bad.m4b"),
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
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/one.m4b")}}
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
    client.post(
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/one.m4b")}}
    )
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
    pairs = mod.parse_path_map("/mnt/bigdata=/a, /mnt/bigdata/books=/b ,")
    assert pairs[0][0] == "/mnt/bigdata/books"


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
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/mnt/bigdata={share}")
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
        "/transcribe-path-batch", json={"jobs": {"k": _job("/mnt/bigdata/books/A/one.m4b")}}
    ).json()["results"]["k"]
    assert res["error"].startswith("worker_misconfigured:")


# ── /health liveness with a hung mount ───────────────────────────────────────


def test_health_is_sync_and_a_hung_root_does_not_block_the_event_loop(srv):
    """A plain def runs in the threadpool, so a root listing that hangs on a
    dead NFS server stalls one thread, not the loop serving the upload
    endpoints."""
    import inspect

    mod, _ = srv
    assert not inspect.iscoroutinefunction(mod.health)


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
    got = mod.map_server_path("/mnt/bigdata/f.m4b", [("/mnt/bigdata", str(link))])
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
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/mnt/bigdata={share}")
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
                "tone": _job("/mnt/bigdata/tone.m4a", 2),
                "broken": _job("/mnt/bigdata/broken.m4a", 2),
                "absent": _job("/mnt/bigdata/absent.m4a", 2),
            }
        },
    ).json()["results"]
    assert results["tone"] == {"text": "tone", "error": None}
    assert (seen["rate"], seen["ch"]) == (16000, 1) and 1.9 < seen["secs"] < 2.1
    assert results["broken"]["error"].startswith("ffmpeg_error:")
    assert results["absent"]["error"].startswith("not_found:")
