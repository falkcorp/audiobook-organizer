#!/usr/bin/env python3
# file: scripts/tests/test_finish_credential_migration.py
# version: 1.0.0
# guid: 7a41f9c3-6b28-4e15-93d7-c05a8ef21b46
# last-edited: 2026-09-09

"""Tests for scripts/finish_credential_migration.py.

Run with:
    python3 -m unittest scripts.tests.test_finish_credential_migration -v
    # or, matching CI (.github/workflows/ci.yml):
    python3 -m unittest discover -s scripts -p 'test_*.py' -v

WHY THESE EXIST
===============
The script's ``--apply`` path moves the settings encryption key -- the one file
in this system that cannot be regenerated -- and it CANNOT be exercised on the
real host from a non-interactive session, because ``sudo`` there needs a
password. So the destructive path would otherwise ship having executed nowhere.

Everything below runs against tmpdirs. ``move_key`` and ``probe`` are pure
functions of paths, which is what makes that possible.
"""

from __future__ import annotations

import contextlib
import io
import os
import pwd
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import finish_credential_migration as fcm  # noqa: E402

KEY = bytes(range(32))


class ProbeTests(unittest.TestCase):
    """The tri-state stat. Getting UNKNOWN wrong is how a non-root run
    concludes a credential does not exist."""

    def test_present(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td, "f")
            p.write_bytes(b"x")
            self.assertIs(fcm.probe(p).presence, fcm.Presence.PRESENT)

    def test_absent(self):
        with tempfile.TemporaryDirectory() as td:
            self.assertIs(fcm.probe(Path(td, "nope")).presence, fcm.Presence.ABSENT)

    def test_dangling_symlink_is_present_not_absent(self):
        # lstat, not stat: a symlink pointing nowhere is still a directory
        # entry occupying that name, and reporting it absent would hide it.
        with tempfile.TemporaryDirectory() as td:
            link = Path(td, "link")
            link.symlink_to(Path(td, "missing-target"))
            self.assertIs(fcm.probe(link).presence, fcm.Presence.PRESENT)

    @unittest.skipIf(os.geteuid() == 0, "root ignores permission bits")
    def test_unreadable_directory_is_unknown_not_absent(self):
        """The regression this whole class exists for.

        On the real host the app-data directory is 0700 audiobook:audiobook, so
        a non-root run cannot traverse it. ``Path.exists()`` answers False --
        indistinguishable from "no key here" -- and the first version of this
        script printed a confident note about a directory it had never seen.
        """
        with tempfile.TemporaryDirectory() as td:
            secret = Path(td, "secret")
            secret.mkdir(mode=0o700)
            (secret / fcm.KEY_NAME).write_bytes(KEY)
            os.chmod(secret, 0o000)
            try:
                target = secret / fcm.KEY_NAME
                # The stdlib helper cannot tell these apart; we must.
                self.assertFalse(target.exists())
                pr = fcm.probe(target)
                self.assertIs(pr.presence, fcm.Presence.UNKNOWN)
                self.assertIn("permission denied", pr.reason)
                self.assertIn("root", pr.reason)
            finally:
                os.chmod(secret, 0o700)

    def test_parent_is_a_file_is_absent(self):
        # Distinct from EACCES: nothing can ever be under a non-directory, so
        # ABSENT is the truthful answer and UNKNOWN would block needlessly.
        with tempfile.TemporaryDirectory() as td:
            notdir = Path(td, "file")
            notdir.write_bytes(b"x")
            self.assertIs(fcm.probe(notdir / fcm.KEY_NAME).presence, fcm.Presence.ABSENT)


