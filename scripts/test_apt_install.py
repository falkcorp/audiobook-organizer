#!/usr/bin/env python3
# file: scripts/test_apt_install.py
# version: 1.1.0
# guid: 0d6f3a85-c21e-47b9-a5d4-7e8b1c92f063
# last-edited: 2026-11-18

"""Tests for scripts/ci/apt_watchdog.py and scripts/ci/apt_install.sh.

The shell script runs against shim ``sudo``, ``timeout`` and ``apt-get``
binaries on PATH and a throwaway apt sources file, so mirror rotation,
partial-download retention and the throughput floor are exercised without
apt, root or a network. Stdlib ``unittest`` like the rest of scripts/.

The mirror rewrite is also run against byte-for-byte copies of the files on the
hosted ubuntu-26.04 runner image (scripts/testdata/apt/, captured from a CI
log on 2026-11-18): a deb822 ubuntu.sources whose archive stanza is
``URIs: mirror+file:/etc/apt/apt-mirrors.txt``. PR #4729 shipped a rewrite that
only knew ``http://...archive.ubuntu.com`` and passed shim tests that used such
a URI, then logged "no archive.ubuntu.com source" on the real runner and never
rotated; that is why the fixtures here are real and not invented.

Shim tests cannot show that apt writes where the watchdog looks or that
Dl-Limit takes effect; the proof for both is a real CI run (see the PR).
"""

from __future__ import annotations

import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from contextlib import redirect_stderr
from io import StringIO
from unittest import mock

CI = pathlib.Path(__file__).resolve().parent / "ci"
TESTDATA = pathlib.Path(__file__).resolve().parent / "testdata" / "apt"
sys.path.insert(0, str(CI))

import apt_watchdog as wd  # noqa: E402

SED = shutil.which("gsed") or shutil.which("sed")


class TooSlowTest(unittest.TestCase):
    def test_inside_grace_never_slow(self):
        self.assertFalse(wd.too_slow([(0, 0), (5, 0)], 5, 0, 1000, 10, 20))

    def test_needs_a_full_window_of_history(self):
        self.assertFalse(wd.too_slow([(20, 0), (25, 0)], 25, 0, 1000, 10, 20))

    def test_flat_is_slow(self):
        self.assertTrue(wd.too_slow([(20, 5), (30, 5), (31, 5)], 31, 0, 1000, 10, 20))

    def test_at_floor_is_not_slow_below_is(self):
        fast = [(20, 0), (30, 10_000), (31, 10_000)]  # 10000 B over 11 s ~ 909 B/s
        self.assertTrue(wd.too_slow(fast, 31, 0, 1000, 10, 20))
        self.assertFalse(wd.too_slow(fast, 31, 0, 900, 10, 20))

    def test_no_samples(self):
        self.assertFalse(wd.too_slow([], 100, 0, 1000, 10, 20))


class WatchdogRunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def run_wd(self, cmd, floor_kib=1.0, **kw):
        return wd.run(cmd, kw.get("dir", self.tmp), floor_kib, 0.6, 0.3, 0.1)

    def test_command_status_passes_through(self):
        self.assertEqual(self.run_wd(["sh", "-c", "exit 7"]), 7)
        self.assertEqual(self.run_wd(["true"]), 0)

    def test_stalled_directory_is_killed_with_124(self):
        t = time.monotonic()
        self.assertEqual(self.run_wd(["sleep", "30"]), wd.SLOW_EXIT)
        self.assertLess(time.monotonic() - t, 10)

    def test_trickle_below_floor_is_killed(self):
        script = f"while :; do printf x >> {self.tmp}/f; sleep 0.1; done"
        self.assertEqual(self.run_wd(["sh", "-c", script], floor_kib=50.0), wd.SLOW_EXIT)

    def test_fast_writer_is_left_alone(self):
        script = f"for i in $(seq 1 15); do head -c 200000 /dev/zero >> {self.tmp}/f; sleep 0.1; done"
        self.assertEqual(self.run_wd(["sh", "-c", script], floor_kib=100.0), 0)

    def test_unreadable_directory_is_not_a_kill(self):
        missing = os.path.join(self.tmp, "nope")
        self.assertIsNone(wd.dir_bytes(missing))
        self.assertEqual(self.run_wd(["sh", "-c", "sleep 1.5"], dir=missing), 0)

    def test_dir_bytes_counts_nested_files(self):
        os.makedirs(os.path.join(self.tmp, "partial"))
        pathlib.Path(self.tmp, "a").write_bytes(b"x" * 10)
        pathlib.Path(self.tmp, "partial", "b").write_bytes(b"x" * 5)
        self.assertEqual(wd.dir_bytes(self.tmp), 15)


