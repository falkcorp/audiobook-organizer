#!/usr/bin/env python3
# file: scripts/test_apt_install.py
# version: 1.0.0
# guid: 0d6f3a85-c21e-47b9-a5d4-7e8b1c92f063
# last-edited: 2026-11-18

"""Tests for scripts/ci/apt_watchdog.py and scripts/ci/apt_install.sh.

The shell script runs against shim ``sudo``, ``timeout`` and ``apt-get``
binaries on PATH and a throwaway apt sources file, so mirror rotation,
partial-download retention and the throughput floor are exercised without
apt, root or a network. Stdlib ``unittest`` like the rest of scripts/.
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

CI = pathlib.Path(__file__).resolve().parent / "ci"
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
            host=$(grep -o 'http://[^/]*archive.ubuntu.com' "$APT_SOURCES_FILES" | sed 's#http://##')
            verb=update
            for a in "$@"; do case "$a" in install) verb=install;; --download-only) verb=download;; esac; done
            echo "$verb $host" >> "{self.log}"
            if [ "$verb" = download ]; then
              if [ -f "{self.archives}/partial/pkg.deb" ]; then echo "resumed $host" >> "{self.log}"; fi
              printf partial >> "{self.archives}/partial/pkg.deb"
              case " $FAKE_SLOW_HOSTS " in *" $host "*) sleep "${{FAKE_SLEEP:-20}}";; esac
            fi
            exit 0
            """,
        )

    def run_script(self, slow_hosts="slow.archive.ubuntu.com", mirrors="fast.archive.ubuntu.com", attempts=3,
                   sleep="20"):
        env = dict(
            os.environ,
            PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            APT_SOURCES_FILES=str(self.src),
            APT_WATCHDOG_DIR=str(self.archives),
            APT_WATCHDOG_ARGS="--window 0.6 --grace 0.3 --poll 0.1",
            APT_INSTALL_BACKOFF="0",
            APT_INSTALL_ATTEMPTS=str(attempts),
            APT_INSTALL_MIN_KIBPS="1",
            APT_MIRRORS=mirrors,
            FAKE_SLOW_HOSTS=slow_hosts,
            FAKE_SLEEP=sleep,
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

    def test_usage_error(self):
        r = subprocess.run(["bash", str(CI / "apt_install.sh")], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