class MoveKeyTests(unittest.TestCase):
    """The destructive path, which cannot be run on prod from a script."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)
        self.legacy = Path(self.td.name, "appdata")
        self.state = Path(self.td.name, "var-lib")
        self.legacy.mkdir()
        self.src = self.legacy / fcm.KEY_NAME
        self.src.write_bytes(KEY)
        self.dst = self.state / fcm.KEY_NAME
        # os.chown needs root; the owner lookup is asserted separately.
        self.owner_patch = mock.patch.object(fcm.os, "chown")
        self.chown = self.owner_patch.start()
        self.addCleanup(self.owner_patch.stop)

    def _move(self, *, apply=True, owner=None) -> str:
        """Run the move, returning what it printed instead of spraying the
        operator narration across the CI log."""
        # NOT os.getlogin(): it raises OSError without a controlling terminal,
        # which is precisely how CI runs.
        me = pwd.getpwuid(os.getuid()).pw_name
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            fcm.move_key(self.src, self.dst, owner or me, apply=apply)
        return buf.getvalue()

    def test_dry_run_changes_nothing(self):
        # Also passes a nonexistent owner: a dry run must not even get as far
        # as resolving the service user, let alone touching the filesystem.
        out = self._move(apply=False, owner="nobody-at-all")
        self.assertTrue(self.src.exists())
        self.assertFalse(self.dst.exists())
        self.chown.assert_not_called()
        # The dry run must state the rename-not-delete promise, since that is
        # what an operator decides to proceed on.
        self.assertIn("NOT deleted", out)

    def test_apply_installs_and_renames_source_aside(self):
        self._move()
        self.assertEqual(self.dst.read_bytes(), KEY)
        self.assertEqual(self.dst.stat().st_mode & 0o777, 0o600)
        # The source is RENAMED, never deleted: it is inert once the new path
        # works and it is the only rollback for an unregenerable file.
        self.assertFalse(self.src.exists())
        aside = [p for p in self.legacy.iterdir() if p.name.startswith(fcm.MIGRATED_PREFIX)]
        self.assertEqual(len(aside), 1, f"expected one renamed-aside key, got {aside}")
        self.assertEqual(aside[0].read_bytes(), KEY)

    def test_new_directory_is_created_0700(self):
        self._move()
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o700)

    def test_unknown_service_user_refuses_before_touching_anything(self):
        with self.assertRaises(fcm.Refused) as cm:
            self._move(owner="definitely-not-a-user-9f3b")
        self.assertIn("does not exist", str(cm.exception))
        # Order matters: the lookup must happen BEFORE the copy, or a bad
        # owner leaves a key sitting at the destination with root ownership.
        self.assertTrue(self.src.exists())
        self.assertFalse(self.dst.exists())

    def test_copy_digest_mismatch_leaves_source_untouched(self):
        real = fcm.sha256
        calls = {"n": 0}

        def flaky(path):
            calls["n"] += 1
            return "corrupted" if calls["n"] == 2 else real(path)

        with mock.patch.object(fcm, "sha256", flaky):
            with self.assertRaises(fcm.Refused) as cm:
                self._move()
        self.assertIn("digest mismatch", str(cm.exception))
        self.assertEqual(self.src.read_bytes(), KEY)
        self.assertFalse(self.dst.exists())
        # The temp file must not be left behind either.
        self.assertEqual(list(self.state.iterdir()), [])

    def test_migrated_files_are_discoverable_by_a_later_run(self):
        self._move()
        found = fcm.find_migrated_keys(self.legacy)
        self.assertEqual(len(found), 1)
        self.assertTrue(found[0].name.startswith(fcm.MIGRATED_PREFIX))


class LegacyRemovalProofTests(unittest.TestCase):
    """``--remove-legacy-key`` must refuse until the app has demonstrably
    started from the new location."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)
        self.migrated = Path(self.td.name, fcm.MIGRATED_PREFIX + "20260909120000")
        self.migrated.write_bytes(KEY)

    def _plan(self, **over):
        plan = fcm.Plan()
        plan.observed_state_dir = over.pop("observed", fcm.NEW_STATE_DIR)
        plan.key_dst = fcm.Probe(
            over.pop("dst_presence", fcm.Presence.PRESENT), fcm.NEW_STATE_DIR / fcm.KEY_NAME
        )
        plan.journal = fcm.Journal(legacy_key_line=over.pop("legacy_line", None))
        assert not over, over
        return plan

    def test_all_proofs_satisfied(self):
        after = self.migrated.stat().st_mtime + 60
        with mock.patch.object(fcm, "process_start_time", return_value=after):
            self.assertEqual(fcm.legacy_removal_blockers(self._plan(), self.migrated), [])

    def test_refuses_when_process_predates_the_rename(self):
        """The subtle one. Everything else can look right while the running
        process is the very one that was started BEFORE the key moved -- so it
        read the key from the old path and proves nothing about the new one."""
        before = self.migrated.stat().st_mtime - 60
        with mock.patch.object(fcm, "process_start_time", return_value=before):
            blockers = fcm.legacy_removal_blockers(self._plan(), self.migrated)
        self.assertTrue(any("started BEFORE the rename" in b for b in blockers))

    def test_refuses_when_the_invocation_used_the_legacy_path(self):
        after = self.migrated.stat().st_mtime + 60
        with mock.patch.object(fcm, "process_start_time", return_value=after):
            blockers = fcm.legacy_removal_blockers(
                self._plan(legacy_line="... expected_at=/var/lib/..."), self.migrated
            )
        self.assertTrue(any("legacy path" in b for b in blockers))

    def test_refuses_when_no_key_at_the_new_location(self):
        after = self.migrated.stat().st_mtime + 60
        with mock.patch.object(fcm, "process_start_time", return_value=after):
            blockers = fcm.legacy_removal_blockers(
                self._plan(dst_presence=fcm.Presence.ABSENT), self.migrated
            )
        self.assertTrue(any("no key confirmed" in b for b in blockers))

    def test_refuses_when_the_service_is_not_running(self):
        with mock.patch.object(fcm, "process_start_time", return_value=None):
            blockers = fcm.legacy_removal_blockers(self._plan(), self.migrated)
        self.assertTrue(any("when the running process started" in b for b in blockers))

    def test_refuses_when_state_dir_diverges(self):
        after = self.migrated.stat().st_mtime + 60
        with mock.patch.object(fcm, "process_start_time", return_value=after):
            blockers = fcm.legacy_removal_blockers(
                self._plan(observed=Path("/somewhere/else")), self.migrated
            )
        self.assertTrue(any("new state dir" in b for b in blockers))


