# file: scripts/tests/test_install_helpers.py
# version: 0.1.0
# guid: 41b6ab2f-6293-451a-89e1-86b8c2c9da4b
# last-edited: 2026-11-20

"""Tests for deploy/rollback-helpers/install_helpers.py (`make install-rollback-helpers`).

Run with:
    python3 -m unittest scripts.tests.test_install_helpers

Everything is installed under a temporary --root. `visudo` is a script this
file writes: it records the file it was asked to check, whether the live
drop-in existed at that moment, and answers as the test says.
"""

from __future__ import annotations

import importlib.util
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

SCRIPTS = Path(__file__).resolve().parents[1]
REPO = SCRIPTS.parent
HELPERS = REPO / "deploy" / "rollback-helpers"
TOOL = HELPERS / "install_helpers.py"
sys.path.insert(0, str(SCRIPTS))
import storage_format_guard as guard  # noqa: E402


def load(name: str, path: Path) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


RH = load("rollback_helper_under_test", HELPERS / "rollback_helper.py")

FAKE_VISUDO = """#!/bin/sh
# argv: -c -f FILE
echo "$3" >> "$VISUDO_LOG"
if [ -e "$LIVE_DROPIN" ]; then echo "live-existed" >> "$VISUDO_LOG"; fi
cp "$3" "$VISUDO_COPY"
if [ -n "$VISUDO_FAIL" ]; then echo "visudo: syntax error near line 3" >&2; exit 1; fi
exit 0
"""

ARGS = [
    "--bin=/usr/local/bin/audiobook-organizer",
    "--db=/path/to/audiobooks.pebble",
    "--deploy-user=alfred",
    "--service-user=audiobook",
    "--state-dir=/var/lib/audiobook-organizer",
    "--archive-dir=/var/backups/audiobook-organizer",
    "--stage-bin=/home/alfred/audiobook-organizer",
    "--stage-local-conf=/home/alfred/audiobook-organizer-local.conf",
]


