# file: scripts/tests/test_dump_r0_routes.py
# version: 1.0.0
# guid: c3e8a5d1-7b42-4f96-8a0d-2e6f9b1c4d73
# last-edited: 2026-11-14
"""Tests for scripts/r2corpus/dump_r0_routes.py and its committed data (F4-07).

The dump itself runs ``go test`` inside a checkout of the R0 commit, which these
tests do not do: ``subprocess.run`` is replaced by a fake that writes the JSON the Go
test would write. What they pin is the script's own logic (the pinned commit, the
document it writes, the cleanup of the test file it copies into the checkout) and that
the committed ``routes.json`` is a dump of the commit ``r0config/r0_reach_commit.txt``
names (F4-08; it was the ``ci/r0_release.txt`` tag until 2026-11-22).

Run: python3 -m unittest scripts.tests.test_dump_r0_routes
"""

from __future__ import annotations

import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from contextlib import ExitStack, redirect_stdout
from unittest import mock

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "r2corpus" / "dump_r0_routes.py"
ROUTES = REPO_ROOT / "internal" / "books" / "rbacreach" / "routes.json"

_spec = importlib.util.spec_from_file_location("dump_r0_routes", SCRIPT)
dump = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
_spec.loader.exec_module(dump)

DUMP = {
    "abs_off": 2,
    "abs_on": 3,
    "routes": [
        {"method": "GET", "path": "/api/v1/gotham/rooftops", "abs_only": False},
        {"method": "POST", "path": "/api/v1/gotham/signal", "abs_only": False},
        {"method": "GET", "path": "/api/items/:id/cover", "abs_only": True},
    ],
}


class ReadCommitTest(unittest.TestCase):
    def read(self, text: str) -> str:
        b = dump._build_module()
        with tempfile.TemporaryDirectory() as d:
            f = pathlib.Path(d) / "r0_reach_commit.txt"
            f.write_text(text, encoding="utf-8")
            with mock.patch.object(dump, "_build_module", lambda: b), mock.patch.object(
                b, "COMMIT_FILE", f
            ):
                return dump.read_commit()

    def test_one_commit_line_among_comments_and_blanks(self):
        sha = "0123456789abcdef0123456789abcdef01234567"
        self.assertEqual(self.read(f"# the R0 commit\n\n{sha}\n# trailing\n"), sha)

    def test_no_commit_line_is_refused(self):
        with self.assertRaises(SystemExit):
            self.read("# only a comment\n\n")

    def test_two_commit_lines_are_refused(self):
        with self.assertRaises(SystemExit):
            self.read("0123456789abcdef0123456789abcdef01234567\n" * 2)

    def test_a_short_or_symbolic_commit_is_refused(self):
        for bad in ("f6698f9bf", "main", "v0.223.0"):
            with self.subTest(bad=bad), self.assertRaises(SystemExit):
                self.read(bad + "\n")


class RunDumpTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.src = pathlib.Path(self.tmp.name) / "src"
        (self.src / "internal" / "server").mkdir(parents=True)
        self.copied = self.src / "internal" / "server" / "r0_route_dump_test.go"

    def test_the_go_test_runs_in_the_checkout_and_its_file_is_removed_after(self):
        seen: dict = {}

        def fake_run(cmd, cwd, env, check):
            seen.update(cmd=cmd, cwd=cwd, dst=env["R0_ROUTE_DUMP"], present=self.copied.exists())
            return mock.Mock(returncode=0)

        with mock.patch.object(dump.subprocess, "run", fake_run):
            dump.run_dump(self.src, pathlib.Path(self.tmp.name) / "out.json")
        self.assertTrue(seen["present"], "the dump test is in the checkout while go test runs")
        self.assertEqual(seen["cwd"], self.src)
        self.assertEqual(seen["cmd"][:2], ["go", "test"])
        self.assertIn("TestR0RouteDump", seen["cmd"])
        self.assertTrue(seen["dst"].endswith("out.json"))
        self.assertFalse(self.copied.exists(), "the copied test file is removed")

    def test_the_copied_file_is_removed_when_go_test_fails(self):
        def fake_run(cmd, cwd, env, check):
            raise dump.subprocess.CalledProcessError(1, cmd)

        with ExitStack() as stack:
            stack.enter_context(mock.patch.object(dump.subprocess, "run", fake_run))
            stack.enter_context(self.assertRaises(dump.subprocess.CalledProcessError))
            dump.run_dump(self.src, pathlib.Path(self.tmp.name) / "out.json")
        self.assertFalse(self.copied.exists())


class MainTest(unittest.TestCase):
    def run_main(self, out: pathlib.Path) -> dict:
        def fake_run_dump(src, dst):
            dst.write_text(json.dumps(DUMP), encoding="utf-8")

        buf = io.StringIO()
        with ExitStack() as stack:
            src = stack.enter_context(tempfile.TemporaryDirectory())
            stack.enter_context(mock.patch.object(dump, "run_dump", fake_run_dump))
            stack.enter_context(mock.patch.object(dump, "read_commit", lambda: "9" * 40))
            stack.enter_context(redirect_stdout(buf))
            self.assertEqual(dump.main(["--src", src, "--out", str(out)]), 0)
        self.assertIn("wrote 3 routes", buf.getvalue())
        return json.loads(out.read_text(encoding="utf-8"))

    def test_the_document_carries_the_commit_the_counts_and_every_route(self):
        with tempfile.TemporaryDirectory() as d:
            doc = self.run_main(pathlib.Path(d) / "nested" / "routes.json")
        self.assertEqual(doc["r0_commit"], "9" * 40)
        self.assertNotIn("r0_tag", doc)
        self.assertEqual(doc["route_count"], 3)
        self.assertEqual((doc["abs_off_count"], doc["abs_on_count"]), (2, 3))
        self.assertEqual(doc["routes"], DUMP["routes"])
        self.assertEqual(doc["_header"]["file"], "internal/books/rbacreach/routes.json")
        self.assertEqual(doc["_header"]["version"], dump.VERSION)

    def test_a_checkout_named_by_src_is_not_removed(self):
        with ExitStack() as stack:
            d = stack.enter_context(tempfile.TemporaryDirectory())
            src = pathlib.Path(d) / "commit"
            src.mkdir()
            stack.enter_context(
                mock.patch.object(dump, "run_dump", lambda s, o: o.write_text(json.dumps(DUMP)))
            )
            stack.enter_context(mock.patch.object(dump, "read_commit", lambda: "9" * 40))
            run = stack.enter_context(mock.patch.object(dump.subprocess, "run"))
            stack.enter_context(redirect_stdout(io.StringIO()))
            dump.main(["--src", str(src), "--out", str(pathlib.Path(d) / "r.json")])
            run.assert_not_called()
            self.assertTrue(src.is_dir())


class CommittedDataTest(unittest.TestCase):
    def setUp(self):
        self.doc = json.loads(ROUTES.read_text(encoding="utf-8"))

    def test_the_committed_dump_is_the_commit_the_pin_file_names(self):
        self.assertEqual(self.doc["r0_commit"], dump.read_commit())

    def test_the_counts_are_the_routes_listed(self):
        routes = self.doc["routes"]
        self.assertEqual(self.doc["route_count"], len(routes))
        self.assertEqual(self.doc["abs_on_count"], len(routes))
        self.assertEqual(self.doc["abs_off_count"], sum(1 for r in routes if not r["abs_only"]))

    def test_no_route_is_listed_twice(self):
        keys = [(r["method"], r["path"]) for r in self.doc["routes"]]
        self.assertEqual(len(keys), len(set(keys)))


if __name__ == "__main__":
    unittest.main()
