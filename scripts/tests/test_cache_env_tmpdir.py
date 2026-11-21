# file: scripts/tests/test_cache_env_tmpdir.py
# version: 1.0.0
# guid: 3c8e1f5a-7b24-4d96-a0e3-92d1c6b4f875
# last-edited: 2026-11-21
"""scripts/ci/cache_env.sh puts GOTMPDIR and TMPDIR on disk under the CI cache root.

Pipeline 4897 (test-native-taglib on the u0-local agent) died at the link of
internal/tagger.test with "final link failed: No space left on device": the Go
linker and `go build` write to TMPDIR/GOTMPDIR, and /tmp on that host is a RAM-backed
tmpfs. The script is sourced by bash with CI_CACHE_ROOT_CANDIDATES pointing at a
temp root, so nothing outside the temp dir is touched.
"""

import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / "scripts" / "ci" / "cache_env.sh"


def source(root, extra_env=None):
    env = {
        "PATH": os.environ["PATH"],
        "CI_CACHE_ROOT_CANDIDATES": root,
        "CI_PIPELINE_NUMBER": "4897",
        "CI_WORKFLOW_NAME": "test-native-taglib",
    }
    env.update(extra_env or {})
    return subprocess.run(
        ["bash", "-c", f'. "{SCRIPT}" || exit 1; echo "$GOTMPDIR|$TMPDIR|$CI_RUN_TMPDIR"'],
        env=env,
        capture_output=True,
        text=True,
        cwd=REPO,
    )


class CacheEnvTmpdirTest(unittest.TestCase):
    def test_exports_on_disk_tmpdir_and_creates_it(self):
        with tempfile.TemporaryDirectory() as root:
            r = source(root)
            self.assertEqual(r.returncode, 0, r.stderr)
            gotmp, tmp, run = r.stdout.strip().splitlines()[-1].split("|")
            self.assertEqual(gotmp, tmp)
            self.assertEqual(tmp, run)
            self.assertEqual(run, os.path.join(root, "tmp", "4897-test-native-taglib"))
            self.assertTrue(os.path.isdir(run))

    def test_sweeps_only_stale_dirs(self):
        with tempfile.TemporaryDirectory() as root:
            tmp = Path(root, "tmp")
            old, fresh = tmp / "1-old", tmp / "2-fresh"
            for d in (old, fresh):
                (d / "go-link-1").mkdir(parents=True)
            seven_hours_ago = time.time() - 7 * 3600
            os.utime(old, (seven_hours_ago, seven_hours_ago))
            r = source(root)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertFalse(old.exists())
            self.assertTrue(fresh.exists())

    def test_no_cache_root_leaves_tmpdir_unchanged(self):
        with tempfile.TemporaryDirectory() as root:
            r = source(os.path.join(root, "missing"), {"TMPDIR": "/somewhere"})
            self.assertNotEqual(r.returncode, 0)
            self.assertNotIn("CI cache root", r.stdout)
            r = subprocess.run(
                ["bash", "-c", f'. "{SCRIPT}"; echo "${{GOTMPDIR-unset}}|$TMPDIR"'],
                env={
                    "PATH": os.environ["PATH"],
                    "CI_CACHE_ROOT_CANDIDATES": os.path.join(root, "missing"),
                    "TMPDIR": "/somewhere",
                },
                capture_output=True,
                text=True,
            )
            self.assertEqual(r.stdout.strip(), "unset|/somewhere")


if __name__ == "__main__":
    unittest.main()
