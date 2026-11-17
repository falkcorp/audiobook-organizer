# file: scripts/tests/test_whisper_decode_child.py
# version: 2.0.2
# guid: 2c9d7e15-6a48-4f3b-8e01-d5b7a39c6f82
# last-edited: 2026-11-17
#
# Tests for scripts/whisper_decode_child.py, the killable decode child of the
# non-Mac Whisper worker, with REAL spawned child processes and a fake backend
# (tests/whisper_fake_backend.py) that hangs, crashes, exits or fails to load, plus
# the worker's HTTP surface running on top of it. No model, no GPU, no Windows
# needed; "spawn" is the only start method on Windows, so this is the same code path
# there. POSIX-only cases (SIGKILL, orphan detection) skip on Windows.

import contextlib
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


def test_one_hung_child_does_not_block_the_other_workers(worker, monkeypatch):
    client, engine = worker(n=2, arg=None)
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FLOOR_S", 60.0)  # the hang outlives the assertion
    hung = threading.Thread(target=lambda: _job(client, "hang"))
    hung.start()
    deadline = time.monotonic() + 10
    while engine.busy()[1] is None and time.monotonic() < deadline:
        time.sleep(0.02)
    t0 = time.monotonic()
    assert _job(client, "ok")["error"] is None
    assert time.monotonic() - t0 < 30, "served by the free child while the other hangs"
    engine.shutdown()  # kills the hung child now rather than at its 60 s deadline
    hung.join(30)


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
    assert took >= 1.8 and took < 60, f"killed at its length-derived limit (2 s), took {took:.1f}s"
    ok = client.post("/transcribe", files={"file": ("up.wav", wav[:-6], "audio/wav")})
    assert ok.json()["error"] is None and ok.json()["text"].startswith("heard:"), "recovered"


# ── round 3: the startup pin, the channel guard, signals, parent death ───────

import signal  # noqa: E402

PIN_ARG = {"cfg": {"device": "auto", "model": None, "compute_type": None}}


def _pinned_children(tmp_path, n=1, **extra):
    """n DecodeChildren of make_pinned sharing one StartPin and one load counter."""
    arg = {**PIN_ARG, "counter": str(tmp_path / "loads"), "cuda_gone": str(tmp_path / "gone")}
    arg.update(extra)
    pin = wdc.StartPin()
    kids = [
        wdc.DecodeChild(
            "whisper_fake_backend:make_pinned",
            arg,
            ready_timeout_s=60.0,
            pin=pin,
            pin_arg=ws._pin_arg,
        )
        for _ in range(n)
    ]
    return pin, kids


def test_without_a_pin_a_restart_resolves_auto_again_and_changes_model(tmp_path):
    # The control that shows what the pin prevents: the fake's second `auto` load
    # lands on cpu/base.en, as a real restart whose CUDA load fails would.
    kid = wdc.DecodeChild(
        "whisper_fake_backend:make_pinned",
        {**PIN_ARG, "counter": str(tmp_path / "loads")},
        ready_timeout_s=60.0,
    )
    try:
        assert kid.start()["model"] == "large-v2"
        with pytest.raises(wpp.PathJobError):
            kid.run(_wav(tmp_path, "hang.wav"), 0.5)
        assert kid.start()["model"] == "base.en", "unpinned: the restart changed model"
    finally:
        kid.shutdown()


def test_a_restart_is_pinned_to_the_first_resolution(tmp_path):
    pin, (kid,) = _pinned_children(tmp_path)
    try:
        first = kid.start()
        assert (first["model"], first["device"], first["compute_type"]) == (
            "large-v2",
            "cuda",
            "float16",
        )
        with pytest.raises(wpp.PathJobError):
            kid.run(_wav(tmp_path, "hang.wav"), 0.5)
        assert kid.run(_wav(tmp_path, "a.wav"), 20) == "heard:a.wav"
        again = kid.meta()
        assert again["loads"] == 2, "it did restart"
        assert (again["model"], again["device"], again["compute_type"]) == (
            "large-v2",
            "cuda",
            "float16",
        ), "and kept the pinned model, device and compute type"
        assert pin.values() == {"model": "large-v2", "device": "cuda", "compute_type": "float16"}
    finally:
        kid.shutdown()


