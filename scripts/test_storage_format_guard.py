#!/usr/bin/env python3
# file: scripts/test_storage_format_guard.py
# version: 1.1.0
# guid: 75f5cdf5-6d94-4c67-a679-062b1e13e414
# last-edited: 2026-10-04

"""Unit tests for scripts/storage_format_guard.py, the `make rollback` guard.

One test per case in TASK-A4 section 7. Each runs the script as `make
rollback` does (a subprocess, ROLLBACK_IGNORE_FORMAT in the environment) and
asserts the exit code and the key line. Run with:
    python3 -m unittest discover -s scripts -p 'test_storage_format_guard.py' -v
"""

import importlib.util
import os
import subprocess
import sys
import unittest
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_SCRIPT = _HERE / "storage_format_guard.py"
_spec = importlib.util.spec_from_file_location("storage_format_guard", _SCRIPT)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
_spec.loader.exec_module(guard)

DB = "/data/db"
BIN = "/usr/local/bin/aorg"


def run_guard(prev, sidecar, checkpoint, ignore=False, current="1", sidecar_err="", current_err=""):
    env = dict(os.environ)
    env.pop("ROLLBACK_IGNORE_FORMAT", None)
    if ignore:
        env["ROLLBACK_IGNORE_FORMAT"] = "1"
    # The `--flag=value` form the Makefile uses, so a value starting with "-"
    # (an old binary's usage text) cannot be read as a flag.
    proc = subprocess.run(
        [sys.executable, str(_SCRIPT), f"--db={DB}", f"--bin={BIN}",
         f"--prev={prev}", f"--sidecar={sidecar}", f"--checkpoint={checkpoint}",
         f"--current={current}", f"--sidecar-err={sidecar_err}", f"--current-err={current_err}"],
        capture_output=True, text=True, env=env, check=False,
    )
    return proc.returncode, proc.stdout + proc.stderr


