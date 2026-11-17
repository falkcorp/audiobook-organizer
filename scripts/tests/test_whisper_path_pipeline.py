# file: scripts/tests/test_whisper_path_pipeline.py
# version: 1.0.0
# guid: 5d2c8e17-9b43-4a06-8f1d-3e7a0c6b9d25
# last-edited: 2026-11-17
#
# Tests for scripts/whisper_path_pipeline.py, the path pipeline both Whisper
# workers share. The part that matters most is the path-safety boundary: the
# server sends a path as a string and this worker opens it, so every way a
# string can name a file outside the mapped roots has a test here.
#
# The lexical rules are parameterised on a path FLAVOR (posixpath / ntpath), so
# the Windows rules run on every CI host, not only on a Windows machine. What
# only a Windows filesystem can answer (realpath of a junction, a device name
# on disk) is NOT covered here and is listed as unverified in the PR.

import ntpath
import os
import pathlib
import posixpath
import sys
import unittest

try:
    import pytest
    from fastapi import FastAPI
    from fastapi.testclient import TestClient
except ImportError as exc:  # pragma: no cover - dependency-gated
    raise unittest.SkipTest(f"whisper pipeline tests need fastapi+httpx+pytest ({exc})")

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import whisper_path_pipeline as wpp  # noqa: E402

PATH_MAP_ENV = wpp.PATH_MAP_ENV


def _code(exc_info) -> str:
    return exc_info.value.code


# ── Windows component validation (lexical, runs everywhere) ──────────────────


@pytest.mark.parametrize(
    "name",
    [
        "CON",
        "con",
        "NUL",
        "nul.m4b",
        "Nul.tar.gz",
        "PRN",
        "AUX.txt",
        "COM1",
        "com9.m4b",
        "LPT1",
        "lpt\u00b2.m4b",
        "COM1 .m4b",
        "CONIN$",
        "CONOUT$",
        "ends-with-dot.",
        "ends-with-space ",
        "a:stream",
        "C:",
        "back\\slash",
        "star*",
        "what?",
        'quote"',
        "lt<",
        "gt>",
        "pipe|",
        "ctl\x01char",
        "x" * 256,
    ],
)
def test_windows_refuses_a_component_it_cannot_address(name):
    assert wpp.component_problem(name, ntpath), name


@pytest.mark.parametrize(
    "name",
    ["Detective Comics #27.m4b", "CONSTANTINE.m4b", "nullable", "COM10", "Conan.m4b", "x" * 255],
)
def test_windows_allows_an_ordinary_component(name):
    assert wpp.component_problem(name, ntpath) is None


@pytest.mark.parametrize("name", ["CON", "NUL.m4b", "a\\b", "x.", "a:b", "x" * 400])
def test_posix_addresses_every_component_the_server_can_have(name):
    # POSIX keeps its long-standing behaviour: nothing here is refused, so the
    # Mac worker's answers do not change.
    assert wpp.component_problem(name, posixpath) is None


# ── containment, case rules and extended-length prefixes ─────────────────────


def test_windows_containment_is_case_insensitive_and_whole_component():
    assert wpp.is_within("X:\\Lib\\Books", "x:\\lib\\books\\a\\b.m4b", ntpath)
    assert wpp.is_within("X:\\Lib\\Books", "X:\\Lib\\Books", ntpath)
    assert not wpp.is_within("X:\\Lib\\Books", "X:\\Lib\\Books2\\a.m4b", ntpath)
    assert not wpp.is_within("X:\\Lib\\Books", "X:\\Lib", ntpath)
    assert not wpp.is_within("X:\\Lib\\Books", "Y:\\Lib\\Books\\a.m4b", ntpath)


def test_windows_containment_sees_through_extended_prefixes():
    assert wpp.is_within("X:\\Lib", "\\\\?\\X:\\lib\\a.m4b", ntpath)
    assert wpp.is_within("\\\\?\\X:\\Lib", "X:\\Lib\\a.m4b", ntpath)
    assert wpp.is_within("\\\\host\\share\\Lib", "\\\\?\\UNC\\HOST\\share\\lib\\a.m4b", ntpath)
    assert not wpp.is_within("\\\\host\\share", "\\\\host\\share2\\a.m4b", ntpath)


def test_a_drive_or_share_root_contains_its_files():
    assert wpp.is_within("X:\\", "X:\\a\\b.m4b", ntpath)
    assert wpp.is_within("\\\\host\\share", "\\\\host\\share\\a\\b.m4b", ntpath)


