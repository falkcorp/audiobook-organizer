# file: scripts/whisper_path_pipeline.py
# version: 1.4.0
# guid: c3e91a57-2d84-4f16-b7a9-0e5d8c4b6f13
# last-edited: 2026-11-17
#
# The path pipeline of a Whisper worker, shared by scripts/whisper_mlx_server.py
# (Mac, MLX) and scripts/whisper_server.py (everything else, faster-whisper).
# Neither server owns this code: it is the backend-independent half of the wire
# contract in internal/transcribe/path.go, and it must behave identically for
# both. What differs per server is only the model behind `transcribe(wav_path)`.
#
# The production server may not decode audio (standing rule), so it sends the
# PATH of a book's first audio file and the worker reads it through its own
# mount of the library, extracts the first N seconds with ffmpeg, and
# transcribes that. Everything here fails CLOSED: an unmapped, traversing or
# unreadable path is a per-job error, never a guess.
#
# What lives here:
#   * the stable per-job error codes (ERR_*) and PathJobError
#   * WHISPER_PATH_MAP parsing and the path-safety boundary (map_server_path)
#   * the root readability probe
#   * clip extraction (ffmpeg, format allow-list) and WAV validation
#   * request validation, the bounded extraction pool and batch handling
#   * the /health fields the Go gate reads, and the two path routes
#
# What does NOT live here: the model, the inference lock or process, the upload
# endpoints, the bind/port options. Those are the server's.
#
# One PathPipeline per server process. Its settings are instance attributes so a
# test (or a server) can change them; its pools are created lazily.

import asyncio
import concurrent.futures
import errno
import hashlib
import json
import logging
import ntpath
import os
import posixpath
import shutil
import subprocess
import tempfile
import threading
import time
import wave
from collections.abc import Callable
from typing import Annotated, Any

from fastapi import Body, HTTPException, Request
from starlette.responses import JSONResponse

log = logging.getLogger("whisper_path_pipeline")

PATH_MAP_ENV = "WHISPER_PATH_MAP"
DEFAULT_CLIP_SECONDS = 90
MAX_CLIP_SECONDS = 600
DEFAULT_EXTRACT_TIMEOUT_S = 120
DEFAULT_EXTRACT_WORKERS = 4
DEFAULT_HEALTH_PATH_TIMEOUT_S = 2.5

# Stable error CODES. internal/plugins/maintenance/intro_transcribe.go maps the
# prefix before the first ": " to a per-book status; free-form ffmpeg stderr
# is never parsed on the Go side. Keep the two lists in step.
#   path_unmapped   path refused, or its mapped root is not readable (lost mount)
#   not_found       mapped root readable but the file is not there
#   ffmpeg_error    ffmpeg failed or produced a WAV that does not parse: a DECODE
#                   verdict about the file (durable on the Go side)
#   ffmpeg_timeout  ffmpeg exceeded the timeout: NOT a verdict (NFS stall), retryable
#   bad_request     malformed job (bad seconds, missing path)
#   whisper_error   inference raised
#   container_rejected  ffmpeg refused the container (not on FORMAT_WHITELIST): a
#                   policy of this worker, not a verdict about the file; non-durable
#   transcribe_timeout  inference did not finish inside the per-clip limit and its
#                   decode process was killed. A verdict about the CLIP, not the
#                   worker. The Go side counts strikes per file and makes the
#                   result permanent at 2.
#   decoder_crashed  the decode process DIED while decoding this clip (a crash or
#                   OOM kill; the next clip gets a fresh one). A verdict about the
#                   CLIP, like transcribe_timeout: counted on the same strike counter.
#   decoder_killed  the decode process was killed from OUTSIDE while on this clip
#                   (SIGKILL or SIGTERM: macOS jetsam under memory pressure, an
#                   operator), or could not be reaped to tell. Not a verdict on its
#                   own: the Go side counts it as a timeout strike, and the file
#                   turns permanent only when a second, DIFFERENT machine (see
#                   machine_id in /health) also times out or is killed on it.
#   request_abandoned  the client's request timeout (X-Whisper-Request-Timeout-S)
#                   passed before this clip started, so it was never run. Not a
#                   verdict about the book: the client has already left, and the
#                   Go side treats it as no answer.
#   worker_misconfigured  this worker cannot do the job at all (ffmpeg not on
#                   PATH, or the inference process cannot START): a fault of the
#                   worker, never a verdict about a book; the Go side defers the
#                   book and writes nothing
ERR_PATH_UNMAPPED = "path_unmapped"
ERR_NOT_FOUND = "not_found"
ERR_FFMPEG = "ffmpeg_error"
ERR_FFMPEG_TIMEOUT = "ffmpeg_timeout"
ERR_BAD_REQUEST = "bad_request"
ERR_WHISPER = "whisper_error"
ERR_WORKER_MISCONFIGURED = "worker_misconfigured"
ERR_CONTAINER_REJECTED = "container_rejected"
ERR_TRANSCRIBE_TIMEOUT = "transcribe_timeout"
ERR_DECODER_CRASHED = "decoder_crashed"
ERR_DECODER_KILLED = "decoder_killed"
ERR_REQUEST_ABANDONED = "request_abandoned"
# This worker's filesystem cannot represent the path the server named (a Windows
# worker and a title with a colon, a trailing dot or a device name). Distinct from
# path_unmapped, which means "not under a mapped root / root lost": the file is
# fine and a POSIX worker can read it. Only a non-POSIX worker emits it.
ERR_PATH_UNREPRESENTABLE = "path_unrepresentable"

