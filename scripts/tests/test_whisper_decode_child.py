# file: scripts/tests/test_whisper_decode_child.py
# version: 1.0.1
# guid: 2c9d7e15-6a48-4f3b-8e01-d5b7a39c6f82
# last-edited: 2026-11-17
#
# Tests for scripts/whisper_decode_child.py, the killable decode child of the
# non-Mac Whisper worker, with REAL spawned child processes and a fake backend
# (tests/whisper_fake_backend.py) that hangs, crashes, exits or fails to load, plus
# the worker's HTTP surface running on top of it. No model, no GPU, no Windows
# needed; "spawn" is the only start method on Windows, so this is the same code path
# there. POSIX-only cases (SIGKILL, orphan detection) skip on Windows.

import math
import os
import pathlib
import struct
import subprocess
import sys
import textwrap
import threading
import time
import unittest
import wave

try:
    import pytest
    from fastapi.testclient import TestClient
except ImportError as exc:  # pragma: no cover - dependency-gated
    raise unittest.SkipTest(f"decode child tests need fastapi+httpx+pytest ({exc})")

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))  # the spawned child imports whisper_fake_backend by name
sys.path.insert(0, str(HERE.parent))
import whisper_decode_child as wdc  # noqa: E402
import whisper_path_pipeline as wpp  # noqa: E402
import whisper_server as ws  # noqa: E402

FACTORY = "whisper_fake_backend:make"
POSIX_ONLY = pytest.mark.skipif(sys.platform == "win32", reason="POSIX signals")


def _wav(directory, name, seconds=1.0, rate=16000):
    path = pathlib.Path(directory) / name
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(
            b"".join(
                struct.pack("<h", int(8000 * math.sin(2 * math.pi * 440 * i / rate)))
                for i in range(int(seconds * rate))
            )
        )
    return str(path)


@pytest.fixture
def make_child():
    made = []

    def make(arg=None, **kw):
        kw.setdefault("ready_timeout_s", 60.0)
        child = wdc.DecodeChild(FACTORY, arg or {}, **kw)
        made.append(child)
        return child

    yield make
    for c in made:
        c.shutdown()


def _code(exc):
    return str(exc.value).split(":")[0]


# ── one child ────────────────────────────────────────────────────────────────


def test_a_clip_is_transcribed_and_the_child_reports_what_it_loaded(make_child, tmp_path):
    child = make_child()
    meta = child.start()
    assert meta["device"] == "cpu" and meta["model"] == "fake" and meta["pid"] != os.getpid()
    assert child.run(_wav(tmp_path, "a.wav"), 20) == "heard:a.wav"
    assert child.meta()["pid"] == meta["pid"], "the same long-lived child answered"


def test_a_hung_decode_is_killed_at_the_deadline_and_the_worker_recovers(make_child, tmp_path):
    child = make_child()
    first = child.start()["pid"]
    old = child._proc
    t0 = time.monotonic()
    with pytest.raises(wpp.PathJobError) as e:
        child.run(_wav(tmp_path, "hang.wav"), 1.0)
    assert _code(e) == "transcribe_timeout"
    assert 0.9 < time.monotonic() - t0 < 10, "answered at the deadline, not an hour later"
    assert not old.is_alive(), "the hung child was killed, not abandoned"
    # The next clip needs no human: a fresh child (new pid) answers it.
    assert child.run(_wav(tmp_path, "b.wav"), 20) == "heard:b.wav"
    assert child.meta()["pid"] != first and child.starts == 2


def test_a_crash_on_a_clip_is_decoder_crashed_with_its_exit_code_then_recovers(
    make_child, tmp_path
):
    child = make_child()
    child.start()
    with pytest.raises(wpp.PathJobError) as e:
        child.run(_wav(tmp_path, "crash.wav"), 20)
    assert _code(e) == "decoder_crashed" and "exit code 7" in str(e.value)
    assert child.run(_wav(tmp_path, "b.wav"), 20) == "heard:b.wav"


@POSIX_ONLY
def test_a_child_killed_from_outside_is_decoder_killed_then_recovers(make_child, tmp_path):
    child = make_child()
    child.start()
    with pytest.raises(wpp.PathJobError) as e:
        child.run(_wav(tmp_path, "killed.wav"), 20)
    assert _code(e) == "decoder_killed"
    assert child.run(_wav(tmp_path, "b.wav"), 20) == "heard:b.wav"


def test_a_backend_error_is_that_clips_error_and_the_child_stays_up(make_child, tmp_path):
    child = make_child()
    pid = child.start()["pid"]
    with pytest.raises(RuntimeError, match="ValueError: model exploded"):
        child.run(_wav(tmp_path, "boom.wav"), 20)
    assert child.run(_wav(tmp_path, "a.wav"), 20) == "heard:a.wav"
    assert child.meta()["pid"] == pid and child.starts == 1, "no restart for a clip error"


