#!/usr/bin/env python3
# file: scripts/r2corpus/write_r0_corpus.py
# version: 1.2.0
# guid: 1d61ab28-ed55-47d1-bb8a-642fbeb8b343
# last-edited: 2026-11-10
"""Write R2's fixture corpus, stage 1, with the R0 release binary itself (F2-12).

09 §9.3 item 1: the corpus every R2 cutover PR migrates is written **by the R0
binary**, driven only through its own API (HTTP routes, and the CLI flags, env and
config file it reads). This script:

1. reads the R0 tag from ``ci/r0_release.txt`` (one non-comment line),
2. fetches that tag's linux/amd64 release asset (or takes ``--binary``),
3. lays out a fixed work directory (``/tmp/r2corpus`` by default: the absolute
   paths R0 stores in its rows are this directory's, never a real install's),
4. boots R0 on a fresh store, runs every case of ``CASES`` in order, stops R0
   cleanly (SIGTERM, exit 0), deletes the one-boot credential files,
5. scans the result for leaked secrets and host identity, copies the store
   directory into ``internal/books/testdata/r2corpus/stage1`` and writes
   ``manifest.json``.

Regenerating is a manual act: nothing is written without ``--regenerate``. CI
only reads the committed corpus. ``--dry-run`` prints every API call in order
without a binary (the tests use it).

The boot spec (layout, argv, env, files) lives here and ``record_r0_answers.py``
imports it, so the answers are recorded under exactly the boot the corpus was
written with.

Run on a linux/amd64 host (the binary is the linux/amd64 release asset), or on any
host with ``--docker`` (the binary runs in a linux/amd64 container under the fixed
host name ``r2corpus``).
"""

from __future__ import annotations

import argparse
import contextlib
import dataclasses
import datetime as _dt
import getpass
import hashlib
import http.cookiejar
import json
import os
import pathlib
import platform
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request
from typing import Any, Callable

_HERE = pathlib.Path(__file__).resolve().parent


def _load_sibling(name: str):
    """Import a sibling script by path (registered before exec, for dataclasses)."""
    if name in sys.modules:
        return sys.modules[name]
    import importlib.util

    spec = importlib.util.spec_from_file_location(name, _HERE / f"{name}.py")
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


pebble_bytes = _load_sibling("pebble_bytes")

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
TAG_FILE = REPO_ROOT / "ci" / "r0_release.txt"
CORPUS_REL = "internal/books/testdata/r2corpus"
CORPUS_DIR = REPO_ROOT / CORPUS_REL
STAGE1_REL = f"{CORPUS_REL}/stage1"
STAGE1_DIR = REPO_ROOT / STAGE1_REL
ANSWERS_DIR = CORPUS_DIR / "answers"
MANIFEST_PATH = CORPUS_DIR / "manifest.json"
FIXTURE_AUDIO = REPO_ROOT / "testdata" / "fixtures"

MANIFEST_HEADER = {
    "file": f"{CORPUS_REL}/manifest.json",
    "version": "1.0.1",
    "guid": "d808d644-1404-4e9f-b549-f20bbbb939bd",
    "last-edited": "2026-11-10",
}

# The release asset 09 §9.3 names: the linux/amd64 binary, not a build at the tag.
ASSET_NAME = "audiobook-organizer_Linux_x86_64"

# Fixed, neutral work directory. R0 writes absolute paths (database_path, root_dir,
# book_file paths, log lines) into the store, so this directory is part of the
# corpus' bytes: it must be the same on every regeneration and must name no real
# user or host (invariants 2 and 3).
DEFAULT_WORKDIR = "/tmp/r2corpus"
DEFAULT_PORT = 18484
CONTAINER_HOSTNAME = "r2corpus"

# Generated fixture credentials (invariant 2): never real ones. Names are Batman
# characters only (public repo).
ADMIN_USER = "bruce.wayne"
FIXTURE_PASSWORD = "fixture-password-wayne-manor-1939"
OTHER_PASSWORD = "fixture-password-gotham-1940"

ABK_RE = re.compile(r"abk_[A-Za-z0-9]{16,}")
PRIVATE_IP_RE = re.compile(
    rb"\b(?:10\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])|192\.168)\.\d{1,3}\.\d{1,3}\b"
)

# Step names of the coverage table (brief "What the corpus must contain").
STEPS = ("M2", "M3", "M4", "M5", "M6", "books")

# Expiries a replay must not outlive (MAJOR 4a of the F2-12 review). R0 caps an
# API key at 365 days (handlers/apikeys.go MaxAPIKeyTTLDays: over it is a 400),
# so every fixture key gets the cap; an invite takes any number of hours, so
# every fixture invite gets ten years. manifest.json records when the first key
# lapses (``lapses``): a replay after that date sees expired keys.
KEY_TTL_DAYS = 365
INVITE_TTL_HOURS = 10 * 365 * 24
# An ABS login session's idle TTL (ABS_REFRESH_TOKEN_TTL, absauth/config.go
# DefaultRefreshTTL is 60 days, no maximum). /api/me/sessions hides a session
# past its expires_at (abs/me.go), so the default would empty that answer 60
# days after a regeneration. Ten years, set in the env layer.
ABS_SESSION_TTL_HOURS = 10 * 365 * 24
# A web session's remember-me TTL is a constant in R0 (handlers/auth.go
# rememberMeSessionTTL, 7 days) that no flag, env or setting changes. No answer
# reads web sessions; manifest.json records the date anyway.
WEB_SESSION_TTL_DAYS = 7

# The maintenance window the corpus configures (tasks.schedules) and the env
# layer's MAINTENANCE_WINDOW_START, in UTC (TZ=UTC). Window tasks would run
# while the writer or the recorder is driving R0 and add rows nobody asked for,
# so both refuse to start inside it, with an hour of margin on each side.
MAINTENANCE_WINDOW_UTC = (2, 5)
WINDOW_MARGIN_HOURS = 1

# A slow scan target outside stage1/ (MAJOR 1): a tree of empty directories the
# library scan has to walk, so a cancel or a stand-down reaches the scan while
# it runs. 40**3 = 64,000 directories took about 4 s to scan on a laptop. Git
# keeps no empty directory, so the tree is never part of the committed corpus.
SLOW_TREE_FANOUT = 40


class CorpusError(RuntimeError):
    """A precondition or an R0 answer the writer cannot continue past."""


# --------------------------------------------------------------------------- tag


def read_r0_tag(path: pathlib.Path = TAG_FILE) -> str:
    """Return the single tag line of ci/r0_release.txt.

    The reader rule F3-08's file uses: the tag is the one line that is neither
    blank nor starts with '#'. Zero or several such lines is an error.
    """
    lines = [
        ln.strip()
        for ln in path.read_text(encoding="utf-8").splitlines()
        if ln.strip() and not ln.startswith("#")
    ]
    if len(lines) != 1:
        raise CorpusError(f"{path}: want exactly one tag line, found {len(lines)}")
    if not re.fullmatch(r"v\d+\.\d+\.\d+", lines[0]):
        raise CorpusError(f"{path}: tag line {lines[0]!r} is not vMAJOR.MINOR.PATCH")
    return lines[0]


def sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch_binary(tag: str, cache: pathlib.Path) -> pathlib.Path:
    """Download the tag's linux/amd64 release asset with gh (idempotent)."""
    dest_dir = cache / tag
    dest = dest_dir / ASSET_NAME
    if dest.exists():
        return dest
    dest_dir.mkdir(parents=True, exist_ok=True)
    cmd = ["gh", "release", "download", tag, "--pattern", ASSET_NAME, "--dir", str(dest_dir)]
    subprocess.run(cmd, check=True, cwd=REPO_ROOT)
    if not dest.exists():
        raise CorpusError(f"release {tag} has no asset {ASSET_NAME}: STOP (09 §9.3)")
    dest.chmod(0o755)
    return dest


# ---------------------------------------------------------------- the boot spec


@dataclasses.dataclass(frozen=True)
class Layout:
    """The fixed work directory and everything R0 is pointed at inside it.

    stage1/ is the committed corpus root and is self-contained: every absolute
    path R0 stores points inside it.

    - ``stage1/db/``: the store directory (``audiobooks.pebble`` and what R0 puts
      beside it: ``config.yaml``, ``library.bleve``, ``logs/``, ...). It is R0's
      working directory too, so relative defaults land here.
    - ``stage1/state/``: ``ABK_STATE_DIR`` (``.encryption_key``).
    - ``stage1/import/``: the import root (fixture audio).
    - ``stage1/library/``: ``root_dir``, the organize output ABS serves.

    import/ and library/ are siblings of db/, not inside it: R0's library sweeps
    never descend into the database's directory (appdirs.FromConfig).
    """

    workdir: pathlib.Path

    @property
    def store(self) -> pathlib.Path:  # the committed directory (stage1/)
        return self.workdir / "stage1"

    @property
    def dbdir(self) -> pathlib.Path:  # the store directory and R0's cwd
        return self.store / "db"

    @property
    def db(self) -> pathlib.Path:
        return self.dbdir / "audiobooks.pebble"

    @property
    def state(self) -> pathlib.Path:  # ABK_STATE_DIR: .encryption_key, tokens
        return self.store / "state"

    @property
    def gapfill_yaml(self) -> pathlib.Path:  # ConfigFilePath(): beside the database
        return self.dbdir / "config.yaml"

    @property
    def import_dir(self) -> pathlib.Path:  # the import root holding fixture audio
        return self.store / "import"

    @property
    def library(self) -> pathlib.Path:  # root_dir (organize output)
        return self.store / "library"

    @property
    def playlists(self) -> pathlib.Path:
        return self.store / "playlists"

    @property
    def home(self) -> pathlib.Path:  # HOME, holding viper's config file (not committed)
        return self.workdir / "home"

    @property
    def viper_yaml(self) -> pathlib.Path:  # the operator YAML layer (--config)
        return self.home / ".audiobook-organizer.yaml"

    @property
    def serve_log(self) -> pathlib.Path:
        return self.workdir / "serve.out"

    @property
    def slow_tree(self) -> pathlib.Path:  # the slow scan target (not committed)
        return self.workdir / "gotham-sewers"


