#!/usr/bin/env python3
# file: scripts/test_storage_format_guard.py
# version: 2.3.1
# guid: 75f5cdf5-6d94-4c67-a679-062b1e13e414
# last-edited: 2026-11-09

"""Unit tests for scripts/storage_format_guard.py's decisions.

The guard reads the host over ssh; these tests hand its decide functions a
table of command results instead, so each rule is checked on its own. The
recipes that call it are tested end to end over a fake host in
scripts/tests/test_make_rollback.py. Run with:
    python3 -m unittest discover -s scripts -p 'test_storage_format_guard.py' -v
"""

import importlib.util
import json
import subprocess
import sys
import unittest
import unittest.mock
from pathlib import Path

_SCRIPT = Path(__file__).resolve().parent / "storage_format_guard.py"
_spec = importlib.util.spec_from_file_location("storage_format_guard", _SCRIPT)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
# Registered before exec_module (the importlib recipe): dataclasses, under
# `from __future__ import annotations`, looks the class's module up in
# sys.modules while the module body runs, and Python 3.12 (CI's) fails
# with AttributeError when it is absent.
sys.modules[_spec.name] = guard
_spec.loader.exec_module(guard)

R = guard.Result
NAME = "20261101T020304Z-0badc0de"
UNKNOWN_LEVELS = R("", "Error: unknown flag: --print-storage-levels\n", 1)
UNKNOWN_FORMAT = R("", "Error: unknown flag: --print-storage-format\n", 1)
MISSING = R("", "cat: /data/db.storage-levels: No such file or directory\n", 1)
SUDO = R("", "sudo: a password is required\n", 1)
NO_POINTER = R("", "cat: /data/db.migration-checkpoint: No such file or directory\n", 1)


def opts(**kw):
    base = {
        "host": "wayne-manor.invalid",
        "db": "/data/db",
        "bin": "/usr/local/bin/aorg",
        "state_dir": "/state",
        "user": "alfred",
        "archive_dir": "/archives",
    }
    base.update(kw)
    return guard.Opts(**base)


def table(**results):
    """A getter answering from results; an unexpected key fails the test."""
    defaults = {
        "probe": R(),
        "levels": R("books 0\ncore 0\n"),
        "prev_levels": R('{"books":0,"core":0}\n'),
    }
    defaults.update(results)

    def get(key):
        if key not in defaults:
            raise AssertionError(f"the guard read {key!r}, which this case does not expect")
        return defaults[key]

    return get


def record(levels, status="completed", release="v0.41.0"):
    return R(
        json.dumps(
            {
                "from_vector": {"levels": levels},
                "from_release": release,
                "status": status,
                "taken_at": "2026-11-01T02:03:04Z",
            }
        )
    )


POINTER = R(json.dumps({"name": NAME, "state": "completed"}))


