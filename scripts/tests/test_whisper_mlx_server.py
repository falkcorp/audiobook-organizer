# file: scripts/tests/test_whisper_mlx_server.py
# version: 1.14.0
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


def _module_without_identity_thread(monkeypatch):
    """The module loaded as the inference child loads it, so no machine-id
    thread is running yet: a test patches the sources, then calls
    start_machine_id() itself."""
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py", "--inference-child", "3", "4"])
    mod = _load_module()
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py"])
    assert mod._machine_id_first is None
    return mod


def _wait_for_machine_id(client, timeout=5.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        mid = client.get("/health").json().get("machine_id")
        if mid:
            return mid
        time.sleep(0.02)
    raise AssertionError("machine_id never appeared in /health")


def test_health_reports_an_opaque_machine_id(monkeypatch):
    """remote.go reads machine_id to tell machines apart: a file's timeout turns
    permanent only on two DIFFERENT machines, and two ports on one box must not
    count twice. It must be a short hex hash (the Go side stores it on the file
    row and returns it in API responses), never the raw name."""
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-gpu-1")
    mod = _module_without_identity_thread(monkeypatch)
    mod.start_machine_id().result(timeout=10)
    mid = TestClient(mod.app).get("/health").json().get("machine_id")
    assert isinstance(mid, str) and len(mid) == 16
    int(mid, 16)  # hex
    assert "gotham" not in mid
    assert mid == mod.machine_id(), "every process on one machine reports the same id"


def test_the_server_process_starts_the_identity_thread_and_the_child_does_not(monkeypatch):
    """The server process starts the machine-id thread at import, so /health
    never runs ioreg. The inference child (which re-imports the module on every
    restart) never starts it."""
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py", "--inference-child", "3", "4"])
    assert _load_module()._machine_id_first is None, "the inference child must not compute the id"
    monkeypatch.setattr(sys, "argv", ["whisper_mlx_server.py"])
    assert _load_module()._machine_id_first is not None, "the server process starts it at import"


def test_a_machine_id_found_on_a_retry_is_reported_and_never_changes(monkeypatch):
    """SF2 (review 4 of #4182): with no stable source the field is left out and
    the thread keeps trying. The first id it finds is reported, and the id never
    changes after, even if a later attempt would find another (a flip would
    make one Mac two known machines on the Go side)."""
    mod = _module_without_identity_thread(monkeypatch)
    answers = [None, None, "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"]
    calls = []

    def flaky():
        calls.append(1)
        return answers[min(len(calls), len(answers)) - 1]

    monkeypatch.setattr(mod, "machine_id", flaky)
    monkeypatch.setattr(mod, "MACHINE_ID_RETRY_S", 0.01)
    mod.start_machine_id().result(timeout=10)
    client = TestClient(mod.app)
    assert _wait_for_machine_id(client) == "aaaaaaaaaaaaaaaa"
    time.sleep(0.1)
    for _ in range(3):
        assert client.get("/health").json()["machine_id"] == "aaaaaaaaaaaaaaaa"
    assert len(calls) == 3, "the thread stops once an id is found"


def test_a_slow_first_attempt_leaves_the_id_out_then_reports_it(monkeypatch):
    """SF2 and N1: a /health that comes while ioreg is still running answers
    within MACHINE_ID_WAIT_S without the field (the Go side then uses an
    unknown-machine id), and reports the id once ioreg answers."""
    mod = _module_without_identity_thread(monkeypatch)
    gate = threading.Event()
    monkeypatch.setattr(mod, "machine_id", lambda: "0123456789abcdef" if gate.wait(10) else None)
    monkeypatch.setattr(mod, "MACHINE_ID_WAIT_S", 0.2)
    mod.start_machine_id()
    client = TestClient(mod.app)
    try:
        start = time.monotonic()
        body = client.get("/health").json()
        assert time.monotonic() - start < 2
        assert "machine_id" not in body and body["status"] == "ok"
    finally:
        gate.set()
    assert _wait_for_machine_id(client) == "0123456789abcdef"


def test_no_stable_source_gives_no_machine_id_never_the_hostname(monkeypatch, tmp_path):
    """SF2: only WHISPER_MACHINE_ID, /etc/machine-id and the macOS platform UUID
    are stable. With none of them (no override, no machine-id file, ioreg timing
    out) there is no id at all: a hostname-derived id would be reported as a
    KNOWN machine and could differ between two processes on one Mac."""
    import hashlib
    import subprocess as sp

    mod = _module_without_identity_thread(monkeypatch)
    monkeypatch.delenv("WHISPER_MACHINE_ID", raising=False)
    monkeypatch.setattr(mod, "MACHINE_ID_FILE", str(tmp_path / "absent"))

    def ioreg_times_out(*args, **kwargs):
        raise sp.TimeoutExpired(args[0], mod.MACHINE_ID_IOREG_TIMEOUT_S)

    monkeypatch.setattr(mod.subprocess, "run", ioreg_times_out)
    for platform in ("linux", "darwin"):
        monkeypatch.setattr(sys, "platform", platform)
        assert mod.machine_id() is None, platform

    # Each stable source, when present, is what the id is made from.
    def ioreg_answers(*args, **kwargs):
        return sp.CompletedProcess(args[0], 0, stdout='  "IOPlatformUUID" = "WAYNE-0001"\n')

    monkeypatch.setattr(mod.subprocess, "run", ioreg_answers)
    assert mod.machine_id() == hashlib.sha256(b"platform-uuid:WAYNE-0001").hexdigest()[:16]
    (tmp_path / "machine-id").write_text("batcave0001\n", encoding="ascii")
    monkeypatch.setattr(mod, "MACHINE_ID_FILE", str(tmp_path / "machine-id"))
    assert mod.machine_id() == hashlib.sha256(b"machine-id:batcave0001").hexdigest()[:16]


def test_machine_id_override_is_hashed_and_distinguishes_machines(srv, monkeypatch):
    """WHISPER_MACHINE_ID lets an operator name a machine (two boxes cloned from
    one image share /etc/machine-id). The value is hashed like every other
    source, and two names give two ids."""
    mod, _ = srv
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-gpu-1")
    a = mod.machine_id()
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-gpu-2")
    b = mod.machine_id()
    assert a != b
    assert "gotham" not in a and len(a) == 16


def test_health_waits_for_the_filesystem_and_the_machine_id_side_by_side(monkeypatch):
    """N4 (review 4 of #4182): the filesystem checks and the machine-id wait
    each have their own deadline, and /health runs them at the same time, so it
    answers within the longer one, not their sum. Both defaults sit under the
    Go probe's 3 s."""
    mod = _module_without_identity_thread(monkeypatch)
    assert mod.MACHINE_ID_WAIT_S <= mod.HEALTH_PATH_TIMEOUT_S < 3.0
    gate = threading.Event()
    monkeypatch.setattr(mod, "machine_id", lambda: "0123456789abcdef" if gate.wait(10) else None)
    monkeypatch.setattr(mod, "MACHINE_ID_WAIT_S", 1.0)
    monkeypatch.setattr(mod, "HEALTH_PATH_TIMEOUT_S", 1.0)
    monkeypatch.setattr(mod, "_blocking_health_fields", lambda: time.sleep(1.5) or {})
    mod.start_machine_id()
    try:
        start = time.monotonic()
        body = TestClient(mod.app).get("/health").json()
        elapsed = time.monotonic() - start
    finally:
        gate.set()
    assert "path_health_error" in body and "machine_id" not in body
    assert elapsed < 1.8, f"/health took {elapsed:.2f}s: the two waits ran one after the other"


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


def test_a_hung_root_listing_answers_200_and_keeps_the_busy_fields(srv, monkeypatch, tmp_path):
    """The NFS listing is the one call that can block; it runs on a dedicated
    pool under a deadline. A miss must NOT be a 503: the Go liveness poller and
    stuck-detection read busy_seconds / busy_limit_seconds from /health, and a 503
    hides them while the worker may be wedged. The answer is a 200 that carries
    every normal field, reports each mapped root as NOT readable (fail closed, so
    Go refuses the worker for path jobs) and says why in path_health_error."""
    mod, client = srv
    share_a, share_b = tmp_path / "a", tmp_path / "b"
    share_a.mkdir()
    share_b.mkdir()
    monkeypatch.setenv("WHISPER_PATH_MAP", f"/lib/Gotham={share_a},/lib/Arkham={share_b}")
    gate = threading.Event()
    monkeypatch.setattr(mod, "HEALTH_PATH_TIMEOUT_S", 0.2)
    monkeypatch.setattr(mod, "_root_readable", lambda root: (gate.wait(10), True)[1])
    mod._inference._set_busy(12.0)  # a clip is "running" with a 12 s limit
    try:
        t0 = time.monotonic()
        r = client.get("/health")
        assert time.monotonic() - t0 < 3
        assert r.status_code == 200
        body = r.json()
        assert body["status"] == "ok" and body["batch_pipeline"] is True
        assert body["busy_limit_seconds"] == 12.0 and body["busy_seconds"] >= 0
        assert body["clip_timeout_floor_s"] == mod.TRANSCRIBE_MIN_TIMEOUT_S
        assert body["clip_timeout_factor"] == mod.TRANSCRIBE_TIMEOUT_FACTOR
        assert body["child_ready_timeout_s"] == mod.CHILD_READY_TIMEOUT_S
        assert body["extract_timeout_s"] == mod.EXTRACT_TIMEOUT_S
        assert body["decode_fingerprint"] == mod.DECODE_FINGERPRINT
        assert body["extract_fingerprint"] == mod.EXTRACT_FINGERPRINT
        # Configured, but nothing is vouched for: every root fails closed.
        assert body["path_pipeline"] is True
        assert body["path_prefixes"] == ["/lib/Gotham", "/lib/Arkham"]
        assert body["path_roots"] == [
            {"prefix": "/lib/Gotham", "readable": False},
            {"prefix": "/lib/Arkham", "readable": False},
        ]
        assert "ffmpeg" not in body, "ffmpeg is unknown, not null"
        assert body["path_health_error"] == "filesystem checks did not answer in time"
    finally:
        mod._inference._set_busy(None)
        gate.set()


def test_a_hung_listing_without_a_map_still_answers_200(srv, monkeypatch):
    mod, client = srv
    monkeypatch.delenv("WHISPER_PATH_MAP", raising=False)
    gate = threading.Event()
    monkeypatch.setattr(mod, "HEALTH_PATH_TIMEOUT_S", 0.2)
    monkeypatch.setattr(mod.shutil, "which", lambda name: (gate.wait(10), None)[1])
    try:
        r = client.get("/health")
        assert r.status_code == 200
        body = r.json()
        assert body["path_pipeline"] is False and body["path_roots"] == []
        assert body["path_health_error"] == "filesystem checks did not answer in time"
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
            transport=httpx.ASGITransport(app=mod.app), base_url="http://192.0.2.10"
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


def test_a_child_that_dies_mid_clip_is_a_decoder_crash_on_the_path_batch(childsrv):
    """A clip that deterministically kills the child must SETTLE: it is reported
    as decoder_crashed, which the Go side strikes like transcribe_timeout. As
    worker_misconfigured (a worker fault, deferred without a strike) it would be
    retried forever, killing the child every time."""
    _, client, put, _ = childsrv
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"dies": put("d.m4b", b"DIEDIE"), "next": put("n.m4b", b"abc")}},
    ).json()["results"]
    assert res["dies"]["text"] == ""
    assert res["dies"]["error"].startswith("decoder_crashed: ")
    assert "exit code 9" in res["dies"]["error"]
    assert "on this clip; it was restarted" in res["dies"]["error"]
    # The next clip starts a fresh child and is served normally.
    assert res["next"] == {"text": "heard-3", "error": None}