class InstallHelpers(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name).resolve()
        self.root = self.tmp / "root"
        self.root.mkdir()
        visudo = self.tmp / "visudo"
        visudo.write_text(FAKE_VISUDO)
        visudo.chmod(0o755)
        self.visudo = visudo
        self.log = self.tmp / "visudo.log"
        self.copy = self.tmp / "visudo.copy"
        self.libdir = self.root / RH.LIBDIR.lstrip("/")
        self.conf = self.root / RH.CONFIG.lstrip("/")
        self.dropin = self.root / "etc" / "sudoers.d" / "audiobook-organizer-rollback"

    def run_tool(self, *extra: str, fail: bool = False, args=None) -> subprocess.CompletedProcess:
        env = {
            **os.environ,
            "VISUDO_LOG": str(self.log),
            "VISUDO_COPY": str(self.copy),
            "LIVE_DROPIN": str(self.dropin),
        }
        if fail:
            env["VISUDO_FAIL"] = "1"
        return subprocess.run(
            [
                sys.executable,
                str(TOOL),
                *(ARGS if args is None else args),
                f"--root={self.root}",
                f"--visudo={self.visudo}",
                *extra,
            ],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def tree(self) -> list[str]:
        return sorted(str(p.relative_to(self.root)) for p in self.root.rglob("*"))

    # -- the dry run ---------------------------------------------------------

    def test_without_apply_it_shows_the_plan_and_changes_nothing(self) -> None:
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.tree(), [], "a dry run writes nothing")
        self.assertFalse(self.log.exists(), "visudo is not even run")
        self.assertIn("dry run", p.stdout)
        self.assertIn("create", p.stdout)
        for stub in RH.STUBS:
            self.assertIn(f"{RH.LIBDIR}/{stub}", p.stdout.replace(str(self.root), ""))
        self.assertIn("sudoers drop-in", p.stdout)
        self.assertIn("APPLY=1", p.stdout, "says how to apply")

    # -- apply ---------------------------------------------------------------

    def test_apply_installs_the_bundle_conf_and_sudoers(self) -> None:
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(
            (self.libdir / "rollback_helper.py").read_bytes(),
            (HELPERS / "rollback_helper.py").read_bytes(),
        )
        for stub in RH.STUBS:
            f = self.libdir / stub
            self.assertEqual(f.read_bytes(), (HELPERS / stub).read_bytes(), stub)
            self.assertEqual(stat.S_IMODE(f.stat().st_mode), 0o755, stub)
        unit = self.libdir / RH.UNIT_TEMPLATE
        self.assertEqual(unit.read_bytes(), (REPO / "deploy" / RH.UNIT_TEMPLATE).read_bytes())
        self.assertEqual(stat.S_IMODE(unit.stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.conf.stat().st_mode), 0o644)
        text = self.conf.read_text()
        for line in (
            "bin = /usr/local/bin/audiobook-organizer",
            "db = /path/to/audiobooks.pebble",
            "service_user = audiobook",
            "stage_bin = /home/alfred/audiobook-organizer",
            "offline_env = HOME=/var/lib/audiobook-organizer ABK_STATE_DIR=/var/lib/audiobook-organizer",
        ):
            self.assertIn(line, text)
        self.assertEqual(stat.S_IMODE(self.dropin.stat().st_mode), 0o440)

    def test_the_conf_it_writes_is_one_the_helper_accepts(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        old = RH.CONFIG
        RH.CONFIG = str(self.conf)
        self.addCleanup(setattr, RH, "CONFIG", old)
        # load_config wants a root-owned file and directory; the test user owns
        # them, which the helper allows for the user running it.
        cfg = RH.load_config()
        self.assertEqual(cfg["bin"], "/usr/local/bin/audiobook-organizer")
        self.assertEqual(cfg["service_user"], "audiobook")
        self.assertEqual(cfg["stage_local_conf"], "/home/alfred/audiobook-organizer-local.conf")

    def test_the_sudoers_is_the_guards_own_rendering(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        o = guard.Opts(
            host="localhost",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user="audiobook",
            stage_bin="/home/alfred/audiobook-organizer",
        )
        self.assertEqual(self.dropin.read_text(), guard.sudoers(o, "alfred"))
        self.assertNotIn("*", self.dropin.read_text())

    # -- visudo gate ---------------------------------------------------------

    def test_visudo_checks_a_temporary_file_before_the_live_one_exists(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        checked = self.log.read_text().splitlines()
        self.assertEqual(len(checked), 1, checked)
        self.assertNotEqual(checked[0], str(self.dropin), "not the live file")
        self.assertTrue(Path(checked[0]).name.startswith("."), "a name sudo's includedir ignores")
        self.assertEqual(
            self.copy.read_text(), self.dropin.read_text(), "what was checked is what was moved"
        )
        self.assertFalse(Path(checked[0]).exists(), "the temporary file is gone")

    def test_a_drop_in_visudo_refuses_installs_nothing_at_all(self) -> None:
        p = self.run_tool("--apply", fail=True)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("visudo refused", p.stdout + p.stderr)
        self.assertIn("NOT installed", p.stdout + p.stderr)
        self.assertFalse(self.dropin.exists())
        self.assertFalse(self.libdir.exists(), "no bundle either: validated before any write")
        self.assertFalse(self.conf.exists())
        leftovers = [p for p in self.root.rglob(".*") if p.is_file()]
        self.assertEqual(leftovers, [], "the temporary file is deleted")

    def test_a_missing_visudo_installs_nothing(self) -> None:
        p = subprocess.run(
            [
                sys.executable,
                str(TOOL),
                *ARGS,
                f"--root={self.root}",
                f"--visudo={self.tmp / 'no-such-visudo'}",
                "--apply",
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual([x for x in self.root.rglob("*") if x.is_file()], [])

    def test_a_tight_umask_does_not_close_the_helpers_directory(self) -> None:
        old = os.umask(0o077)
        self.addCleanup(os.umask, old)
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        for d in (self.libdir, self.conf.parent, self.libdir.parent):
            self.assertEqual(stat.S_IMODE(d.stat().st_mode), 0o755, d)
        self.assertEqual(stat.S_IMODE((self.libdir / "install-staged").stat().st_mode), 0o755)

    # -- idempotent ------------------------------------------------------------

    def test_a_second_run_changes_nothing(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        before = {
            p: (p.read_bytes(), p.stat().st_mtime_ns) for p in self.root.rglob("*") if p.is_file()
        }
        self.log.unlink()
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("Nothing to do", p.stdout)
        after = {
            p: (p.read_bytes(), p.stat().st_mtime_ns) for p in self.root.rglob("*") if p.is_file()
        }
        self.assertEqual(after, before, "no file was rewritten")
        self.assertFalse(self.log.exists(), "nothing changed, so visudo was not asked")

    def test_a_changed_helper_is_replaced_and_only_that(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        h = self.libdir / "rollback_helper.py"
        h.write_text("# an older helper\n")
        h.chmod(0o755)
        conf_mtime = self.conf.stat().st_mtime_ns
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(h.read_bytes(), (HELPERS / "rollback_helper.py").read_bytes())
        self.assertEqual(self.conf.stat().st_mtime_ns, conf_mtime)
        self.assertIn("replace", p.stdout)

    def test_a_wrong_mode_is_corrected(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        stub = self.libdir / "install-staged"
        stub.chmod(0o777)
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.assertEqual(stat.S_IMODE(stub.stat().st_mode), 0o755)

    # -- rollback.conf is the owner's ------------------------------------------

    def test_an_edited_conf_is_not_overwritten_without_replace_conf(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.write_text(self.conf.read_text() + "\n# the owner's note\n")
        edited = self.conf.read_text()
        p = self.run_tool("--apply")
        self.assertNotEqual(p.returncode, 0, "says it left something undone")
        self.assertEqual(self.conf.read_text(), edited)
        self.assertIn("--replace-conf", p.stdout)
        self.assertIn("the owner's note", p.stdout, "shows the difference")

    def test_replace_conf_overwrites_it(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.write_text("bin = /somewhere/else\n")
        p = self.run_tool("--apply", "--replace-conf")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("bin = /usr/local/bin/audiobook-organizer", self.conf.read_text())

    def test_the_rest_is_still_installed_when_the_conf_is_left_alone(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.write_text("bin = /somewhere/else\n")
        (self.libdir / "pprof").write_text("old\n")
        self.run_tool("--apply")
        self.assertEqual((self.libdir / "pprof").read_bytes(), (HELPERS / "pprof").read_bytes())
        self.assertEqual(self.conf.read_text(), "bin = /somewhere/else\n")

    # -- bad input ---------------------------------------------------------------

    def test_a_missing_required_value_names_it_and_writes_nothing(self) -> None:
        args = [a for a in ARGS if not a.startswith("--archive-dir")]
        p = self.run_tool("--apply", args=args)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("archive_dir", p.stdout + p.stderr)
        self.assertEqual(self.tree(), [])

    def test_a_bad_deploy_user_is_refused(self) -> None:
        args = [a for a in ARGS if not a.startswith("--deploy-user")] + ["--deploy-user=a b; id"]
        p = self.run_tool("--apply", args=args)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.tree(), [])

    def test_a_bad_db_path_is_refused_by_the_guards_validation(self) -> None:
        args = [a for a in ARGS if not a.startswith("--db")] + ["--db=/tmp/x y"]
        p = self.run_tool("--apply", args=args)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.tree(), [])

    def test_the_helper_and_the_guard_agree_on_the_directory(self) -> None:
        self.assertEqual(RH.LIBDIR, guard.HELPER_DIR)

    def test_every_stub_in_the_bundle_is_installed(self) -> None:
        on_disk = {
            p.name
            for p in HELPERS.iterdir()
            if p.is_file() and p.suffix == "" and p.name != "rollback.conf.example"
        }
        self.assertEqual(on_disk, set(RH.STUBS), "a stub added to the directory but not installed")


if __name__ == "__main__":
    unittest.main()