class Rollback(unittest.TestCase):
    def decide(self, ignore=False, **results):
        return guard.decide_rollback(opts(), table(**results), ignore)

    def test_r1_store_r1_prev_passes(self):
        code, lines = self.decide()
        self.assertEqual(code, 0, lines)

    def test_r1_store_r0_prev_passes(self):
        code, lines = self.decide(prev_levels=UNKNOWN_LEVELS, prev_format=R("1\n"))
        self.assertEqual(code, 0, lines)

    def test_r1_store_prev_older_than_both_flags_passes(self):
        code, lines = self.decide(prev_levels=UNKNOWN_LEVELS, prev_format=UNKNOWN_FORMAT)
        self.assertEqual(code, 0, lines)

    def test_r2_store_r1_prev_refuses_naming_rollback_restore(self):
        code, lines = self.decide(levels=R("books 1\ncore 1\n"))
        self.assertEqual(code, 1)
        self.assertIn("make rollback-restore", "\n".join(lines))

    def test_r2_store_r0_prev_refuses(self):
        code, _ = self.decide(
            levels=R("books 1\ncore 1\n"), prev_levels=UNKNOWN_LEVELS, prev_format=R("1\n")
        )
        self.assertEqual(code, 1)

    def test_store_below_prev_passes(self):
        """A binary swap to a build that serves more is fine: it brings the
        store forward with its own steps (05-storage §5S.3.7)."""
        code, _ = self.decide(prev_levels=R('{"books":1,"core":0}'))
        self.assertEqual(code, 0)

    def test_inflight_refuses_with_no_override(self):
        code, lines = self.decide(ignore=True, probe=R("/state/restore/x/inflight.json\n"))
        self.assertEqual(code, 1)
        self.assertIn("nothing overrides", "\n".join(lines))

    def test_probe_sudo_refusal_names_the_line(self):
        code, lines = self.decide(probe=SUDO)
        self.assertEqual(code, 1)
        self.assertIn(f"sudo -n {guard.HELPER_DIR}/inflight-probe", "\n".join(lines))

    def test_probe_sudo_on_stderr_with_exit_0_refuses(self):
        code, _ = self.decide(probe=R("", "sudo: unable to resolve host\n", 0))
        self.assertEqual(code, 1)

    def test_prev_crash_is_not_guessed(self):
        code, lines = self.decide(prev_levels=R("", "panic: runtime error\n", 2))
        self.assertEqual(code, 1)
        self.assertIn("cannot tell", "\n".join(lines))

    def test_prev_garbage_json_refuses(self):
        for out in ('{"books":"0"}', '["books"]', '{"books":-1}', '{"books":true}', "1"):
            code, _ = self.decide(prev_levels=R(out))
            self.assertEqual(code, 1, out)

    def test_r0_prev_format_2_does_not_map(self):
        code, lines = self.decide(prev_levels=UNKNOWN_LEVELS, prev_format=R("2\n"))
        self.assertEqual(code, 1)
        self.assertIn("format 2", "\n".join(lines))

    def test_levels_sidecar_unreadable_refuses_even_with_ignore(self):
        code, lines = self.decide(ignore=True, levels=SUDO)
        self.assertEqual(code, 1)
        self.assertIn("does not cover levels", "\n".join(lines))

    def test_levels_sidecar_damaged_refuses(self):
        for text in ("books\n", "books x\n", "books 0\nbooks 1\n", "books -1\n"):
            code, _ = self.decide(levels=R(text))
            self.assertEqual(code, 1, text)

    def test_owner_absent_from_prev_is_level_0(self):
        code, _ = self.decide(levels=R("books 0\ncore 0\nbatsignal 1\n"))
        self.assertEqual(code, 1)
        code, _ = self.decide(levels=R("books 0\ncore 0\nbatsignal 0\n"))
        self.assertEqual(code, 0)


class LegacyProjection(unittest.TestCase):
    """No .storage-levels: HEAD's projection rule, ROLLBACK_IGNORE_FORMAT."""

    def decide(self, ignore=False, **results):
        results.setdefault("levels", MISSING)
        results.setdefault("pointer", NO_POINTER)
        results.setdefault("prev_levels", UNKNOWN_LEVELS)
        results.setdefault("prev_format", R("1\n"))
        return guard.decide_rollback(opts(), table(**results), ignore)

    def test_format_1_passes(self):
        self.assertEqual(self.decide(format=R("1\n"))[0], 0)

    def test_format_2_refuses_and_ignore_does_not_override(self):
        code, lines = self.decide(ignore=True, format=R("2\n"))
        self.assertEqual(code, 1)
        self.assertIn("does not override", "\n".join(lines))

    def test_unreadable_without_ignore_refuses(self):
        code, lines = self.decide(format=SUDO)
        self.assertEqual(code, 1)
        self.assertIn("ROLLBACK_IGNORE_FORMAT=1", "\n".join(lines))

    def test_unreadable_with_ignore_and_current_format_1_passes(self):
        code, lines = self.decide(ignore=True, format=SUDO, current_format=R("1\n"))
        self.assertEqual(code, 0, lines)
        self.assertIn("WARNING", lines[0])

    def test_unreadable_with_ignore_and_newer_current_refuses(self):
        self.assertEqual(self.decide(ignore=True, format=SUDO, current_format=R("2\n"))[0], 1)

    def test_unreadable_with_ignore_and_silent_current_refuses(self):
        self.assertEqual(
            self.decide(ignore=True, format=SUDO, current_format=R("", "Segmentation fault", 139))[
                0
            ],
            1,
        )

    def test_unreadable_with_ignore_and_old_current_passes(self):
        self.assertEqual(self.decide(ignore=True, format=SUDO, current_format=UNKNOWN_FORMAT)[0], 0)


