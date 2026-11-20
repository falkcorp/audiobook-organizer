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

import contextlib
import importlib.util
import io
import os
import pwd
import stat
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
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
IH = load("install_helpers_under_test", TOOL)

FAKE_VISUDO = """#!/bin/sh
# argv: -c -f FILE (one file), or -c alone (the whole configuration)
if [ "$2" != "-f" ]; then
  echo "whole" >> "$VISUDO_WHOLE_LOG"
  if [ -n "$VISUDO_WHOLE_FAIL" ]; then echo "visudo: unknown alias" >&2; exit 1; fi
  exit 0
fi
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

    def run_tool(
        self, *extra: str, fail: bool = False, whole_fail: bool = False, args=None
    ) -> subprocess.CompletedProcess:
        env = {
            **os.environ,
            "VISUDO_LOG": str(self.log),
            "VISUDO_COPY": str(self.copy),
            "LIVE_DROPIN": str(self.dropin),
            "VISUDO_WHOLE_LOG": str(self.tmp / "visudo.whole.log"),
        }
        if fail:
            env["VISUDO_FAIL"] = "1"
        if whole_fail:
            env["VISUDO_WHOLE_FAIL"] = "1"
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

    # -- what is already there is not trusted (r2 M-2) --------------------------

    def call(self, *extra: str) -> tuple[int | str, str]:
        """main() in this process, so the owner check can be steered."""
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            try:
                rc: int | str = IH.main(
                    [*ARGS, f"--root={self.root}", f"--visudo={self.visudo}", *extra]
                )
            except SystemExit as e:
                rc = str(e.code)
        return rc, out.getvalue()

    def stranger(self):
        """Every file and directory the test user owns now counts as owned by
        somebody else (the test cannot chown to a real second user)."""
        return unittest.mock.patch.object(IH, "trusted_uids", lambda: {os.geteuid() + 1})

    def step(self, path: Path, data: bytes, mode: int, kind: str = "file") -> Any:
        return IH.Step(str(path), data, mode, kind)

    def test_a_file_with_the_right_bytes_and_mode_but_the_wrong_owner_is_replaced(self) -> None:
        f = self.tmp / "helper"
        f.write_bytes(b"x")
        f.chmod(0o755)
        step = self.step(f, b"x", 0o755)
        self.assertEqual(IH.state(step), "unchanged" if False else IH.state(step))
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        self.assertEqual(IH.state(step), "unchanged")
        with self.stranger():
            self.assertEqual(IH.state(step), "fix")

    def test_a_group_writable_file_is_replaced(self) -> None:
        f = self.tmp / "helper"
        f.write_bytes(b"x")
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        for mode in (0o775, 0o757, 0o777):
            with self.subTest(mode=oct(mode)):
                f.chmod(mode)
                self.assertEqual(IH.state(self.step(f, b"x", 0o755)), "fix")

    def test_a_hard_linked_file_is_replaced(self) -> None:
        f = self.tmp / "helper"
        f.write_bytes(b"x")
        f.chmod(0o755)
        os.link(f, self.tmp / "elsewhere")
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        self.assertEqual(IH.state(self.step(f, b"x", 0o755)), "fix")

    def test_a_symlink_where_a_file_belongs_is_replaced_not_followed(self) -> None:
        target = self.tmp / "target"
        target.write_bytes(b"x")
        link = self.tmp / "helper"
        link.symlink_to(target)
        self.assertEqual(IH.state(self.step(link, b"x", 0o755)), "replace")

    def test_a_directory_the_wrong_owner_or_mode_is_fixed(self) -> None:
        d = self.tmp / "dir"
        d.mkdir()
        d.chmod(0o755)
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        step = self.step(d, b"", 0o755, "dir")
        self.assertEqual(IH.state(step), "unchanged")
        d.chmod(0o775)
        self.assertEqual(IH.state(step), "fix")
        d.chmod(0o755)
        with self.stranger():
            self.assertEqual(IH.state(step), "fix")

    def test_fixing_a_directory_chowns_it_to_root_and_sets_the_mode(self) -> None:
        d = self.tmp / "dir"
        d.mkdir()
        d.chmod(0o777)
        with unittest.mock.patch.object(IH.os, "chown") as chown:
            IH.fix_dir(str(d), 0o755, chown=True)
        chown.assert_called_once_with(str(d), 0, 0)
        self.assertEqual(stat.S_IMODE(d.stat().st_mode), 0o755)

    def test_a_symlinked_directory_is_refused(self) -> None:
        real = self.tmp / "real"
        real.mkdir()
        link = self.tmp / "dir"
        link.symlink_to(real)
        with self.assertRaises(SystemExit) as cm:
            IH.state(self.step(link, b"", 0o755, "dir"))
        self.assertIn(str(link), str(cm.exception))

    def test_apply_fixes_an_existing_group_writable_directory(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.parent.chmod(0o775)
        self.libdir.chmod(0o750)
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("fix", p.stdout)
        for d in (self.conf.parent, self.libdir):
            self.assertEqual(stat.S_IMODE(d.stat().st_mode), 0o755, d)

    def test_a_bad_parent_is_refused_naming_it_and_nothing_is_written(self) -> None:
        for rel in ("usr/local/libexec", "etc"):
            with self.subTest(parent=rel):
                self.clear_root()
                d = self.root / rel
                d.mkdir(parents=True)
                d.chmod(0o777)
                before = self.tree()
                p = self.run_tool("--apply")
                self.assertNotEqual(p.returncode, 0)
                self.assertIn(str(d), p.stdout + p.stderr)
                self.assertIn("parent of", p.stdout + p.stderr)
                self.assertEqual(self.tree(), before, "refused before any write")

    def clear_root(self) -> None:
        import shutil

        for child in list(self.root.iterdir()):
            with contextlib.suppress(OSError):
                child.chmod(0o755)
            shutil.rmtree(child, ignore_errors=True)

    def test_a_group_writable_parent_is_refused_even_in_a_dry_run(self) -> None:
        (self.root / "usr" / "local").mkdir(parents=True)
        (self.root / "usr" / "local").chmod(0o775)
        p = self.run_tool()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("usr/local", p.stdout + p.stderr)

    def test_the_sudoers_directory_itself_must_be_root_owned_and_closed(self) -> None:
        d = self.root / "etc" / "sudoers.d"
        d.mkdir(parents=True)
        d.chmod(0o777)
        p = self.run_tool("--apply")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sudoers.d", p.stdout + p.stderr)
        self.assertFalse(self.dropin.exists())

    def test_a_parent_owned_by_somebody_else_is_refused(self) -> None:
        (self.root / "usr" / "local").mkdir(parents=True)
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        IH.check_parents(str(self.root), str(self.libdir), include_target=False)  # fine
        with self.stranger(), self.assertRaises(SystemExit) as cm:
            IH.check_parents(str(self.root), str(self.libdir), include_target=False)
        self.assertIn(str(self.root), str(cm.exception))
        self.assertIn("not root-owned", str(cm.exception))

    def test_a_parent_that_is_a_file_is_refused(self) -> None:
        (self.root / "usr").write_text("not a directory")
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        with self.assertRaises(SystemExit) as cm:
            IH.check_parents(str(self.root), str(self.libdir), include_target=False)
        self.assertIn("not a directory", str(cm.exception))

    # -- the sudoers drop-in (r2 m7, m8) ---------------------------------------------

    def test_the_deploy_user_is_required_and_never_guessed(self) -> None:
        args = [a for a in ARGS if not a.startswith("--deploy-user")]
        env = {**os.environ, "SUDO_USER": "alfred", "USER": "alfred"}
        p = subprocess.run(
            [sys.executable, str(TOOL), *args, f"--root={self.root}", "--apply"],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("--deploy-user is required", p.stdout + p.stderr)
        self.assertEqual(self.tree(), [])

    def test_the_dry_run_names_the_grantee_and_prints_the_rendered_sudoers(self) -> None:
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("Sudoers grantee: alfred", p.stdout)
        self.assertIn("alfred ALL=", p.stdout)
        self.assertIn(
            str(self.dropin).replace(str(self.root), ""), p.stdout.replace(str(self.root), "")
        )

    def test_the_dry_run_shows_a_diff_against_the_installed_sudoers(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        old = self.dropin.read_text()
        self.dropin.chmod(0o640)
        self.dropin.write_text(old.replace("alfred", "bruce"))
        self.dropin.chmod(0o440)
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("-bruce ALL=(root)", p.stdout)
        self.assertIn("+alfred ALL=(root)", p.stdout)
        self.assertEqual(
            self.dropin.read_text(), old.replace("alfred", "bruce"), "dry run: untouched"
        )

    def test_a_sudoers_with_the_right_text_and_the_wrong_mode_is_rewritten(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.dropin.chmod(0o666)
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(stat.S_IMODE(self.dropin.stat().st_mode), 0o440)
        checked = [x for x in self.log.read_text().splitlines() if x != "live-existed"]
        self.assertEqual(len(checked), 2, "validated again")

    def test_a_sudoers_with_the_right_text_and_a_mode_that_is_not_0440_is_rewritten(self) -> None:
        """Not group- or world-writable, but not the mode sudo expects either."""
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        for mode in (0o400, 0o444, 0o640):
            with self.subTest(mode=oct(mode)):
                self.dropin.chmod(mode)
                p = self.run_tool("--apply")
                self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertEqual(stat.S_IMODE(self.dropin.stat().st_mode), 0o440)

    def test_a_sudoers_with_the_right_text_and_the_wrong_owner_is_not_unchanged(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        text = self.dropin.read_text()
        IH.STAGING = True
        self.addCleanup(setattr, IH, "STAGING", False)
        self.assertEqual(IH.sudoers_state(str(self.dropin), text), "unchanged")
        with self.stranger():
            self.assertEqual(IH.sudoers_state(str(self.dropin), text), "fix")

    def test_a_whole_config_check_runs_after_the_drop_in_is_in_place(self) -> None:
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual((self.tmp / "visudo.whole.log").read_text().splitlines(), ["whole"])

    def test_a_drop_in_that_breaks_the_whole_config_is_removed_again(self) -> None:
        p = self.run_tool("--apply", whole_fail=True)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("whole configuration", p.stdout + p.stderr)
        self.assertIn("removed", p.stdout + p.stderr)
        self.assertFalse(self.dropin.exists())

    def test_a_drop_in_that_breaks_the_whole_config_puts_the_old_one_back(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        old = self.dropin.read_text()
        self.dropin.chmod(0o640)
        self.dropin.write_text(old + "# an older rendering\n")
        self.dropin.chmod(0o440)
        older = self.dropin.read_bytes()
        p = self.run_tool("--apply", whole_fail=True)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("put back as it was", p.stdout + p.stderr)
        self.assertEqual(self.dropin.read_bytes(), older)
        self.assertEqual(stat.S_IMODE(self.dropin.stat().st_mode), 0o440)

    # -- rollback.conf with the right bytes (r2 m12) ----------------------------------

    def test_a_conf_with_the_right_bytes_and_the_wrong_mode_gets_its_mode_fixed(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.chmod(0o600)
        p = self.run_tool("--apply")
        self.assertEqual(
            p.returncode, 0, "the owner's bytes are not touched, so nothing is left undone"
        )
        self.assertEqual(stat.S_IMODE(self.conf.stat().st_mode), 0o644)
        self.assertNotIn("--replace-conf", p.stdout)

    def test_a_group_writable_conf_with_the_right_bytes_is_fixed_too(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.conf.chmod(0o664)
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.assertEqual(stat.S_IMODE(self.conf.stat().st_mode), 0o644)

    # -- the checkout is not trusted either (r2 m9) -------------------------------------

    def fake_checkout(self) -> Path:
        co = self.tmp / "checkout"
        for rel in (
            "deploy/rollback-helpers/rollback_helper.py",
            "deploy/audiobook-organizer.service",
            "scripts/storage_format_guard.py",
        ):
            f = co / rel
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text("x")
            f.chmod(0o644)
        for d in (co, co / "deploy", co / "scripts", co / "deploy" / "rollback-helpers"):
            d.chmod(0o755)
        return co

    def sources(self, co: Path, user: str) -> list[str]:
        here = co / "deploy" / "rollback-helpers"
        patches = [
            unittest.mock.patch.object(IH, "REPO", co),
            unittest.mock.patch.object(IH, "HERE", here),
        ]
        with contextlib.ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            return IH.check_sources(user, strict=True)

    def test_a_clean_checkout_is_accepted(self) -> None:
        self.assertEqual(self.sources(self.fake_checkout(), "alfred"), [])

    def test_a_sticky_world_writable_ancestor_like_tmp_is_not_a_finding(self) -> None:
        co = self.fake_checkout()
        parent = co.parent
        parent.chmod(0o1777)
        self.addCleanup(parent.chmod, 0o700)
        self.assertEqual(self.sources(co, "alfred"), [])

    def test_a_world_writable_ancestor_without_the_sticky_bit_is_named(self) -> None:
        co = self.fake_checkout()
        parent = co.parent
        parent.chmod(0o777)
        self.addCleanup(parent.chmod, 0o700)
        self.assertIn(str(parent), self.sources(co, "alfred"))

    def test_a_world_writable_source_file_is_named(self) -> None:
        co = self.fake_checkout()
        guard_py = co / "scripts" / "storage_format_guard.py"
        guard_py.chmod(0o666)
        self.assertEqual(self.sources(co, "alfred"), [str(guard_py)])

    def test_a_world_writable_checkout_directory_is_named(self) -> None:
        co = self.fake_checkout()
        (co / "deploy").chmod(0o777)
        self.assertIn(str(co / "deploy"), self.sources(co, "alfred"))

    def test_files_the_deploy_user_owns_or_shares_a_group_with_are_named(self) -> None:
        me = pwd.getpwuid(os.getuid()).pw_name
        co = self.fake_checkout()
        got = self.sources(co, me)
        self.assertIn(str(co / "scripts" / "storage_format_guard.py"), got, "owned by that user")
        self.assertIn(str(co), got)

    def test_a_group_writable_file_is_named_for_a_member_of_the_group(self) -> None:
        me = pwd.getpwuid(os.getuid()).pw_name
        co = self.fake_checkout()
        f = co / "deploy" / "audiobook-organizer.service"
        with unittest.mock.patch.object(IH.os, "getgrouplist", lambda *_: [f.stat().st_gid]):
            st = f.stat()
            self.assertFalse(IH.writable_by(st, 12345, {st.st_gid}), "not group-writable yet")
            f.chmod(0o664)
            self.assertTrue(IH.writable_by(f.stat(), 12345, {f.stat().st_gid}))
            self.assertFalse(IH.writable_by(f.stat(), 12345, set()))
        self.assertTrue(me)

    def test_apply_refuses_to_run_from_a_checkout_the_deploy_user_can_write(self) -> None:
        me = pwd.getpwuid(os.getuid()).pw_name
        args = [a for a in ARGS if not a.startswith("--deploy-user")] + [f"--deploy-user={me}"]
        p = self.run_tool("--apply", args=args)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("REFUSING", p.stdout + p.stderr)
        self.assertIn("root-owned export", p.stdout + p.stderr)
        self.assertEqual(self.tree(), [], "nothing was written")

    def test_a_dry_run_from_such_a_checkout_says_so_and_still_shows_the_plan(self) -> None:
        me = pwd.getpwuid(os.getuid()).pw_name
        args = [a for a in ARGS if not a.startswith("--deploy-user")] + [f"--deploy-user={me}"]
        p = self.run_tool(args=args)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("NOTICE", p.stdout)
        self.assertIn("APPLY=1 will refuse", p.stdout)

    def test_the_sudo_re_run_is_isolated_too(self) -> None:
        argv = IH.sudo_argv()
        self.assertEqual(argv[0], "sudo")
        self.assertEqual(argv[1], sys.executable)
        self.assertEqual(argv[2], "-I")
        self.assertEqual(argv[3:], sys.argv)

    def test_python_runs_isolated_whatever_the_environment_says(self) -> None:
        poison = self.tmp / "poison"
        poison.mkdir()
        (poison / "difflib.py").write_text("raise RuntimeError('loaded from PYTHONPATH')\n")
        env = {
            **os.environ,
            "PYTHONPATH": str(poison),
            "VISUDO_LOG": str(self.log),
            "VISUDO_COPY": str(self.copy),
            "LIVE_DROPIN": str(self.dropin),
        }
        p = subprocess.run(
            [sys.executable, str(TOOL), *ARGS, f"--root={self.root}"],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("loaded from PYTHONPATH", p.stdout + p.stderr)

    # -- the guard's patterns (r2 m5) -----------------------------------------------------

    def test_a_trailing_newline_does_not_pass_the_safe_user_or_path_patterns(self) -> None:
        for bad in ("alfred\n", "alfred\nroot"):
            self.assertIsNone(guard._SAFE_USER.fullmatch(bad), repr(bad))
        for bad in ("/var/lib/x\n", "/var/lib/x\n/etc"):
            self.assertIsNone(guard._SAFE_PATH.fullmatch(bad), repr(bad))
        self.assertIsNotNone(guard._SAFE_USER.fullmatch("alfred"))
        self.assertIsNotNone(guard._SAFE_PATH.fullmatch("/var/lib/x"))

    def test_the_validation_rejects_a_path_and_a_user_with_a_trailing_newline(self) -> None:
        o = guard.Opts(
            host="localhost",
            db="/path/to/audiobooks.pebble\n",
            bin="/usr/local/bin/audiobook-organizer",
            user="audiobook",
            stage_bin="/home/alfred/audiobook-organizer",
        )
        self.assertTrue(guard._validate(o))
        o2 = guard.Opts(
            host="localhost",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user="audiobook\n",
            stage_bin="/home/alfred/audiobook-organizer",
        )
        self.assertTrue(guard._validate(o2))

    def test_a_deploy_user_with_a_trailing_newline_is_refused_by_the_installer(self) -> None:
        args = [a for a in ARGS if not a.startswith("--deploy-user")] + ["--deploy-user=alfred\n"]
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