# The operator YAML layer (viper's --config file). Values here are what R0 reads
# from its config file; the stored layer is written through PUT /api/v1/config.
VIPER_YAML = {
    "log_level": "info",
    "concurrent_scans": 2,
    "scan_on_startup": False,
    "auto_update_enabled": False,
    "write_startup_readonly_key": False,
}

# The gap-fill config.yaml beside the store (persistence.go LoadConfigFromFile):
# read after the DB and only fills empty fields. R0 rewrites it on every settings
# save (SaveConfigToFile merges, keeping keys it does not manage), so the
# committed file is the one R0 left.
GAPFILL_YAML = {
    "hardcover_api_token": "fixture-hardcover-token-oracle",
    "enable_ai_parsing": False,
}


def boot_env(layout: Layout, port: int) -> dict[str, str]:
    """The environment layer. One key per env-read class (brief M2 row)."""
    return {
        "HOME": str(layout.home),
        "ABK_STATE_DIR": str(layout.state),
        "TZ": "UTC",
        # A plain viper key (AutomaticEnv, SyncConfigFromEnv): a secret.
        "ACOUSTID_API_KEY": "fixture-acoustid-key-lucius",
        # BindEnv'd scheduler keys: the scheduler layer.
        "SCHEDULED_LIBRARY_SCAN_ON_STARTUP": "false",
        "SCHEDULED_DEDUP_REFRESH_INTERVAL": "720",
        # A BindEnv'd maintenance-window key.
        "MAINTENANCE_WINDOW_START": "2",
        # Env-authoritative (locked) integration keys, one a secret.
        "DELUGE_WEB_URL": "http://deluge.invalid:8112",
        "DELUGE_WEB_PASSWORD": "fixture-deluge-password-oracle",
        # Env-authoritative ABS keys: the surface is off unless set, and the JWT
        # secret is never stored (config.go ABSJWTSecret).
        "ABS_API_ENABLED": "true",
        "ABS_AUTH_MODES": "jwt",
        "ABS_JWT_SECRET": "fixture-abs-jwt-secret-for-the-r2-corpus-only-0001",
        # The ABS session idle TTL: long enough that the corpus' ABS sessions
        # outlive every other answer-visible expiry (ABS_SESSION_TTL_HOURS).
        "ABS_REFRESH_TOKEN_TTL": f"{ABS_SESSION_TTL_HOURS}h",
    }


def boot_argv(binary: str, layout: Layout, port: int) -> list[str]:
    """The flag layer."""
    return [
        binary,
        "serve",
        "--config",
        str(layout.viper_yaml),
        "--db",
        str(layout.db),
        "--dir",
        str(layout.library),
        "--playlists",
        str(layout.playlists),
        "--host",
        "127.0.0.1",
        "--port",
        str(port),
        "--workers",
        "2",
    ]


def boot_spec(layout: Layout, port: int) -> dict[str, Any]:
    """The boot recorded in manifest.json, binary path elided."""
    return {
        "workdir": str(layout.workdir),
        "cwd": str(layout.dbdir),
        "argv": boot_argv("<r0-binary>", layout, port),
        "env": boot_env(layout, port),
        "files": {
            str(layout.viper_yaml): VIPER_YAML,
            str(layout.gapfill_yaml): GAPFILL_YAML,
        },
        "container_hostname": CONTAINER_HOSTNAME,
    }


def _yaml_dump(data: dict[str, Any]) -> str:
    """A tiny YAML writer for flat scalar maps (no PyYAML dependency)."""
    out = []
    for k, v in data.items():
        if isinstance(v, bool):
            s = "true" if v else "false"
        elif isinstance(v, (int, float)):
            s = str(v)
        else:
            s = json.dumps(str(v))
        out.append(f"{k}: {s}")
    return "\n".join(out) + "\n"


# Fixture audio: the repo's 0.1 s samples, laid out under Batman titles and
# characters. Copies of the MP3 get distinct ID3v1 tags so no two files hash
# alike (the scanner would otherwise see byte-identical duplicates).
LIBRARY_BOOKS = (
    # (author, title, [(filename, source sample)])
    ("Alfred Pennyworth", "The Long Halloween", [("The Long Halloween.m4b", "test_sample.m4b")]),
    ("Barbara Gordon", "Year One", [("Year One.mp3", "test_sample.mp3")]),
    (
        "Lucius Fox",
        "Knightfall",
        [
            ("Knightfall - Part 01.mp3", "test_sample.mp3"),
            ("Knightfall - Part 02.mp3", "test_sample.mp3"),
            ("Knightfall - Part 03.mp3", "test_sample.mp3"),
        ],
    ),
    ("Selina Kyle", "Hush", [("Hush.flac", "test_sample.flac")]),
    ("Leslie Thompkins", "The Killing Joke", [("The Killing Joke.mp3", "test_sample.mp3")]),
    ("Harvey Bullock", "Dark Victory", [("Dark Victory.mp3", "test_sample.mp3")]),
)


# Dropped into the import root while R0 runs (the watcher's auto-scan imports it).
LATE_BOOKS = (("Renee Montoya", "No Man's Land", [("No Man's Land.mp3", "test_sample.mp3")]),)


def _id3v1(title: str, artist: str, album: str, track: int) -> bytes:
    def field(s: str, n: int) -> bytes:
        return s.encode("latin-1", "replace")[:n].ljust(n, b"\0")

    # ID3v1.1: 28-byte comment, zero byte, track number, genre 255 (none).
    return (
        b"TAG"
        + field(title, 30)
        + field(artist, 30)
        + field(album, 30)
        + b"1939"
        + field("", 28)
        + b"\0"
        + struct.pack("B", track)
        + b"\xff"
    )


def lay_out_import(
    import_dir: pathlib.Path,
    fixtures: pathlib.Path = FIXTURE_AUDIO,
    books: tuple[Any, ...] = LIBRARY_BOOKS,
) -> list[str]:
    """Create fixture books under import_dir; returns relative paths in order."""
    written = []
    for author, title, files in books:
        book_dir = import_dir / author / title
        book_dir.mkdir(parents=True, exist_ok=True)
        for idx, (name, sample) in enumerate(files, start=1):
            data = (fixtures / sample).read_bytes()
            if name.endswith(".mp3"):
                data += _id3v1(
                    title if len(files) == 1 else f"{title} {idx:02d}", author, title, idx
                )
            (book_dir / name).write_bytes(data)
            written.append(str((book_dir / name).relative_to(import_dir)))
    return written


def prepare_workdir(layout: Layout) -> None:
    """A fresh work directory: empty store, the operator files, fixture audio."""
    if layout.workdir.exists():
        shutil.rmtree(layout.workdir)
    for d in (
        layout.dbdir,
        layout.state,
        layout.home,
        layout.import_dir,
        layout.library,
        layout.playlists,
    ):
        d.mkdir(parents=True, exist_ok=True)
    layout.state.chmod(0o700)
    layout.viper_yaml.write_text(_yaml_dump(VIPER_YAML), encoding="utf-8")
    layout.gapfill_yaml.write_text(_yaml_dump(GAPFILL_YAML), encoding="utf-8")
    layout.gapfill_yaml.chmod(0o600)
    lay_out_import(layout.import_dir)
    lay_out_slow_tree(layout.slow_tree)


def lay_out_slow_tree(root: pathlib.Path, fanout: int = SLOW_TREE_FANOUT) -> int:
    """fanout**3 empty directories under root; returns how many leaves."""
    for a in range(fanout):
        for b in range(fanout):
            for c in range(fanout):
                (root / f"tunnel-{a:02d}" / f"junction-{b:02d}" / f"vault-{c:02d}").mkdir(
                    parents=True, exist_ok=True
                )
    return fanout**3


def check_outside_maintenance_window(
    now: _dt.datetime | None = None, window: tuple[int, int] = MAINTENANCE_WINDOW_UTC
) -> None:
    """Refuse to drive R0 inside the configured maintenance window (UTC).

    ``window`` is (start, end) in UTC hours, the end exclusive; a window that
    wraps midnight (start > end) is handled, and so is the margin around it.
    """
    now = now or _dt.datetime.now(_dt.timezone.utc)
    start, end = window
    lo, hi = start - WINDOW_MARGIN_HOURS, end + WINDOW_MARGIN_HOURS
    span = (hi - lo) % 24 or 24
    if (now.astimezone(_dt.timezone.utc).hour - lo) % 24 < span:
        raise CorpusError(
            f"it is {now:%H:%M} UTC: the corpus' maintenance window is "
            f"{start:02d}:00-{end:02d}:00 UTC and window tasks would run while R0 is "
            f"driven; start outside {lo:02d}:00-{hi:02d}:00 UTC"
        )