# Demuxers ffmpeg may use on a library file. An allow-list, not a deny-list: the
# share is writable by anyone who can drop a file in it, and a planted .m3u8 /
# .sdp / .concat "audio file" would otherwise make ffmpeg's playlist demuxers
# open arbitrary paths on this worker. Names are ffmpeg demuxer names, covering
# every container in intro_transcribe.go's transcribableExtSet (.wma is asf).
# There is no separate "opus" demuxer: Ogg Opus is read by "ogg".
FORMAT_WHITELIST = "mov,mp4,m4a,3gp,3g2,mj2,mp3,flac,ogg,wav,aac,matroska,webm,asf"

# Substrings of ffmpeg stderr that mean the READ failed (a dropped or stalling
# NFS mount), not that the file cannot be decoded. Matched case-insensitively.
IO_FAULT_MARKERS = (
    "input/output error",
    "operation timed out",
    "connection timed out",
    "stale file handle",
    "device not configured",
    "no such device",
    "transport endpoint",
)

# ffmpeg's text when -format_whitelist refuses the input's container.
WHITELIST_REJECTION_MARKER = "not on whitelist"

# WHISPER_PATH_REQUIRE_MOUNT=0 turns off the "mapped root must be a mount point"
# check in root_readable (tests, or a map that deliberately points at a plain
# directory). Read live, default on.
REQUIRE_MOUNT_ENV = "WHISPER_PATH_REQUIRE_MOUNT"

# Every argument that changes the extracted WAV. Hashed into /health's
# extract_fingerprint, which the Go client folds into path-job journal keys
# only (the decode fingerprint is untouched, so upload-path entries stay valid).
# Changing ANY of these orphans every journalled path transcript.
EXTRACT_SETTINGS = {
    "args": [
        "-format_whitelist",
        FORMAT_WHITELIST,
        "-ss",
        "0",
        "-vn",
        "-ac",
        "1",
        "-ar",
        "16000",
        "-f",
        "wav",
    ],
    "max_seconds": MAX_CLIP_SECONDS,
}
EXTRACT_FINGERPRINT = hashlib.sha256(
    json.dumps(EXTRACT_SETTINGS, sort_keys=True).encode()
).hexdigest()[:16]

# Bytes per second of the 16 kHz mono 16-bit PCM this worker extracts; only used
# to estimate the length of an upload whose header cannot be read.
_PCM_BYTES_PER_S = 32000

REQUEST_TIMEOUT_HEADER = "X-Whisper-Request-Timeout-S"
ABANDONED_DETAIL = "the client's request timeout passed before this clip started"
# Larger than any real ceiling; keeps time.monotonic() + n finite for a header
# of absurd length instead of raising OverflowError.
_MAX_REQUEST_TIMEOUT_S = 10**9


class PathJobError(Exception):
    """A per-job failure carrying one of the stable ERR_* codes."""

    def __init__(self, code: str, detail: str):
        super().__init__(f"{code}: {detail}")
        self.code = code


class ChildFaultError(RuntimeError):
    """The inference process could not START: a fault of THIS WORKER, never a
    statement about the clip being decoded (no clip was ever sent to it).
    `starting` is True for exactly that case, and it is the case where every later
    clip in the same request would hit the same wall, so callers stop sending it
    clips. A decode process that DIES while decoding a clip is not this: that is a
    verdict about the clip (PathJobError, decoder_crashed), because a clip that
    deterministically kills it must be able to settle. The flag stays so a
    handler can tell the two apart if a start-less fault is ever raised again."""

    def __init__(self, message: str, *, starting: bool, category: str | None = None):
        super().__init__(message)
        self.starting = starting
        # A short fixed word for the UNAUTHENTICATED /health (child_start_error) and for
        # job answers; the message, which can carry a path or a library's exception
        # text, goes to the log only. None is a fault with no word of its own, which
        # misconfigured_error answers with the message: the Mac worker's faults, whose
        # text is pinned (test_whisper_mlx_pins) and is not changed by this.
        self.category = category


def misconfigured_error(e: "ChildFaultError") -> dict:
    """The job answer for a worker that cannot decode: the stable code and the fault's
    fixed category word, NEVER the exception text. A job answer reaches whoever can
    reach the port (there is no authentication), and the message can carry a path or a
    library's exception text; it is in the worker log, where the caller of this
    function has already written it. The same word /health shows as child_start_error.
    A fault with no category (the Mac worker's) is answered with its message, as before."""
    return error_result(PathJobError(ERR_WORKER_MISCONFIGURED, e.category or str(e)))


# Limits on what a network request may ask for. The Go client never comes near
# them (it sends whisper_batch_size jobs, default 16, with book-ID keys and
# absolute library paths); they exist so a request that is not the Go client's
# cannot make this worker allocate or queue without bound.
MAX_PATH_CHARS = 4096  # PATH_MAX on the server that names these files
MAX_KEY_CHARS = 512
MAX_JOBS_PER_REQUEST = 1024
MAX_BODY_BYTES = 8 * 1024 * 1024

# Windows will not name a file with these as its base name, with or without an
# extension ("NUL.m4b" is the NUL device). The superscript digits count too.
_NT_RESERVED = frozenset(
    {"con", "prn", "aux", "nul", "conin$", "conout$"}
    | {f"{dev}{n}" for dev in ("com", "lpt") for n in "0123456789\u00b9\u00b2\u00b3"}
)
_NT_BAD_CHARS = frozenset('<>:"|?*\\')
# Beyond this a Windows path needs the \\?\ form for tools that are not
# long-path aware (ffmpeg's manifest is not known to be).
NT_EXTENDED_FROM = 240