class AptLayoutTest(unittest.TestCase):
    """The watchdog against the directory layout apt really produces.

    ``apt-get -o Dir::Cache::archives=D install --download-only`` writes each
    .deb to D/partial/<name> and renames it to D/<name> when complete, with an
    empty D/lock; observed on a hosted runner (a 67 MB set: bytes under
    D/partial first, then in D).
    """

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        os.makedirs(os.path.join(self.tmp, "partial"))
        pathlib.Path(self.tmp, "lock").write_bytes(b"")

    def apt_writer(self, files, chunk, pause):
        """A shell loop that fetches ``files`` .deb files the way apt does."""
        return (
            f"d={self.tmp}; for n in $(seq 1 {files}); do "
            f"for c in $(seq 1 10); do head -c {chunk} /dev/zero >> $d/partial/pkg$n.deb; sleep {pause}; done; "
            f"mv $d/partial/pkg$n.deb $d/pkg$n.deb; done"
        )

    def run_wd(self, script, floor_kib):
        err = StringIO()
        with redirect_stderr(err):
            rc = wd.run(["sh", "-c", script], self.tmp, floor_kib, 0.6, 0.3, 0.1, report=0.3)
        return rc, err.getvalue()

    def test_healthy_apt_layout_is_not_killed_and_timing_is_logged(self):
        # 3 packages x 10 chunks x 100 kB over ~3 s = ~1 MB/s, floor 100 KiB/s.
        rc, err = self.run_wd(self.apt_writer(3, 100_000, 0.1), floor_kib=100.0)
        self.assertEqual(rc, 0, err)
        self.assertNotIn("killing", err)
        self.assertIn("apt_watchdog: command exited 0", err)
        self.assertRegex(err, r"holds 2\.\d MiB after \d+s \(average \d+ KiB/s, floor 100 KiB/s\)")
        self.assertRegex(err, r"MiB so far, \d+ KiB/s over the last 0\.6s")

    def test_trickle_in_partial_is_killed_and_the_rate_is_logged(self):
        # Growth only ever happens under partial/: the watchdog must still see it
        # (a top-level-only measure sees zero here), and 1 kB/0.1 s is far under
        # a 100 KiB/s floor.
        script = f"while :; do head -c 1000 /dev/zero >> {self.tmp}/partial/x.deb; sleep 0.1; done"
        rc, err = self.run_wd(script, floor_kib=100.0)
        self.assertEqual(rc, wd.SLOW_EXIT, err)
        self.assertRegex(err, r"grew at \d+ KiB/s, under the 100 KiB/s floor")

    def test_growth_only_in_partial_counts_as_progress(self):
        # 200 kB / 0.1 s = ~2 MB/s, all of it in partial/, never renamed.
        script = f"for i in $(seq 1 15); do head -c 200000 /dev/zero >> {self.tmp}/partial/x.deb; sleep 0.1; done"
        rc, err = self.run_wd(script, floor_kib=500.0)
        self.assertEqual(rc, 0, err)
        self.assertNotIn("killing", err)

    def test_rename_out_of_partial_is_not_a_drop(self):
        meter = wd.Meter(self.tmp)
        pathlib.Path(self.tmp, "partial", "a.deb").write_bytes(b"x" * 100)
        self.assertEqual(meter.read(), 100)
        # the scan missed a.deb mid-rename: raw size dips, the meter does not
        with mock.patch.object(wd, "dir_bytes", return_value=40):
            self.assertEqual(meter.read(), 100)
        os.rename(os.path.join(self.tmp, "partial", "a.deb"), os.path.join(self.tmp, "a.deb"))
        self.assertEqual(meter.read(), 100)
        pathlib.Path(self.tmp, "partial", "b.deb").write_bytes(b"x" * 30)
        self.assertEqual(meter.read(), 130)

    def test_partial_is_scanned_before_the_directory(self):
        with mock.patch.object(wd.os, "walk", wraps=os.walk) as walk:
            wd.dir_bytes(self.tmp)
        self.assertIs(walk.call_args.kwargs.get("topdown"), False)

    def test_unreadable_directory_leaves_the_meter_alone(self):
        meter = wd.Meter(os.path.join(self.tmp, "missing"))
        self.assertIsNone(meter.read())
        self.assertEqual(meter.high, 0)

    def test_window_rate(self):
        self.assertIsNone(wd.window_rate([], 10, 5))
        self.assertIsNone(wd.window_rate([(8, 0), (10, 100)], 10, 5))  # under a window of history
        self.assertEqual(wd.window_rate([(0, 0), (5, 500), (10, 1500)], 10, 5), 200.0)