def shifted_window(now: _dt.datetime | None = None) -> tuple[int, int]:
    """A maintenance window (start, end) UTC twelve hours away from now.

    A replay that must run at any hour (F2-15's CI jobs) boots with this window
    instead of MAINTENANCE_WINDOW_UTC, so no window task runs while it drives
    the binary; shifted_window_env gives the boot's env layer for it.
    """
    now = now or _dt.datetime.now(_dt.timezone.utc)
    length = MAINTENANCE_WINDOW_UTC[1] - MAINTENANCE_WINDOW_UTC[0]
    start = (now.astimezone(_dt.timezone.utc).hour + 12) % 24
    return start, (start + length) % 24


def shifted_window_env(window: tuple[int, int]) -> dict[str, str]:
    """The env layer that moves the maintenance window to ``window``."""
    return {"MAINTENANCE_WINDOW_START": str(window[0]), "MAINTENANCE_WINDOW_END": str(window[1])}


# ------------------------------------------------------------- the R0 process


class R0Process:
    """Start, probe and cleanly stop the R0 binary (natively or in a container)."""

    def __init__(
        self,
        binary: pathlib.Path,
        layout: Layout,
        port: int,
        docker: bool = False,
        extra_env: dict[str, str] | None = None,
    ) -> None:
        self.binary = binary
        self.layout = layout
        self.port = port
        self.docker = docker
        # Replaces boot_env's values by name (F2-15's pre-freeze replays move
        # the maintenance window away from the clock: shifted_window_env).
        self.extra_env = dict(extra_env or {})
        self.proc: subprocess.Popen[bytes] | None = None
        self._log = None
        self.container = f"r2corpus-{os.getpid()}"

    @property
    def base(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def _command(self) -> tuple[list[str], dict[str, str]]:
        env = {**boot_env(self.layout, self.port), **self.extra_env}
        if not self.docker:
            argv = boot_argv(str(self.binary), self.layout, self.port)
            full_env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), **env}
            return argv, full_env
        wd = str(self.layout.workdir)
        argv = [
            "docker", "run", "--rm", "--init", "--name", self.container,
            "--platform", "linux/amd64", "--hostname", CONTAINER_HOSTNAME,
            "--user", f"{os.getuid()}:{os.getgid()}",
            "-p", f"127.0.0.1:{self.port}:{self.port}",
            "-v", f"{wd}:{wd}",
            "-v", f"{self.binary.resolve()}:/r0/audiobook-organizer:ro",
            "-w", str(self.layout.dbdir),
        ]  # fmt: skip
        for k, v in env.items():
            argv += ["-e", f"{k}={v}"]
        argv += ["debian:bookworm-slim"]
        inner = boot_argv("/r0/audiobook-organizer", self.layout, self.port)
        # Inside the container the server must listen on all interfaces so the
        # published port reaches it; the host side is still bound to 127.0.0.1.
        inner[inner.index("--host") + 1] = "0.0.0.0"
        return argv + inner, dict(os.environ)

    def start(self, timeout: float = 120.0) -> None:
        argv, env = self._command()
        self._log = self.layout.serve_log.open("ab")
        self.proc = subprocess.Popen(  # noqa: S603 - fixed argv
            argv, cwd=self.layout.dbdir, env=env, stdout=self._log, stderr=subprocess.STDOUT
        )
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise CorpusError(f"R0 exited during boot ({self.proc.returncode}); see serve.out")
            try:
                with urllib.request.urlopen(f"{self.base}/api/v1/auth/status", timeout=3) as r:
                    if r.status == 200:
                        return
            except (urllib.error.URLError, ConnectionError, socket.timeout):
                pass
            time.sleep(0.5)
        raise CorpusError("R0 did not answer /api/v1/auth/status in time")

    def stop(self, timeout: float = 120.0) -> None:
        """SIGTERM and wait for a clean exit 0: the store is R0's shutdown output."""
        if self.proc is None:
            return
        if self.docker:
            subprocess.run(["docker", "kill", "--signal", "TERM", self.container], check=False)
        else:
            self.proc.send_signal(signal.SIGTERM)
        try:
            rc = self.proc.wait(timeout=timeout)
        finally:
            if self._log:
                self._log.close()
        self.proc = None
        if rc != 0:
            raise CorpusError(f"R0 did not stop cleanly (exit {rc}); the store is not usable")


# ---------------------------------------------------------------- HTTP client


@dataclasses.dataclass
class Response:
    status: int
    headers: dict[str, str]
    body: Any
    raw: bytes


class Client:
    """A stdlib HTTP client with one cookie jar per identity and API-key auth."""

    def __init__(self, base: str, user_agent: str = "r2corpus-writer/1.0") -> None:
        self.base = base.rstrip("/")
        self.user_agent = user_agent
        self.jars: dict[str, http.cookiejar.CookieJar] = {}
        self.keys: dict[str, str] = {}  # fixture key name -> raw secret (memory only)
        self.bearers: dict[str, str] = {}  # ABS identity -> access token (memory only)

    def request(
        self,
        method: str,
        path: str,
        body: Any = None,
        auth: str = "admin",
        headers: dict[str, str] | None = None,
        timeout: float = 120.0,
    ) -> Response:
        """Send one request; a 429 from R0's per-IP limiters is waited out and retried."""
        for _ in range(20):
            r = self._once(method, path, body, auth, headers, timeout)
            if r.status != 429:
                return r
            wait = r.headers.get("retry-after", "")
            time.sleep(min(float(wait), 60.0) if wait.isdigit() else 10.0)
        return r

    def _once(
        self,
        method: str,
        path: str,
        body: Any,
        auth: str,
        headers: dict[str, str] | None,
        timeout: float,
    ) -> Response:
        hdrs = {"Accept": "application/json", "User-Agent": self.user_agent}
        hdrs.update(headers or {})
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            hdrs["Content-Type"] = "application/json"
        jar = None
        if auth.startswith("key:"):
            # R0 reads an abk_ key from "Authorization: Bearer" (middleware/auth.go).
            hdrs["Authorization"] = f"Bearer {self.keys[auth[4:]]}"
        elif auth.startswith("abs:"):
            hdrs["Authorization"] = f"Bearer {self.bearers[auth[4:]]}"
        elif auth != "none":
            jar = self.jars.setdefault(auth, http.cookiejar.CookieJar())
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=hdrs)
        handlers: list[Any] = []
        if jar is not None:
            handlers.append(urllib.request.HTTPCookieProcessor(jar))
        opener = urllib.request.build_opener(*handlers)
        try:
            with opener.open(req, timeout=timeout) as r:
                raw = r.read()
                status, rh = r.status, dict(r.headers.items())
        except urllib.error.HTTPError as e:
            raw = e.read()
            status, rh = e.code, dict(e.headers.items())
        try:
            parsed: Any = json.loads(raw) if raw else None
        except ValueError:
            parsed = {"__non_json_body__": raw[:2000].decode("utf-8", "replace")}
        return Response(status, {k.lower(): v for k, v in rh.items()}, parsed, raw)


# ---------------------------------------------------------------- case DSL


@dataclasses.dataclass(frozen=True)
class Call:
    """One API call. ``${name}`` in path/body strings is filled from saved IDs."""

    method: str
    path: str
    body: Any = None
    auth: str = "admin"
    save: tuple[tuple[str, str], ...] = ()  # (name, dotted path into the response)
    save_key: str = ""  # store the response's raw API key under this fixture name
    expect: tuple[int, ...] = (200, 201, 202, 204)

    def describe(self) -> str:
        b = "" if self.body is None else " " + json.dumps(self.body, sort_keys=True)
        return f"{self.method} {self.path} [auth={self.auth}]{b}"


@dataclasses.dataclass(frozen=True)
class Poll:
    """Repeat a GET until ``until(body)`` is true."""

    path: str
    until: Callable[[Any], bool]
    what: str
    auth: str = "admin"
    timeout: float = 120.0

    def describe(self) -> str:
        return f"POLL GET {self.path} until {self.what}"


@dataclasses.dataclass(frozen=True)
class Hook:
    """A writer-side action: a restart, a login, a file the operator writes."""

    what: str
    fn: Callable[[Ctx], None]

    def describe(self) -> str:
        return f"HOOK {self.what}"


@dataclasses.dataclass(frozen=True)
class Case:
    case: str
    family: str
    step: str
    how: str
    steps: tuple[Any, ...]


@dataclasses.dataclass
class Ctx:
    client: Client | None
    proc: R0Process | None
    layout: Layout
    ids: dict[str, Any] = dataclasses.field(default_factory=dict)
    dry: bool = False
    log: list[str] = dataclasses.field(default_factory=list)


def _fill(value: Any, ids: dict[str, Any]) -> Any:
    if isinstance(value, str):
        if re.fullmatch(r"\$\{[a-z0-9_]+\}", value):
            return ids[value[2:-1]]  # a whole-string placeholder keeps the ID's type
        return re.sub(r"\$\{([a-z0-9_]+)\}", lambda m: str(ids[m.group(1)]), value)
    if isinstance(value, dict):
        return {k: _fill(v, ids) for k, v in value.items()}
    if isinstance(value, list):
        return [_fill(v, ids) for v in value]
    return value


def dig(body: Any, dotted: str) -> Any:
    cur = body
    for part in dotted.split("."):
        if isinstance(cur, list):
            cur = cur[int(part)]
        elif isinstance(cur, dict):
            cur = cur[part]
        else:
            raise KeyError(dotted)
    return cur


def find_key_secret(body: Any) -> str | None:
    """The raw abk_ secret anywhere in a mint response."""
    m = ABK_RE.search(json.dumps(body))
    return m.group(0) if m else None