def test_a_child_that_dies_mid_clip_is_a_decoder_crash_on_the_upload_batch(childsrv):
    _, client, _, _ = childsrv
    res = client.post(
        "/transcribe-batch",
        files=[
            ("files", ("a", b"1234", "audio/wav")),
            ("files", ("b", b"xxDIEDIExx", "audio/wav")),
            ("files", ("c", b"123456", "audio/wav")),
        ],
    ).json()["results"]
    assert res["a"] == {"text": "heard-4", "error": None}
    assert res["b"]["text"] == ""
    assert res["b"]["error"].startswith("decoder_crashed: "), res["b"]
    assert "exit code 9" in res["b"]["error"]
    assert res["c"] == {"text": "heard-6", "error": None}, "the child restarts for a later clip"


def test_a_child_that_dies_mid_clip_is_a_decoder_crash_on_the_single_upload(childsrv):
    _, client, _, _ = childsrv
    r = client.post("/transcribe", files={"file": ("a", b"DIEDIE", "audio/wav")}).json()
    assert r["text"] == "" and r["error"].startswith("decoder_crashed: ")


def test_a_child_sigkilled_mid_clip_is_decoder_killed_not_a_crash(childsrv):
    """A SIGKILL comes from outside the decoder (jetsam, an operator), so it is
    decoder_killed, not decoder_crashed: the Go side strikes it like a timeout
    that only a second, different machine can make permanent, where a crash is a
    verdict about the clip. Path and upload batches."""
    _, client, put, _ = childsrv
    res = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"killed": put("k.m4b", b"KILLKILL"), "next": put("n.m4b", b"abc")}},
    ).json()["results"]
    assert res["killed"]["text"] == ""
    assert res["killed"]["error"].startswith("decoder_killed: "), res["killed"]
    assert "exit code -9" in res["killed"]["error"]
    assert res["next"] == {"text": "heard-3", "error": None}, "a fresh child serves the next clip"
    up = client.post("/transcribe", files={"file": ("a", b"KILLKILL", "audio/wav")}).json()
    assert up["text"] == "" and up["error"].startswith("decoder_killed: ")


