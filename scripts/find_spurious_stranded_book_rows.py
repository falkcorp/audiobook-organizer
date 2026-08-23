#!/usr/bin/env python3
# file: scripts/find_spurious_stranded_book_rows.py
# version: 1.1.0
# guid: 7c90e537-566f-4c72-ae73-033055c31c4a
# last-edited: 2026-08-23
"""REPORT-ONLY scan for Book rows the .tmp-rename path-construction bug may
have spuriously created (TODO.md: "Investigate book rows affected as a side
effect").

From 2026-03-03 to 2026-08-15 a slash leaked into segment_title_format and
turned "<title> - <track>/<total_tracks>" into a real path separator (see
scripts/repair_stranded_tracks.py's module docstring for the full mechanism).
Every such single-file directory looked, to the scanner, like a tiny
audiobook of its own -- scrubVar's own comment (internal/organizer/
path_format.go) records ONE real book turning into 85 separate Book rows.

This script does NOT touch any of those rows. It only counts them, so a human
can decide what -- if anything -- to do about soft-delete/purge archaeology.
Per the owner's standing rule (never delete files/rows in any repair) and
this item's own text ("Report counts before proposing any restore. Do not
mass-restore rows."), there is no --fix/--apply flag here and there never
should be one.

WHAT THIS SCRIPT DOES
    1. Walks a library root on disk with repair_stranded_tracks.find_bogus_dirs
       (imported, not reimplemented) to get the set of wreckage directories the
       bug created -- e.g. ".../Project Hail Mary - 24" holding one "31.mp3".
    2. Pages through the live GET /api/v1/audiobooks API to enumerate every
       Book row (id, title, file_path) -- including rows whose file no longer
       exists on disk, which is the whole point: the filesystem side alone
       cannot explain a row that only the DB remembers.
    3. Tests two independent, purely-textual heuristics per row:
         (a) title is bare digits ("85")
         (b) file_path's containing directory name ends " - <digits>"
             ("Project Hail Mary - 24"), the exact shape the bug produces
    4. Cross-references both heuristics against the affected-directory set
       from step 1 and reports counts (see CATEGORIES below), plus the
       matching rows themselves, to a JSON report file. Nothing is mutated.

CATEGORIES (mutually exclusive, sum to total_books_scanned)
    Both heuristics are noisy alone -- a real book can be titled a bare number
    ("1984") and a real multi-disc folder can be named "Disc - 2" -- so a hit
    is reported at HIGH confidence only when it also lands inside a directory
    find_bogus_dirs already flagged as wreckage, and at LOW confidence
    (a separate bucket, not folded into the high-confidence count) otherwise:

        in_affected_dir_numeric_title_only   -- HIGH confidence
        in_affected_dir_path_segment_only    -- HIGH confidence
        in_affected_dir_both_heuristics      -- HIGH confidence
        in_affected_dir_neither_heuristic    -- in the wreckage footprint but
                                                 neither textual heuristic
                                                 fired; worth a closer look
        numeric_title_outside_affected_dir   -- LOW confidence / likely
                                                 false positive (e.g. "1984")
        path_segment_outside_affected_dir    -- LOW confidence / likely a
                                                 legitimate multi-disc layout
        no_signal                            -- neither heuristic fired and
                                                 not in an affected directory

AUTH
    Reads the long-lived API key from --token-file (default
    ~/.config/audiobook-organizer/api-key, a bare abk_... token). Falls back
    to an "api_key=..." line for parity with scripts/transcribe_monitor.py's
    .api-token format, in case a worktree-scoped token is passed instead.

USAGE
    python3 scripts/find_spurious_stranded_book_rows.py /mnt/bigdata/books \\
        --base https://<server>:8484 --insecure

    python3 scripts/find_spurious_stranded_book_rows.py --help
"""

from __future__ import annotations

import argparse
import json
import os
import re
import ssl
import sys
import urllib.error
import urllib.request
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import PurePath

# repair_stranded_tracks.py sits next to this file and exposes
# find_bogus_dirs() as a pure function (no side effects on import -- its
# main() is guarded by `if __name__ == "__main__"`). Reuse it rather than
# reimplementing the wreckage-directory detector.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from repair_stranded_tracks import find_bogus_dirs  # noqa: E402

DEFAULT_TOKEN_FILE = os.path.expanduser("~/.config/audiobook-organizer/api-key")
DEFAULT_PAGE_SIZE = 500