class RunDecodeTests(unittest.TestCase):
    def test_invalid_utf8_does_not_raise(self):
        """The failure that killed the first prod run: this host's journal
        contains an invalid continuation byte, and ``text=True`` raises
        ``UnicodeDecodeError`` from inside ``communicate()``."""
        rc, out, _ = fcm.run(
            [sys.executable, "-c", r"import sys; sys.stdout.buffer.write(b'token_file=/a\xe2 x')"],
            check=False,
        )
        self.assertEqual(rc, 0)
        self.assertIn("token_file=/a", out)

    def test_nonzero_raises_when_checked(self):
        with self.assertRaises(fcm.Refused):
            fcm.run([sys.executable, "-c", "raise SystemExit(3)"])


class JournalTests(unittest.TestCase):
    """A failed lookup and an empty one must not be the same value: an
    unidentifiable live token is what stops the script calling anything stale."""

    def test_no_invocation_is_a_problem_not_an_empty_result(self):
        with mock.patch.object(fcm, "unit_property", return_value=""):
            j = fcm.read_journal()
        self.assertIsNotNone(j.problem)
        self.assertIsNone(j.token_file)

    def test_journalctl_exit_1_is_an_empty_result_not_a_failure(self):
        # journalctl exits 1 for "no entries matched", which is legitimate.
        with mock.patch.object(fcm, "unit_property", return_value="abc123"), mock.patch.object(
            fcm, "run", return_value=(1, "", "")
        ):
            j = fcm.read_journal()
        self.assertIsNone(j.problem)
        self.assertIsNone(j.token_file)

    def test_journalctl_hard_failure_is_a_problem(self):
        with mock.patch.object(fcm, "unit_property", return_value="abc123"), mock.patch.object(
            fcm, "run", return_value=(127, "", "journalctl: not found")
        ):
            j = fcm.read_journal()
        self.assertIsNotNone(j.problem)

    def test_parses_token_path_and_expiry(self):
        line = (
            'level=INFO msg="Emergency access token written" '
            "token_file=/var/lib/audiobook-organizer/.bootstrap-token "
            "expires_at=2026-09-09T16:27:54-04:00"
        )
        with mock.patch.object(fcm, "unit_property", return_value="abc123"), mock.patch.object(
            fcm, "run", return_value=(0, line + "\n", "")
        ):
            j = fcm.read_journal()
        self.assertEqual(j.token_file, Path("/var/lib/audiobook-organizer/.bootstrap-token"))
        self.assertEqual(j.token_expires, "2026-09-09T16:27:54-04:00")
        self.assertIsNone(j.legacy_key_line)

    def test_recognises_the_legacy_key_warning(self):
        # The needle is ASCII on purpose: the real message contains an em dash,
        # and shipping that through journalctl's --grep is an encoding problem.
        line = (
            'level=WARN msg="settings encryption key found at its OLD location" '
            "found_at=/mnt/x/.appdata/.encryption_key "
            "expected_at=/var/lib/audiobook-organizer/.encryption_key"
        )
        with mock.patch.object(fcm, "unit_property", return_value="abc123"), mock.patch.object(
            fcm, "run", return_value=(0, line + "\n", "")
        ):
            j = fcm.read_journal()
        self.assertIsNotNone(j.legacy_key_line)
        self.assertIsNone(j.token_file)