def test_a_child_sigterm_or_unreaped_is_decoder_killed(childsrv, monkeypatch):
    mod, client, put, _ = childsrv
    real = mod.InferenceChild._exit_code
    for code in (-15, None):
        monkeypatch.setattr(
            mod.InferenceChild, "_exit_code", lambda self, c=code: (real(self), c)[1]
        )
        r = client.post("/transcribe-path", json=put("t.m4b", b"DIEDIE")).json()
        assert r["error"].startswith("decoder_killed: "), (code, r)


@pytest.mark.parametrize("data", [b"ABRTABRT", b"DIEDIE"])
def test_a_child_that_crashes_by_itself_is_still_decoder_crashed(childsrv, data):
    """SIGABRT (exit code -6) and a plain non-zero exit are the decoder dying on
    the clip: a verdict about the clip, struck on the Go side."""
    _, client, put, _ = childsrv
    r = client.post("/transcribe-path", json=put("c.m4b", data)).json()
    assert r["text"] == "" and r["error"].startswith("decoder_crashed: "), r


def test_decoder_killed_and_request_abandoned_codes_are_stable(srv):
    mod, _ = srv
    assert mod.ERR_DECODER_KILLED == "decoder_killed"
    assert mod.ERR_REQUEST_ABANDONED == "request_abandoned"