class Restore(unittest.TestCase):
    def decide(self, o=None, **results):
        results.setdefault("pointer", POINTER)
        results.setdefault("record:" + NAME, record({"books": 0, "core": 0}))
        return guard.decide_restore(o or opts(), table(**results))

    def test_checkpoint_matching_prev_prints_the_restore_command(self):
        code, lines, cmd = self.decide()
        self.assertEqual(code, 0, lines)
        self.assertEqual(cmd, f"sudo -n {guard.HELPER_DIR}/restore-offline checkpoint {NAME}")
        joined = "\n".join(lines)
        self.assertIn("2026-11-01T02:03:04Z", joined)
        self.assertIn("credential change", joined)

    def test_mismatch_refuses_naming_from_release(self):
        code, lines, cmd = self.decide(prev_levels=R('{"books":1,"core":1}'))
        self.assertEqual((code, cmd), (1, ""))
        self.assertIn("v0.41.0", "\n".join(lines))

    def test_mismatch_without_from_release_says_so(self):
        code, lines, _ = self.decide(
            prev_levels=R('{"books":1,"core":1}'),
            **{"record:" + NAME: record({"books": 0}, release="")},
        )
        self.assertEqual(code, 1)
        self.assertIn("records no from_release", "\n".join(lines))

    def test_equality_not_at_most(self):
        code, _, _ = self.decide(prev_levels=R('{"books":1,"core":0}'))
        self.assertEqual(code, 1)

    def test_absent_owner_equals_zero(self):
        code, _, _ = self.decide(prev_levels=R("{}"))
        self.assertEqual(code, 0)

    def test_inflight_refuses_first(self):
        code, lines, _ = self.decide(probe=R("/state/restore/x/inflight.json\n"))
        self.assertEqual(code, 1)
        self.assertIn("in flight", "\n".join(lines))

    def test_no_pointer_names_rollback_archive(self):
        code, lines, _ = self.decide(
            pointer=R("", "cat: /data/db.migration-checkpoint: No such file or directory", 1)
        )
        self.assertEqual(code, 1)
        self.assertIn("ROLLBACK_ARCHIVE", "\n".join(lines))

    def test_head_style_pointer_refuses(self):
        code, lines, _ = self.decide(pointer=R("/data/.migration-backups/x\n"))
        self.assertEqual(code, 1)
        self.assertIn("not a checkpoint pointer", "\n".join(lines))

    def test_pointer_name_outside_the_grammar_refuses(self):
        code, _, _ = self.decide(pointer=R(json.dumps({"name": "../../etc/x"})))
        self.assertEqual(code, 1)

    def test_consumed_record_refuses(self):
        code, lines, _ = self.decide(**{"record:" + NAME: record({"books": 0}, status="consumed")})
        self.assertEqual(code, 1)
        self.assertIn("consumed", "\n".join(lines))

    def test_unreadable_record_names_checkpoint_dir(self):
        code, lines, _ = self.decide(**{"record:" + NAME: SUDO})
        self.assertEqual(code, 1)
        self.assertIn("checkpoint_dir", "\n".join(lines))

    def test_record_is_read_through_the_helper_as_the_service_user(self):
        self.assertEqual(
            guard.commands(opts(), NAME)["record"],
            f"sudo -n -u alfred {guard.HELPER_DIR}/checkpoint-record {NAME}",
        )
        self.assertEqual(opts(checkpoint_dir="/cp").cp_area, "/cp/.mediacore-checkpoints")
        self.assertEqual(opts().cp_area, "/data/.mediacore-checkpoints")

    def into(self, levels):
        return R(f"rollback helper: running as alfred\nLEVELS-BEGIN\n{levels}LEVELS-END\n")

    def test_archive_reads_the_scratch_copy(self):
        o = opts(archive="batcave.tar.gz")
        code, lines, cmd = guard.decide_restore(o, table(into=self.into("books 0\ncore 0\n")))
        self.assertEqual(code, 0, lines)
        self.assertEqual(cmd, f"sudo -n {guard.HELPER_DIR}/restore-offline archive batcave.tar.gz")
        self.assertEqual(
            guard.commands(o)["into"],
            f"sudo -n {guard.HELPER_DIR}/restore-offline into-check batcave.tar.gz",
        )

    def test_archive_mismatch_refuses(self):
        o = opts(archive="batcave.tar.gz")
        code, lines, _ = guard.decide_restore(o, table(into=self.into("books 1\ncore 1\n")))
        self.assertEqual(code, 1)
        self.assertIn("records no version", "\n".join(lines))

    def test_archive_extraction_failure_refuses(self):
        o = opts(archive="batcave.tar.gz")
        for r in (R("", "backup: extract store: not a tar", 1), R("no markers here\n")):
            code, _, _ = guard.decide_restore(o, table(into=r))
            self.assertEqual(code, 1, r)