def _is_nt(pathmod: Any) -> bool:
    return getattr(pathmod, "__name__", "") == "ntpath"


def component_problem(part: str, pathmod: Any = None) -> str | None:
    """Why `part` (one component of a SERVER path) cannot safely be appended to a
    local root on this worker's filesystem, or None.

    On POSIX every component the server can have is addressable, so this is None.
    On Windows a component from the network is hostile input to an API that
    rewrites names: a backslash is a separator the server's own "/" split never
    saw, ":" is a drive or an alternate data stream, a trailing dot or space is
    stripped (so ".. " IS ".."), and a device name opens the device, not a file.
    A path refused here exists on the server, so the caller reports it as
    path_unrepresentable (retried, never a verdict on the book)."""
    pathmod = pathmod or os.path
    if not _is_nt(pathmod):
        return None
    if len(part) > 255:
        return "component is longer than 255 characters"
    if any(ord(c) < 32 or c in _NT_BAD_CHARS for c in part):
        return "component has a character Windows cannot use in a name"
    if part[-1] in ". ":
        return "component ends with a dot or a space, which Windows strips"
    if part.split(".", 1)[0].rstrip(" ").lower() in _NT_RESERVED:
        return "component is a reserved Windows device name"
    return None


def strip_extended_prefix(path: str) -> str:
    r"""Drop a Windows extended-length prefix: \\?\C:\x gives C:\x and
    \\?\UNC\h\s\x gives \\h\s\x (a no-op on any other string). realpath may
    hand back either spelling of the same file."""
    if path[:8].upper() == "\\\\?\\UNC\\":
        return "\\\\" + path[8:]
    if path.startswith(("\\\\?\\", "\\\\.\\")):
        return path[4:]
    return path


def to_extended(path: str) -> str:
    r"""The extended-length (\\?\) spelling of an absolute Windows path (a no-op
    if it already is one)."""
    if path.startswith("\\\\?\\"):
        return path
    if path.startswith("\\\\"):
        return "\\\\?\\UNC\\" + path[2:]
    return "\\\\?\\" + path


def is_within(root: str, path: str, pathmod: Any = None) -> bool:
    """True when `path` is `root` or lies under it, by whole components. Both
    must already be absolute and resolved. Case-insensitive on Windows (NTFS and
    SMB are), exact on POSIX."""
    pathmod = pathmod or os.path
    sep = pathmod.sep
    r, p = strip_extended_prefix(root), strip_extended_prefix(path)
    if _is_nt(pathmod):
        r, p = ntpath.normcase(r), ntpath.normcase(p)
    r, p = r.rstrip(sep), p.rstrip(sep)
    return p == r or p.startswith(r + sep)


def _parse_local_root(label: str, local: str, pathmod: Any) -> str:
    """Validate and normalise the LOCAL side of one map entry for `pathmod`'s flavor.

    `label` is "entry N". Error text never quotes the entry: it is what /health
    shows to any caller, and a local side can name a host and share."""
    if _is_nt(pathmod):
        drive, rest = ntpath.splitdrive(local)
        extended = local.startswith(("\\\\?\\", "\\\\.\\"))
        unc = drive.startswith("\\\\")
        if unc and "\\" not in drive[2:]:
            drive = ""  # \\host with no share is not a path
        # A bare drive ("X:") is drive-RELATIVE: it resolves against the process's
        # per-drive working directory. A UNC share root has an empty rest and is fine.
        if not drive or extended or (rest[:1] not in ("\\", "/") and not (unc and rest == "")):
            raise ValueError(
                f"{label}: the local side must be a drive path (X:\\dir, with the backslash) "
                "or a UNC share (\\\\host\\share\\dir), without a \\\\?\\ prefix"
            )
        if ".." in ntpath.normcase(local).replace("/", "\\").split("\\"):
            raise ValueError(f"{label}: the local side must be absolute with no '..'")
        return ntpath.normpath(local)
    if not local.startswith("/") or ".." in local.split("/"):
        raise ValueError(f"{label}: the local side must be absolute with no '..'")
    local = posixpath.normpath(local)
    if local == "/":
        raise ValueError(f"{label} maps the filesystem root")
    return local


def parse_path_map(raw: str, pathmod: Any = None) -> list[tuple[str, str]]:
    """Parse WHISPER_PATH_MAP into [(server_prefix, local_root), ...].

    Raises ValueError on ANY malformed entry: a half-understood map is a map
    that sends some jobs to the wrong place, so the whole thing is refused.
    The SERVER side is always a POSIX absolute path (the production server's),
    matched exactly. The LOCAL side is absolute in `pathmod`'s flavor (this
    worker's: POSIX, or a drive path or UNC share on Windows), no "..". Neither
    side may be the filesystem root "/". A Windows drive root (X:\\) or a UNC
    share root is allowed as a local root: the operator names it deliberately,
    and a network library is usually exactly that. Longest server prefix first.
    """
    pathmod = pathmod or os.path
    pairs: list[tuple[str, str]] = []
    # Errors name the entry by number, never by value: they reach /health.
    for number, entry in enumerate((e.strip() for e in raw.split(",") if e.strip()), 1):
        label = f"entry {number}"
        server, sep, local = entry.partition("=")
        server, local = server.strip(), local.strip()
        if not sep or not server or not local:
            raise ValueError(f"{label} is not server-path=local-path")
        if not server.startswith("/") or ".." in server.split("/"):
            raise ValueError(f"{label}: the server side must be absolute with no '..'")
        server = posixpath.normpath(server)
        if server == "/":
            raise ValueError(f"{label} maps the filesystem root")
        pairs.append((server, _parse_local_root(label, local, pathmod)))
    pairs.sort(key=lambda p: len(p[0]), reverse=True)
    return pairs