def test_posix_containment_is_case_sensitive_and_whole_component():
    assert wpp.is_within("/mnt/Lib", "/mnt/Lib/a/b.m4b", posixpath)
    assert not wpp.is_within("/mnt/Lib", "/mnt/lib/a/b.m4b", posixpath)
    assert not wpp.is_within("/mnt/Lib", "/mnt/Lib2/a.m4b", posixpath)


def test_extended_prefix_round_trip():
    assert wpp.strip_extended_prefix("\\\\?\\X:\\a") == "X:\\a"
    assert wpp.strip_extended_prefix("\\\\?\\UNC\\h\\s\\a") == "\\\\h\\s\\a"
    assert wpp.strip_extended_prefix("X:\\a") == "X:\\a"
    assert wpp.to_extended("X:\\a") == "\\\\?\\X:\\a"
    assert wpp.to_extended("\\\\h\\s\\a") == "\\\\?\\UNC\\h\\s\\a"
    assert wpp.to_extended("\\\\?\\X:\\a") == "\\\\?\\X:\\a"


# ── WHISPER_PATH_MAP parsing, both flavors ───────────────────────────────────


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("/lib/Gotham=X:\\Gotham", [("/lib/Gotham", "X:\\Gotham")]),
        ("/lib/Gotham=X:/Gotham/books", [("/lib/Gotham", "X:\\Gotham\\books")]),
        ("/lib=\\\\arkham\\audio\\lib", [("/lib", "\\\\arkham\\audio\\lib")]),
        ("/lib=X:\\", [("/lib", "X:\\")]),
        ("/lib=\\\\arkham\\audio", [("/lib", "\\\\arkham\\audio")]),
        ("/a=X:\\a, /a/b=X:\\b", [("/a/b", "X:\\b"), ("/a", "X:\\a")]),
    ],
)
def test_windows_map_accepts(raw, expected):
    assert wpp.parse_path_map(raw, ntpath) == expected


@pytest.mark.parametrize(
    "raw",
    [
        "/lib=/mnt/posix",  # a POSIX local root on Windows
        "/lib=relative\\dir",
        "/lib=X:relative",  # drive-relative: resolves against a per-drive cwd
        "/lib=\\rooted-no-drive",
        "/lib=\\\\?\\X:\\lib",  # the extended prefix is the worker's to add
        "/lib=\\\\.\\X:\\lib",
        "/lib=X:\\a\\..\\b",
        "/lib=X:/a/../b",
        "X:\\lib=/lib",  # the SERVER side is always POSIX
        "/lib/../x=X:\\x",
        "/=X:\\x",
        "/lib=",
        "=X:\\x",
        "/lib",
    ],
)
def test_windows_map_refuses(raw):
    with pytest.raises(ValueError):
        wpp.parse_path_map(raw, ntpath)


@pytest.mark.parametrize("raw", ["/lib=X:\\x", "/lib=relative", "/lib=/a/../b", "/lib=/", "/=/a"])
def test_posix_map_refuses_what_it_always_refused(raw):
    with pytest.raises(ValueError):
        wpp.parse_path_map(raw, posixpath)


# ── the lexical half of mapping, Windows flavor ──────────────────────────────


def _nt_pipeline(raw="/lib/Gotham=X:\\Gotham"):
    p = wpp.PathPipeline()
    p.pathmod = ntpath
    return p, wpp.parse_path_map(raw, ntpath)


def test_windows_split_maps_a_plain_path():
    p, pairs = _nt_pipeline()
    server, local, rest = p.split_server_path("/lib/Gotham/Wayne/Year One.m4b", pairs)
    assert (server, local, rest) == ("/lib/Gotham", "X:\\Gotham", ["Wayne", "Year One.m4b"])