# (a) a title that is nothing but digits, e.g. "85". Matched with
# re.fullmatch, not this compiled pattern directly -- kept as a plain
# r"\d+" fragment so is_numeric_title's re.fullmatch(r"\d+", ...) call and
# this docstring stay in sync if the shape ever changes.
NUMERIC_TITLE_PATTERN = r"\d+"
# (b) the containing directory ends " - <digits>", the exact shape
# repair_stranded_tracks.DIR_RE matches ("<title> - <track>").
PATH_SEGMENT_RE = re.compile(r" - \d+$")

CATEGORY_KEYS = (
    "in_affected_dir_numeric_title_only",
    "in_affected_dir_path_segment_only",
    "in_affected_dir_both_heuristics",
    "in_affected_dir_neither_heuristic",
    "numeric_title_outside_affected_dir",
    "path_segment_outside_affected_dir",
    "no_signal",
)


@dataclass
class Report:
    generated_at: str
    library_root: str
    api_base: str
    total_books_scanned: int
    affected_directories_found: int
    counts: dict = field(default_factory=dict)
    matches: dict = field(default_factory=dict)
    notes: list = field(default_factory=list)


# --- auth -------------------------------------------------------------------


def read_token(token_file: str) -> str:
    """Read the API key from token_file.

    Accepts either a bare token (the ~/.config/audiobook-organizer/api-key
    convention -- a single abk_... line) or an "api_key=..." line (the
    .api-token multi-line convention used by scripts/transcribe_monitor.py),
    so a worktree-scoped token file works here too.
    """
    try:
        with open(token_file) as f:
            lines = [ln.strip() for ln in f if ln.strip()]
    except OSError as e:
        print(f"FATAL: cannot read token file {token_file}: {e}", file=sys.stderr)
        sys.exit(2)
    for ln in lines:
        if ln.startswith("api_key="):
            return ln.split("=", 1)[1].strip()
    if len(lines) == 1 and "=" not in lines[0]:
        return lines[0]
    print(f"FATAL: no bare token or api_key= line found in {token_file}", file=sys.stderr)
    sys.exit(2)


# --- API client (report-only: GET requests only, never POST/PUT/DELETE) -----


def _http_get_json(url: str, token: str, insecure: bool, timeout: int = 30) -> dict:
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    ctx = ssl._create_unverified_context() if insecure else None
    with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
        return json.loads(resp.read().decode())


def fetch_all_books(base: str, token: str, insecure: bool, page_size: int = DEFAULT_PAGE_SIZE):
    """Page through GET /api/v1/audiobooks and yield every book dict.

    This loop makes O(total_books / page_size) network calls -- a handful for
    a library this size -- not one call per book, so it is not the kind of
    per-item hotspot the repo's concurrency rule targets; the per-book work
    below (two regex tests) is pure and cheap enough that a worker pool would
    add complexity without a measurable win.
    """
    offset = 0
    seen = 0
    total = None
    while total is None or offset < total:
        url = f"{base.rstrip('/')}/api/v1/audiobooks?limit={page_size}&offset={offset}"
        body = _http_get_json(url, token, insecure)
        payload = body.get("data") if isinstance(body, dict) and "data" in body else body
        if not isinstance(payload, dict) or "items" not in payload:
            # Fail loudly rather than silently yielding zero books. A
            # response-shape mismatch (e.g. the API contract changing) would
            # otherwise look identical to "empty library" -- items=[], exit
            # 0, a report with every count at zero. See
            # internal/server/audiobooks_helpers.go buildAudiobookListResponse
            # / internal/server/handlers/audiobooks/handler.go ListAudiobooks
            # for the current contract this script was verified against.
            got = sorted(payload.keys()) if isinstance(payload, dict) else type(payload).__name__
            raise RuntimeError(
                f"unexpected /api/v1/audiobooks response shape at offset={offset}: "
                f"expected a dict with an 'items' key (optionally wrapped in a "
                f"'data' envelope), got: {got}"
            )
        items = payload.get("items", [])
        total = payload.get("count", len(items))
        if not items:
            break
        for book in items:
            seen += 1
            yield book
        offset += len(items)
        if len(items) < page_size:
            # Short page: the server has nothing more, regardless of what
            # `count` claims (defensive -- avoids an infinite loop on a
            # miscounted total).
            break