def check_seconds(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= MAX_CLIP_SECONDS:
        raise PathJobError(ERR_BAD_REQUEST, f"seconds must be an integer in 1..{MAX_CLIP_SECONDS}")
    return value


def error_result(e: Exception) -> dict:
    return {"text": "", "error": str(e)}


def abandoned_result() -> dict:
    return error_result(PathJobError(ERR_REQUEST_ABANDONED, ABANDONED_DETAIL))


def abandon_deadline(request: Request) -> float | None:
    """The monotonic time after which the client has given up on this request, from
    X-Whisper-Request-Timeout-S (whole seconds, counted from now: the handler's
    entry). A missing, empty, non-numeric or non-positive header is no deadline,
    which is today's behaviour: only ASCII digits are read, so "1.5", "-5" and
    "1_0" are ignored rather than half-understood."""
    raw = (request.headers.get(REQUEST_TIMEOUT_HEADER) or "").strip()
    if not (raw.isascii() and raw.isdigit()):
        return None
    # int() of a header past Python's 4300-digit limit raises ValueError (a 500);
    # anything over 12 digits is past the cap anyway.
    seconds = _MAX_REQUEST_TIMEOUT_S if len(raw) > 12 else min(int(raw), _MAX_REQUEST_TIMEOUT_S)
    if seconds <= 0:
        return None
    return time.monotonic() + seconds


def past(deadline: float | None) -> bool:
    return deadline is not None and time.monotonic() >= deadline


def audio_seconds(path: str) -> float:
    """Length of the clip in seconds, from the WAV header (frames / rate), not
    from the length the caller asked for: the clip can be shorter than asked
    (a file shorter than the clip length), capped by what the file's size can
    hold. Falls back to a size estimate for a file that is not a parseable WAV."""
    try:
        by_size = os.path.getsize(path) / _PCM_BYTES_PER_S
    except OSError:
        return 0.0
    try:
        with wave.open(path, "rb") as w:
            rate = w.getframerate()
            if rate > 0:
                # The header is the caller's claim, the size is the file's. A
                # header that claims more frames than the file can hold would
                # stretch the clip's deadline (a forged or truncated WAV), so the
                # deadline is never longer than the bytes on disk allow. The
                # clips this worker cuts are 16 kHz mono 16-bit, where the two
                # agree exactly.
                return min(w.getnframes() / rate, by_size)
    except (wave.Error, EOFError, OSError):
        pass
    return by_size


def _ffmpeg_tail(stderr: bytes) -> str:
    lines = [ln.strip() for ln in stderr.decode("utf-8", "replace").splitlines() if ln.strip()]
    return (lines[-1] if lines else "no output")[:200]


def validate_wav(path: str) -> None:
    """Non-empty and parses as the 16 kHz mono PCM we asked for."""
    try:
        if os.path.getsize(path) <= 44:
            raise PathJobError(ERR_FFMPEG, "ffmpeg produced an empty clip")
        with wave.open(path, "rb") as w:
            if w.getnframes() <= 0 or w.getframerate() != 16000 or w.getnchannels() != 1:
                raise PathJobError(ERR_FFMPEG, "ffmpeg produced an invalid WAV")
    except (wave.Error, EOFError, OSError) as e:
        raise PathJobError(ERR_FFMPEG, f"invalid WAV: {e}")


def discard_extraction(fut: concurrent.futures.Future) -> None:
    """Abandon an extraction without waiting for it, and without leaking its clip.
    A queued one is cancelled and never runs; one already running cannot be, so
    its tmpdir is removed by a callback when it finishes (add_done_callback runs
    the callback at once if it already has)."""
    if fut.cancel():
        return

    def remove_clip(done: concurrent.futures.Future) -> None:
        if done.cancelled() or done.exception() is not None:
            return  # extract_clip cleans up after its own failure
        shutil.rmtree(done.result()[0], ignore_errors=True)

    fut.add_done_callback(remove_clip)


class PathPipeline:
    """The path pipeline of one worker process (see the module header)."""

    def __init__(self) -> None:
        self.extract_timeout_s: int = int(
            os.environ.get("WHISPER_EXTRACT_TIMEOUT", str(DEFAULT_EXTRACT_TIMEOUT_S))
        )
        self.extract_workers: int = max(
            1, int(os.environ.get("WHISPER_EXTRACT_WORKERS", str(DEFAULT_EXTRACT_WORKERS)))
        )
        # /health runs on the event loop, NOT in the anyio threadpool the sync
        # handlers share (40 threads by default): a long batch queues handlers
        # on the server's inference lock, each parked in a threadpool thread, and
        # a /health that needed a thread too would queue behind them -- the Go
        # liveness probe would then read a busy worker as a dead one. Only the
        # two calls that can block (the root listing and the ffmpeg lookup) go
        # to this dedicated pool, bounded by a deadline a little under the Go
        # probe's 3 s. A hung mount strands at most these two threads. /health
        # then still answers 200: the Go liveness poller and its stuck-detection
        # read busy_seconds / busy_limit_seconds from this body, and a 503 would
        # hide them while the worker may be wedged. The reply says the
        # filesystem could not be checked (path_health_error), reports every
        # mapped root as not readable so path jobs are refused, and omits
        # "ffmpeg", which is unknown.
        self.health_timeout_s: float = DEFAULT_HEALTH_PATH_TIMEOUT_S
        self._health_pool = concurrent.futures.ThreadPoolExecutor(
            max_workers=2, thread_name_prefix="health"
        )
        self._extract_pool: concurrent.futures.ThreadPoolExecutor | None = None
        self._extract_pool_lock = threading.Lock()
        # The path flavor of THIS worker's filesystem (ntpath on Windows). Only
        # the lexical checks consult it, so a test can run them for the other
        # flavor; everything that touches the disk uses the real os.path.
        self.pathmod: Any = os.path

    # ── the map and the safety boundary ──────────────────────────────────────

    def path_map(self) -> tuple[list[tuple[str, str]], str]:
        """The live (pairs, error) from the environment. Read per call, not cached:
        health must describe what a job would do right now."""
        raw = os.environ.get(PATH_MAP_ENV, "")
        try:
            return parse_path_map(raw, self.pathmod), ""
        except ValueError as e:
            return [], f"{PATH_MAP_ENV} invalid: {e}"

    def root_readable(self, root: str) -> bool:
        """True when `root` can be listed right now. A directory read, not isdir: a
        lost NFS mount leaves the mountpoint directory behind, so isdir stays true
        while every read fails. scandir stopping at the first entry, not listdir:
        the root holds the whole library, and building that list on every /health
        poll is wasted work on a mount that is already slow.

        The root must also still be a MOUNT POINT. A soft, read-only NFS mount that
        stays unresponsive past its deadtimeout is forcibly unmounted (man
        mount_nfs), which leaves the empty mountpoint directory behind: scandir on
        it succeeds, and every job would then report not_found. ismount is False
        there, so the root is correctly reported unreadable."""
        # Windows has no soft-unmount that leaves an empty mountpoint behind, and
        # ntpath.ismount is true only for a drive or share ROOT, so a root such as
        # X:\audiobooks would never pass: the check is POSIX-only by default.
        require_mount = os.environ.get(REQUIRE_MOUNT_ENV, "0" if _is_nt(self.pathmod) else "1")
        if require_mount != "0" and not os.path.ismount(root):
            return False
        try:
            with os.scandir(root) as entries:
                next(entries, None)
            return True
        except OSError:
            return False

    def mapped_root_of(self, local_path: str) -> str | None:
        """The mapped local root that `local_path` lives under, or None."""
        pairs, _ = self.path_map()
        for _server, local in pairs:
            # Both spellings: map_server_path hands ffmpeg the REALPATH, which
            # differs from the configured root when the mountpoint sits under a symlink.
            for base in (local, os.path.realpath(local)):
                if is_within(base, local_path, self.pathmod):
                    return local
        return None

    def split_server_path(
        self, path: Any, pairs: list[tuple[str, str]]
    ) -> tuple[str, str, list[str]]:
        """The lexical half of mapping: (server_prefix, local_root, remaining
        components) for the longest matching prefix, or PathJobError. Touches no
        filesystem, so it is testable for either path flavor.

        Matching is by path COMPONENT: /srv/mediaX/a must not match /srv/media.
        The server's path is always POSIX, so it is split on "/" only, then every
        component is checked against what THIS worker's filesystem can address
        (component_problem): on Windows a backslash, a drive colon, a device name
        or a trailing dot would otherwise change what the joined path means.
        """
        if not isinstance(path, str) or not path or "\x00" in path:
            raise PathJobError(ERR_BAD_REQUEST, "path must be a non-empty string")
        if len(path) > MAX_PATH_CHARS:
            raise PathJobError(ERR_BAD_REQUEST, f"path is longer than {MAX_PATH_CHARS} characters")
        if not path.startswith("/"):
            raise PathJobError(ERR_PATH_UNMAPPED, "path is not absolute")
        parts = [p for p in path.split("/") if p not in ("", ".")]
        if ".." in parts:
            raise PathJobError(ERR_PATH_UNMAPPED, "path contains '..'")
        for server, local in pairs:
            sparts = server.strip("/").split("/")
            if parts[: len(sparts)] != sparts:
                continue
            rest = parts[len(sparts) :]
            for part in rest:
                problem = component_problem(part, self.pathmod)
                if problem:
                    raise PathJobError(ERR_PATH_UNREPRESENTABLE, problem)
            return server, local, rest
        raise PathJobError(ERR_PATH_UNMAPPED, "path is not under a mapped prefix")

    def map_server_path(self, path: Any, pairs: list[tuple[str, str]]) -> str:
        """Translate a server path to its local path, or raise PathJobError.

        See split_server_path for the lexical rules. On top of those, a mapped
        path whose realpath leaves its root (a symlink or junction out of the
        share) is refused. A mapped root that cannot be listed is a lost mount:
        path_unmapped, so the caller keeps the book retryable rather than burying it.
        """
        server, local, rest = self.split_server_path(path, pairs)
        if not self.root_readable(local):
            raise PathJobError(
                ERR_PATH_UNMAPPED, f"mapped root for {server} is not readable (mount lost?)"
            )
        mapped = os.path.join(local, *rest)
        real_root = os.path.realpath(local)
        real = os.path.realpath(mapped)
        if not is_within(real_root, real, self.pathmod):
            raise PathJobError(ERR_PATH_UNMAPPED, "path resolves outside its mapped root")
        if _is_nt(self.pathmod) and len(real) >= NT_EXTENDED_FROM:
            real = to_extended(real)  # past MAX_PATH the plain spelling is not openable
        # The resolved path, not `mapped`: the file itself is no longer a symlink.
        # This narrows the window, it does not close it: a DIRECTORY component of
        # the resolved path can still be swapped for a symlink between this check
        # and ffmpeg's open by someone who can write to the share. The impact is
        # bounded (a whitelisted media file elsewhere on this worker, transcript
        # only).
        return real

    def prepare_path_job(self, job: Any) -> tuple[str, int]:
        """Validate one job and return (local_path, seconds); raises PathJobError."""
        if not isinstance(job, dict):
            raise PathJobError(ERR_BAD_REQUEST, "job must be an object")
        seconds = check_seconds(job.get("seconds", DEFAULT_CLIP_SECONDS))
        pairs, perr = self.path_map()
        if not pairs:
            raise PathJobError(
                ERR_PATH_UNMAPPED, perr or f"{PATH_MAP_ENV} is not set on this worker"
            )
        return self.map_server_path(job.get("path"), pairs), seconds

    # ── extraction ───────────────────────────────────────────────────────────

    def _raise_ffmpeg_failure(self, src: str, stderr: bytes) -> None:
        """Classify a nonzero ffmpeg exit. ffmpeg_error is a DURABLE verdict on the
        Go side (the book is hidden until its mtime changes), so it is reserved for
        a file that is still readable and still will not decode. A read fault
        mid-decode (the NFS mount dropped, the handle went stale) exits nonzero
        exactly like a corrupt file, so the evidence is re-checked after the fact:

          the mapped root is no longer listable, or the file is gone -> path_unmapped
          stderr names an I/O fault or timeout                       -> ffmpeg_timeout
          ffmpeg refused the container (format whitelist)            -> container_rejected
          otherwise                                                  -> ffmpeg_error
        """
        root = self.mapped_root_of(src)
        if (root is not None and not self.root_readable(root)) or not os.path.isfile(src):
            raise PathJobError(
                ERR_PATH_UNMAPPED, "source became unreadable while ffmpeg ran (mount lost?)"
            )
        text = stderr.decode("utf-8", "replace").lower()
        if any(marker in text for marker in IO_FAULT_MARKERS):
            raise PathJobError(
                ERR_FFMPEG_TIMEOUT, f"read fault during decode: {_ffmpeg_tail(stderr)}"
            )
        if WHITELIST_REJECTION_MARKER in text:
            raise PathJobError(
                ERR_CONTAINER_REJECTED,
                "container is not on this worker's format whitelist (renamed or unusual file)",
            )
        raise PathJobError(ERR_FFMPEG, _ffmpeg_tail(stderr))

    def extract_clip(self, src: str, seconds: int) -> tuple[str, str]:
        """Extract the first `seconds` of `src` to a validated 16 kHz mono WAV.

        Returns (tmpdir, wav_path); the CALLER removes tmpdir. ffmpeg writes a name
        inside a fresh mkdtemp directory with -y, never a NamedTemporaryFile (which
        already exists, so ffmpeg without -y refuses to overwrite it).
        """
        if not os.path.isfile(src):
            raise PathJobError(ERR_NOT_FOUND, "file not found under the mapped root")
        ffmpeg = shutil.which("ffmpeg")
        if not ffmpeg:
            raise PathJobError(ERR_WORKER_MISCONFIGURED, "ffmpeg is not on PATH on this worker")
        tmpdir = tempfile.mkdtemp(prefix="whisper-clip-")
        wav = os.path.join(tmpdir, "clip.wav")
        try:
            try:
                proc = subprocess.run(
                    [
                        ffmpeg,
                        "-nostdin",
                        "-y",
                        "-v",
                        "error",
                        "-format_whitelist",
                        FORMAT_WHITELIST,
                        "-ss",
                        "0",
                        "-t",
                        str(seconds),
                        # Local files only: no network protocol can be reached through
                        # a container that references one. Not in EXTRACT_SETTINGS:
                        # it does not change the WAV, so the fingerprint stays.
                        "-protocol_whitelist",
                        "file",
                        "-i",
                        src,
                        "-vn",
                        "-ac",
                        "1",
                        "-ar",
                        "16000",
                        "-f",
                        "wav",
                        wav,
                    ],
                    stdin=subprocess.DEVNULL,
                    capture_output=True,
                    timeout=self.extract_timeout_s,
                )
            except subprocess.TimeoutExpired:
                raise PathJobError(ERR_FFMPEG_TIMEOUT, f"ffmpeg exceeded {self.extract_timeout_s}s")
            except OSError as e:
                raise PathJobError(
                    ERR_FFMPEG_TIMEOUT if e.errno in (errno.EIO, errno.ETIMEDOUT) else ERR_FFMPEG,
                    str(e),
                )
            if proc.returncode != 0:
                self._raise_ffmpeg_failure(src, proc.stderr)
            validate_wav(wav)
            return tmpdir, wav
        except BaseException:
            shutil.rmtree(tmpdir, ignore_errors=True)
            raise

    def get_extract_pool(self) -> concurrent.futures.ThreadPoolExecutor:
        """One bounded pool per process, so concurrent requests share the cap
        instead of each fanning out its own ffmpegs. Inference still serializes
        on the server's own lock; extraction of job N+1 overlaps inference of
        job N."""
        with self._extract_pool_lock:
            if self._extract_pool is None:
                self._extract_pool = concurrent.futures.ThreadPoolExecutor(
                    max_workers=self.extract_workers, thread_name_prefix="extract"
                )
            return self._extract_pool

    # ── batch handling ───────────────────────────────────────────────────────

    def run_path_jobs(
        self,
        jobs: dict[str, Any],
        deadline: float | None,
        transcribe: Callable[[str], str],
    ) -> dict[str, dict]:
        """Run every job; never raises. One result per key, errors isolated.

        `transcribe(wav_path)` is the server's model: it returns the text, or
        raises PathJobError (a coded verdict), ChildFaultError (it could not
        start), or any other exception (a whisper_error).

        Once the child is known not to start, or `deadline` (monotonic, see
        abandon_deadline) has passed, every remaining key is answered AT ONCE without
        waiting for its extraction, which can take up to extract_timeout_s on a slow
        mount: queued extractions are cancelled and running ones remove their own
        clip when they finish (discard_extraction)."""
        results: dict[str, dict] = {}
        pending: dict[str, concurrent.futures.Future] = {}
        pool = self.get_extract_pool()
        for key, job in jobs.items():
            try:
                src, seconds = self.prepare_path_job(job)
            except PathJobError as e:
                results[key] = error_result(e)
                continue
            except Exception as e:  # defensive: a mapping bug must not fail the batch
                results[key] = error_result(PathJobError(ERR_PATH_UNMAPPED, str(e)))
                continue
            pending[key] = pool.submit(self.extract_clip, src, seconds)
        start_fault: ChildFaultError | None = None
        try:
            for key, fut in pending.items():
                if start_fault is not None or past(deadline):
                    # The child never became ready (every further clip would wait out
                    # the same ready timeout), or the client gave up: answer
                    # without waiting for this clip's extraction.
                    results[key] = (
                        abandoned_result()
                        if start_fault is None
                        else misconfigured_error(start_fault)
                    )
                    discard_extraction(fut)
                    continue
                tmpdir = None
                try:
                    tmpdir, wav = fut.result()
                    if past(deadline):
                        # Extraction itself took past the deadline.
                        results[key] = abandoned_result()
                        continue
                    text = transcribe(wav)
                    log.info(f"transcribed path job {key}: {len(text)} chars")
                    results[key] = {"text": text, "error": None}
                except PathJobError as e:
                    log.error(f"path job {key} failed: {e}")
                    results[key] = error_result(e)
                except ChildFaultError as e:
                    # A child that cannot start is this worker's fault, not a verdict
                    # on the clip: the Go side defers worker_misconfigured and writes
                    # nothing, where whisper_error would be recorded as failed.
                    log.error(f"path job {key}: inference child fault: {e}")
                    results[key] = misconfigured_error(e)
                    if e.starting:
                        start_fault = e
                except Exception as e:
                    log.error(f"path job {key} failed: {e}")
                    results[key] = error_result(PathJobError(ERR_WHISPER, str(e)))
                finally:
                    if tmpdir:
                        shutil.rmtree(tmpdir, ignore_errors=True)
        finally:
            # A cancelled/aborted loop must not leak the clips still being cut.
            for key, fut in pending.items():
                if key not in results:
                    discard_extraction(fut)
        return results

    # ── /health ──────────────────────────────────────────────────────────────

    def path_health(self, probe: bool = True) -> dict:
        """The path pipeline's /health fields, computed live.

        path_pipeline means "a valid WHISPER_PATH_MAP is configured"; whether each
        mapped root is readable RIGHT NOW is path_roots. They are separate on
        purpose: configuration is the capability, a lost mount is a fault, and
        the Go client defers pages (writes nothing) while any root is unreadable
        instead of recording every book as failed.

        probe=False does not touch the filesystem: it is the answer when the listing
        did not return in time, and reports every mapped root as NOT readable (fail
        closed: nothing was seen to be readable), where probe=True lists each root.
        """
        pairs, err = self.path_map()
        out: dict = {
            "path_pipeline": bool(pairs),
            "path_prefixes": [server for server, _ in pairs],
            "path_roots": [
                {"prefix": server, "readable": probe and self.root_readable(local)}
                for server, local in pairs
            ],
            "extract_fingerprint": EXTRACT_FINGERPRINT,
        }
        if err:
            out["path_map_error"] = err
        return out

    def blocking_health_fields(self) -> dict:
        return {"ffmpeg": shutil.which("ffmpeg"), **self.path_health()}

    async def bounded_blocking_health_fields(self) -> dict:
        """blocking_health_fields on the health pool, or the fallback fields once
        health_timeout_s passes."""
        loop = asyncio.get_running_loop()
        try:
            return await asyncio.wait_for(
                loop.run_in_executor(self._health_pool, self.blocking_health_fields),
                self.health_timeout_s,
            )
        except asyncio.TimeoutError:
            return {
                **self.path_health(probe=False),
                "path_health_error": "filesystem checks did not answer in time",
            }

    async def health_payload(
        self,
        *,
        identity,
        model: str,
        backend: str,
        device: str,
        decode_fingerprint: str,
        busy: Callable[[], tuple[float, float | None]],
        start_error: Callable[[], str | None],
        clip_timeout_floor_s: float,
        clip_timeout_factor: float,
        child_ready_timeout_s: float,
        extra: dict | None = None,
    ) -> dict:
        """The /health body both workers share.

        The two waits run side by side, so the reply comes within the longer of
        them (health_timeout_s), not their sum, and inside the Go probe's 3 s.

        busy and start_error are read WITHOUT the server's inference lock (a
        wedged job holds it). busy_seconds is how long the current clip has been
        running (0 when idle); busy_limit_seconds is that clip's own limit. A
        busy_seconds past its limit means the deadline did not fire, and the Go
        client then refuses this endpoint. The clip_timeout_* fields let the
        client size its HTTP timeout to the worst case of a batch, and
        child_ready_timeout_s / extract_timeout_s are the two things a request
        can wait on BEFORE a clip's own clock starts, so the client's ceiling
        covers them too. child_start_error is present ONLY while the decoder's
        last start failed recently, absent (not null) otherwise.

        `extra` is merged last: the server's own additive fields.
        """
        blocking, machine = await asyncio.gather(
            self.bounded_blocking_health_fields(), identity.reported()
        )
        busy_seconds, busy_limit = busy()
        child_start_error = start_error()
        return {
            "status": "ok",
            "model": model,
            "batch_pipeline": True,
            "device": device,
            "backend": backend,
            "decode_fingerprint": decode_fingerprint,
            # Which machine this is, as an opaque hash. Absent while no stable
            # source has answered (see whisper_machine_id).
            **({"machine_id": machine} if machine else {}),
            "busy_seconds": round(busy_seconds, 1),
            "busy_limit_seconds": busy_limit,
            "clip_timeout_floor_s": clip_timeout_floor_s,
            "clip_timeout_factor": clip_timeout_factor,
            "child_ready_timeout_s": child_ready_timeout_s,
            "extract_timeout_s": self.extract_timeout_s,
            **({"child_start_error": child_start_error} if child_start_error else {}),
            **blocking,
            **(extra or {}),
        }


class _BodyTooLargeError(Exception):
    pass


class BodyLimitMiddleware:
    """Pure-ASGI cap on a request body for the given paths: 413 past `limit`
    bytes, whether the size is declared in Content-Length or only discovered by
    counting a chunked body. Not BaseHTTPMiddleware, which would buffer.

    FastAPI catches any exception raised while it reads the body and answers
    400 itself, so raising from `receive` is not enough: once the limit has been
    crossed the response is REPLACED, whatever the app tried to send, with a 413."""

    def __init__(self, app, paths: tuple[str, ...], limit: int) -> None:
        self.app, self.paths, self.limit = app, paths, limit

    async def __call__(self, scope, receive, send) -> None:
        if scope["type"] != "http" or scope["path"] not in self.paths:
            await self.app(scope, receive, send)
            return
        reply = JSONResponse({"detail": f"body is larger than {self.limit} bytes"}, 413)
        declared = dict(scope["headers"]).get(b"content-length", b"")
        if declared.isdigit() and int(declared) > self.limit:
            await reply(scope, receive, send)
            return
        seen = 0
        exceeded = False
        replied = False

        async def counting_receive():
            nonlocal seen, exceeded
            msg = await receive()
            if msg["type"] == "http.request":
                seen += len(msg.get("body", b""))
                if seen > self.limit:
                    exceeded = True
                    raise _BodyTooLargeError
            return msg

        async def replacing_send(msg):
            nonlocal replied
            if not exceeded:
                await send(msg)
            elif not replied:
                replied = True
                await reply(scope, receive, send)

        try:
            await self.app(scope, counting_receive, replacing_send)
        except _BodyTooLargeError:
            if not replied:
                await reply(scope, receive, send)


def install_path_routes(
    app,
    run_jobs: Callable[[dict[str, Any], float | None], dict[str, dict]],
    identity: Callable[[], dict] | None = None,
) -> tuple[Callable, Callable]:
    """Register /transcribe-path and /transcribe-path-batch on `app`.

    `identity`, when given, is called AFTER the jobs ran and its dict is returned as
    the response's "identity": the model, backend and fingerprints this worker
    decoded with, the same values /health reports. The Go client files each result
    under that, not under an earlier /health probe, so a worker process that came
    back with a different model between the probe and the request cannot have its
    text filed under the old one. Left out, the responses are exactly what they were.

    `run_jobs(jobs, deadline)` is the server's runner (PathPipeline.run_path_jobs
    bound to its model). Plain `def`, not `async def`: FastAPI runs these in its
    threadpool, so a long batch does not freeze the event loop and /health keeps
    answering. The Go pool probes /health with a 3 s timeout and refuses an
    endpoint it cannot read. Returns the two handlers.
    """

    app.add_middleware(
        BodyLimitMiddleware,
        paths=("/transcribe-path", "/transcribe-path-batch"),
        limit=MAX_BODY_BYTES,
    )

    @app.post("/transcribe-path")
    def transcribe_path(body: Annotated[dict, Body()]):
        """Single path job: {"path": ..., "seconds": 90} -> {"text","error"}."""
        result = run_jobs({"job": body}, None)["job"]
        return {**result, "identity": identity()} if identity else result

    @app.post("/transcribe-path-batch")
    def transcribe_path_batch(body: Annotated[dict, Body()], request: Request):
        """{"jobs": {key: {"path","seconds"}}} -> {"results": {key: {"text","error"}}}.

        Keys are echoed verbatim. Jobs are validated one by one, not through one
        request model, so a single malformed job cannot 422 the whole batch.

        X-Whisper-Request-Timeout-S, when sent, is the client's HTTP ceiling: jobs not
        yet started when it passes are answered as abandoned (see run_path_jobs).
        """
        jobs = body.get("jobs")
        if not isinstance(jobs, dict):
            raise HTTPException(status_code=422, detail='"jobs" must be an object')
        if len(jobs) > MAX_JOBS_PER_REQUEST:
            raise HTTPException(
                status_code=422, detail=f"at most {MAX_JOBS_PER_REQUEST} jobs per request"
            )
        if any(not isinstance(k, str) or len(k) > MAX_KEY_CHARS for k in jobs):
            raise HTTPException(
                status_code=422,
                detail=f"job keys must be strings of at most {MAX_KEY_CHARS} characters",
            )
        results = run_jobs(jobs, abandon_deadline(request))
        return {"results": results, "identity": identity()} if identity else {"results": results}

    return transcribe_path, transcribe_path_batch