def test_busy_is_visible_from_another_thread_while_a_clip_runs(make_child, tmp_path):
    child = make_child()
    child.start()
    assert child.busy() == (0.0, None)
    seen = {}
    t = threading.Thread(
        target=lambda: seen.setdefault("r", _swallow(child.run, _wav(tmp_path, "slow.wav"), 30))
    )
    t.start()
    deadline = time.monotonic() + 10
    while child.busy()[1] is None and time.monotonic() < deadline:
        time.sleep(0.01)
    secs, limit = child.busy()
    t.join(20)
    assert limit == 30 and secs >= 0
    assert child.busy() == (0.0, None), "cleared when the clip is done"
    assert seen["r"] == "heard:slow.wav"


def _swallow(fn, *a):
    try:
        return fn(*a)
    except Exception as e:  # pragma: no cover
        return e


def test_busy_clears_after_a_timeout(make_child, tmp_path):
    child = make_child()
    child.start()
    with pytest.raises(wpp.PathJobError):
        child.run(_wav(tmp_path, "hang.wav"), 0.5)
    assert child.busy() == (0.0, None)


# ── a child that cannot start ────────────────────────────────────────────────


def test_a_model_that_cannot_load_is_a_start_fault_with_the_reason(make_child):
    child = make_child({"load": "fatal"})
    with pytest.raises(wpp.ChildFaultError) as e:
        child.start()
    assert e.value.starting is True
    assert "could not load its model" in str(e.value) and "could not load the model" in str(e.value)
    assert "could not load the model" in child.start_error()


def test_a_child_that_exits_before_ready_reports_its_exit_code(make_child):
    child = make_child({"load": "exit"})
    with pytest.raises(wpp.ChildFaultError, match=r"exited while starting \(exit code 5\)"):
        child.start()


def test_a_child_that_is_never_ready_is_killed_and_reported(make_child):
    child = make_child({"load": "hang"}, ready_timeout_s=1.5)
    with pytest.raises(wpp.ChildFaultError, match="not ready after"):
        child.start()
    assert not child.alive(), "the child that never became ready was killed"


def test_a_start_fault_expires_so_a_refused_worker_is_tried_again(make_child, tmp_path):
    marker = tmp_path / "model-missing"
    marker.write_text("x")
    child = make_child({"fatal_if_exists": str(marker)}, fault_ttl_s=0.5)
    with pytest.raises(wpp.ChildFaultError):
        child.start()
    assert child.start_error() is not None
    time.sleep(0.6)
    assert child.start_error() is None, "expired: the client may send the request that restarts it"
    marker.unlink()
    assert child.run(_wav(tmp_path, "a.wav"), 20) == "heard:a.wav", "and it does restart"
    assert child.start_error() is None


@POSIX_ONLY
def test_an_orphaned_child_exits_when_its_parent_dies_without_cleanup(tmp_path):
    script = tmp_path / "parent.py"
    script.write_text(
        textwrap.dedent(
            f"""
            import os, sys
            sys.path[:0] = [{str(HERE)!r}, {str(HERE.parent)!r}]
            import whisper_decode_child as wdc
            if __name__ == "__main__":
                c = wdc.DecodeChild({FACTORY!r}, {{}}, ready_timeout_s=60)
                print(c.start()["pid"], flush=True)
                os._exit(0)  # no atexit, no shutdown: the child is orphaned
            """
        )
    )
    out = subprocess.run([sys.executable, str(script)], capture_output=True, text=True, timeout=120)
    pid = int(out.stdout.strip().splitlines()[-1])
    deadline = time.monotonic() + 3 * wdc.ORPHAN_POLL_S + 10
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.2)
    os.kill(pid, 9)
    pytest.fail("an orphaned decode child did not exit")


# ── the worker on top of real children ───────────────────────────────────────


@pytest.fixture
def worker(monkeypatch, tmp_path):
    """The real HTTP app over a real engine of `n` spawned fake-backend children,
    with the clip extractor stubbed to hand back the WAV named by the job."""
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-child-1")
    monkeypatch.setenv(wpp.REQUIRE_MOUNT_ENV, "0")
    share = tmp_path / "share"
    share.mkdir()
    monkeypatch.setenv(wpp.PATH_MAP_ENV, f"/lib/Gotham={share}")
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FLOOR_S", 1.0)
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FACTOR", 0.0)
    made = []

    def build(n=1, arg=None, **kw):
        kw.setdefault("ready_timeout_s", 60.0)
        children = [wdc.DecodeChild(FACTORY, arg or {}, **kw) for _ in range(n)]
        engine = ws.Engine(children)
        for c in children:
            c.start(60)
        app = ws.create_app(ws.ServerConfig(num_workers=n), engine)

        def extract(src, seconds):
            d = tmp_path / f"clip-{len(made)}-{pathlib.Path(src).stem}"
            d.mkdir()
            made.append(d)
            return str(d), _wav(d, pathlib.Path(src).stem + ".wav")

        app.state.pipeline.extract_clip = extract
        (share / "Wayne").mkdir(exist_ok=True)
        for name in ("hang", "crash", "ok", "boom", "killed"):
            (share / "Wayne" / f"{name}.m4b").write_bytes(b"x")
        return TestClient(app), engine

    engines = []
    orig_build = build

    def tracked(*a, **k):
        client, engine = orig_build(*a, **k)
        engines.append(engine)
        return client, engine

    yield tracked
    for e in engines:
        e.shutdown()