def test_abandoned_results_carry_the_request_abandoned_code_on_both_batch_paths(
    srv, pathsrv, monkeypatch
):
    mod, _ = srv
    up = mod._run_upload_batch(_uploads("a"), deadline=time.monotonic() - 1)
    assert up["a"]["error"].startswith("request_abandoned: ")
    assert not up["a"]["error"].startswith("whisper_error")
    pmod, _, _ = pathsrv
    monkeypatch.setattr(pmod, "_transcribe_file", lambda p: "ok")
    pj = pmod._run_path_jobs(
        {"a": _job("/lib/Gotham/books/A/one.m4b")}, deadline=time.monotonic() - 1
    )
    assert pj["a"]["error"].startswith("request_abandoned: ")
    assert not pj["a"]["error"].startswith("whisper_error")


def test_decoder_crashed_code_is_stable(srv):
    mod, _ = srv
    assert mod.ERR_DECODER_CRASHED == "decoder_crashed"


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


# ── A worker whose child cannot start says so in /health, and recovers ───────


def test_health_has_no_child_start_error_by_default(srv):
    _, client = srv
    assert "child_start_error" not in client.get("/health").json()


def test_a_failed_child_start_is_reported_in_health(childsrv, monkeypatch):
    """Go refuses a worker that reports child_start_error, so a worker that cannot
    start its child is taken out of rotation BEFORE a request is sent to it."""
    mod, client, put, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    res = client.post("/transcribe-path", json=put("a.m4b", b"abc")).json()
    assert res["error"].startswith("worker_misconfigured:")
    body = client.get("/health").json()
    assert "child_start_error" in body
    assert "while starting" in body["child_start_error"]
    assert body["child_start_error"] in res["error"]