def test_a_restart_that_cannot_load_the_pinned_model_is_a_start_fault_not_a_fallback(tmp_path):
    pin, (kid,) = _pinned_children(tmp_path)
    try:
        kid.start()
        (tmp_path / "gone").write_text("x")  # CUDA can no longer be used
        with pytest.raises(wpp.PathJobError):
            kid.run(_wav(tmp_path, "hang.wav"), 0.5)
        with pytest.raises(wpp.ChildFaultError) as e:
            kid.run(_wav(tmp_path, "a.wav"), 20)
        assert e.value.starting and "CUDA out of memory" in str(e.value)
        assert pin.values()["model"] == "large-v2", "the pin is not rewritten by the failure"
    finally:
        kid.shutdown()


def test_a_child_that_reports_other_than_the_pin_is_refused_even_if_it_loaded(tmp_path):
    # A factory that disregards the explicit values it is given (ignore_pin): the
    # parent compares what the child REPORTS with the pin and refuses it.
    pin, (kid,) = _pinned_children(tmp_path, ignore_pin=True)
    try:
        kid.start()
        with pytest.raises(wpp.PathJobError):
            kid.run(_wav(tmp_path, "hang.wav"), 0.5)
        with pytest.raises(wpp.ChildFaultError, match="pinned to"):
            kid.run(_wav(tmp_path, "a.wav"), 20)
        assert not kid.alive(), "the mismatching child was killed"
    finally:
        kid.shutdown()


def test_with_several_workers_all_serve_the_pin_or_the_worker_refuses_to_start(tmp_path):
    pin, kids = _pinned_children(tmp_path, n=2)
    try:
        metas = [k.start() for k in kids]
        assert {(m["model"], m["device"], m["compute_type"]) for m in metas} == {
            ("large-v2", "cuda", "float16")
        }, "child 2 was started with the pinned values, so it did not fall back"
        engine = ws.Engine(kids, pin)
        assert engine.meta()["model"] == "large-v2"
    finally:
        for k in kids:
            k.shutdown()
    (tmp_path / "second").mkdir()
    pin, kids = _pinned_children(tmp_path / "second", n=2)
    try:
        kids[0].start()
        (tmp_path / "second" / "gone").write_text("x")  # child 2 cannot get the GPU copy
        with pytest.raises(wpp.ChildFaultError, match="CUDA out of memory"):
            kids[1].start()
    finally:
        for k in kids:
            k.shutdown()


def test_engine_meta_is_the_pin_even_when_no_child_is_up(tmp_path):
    # /health must keep showing what the worker serves while its child is down or
    # restarting, not an empty meta that would drop the fingerprint.
    pin, (kid,) = _pinned_children(tmp_path)
    full = {"model": "large-v2", "device": "cuda", "compute_type": "float16", "pid": 1}
    assert pin.check_and_record(full) is None
    engine = ws.Engine([kid], pin)  # kid was never started
    assert not kid.alive()
    assert engine.meta()["model"] == "large-v2" and engine.meta()["device"] == "cuda"


def test_health_reports_the_pinned_resolution_even_after_a_restart(tmp_path, monkeypatch):
    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-pin-1")
    pin, (kid,) = _pinned_children(tmp_path)
    engine = ws.Engine([kid], pin)
    try:
        kid.start()
        app = ws.create_app(ws.ServerConfig(), engine)
        with pytest.raises(wpp.PathJobError):
            kid.run(_wav(tmp_path, "hang.wav"), 0.5)
        h = TestClient(app).get("/health").json()
        assert h["model"] == "large-v2" and h["device"] == "cuda" and h["compute_type"] == "float16"
        assert h["decode_fingerprint"] == ws.decode_fingerprint("large-v2", 8, "float16", True)
    finally:
        engine.shutdown()


@pytest.fixture
def one_second_floor(monkeypatch):
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FLOOR_S", 1.0)
    monkeypatch.setattr(ws, "CLIP_TIMEOUT_FACTOR", 0.0)


