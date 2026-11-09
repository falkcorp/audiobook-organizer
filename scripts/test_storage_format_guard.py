#!/usr/bin/env python3
# file: scripts/test_storage_format_guard.py
# version: 2.0.0
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
import unittest
from pathlib import Path

_SCRIPT = Path(__file__).resolve().parent / "storage_format_guard.py"
_spec = importlib.util.spec_from_file_location("storage_format_guard", _SCRIPT)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
_spec.loader.exec_module(guard)

R = guard.Result
NAME = "20261101T020304Z-0badc0de"
UNKNOWN_LEVELS = R("", "Error: unknown flag: --print-storage-levels\n", 1)
UNKNOWN_FORMAT = R("", "Error: unknown flag: --print-storage-format\n", 1)
MISSING = R("", "cat: /data/db.storage-levels: No such file or directory\n", 1)
SUDO = R("", "sudo: a password is required\n", 1)


def opts(**kw):
    base = {
        "host": "wayne-manor.invalid",
        "db": "/data/db",
        "bin": "/usr/local/bin/aorg",
        "state_dir": "/state",
        "user": "alfred",
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
        self.assertIn(
            "sudo -n /usr/bin/find /state -maxdepth 3 -path '/state/restore/*/inflight.json' -print",
            "\n".join(lines),
        )

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
        self.assertEqual(
            cmd,
            "sudo -n -u alfred /usr/bin/env /usr/local/bin/aorg storage restore-offline "
            f"--db /data/db --checkpoint {NAME} --accept-loss",
        )
        joined = "\n".join(lines)
        self.assertIn("2026-11-01T02:03:04Z", joined)
        self.assertIn("credential change", joined)

    def test_offline_env_is_passed(self):
        _, _, cmd = self.decide(o=opts(offline_env="HOME=/state ABK_STATE_DIR=/state"))
        self.assertIn("/usr/bin/env HOME=/state ABK_STATE_DIR=/state /usr/local/bin/aorg", cmd)

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
        self.assertIn("DEPLOY_CHECKPOINT_DIR", "\n".join(lines))

    def test_checkpoint_dir_override(self):
        o = opts(checkpoint_dir="/cp")
        self.assertEqual(
            guard.commands(o, NAME)["record"],
            f"sudo -n -u alfred cat /cp/.mediacore-checkpoints/{NAME}.json",
        )
        self.assertEqual(
            guard.commands(opts(), NAME)["record"],
            f"sudo -n -u alfred cat /data/.mediacore-checkpoints/{NAME}.json",
        )

    def test_archive_reads_the_scratch_copy(self):
        o = opts(archive="/archives/batcave.tar.gz")
        get = table(into_clean=R(), into=R(), into_levels=R("books 0\ncore 0\n"))
        code, lines, cmd = guard.decide_restore(o, get)
        self.assertEqual(code, 0, lines)
        self.assertTrue(cmd.endswith("--archive /archives/batcave.tar.gz --accept-loss"), cmd)
        self.assertIn(
            "--into /data/.mediacore-checkpoints/scratch/rollback-restore-levels",
            guard.commands(o)["into"],
        )

    def test_archive_mismatch_refuses(self):
        o = opts(archive="/archives/batcave.tar.gz")
        get = table(into_clean=R(), into=R(), into_levels=R("books 1\ncore 1\n"))
        code, lines, _ = guard.decide_restore(o, get)
        self.assertEqual(code, 1)
        self.assertIn("records no version", "\n".join(lines))

    def test_archive_extraction_failure_refuses(self):
        o = opts(archive="/archives/batcave.tar.gz")
        get = table(into_clean=R(), into=R("", "backup: extract store: not a tar", 1))
        code, _, _ = guard.decide_restore(o, get)
        self.assertEqual(code, 1)


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
    def test_unsafe_paths_are_refused(self):
        for bad in ("/data/my db", "/data/$(id)", "relative/db", "/data/a;b", "/data/'x'"):
            with self.subTest(bad):
                self.assertEqual(
                    guard.main(
                        ["rollback", "--host", "h", "--db", bad, "--bin", "/b", "--state-dir", "/s"]
                    ),
                    2,
                )

    def test_unsafe_env_is_refused(self):
        self.assertEqual(
            guard.main(
                [
                    "restore",
                    "--host",
                    "h",
                    "--db",
                    "/d",
                    "--bin",
                    "/b",
                    "--state-dir",
                    "/s",
                    "--offline-env",
                    "HOME=/x;id",
                ]
            ),
            2,
        )

    def test_parse_vector_and_sidecar(self):
        self.assertEqual(guard.parse_vector('{"core":0,"books":2}'), {"core": 0, "books": 2})
        self.assertEqual(guard.parse_sidecar("books 2\ncore 0\n")[0], {"books": 2, "core": 0})


if __name__ == "__main__":
    unittest.main()