# --- classification -----------------------------------------------------


def is_numeric_title(title: str | None) -> bool:
    return bool(title) and bool(re.fullmatch(NUMERIC_TITLE_PATTERN, title.strip()))


def has_bogus_path_segment(file_path: str | None) -> bool:
    if not file_path:
        return False
    parent_name = PurePath(file_path).parent.name
    return bool(PATH_SEGMENT_RE.search(parent_name))


def is_in_affected_dir(file_path: str | None, affected_dirs: set) -> bool:
    """True if file_path's containing directory is a wreckage directory.

    Book.FilePath for a single-file wreckage directory is the FILE itself
    (see internal/scanner/scanner.go groupFilesIntoBooks, the len(files)<=1
    branch), so the wreckage directory is the file's *parent*. A directory
    match is also accepted defensively in case FilePath ever names the
    directory itself (the multi-file "shared album tag" branch of the same
    function does that).
    """
    if not file_path:
        return False
    p = PurePath(file_path)
    return str(p.parent) in affected_dirs or str(p) in affected_dirs


def classify(book: dict, affected_dirs: set) -> str:
    """Return one of CATEGORY_KEYS for a single book row."""
    title = book.get("title")
    file_path = book.get("file_path")

    numeric_title = is_numeric_title(title)
    path_segment = has_bogus_path_segment(file_path)
    affected = is_in_affected_dir(file_path, affected_dirs)

    if affected:
        if numeric_title and path_segment:
            return "in_affected_dir_both_heuristics"
        if numeric_title:
            return "in_affected_dir_numeric_title_only"
        if path_segment:
            return "in_affected_dir_path_segment_only"
        return "in_affected_dir_neither_heuristic"

    if numeric_title:
        return "numeric_title_outside_affected_dir"
    if path_segment:
        return "path_segment_outside_affected_dir"
    return "no_signal"


def build_report(books, affected_dirs: set, library_root: str, api_base: str) -> Report:
    counts = dict.fromkeys(CATEGORY_KEYS, 0)
    matches: dict = {k: [] for k in CATEGORY_KEYS if k != "no_signal"}
    total = 0

    for book in books:
        total += 1
        category = classify(book, affected_dirs)
        counts[category] += 1
        if category != "no_signal":
            matches[category].append(
                {
                    "id": book.get("id"),
                    "title": book.get("title"),
                    "file_path": book.get("file_path"),
                }
            )

    notes = [
        "REPORT-ONLY: no row was modified, restored, or deleted. This "
        "script has no --fix/--apply flag and never will (owner "
        "decision #9 / #12: report counts before proposing any "
        "restore; never mass-restore).",
    ]

    in_affected_dir_total = (
        counts["in_affected_dir_numeric_title_only"]
        + counts["in_affected_dir_path_segment_only"]
        + counts["in_affected_dir_both_heuristics"]
        + counts["in_affected_dir_neither_heuristic"]
    )
    if affected_dirs and in_affected_dir_total == 0:
        notes.append(
            "WARNING: wreckage directories were found on disk under "
            f"{library_root!r} ({len(affected_dirs)} of them), but zero DB "
            "rows' file_path fell inside any of them -- all four "
            "in_affected_dir_* buckets are 0. This usually means the "
            "library root's path prefix does not match what the database "
            "recorded for file_path (a different mount point, a relative "
            "vs. absolute path, or a trailing-slash mismatch), not that "
            "there is genuinely no cross-reference. Compare a real "
            "file_path value from the low-confidence matches below (or "
            "from the API directly) against the affected directories this "
            "scan found before trusting the result."
        )

    return Report(
        generated_at=datetime.now(timezone.utc).isoformat(),
        library_root=library_root,
        api_base=api_base,
        total_books_scanned=total,
        affected_directories_found=len(affected_dirs),
        counts=counts,
        matches=matches,
        notes=notes
        + [
            "REPORT-ONLY: no row was modified, restored, or deleted. This "
            "script has no --fix/--apply flag and never will (owner "
            "decision #9 / #12: report counts before proposing any "
            "restore; never mass-restore).",
            "A numeric title outside an affected directory is a LOW "
            "confidence signal only -- a real book can genuinely be titled "
            "a bare number (e.g. '1984', '2001'); it is reported separately "
            "from the high-confidence in_affected_dir_* buckets, never "
            "folded into them.",
            "A path segment ending ' - <digits>' outside an affected "
            "directory is also LOW confidence -- an ordinary multi-disc "
            "folder ('The Stand - 2') has the same shape; see "
            "repair_stranded_tracks.find_bogus_dirs' docstring for why the "
            "naive rule alone over-matched by ~17x on this library.",
            "Rows are counted from the DB side regardless of whether "
            "file_path still exists on disk -- the whole point of this scan "
            "is rows the filesystem can no longer explain.",
            "The affected-directory set only reflects wreckage still "
            "PRESENT on disk under the given root at scan time; directories "
            "already cleaned up by some other process will not appear here, "
            "which undercounts rather than overcounts the true affected set.",
            "This scan cannot determine whether any given match is safe to "
            "restore or purge -- that judgment, and any action, is "
            "explicitly out of scope and left to a human.",
        ],
    )