def test_build_engine_runs_the_production_wiring_with_a_fake_model(tmp_path, one_second_floor):
    # build_engine -> asdict(cfg) pickled to a spawned child -> BACKEND_FACTORY
    # (whisper_server:child_backend) -> ServerConfig(**) -> load_transcriber, with only
    # the model class replaced through the transcriber_factory seam.
    cfg = ws.ServerConfig(device="cpu", num_workers=2)
    engine = ws.build_engine(cfg, transcriber_factory="whisper_fake_backend:FakeTranscriber")
    try:
        m = engine.meta()
        assert m["device"] == "cpu" and m["model"] == "base.en" and m["compute_type"] == "int8"
        assert [r.meta()["pid"] for r in engine.runners][0] != os.getpid()
        assert engine.run(_wav(tmp_path, "a.wav"), 1.0) == "heard:a.wav"
        with pytest.raises(wpp.PathJobError) as e:
            engine.run(_wav(tmp_path, "hang.wav"), 0.1)  # 0.1 s of audio: the 1 s floor decides
        assert _code(e) == "transcribe_timeout"
    finally:
        engine.shutdown()


def test_a_poll_that_raises_between_send_and_receive_kills_the_child_not_desyncs_it(
    make_child, tmp_path
):
    child = make_child()
    child.start()
    old = child._proc
    real_poll = child._conn.poll
    calls = []

    def poll(timeout=None):
        calls.append(1)
        if len(calls) == 1:
            time.sleep(0.3)  # the child is already working on clip a
            raise OverflowError("timeout too large")
        return real_poll(timeout)

    child._conn.poll = poll
    with pytest.raises(OverflowError):
        child.run(_wav(tmp_path, "slow.wav"), 20)
    assert not old.is_alive(), "the child holding an unread answer was killed"
    # The stale "heard:slow.wav" must not come back as clip b's answer.
    assert child.run(_wav(tmp_path, "b.wav"), 20) == "heard:b.wav"
    assert child.run(_wav(tmp_path, "c.wav"), 20) == "heard:c.wav"


def test_a_reply_to_a_different_request_is_refused(make_child, tmp_path):
    child = make_child()
    child.start()
    real_recv = child._conn.recv
    child._conn.recv = lambda: {**real_recv(), "id": 999}
    with pytest.raises(wpp.PathJobError) as e:
        child.run(_wav(tmp_path, "a.wav"), 20)
    assert _code(e) == "decoder_crashed" and "different request" in str(e.value)
    assert not child.alive()
    assert child.run(_wav(tmp_path, "b.wav"), 20) == "heard:b.wav"


@pytest.mark.parametrize("bad", ["nan", "inf", "-1", "0", "abc", "1e99"])
def test_timeout_environment_variables_are_validated_at_startup(bad):
    out = subprocess.run(
        [sys.executable, "-c", "import whisper_server"],
        capture_output=True,
        text=True,
        cwd=str(HERE.parent),
        env={**os.environ, "WHISPER_TRANSCRIBE_MIN_TIMEOUT_S": bad},
        timeout=60,
    )
    assert out.returncode != 0
    assert "WHISPER_TRANSCRIBE_MIN_TIMEOUT_S" in out.stderr + out.stdout


def test_the_computed_deadline_has_a_floor_and_a_hard_cap():
    f = ws.Engine.clip_limit_s
    assert f(None) == ws.CLIP_TIMEOUT_FLOOR_S
    assert f(10.0) == max(ws.CLIP_TIMEOUT_FLOOR_S, 8 * 10.0)
    assert f(1e9) == ws.CLIP_TIMEOUT_CAP_S == 3600.0, "a huge upload cannot earn 69 hours"


