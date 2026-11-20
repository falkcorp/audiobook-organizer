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
import shutil
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
        # The tool runs from a copy under the temp dir, never from this checkout:
        # whether the checkout's own ancestors (/home, a runner's work directory)
        # pass the root-only source check depends on the machine, not on the test.
        self.tool = (
            self.installer_copy("tool") / "deploy" / "rollback-helpers" / "install_helpers.py"
        )

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
                str(self.tool),
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
                str(self.tool),
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
            [sys.executable, str(self.tool), *args, f"--root={self.root}", "--apply"],
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

    def make_n(self, *extra: str) -> str:
        p = subprocess.run(
            [
                "make",
                "-n",
                "install-rollback-helpers",
                "DEPLOY_BIN=/opt/x",
                "DEPLOY_DB=/var/y",
                *extra,
            ],
            cwd=REPO,
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(p.returncode, 0, p.stderr)
        return p.stdout

    def test_make_apply_runs_the_installer_from_a_root_owned_export_not_the_checkout(self) -> None:
        out = self.make_n("APPLY=1", "ROLLBACK_EXPORT_DIR=/srv/exp")
        self.assertIn('sudo mktemp -d "/srv/exp/aorg-install.XXXXXX"', out, "a fresh dir every run")
        self.assertNotIn("sudo install -d", out, "no fixed, reusable, pre-creatable directory")
        self.assertNotIn('"/srv/exp/aorg-install"', out)
        self.assertIn("git archive HEAD deploy/rollback-helpers", out)
        self.assertIn("scripts/storage_format_guard.py", out)
        self.assertIn(
            'sudo python3 -I -B "$exp/deploy/rollback-helpers/install_helpers.py"',
            out,
        )
        self.assertIn(
            "trap 'sudo rm -rf -- \"$exp\"' EXIT", out, "removed even when the install fails"
        )
        self.assertLess(
            out.index('case "$exp" in'),
            out.index("trap "),
            "nothing is removed unless the path checks out",
        )
        self.assertIn('"/srv/exp"/aorg-install.??????', out)
        self.assertIn("--apply", out)
        piped = [ln for ln in out.splitlines() if "| sudo tar" in ln]
        self.assertEqual(len(piped), 1, out)
        for needed in (
            "deploy/rollback-helpers",
            "deploy/audiobook-organizer.service",
            "scripts/storage_format_guard.py",
            "--no-same-owner",
            "git -c tar.umask=0022 archive",
            "tar -x -f -",
            '-C "$exp"',
        ):
            self.assertIn(needed, piped[0], "what the installer reads, extracted root-owned")
        applied = out.split('if [ -n "1" ]; then', 1)[1].split("else", 1)[0]
        self.assertNotIn(
            "python3 -I -B deploy/rollback-helpers", applied, "never from the checkout"
        )

    def test_make_without_apply_stays_in_the_checkout_and_never_calls_sudo(self) -> None:
        out = self.make_n()
        self.assertIn("python3 -I -B deploy/rollback-helpers/install_helpers.py", out)
        branch = out.split("else", 1)[1]
        self.assertNotIn("sudo", branch)
        self.assertNotIn("--apply", branch)

    def test_a_dry_run_that_cannot_read_the_root_only_dropin_says_so_not_a_traceback(self) -> None:
        if os.geteuid() == 0:
            self.skipTest("root can read any file")
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        before = self.dropin.read_text()
        self.dropin.chmod(0o000)
        try:
            p = self.run_tool()
        finally:
            self.dropin.chmod(0o440)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("Traceback", p.stderr)
        self.assertIn("unreadable", p.stdout)
        self.assertIn("APPLY=1 compares it as root", p.stdout)
        self.assertNotIn("with sudo", p.stdout, "the dry run never asks for root")
        self.assertIn("alfred ALL=", p.stdout, "the rendered text is still shown")
        self.assertEqual(self.dropin.read_text(), before, "dry run: untouched")

    def test_a_dry_run_over_a_root_only_installed_file_says_unreadable_not_a_traceback(
        self,
    ) -> None:
        if os.geteuid() == 0:
            self.skipTest("root can read any file")
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        target = self.libdir / "rollback_helper.py"
        target.chmod(0o000)
        try:
            p = self.run_tool()
        finally:
            target.chmod(0o755)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("Traceback", p.stderr)
        line = next(ln for ln in p.stdout.splitlines() if ln.endswith("rollback_helper.py"))
        self.assertIn("unreadable", line)

    def test_an_apply_that_cannot_read_the_dropin_it_would_restore_refuses_cleanly(self) -> None:
        if os.geteuid() == 0:
            self.skipTest("root can read any file")
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        before = self.dropin.read_text()
        self.dropin.chmod(0o000)
        try:
            p = self.run_tool("--apply")
        finally:
            self.dropin.chmod(0o440)
        self.assertNotEqual(p.returncode, 0)
        self.assertNotIn("Traceback", p.stderr)
        self.assertIn("cannot read", p.stderr + p.stdout)
        self.assertEqual(self.dropin.read_text(), before, "left as it was")

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

    def fake_checkout(self, nest: str = "") -> Path:
        """nest: extra directories between the temp dir and the checkout, so a
        test can tell the checkout's parent from its grandparents."""
        co = self.tmp / nest / "checkout"
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

    def sources(self, co: Path, staging: bool = True) -> list[str]:
        """check_sources over a fake checkout. staging=True trusts the running
        user as well as root (what --root does, and the only way a checkout this
        test made can be accepted); False is the real run: root only."""
        here = co / "deploy" / "rollback-helpers"
        with contextlib.ExitStack() as stack:
            stack.enter_context(unittest.mock.patch.object(IH, "REPO", co))
            stack.enter_context(unittest.mock.patch.object(IH, "HERE", here))
            stack.enter_context(unittest.mock.patch.object(IH, "STAGING", staging))
            return IH.check_sources()

    def fake_stat(self, uid: int, mode: int) -> os.stat_result:
        return os.stat_result((mode, 1, 1, 1, uid, 0, 0, 0, 0, 0))

    def test_a_clean_checkout_is_accepted(self) -> None:
        self.assertEqual(self.sources(self.fake_checkout()), [])

    def test_a_checkout_owned_by_anyone_but_root_is_named_in_a_real_run(self) -> None:
        if os.geteuid() == 0:
            self.skipTest("everything this test makes is root's when run as root")
        co = self.fake_checkout()
        got = self.sources(co, staging=False)
        for must in (co, co / "scripts", co / "scripts" / "storage_format_guard.py", co.parent):
            self.assertIn(
                str(must), got, "owned by a user, whatever its mode: the owner can chmod it"
            )

    def test_every_ancestor_of_the_checkout_is_watched_not_just_its_parent(self) -> None:
        """r3 m9: whoever can rename any directory above the checkout swaps the
        whole tree, so narrowing the watch to REPO.parent must fail here."""
        if os.geteuid() == 0:
            self.skipTest("everything this test makes is root's when run as root")
        co = self.fake_checkout(nest="outer/inner")
        got = self.sources(co, staging=False)
        for ancestor in (co.parent, co.parent.parent, self.tmp):
            self.assertIn(str(ancestor), got, "a user-owned directory above the checkout")

    def test_a_root_run_from_a_checkout_that_is_not_roots_is_refused_even_as_a_dry_run(
        self,
    ) -> None:
        """r3 m9: the euid==0 branch. The dry run never needs root, and as root
        it would load the guard from the checkout, so it is refused with the
        recipe that works."""
        with contextlib.ExitStack() as stack:
            stack.enter_context(
                unittest.mock.patch.object(IH, "check_sources", return_value=["/home/x/checkout"])
            )
            stack.enter_context(unittest.mock.patch.object(IH.os, "geteuid", return_value=0))
            cm = stack.enter_context(self.assertRaises(SystemExit))
            IH.main(ARGS)
        msg = str(cm.exception.code)
        self.assertIn("REFUSING", msg)
        self.assertIn("/home/x/checkout", msg)
        # the printed recipe is the Makefile's, so following it by hand works
        for needed in (
            "sudo mktemp -d",
            "git -c tar.umask=0022 archive HEAD",
            "tar -x -f - --no-same-owner",
            "python3 -I -B",
            "make install-rollback-helpers APPLY=1",
        ):
            self.assertIn(needed, msg)
        self.assertNotIn("--apply", msg, "the by-hand recipe repeats the caller's own arguments")

    def test_an_ordinary_user_dry_run_from_a_checkout_that_is_not_roots_is_not_refused(
        self,
    ) -> None:
        """m1: the dry run never needs root, so a user's checkout is fine for it."""
        with contextlib.ExitStack() as stack:
            stack.enter_context(
                unittest.mock.patch.object(IH, "check_sources", return_value=["/home/x/checkout"])
            )
            stack.enter_context(unittest.mock.patch.object(IH.os, "geteuid", return_value=1000))
            stack.enter_context(
                unittest.mock.patch.object(IH, "load", side_effect=RuntimeError("past the refusal"))
            )
            stack.enter_context(self.assertRaises(RuntimeError))
            IH.main(ARGS)

    def test_a_read_only_path_owned_by_the_deploy_user_is_still_untrusted(self) -> None:
        """r3 H1. Mode 0555 is not safe when the owner is not root: chmod u+w."""
        self.assertTrue(IH.untrusted_source(self.fake_stat(1000, stat.S_IFREG | 0o444), False))
        self.assertTrue(IH.untrusted_source(self.fake_stat(1000, stat.S_IFDIR | 0o555), False))
        self.assertTrue(IH.untrusted_source(self.fake_stat(1000, stat.S_IFDIR | 0o555), True))
        with unittest.mock.patch.object(IH, "STAGING", False):
            self.assertFalse(IH.untrusted_source(self.fake_stat(0, stat.S_IFDIR | 0o755), True))
            self.assertFalse(IH.untrusted_source(self.fake_stat(0, stat.S_IFREG | 0o644), False))

    def test_the_sticky_exemption_is_for_a_root_owned_directory_above_the_checkout(self) -> None:
        sticky = stat.S_IFDIR | stat.S_ISVTX | 0o777
        with unittest.mock.patch.object(IH, "STAGING", False):
            self.assertFalse(IH.untrusted_source(self.fake_stat(0, sticky), True), "/tmp")
            self.assertTrue(IH.untrusted_source(self.fake_stat(0, sticky), False), "not above it")
            self.assertTrue(IH.untrusted_source(self.fake_stat(1000, sticky), True), "r3 H2")
            self.assertTrue(
                IH.untrusted_source(self.fake_stat(0, stat.S_IFDIR | 0o777), True),
                "world-writable and not sticky",
            )
            self.assertTrue(
                IH.untrusted_source(self.fake_stat(0, stat.S_IFREG | stat.S_ISVTX | 0o777), True),
                "a file is never exempt",
            )

    def test_a_child_of_a_1777_parent_that_the_deploy_user_owns_is_named(self) -> None:
        """r3 H2: the entry below /tmp is the deploy user's, mode 0555 or not."""
        if os.geteuid() == 0:
            self.skipTest("everything this test makes is root's when run as root")
        co = self.fake_checkout()
        co.parent.chmod(0o1777)
        self.addCleanup(co.parent.chmod, 0o700)
        co.chmod(0o555)
        self.addCleanup(co.chmod, 0o755)
        got = self.sources(co, staging=False)
        self.assertIn(str(co), got)
        # and with staging's extra trusted user, a USER-owned sticky ancestor is
        # not skipped: only root's is
        self.assertIn(str(co.parent), self.sources(co, staging=True))

    def test_a_checkout_under_a_real_sticky_root_directory_like_tmp_is_not_a_finding(self) -> None:
        tmp = Path("/tmp").resolve()
        st = tmp.stat()
        if not (st.st_uid == 0 and st.st_mode & stat.S_ISVTX):
            self.skipTest("/tmp is not a root-owned sticky directory here")
        d = Path(tempfile.mkdtemp(dir=tmp)).resolve()
        self.addCleanup(shutil.rmtree, d, True)
        co = d / "checkout"
        for rel in (
            "deploy/rollback-helpers/rollback_helper.py",
            "deploy/audiobook-organizer.service",
            "scripts/storage_format_guard.py",
        ):
            f = co / rel
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text("x")
        self.assertEqual(self.sources(co), [], "the sticky root-owned ancestor is skipped")

    def test_a_world_writable_ancestor_without_the_sticky_bit_is_named(self) -> None:
        co = self.fake_checkout()
        parent = co.parent
        parent.chmod(0o777)
        self.addCleanup(parent.chmod, 0o700)
        self.assertIn(str(parent), self.sources(co))

    def test_a_world_or_group_writable_source_file_is_named(self) -> None:
        for mode in (0o666, 0o664, 0o646):
            with self.subTest(mode=oct(mode)):
                co = self.fake_checkout()
                guard_py = co / "scripts" / "storage_format_guard.py"
                guard_py.chmod(mode)
                self.assertEqual(self.sources(co), [str(guard_py)])

    def test_a_group_or_world_writable_checkout_directory_is_named(self) -> None:
        co = self.fake_checkout()
        for d in ("deploy", "scripts", "deploy/rollback-helpers"):
            (co / d).chmod(0o775)
            self.assertIn(str(co / d), self.sources(co))
            (co / d).chmod(0o755)

    def test_a_symlinked_source_is_named(self) -> None:
        co = self.fake_checkout()
        guard_py = co / "scripts" / "storage_format_guard.py"
        real = self.tmp / "elsewhere.py"
        real.write_text("x")
        guard_py.unlink()
        guard_py.symlink_to(real)
        self.assertEqual(self.sources(co), [str(guard_py)])

    def test_a_pycache_in_a_source_directory_is_held_to_the_same_rule(self) -> None:
        co = self.fake_checkout()
        for d in ("scripts", "deploy", "deploy/rollback-helpers"):
            cache = co / d / "__pycache__"
            cache.mkdir()
            cache.chmod(0o755)
            pyc = cache / "x.cpython-0.pyc"
            pyc.write_bytes(b"x")
            pyc.chmod(0o644)
        self.assertEqual(self.sources(co), [], "clean ones are fine")
        pyc.chmod(0o666)
        self.assertEqual(self.sources(co), [str(pyc)])
        pyc.chmod(0o644)
        (co / "scripts" / "__pycache__").chmod(0o777)
        self.assertEqual(self.sources(co), [str(co / "scripts" / "__pycache__")])

    def test_a_path_with_a_posix_acl_is_named(self) -> None:
        co = self.fake_checkout()
        guard_py = co / "scripts" / "storage_format_guard.py"

        def listxattr(path, *, follow_symlinks=True):
            return ["system.posix_acl_access"] if str(path) == str(guard_py) else []

        with unittest.mock.patch.object(IH.os, "listxattr", listxattr, create=True):
            self.assertEqual(self.sources(co), [str(guard_py)])
        with unittest.mock.patch.object(
            IH.os, "listxattr", lambda *a, **k: ["user.x"], create=True
        ):
            self.assertEqual(self.sources(co), [], "another attribute is not an ACL")

    def installer_copy(self, name: str = "realcopy") -> Path:
        """A checkout made of the real installer, helper, guard and unit
        template, whose permissions the test controls."""
        co = self.tmp / name
        helpers = co / "deploy" / "rollback-helpers"
        helpers.mkdir(parents=True)
        for f in HELPERS.iterdir():
            if f.is_file():
                shutil.copy(f, helpers / f.name)
        shutil.copy(REPO / "deploy" / "audiobook-organizer.service", co / "deploy")
        (co / "scripts").mkdir()
        shutil.copy(SCRIPTS / "storage_format_guard.py", co / "scripts")
        for d in (co, co / "deploy", co / "scripts", helpers):
            d.chmod(0o755)
        return co

    def run_copy(self, co: Path, *extra: str) -> subprocess.CompletedProcess:
        env = {
            **os.environ,
            "VISUDO_LOG": str(self.log),
            "VISUDO_COPY": str(self.copy),
            "LIVE_DROPIN": str(self.dropin),
            "VISUDO_WHOLE_LOG": str(self.tmp / "visudo.whole.log"),
        }
        return subprocess.run(
            [
                sys.executable,
                str(co / "deploy" / "rollback-helpers" / "install_helpers.py"),
                *ARGS,
                f"--root={self.root}",
                f"--visudo={self.visudo}",
                *extra,
            ],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def test_apply_refuses_to_run_from_a_checkout_that_others_can_write(self) -> None:
        co = self.installer_copy()
        (co / "scripts" / "storage_format_guard.py").chmod(0o666)
        p = self.run_copy(co, "--apply")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("REFUSING", p.stdout + p.stderr)
        self.assertIn("storage_format_guard.py", p.stdout + p.stderr)
        self.assertIn("root-owned export", p.stdout + p.stderr)
        self.assertEqual(self.tree(), [], "nothing was written")

    def test_a_dry_run_from_such_a_checkout_says_so_and_still_shows_the_plan(self) -> None:
        co = self.installer_copy()
        (co / "scripts" / "storage_format_guard.py").chmod(0o666)
        p = self.run_copy(co)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("NOTICE", p.stdout)
        self.assertIn("with --apply from here would refuse", p.stdout)
        self.assertIn("exports HEAD to a root-owned directory", p.stdout)

    def test_a_clean_copy_applies(self) -> None:
        p = self.run_copy(self.installer_copy(), "--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_the_sources_are_run_from_their_bytes_never_from_a_cached_pyc(self) -> None:
        """r3 M3. An UNCHECKED hash-based .pyc is never compared with the source:
        a loader that consults __pycache__ runs it as root."""
        import py_compile

        co = self.installer_copy()
        src = co / "scripts" / "storage_format_guard.py"
        evil = self.tmp / "evil.py"
        evil.write_text("POISONED = True\n")
        cfile = importlib.util.cache_from_source(str(src))
        py_compile.compile(
            str(evil),
            cfile=cfile,
            doraise=True,
            invalidation_mode=py_compile.PycInvalidationMode.UNCHECKED_HASH,
        )
        self.assertTrue(os.path.exists(cfile))
        # the control: the standard loader DOES take the cached bytecode
        spec = importlib.util.spec_from_file_location("ctl_poisoned", src)
        assert spec and spec.loader
        ctl = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(ctl)
        self.assertTrue(getattr(ctl, "POISONED", False), "the control must be poisoned")
        mod = IH.load("guard_under_load_test", src)
        self.assertFalse(hasattr(mod, "POISONED"))
        self.assertTrue(hasattr(mod, "sudoers"), "the real source ran")
        self.assertIs(sys.modules["guard_under_load_test"], mod)
        self.addCleanup(sys.modules.pop, "guard_under_load_test", None)
        self.addCleanup(sys.modules.pop, "ctl_poisoned", None)

    def test_the_installer_writes_no_bytecode(self) -> None:
        code = (
            "import runpy, sys\n"
            f"sys.argv = {[str(self.tool), *ARGS, f'--root={self.root}', f'--visudo={self.visudo}']!r}\n"
            f"try:\n    runpy.run_path({str(self.tool)!r}, run_name='__main__')\n"
            "except SystemExit:\n    pass\n"
            "print('DWB', sys.dont_write_bytecode)\n"
        )
        p = subprocess.run(
            [sys.executable, "-I", "-c", code], capture_output=True, text=True, timeout=60
        )
        self.assertIn("DWB True", p.stdout, p.stdout + p.stderr)

    def test_the_sudo_re_run_is_isolated_too(self) -> None:
        argv = IH.sudo_argv()
        self.assertEqual(argv[0], "sudo")
        self.assertEqual(argv[1], sys.executable)
        self.assertEqual(argv[2:4], ["-I", "-B"], "the same flags as the Makefile and the docs")
        self.assertEqual(argv[4:], sys.argv)

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
            [sys.executable, str(self.tool), *ARGS, f"--root={self.root}"],
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