def _job(client, name):
    r = client.post(
        "/transcribe-path", json={"path": f"/lib/Gotham/Wayne/{name}.m4b", "seconds": 5}
    )
    assert r.status_code == 200, r.text
    return r.json()


def test_a_hung_decode_answers_transcribe_timeout_then_the_next_job_works(worker):
    client, _ = worker()
    res = _job(client, "hang")
    assert res["error"].startswith("transcribe_timeout:") and res["text"] == ""
    ok = _job(client, "ok")
    assert ok == {"text": "heard:ok.wav", "error": None}, "recovered with no restart by a human"


def test_a_crash_answers_decoder_crashed_through_the_http_surface(worker):
    client, _ = worker()
    assert _job(client, "crash")["error"].startswith("decoder_crashed:")
    assert _job(client, "ok")["error"] is None


def test_a_backend_error_is_a_whisper_error_not_a_decoder_code(worker):
    client, _ = worker()
    err = _job(client, "boom")["error"]
    assert err.startswith("whisper_error:") and "model exploded" in err


def test_health_answers_while_a_decode_hangs_and_reports_it_busy(worker):
    client, _ = worker()
    t = threading.Thread(target=lambda: _job(client, "hang"))
    t.start()
    try:
        deadline = time.monotonic() + 10
        body = {}
        while time.monotonic() < deadline:
            body = client.get("/health").json()
            if body["busy_limit_seconds"] is not None:
                break
            time.sleep(0.05)
        assert body["busy_limit_seconds"] == 1.0 and body["busy_seconds"] >= 0
        assert body["device"] == "cpu" and body["model"] == "fake"
    finally:
        t.join(20)
    assert client.get("/health").json()["busy_limit_seconds"] is None


def test_one_hung_child_does_not_block_the_other_workers(worker):
    client, engine = worker(n=2, arg=None)
    hung = threading.Thread(target=lambda: _job(client, "hang"))
    hung.start()
    deadline = time.monotonic() + 10
    while engine.busy()[1] is None and time.monotonic() < deadline:
        time.sleep(0.02)
    t0 = time.monotonic()
    assert _job(client, "ok")["error"] is None
    assert time.monotonic() - t0 < 0.9, "served by the free child while the other hangs"
    hung.join(20)


def test_a_restart_that_cannot_load_is_worker_misconfigured_and_visible_in_health(worker, tmp_path):
    marker = tmp_path / "gone"
    client, _ = worker(arg={"fatal_if_exists": str(marker)})
    marker.write_text("x")  # from now on a restart cannot load its model
    assert _job(client, "hang")["error"].startswith("transcribe_timeout:")
    err = _job(client, "ok")["error"]
    assert err.startswith("worker_misconfigured:") and "could not load its model" in err
    assert "child_start_error" in client.get("/health").json()
    marker.unlink()
    assert _job(client, "ok")["error"] is None, "and it comes back by itself once it can load"
    assert "child_start_error" not in client.get("/health").json()


def test_an_upload_that_hangs_is_killed_at_a_limit_from_its_own_length(
    worker, monkeypatch, tmp_path
):
    client, _ = worker()
    # floor 0.5 s, factor 1: a 2 s upload gets 2 s, which is its length, not the floor.
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FLOOR_S", 0.5)
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FACTOR", 1.0)
    wav = pathlib.Path(_wav(tmp_path, "up.wav", seconds=2.0)).read_bytes() + b"HANGME"
    t0 = time.monotonic()
    r = client.post("/transcribe", files={"file": ("up.wav", wav, "audio/wav")})
    took = time.monotonic() - t0
    assert r.status_code == 200
    assert r.json()["error"].startswith("transcribe_timeout:"), r.json()
    assert 1.8 < took < 15, f"killed at its length-derived limit (2 s), took {took:.1f}s"
    ok = client.post("/transcribe", files={"file": ("up.wav", wav[:-6], "audio/wav")})
    assert ok.json()["error"] is None and ok.json()["text"].startswith("heard:"), "recovered"