def run_step(ctx: Ctx, step: Any) -> None:
    ctx.log.append(step.describe())
    if ctx.dry:
        if isinstance(step, Call):
            for name, _ in step.save:
                ctx.ids.setdefault(name, f"<{name}>")
        return
    assert ctx.client is not None
    if isinstance(step, Hook):
        step.fn(ctx)
        return
    if isinstance(step, Poll):
        deadline = time.monotonic() + step.timeout
        while True:
            r = ctx.client.request("GET", _fill(step.path, ctx.ids), auth=step.auth)
            if r.status == 200 and step.until(r.body):
                return
            if time.monotonic() > deadline:
                b = r.body.get("data", r.body) if isinstance(r.body, dict) else r.body
                keys = sorted(b) if isinstance(b, dict) else type(b).__name__
                last = f"keys={keys} status={_status(r.body)!r}"
                raise CorpusError(f"timed out: {step.describe()} (last {r.status}: {last})")
            time.sleep(1.0)
    r = ctx.client.request(
        step.method, _fill(step.path, ctx.ids), _fill(step.body, ctx.ids), auth=step.auth
    )
    if r.status not in step.expect:
        snippet = json.dumps(r.body)[:600]
        raise CorpusError(f"{step.describe()} -> {r.status}: {snippet}")
    for name, path in step.save:
        ctx.ids[name] = dig(r.body, path)
    if step.save_key:
        secret = find_key_secret(r.body)
        if not secret:
            raise CorpusError(f"{step.describe()}: no abk_ key in the response")
        ctx.client.keys[step.save_key] = secret


# ------------------------------------------------------------------- hooks


CANCEL_ATTEMPTS = 8


def op_status(ctx: Ctx, op_id: str) -> str:
    assert ctx.client is not None
    r = ctx.client.request("GET", f"/api/v1/operations/v2/{op_id}", auth="admin")
    return _status(r.body) if r.status == 200 else ""


def wait_status(
    ctx: Ctx, op_id: str, wanted: frozenset[str] | set[str], timeout: float = 120.0
) -> str:
    """Poll a run until its status is in ``wanted``; returns that status."""
    deadline = time.monotonic() + timeout
    while True:
        st = op_status(ctx, op_id)
        if st in wanted:
            return st
        if time.monotonic() > deadline:
            raise CorpusError(f"run {op_id} never reached {sorted(wanted)} (last {st!r})")
        time.sleep(0.25)


def start_op(ctx: Ctx, def_id: str, params: dict[str, Any]) -> str:
    assert ctx.client is not None
    r = ctx.client.request(
        "POST", "/api/v1/operations/v2", {"def_id": def_id, "params": params}, auth="admin"
    )
    if r.status not in (200, 201, 202):
        raise CorpusError(f"POST /api/v1/operations/v2 {def_id} -> {r.status}: {r.body}")
    return str(dig(r.body, "op_id"))


def _start_and_cancel_scan(ctx: Ctx) -> None:
    """Start a scan of the slow tree and DELETE it while it runs, until a cancel takes.

    An accepted DELETE does not prove the run ends canceled. R0 answers 204 when
    the cancel races the run's own completion (registry.go cancelByRow: "a cancel
    racing completion is not a 404"), and the worker then records the completion
    (worker.go keepsUserCancel), so the row ends ``completed``. A cancel landing
    after the run finished answers 404. So after every DELETE the writer waits
    for the run to be terminal and accepts only ``canceled``; anything else is a
    lost race and it tries again. The slow tree (64,000 empty directories) keeps
    the scan running for seconds, so a lost race is rare; each one leaves one
    more completed scan run, a normal row of the same family.
    """
    assert ctx.client is not None
    last = ""
    for _ in range(CANCEL_ATTEMPTS):
        op_id = start_op(
            ctx, "library.scan", {"folder_path": str(ctx.layout.slow_tree), "force_update": True}
        )
        if wait_status(ctx, op_id, TERMINAL | {"running"}, timeout=60.0) == "running":
            d = ctx.client.request("DELETE", f"/api/v1/operations/v2/{op_id}", auth="admin")
            if d.status not in (200, 202, 204, 404, 409):
                raise CorpusError(f"DELETE /api/v1/operations/v2/{op_id} -> {d.status}: {d.body}")
        final = wait_status(ctx, op_id, TERMINAL)
        if final == "canceled":
            ctx.ids["op_canceled"] = op_id
            return
        last = f"run {op_id} ended {final!r}"
    raise CorpusError(f"no scan could be canceled in flight after {CANCEL_ATTEMPTS} tries ({last})")


STANDDOWN_GRACE_SECONDS = 600  # R0 caps the grace at 10 minutes


def _set_standdown_grace(ctx: Ctx, seconds: int) -> None:
    assert ctx.client is not None
    r = ctx.client.request(
        "PUT", "/api/v1/config", {"scan_standdown_grace_seconds": seconds}, auth="admin"
    )
    if r.status != 200:
        raise CorpusError(f"PUT scan_standdown_grace_seconds={seconds} -> {r.status}")


def _end_standdown_grace(ctx: Ctx) -> None:
    """End an armed re-queue grace: an acquire cancels it, a 1 s grace replaces it.

    R0 reads the grace from config at each release, and an acquire during a
    grace cancels it (scan_standdown.go AcquireScanStandDown), so one more
    holder run under a 1 s grace leaves no grace after it.
    """
    _set_standdown_grace(ctx, 1)
    holder = start_op(ctx, "maintenance.metadata-refresh", {})
    if wait_status(ctx, holder, TERMINAL) != "completed":
        raise CorpusError(f"maintenance.metadata-refresh {holder} did not complete")
    time.sleep(2.0)
    _set_standdown_grace(ctx, STANDDOWN_GRACE_SECONDS)


def _quiesce_scan_under_standdown(ctx: Ctx) -> None:
    """A library scan stood down by a maintenance op, and a second scan held queued.

    R0's library scan never takes the scan stand-down: it is the op that stands
    down. A maintenance op takes it; ``maintenance.metadata-refresh`` (read-only,
    no parameters) holds it for its whole run (plugins/maintenance/metadata.go).
    Acquiring it quiesces a running ``library.scan``: the scan parks and its row
    becomes ``interrupted_quiesced``, and the stand-down marker is written to the
    ``registry.scan_standdown`` setting row (cleared to an empty value, not
    deleted, at the last release). The last release starts the re-queue grace
    (``scan_standdown_grace_seconds``, set to 600 by the case before this hook);
    while it runs, a newly enqueued ``library.scan`` is not dispatched (Gate 3.5
    in registry/dispatcher.go) and stays ``queued``. The clean stop that follows
    stops the grace timer and leaves both rows as they are (Registry.Shutdown:
    "the quiesced scan row stays interrupted_quiesced"; a claim never dispatched
    is released back as a queued row).
    """
    last = ""
    for _ in range(CANCEL_ATTEMPTS):
        scan = start_op(ctx, "library.scan", {"folder_path": str(ctx.layout.slow_tree)})
        if wait_status(ctx, scan, TERMINAL | {"running"}, timeout=60.0) != "running":
            last = f"scan {scan} finished before the holder could start"
            continue
        holder = start_op(ctx, "maintenance.metadata-refresh", {})
        if wait_status(ctx, holder, TERMINAL) != "completed":
            raise CorpusError(f"maintenance.metadata-refresh {holder} did not complete")
        st = wait_status(ctx, scan, TERMINAL | {"interrupted_quiesced"}, timeout=30.0)
        if st == "interrupted_quiesced":
            break
        # Lost race: the scan finished before the holder acquired. The release
        # still armed the 600 s grace, which would hold the next attempt's scan
        # queued, so end it first.
        last = f"scan {scan} ended {st!r} before the holder acquired"
        _end_standdown_grace(ctx)
    else:
        raise CorpusError(f"no scan could be stood down after {CANCEL_ATTEMPTS} tries ({last})")
    # The queued scan targets the slow tree too. A library.scan also scans every
    # import root whatever its folder, so a replay's boot (which dispatches this
    # row) rescans the library: the recorder settles boot-time runs and lists
    # the book fields that rescan rewrites as volatile.
    queued = start_op(ctx, "library.scan", {"folder_path": str(ctx.layout.slow_tree)})
    time.sleep(3.0)
    st = op_status(ctx, queued)
    if st != "queued":
        raise CorpusError(f"the scan enqueued during the grace is {st!r}, not queued")
    ctx.ids.update(op_quiesced=scan, op_standdown_holder=holder, op_queued=queued)
    ctx.ids["standdown_grace_started"] = time.monotonic()


# The rows the stand-down case leaves, and the status each must still have in
# the store after the clean stop.
STANDDOWN_ROWS = (("op_quiesced", "interrupted_quiesced"), ("op_queued", "queued"))
# Seconds of the grace the writer must still have left when it stops R0.
GRACE_MARGIN_SECONDS = 60


def check_grace_left(ids: dict[str, Any], now: float | None = None) -> None:
    """Refuse to stop R0 if the stand-down's re-queue grace may have run out.

    When the grace ends, R0 re-queues the quiesced scan and dispatches both
    scans, so neither row would be left as the case wrote it.
    """
    started = ids.get("standdown_grace_started")
    if started is None:
        return
    elapsed = (time.monotonic() if now is None else now) - started
    if elapsed > STANDDOWN_GRACE_SECONDS - GRACE_MARGIN_SECONDS:
        raise CorpusError(
            f"{elapsed:.0f} s since the stand-down grace started (grace "
            f"{STANDDOWN_GRACE_SECONDS} s): the quiesced and queued scans may have run"
        )