class StorageFormatGuardTest(unittest.TestCase):
    def test_case1_prev_1_store_1_allows(self):
        code, out = run_guard("1", "1", "")
        self.assertEqual(code, 0, out)
        self.assertIn("swap allowed", out)
        self.assertNotIn("REFUSING", out)

    def test_case2_prev_empty_reads_as_1(self):
        code, out = run_guard("", "1\n", "")
        self.assertEqual(code, 0, out)
        self.assertIn("previous binary supports 1", out)

    def test_case3_prev_error_text_reads_as_1(self):
        # An old binary's error text can contain digits; only an all-digit
        # string counts as a format.
        code, out = run_guard("Error: unknown flag: --print-storage-format (exit 2)", "1", "")
        self.assertEqual(code, 0, out)
        self.assertIn("previous binary supports 1", out)

    def test_case4_store_above_prev_refused_with_checkpoint(self):
        ckpt = "/data/.migration-backups/1-2-x"
        code, out = run_guard("1", "2", ckpt + "\n")
        self.assertEqual(code, 1, out)
        self.assertIn(
            "REFUSING: the previous binary supports storage format 1, the store is at 2. "
            "A binary swap cannot go back past a format change; nothing was swapped.", out)
        self.assertIn("sudo systemctl stop audiobook-organizer.service", out)
        self.assertIn(f"checkpoint_dir: {ckpt}", out)
        self.assertIn(f"sudo cp -a {ckpt}/db {DB}", out)
        # The .pre-format step and its note print even though no such file
        # exists here: A9's deploy-cutover.sh is what creates it.
        self.assertFalse(Path(f"{BIN}.pre-format-2").exists())
        self.assertIn(f"install {BIN}.pre-format-2 as {BIN} (never {BIN}.prev)", out)
        self.assertIn(
            f"note: {BIN}.pre-format-2 is saved by scripts/deploy-cutover.sh at deploy time; "
            "if it is absent, build the release that matches format 2 and install that binary", out)
        self.assertIn("docs/system/runbooks.md#storage-format-restore", out)

    def test_case5_store_above_prev_ignore_flag_still_refused(self):
        code, out = run_guard("1", "2", "", ignore=True)
        self.assertEqual(code, 1, out)
        self.assertIn("REFUSING: the previous binary supports storage format 1", out)
        self.assertIn(
            f"checkpoint_dir: none recorded in {DB}.migration-checkpoint. "
            "Do not pick the newest .migration-backups/ entry; read the storage_migration report.", out)

    def test_case6_sidecar_unreadable_refused(self):
        code, out = run_guard("1", "", "")
        self.assertEqual(code, 1, out)
        self.assertIn(
            f"REFUSING: cannot read the store's storage format from {DB}.storage-format. "
            "Set ROLLBACK_IGNORE_FORMAT=1 only if you have verified the store is at format 1.", out)

    def test_case7_sidecar_unreadable_ignore_flag_warns(self):
        code, out = run_guard("1", "garbage", "", ignore=True)
        self.assertEqual(code, 0, out)
        self.assertIn("WARNING: cannot read the store's storage format", out)

    def test_sidecar_zero_is_unreadable(self):
        code, out = run_guard("1", "0", "")
        self.assertEqual(code, 1, out)
        self.assertIn("REFUSING: cannot read the store's storage format", out)

    def test_prev_zero_reads_as_1(self):
        code, out = run_guard("0", "1", "")
        self.assertEqual(code, 0, out)

    def test_unreadable_sidecar_names_the_cause(self):
        code, out = run_guard("1", "", "", sidecar_err="sudo: a password is required")
        self.assertEqual(code, 1, out)
        self.assertIn("cause: stderr: sudo: a password is required", out)
        self.assertIn("docs/system/deploy-and-gpu-ops.md", out)

    def test_ignore_flag_refuses_when_current_binary_is_newer(self):
        code, out = run_guard("1", "", "", ignore=True, current="2")
        self.assertEqual(code, 1, out)
        self.assertIn("the current binary /usr/local/bin/aorg supports storage format 2", out)

    def test_ignore_flag_allows_when_current_binary_not_newer(self):
        code, out = run_guard("1", "", "", ignore=True, current="1")
        self.assertEqual(code, 0, out)
        self.assertIn("WARNING", out)

    def test_dash_leading_prev_value_is_not_a_flag(self):
        code, out = run_guard("--help: unknown flag", "1", "")
        self.assertEqual(code, 0, out)
        self.assertIn("swap allowed", out)

    def test_ignore_flag_refuses_crashing_current_binary(self):
        code, out = run_guard("1", "", "", ignore=True, current="",
                              current_err="panic: runtime error: invalid memory address")
        self.assertEqual(code, 1, out)
        self.assertIn("gave no answer", out)
        self.assertIn("panic: runtime error", out)

    def test_ignore_flag_refuses_ssh_failure_for_current(self):
        code, out = run_guard("1", "", "", ignore=True, current="",
                              current_err="ssh: connect to host test-host port 22: Connection refused")
        self.assertEqual(code, 1, out)
        self.assertIn("Connection refused", out)

    def test_ignore_flag_old_build_current_reads_as_1(self):
        code, out = run_guard("1", "", "", ignore=True, current="",
                              current_err="Error: unknown flag: --print-storage-format\nUsage:\n  audiobook-organizer [command]")
        self.assertEqual(code, 0, out)
        self.assertIn("predates --print-storage-format", out)

    def test_unparseable_sidecar_is_quoted(self):
        code, out = run_guard("1", "1 2", "")
        self.assertEqual(code, 1, out)
        self.assertIn("read '1 2', not one integer >= 1", out)

    def test_decide_store_equal_or_below_prev_allows(self):
        code, _ = guard.decide(DB, BIN, "2", "1", "", False)
        self.assertEqual(code, 0)
        code, _ = guard.decide(DB, BIN, "2", "2", "", False)
        self.assertEqual(code, 0)


if __name__ == "__main__":
    unittest.main()