class ReviewProbes(unittest.TestCase):
    """T3 review of #4511: the guard-level probes (G10, P4, P5, P7)."""

    def restore(self, rec):
        get = table(pointer=POINTER, **{"record:" + NAME: R(json.dumps(rec))})
        return guard.decide_restore(opts(), get)

    def base(self, **kw):
        rec = {
            "from_vector": {"levels": {"books": 0, "core": 0}},
            "from_release": "v0.41.0",
            "status": "completed",
            "taken_at": "2026-11-01T02:03:04Z",
        }
        rec.update(kw)
        return rec

    def test_g10_record_without_levels_refuses(self):
        for fv in ({}, {"levels": None}, {"versions": {"books": 66}}, None, []):
            code, lines, cmd = self.restore(self.base(from_vector=fv))
            self.assertEqual((code, cmd), (1, ""), fv)
            self.assertIn("from_vector levels", "\n".join(lines), fv)

    def test_p5_released_checkpoint_refuses_before_the_stop(self):
        """core/storage/checkpoints.go Restorable(): open or served with
        delete_after set is released; only completed passes regardless."""
        for status, delete_after, ok in (
            ("served", "2026-11-08T00:00:00Z", False),
            ("open", "2026-11-08T00:00:00Z", False),
            ("served", None, True),
            ("open", None, True),
            ("completed", "2026-11-08T00:00:00Z", True),
            ("consumed", None, False),
            ("restore_left", None, False),
            ("riddled", None, False),
            ("", None, False),
        ):
            rec = self.base(status=status)
            if delete_after:
                rec["delete_after"] = delete_after
            code, lines, _ = self.restore(rec)
            self.assertEqual(code == 0, ok, (status, delete_after, lines))

    def test_p7_pointer_without_levels_sidecar_refuses_rollback(self):
        """A store with a checkpoint pointer was opened by a core build: a
        missing .storage-levels is damage, not a pre-core store."""
        get = table(levels=MISSING, pointer=POINTER, format=R("1\n"))
        code, lines = guard.decide_rollback(opts(), get, False)
        self.assertEqual(code, 1, lines)
        self.assertIn("migration-checkpoint", "\n".join(lines))

    def test_p7_no_pointer_keeps_the_projection_rule(self):
        missing_pointer = R(
            "", "cat: /data/db.migration-checkpoint: No such file or directory\n", 1
        )
        get = table(levels=MISSING, pointer=missing_pointer, format=R("1\n"))
        code, lines = guard.decide_rollback(opts(), get, False)
        self.assertEqual(code, 0, lines)

    def test_p4_stopped_check_ssh_failure_says_state_unknown(self):
        lines = "\n".join(
            guard.check_stopped(opts(), table(unit=R("", "ssh: connect: timed out\n", 255)))
        )
        self.assertIn("unknown", lines)
        self.assertNotIn("Something started it", lines)

    def test_control_characters_from_the_host_are_stripped(self):
        lines = "\n".join(
            guard.check_inflight(opts(), table(probe=R("/state/restore/x\x1b[2J/inflight.json\n")))
        )
        self.assertNotIn("\x1b", lines)
        self.assertIn("in flight", lines)