class BinaryMarkerTests(unittest.TestCase):
    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)

    def test_finds_the_marker(self):
        p = Path(self.td.name, "bin")
        p.write_bytes(b"\x00" * 1000 + fcm.CAPABILITY_MARKER + b"\x00" * 1000)
        self.assertTrue(fcm.binary_has_marker(p))

    def test_absent_marker(self):
        p = Path(self.td.name, "bin")
        p.write_bytes(b"\x00" * 5000)
        self.assertFalse(fcm.binary_has_marker(p))

    def test_marker_straddling_a_chunk_boundary(self):
        """The overlap in the chunked read. A 4 MiB boundary landing inside the
        marker would otherwise report a post-#3171 binary as pre-#3171 -- which
        fails safe, but by luck rather than design."""
        chunk = 4 << 20
        half = len(fcm.CAPABILITY_MARKER) // 2
        p = Path(self.td.name, "bin")
        p.write_bytes(b"\x00" * (chunk - half) + fcm.CAPABILITY_MARKER + b"\x00" * 64)
        self.assertTrue(fcm.binary_has_marker(p))

    def test_missing_binary_refuses(self):
        with self.assertRaises(fcm.Refused) as cm:
            fcm.binary_has_marker(Path(self.td.name, "nope"))
        self.assertIn("not found", str(cm.exception))


class CandidateDirTests(unittest.TestCase):
    def test_new_dir_first_then_legacy(self):
        dirs = fcm.candidate_dirs(Path("/mnt/x/.appdata/audiobooks.pebble"))
        self.assertEqual(dirs, [fcm.NEW_STATE_DIR, Path("/mnt/x/.appdata")])

    def test_deduplicates_when_they_coincide(self):
        dirs = fcm.candidate_dirs(fcm.NEW_STATE_DIR / "audiobooks.pebble")
        self.assertEqual(dirs, [fcm.NEW_STATE_DIR])

    def test_unknown_database_path(self):
        self.assertEqual(fcm.candidate_dirs(None), [fcm.NEW_STATE_DIR])


if __name__ == "__main__":
    unittest.main()