@POSIX_ONLY
def test_ctrl_c_does_not_reach_the_decode_and_the_child_has_its_own_session(make_child, tmp_path):
    # A process started from a background shell job inherits SIGINT as IGNORED, and
    # Python then never installs its KeyboardInterrupt handler, which would make this
    # test pass for the wrong reason. Start the child from a parent with the default
    # disposition so the child's own signal.signal(SIGINT, SIG_IGN) is what is tested.
    previous = signal.signal(signal.SIGINT, signal.SIG_DFL)
    try:
        child = make_child()
        pid = child.start()["pid"]
    finally:
        signal.signal(signal.SIGINT, previous)
    assert os.getsid(pid) != os.getsid(0), "its own session: a terminal's SIGINT misses it"
    out = {}

    def run():
        out["r"] = _swallow(child.run, _wav(tmp_path, "slow.wav"), 20)

    t = threading.Thread(target=run)
    t.start()
    time.sleep(0.1)
    os.kill(pid, signal.SIGINT)  # even delivered directly, it is ignored
    t.join(20)
    assert out["r"] == "heard:slow.wav", "the in-flight clip was not failed by Ctrl-C"
    assert child.alive()


def _pid_alive(pid):
    if sys.platform == "win32":
        r = subprocess.run(
            ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
            capture_output=True,
            text=True,
        )
        return f'"{pid}"' in r.stdout
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def _kill_pid(pid):
    if sys.platform == "win32":
        subprocess.run(["taskkill", "/F", "/PID", str(pid)], capture_output=True)
    else:
        with contextlib.suppress(ProcessLookupError):
            os.kill(pid, 9)


def _parent_dies_mid_decode(tmp_path, clip, disable_job=False):
    """Run a parent that starts a decode child, puts it on `clip` and dies without any
    cleanup. Returns the pid of the REAL child interpreter."""
    wav = _wav(tmp_path, clip)
    script = tmp_path / "parent.py"
    script.write_text(
        textwrap.dedent(
            f"""
            import os, sys, threading, time
            sys.path[:0] = [{str(HERE)!r}, {str(HERE.parent)!r}]
            import whisper_decode_child as wdc
            if __name__ == "__main__":
                if {disable_job!r}:
                    wdc._assign_to_job = lambda pid: None
                c = wdc.DecodeChild({FACTORY!r}, {{}}, ready_timeout_s=120)
                pid = c.start()["pid"]
                threading.Thread(target=lambda: c.run({wav!r}, 3000), daemon=True).start()
                time.sleep(1.5)
                print(pid, flush=True)
                os._exit(0)  # no atexit, no shutdown, no pipe close courtesy
            """
        )
    )
    # stdout goes to a FILE, not a pipe: on Windows the decode child inherits the
    # parent's pipe handles, so after the parent dies the child still holds the write
    # end and a pipe reader would wait for an EOF that never comes (subprocess.run
    # then hangs even past its own timeout).
    out_path = tmp_path / "parent.out"
    with open(out_path, "w") as sink:
        subprocess.run(
            [sys.executable, str(script)], stdout=sink, stderr=subprocess.STDOUT, timeout=180
        )
    return int(out_path.read_text().strip().splitlines()[-1])


def _wait_gone(pid, seconds):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if not _pid_alive(pid):
            return True
        time.sleep(0.25)
    return False


def test_a_parent_killed_mid_decode_takes_its_decode_child_with_it(tmp_path):
    pid = _parent_dies_mid_decode(tmp_path, "hang.wav")
    try:
        assert _wait_gone(pid, 3 * wdc.ORPHAN_POLL_S + 15), "the decoding child outlived its parent"
    finally:
        _kill_pid(pid)


@pytest.mark.skipif(sys.platform != "win32", reason="Job Objects are Windows only")
def test_a_gil_holding_native_hang_is_still_killed_with_the_parent_by_the_job_object(tmp_path):
    # The orphan watch is a Python thread, which a native call that holds the GIL
    # starves; only the Job Object can reap this child.
    pid = _parent_dies_mid_decode(tmp_path, "gilhang.wav")
    try:
        assert _wait_gone(pid, 30), "the job object did not kill the GIL-holding child"
    finally:
        _kill_pid(pid)


@pytest.mark.skipif(sys.platform != "win32", reason="Job Objects are Windows only")
def test_control_without_the_job_object_a_gil_holding_child_survives_its_parent(tmp_path):
    # Proves the test above tests the job object and not something else.
    pid = _parent_dies_mid_decode(tmp_path, "gilhang.wav", disable_job=True)
    try:
        assert not _wait_gone(pid, 12), "survived: nothing else reaps it (that is the point)"
    finally:
        _kill_pid(pid)