class Stopped(unittest.TestCase):
    def test_states(self):
        for state, ok in (
            ("inactive", True),
            ("failed", True),
            ("active", False),
            ("activating", False),
            ("deactivating", False),
        ):
            lines = guard.check_stopped(opts(), table(unit=R(f"ActiveState={state}\nSubState=x\n")))
            self.assertEqual(lines == [], ok, state)

    def test_ssh_failure_refuses(self):
        self.assertNotEqual(guard.check_stopped(opts(), table(unit=R("", "ssh: timeout", 255))), [])


class Arguments(unittest.TestCase):
    def test_unsafe_values_are_refused(self):
        for field, bad in (
            ("db", "/data/my db"),
            ("db", "/data/$(id)"),
            ("db", "relative/db"),
            ("db", "/data/a;b"),
            ("db", "/data/../etc"),
            ("bin", "/b/'x'"),
            ("archive", "../data/db"),
            ("archive", "/tmp/joker.tar"),
            ("archive", "x; touch /tmp/pwned-r127"),
            ("archive", "-rf"),
            ("archive", "x\n"),
            ("user", "root"),
            ("unit", "x.service; id"),
            ("unit", "batman:robin.service"),
            ("host", "-oProxyCommand=id"),
        ):
            with self.subTest(field=field, bad=bad):
                self.assertNotEqual(guard._validate(opts(**{field: bad})), "")
        self.assertEqual(guard._validate(opts(archive="batcave-20261101.tar.gz")), "")

    def test_ssh_keeps_the_connection_alive(self):
        """ServerAliveInterval: a restore or wait that prints nothing for
        minutes must not be dropped by a NAT or firewall idle timeout."""
        pairs = list(zip(guard.SSH_OPTS[::2], guard.SSH_OPTS[1::2]))
        self.assertIn(("-o", "ServerAliveInterval=15"), pairs)
        self.assertIn(("-o", "ServerAliveCountMax=4"), pairs)
        seen = []

        def fake_run(argv, **kw):
            seen.append(argv)
            return subprocess.CompletedProcess(argv, 0, "", "")

        with unittest.mock.patch.object(guard.subprocess, "run", fake_run):
            guard.run_remote("wayne-manor.invalid", "true", timeout=5)
        self.assertEqual(seen[0][: 1 + len(guard.SSH_OPTS)], ["ssh", *guard.SSH_OPTS])

    def test_sudoers_cli(self):
        import contextlib
        import io

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = guard.main(
                ["sudoers", "--deploy-user", "deploy", "--db", "/path/to/audiobooks.pebble"]
            )
        self.assertEqual(code, 0)
        self.assertIn("deploy ALL=(audiobook) NOPASSWD:", buf.getvalue())
        self.assertNotIn("*", buf.getvalue())
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(guard.main(["sudoers", "--deploy-user", "deploy", "--db", "/x y"]), 2)


class HostConfig(unittest.TestCase):
    CONF = (
        "# rollback helpers\nbin = /usr/local/bin/aorg\ndb = /data/db\nstate_dir = /var/lib/x\n"
        "unit = audiobook-organizer.service\nservice_user = alfred\narchive_dir = /archives\ncheckpoint_dir =\n"
    )

    def test_fills_the_host_values(self):
        o = guard.Opts(host="h", db="/data/db", bin="/usr/local/bin/aorg")
        self.assertEqual(guard.load_host_config(o, table(host_config=R(self.CONF))), [])
        self.assertEqual(
            (o.state_dir, o.user, o.archive_dir), ("/var/lib/x", "alfred", "/archives")
        )

    def test_refuses_a_mismatch_or_a_missing_config(self):
        o = guard.Opts(host="h", db="/data/other", bin="/usr/local/bin/aorg")
        lines = guard.load_host_config(o, table(host_config=R(self.CONF)))
        self.assertIn("DEPLOY_DB", "\n".join(lines))
        o = guard.Opts(host="h", db="/data/db", bin="/usr/local/bin/aorg")
        lines = guard.load_host_config(
            o, table(host_config=R("", "cat: No such file or directory", 1))
        )
        self.assertIn("Install the rollback helpers", "\n".join(lines))
        lines = guard.load_host_config(o, table(host_config=R("bin = /usr/local/bin/aorg\n")))
        self.assertIn("does not parse", "\n".join(lines))


SECRET = "hunter2"