def check_standdown_rows(db: pathlib.Path, ids: dict[str, Any]) -> list[str]:
    """After the stop: the stand-down case's rows survived in the store as written.

    Reads the run records (``{"ID": ..., "Status": ...}``) from the Pebble files
    themselves (pebble_bytes.latest_record), not through R0: booting R0 to look
    would dispatch the very rows being checked.
    """
    found = []
    for key, want in STANDDOWN_ROWS:
        op_id = ids[key]
        rec = pebble_bytes.latest_record(db, b'{"ID":"' + str(op_id).encode() + b'"')
        got = rec.get("Status") if isinstance(rec, dict) else None
        if got != want:
            raise CorpusError(f"after the stop, run {op_id} ({key}) is {got!r}, not {want!r}")
        found.append(f"{key}={got}")
    return found


def hook_bootstrap(ctx: Ctx) -> None:
    """First admin: R0's own emergency path (the bootstrap token exchange).

    R0 seeds a system user at first boot, so POST /auth/setup answers 409 on a
    fresh store; the bootstrap exchange is the only API path to a first admin. It
    creates user ``admin`` with a generated password and mints an all-scope key.
    """
    assert ctx.client is not None
    token_file = ctx.layout.state / ".bootstrap-token"
    # R0 writes the token after the listener is up (server_lifecycle.go), so the
    # first /auth/status answer can precede the file.
    deadline = time.monotonic() + 60.0
    while not token_file.exists():
        if time.monotonic() > deadline:
            raise CorpusError(f"R0 wrote no {token_file.name} within 60 s")
        time.sleep(0.5)
    time.sleep(0.5)
    token = token_file.read_text(encoding="utf-8").strip()
    r = ctx.client.request(
        "POST",
        "/api/v1/auth/bootstrap",
        {"token": token, "key_name": "bootstrap-admin"},
        auth="none",
    )
    if r.status != 200:
        raise CorpusError(f"bootstrap exchange -> {r.status}: {r.body}")
    data = r.body["data"]
    ctx.ids["admin_id"] = data["user_id"]
    ctx.ids["admin_username"] = data["username"]
    ctx.client.keys["bootstrap-admin"] = data["api_key"]
    generated = data.get("generated_password")
    if not generated:
        raise CorpusError("bootstrap did not create the admin (an active admin already existed)")
    login(ctx, "admin", data["username"], generated)
    r = ctx.client.request(
        "PUT",
        "/api/v1/auth/me/password",
        {"current_password": generated, "new_password": FIXTURE_PASSWORD},
    )
    if r.status not in (200, 204):
        raise CorpusError(f"admin password change -> {r.status}: {r.body}")
    login(ctx, "admin", data["username"], FIXTURE_PASSWORD)
    # The exchange's all-scope key is a one-boot credential: revoke it (the row
    # stays, revoked) so the corpus holds no live all-scope key nobody can use.
    key_id = data.get("key_id")
    if not key_id:
        raise CorpusError("the bootstrap exchange named no key_id to revoke")
    r = ctx.client.request("DELETE", f"/api/v1/auth/api-keys/{key_id}")
    if r.status not in (200, 204):
        raise CorpusError(f"revoke bootstrap-admin -> {r.status}: {r.body}")
    ctx.client.keys.pop("bootstrap-admin", None)
    ctx.ids["key_bootstrap"] = key_id


def login(ctx: Ctx, identity: str, username: str, password: str) -> None:
    assert ctx.client is not None
    ctx.client.jars.pop(identity, None)
    r = ctx.client.request(
        "POST",
        "/api/v1/auth/login",
        {"username": username, "password": password, "remember_me": True},
        auth=identity,
    )
    if r.status != 200:
        raise CorpusError(f"login {username} -> {r.status}: {r.body}")


def hook_login(identity: str, username: str, password: str) -> Hook:
    return Hook(
        f"web login {username} (a session row)", lambda c: login(c, identity, username, password)
    )


def hook_accept_invite(identity: str, token_id: str, user_id: str, password: str) -> Hook:
    def fn(ctx: Ctx) -> None:
        assert ctx.client is not None
        r = ctx.client.request(
            "POST",
            "/api/v1/auth/accept-invite",
            {"token": ctx.ids[token_id], "password": password},
            auth=identity,
        )
        if r.status not in (200, 201):
            raise CorpusError(f"accept-invite {token_id} -> {r.status}: {r.body}")
        ctx.ids[user_id] = r.body["data"]["user"]["id"]

    return Hook(f"accept invite {token_id} as {identity} (user, session)", fn)


def hook_restart(why: str) -> Hook:
    def fn(ctx: Ctx) -> None:
        assert ctx.proc is not None and ctx.client is not None
        ctx.proc.stop()
        ctx.proc.start()
        # Sessions survive a restart (they are store rows); cookies stay valid.

    return Hook(f"restart R0 ({why})", fn)


def hook_abs_login(identity: str, username: str, password: str) -> Hook:
    def fn(ctx: Ctx) -> None:
        assert ctx.client is not None
        r = ctx.client.request(
            "POST",
            "/login",
            {
                "username": username,
                "password": password,
                "deviceInfo": {"clientName": "r2corpus", "deviceId": f"{identity}-device-01"},
            },
            auth="none",
            headers={"x-return-tokens": "true"},
        )
        if r.status != 200:
            raise CorpusError(f"ABS login {username} -> {r.status}: {r.body}")
        user = r.body.get("user", {})
        token = user.get("accessToken") or user.get("token")
        if not token:
            raise CorpusError(f"ABS login {username}: no token in {sorted(user)}")
        ctx.client.bearers[identity] = token

    return Hook(f"ABS login {username} (an abs_sess: refresh session)", fn)


def _slug(title: str) -> str:
    return re.sub(r"[^a-z0-9]+", "_", title.lower()).strip("_")


def hook_pick_books(ctx: Ctx) -> None:
    """Save book IDs by title, in LIBRARY_BOOKS order, for the later cases."""
    assert ctx.client is not None
    r = ctx.client.request("GET", "/api/v1/audiobooks?limit=100&sort_by=title&sort_order=asc")
    items = r.body.get("data", r.body)
    if isinstance(items, dict):
        items = items.get("items") or items.get("audiobooks") or items.get("data") or []
    by_title = {b.get("title"): b for b in items}
    for _, title, _ in LIBRARY_BOOKS:
        if title not in by_title:
            raise CorpusError(
                f"book {title!r} not in the library after the scan: {sorted(by_title)}"
            )
        ctx.ids[f"book_{_slug(title)}"] = by_title[title]["id"]


def hook_pick_abs_item(ctx: Ctx) -> None:
    """The ABS library and the multi-file item (Knightfall) by the ABS API."""
    assert ctx.client is not None
    r = ctx.client.request("GET", "/api/libraries", auth="abs:robin")
    libs = r.body.get("libraries", [])
    if not libs:
        raise CorpusError(f"ABS reports no library: {r.status} {r.body}")
    ctx.ids["abs_library"] = libs[0]["id"]
    r = ctx.client.request(
        "GET", f"/api/libraries/{libs[0]['id']}/items?limit=50&page=0", auth="abs:robin"
    )
    seen = []
    for item in r.body.get("results", []):
        meta = (item.get("media") or {}).get("metadata") or {}
        title = meta.get("title") or item.get("title")
        seen.append(title)
        if title == "Knightfall":
            ctx.ids["abs_item"] = item["id"]
            return
    raise CorpusError(
        f"ABS items list has no Knightfall: {r.status} titles={seen} "
        f"body={json.dumps(r.body)[:600]}"
    )


def hook_play_sync_close(ctx: Ctx) -> None:
    assert ctx.client is not None
    item = ctx.ids["abs_item"]
    r = ctx.client.request(
        "POST",
        f"/api/items/{item}/play",
        {
            "deviceInfo": {"clientName": "r2corpus", "deviceId": "robin-device-01"},
            "mediaPlayer": "unknown",
            "forceDirectPlay": True,
        },
        auth="abs:robin",
    )
    if r.status != 200:
        raise CorpusError(f"ABS play -> {r.status}: {r.body}")
    sid = r.body["id"]
    for path, body in (
        (f"/api/session/{sid}/sync", {"currentTime": 0.1, "timeListened": 0.1, "duration": 0.3}),
        (f"/api/session/{sid}/close", {"currentTime": 0.2, "timeListened": 0.1, "duration": 0.3}),
    ):
        r = ctx.client.request("POST", path, body, auth="abs:robin")
        if r.status != 200:
            raise CorpusError(f"ABS {path} -> {r.status}: {r.body}")


def hook_drop_late_book(ctx: Ctx) -> None:
    """The operator drops a book into the watched import root (no store write).

    The watcher adds a watch on a new directory when it sees the directory's
    Create event, so the directories are made first and the audio file is written
    after the watch exists (internal/watcher/watcher.go handleEvent).
    """
    for author, title, _ in LATE_BOOKS:
        (ctx.layout.import_dir / author / title).mkdir(parents=True, exist_ok=True)
    time.sleep(3.0)
    lay_out_import(ctx.layout.import_dir, books=LATE_BOOKS)


def _syslog_rows(body: Any) -> int:
    d = body.get("data", {}) if isinstance(body, dict) else {}
    return int(d.get("count") or 0) if isinstance(d, dict) else 0


# ------------------------------------------------------------------- cases

