# file: scripts/tests/test_deploy_generation_args.py
# version: 0.1.0
# guid: d99797f9-23dc-4c27-bf9f-9c586594c042
# last-edited: 2026-11-20

"""Tests for scripts/deploy_generation_args.py.

Run with:
    python3 -m unittest scripts.tests.test_deploy_generation_args

`ssh` is a shell script in a temporary directory at the front of PATH: it
prints what the test gives for the command it was asked to run, so nothing
here reaches a host.
"""

from __future__ import annotations

import hashlib
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]
REPO = SCRIPTS.parent
TOOL = SCRIPTS / "deploy_generation_args.py"
STAGE = "/home/alfred/audiobook-organizer"

FAKE_SSH = """#!/bin/sh
# argv: -o BatchMode=yes HOST COMMAND
cmd="$4"
echo "$cmd" >> "$FAKE_SSH_LOG"
case "$cmd" in
  *--version-json) printf '%s' "$FAKE_VERSION_OUT"; exit "${FAKE_VERSION_RC:-0}" ;;
  *--print-storage-levels) printf '%s' "$FAKE_LEVELS_OUT"; exit "${FAKE_LEVELS_RC:-0}" ;;
  *--print-storage-format) printf '%s' "$FAKE_FORMAT_OUT"; exit "${FAKE_FORMAT_RC:-0}" ;;
esac
exit 99
"""


class GenerationArgs(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        bindir = self.tmp / "bin"
        bindir.mkdir()
        ssh = bindir / "ssh"
        ssh.write_text(FAKE_SSH)
        ssh.chmod(ssh.stat().st_mode | stat.S_IXUSR)
        self.log = self.tmp / "ssh.log"
        self.log.write_text("")
        self.file = self.tmp / "audiobook-organizer-linux-amd64"
        self.file.write_bytes(b"the batmobile, as built")
        self.sha = hashlib.sha256(b"the batmobile, as built").hexdigest()
        self.list = self.tmp / "retired.txt"
        self.list.write_text("# enforced: false\nABK_X\twarn\t-\n")
        self.env = {
            "PATH": f"{bindir}{os.pathsep}{os.environ['PATH']}",
            "FAKE_SSH_LOG": str(self.log),
            "FAKE_VERSION_OUT": '{"version": "v1.4.2", "commit": "0badc0de"}',
            "FAKE_LEVELS_OUT": '{"books": 2, "core": 1}',
        }

    def run_tool(self, **env) -> subprocess.CompletedProcess:
        e = {**self.env, **env}
        return subprocess.run(
            [
                sys.executable,
                str(TOOL),
                "--host",
                "wayne-manor.invalid",
                "--stage",
                STAGE,
                "--file",
                str(self.file),
                "--list",
                str(self.list),
            ],
            env=e,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def test_prints_the_four_words_the_helper_takes(self) -> None:
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.split(), [self.sha, "v1.4.2", "books=2,core=1", "0"])
        asked = self.log.read_text().splitlines()
        self.assertEqual(
            asked, [f"{STAGE} --version-json", f"{STAGE} --print-storage-levels"], "nothing else"
        )

    def test_the_hash_is_of_the_uploaded_file_not_of_anything_on_the_host(self) -> None:
        self.assertEqual(self.run_tool().stdout.split()[0], self.sha)

    def test_enforced_true_is_one(self) -> None:
        self.list.write_text("# enforced: true\nABK_X\twarn\t-\n")
        self.assertEqual(self.run_tool().stdout.split()[3], "1")

    def test_an_unreadable_or_markerless_list_is_unknown(self) -> None:
        self.list.write_text("ABK_X\twarn\t-\n")
        self.assertEqual(self.run_tool().stdout.split()[3], "-")
        self.list.unlink()
        self.assertEqual(self.run_tool().stdout.split()[3], "-")

    def test_an_empty_vector_is_dot(self) -> None:
        self.assertEqual(self.run_tool(FAKE_LEVELS_OUT="{}").stdout.split()[2], ".")

    def test_a_failed_version_is_unknown_and_the_deploy_goes_on(self) -> None:
        for kw in ({"FAKE_VERSION_RC": "1"}, {"FAKE_VERSION_OUT": "not json"}):
            with self.subTest(kw=kw):
                p = self.run_tool(**kw)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(p.stdout.split()[1], "-")

    def test_a_version_the_helper_would_refuse_is_unknown(self) -> None:
        p = self.run_tool(FAKE_VERSION_OUT='{"version": "v1; rm -rf /"}')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.split()[1], "-")
        self.assertNotIn("rm", p.stdout)

    def test_levels_that_are_not_small_integers_are_unknown(self) -> None:
        for bad in ('{"books": "2"}', '{"books": true}', "[1, 2]", "nope", '{"Bad Owner": 1}'):
            with self.subTest(out=bad):
                p = self.run_tool(FAKE_LEVELS_OUT=bad)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(p.stdout.split()[2], "-")

    def test_a_build_without_the_levels_flag_and_format_one_is_every_owner_at_zero(self) -> None:
        p = self.run_tool(FAKE_LEVELS_RC="2", FAKE_FORMAT_OUT="1\n")
        self.assertEqual(p.stdout.split()[2], ".")

    def test_a_build_without_the_levels_flag_and_another_format_is_unknown(self) -> None:
        p = self.run_tool(FAKE_LEVELS_RC="2", FAKE_FORMAT_OUT="13\n")
        self.assertEqual(p.stdout.split()[2], "-")
        p = self.run_tool(FAKE_LEVELS_RC="2", FAKE_FORMAT_RC="2")
        self.assertEqual(p.stdout.split()[2], "-")

    def test_an_unreadable_local_file_fails_the_deploy(self) -> None:
        self.file.unlink()
        p = self.run_tool()
        self.assertEqual(p.returncode, 1)
        self.assertEqual(p.stdout, "")
        self.assertIn("cannot read", p.stderr)

    def test_an_unreachable_host_still_prints_the_hash_and_unknown_facts(self) -> None:
        p = self.run_tool(FAKE_VERSION_RC="255", FAKE_LEVELS_RC="255", FAKE_FORMAT_RC="255")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.split(), [self.sha, "-", "-", "0"])

    def test_the_output_is_accepted_by_the_helpers_parser(self) -> None:
        sys.path.insert(0, str(SCRIPTS))
        try:
            import deploy_generation_args as tool
        finally:
            sys.path.pop(0)
        helper = tool.load_helper()
        for env in ({}, {"FAKE_LEVELS_OUT": "{}"}, {"FAKE_VERSION_RC": "1"}):
            words = self.run_tool(**env).stdout.split()
            self.assertIsNotNone(helper._parse_meta(words), words)


if __name__ == "__main__":
    unittest.main()