def write_report(report: Report, path: str) -> None:
    with open(path, "w") as fh:
        json.dump(asdict(report), fh, indent=2)


def print_summary(report: Report) -> None:
    print("\n=== spurious stranded book row scan ===")
    print(f"  library root:            {report.library_root}")
    print(f"  affected dirs found:     {report.affected_directories_found}")
    print(f"  books scanned:           {report.total_books_scanned}")
    print()
    for key in CATEGORY_KEYS:
        print(f"  {key:38} {report.counts.get(key, 0)}")
    for note in report.notes:
        if note.startswith("WARNING:"):
            print(f"\n{note}", file=sys.stderr)


def default_report_path() -> str:
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return f"/tmp/spurious-stranded-book-rows-{ts}.json"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("root", help="library root to scan for wreckage directories")
    ap.add_argument(
        "--base",
        default=os.environ.get("ABK_API_URL") or os.environ.get("ABK_BASE"),
        help="API base URL, e.g. https://<server>:8484 (or set ABK_API_URL)",
    )
    ap.add_argument(
        "--token-file",
        default=DEFAULT_TOKEN_FILE,
        help=f"path to the API key file (default: {DEFAULT_TOKEN_FILE})",
    )
    ap.add_argument(
        "--insecure", action="store_true", help="skip TLS cert verification (self-signed prod cert)"
    )
    ap.add_argument("--page-size", type=int, default=DEFAULT_PAGE_SIZE)
    ap.add_argument("--report", default=None, help="output report path (default: timestamped, in /tmp)")
    args = ap.parse_args(argv)

    if not args.base:
        ap.error("--base is required (or set ABK_API_URL)")

    # Normalize to an absolute path up front. A relative --root would make
    # find_bogus_dirs return relative paths while the DB's file_path is
    # always absolute, so every cross-reference would silently miss even
    # though wreckage directories were genuinely found (see the
    # affected_directories_found>0-but-zero-cross-reference guard below,
    # which this also helps avoid tripping spuriously).
    root = os.path.abspath(args.root)

    print(f"scanning {root} for wreckage directories ...", flush=True)
    affected_dirs = set(find_bogus_dirs(root))
    print(f"  {len(affected_dirs)} wreckage directories found", flush=True)

    print(f"fetching books from {args.base} ...", flush=True)
    token = read_token(args.token_file)
    try:
        books = list(fetch_all_books(args.base, token, args.insecure, args.page_size))
    except urllib.error.HTTPError as e:
        # HTTPError is a URLError subclass, so it must be caught first. The
        # response body often names the actual cause (expired vs. revoked
        # token, etc.) -- the bare status code alone misdiagnoses it.
        try:
            detail = e.read().decode(errors="replace")
        except Exception:
            detail = ""
        print(f"FATAL: could not fetch books from {args.base}: HTTP {e.code} {e.reason}", file=sys.stderr)
        if detail:
            print(f"  response body: {detail[:2000]}", file=sys.stderr)
        return 2
    except (urllib.error.URLError, OSError, RuntimeError) as e:
        print(f"FATAL: could not fetch books from {args.base}: {e}", file=sys.stderr)
        return 2

    report = build_report(books, affected_dirs, root, args.base)
    print_summary(report)

    report_path = args.report or default_report_path()
    write_report(report, report_path)
    print(f"\nfull report: {report_path}")
    print("\nREPORT-ONLY -- nothing was moved, restored, or deleted.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