def test_a_failed_child_start_in_an_upload_is_reported_in_health(childsrv, monkeypatch):
    _, client, _, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    client.post("/transcribe", files={"file": ("a", b"abc", "audio/wav")})
    assert "while starting" in client.get("/health").json()["child_start_error"]


def test_child_start_error_expires_after_the_ttl(childsrv, monkeypatch):
    """Without expiry a refused worker would never be sent the request that lets
    it recover: the key must go ABSENT (not null) once the TTL has passed."""
    mod, client, put, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    client.post("/transcribe-path", json=put("a.m4b", b"abc"))
    assert "child_start_error" in client.get("/health").json()
    monkeypatch.setattr(mod, "CHILD_FAULT_TTL_S", 0.3)
    time.sleep(0.4)
    assert "child_start_error" not in client.get("/health").json()


def test_a_successful_start_clears_child_start_error(childsrv, monkeypatch):
    mod, client, put, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    client.post("/transcribe-path", json=put("a.m4b", b"abc"))
    assert "child_start_error" in client.get("/health").json()
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "fake_whisper_backend:factory")
    ok = client.post("/transcribe-path", json=put("b.m4b", b"abcd")).json()
    assert ok == {"text": "heard-4", "error": None}
    assert "child_start_error" not in client.get("/health").json()


def test_a_mid_clip_death_is_not_a_start_failure(childsrv):
    mod, client, put, _ = childsrv
    client.post("/transcribe-path", json=put("d.m4b", b"DIEDIE"))
    assert "child_start_error" not in client.get("/health").json()


def test_health_reads_the_start_error_without_the_inference_lock(childsrv, monkeypatch):
    """A wedged job holds _inference_lock; /health must still report the fault."""
    mod, client, put, _ = childsrv
    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    client.post("/transcribe-path", json=put("a.m4b", b"abc"))
    with mod._inference_lock:
        assert "child_start_error" in client.get("/health").json()


def test_child_fault_ttl_comes_from_the_environment(monkeypatch):
    assert _load_module().CHILD_FAULT_TTL_S == 300
    monkeypatch.setenv("WHISPER_CHILD_FAULT_TTL_S", "42")
    assert _load_module().CHILD_FAULT_TTL_S == 42


# ── An abandoned batch stops starting clips ──────────────────────────────────

ABANDONED = "request_abandoned: the client's request timeout passed before this clip started"


class _Upload:
    """The two attributes the upload loop reads from a starlette UploadFile."""

    def __init__(self, filename, data):
        import io

        self.filename = filename
        self.file = io.BytesIO(data)


def _uploads(*names):
    return [_Upload(n, b"1234") for n in names]


def test_the_abandon_header_parser(srv):
    mod, _ = srv

    from starlette.datastructures import Headers

    class Req:
        def __init__(self, value):
            raw = {} if value is None else {"X-Whisper-Request-Timeout-S": value}
            self.headers = Headers(raw)  # case-insensitive, like the real request

    before = time.monotonic()
    got = mod._abandon_deadline(Req("30"))
    assert before + 30 <= got <= time.monotonic() + 30
    assert mod._abandon_deadline(Req(" 7 ")) is not None
    for bad in (None, "", "  ", "abc", "-5", "0", "1.5", "1_0", "5s", "\u00b2"):
        assert mod._abandon_deadline(Req(bad)) is None, bad
    assert mod._abandon_deadline(Req("9" * 400)) is not None, "huge is capped, never an error"


def test_without_a_deadline_every_upload_runs(srv):
    mod, _ = srv
    calls = []
    mod._transcribe_file = lambda p: (calls.append(p), "ok")[1]
    res = mod._run_upload_batch(_uploads("a", "b", "c"))
    assert len(calls) == 3
    assert all(r == {"text": "ok", "error": None} for r in res.values())


def test_an_upload_batch_stops_after_the_clip_in_flight(srv):
    """The deadline is checked before STARTING a clip, never mid-clip: clip a is
    already running when it passes, finishes, and b and c are not run."""
    mod, _ = srv
    calls = []

    def slow(path):
        calls.append(path)
        time.sleep(0.5)
        return "ok"

    mod._transcribe_file = slow
    res = mod._run_upload_batch(_uploads("a", "b", "c"), deadline=time.monotonic() + 0.2)
    assert len(calls) == 1
    assert res["a"] == {"text": "ok", "error": None}
    assert res["b"] == {"text": "", "error": ABANDONED}
    assert res["c"] == {"text": "", "error": ABANDONED}