@pytest.mark.parametrize(
    "path,code",
    [
        ("/lib/Gotham/..\\..\\secret.txt", "path_unmapped"),  # backslash traversal
        ("/lib/Gotham/a\\b.m4b", "path_unmapped"),
        ("/lib/Gotham/.. /x.m4b", "path_unmapped"),  # ".. " is ".." on Windows
        ("/lib/Gotham/../x.m4b", "path_unmapped"),
        ("/lib/Gotham/C:\\Windows\\x", "path_unmapped"),  # drive escape
        ("/lib/Gotham/C:/x", "path_unmapped"),
        ("/lib/Gotham/\\\\evil\\share\\x", "path_unmapped"),  # UNC escape
        ("/lib/Gotham/\\\\?\\C:\\x", "path_unmapped"),
        ("/lib/Gotham/CON", "path_unmapped"),
        ("/lib/Gotham/NUL.m4b", "path_unmapped"),
        ("/lib/Gotham/sub/com1.m4b", "path_unmapped"),
        ("/lib/Gotham/stream.m4b:hidden", "path_unmapped"),  # alternate data stream
        ("/lib/Gotham/trailing.", "path_unmapped"),
        ("/lib/GothamX/a.m4b", "path_unmapped"),  # sibling prefix
        ("/lib/gotham/a.m4b", "path_unmapped"),  # the SERVER side stays exact
        ("lib/Gotham/a.m4b", "path_unmapped"),
        ("X:\\Gotham\\a.m4b", "path_unmapped"),  # a local path is not a server path
        ("", "bad_request"),
        ("/lib/Gotham/a\x00.m4b", "bad_request"),
        (None, "bad_request"),
        (7, "bad_request"),
        ("/lib/Gotham/" + "a/" * (wpp.MAX_PATH_CHARS // 2), "bad_request"),
    ],
)
def test_windows_split_refuses(path, code):
    p, pairs = _nt_pipeline()
    with pytest.raises(wpp.PathJobError) as e:
        p.split_server_path(path, pairs)
    assert _code(e) == code, (path, e.value)


def test_the_same_names_are_ordinary_on_a_posix_worker():
    p = wpp.PathPipeline()
    p.pathmod = posixpath
    pairs = wpp.parse_path_map("/lib/Gotham=/mnt/g", posixpath)
    for name in ("CON", "NUL.m4b", "a\\b.m4b", "trailing.", "x:y"):
        assert p.split_server_path(f"/lib/Gotham/{name}", pairs)[2] == [name]


# ── the filesystem half, POSIX (real files, real symlinks) ───────────────────


@pytest.fixture
def lib(tmp_path, monkeypatch):
    monkeypatch.setenv(wpp.REQUIRE_MOUNT_ENV, "0")
    root = tmp_path / "share"
    (root / "books" / "Wayne").mkdir(parents=True)
    (root / "books" / "Wayne" / "Year One.m4b").write_bytes(b"x")
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "secret.m4b").write_bytes(b"x")
    monkeypatch.setenv(PATH_MAP_ENV, f"/lib/Gotham={root}")
    return wpp.PathPipeline(), root, outside


def _job(path, seconds=90):
    return {"path": path, "seconds": seconds}


def test_a_mapped_file_resolves_to_its_real_path(lib):
    p, root, _ = lib
    src, seconds = p.prepare_path_job(_job("/lib/Gotham/books/Wayne/Year One.m4b"))
    assert src == os.path.realpath(root / "books" / "Wayne" / "Year One.m4b")
    assert seconds == 90


@pytest.mark.parametrize(
    "path",
    [
        "/lib/Gotham/books/../../outside/secret.m4b",
        "/lib/Gotham/../outside/secret.m4b",
        "/lib/Gotham/books/Wayne/../../../outside/secret.m4b",
        "/etc/passwd",
        "/lib/GothamX/books/a.m4b",
        "/lib/Gotham2/x",
        "/lib",
        "lib/Gotham/books/Wayne/Year One.m4b",
        "./lib/Gotham/books/Wayne/Year One.m4b",
        "//etc/passwd",
    ],
)
def test_posix_refuses_traversal_and_absolute_escape(lib, path):
    p, _, _ = lib
    with pytest.raises(wpp.PathJobError) as e:
        p.prepare_path_job(_job(path))
    assert _code(e) == "path_unmapped", path


def test_posix_refuses_a_symlink_that_leaves_the_root(lib):
    p, root, outside = lib
    (root / "books" / "escape").symlink_to(outside)
    (root / "books" / "file-escape.m4b").symlink_to(outside / "secret.m4b")
    for rel in ("escape/secret.m4b", "file-escape.m4b"):
        with pytest.raises(wpp.PathJobError) as e:
            p.prepare_path_job(_job(f"/lib/Gotham/books/{rel}"))
        assert _code(e) == "path_unmapped"
        assert "outside" in str(e.value)


def test_posix_follows_a_symlink_that_stays_inside(lib):
    p, root, _ = lib
    (root / "books" / "alias").symlink_to(root / "books" / "Wayne")
    src, _ = p.prepare_path_job(_job("/lib/Gotham/books/alias/Year One.m4b"))
    assert src == os.path.realpath(root / "books" / "Wayne" / "Year One.m4b")


def test_posix_refuses_a_symlink_to_a_sibling_whose_name_extends_the_root(lib, tmp_path):
    # share -> share-evil: a string-prefix test would call it inside.
    p, root, _ = lib
    evil = tmp_path / "share-evil"
    evil.mkdir()
    (evil / "x.m4b").write_bytes(b"x")
    (root / "books" / "evil").symlink_to(evil)
    with pytest.raises(wpp.PathJobError) as e:
        p.prepare_path_job(_job("/lib/Gotham/books/evil/x.m4b"))
    assert _code(e) == "path_unmapped"


def test_posix_device_names_are_ordinary_files(lib):
    p, root, _ = lib
    (root / "books" / "CON.m4b").write_bytes(b"x")
    src, _ = p.prepare_path_job(_job("/lib/Gotham/books/CON.m4b"))
    assert src.endswith("CON.m4b")


def test_a_lost_root_is_unmapped_not_a_verdict(lib):
    p, root, _ = lib
    for child in list(root.iterdir()):
        for sub in list(child.rglob("*"))[::-1]:
            sub.unlink() if sub.is_file() or sub.is_symlink() else sub.rmdir()
        child.rmdir()
    root.rmdir()
    with pytest.raises(wpp.PathJobError) as e:
        p.prepare_path_job(_job("/lib/Gotham/books/Wayne/Year One.m4b"))
    assert _code(e) == "path_unmapped"
    assert "not readable" in str(e.value)


def test_a_missing_file_under_a_readable_root_is_not_found_at_extraction(lib):
    p, _, _ = lib
    src, seconds = p.prepare_path_job(_job("/lib/Gotham/books/Wayne/Nope.m4b"))
    with pytest.raises(wpp.PathJobError) as e:
        p.extract_clip(src, seconds)
    assert _code(e) == "not_found"


@pytest.mark.parametrize("seconds", [0, -1, 601, 1.5, "90", None, True])
def test_seconds_outside_one_to_six_hundred_are_refused(lib, seconds):
    p, _, _ = lib
    with pytest.raises(wpp.PathJobError) as e:
        p.prepare_path_job({"path": "/lib/Gotham/books/Wayne/Year One.m4b", "seconds": seconds})
    assert _code(e) == "bad_request"


def test_mapped_root_of_sees_the_configured_and_the_real_spelling(lib, tmp_path):
    p, root, _ = lib
    link = tmp_path / "link"
    link.symlink_to(root)
    os.environ[PATH_MAP_ENV] = f"/lib/Gotham={link}"
    real_file = os.path.realpath(root / "books" / "Wayne" / "Year One.m4b")
    assert p.mapped_root_of(real_file) == str(link)
    assert p.mapped_root_of(str(link / "books")) == str(link)
    assert p.mapped_root_of(str(tmp_path / "share-evil" / "x")) is None


# ── limits over HTTP, and refused jobs never reach extraction ────────────────


@pytest.fixture
def http(lib):
    p, root, outside = lib
    cut: list[str] = []

    def fake_extract(src, seconds):
        cut.append(src)
        raise wpp.PathJobError(wpp.ERR_NOT_FOUND, "stub")

    p.extract_clip = fake_extract
    app = FastAPI()
    wpp.install_path_routes(
        app, lambda jobs, deadline: p.run_path_jobs(jobs, deadline, lambda w: "")
    )
    return TestClient(app), cut, root, outside


def test_refused_jobs_never_reach_extraction(http):
    client, cut, root, outside = http
    (root / "books" / "escape").symlink_to(outside)
    jobs = {
        "traversal": _job("/lib/Gotham/../outside/secret.m4b"),
        "absolute": _job("/etc/passwd"),
        "symlink": _job("/lib/Gotham/books/escape/secret.m4b"),
        "prefix": _job("/lib/GothamX/a.m4b"),
        "ok": _job("/lib/Gotham/books/Wayne/Year One.m4b"),
    }
    res = client.post("/transcribe-path-batch", json={"jobs": jobs}).json()["results"]
    assert [k for k in jobs if res[k]["error"].startswith("path_unmapped:")] == [
        "traversal",
        "absolute",
        "symlink",
        "prefix",
    ]
    assert len(cut) == 1 and cut[0].endswith("Year One.m4b")


def test_too_many_jobs_is_422(http):
    client, cut, *_ = http
    jobs = {
        str(i): _job("/lib/Gotham/books/Wayne/Year One.m4b")
        for i in range(wpp.MAX_JOBS_PER_REQUEST + 1)
    }
    r = client.post("/transcribe-path-batch", json={"jobs": jobs})
    assert r.status_code == 422 and cut == []


def test_the_job_limit_itself_is_accepted(http):
    client, cut, *_ = http
    jobs = {str(i): _job("/lib/Gotham/nope/x.m4b") for i in range(wpp.MAX_JOBS_PER_REQUEST)}
    r = client.post("/transcribe-path-batch", json={"jobs": jobs})
    assert r.status_code == 200 and len(r.json()["results"]) == wpp.MAX_JOBS_PER_REQUEST


def test_an_oversized_key_is_422(http):
    client, *_ = http
    r = client.post(
        "/transcribe-path-batch",
        json={"jobs": {"k" * (wpp.MAX_KEY_CHARS + 1): _job("/lib/Gotham/x")}},
    )
    assert r.status_code == 422


def test_an_oversized_path_is_a_per_job_bad_request(http):
    client, cut, *_ = http
    big = "/lib/Gotham/" + "a" * wpp.MAX_PATH_CHARS
    res = client.post("/transcribe-path-batch", json={"jobs": {"k": _job(big)}}).json()["results"]
    assert res["k"]["error"].startswith("bad_request:") and cut == []


def test_a_declared_oversized_body_is_413(http):
    client, cut, *_ = http
    r = client.post(
        "/transcribe-path-batch",
        content=b"{" + b" " * (wpp.MAX_BODY_BYTES + 1) + b"}",
        headers={"content-type": "application/json"},
    )
    assert r.status_code == 413 and cut == []


def test_a_chunked_oversized_body_is_413(http):
    client, cut, *_ = http

    def chunks():
        yield b'{"jobs": {"k": {"path": "/lib/Gotham/x", "pad": "'
        for _ in range(wpp.MAX_BODY_BYTES // 65536 + 2):
            yield b"a" * 65536
        yield b'"}}}'

    r = client.post(
        "/transcribe-path-batch", content=chunks(), headers={"content-type": "application/json"}
    )
    assert r.status_code == 413 and cut == []


def test_the_body_limit_does_not_apply_to_other_routes(http):
    client, *_ = http
    assert client.post("/nonexistent", content=b"a" * (wpp.MAX_BODY_BYTES + 1)).status_code == 404


def test_a_body_under_the_limit_still_works(http):
    client, *_ = http
    r = client.post("/transcribe-path", json=_job("/lib/Gotham/books/Wayne/Year One.m4b"))
    assert r.status_code == 200 and r.json()["error"].startswith("not_found:")


# ── machine identity: the Windows source ─────────────────────────────────────


def test_machine_id_override_still_wins_and_hashes(monkeypatch):
    import hashlib

    import whisper_machine_id as wmi

    monkeypatch.setenv("WHISPER_MACHINE_ID", "gotham-cpu-1")
    ident = wmi.MachineIdentity()
    assert ident.compute() == hashlib.sha256(b"override:gotham-cpu-1").hexdigest()[:16]


def test_machine_id_uses_the_windows_machine_guid_when_nothing_else_answers(monkeypatch, tmp_path):
    import hashlib

    import whisper_machine_id as wmi

    monkeypatch.delenv("WHISPER_MACHINE_ID", raising=False)
    monkeypatch.setattr(sys, "platform", "win32")
    ident = wmi.MachineIdentity()
    ident.id_file = str(tmp_path / "absent")
    monkeypatch.setattr(ident, "windows_machine_guid", lambda: "0000-BATCAVE")
    assert ident.compute() == hashlib.sha256(b"machine-guid:0000-BATCAVE").hexdigest()[:16]
    monkeypatch.setattr(ident, "windows_machine_guid", lambda: None)
    assert ident.compute() is None, "no stable source means no id, never the hostname"


def test_windows_machine_guid_is_none_without_winreg():
    import whisper_machine_id as wmi

    if sys.platform != "win32":
        assert wmi.MachineIdentity().windows_machine_guid() is None