ALL_ATOMS = [
    "library.view",
    "library.edit_metadata",
    "library.delete",
    "library.organize",
    "scan.trigger",
    "integrations.manage",
    "users.manage",
    "settings.manage",
    "playlists.create",
    "collections.manage",
    "requests.create",
    "requests.approve",
    "fingerprint.worker",
]


def _invite(user: str, role: str, email: str) -> Call:
    return Call(
        "POST",
        "/api/v1/users/invite",
        {"username": user, "role_id": role, "email": email, "expires_in": INVITE_TTL_HOURS},
        save=((f"invite_{user.replace('.', '_')}_token", "data.token"),
              (f"invite_{user.replace('.', '_')}_id", "data.invite.id")),
    )  # fmt: skip


TERMINAL = frozenset({"completed", "succeeded", "failed", "canceled", "cancelled"})


def _done(op: str) -> Poll:
    return Poll(
        "/api/v1/operations/v2/${" + op + "}",
        lambda b: _status(b) in TERMINAL,
        f"{op} is terminal",
    )


def _status(body: Any) -> str:
    """The run status of a GET /operations/v2/:id answer ({"data": {"operation": ...}})."""
    d = body.get("data", body) if isinstance(body, dict) else {}
    if not isinstance(d, dict):
        return ""
    for holder in (d.get("operation"), d.get("op"), d):
        if isinstance(holder, dict) and isinstance(holder.get("status"), str):
            return holder["status"]
    return ""


CONFIG_SETTINGS = {
    # Plain values of several classes the rename table moves (05-config §1.12.2):
    # strings, booleans, numbers, nested objects, lists.
    "organization_strategy": "copy",
    "folder_naming_pattern": "{author}/{title}",
    "file_naming_pattern": "{title} - {track:02d}",
    "auto_organize": False,
    "concurrent_scans": 3,
    "log_retention_days": 21,
    "setup_complete": True,
    # The import-root watcher (boot-time; the restart in integrations.blob arms it):
    # its "Auto-scan triggered" lines are the syslog: rows of normal operation.
    "auto_scan_enabled": True,
    "auto_scan_debounce_seconds": 2,
    # Plaintext-in-blob secrets and HEAD's encrypted rows: the four IsSecret rows.
    "openai_api_key": "fixture-openai-key-oracle-alfred",
    "google_books_api_key": "fixture-google-books-key-oracle",
    "basic_auth_password": "fixture-basic-auth-password-oracle",
}

CONFIG_INTEGRATIONS = {
    # Integration-instance fields in the blob (M6): Deluge (legacy keys and the
    # download client, with its daemon password), iTunes, metadata-source
    # credentials, and the webhook plugin (HEAD's webhook settings).
    "deluge_discovery_label": "batcave",
    "download_client": {
        "torrent": {
            "type": "deluge",
            "deluge": {
                "host": "deluge.invalid",
                "port": 58846,
                "username": "alfred.pennyworth",
                "password": "fixture-deluge-daemon-password-oracle",
            },
        }
    },
    "itunes": {"windows_root_path": "C:\\Gotham\\Music", "path_trim_enabled": True},
    "metadata_sources": [
        {"id": "audible", "name": "Audible", "enabled": True, "priority": 1,
         "requires_auth": False, "credentials": {}},
        {"id": "openlibrary", "name": "Open Library", "enabled": True, "priority": 2,
         "requires_auth": False, "credentials": {}},
        {"id": "audnexus", "name": "Audnexus", "enabled": False, "priority": 3,
         "requires_auth": False, "credentials": {}},
        {"id": "google-books", "name": "Google Books", "enabled": False, "priority": 4,
         "requires_auth": True, "credentials": {"apiKey": ""}},
        {"id": "hardcover", "name": "Hardcover", "enabled": True, "priority": 5,
         "requires_auth": True, "credentials": {"apiKey": "fixture-hardcover-source-key"}},
        {"id": "wikipedia", "name": "Wikipedia", "enabled": False, "priority": 6,
         "requires_auth": False, "credentials": {}},
    ],
    "plugins": {
        "webhook": {
            "enabled": True,
            "settings": {
                "urls": "http://webhook.invalid/gotham",
                "secret": "fixture-webhook-hmac-secret-oracle",
                "events": "all",
            },
        }
    },
}  # fmt: skip

