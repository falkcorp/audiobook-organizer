#!/usr/bin/env python3
# file: scripts/tests/test_find_spurious_stranded_book_rows.py
# version: 1.2.0
# guid: 3e6f8b12-9d47-4a5c-b8e1-2f7a5c9d0e63
# last-edited: 2026-08-23
"""Tests for scripts/find_spurious_stranded_book_rows.py.

Run with:
    python3 -m unittest scripts.tests.test_find_spurious_stranded_book_rows -v
    # or, matching CI (.github/workflows/ci.yml):
    python3 -m unittest discover -s scripts -p 'test_*.py' -v
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest import mock
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import find_spurious_stranded_book_rows as fsb  # noqa: E402

# --- heuristic unit tests ----------------------------------------------


class TestIsNumericTitle(unittest.TestCase):
    def test_bare_digits_is_numeric(self):
        self.assertTrue(fsb.is_numeric_title("85"))

    def test_bare_digits_with_surrounding_whitespace(self):
        self.assertTrue(fsb.is_numeric_title("  42 "))

    def test_real_title_is_not_numeric(self):
        self.assertFalse(fsb.is_numeric_title("Foundation and Empire"))

    def test_a_real_book_titled_a_number_is_flagged_by_this_heuristic_alone(self):
        # This is exactly the false-positive shape the module docstring warns
        # about -- '1984' is a real book. The heuristic alone can't tell the
        # difference; only the affected-directory cross-reference in
        # classify() can. This test pins the heuristic's own (over-eager)
        # behavior so a future change doesn't silently narrow it without
        # updating classify()'s LOW-confidence bucket to match.
        self.assertTrue(fsb.is_numeric_title("1984"))

    def test_digits_with_trailing_text_is_not_numeric(self):
        self.assertFalse(fsb.is_numeric_title("85a"))

    def test_none_title_is_not_numeric(self):
        self.assertFalse(fsb.is_numeric_title(None))

    def test_empty_title_is_not_numeric(self):
        self.assertFalse(fsb.is_numeric_title(""))


class TestHasBogusPathSegment(unittest.TestCase):
    def test_wreckage_shaped_parent_matches(self):
        self.assertTrue(
            fsb.has_bogus_path_segment("/lib/BookA/Project Hail Mary - 24/31.mp3")
        )

    def test_ordinary_book_directory_does_not_match(self):
        self.assertFalse(fsb.has_bogus_path_segment("/lib/BookA/Neuromancer/chapter1.mp3"))

    def test_none_file_path_does_not_match(self):
        self.assertFalse(fsb.has_bogus_path_segment(None))

    def test_empty_file_path_does_not_match(self):
        self.assertFalse(fsb.has_bogus_path_segment(""))

    def test_legit_multidisc_folder_is_flagged_by_this_heuristic_alone(self):
        # Same false-positive shape as the numeric-title heuristic: an
        # ordinary "Disc - 2" folder matches the naive pattern too. Pinned
        # here for the same reason -- classify() is what tells them apart,
        # not this function.
        self.assertTrue(fsb.has_bogus_path_segment("/lib/TheStand/The Stand - 2/01.mp3"))


class TestIsInAffectedDir(unittest.TestCase):
    def setUp(self):
        self.affected = {"/lib/BookA/Project Hail Mary - 24"}

    def test_file_inside_an_affected_dir_matches_via_parent(self):
        self.assertTrue(
            fsb.is_in_affected_dir("/lib/BookA/Project Hail Mary - 24/31.mp3", self.affected)
        )

    def test_file_path_equal_to_an_affected_dir_matches_directly(self):
        # Defensive case: groupFilesIntoBooks' shared-album-tag branch sets
        # FilePath to the directory itself, not a file inside it.
        self.assertTrue(fsb.is_in_affected_dir("/lib/BookA/Project Hail Mary - 24", self.affected))

    def test_file_outside_any_affected_dir_does_not_match(self):
        self.assertFalse(fsb.is_in_affected_dir("/lib/BookB/Neuromancer/chapter1.mp3", self.affected))

    def test_none_file_path_does_not_match(self):
        self.assertFalse(fsb.is_in_affected_dir(None, self.affected))


# --- classify(): the cross-reference logic ------------------------------


class TestClassify(unittest.TestCase):
    """One book fixture per CATEGORY_KEYS bucket.

    Each fixture's affected_dirs set is chosen independently of
    find_bogus_dirs' real output shape (that round-trip is covered
    separately, in TestEndToEndAgainstRealFilesystem below) so that each
    branch of classify() is exercised in isolation, including combinations
    find_bogus_dirs itself could never produce (e.g. an affected directory
    whose name does NOT end in digits) -- classify() must not assume its
    caller only ever passes it wreckage-shaped directories.
    """

    def test_both_heuristics_in_an_affected_dir(self):
        book = {"id": "1", "title": "31", "file_path": "/lib/BookA/Project Hail Mary - 24/31.mp3"}
        affected = {"/lib/BookA/Project Hail Mary - 24"}
        self.assertEqual(fsb.classify(book, affected), "in_affected_dir_both_heuristics")

    def test_numeric_title_only_in_an_affected_dir(self):
        # affected dir name deliberately does NOT end in digits, so only the
        # title heuristic fires even though the row is in the wreckage set.
        book = {"id": "2", "title": "85", "file_path": "/lib/BookA/WeirdDir/85.mp3"}
        affected = {"/lib/BookA/WeirdDir"}
        self.assertEqual(fsb.classify(book, affected), "in_affected_dir_numeric_title_only")

    def test_path_segment_only_in_an_affected_dir(self):
        book = {"id": "3", "title": "Some Title", "file_path": "/lib/BookA/Some Title - 5/5.mp3"}
        affected = {"/lib/BookA/Some Title - 5"}
        self.assertEqual(fsb.classify(book, affected), "in_affected_dir_path_segment_only")

    def test_neither_heuristic_but_in_an_affected_dir(self):
        book = {"id": "4", "title": "Neuromancer", "file_path": "/lib/BookA/SomeDir/track.mp3"}
        affected = {"/lib/BookA/SomeDir"}
        self.assertEqual(fsb.classify(book, affected), "in_affected_dir_neither_heuristic")

    def test_numeric_title_outside_any_affected_dir_is_the_false_positive_bucket(self):
        # The '1984' case called out explicitly in the brief: a REAL book
        # genuinely titled a bare number, nowhere near any wreckage
        # directory. Must land in the LOW-confidence bucket, never in an
        # in_affected_dir_* (high-confidence) bucket.
        book = {"id": "5", "title": "1984", "file_path": "/lib/Orwell/1984/1984.m4b"}
        affected: set = set()
        self.assertEqual(fsb.classify(book, affected), "numeric_title_outside_affected_dir")

    def test_path_segment_outside_any_affected_dir_is_the_false_positive_bucket(self):
        book = {"id": "6", "title": "The Stand", "file_path": "/lib/King/The Stand - 2/01.mp3"}
        affected: set = set()
        self.assertEqual(fsb.classify(book, affected), "path_segment_outside_affected_dir")

    def test_a_clean_legitimate_book_is_no_signal(self):
        # The "known-good input still passes" case: nothing about this row
        # should ever be flagged.
        book = {"id": "7", "title": "Neuromancer", "file_path": "/lib/Gibson/Neuromancer.m4b"}
        affected: set = set()
        self.assertEqual(fsb.classify(book, affected), "no_signal")

    def test_classify_never_raises_on_missing_fields(self):
        for book in ({}, {"id": "x"}, {"id": "x", "title": None, "file_path": None}):
            with self.subTest(book=book):
                self.assertEqual(fsb.classify(book, set()), "no_signal")


# --- build_report(): the full aggregation --------------------------------


class TestBuildReport(unittest.TestCase):
    def test_counts_partition_the_population_and_sum_to_total(self):
        affected = {
            "/lib/BookA/Project Hail Mary - 24",
            "/lib/BookA/WeirdDir",
            "/lib/BookA/Some Title - 5",
            "/lib/BookA/SomeDir",
        }
        books = [
            {"id": "1", "title": "31", "file_path": "/lib/BookA/Project Hail Mary - 24/31.mp3"},
            {"id": "2", "title": "85", "file_path": "/lib/BookA/WeirdDir/85.mp3"},
            {"id": "3", "title": "Some Title", "file_path": "/lib/BookA/Some Title - 5/5.mp3"},
            {"id": "4", "title": "Neuromancer", "file_path": "/lib/BookA/SomeDir/track.mp3"},
            {"id": "5", "title": "1984", "file_path": "/lib/Orwell/1984/1984.m4b"},
            {"id": "6", "title": "The Stand", "file_path": "/lib/King/The Stand - 2/01.mp3"},
            {"id": "7", "title": "Neuromancer", "file_path": "/lib/Gibson/Neuromancer.m4b"},
        ]

        report = fsb.build_report(books, affected, "/lib", "http://test")

        expected = {
            "in_affected_dir_both_heuristics": 1,
            "in_affected_dir_numeric_title_only": 1,
            "in_affected_dir_path_segment_only": 1,
            "in_affected_dir_neither_heuristic": 1,
            "numeric_title_outside_affected_dir": 1,
            "path_segment_outside_affected_dir": 1,
            "no_signal": 1,
        }
        self.assertEqual(report.counts, expected)
        self.assertEqual(report.total_books_scanned, 7)
        self.assertEqual(sum(report.counts.values()), report.total_books_scanned)

        # A false-positive-shaped row (numeric title, not in an affected
        # dir) must never be silently promoted into a high-confidence
        # bucket, and it must still be REPORTED (not dropped), so a human
        # can see it and rule it out.
        self.assertEqual(len(report.matches["numeric_title_outside_affected_dir"]), 1)
        self.assertEqual(report.matches["numeric_title_outside_affected_dir"][0]["id"], "5")

        # no_signal rows are counted but not enumerated individually --
        # they are the bulk of any real library and carry no signal to
        # review.
        self.assertNotIn("no_signal", report.matches)

    def test_notes_state_the_scans_limits_plainly(self):
        report = fsb.build_report([], set(), "/lib", "http://test")
        joined = " ".join(report.notes).lower()
        self.assertIn("report-only", joined)
        self.assertIn("no --fix", joined)
        self.assertIn("cannot determine whether", joined)

    def test_notes_contain_no_duplicate_entries(self):
        # assertIn on a joined string (as above) cannot see a note repeated
        # verbatim -- the substring is still "in" the joined text either
        # way. Pins the REPORT-ONLY note previously appearing twice (once
        # from the mismatch-warning branch's own list, once again as the
        # first element of the static notes list appended after it).
        report = fsb.build_report([], set(), "/lib", "http://test")
        self.assertEqual(len(set(report.notes)), len(report.notes), report.notes)

    def test_empty_input_yields_zero_counts_not_an_exception(self):
        report = fsb.build_report([], set(), "/lib", "http://test")
        self.assertEqual(report.total_books_scanned, 0)
        self.assertTrue(all(n == 0 for n in report.counts.values()))

    def test_warns_when_wreckage_found_but_nothing_cross_references_it(self):
        # affected_dirs is non-empty (wreckage WAS found on disk) but no
        # book's file_path falls inside any of them -- the classic symptom
        # of --root's path prefix not matching what the DB recorded (a
        # different mount point, relative vs. absolute, etc). This must be
        # surfaced loudly, not silently reported as "no high-confidence
        # matches" indistinguishable from "no wreckage cross-references at
        # all".
        affected = {"/mnt/bigdata/books/BookA/Project Hail Mary - 24"}
        books = [
            # Same wreckage shape, but under a DIFFERENT root prefix than
            # affected_dirs -- e.g. a Mac SMB mount vs. the DB's real path.
            {"id": "1", "title": "31", "file_path": "/Volumes/nas/BookA/Project Hail Mary - 24/31.mp3"},
        ]
        report = fsb.build_report(books, affected, "/Volumes/nas", "http://test")
        joined = " ".join(report.notes)
        self.assertIn("WARNING", joined)
        self.assertIn("path prefix", joined)

    def test_no_warning_when_there_is_no_wreckage_on_disk_at_all(self):
        # affected_dirs empty is the ordinary "nothing found" case -- not a
        # mismatch, so no warning should fire.
        report = fsb.build_report([], set(), "/lib", "http://test")
        joined = " ".join(report.notes)
        self.assertNotIn("WARNING", joined)

    def test_no_warning_when_cross_reference_succeeds(self):
        affected = {"/lib/BookA/Project Hail Mary - 24"}
        books = [
            {"id": "1", "title": "31", "file_path": "/lib/BookA/Project Hail Mary - 24/31.mp3"},
        ]
        report = fsb.build_report(books, affected, "/lib", "http://test")
        joined = " ".join(report.notes)
        self.assertNotIn("WARNING", joined)


# --- read_token() ---------------------------------------------------------


class TestReadToken(unittest.TestCase):
    def test_bare_token_file(self):
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            f.write("abk_thisisatoken\n")
            path = f.name
        try:
            self.assertEqual(fsb.read_token(path), "abk_thisisatoken")
        finally:
            os.unlink(path)

    def test_api_key_prefixed_line(self):
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            f.write("api_key=abk_prefixed\nkey_id=irrelevant\n")
            path = f.name
        try:
            self.assertEqual(fsb.read_token(path), "abk_prefixed")
        finally:
            os.unlink(path)

    def test_unreadable_file_exits_nonzero(self):
        with self.assertRaises(SystemExit) as ctx:
            fsb.read_token("/nonexistent/path/does-not-exist")
        self.assertEqual(ctx.exception.code, 2)

    def test_garbage_file_exits_nonzero(self):
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            f.write("key_id=foo\nusername=bar\n")  # no api_key= and multi-line
            path = f.name
        try:
            with self.assertRaises(SystemExit) as ctx:
                fsb.read_token(path)
            self.assertEqual(ctx.exception.code, 2)
        finally:
            os.unlink(path)


# --- fetch_all_books(): pagination, no network ----------------------------


class TestFetchAllBooksPagination(unittest.TestCase):
    def test_pages_until_a_short_page_is_seen(self):
        pages = [
            {"data": {"items": [{"id": "1"}, {"id": "2"}], "count": 5, "limit": 2, "offset": 0}},
            {"data": {"items": [{"id": "3"}, {"id": "4"}], "count": 5, "limit": 2, "offset": 2}},
            {"data": {"items": [{"id": "5"}], "count": 5, "limit": 2, "offset": 4}},
        ]

        with mock.patch.object(fsb, "_http_get_json", side_effect=pages):
            books = list(fsb.fetch_all_books("http://test", "tok", False, page_size=2))

        self.assertEqual([b["id"] for b in books], ["1", "2", "3", "4", "5"])

    def test_empty_first_page_stops_immediately(self):
        with mock.patch.object(
            fsb, "_http_get_json", return_value={"data": {"items": [], "count": 0}}
        ):
            books = list(fsb.fetch_all_books("http://test", "tok", False, page_size=50))
        self.assertEqual(books, [])

    def test_short_page_stops_even_if_count_claims_more(self):
        # A short page (fewer items than page_size) means the server has
        # nothing more; `count` is never consulted at all (see next test).
        pages = [{"data": {"items": [{"id": "1"}], "count": 99, "limit": 50, "offset": 0}}]
        with mock.patch.object(fsb, "_http_get_json", side_effect=pages):
            books = list(fsb.fetch_all_books("http://test", "tok", False, page_size=50))
        self.assertEqual([b["id"] for b in books], ["1"])

    def test_a_full_first_page_continues_to_a_second_page_regardless_of_count(self):
        # Pins the C2 fix: audiobooks_helpers.go:100-115 documents `count`
        # falling back to len(enriched) -- the PAGE length -- whenever the
        # server's CountAudiobooks[Filtered] call errors. A page-length
        # `count` is indistinguishable, from the client, between "count
        # errored" and "there really are exactly page_size books total". The
        # old implementation used `count` as its sole loop terminator
        # (`while offset < total`), so a full first page whose `count`
        # equals page_size stopped the scan right there, silently truncating
        # to one page. The fix removes `count` from the loop condition
        # entirely -- only a page shorter than page_size may terminate it --
        # so this must fetch BOTH pages even though page one's count == 2
        # (== page_size) claims there is nothing more.
        pages = [
            {"data": {"items": [{"id": "1"}, {"id": "2"}], "count": 2, "limit": 2, "offset": 0}},
            {"data": {"items": [{"id": "3"}], "count": 2, "limit": 2, "offset": 2}},
        ]
        with mock.patch.object(fsb, "_http_get_json", side_effect=pages) as mocked:
            books = list(fsb.fetch_all_books("http://test", "tok", False, page_size=2))

        self.assertEqual([b["id"] for b in books], ["1", "2", "3"])
        self.assertEqual(mocked.call_count, 2)

    def test_every_request_asks_for_quarantined_rows(self):
        # Pins the C1 fix: handlers/audiobooks/handler.go:595 defaults
        # show_quarantined to false, and audiobooks_helpers.go:58 then sets
        # ExcludeQuarantined = true, dropping quarantined rows from both
        # items and count. A quarantined-but-not-purged row (missing file on
        # disk) is exactly the shape this scan hunts for, so every page
        # request must explicitly ask for them.
        pages = [{"data": {"items": [{"id": "1"}], "count": 1, "limit": 50, "offset": 0}}]
        with mock.patch.object(fsb, "_http_get_json", side_effect=pages) as mocked:
            list(fsb.fetch_all_books("http://test", "tok", False, page_size=50))

        called_url = mocked.call_args[0][0]
        self.assertIn("show_quarantined=true", called_url)

    def test_unexpected_response_shape_raises_instead_of_yielding_zero_silently(self):
        # If the API response contract ever changes (e.g. "items" renamed or
        # dropped), the old behavior was `.get("items", [])` -- silently
        # treating it as an empty page, exiting 0 with a report claiming zero
        # books everywhere. That is indistinguishable from "empty library"
        # and would hide a real integration break. It must now raise.
        with mock.patch.object(
            fsb, "_http_get_json", return_value={"data": {"audiobooks": [], "total": 0}}
        ), self.assertRaises(RuntimeError) as ctx:
            list(fsb.fetch_all_books("http://test", "tok", False, page_size=50))
        self.assertIn("items", str(ctx.exception))

    def test_response_with_no_data_envelope_and_no_items_also_raises(self):
        # Repo targets Python 3.8 (see ruff target-version), which doesn't
        # support parenthesized multi-context `with`, so this stays nested.
        with mock.patch.object(fsb, "_http_get_json", return_value={"status": "ok"}):  # noqa: SIM117
            with self.assertRaises(RuntimeError):
                list(fsb.fetch_all_books("http://test", "tok", False, page_size=50))


# --- end-to-end against a real filesystem + a real local HTTP server ------


class _FakeAudiobooksHandler(BaseHTTPRequestHandler):
    """Serves one fixed page of GET /api/v1/audiobooks, mirroring the real
    RespondWithOK {"data": {"items": ..., "count": ...}} response shape."""

    books: list = []

    def do_GET(self):  # noqa: N802 (BaseHTTPRequestHandler's naming)
        parsed = urlparse(self.path)
        if parsed.path != "/api/v1/audiobooks":
            self.send_response(404)
            self.end_headers()
            return
        qs = parse_qs(parsed.query)
        # Mirrors the real default (handlers/audiobooks/handler.go:595):
        # show_quarantined defaults to false server-side and, unset, would
        # silently exclude quarantined rows. 400 here so a regression on the
        # client's show_quarantined=true param fails this E2E test loudly --
        # sending it and not sending it must NOT be indistinguishable to
        # this fixture (that indistinguishability is exactly how the C1
        # regression shipped past 40 passing tests the first time).
        if qs.get("show_quarantined", ["false"])[0] != "true":
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b"show_quarantined=true is required by this fake handler")
            return
        limit = int(qs.get("limit", ["500"])[0])
        offset = int(qs.get("offset", ["0"])[0])
        page = self.books[offset : offset + limit]
        body = json.dumps(
            {"data": {"items": page, "count": len(self.books), "limit": limit, "offset": offset}}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):  # silence per-request stderr noise
        pass


class TestEndToEndAgainstRealFilesystem(unittest.TestCase):
    """Builds a REAL wreckage directory tree on disk, runs find_bogus_dirs
    (the imported, unmodified sibling function) against it, serves a REAL
    local HTTP server standing in for the audiobook-organizer API, and runs
    the script's actual main() end to end -- this is what acceptance
    criterion #2 ("running it against a local/dev server produces a report
    file and prints non-negative counts") is demonstrating.
    """

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        # realpath, not just the raw tempdir name: on macOS /var is a
        # symlink to /private/var, and os.chdir()+os.getcwd() (used by the
        # relative-root test below) resolves through it. Without this,
        # self.root and the path main()'s os.path.abspath(relative_root)
        # reconstructs after a chdir would be two different (if equivalent)
        # strings, breaking the relative-root test on symlink grounds
        # entirely unrelated to what it's actually testing.
        self.root = os.path.realpath(self.tmp.name)

        # Two sibling wreckage directories sharing a title stem -- the
        # minimum shape find_bogus_dirs' grouping requires (a lone directory
        # is never flagged; see its docstring).
        book_dir = os.path.join(self.root, "BookA")
        os.makedirs(os.path.join(book_dir, "Project Hail Mary - 24"))
        os.makedirs(os.path.join(book_dir, "Project Hail Mary - 23"))
        with open(os.path.join(book_dir, "Project Hail Mary - 24", "31.mp3"), "wb") as f:
            f.write(b"x")
        with open(os.path.join(book_dir, "Project Hail Mary - 23", "31.mp3"), "wb") as f:
            f.write(b"x")

        self.affected = set(fsb.find_bogus_dirs(self.root))
        # Confirms the fixture actually reproduces wreckage BEFORE trusting
        # anything downstream -- an empty affected set would make every
        # other assertion in this test vacuously true.
        self.assertEqual(
            self.affected,
            {
                os.path.join(book_dir, "Project Hail Mary - 24"),
                os.path.join(book_dir, "Project Hail Mary - 23"),
            },
        )

        books = [
            {
                "id": "1",
                "title": "31",
                "file_path": os.path.join(book_dir, "Project Hail Mary - 24", "31.mp3"),
            },
            {
                "id": "2",
                "title": "31",
                "file_path": os.path.join(book_dir, "Project Hail Mary - 23", "31.mp3"),
            },
            {
                "id": "3",
                "title": "Project Hail Mary",
                "file_path": os.path.join(book_dir, "Project Hail Mary.m4b"),
            },
            {
                "id": "4",
                "title": "1984",
                "file_path": os.path.join(self.root, "Library", "1984", "1984.m4b"),
            },
        ]

        handler = type("Handler", (_FakeAudiobooksHandler,), {"books": books})
        self.server = HTTPServer(("127.0.0.1", 0), handler)
        self.server_thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.server_thread.start()
        self.base_url = f"http://127.0.0.1:{self.server.server_port}"

        self.token_file = os.path.join(self.root, "api-key")
        with open(self.token_file, "w") as f:
            f.write("abk_test_token\n")
        self.report_path = os.path.join(self.root, "report.json")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.server_thread.join(timeout=5)
        self.tmp.cleanup()

    def test_full_run_produces_a_report_with_correct_nonnegative_counts(self):
        rc = fsb.main(
            [
                self.root,
                "--base",
                self.base_url,
                "--token-file",
                self.token_file,
                "--report",
                self.report_path,
            ]
        )
        self.assertEqual(rc, 0)
        self.assertTrue(os.path.isfile(self.report_path))

        with open(self.report_path) as f:
            report = json.load(f)

        self.assertEqual(report["total_books_scanned"], 4)
        self.assertTrue(all(n >= 0 for n in report["counts"].values()))
        self.assertEqual(sum(report["counts"].values()), report["total_books_scanned"])
        # Books 1 and 2 sit in the real wreckage directories found on disk,
        # with numeric titles matching their wreckage-shaped parent dirs.
        self.assertEqual(report["counts"]["in_affected_dir_both_heuristics"], 2)
        # Book 3 is the ORIGINAL legitimate book, elsewhere, untouched.
        self.assertEqual(report["counts"]["no_signal"], 1)
        # Book 4 is the '1984' false-positive-shaped case: numeric title,
        # nowhere near the wreckage -- must be LOW confidence, not folded
        # into the high-confidence buckets.
        self.assertEqual(report["counts"]["numeric_title_outside_affected_dir"], 1)
        self.assertEqual(report["counts"]["in_affected_dir_numeric_title_only"], 0)

    def test_a_relative_root_finds_the_same_wreckage_as_the_absolute_one(self):
        # Every other test in this file passes an already-absolute tempdir
        # path, which lets main()'s os.path.abspath(args.root) normalization
        # survive mutation for free -- an absolute path is unchanged by
        # abspath, so removing that call would never fail those tests. A
        # relative --root would make find_bogus_dirs return relative paths
        # while the DB's file_path is always absolute, so the cross-
        # reference would silently miss every row despite wreckage genuinely
        # being found on disk -- exactly the path-prefix-mismatch shape the
        # WARNING note (TestBuildReport) exists to catch, so a regression
        # here would show up as that warning firing, not as a crash.
        parent, base = os.path.split(self.root)
        cwd = os.getcwd()
        os.chdir(parent)
        try:
            rc = fsb.main(
                [
                    base,  # relative, not self.root
                    "--base",
                    self.base_url,
                    "--token-file",
                    self.token_file,
                    "--report",
                    self.report_path,
                ]
            )
        finally:
            os.chdir(cwd)

        self.assertEqual(rc, 0)
        with open(self.report_path) as f:
            report = json.load(f)

        self.assertEqual(report["total_books_scanned"], 4)
        self.assertEqual(report["counts"]["in_affected_dir_both_heuristics"], 2)
        self.assertNotIn("WARNING", " ".join(report["notes"]))


if __name__ == "__main__":
    unittest.main()