def test_an_upload_batch_already_past_its_deadline_runs_nothing(srv):
    mod, _ = srv
    calls = []
    mod._transcribe_file = lambda p: (calls.append(p), "ok")[1]
    res = mod._run_upload_batch(_uploads("a", "b"), deadline=time.monotonic() - 1)
    assert calls == []
    assert [res[k]["error"] for k in ("a", "b")] == [ABANDONED, ABANDONED]


def test_the_upload_batch_header_abandons_the_rest(srv):
    mod, client = srv
    calls = []

    def slow(path):
        calls.append(path)
        time.sleep(1.3)
        return "ok"

    mod._transcribe_file = slow
    r = client.post(
        "/transcribe-batch",
        headers={"X-Whisper-Request-Timeout-S": "1"},
        files=[("files", (n, b"1234", "audio/wav")) for n in ("a", "b", "c")],
    )
    res = r.json()["results"]
    assert len(calls) == 1
    assert res["a"] == {"text": "ok", "error": None}
    assert res["b"]["error"] == ABANDONED and res["c"]["error"] == ABANDONED


@pytest.mark.parametrize("value", [None, "", "abc", "-3", "0", "2.5"])
def test_a_missing_or_garbage_header_means_no_deadline(srv, value):
    mod, client = srv
    mod._transcribe_file = lambda p: "ok"
    headers = {} if value is None else {"X-Whisper-Request-Timeout-S": value}
    r = client.post(
        "/transcribe-batch",
        headers=headers,
        files=[("files", (n, b"1234", "audio/wav")) for n in ("a", "b", "c")],
    )
    assert all(v == {"text": "ok", "error": None} for v in r.json()["results"].values())


def test_the_path_batch_header_abandons_the_rest(pathsrv, monkeypatch):
    mod, client, share = pathsrv
    calls = []

    def slow(path):
        calls.append(path)
        time.sleep(1.3)
        return "ok"

    monkeypatch.setattr(mod, "_transcribe_file", slow)
    jobs = {k: _job("/lib/Gotham/books/A/one.m4b") for k in ("a", "b", "c")}
    r = client.post(
        "/transcribe-path-batch", headers={"X-Whisper-Request-Timeout-S": "1"}, json={"jobs": jobs}
    )
    res = r.json()["results"]
    assert len(calls) == 1
    assert res["a"] == {"text": "ok", "error": None}
    assert res["b"]["error"] == ABANDONED and res["c"]["error"] == ABANDONED


@pytest.mark.parametrize("value", [None, "", "abc", "-3", "0"])
def test_the_path_batch_ignores_a_missing_or_garbage_header(pathsrv, value):
    _, client, _ = pathsrv
    headers = {} if value is None else {"X-Whisper-Request-Timeout-S": value}
    jobs = {k: _job("/lib/Gotham/books/A/one.m4b") for k in ("a", "b")}
    res = client.post("/transcribe-path-batch", headers=headers, json={"jobs": jobs}).json()
    assert all(v["error"] is None for v in res["results"].values())


def _clip_dir(tmp_path, name):
    """A tmpdir holding a clip, as _extract_clip returns one."""
    d = tmp_path / name
    d.mkdir()
    (d / "clip.wav").write_bytes(b"abc")
    return d


def test_discarding_a_finished_extraction_removes_its_clip(srv, tmp_path):
    import concurrent.futures

    mod, _ = srv
    d = _clip_dir(tmp_path, "done")
    fut = concurrent.futures.Future()
    fut.set_result((str(d), str(d / "clip.wav")))
    mod._discard_extraction(fut)
    assert not d.exists()


def test_discarding_a_running_extraction_removes_its_clip_when_it_finishes(srv, tmp_path):
    import concurrent.futures

    mod, _ = srv
    d = _clip_dir(tmp_path, "running")
    fut = concurrent.futures.Future()
    assert fut.set_running_or_notify_cancel()  # running: cancel() cannot stop it
    mod._discard_extraction(fut)
    assert d.exists(), "nothing to remove until the extraction finishes"
    fut.set_result((str(d), str(d / "clip.wav")))
    assert not d.exists()