CASES: tuple[Case, ...] = (
    Case(
        "admin.bootstrap",
        "user, apikey, setting:bootstrap_*",
        "M3",
        "bootstrap token exchange: first admin `admin` + an all-scope admin-owned key",
        (Hook("bootstrap exchange, admin password change, admin login", hook_bootstrap),),
    ),
    Case(
        "users.invited",
        "user, invite, session",
        "M3",
        "invites for every seeded role (admin, editor, viewer, fp-worker), accepted "
        "(each sets a session)",
        (
            _invite("dick.grayson", "editor", "robin@example.com"),
            _invite("barbara.gordon", "viewer", "batgirl@example.com"),
            _invite("lucius.fox", "admin", "lucius@example.com"),
            _invite("fp.worker", "fp-worker", "worker@example.com"),
            _invite("jason.todd", "viewer", "redhood@example.com"),
            hook_accept_invite("robin", "invite_dick_grayson_token", "user_robin", OTHER_PASSWORD),
            hook_accept_invite(
                "batgirl", "invite_barbara_gordon_token", "user_batgirl", OTHER_PASSWORD
            ),
            hook_accept_invite("lucius", "invite_lucius_fox_token", "user_lucius", OTHER_PASSWORD),
            hook_accept_invite(
                "fpworker", "invite_fp_worker_token", "user_fpworker", OTHER_PASSWORD
            ),
            hook_accept_invite(
                "redhood", "invite_jason_todd_token", "user_redhood", OTHER_PASSWORD
            ),
        ),
    ),
    Case(
        "users.disabled",
        "user (status inactive)",
        "M3",
        "deactivate one viewer",
        (Call("POST", "/api/v1/users/${user_redhood}/deactivate"),),
    ),
    Case(
        "invites.open",
        "invite",
        "M3",
        "an invite left open and one revoked",
        (
            _invite("tim.drake", "viewer", "tim@example.com"),
            _invite("stephanie.brown", "editor", "spoiler@example.com"),
            Call("DELETE", "/api/v1/users/invites/${invite_stephanie_brown_id}"),
        ),
    ),
    Case(
        "sessions.web",
        "session",
        "M3",
        "web logins (remember_me) for several users",
        (
            hook_login("robin2", "dick.grayson", OTHER_PASSWORD),
            hook_login("batgirl2", "barbara.gordon", OTHER_PASSWORD),
        ),
    ),
    Case(
        "apikeys.variants",
        "apikey, idx:apikey",
        "M3",
        "scoped, empty-scope, every-atom, admin-owned-for-another-user, fp-worker keys",
        (
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": "scoped-view",
                    "scopes": ["library.view"],
                    "expires_in_days": KEY_TTL_DAYS,
                },
                save=(("key_scoped", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {"name": "empty-scope", "scopes": [], "expires_in_days": KEY_TTL_DAYS},
                save=(("key_empty", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {"name": "every-atom", "scopes": ALL_ATOMS, "expires_in_days": KEY_TTL_DAYS},
                save=(("key_every", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": "admin-owned-for-robin",
                    "scopes": ["library.view", "playlists.create"],
                    "user_id": "${user_robin}",
                    "expires_in_days": KEY_TTL_DAYS,
                },
                save=(("key_for_robin", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": "fp-worker-key",
                    "scopes": ["fingerprint.worker"],
                    "user_id": "${user_fpworker}",
                    "expires_in_days": KEY_TTL_DAYS,
                },
                save=(("key_fpworker", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": "editor-own-key",
                    "scopes": ["library.view"],
                    "expires_in_days": KEY_TTL_DAYS,
                },
                auth="robin",
                save=(("key_robin_own", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/auth/api-keys",
                {
                    "name": "revoked-key",
                    "scopes": ["library.view"],
                    "expires_in_days": KEY_TTL_DAYS,
                },
                save=(("key_revoked", "data.id"),),
            ),
            Call("DELETE", "/api/v1/auth/api-keys/${key_revoked}"),
        ),  # fmt: skip
    ),
    Case(
        "config.stored",
        "setting:config_blob, setting:<secret> (encrypted)",
        "M2",
        "PUT /api/v1/config: plain keys of every class and the four IsSecret secrets",
        (Call("PUT", "/api/v1/config", CONFIG_SETTINGS),),
    ),
    Case(
        "config.layers",
        "config.yaml (gap-fill), env, viper YAML",
        "M2",
        "operator layers: gap-fill config.yaml beside the store, env per layer, viper YAML",
        (Poll("/api/v1/config", lambda b: "data" in b, "GET /config answers (layers applied)"),),
    ),
    Case(
        "integrations.blob",
        "setting:config_blob (integration fields), webhook plugin settings",
        "M6",
        "integration fields and the webhook plugin through PUT /api/v1/config, then a restart",
        (
            Call("PUT", "/api/v1/config", CONFIG_INTEGRATIONS),
            hook_restart("plugins initialise at boot"),
        ),
    ),
    Case(
        "import.roots",
        "importpath, book, book_file, author, ... (the library)",
        "M6",
        "an import root added through the API; its auto-scan imports the fixture library",
        (
            Call(
                "POST",
                "/api/v1/import-paths",
                {"path": "${import_dir}", "name": "Gotham Archive", "enabled": True},
                save=(("import_path_id", "data.import_path.id"),),
            ),
            Poll(
                "/api/v1/audiobooks?limit=100",
                lambda b: _count_books(b) >= len(LIBRARY_BOOKS),
                f"{len(LIBRARY_BOOKS)} books are in the library",
                timeout=300.0,
            ),
            Hook("save book IDs by title", hook_pick_books),
        ),  # fmt: skip
    ),
    Case(
        "books.organized",
        "book, book_file (organized copies under root_dir), undo journal",
        "books",
        "every imported book organized into root_dir (copy strategy); ABS serves root_dir",
        tuple(
            Call("POST", f"/api/v1/audiobooks/${{book_{_slug(t)}}}/organize")
            for _, t, _ in LIBRARY_BOOKS
        ),
    ),
    Case(
        "tasks.schedules",
        "setting: scheduler task config, maintenance window",
        "M4",
        "task schedules edited through the task routes; maintenance window config",
        (
            Call("PUT", "/api/v1/tasks/library_scan_full", {"interval_minutes": 1440}),
            Call("PUT", "/api/v1/tasks/dedup_refresh", {"enabled": True, "run_on_startup": False}),
            Call("PUT", "/api/v1/tasks/purge_old_logs", {"run_in_maintenance_window": False}),
            Call("PUT", "/api/v1/tasks/library_scan", {"enabled": False}),
            Call(
                "PUT",
                "/api/v1/maintenance-window/config",
                {"enabled": True, "window_start": 2, "window_end": 5},
            ),
        ),  # fmt: skip
    ),
    Case(
        "runs.succeeded",
        "opv2:run (succeeded), op history",
        "M4",
        "a library scan run that succeeds",
        (
            Call(
                "POST",
                "/api/v1/operations/v2",
                {"def_id": "library.scan", "params": {"folder_path": "${import_dir}"}},
                save=(("op_scan", "op_id"),),
            ),
            _done("op_scan"),
        ),  # fmt: skip
    ),
    Case(
        "runs.failed",
        "opv2:run (failed)",
        "M4",
        "a library scan of a missing folder (fails)",
        (
            Call(
                "POST",
                "/api/v1/operations/v2",
                {"def_id": "library.scan", "params": {"folder_path": "${missing_dir}"}},
                save=(("op_failed", "op_id"),),
            ),
            _done("op_failed"),
        ),  # fmt: skip
    ),
    Case(
        "runs.canceled",
        "opv2:run (canceled)",
        "M4",
        "a slow library scan canceled while running through DELETE /operations/v2/:id "
        "(retried until the run ends canceled, not completed)",
        (
            Hook(
                "scan the slow tree and cancel it while it runs, until the run ends canceled",
                _start_and_cancel_scan,
            ),
        ),
    ),
    Case(
        "runs.task",
        "opv2:run (scheduled task run), task last_run clock",
        "M4",
        "a scheduled task run on demand through POST /tasks/:name/run",
        (Call("POST", "/api/v1/tasks/purge_old_logs/run"),),
    ),
    Case(
        "books.playlists",
        "playlist",
        "books",
        "a static playlist with two books and a smart playlist",
        (
            Call(
                "POST",
                "/api/v1/playlists",
                {
                    "name": "Arkham Reading List",
                    "type": "static",
                    "book_ids": ["${book_the_long_halloween}"],
                },
                save=(("playlist_static", "data.id"),),
            ),
            Call(
                "POST",
                "/api/v1/playlists/${playlist_static}/books",
                {"book_ids": ["${book_year_one}"]},
            ),
            Call(
                "POST",
                "/api/v1/playlists",
                {"name": "Knight Stories", "type": "smart", "query": "Knight"},
                save=(("playlist_smart", "data.id"),),
            ),
        ),  # fmt: skip
    ),
    Case(
        "books.reading",
        "user book state (status overrides)",
        "books",
        "reading status set through the web API for two users",
        (
            Call("PATCH", "/api/v1/audiobooks/${book_hush}/status", {"status": "finished"}),
            Call(
                "PATCH",
                "/api/v1/audiobooks/${book_dark_victory}/status",
                {"status": "abandoned"},
                auth="robin",
            ),
            Call(
                "PATCH",
                "/api/v1/audiobooks/${book_year_one}/status",
                {"status": "in_progress"},
                auth="batgirl",
            ),
        ),  # fmt: skip
    ),
    Case(
        "books.abs",
        "abs_sess:, ABS progress, bookmarks, playback sessions",
        "books",
        "ABS logins, a play/sync/close session, progress and a bookmark through the ABS API",
        (
            hook_abs_login("robin", "dick.grayson", OTHER_PASSWORD),
            hook_abs_login("batgirl", "barbara.gordon", OTHER_PASSWORD),
            Hook("find the ABS library and the multi-file item", hook_pick_abs_item),
            Hook("play, sync and close a session", hook_play_sync_close),
            Call(
                "PATCH",
                "/api/me/progress/${abs_item}",
                {"currentTime": 0.2, "duration": 0.3, "isFinished": False},
                auth="abs:robin",
            ),
            Call(
                "POST",
                "/api/me/item/${abs_item}/bookmark",
                {"time": 0.15, "title": "The Bane confrontation"},
                auth="abs:robin",
            ),
            Call(
                "PATCH",
                "/api/me/progress/${abs_item}",
                {"currentTime": 0.3, "duration": 0.3, "isFinished": True},
                auth="abs:batgirl",
            ),
        ),  # fmt: skip
    ),
    Case(
        "syslog.normal",
        "syslog:, book (auto-scanned), opv2:run (library.scan from the watcher)",
        "M5",
        "a book dropped into the watched import root: the auto-scan watcher logs to syslog:",
        (
            # The watcher supervisor reads the import roots at boot and every 5
            # minutes (internal/watcher/supervisor.go), so a restart arms it.
            hook_restart("the watcher picks up the import root at boot"),
            Hook("drop No Man's Land into the import root", hook_drop_late_book),
            Poll(
                "/api/v1/system/activity-log?limit=5",
                lambda b: _syslog_rows(b) > 0,
                "a system activity-log row exists",
                timeout=180.0,
            ),
            Poll(
                "/api/v1/audiobooks?limit=100",
                lambda b: _count_books(b) >= len(LIBRARY_BOOKS) + len(LATE_BOOKS),
                "the dropped book is imported",
                timeout=300.0,
            ),
        ),  # fmt: skip
    ),
    Case(
        "runs.standdown",
        "setting:registry.scan_standdown, opv2:run (interrupted_quiesced), opv2:run (queued)",
        "M4",
        "a running library scan stood down by maintenance.metadata-refresh (R0's scans "
        "never take the stand-down; maintenance ops do), then a second scan held queued "
        "by the stand-down's re-queue grace; the stop leaves both rows as they are",
        (
            # Last before the stop: a restart would stop the grace timer and the
            # boot resume sweep would re-queue and run the quiesced scan.
            Call(
                "PUT", "/api/v1/config", {"scan_standdown_grace_seconds": STANDDOWN_GRACE_SECONDS}
            ),
            Hook(
                "scan the slow tree, stand it down with metadata-refresh, enqueue a scan",
                _quiesce_scan_under_standdown,
            ),
        ),
    ),
    Case(
        "runs.paused",
        "operations pause marker",
        "M4",
        "the executor paused through POST /operations/pause and left paused at the stop",
        (Call("POST", "/api/v1/operations/pause", {"reason": "r2corpus: paused executor"}),),
    ),
)

# Families the coverage table asks for that R0's API cannot write: F2-13's overlay.
NOT_WRITABLE = (
    {
        "family": "oauthid:",
        "step": "M3",
        "why": "OAuth identities are created only by an SSO sign-in against an external provider",
    },
    {
        "family": "inviteallow:",
        "step": "M3",
        "why": "the invite allow-list is written only when an invite delivered by email is consumed",
    },
    {
        "family": "aijob*",
        "step": "M6",
        "why": "AI jobs are created only through the OpenAI Batch API (no fake endpoint in R0)",
    },
    {
        "family": "opv2:run (running, stopping)",
        "step": "M4",
        "why": "a clean stop cannot leave a run running or stopping: Registry.Shutdown "
        "(internal/operations/registry/registry.go at the R0 tag) cancels every running op "
        "with ErrShutdown and marks what has not drained interrupted per its ResumePolicy; "
        "F2-13 writes these states",
    },
)


def _count_books(body: Any) -> int:
    d = body.get("data", body) if isinstance(body, dict) else body
    if isinstance(d, dict):
        for k in ("count", "total"):
            if isinstance(d.get(k), int):
                return d[k]
        d = d.get("items") or d.get("audiobooks") or []
    return len(d) if isinstance(d, list) else 0


# --------------------------------------------------------------- the writer


def run_cases(ctx: Ctx, cases: tuple[Case, ...] = CASES) -> None:
    ctx.ids.setdefault("import_dir", str(ctx.layout.import_dir))
    ctx.ids.setdefault("missing_dir", str(ctx.layout.workdir / "missing-folder"))
    for case in cases:
        ctx.log.append(f"## {case.step} {case.case}: {case.how}")
        for step in case.steps:
            run_step(ctx, step)


ONE_BOOT_FILES = (".bootstrap-token", ".readonly-key")


def scrub_one_boot_credentials(layout: Layout) -> list[str]:
    """Delete the one-boot credential files R0 leaves in its state directory."""
    removed = []
    for name in ONE_BOOT_FILES:
        p = layout.state / name
        if p.exists():
            p.unlink()
            removed.append(name)
    return removed


def leak_patterns() -> list[tuple[str, re.Pattern[bytes]]]:
    """Byte patterns that must never appear anywhere in the committed corpus."""
    pats: list[tuple[str, re.Pattern[bytes]]] = [
        ("abk_ API key", re.compile(rb"abk_[A-Za-z0-9]{16,}")),
        ("bootstrap token", re.compile(rb"abbs_[A-Za-z0-9_-]{16,}")),
        ("private IPv4", PRIVATE_IP_RE),
    ]
    host = socket.gethostname().split(".")[0]
    if len(host) >= 3 and host.lower() not in ("localhost", CONTAINER_HOSTNAME):
        pats.append(("host name", re.compile(re.escape(host.encode()), re.IGNORECASE)))
    with contextlib.suppress(Exception):
        user = getpass.getuser()
        if len(user) >= 3 and user not in ("root",):
            pats.append(("user name", re.compile(rb"/" + re.escape(user.encode()) + rb"\b")))
    return pats


def scan_for_leaks(root: pathlib.Path) -> list[str]:
    """Every file's raw bytes, and a Pebble store's plaintext, against leak_patterns.

    Pebble Snappy-compresses SST blocks and cuts WAL batches into chunks, so the
    raw bytes alone can hide a secret; pebble_bytes.plaintext_views adds the
    decompressed blocks and the reassembled WAL. A store file it cannot walk is
    a finding too (fail closed), never skipped.
    """
    findings = []
    pats = leak_patterns()
    for p in sorted(root.rglob("*")):
        if not p.is_file():
            continue
        rel = p.relative_to(root)
        try:
            views = pebble_bytes.plaintext_views(p)
        except pebble_bytes.StoreDecodeError as e:
            findings.append(f"{rel}: cannot be decoded for the scan ({e})")
            continue
        for label, pat in pats:
            if any(pat.search(v) for v in views):
                findings.append(f"{rel}: {label}")
    return findings


def store_files(store: pathlib.Path) -> list[dict[str, Any]]:
    out = []
    for p in sorted(store.rglob("*")):
        if p.is_file():
            out.append(
                {
                    "path": p.relative_to(store).as_posix(),
                    "size": p.stat().st_size,
                    "sha256": sha256_file(p),
                }
            )
    return out


def lapses(now: _dt.datetime) -> dict[str, str]:
    """When the corpus' expiring rows lapse, and the date the answers stop holding.

    ``answers_valid_until`` is the earliest lapse an answer can see (API key
    status, the open invite, ABS sessions); the recorder refuses to record past
    it. Web sessions lapse sooner, but no answer reads them.
    """
    fmt = "%Y-%m-%dT%H:%M:%SZ"
    visible = {
        "api_keys": now + _dt.timedelta(days=KEY_TTL_DAYS),
        "invites": now + _dt.timedelta(hours=INVITE_TTL_HOURS),
        "abs_sessions": now + _dt.timedelta(hours=ABS_SESSION_TTL_HOURS),
    }
    out = {k: v.strftime(fmt) for k, v in visible.items()}
    out["web_sessions"] = (now + _dt.timedelta(days=WEB_SESSION_TTL_DAYS)).strftime(fmt)
    out["answers_valid_until"] = min(visible.values()).strftime(fmt)
    out["why"] = (
        f"R0 caps an API key at {KEY_TTL_DAYS} days; keys, invites and ABS sessions get "
        "the longest life R0 allows, and a replay after answers_valid_until sees expired "
        f"rows. Web sessions keep R0's fixed {WEB_SESSION_TTL_DAYS}-day remember-me TTL; "
        "no answer reads them"
    )
    return out


def build_manifest(
    tag: str,
    binary_sha: str,
    boot: dict[str, Any],
    store_dir: pathlib.Path,
    answers: list[dict[str, str]] | None = None,
    cases: tuple[Case, ...] = CASES,
    now: _dt.datetime | None = None,
) -> dict[str, Any]:
    now = now or _dt.datetime.now(_dt.timezone.utc)
    fmt = "%Y-%m-%dT%H:%M:%SZ"
    return {
        "_header": MANIFEST_HEADER,
        "r0_tag": tag,
        "r0_binary_sha256": binary_sha,
        "written_at": now.strftime(fmt),
        "lapses": lapses(now),
        "credentials": "every credential in this corpus is a generated fixture value "
        "(invariant 2); API keys in answer files are <api-key:NAME> placeholders",
        "boot": boot,
        "stage1": [
            {"case": c.case, "family": c.family, "step": c.step, "how": c.how, "store": STAGE1_REL}
            for c in cases
        ],
        "not_writable_by_r0_api": list(NOT_WRITABLE),
        "overlay": [],
        "blockers": [],
        "stores": {STAGE1_REL: store_files(store_dir)},
        "answers": answers or [],
        "volatile_fields": [],
    }


def write_manifest(manifest: dict[str, Any], path: pathlib.Path = MANIFEST_PATH) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def check_native_binary(binary: pathlib.Path, allow_foreign: bool) -> None:
    if platform.system() == "Linux" and platform.machine() in ("x86_64", "AMD64"):
        return
    if allow_foreign:
        return
    raise CorpusError(
        f"{binary.name} is the linux/amd64 release asset and this host is "
        f"{platform.system()}/{platform.machine()}: use --docker, or run on linux/amd64"
    )


def dry_run(out: Any = None) -> list[str]:
    out = out if out is not None else sys.stdout
    layout = Layout(pathlib.Path(DEFAULT_WORKDIR))
    ctx = Ctx(client=None, proc=None, layout=layout, dry=True)
    run_cases(ctx)
    for line in ctx.log:
        print(line, file=out)
    return ctx.log


def write_corpus(args: argparse.Namespace) -> int:
    tag = args.tag or read_r0_tag()
    if args.binary:
        binary = pathlib.Path(args.binary).resolve()
    else:
        binary = fetch_binary(tag, pathlib.Path(args.cache).expanduser())
    if not args.docker:
        check_native_binary(binary, args.allow_foreign_binary)
    check_outside_maintenance_window()
    binary_sha = sha256_file(binary)
    out_store = pathlib.Path(args.out).resolve() if args.out else STAGE1_DIR
    if args.allow_foreign_binary and out_store == STAGE1_DIR:
        raise CorpusError("--allow-foreign-binary is for development: give --out outside the repo")

    layout = Layout(pathlib.Path(args.workdir))
    prepare_workdir(layout)
    proc = R0Process(binary, layout, args.port, docker=args.docker)
    ctx = Ctx(client=Client(proc.base), proc=proc, layout=layout)
    proc.start()
    try:
        run_cases(ctx)
    except BaseException:
        # Do not let a failed stop mask the case's own error.
        with contextlib.suppress(Exception):
            proc.stop()
        raise
    check_grace_left(ctx.ids)
    proc.stop()
    print(f"stand-down rows after the stop: {check_standdown_rows(layout.db, ctx.ids)}")
    removed = scrub_one_boot_credentials(layout)
    print(f"removed one-boot credentials: {removed}")
    findings = scan_for_leaks(layout.store)
    if findings:
        for f in findings:
            print(f"LEAK: {f}", file=sys.stderr)
        raise CorpusError("the store holds a secret or host identity; nothing was copied")

    if out_store.exists():
        shutil.rmtree(out_store)
    shutil.copytree(layout.store, out_store, symlinks=False)
    manifest = build_manifest(tag, binary_sha, boot_spec(layout, args.port), out_store)
    # Keep what a previous recording wrote: record_r0_answers.py owns the answers
    # list and the volatile-field list.
    manifest_path = MANIFEST_PATH if out_store == STAGE1_DIR else out_store.parent / "manifest.json"
    if manifest_path.exists():
        with contextlib.suppress(ValueError):
            old = json.loads(manifest_path.read_text())
            for key in ("answers", "volatile_fields"):
                if key in old:
                    manifest[key] = old[key]
    write_manifest(manifest, manifest_path)
    (layout.workdir / "writer-calls.txt").write_text("\n".join(ctx.log) + "\n", encoding="utf-8")
    print(f"wrote {out_store} ({sum(f['size'] for f in manifest['stores'][STAGE1_REL])} bytes)")
    return 0


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="Write R2's stage-1 fixture corpus with the R0 release binary (F2-12).",
    )
    ap.add_argument(
        "--regenerate",
        action="store_true",
        help="actually boot R0 and (re)write the committed corpus (a manual act)",
    )
    ap.add_argument(
        "--dry-run", action="store_true", help="list every API call in order without a binary"
    )
    ap.add_argument("--tag", help="override ci/r0_release.txt (a re-cut only)")
    ap.add_argument("--binary", help="use this R0 binary instead of downloading the release asset")
    ap.add_argument("--cache", default="~/.cache/r2corpus", help="download cache directory")
    ap.add_argument(
        "--docker",
        action="store_true",
        help="run the linux/amd64 binary in a container (host name r2corpus)",
    )
    ap.add_argument(
        "--workdir",
        default=DEFAULT_WORKDIR,
        help="fixed work directory (its paths end up in the store)",
    )
    ap.add_argument("--port", type=int, default=DEFAULT_PORT)
    ap.add_argument("--out", help="write the store here instead of the committed stage1/")
    ap.add_argument(
        "--allow-foreign-binary",
        action="store_true",
        help="development only: drive a non-linux/amd64 R0 build; needs --out",
    )
    return ap  # fmt: skip


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.dry_run:
        dry_run()
        return 0
    if not args.regenerate:
        print(
            "nothing to do: pass --dry-run, or --regenerate to rewrite the corpus", file=sys.stderr
        )
        return 2
    try:
        return write_corpus(args)
    except CorpusError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