def _exe(path: pathlib.Path, body: str) -> None:
    path.write_text("#!/usr/bin/env bash\n" + textwrap.dedent(body))
    path.chmod(path.stat().st_mode | stat.S_IEXEC)


class AptInstallTest(unittest.TestCase):
    """apt_install.sh with fake sudo/timeout/apt-get.

    The fake apt-get logs ``<verb> <host>`` per call. Its download-only call
    writes a partial file and then behaves per FAKE_SLOW_HOSTS: on a slow host
    it sleeps without writing (a stalled mirror), otherwise it finishes.
    """

    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        self.archives = self.tmp / "archives"
        (self.archives / "partial").mkdir(parents=True)
        self.src = self.tmp / "ubuntu.sources"
        self.src.write_text(
            "Types: deb\nURIs: http://slow.archive.ubuntu.com/ubuntu/\nSuites: noble\n\n"
            "Types: deb\nURIs: http://security.ubuntu.com/ubuntu/\nSuites: noble-security\n"
        )
        self.log = self.tmp / "calls.log"
        _exe(self.bin / "sudo", 'exec "$@"\n')
        _exe(self.bin / "sed", f'exec "{SED}" "$@"\n')
        _exe(
            self.bin / "timeout",
            """
            [ "$1" = "-k" ] && shift 2
            shift
            exec "$@"
            """,
        )
        _exe(
            self.bin / "apt-get",
            f"""
            uri=$(grep -h '^URIs:' $APT_SOURCES_FILES | head -1 | awk '{{print $2}}')
            case "$uri" in
              mirror+file:*) host=$(head -1 "$FAKE_MIRRORS_FILE" | sed -E 's#https?://([^/]*)/.*#\\1#') ;;
              *) host=$(echo "$uri" | sed -E 's#https?://([^/]*)/.*#\\1#') ;;
            esac
            verb=update
            for a in "$@"; do case "$a" in install) verb=install;; --download-only) verb=download;; esac; done
            echo "$verb $host" >> "{self.log}"
            case " $* " in *Dl-Limit=*) echo "limit $host" >> "{self.log}";; esac
            if [ "$verb" = download ]; then
              if [ -f "{self.archives}/partial/pkg.deb" ]; then echo "resumed $host" >> "{self.log}"; fi
              printf partial >> "{self.archives}/partial/pkg.deb"
              case " $FAKE_SLOW_HOSTS " in *" $host "*) sleep "${{FAKE_SLEEP:-20}}";; esac
            fi
            exit 0
            """,
        )

    def run_script(self, slow_hosts="slow.archive.ubuntu.com", mirrors="fast.archive.ubuntu.com", attempts=3,
                   sleep="20", sources=None, **extra_env):
        env = dict(
            os.environ,
            PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            APT_SOURCES_FILES=" ".join(str(p) for p in (sources or [self.src])),
            FAKE_MIRRORS_FILE=str(TESTDATA / "apt-mirrors.txt"),
            APT_WATCHDOG_DIR=str(self.archives),
            APT_WATCHDOG_ARGS="--window 0.6 --grace 0.3 --poll 0.1",
            APT_INSTALL_BACKOFF="0",
            APT_INSTALL_ATTEMPTS=str(attempts),
            APT_INSTALL_MIN_KIBPS="1",
            APT_MIRRORS=mirrors,
            FAKE_SLOW_HOSTS=slow_hosts,
            FAKE_SLEEP=sleep,
            **extra_env,
        )
        return subprocess.run(
            ["bash", str(CI / "apt_install.sh"), "pkg"],
            env=env, capture_output=True, text=True, timeout=60,
        )

    def calls(self):
        return self.log.read_text().splitlines()

    def test_healthy_mirror_installs_first_try_and_leaves_sources(self):
        before = self.src.read_text()
        r = self.run_script(slow_hosts="")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.calls(), ["update slow.archive.ubuntu.com",
                                        "download slow.archive.ubuntu.com",
                                        "install slow.archive.ubuntu.com"])
        self.assertEqual(self.src.read_text(), before)

    def test_slow_mirror_is_killed_then_rotated_and_partial_resumed(self):
        t = time.monotonic()
        r = self.run_script()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertLess(time.monotonic() - t, 15, "the floor, not the 20 s sleep, ended attempt 1")
        self.assertIn("attempt 1/3 failed", r.stderr)
        calls = self.calls()
        self.assertEqual(calls[:2], ["update slow.archive.ubuntu.com", "download slow.archive.ubuntu.com"])
        self.assertEqual(calls[2:], ["update fast.archive.ubuntu.com", "download fast.archive.ubuntu.com",
                                     "resumed fast.archive.ubuntu.com", "install fast.archive.ubuntu.com"])
        text = self.src.read_text()
        self.assertIn("http://fast.archive.ubuntu.com/ubuntu/", text)
        self.assertIn("http://security.ubuntu.com/ubuntu/", text, "security host must not be rewritten")
        self.assertEqual((self.archives / "partial" / "pkg.deb").read_text(), "partialpartial")

    def test_mirrors_rotate_in_order_and_wrap(self):
        r = self.run_script(slow_hosts="slow.archive.ubuntu.com m1.archive.ubuntu.com m2.archive.ubuntu.com",
                            mirrors="m1.archive.ubuntu.com m2.archive.ubuntu.com", attempts=4, sleep="2")
        self.assertEqual(r.returncode, 0, r.stderr)
        hosts = [c.split()[1] for c in self.calls() if c.startswith("download")]
        self.assertEqual(hosts, ["slow.archive.ubuntu.com", "m1.archive.ubuntu.com", "m2.archive.ubuntu.com", "m1.archive.ubuntu.com"])

    def test_last_attempt_has_no_floor(self):
        # One attempt = the last attempt: a 3 s no-growth download passes (the
        # floor would have killed it at ~1 s), because only the cap applies.
        _exe(self.bin / "apt-get", self.calls_script_sleep(3))
        r = self.run_script(slow_hosts="", attempts=1)
        self.assertEqual(r.returncode, 0, r.stderr)

    def calls_script_sleep(self, secs):
        return f"""
        for a in "$@"; do [ "$a" = --download-only ] && sleep {secs}; done
        exit 0
        """

    def test_all_attempts_fail_exits_1(self):
        _exe(self.bin / "apt-get", "exit 1\n")
        r = self.run_script(slow_hosts="")
        self.assertEqual(r.returncode, 1)
        self.assertIn("all 3 attempts failed", r.stderr)

    def test_no_archive_source_keeps_configured_mirror(self):
        self.src.write_text("URIs: http://mirror.example.test/ubuntu/\n")
        _exe(self.bin / "apt-get", "exit 1\n")
        r = self.run_script(slow_hosts="")
        self.assertIn("keeping the configured mirror", r.stderr)
        self.assertEqual(self.src.read_text(), "URIs: http://mirror.example.test/ubuntu/\n")

    # --- the runner image's real files -------------------------------------

    def real_sources(self):
        """Copies of the real runner files (the rewrite edits them in place)."""
        out = self.tmp / "real"
        out.mkdir()
        for name in ("ubuntu.sources", "sources.list"):
            shutil.copy(TESTDATA / name, out / name)
        return out / "ubuntu.sources", out / "sources.list"

    def test_real_runner_ubuntu_sources_is_rotated(self):
        deb822, classic = self.real_sources()
        before = deb822.read_text()
        r = self.run_script(slow_hosts="us-east-2.ec2.archive.ubuntu.com",
                            sources=[classic, deb822])
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("keeping the configured mirror", r.stderr)
        self.assertIn("archive mirror is now fast.archive.ubuntu.com", r.stderr)
        hosts = [c.split()[1] for c in self.calls() if c.startswith("download")]
        self.assertEqual(hosts, ["us-east-2.ec2.archive.ubuntu.com", "fast.archive.ubuntu.com"])
        after = deb822.read_text()
        self.assertIn("URIs: http://fast.archive.ubuntu.com/ubuntu/\n", after)
        self.assertNotIn("mirror+file", after)
        # Only that one line changed: the security stanza, Suites, Components,
        # Signed-By and every comment are untouched.
        diff = [(a, b) for a, b in zip(before.splitlines(), after.splitlines()) if a != b]
        self.assertEqual(diff, [("URIs: mirror+file:/etc/apt/apt-mirrors.txt",
                                 "URIs: http://fast.archive.ubuntu.com/ubuntu/")])
        self.assertEqual(len(before.splitlines()), len(after.splitlines()))
        self.assertIn("URIs: http://security.ubuntu.com/ubuntu\n", after)

    def test_real_runner_files_rotate_through_the_list_and_wrap(self):
        deb822, classic = self.real_sources()
        r = self.run_script(
            slow_hosts="us-east-2.ec2.archive.ubuntu.com m1.archive.ubuntu.com m2.archive.ubuntu.com",
            mirrors="m1.archive.ubuntu.com m2.archive.ubuntu.com", attempts=4, sleep="2",
            sources=[classic, deb822])
        self.assertEqual(r.returncode, 0, r.stderr)
        hosts = [c.split()[1] for c in self.calls() if c.startswith("download")]
        self.assertEqual(hosts, ["us-east-2.ec2.archive.ubuntu.com", "m1.archive.ubuntu.com",
                                 "m2.archive.ubuntu.com", "m1.archive.ubuntu.com"])

    def test_real_runner_comment_only_sources_list_is_left_alone(self):
        deb822, classic = self.real_sources()
        before = classic.read_text()
        r = self.run_script(slow_hosts="us-east-2.ec2.archive.ubuntu.com", sources=[classic, deb822])
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(classic.read_text(), before)

    def test_real_mirrors_file_is_never_edited(self):
        # The rewrite changes the source, not the list file the image ships.
        mirrors = (TESTDATA / "apt-mirrors.txt").read_text()
        deb822, classic = self.real_sources()
        self.run_script(slow_hosts="us-east-2.ec2.archive.ubuntu.com", sources=[classic, deb822])
        self.assertEqual((TESTDATA / "apt-mirrors.txt").read_text(), mirrors)

    def test_classic_list_shapes_are_rewritten_and_comments_are_not(self):
        lst = self.tmp / "classic.list"
        lst.write_text(
            "# deb http://old.archive.ubuntu.com/ubuntu resolute main\n"
            "deb mirror+file:/etc/apt/apt-mirrors.txt resolute main\n"
            "deb [arch=amd64] https://us-east-2.ec2.archive.ubuntu.com/ubuntu resolute-updates main\n"
            "deb mirror://mirrors.ubuntu.com/mirrors.txt resolute-backports main\n"
            "deb http://security.ubuntu.com/ubuntu resolute-security main\n"
            "deb http://ports.ubuntu.com/ubuntu-ports resolute main\n"
        )
        _exe(self.bin / "apt-get", "exit 1\n")
        r = self.run_script(slow_hosts="", attempts=2, sources=[lst])
        self.assertEqual(r.returncode, 1)
        self.assertEqual(
            lst.read_text(),
            "# deb http://old.archive.ubuntu.com/ubuntu resolute main\n"
            "deb http://fast.archive.ubuntu.com/ubuntu/ resolute main\n"
            "deb [arch=amd64] https://fast.archive.ubuntu.com/ubuntu resolute-updates main\n"
            "deb http://fast.archive.ubuntu.com/ubuntu/ resolute-backports main\n"
            "deb http://security.ubuntu.com/ubuntu resolute-security main\n"
            "deb http://ports.ubuntu.com/ubuntu-ports resolute main\n",
        )

    def test_rotating_to_the_host_already_in_use_still_counts_as_rotated(self):
        # archive.ubuntu.com -> archive.ubuntu.com must not log "no source".
        self.src.write_text("URIs: http://archive.ubuntu.com/ubuntu/\n")
        _exe(self.bin / "apt-get", "exit 1\n")
        r = self.run_script(slow_hosts="", mirrors="archive.ubuntu.com", attempts=2)
        self.assertNotIn("keeping the configured mirror", r.stderr)
        self.assertIn("archive mirror is now archive.ubuntu.com", r.stderr)

    # --- the CI-proof throttle hook ----------------------------------------

    def test_dl_limit_hook_applies_to_attempt_one_only(self):
        r = self.run_script(slow_hosts="slow.archive.ubuntu.com", APT_INSTALL_TEST_DL_LIMIT_KIB="50")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("TEST HOOK: attempt 1 download limited to 50 KiB/s", r.stderr)
        limited = [c for c in self.calls() if c.startswith("limit")]
        self.assertEqual(limited, ["limit slow.archive.ubuntu.com"])

    def test_no_dl_limit_without_the_hook(self):
        r = self.run_script(slow_hosts="")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual([c for c in self.calls() if c.startswith("limit")], [])
        self.assertNotIn("TEST HOOK", r.stderr)

    def test_usage_error(self):
        r = subprocess.run(["bash", str(CI / "apt_install.sh")], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