def test_discarding_a_queued_extraction_cancels_it(srv):
    import concurrent.futures

    mod, _ = srv
    fut = concurrent.futures.Future()
    mod._discard_extraction(fut)
    assert fut.cancelled()


def test_discarding_a_failed_extraction_is_quiet(srv):
    import concurrent.futures

    mod, _ = srv
    fut = concurrent.futures.Future()
    assert fut.set_running_or_notify_cancel()
    mod._discard_extraction(fut)
    fut.set_exception(mod.PathJobError(mod.ERR_NOT_FOUND, "gone"))  # nothing to clean


def test_an_abandoned_path_batch_cancels_its_extractions_and_removes_their_clips(
    pathsrv, monkeypatch
):
    mod, _, _ = pathsrv
    import tempfile

    made, gate = [], threading.Event()

    def extract(src, seconds):
        gate.wait(10)
        d = tempfile.mkdtemp(prefix="test-clip-")
        made.append(d)
        (pathlib.Path(d) / "clip.wav").write_bytes(b"x")
        return d, str(pathlib.Path(d) / "clip.wav")

    monkeypatch.setattr(mod, "_extract_clip", extract)
    monkeypatch.setattr(mod, "EXTRACT_WORKERS", 1)
    calls = []
    monkeypatch.setattr(mod, "_transcribe_file", lambda p: (calls.append(p), "ok")[1])
    jobs = {k: _job("/lib/Gotham/books/A/one.m4b") for k in ("a", "b", "c")}
    t0 = time.monotonic()
    res = mod._run_path_jobs(jobs, deadline=time.monotonic() - 1)
    assert time.monotonic() - t0 < 5, "must not wait for the extractions"
    assert calls == []
    assert {k: v["error"] for k, v in res.items()} == dict.fromkeys(jobs, ABANDONED)
    gate.set()
    mod._extract_pool.shutdown(wait=True)  # running extractions and their callbacks are done
    assert not any(pathlib.Path(d).exists() for d in made), "no temp clip may leak"


# ── A start fault answers the rest of a path batch without waiting ───────────


def test_a_start_fault_answers_the_rest_without_waiting_for_extraction(childsrv, monkeypatch):
    """After the child fails to start, each remaining job used to be waited on
    (up to EXTRACT_TIMEOUT_S apiece) before being answered. They are answered at
    once; running extractions drop their own clip when they finish, queued ones
    are cancelled and never run."""
    mod, client, put, made = childsrv
    import tempfile

    monkeypatch.setenv("WHISPER_BACKEND_FACTORY", "no_such_backend_module:factory")
    monkeypatch.setattr(mod, "EXTRACT_WORKERS", 1)  # one runs, the rest queue
    gate = threading.Event()
    extracted = []

    def extract(src, seconds):
        name = pathlib.Path(src).name
        extracted.append(name)
        if name != "first.m4b":
            gate.wait(8)  # a slow NFS read
        d = tempfile.mkdtemp(prefix="test-clip-")
        made.append(d)
        (pathlib.Path(d) / "clip.wav").write_bytes(b"abc")
        return d, str(pathlib.Path(d) / "clip.wav")

    monkeypatch.setattr(mod, "_extract_clip", extract)
    jobs = {k: put(f"{k}.m4b", b"abc") for k in ("b", "c", "d")}
    jobs = {"first": put("first.m4b", b"abc"), **jobs}
    t0 = time.monotonic()
    try:
        res = client.post("/transcribe-path-batch", json={"jobs": jobs}).json()["results"]
        elapsed = time.monotonic() - t0
        assert elapsed < 5, f"waited {elapsed:.1f}s for slow extractions"
        assert set(res) == set(jobs)
        for k in jobs:
            assert res[k]["error"].startswith("worker_misconfigured:"), (k, res[k])
    finally:
        gate.set()
    mod._extract_pool.shutdown(wait=True)  # running extractions and their callbacks are done
    assert len(made) == 2, "first and b were cut"
    assert not any(pathlib.Path(d).exists() for d in made), "no temp clip may leak"
    # b was already being extracted; c and d were still queued and got cancelled.
    assert sorted(extracted) == ["b.m4b", "first.m4b"]


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