class NoEcho(unittest.TestCase):
    """The sidecars sit in the service's own directory, which the service
    user can write, so a symlink there could point anywhere. They are read
    as the service user, never root, and no refusal prints what was read:
    a length and a hash at most, and only names that match a fixed grammar."""

    def test_sidecars_are_read_as_the_service_user(self):
        o = opts()
        c = guard.commands(o)
        for key, suffix in (
            ("levels", ".storage-levels"),
            ("format", ".storage-format"),
            ("pointer", ".migration-checkpoint"),
        ):
            self.assertEqual(c[key], f"sudo -n -u alfred /usr/bin/cat /data/db{suffix}", key)
        root_line, user_line = guard.sudoers(o, "deploy").split("\ndeploy ALL=(alfred)")
        self.assertNotIn("/usr/bin/cat", root_line)
        self.assertIn("/usr/bin/cat /data/db.storage-levels", user_line)

    def assert_quiet(self, lines):
        joined = "\n".join(lines)
        self.assertNotIn(SECRET, joined)
        return joined

    def test_a_bad_levels_sidecar_is_not_echoed(self):
        for text in (
            f"{SECRET} x\n",
            f"books {SECRET}\n",
            f"Books{SECRET} 1\n",
            "books 1\nbooks 2\n",
        ):
            with self.subTest(text=text):
                code, lines = guard.decide_rollback(opts(), table(levels=R(text)), False)
                self.assertEqual(code, 1)
                self.assert_quiet(lines)

    def test_owner_names_follow_a_grammar(self):
        self.assertIsNone(guard.parse_sidecar("Books 1\n")[0])
        self.assertIsNone(guard.parse_sidecar("bo oks 1\n")[0])
        self.assertIsNone(guard.parse_vector('{"Books": 1}'))
        self.assertIsNone(guard.parse_vector('{"b\\u0007": 1}'))
        self.assertEqual(guard.parse_sidecar("abo.books 1\n")[0], {"abo.books": 1})

    def test_a_bad_pointer_is_not_echoed(self):
        for pointer in (R(f"{SECRET} not json"), R(json.dumps({"name": SECRET}))):
            with self.subTest(pointer=pointer.stdout):
                code, lines, _ = guard.decide_restore(opts(), table(pointer=pointer))
                self.assertEqual(code, 1)
                joined = self.assert_quiet(lines)
                if "not json" in pointer.stdout:
                    self.assertIn("sha256", joined)

    def test_record_fields_are_not_echoed_unless_they_match(self):
        for rec in (
            record({"books": 0, "core": 0}, status=SECRET),
            record({"books": 0}, release=f"{SECRET}; rm -rf"),
        ):
            with self.subTest(rec=rec.stdout):
                results = {"pointer": POINTER, "record:" + NAME: rec}
                results.setdefault("prev_levels", R('{"books":1,"core":1}'))
                code, lines, _ = guard.decide_restore(opts(), table(**results))
                self.assertEqual(code, 1)
                self.assert_quiet(lines)

    def test_cause_hashes_stdout(self):
        text = guard._cause(R(f"{SECRET}\n", "cat: oops\n", 1))
        self.assertNotIn(SECRET, text)
        self.assertIn("8 bytes", text)
        self.assertIn("sha256 ", text)
        self.assertIn("cat: oops", text)


class Sudoers(unittest.TestCase):
    def test_no_wildcards_and_one_bare_grant_per_validating_helper(self):
        text = guard.sudoers(opts(), "deploy")
        self.assertNotIn("*", text)
        h = guard.HELPER_DIR
        self.assertIn(f'{h}/inflight-probe ""', text)
        self.assertIn(f'{h}/swap-prev ""', text)
        self.assertIn(f"{h}/restore-offline, ", text)
        self.assertTrue(text.rstrip().endswith(f"{h}/checkpoint-record"), text)
        self.assertNotIn("tee", text)
        self.assertNotIn("find", text)

    def test_parse_vector_and_sidecar(self):
        self.assertEqual(guard.parse_vector('{"core":0,"books":2}'), {"core": 0, "books": 2})
        self.assertEqual(guard.parse_sidecar("books 2\ncore 0\n")[0], {"books": 2, "core": 0})


if __name__ == "__main__":
    unittest.main()
