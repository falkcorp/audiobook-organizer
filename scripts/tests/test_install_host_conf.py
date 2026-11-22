# file: scripts/tests/test_install_host_conf.py
# version: 0.1.0
# guid: 7e2d9b64-1c85-4f3a-a0d7-5b9e63c14a82
# last-edited: 2026-11-22

"""Tests for `install_helpers.py --host-conf` (`make install-host-conf`).

Run with:
    python3 -m unittest scripts.tests.test_install_host_conf

Everything is installed under a temporary --root. `systemctl` is a script this
file writes: it logs its argv, so a test can say what was run and what never
was (a restart). The rules host.conf is checked against are the rollback
helper's own (rollback_helper._check_rules), so these tests pin that the
installer routes through them, not what they say; test_rollback_helper.py
pins that.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import os
import re
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
UNIT = "audiobook-organizer.service"
RH_LIBDIR = "/usr/local/libexec/audiobook-organizer/rollback"
CANARY = "canary_Zq7x"


def load(name: str, path: Path) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


IH = load("install_helpers_for_host_conf", HELPERS / "install_helpers.py")

FAKE_SYSTEMCTL = """#!/bin/sh
echo "$*" >> "$SYSTEMCTL_LOG"
if [ "$1" = "show" ]; then
  if [ -e "$HOST_CONF_DEST" ]; then echo "DropInPaths=$HOST_CONF_DEST"; else echo "DropInPaths="; fi
fi
exit "${SYSTEMCTL_EXIT:-0}"
"""

GOOD = (
    "[Service]\n"
    "ExecStart=\n"
    "ExecStart=/usr/local/bin/audiobook-organizer serve --port 8484 \\\n"
    "      --db /path/to/audiobooks.pebble\n"
    "WorkingDirectory=/var/lib/audiobook-organizer\n"
    "MemoryMax=30G\n"
    "TimeoutStopSec=90\n"
)

CONF_VALUES = {
    "bin": "/usr/local/bin/audiobook-organizer",
    "db": "/path/to/audiobooks.pebble",
    "state_dir": "/var/lib/audiobook-organizer",
    "unit": UNIT,
    "service_user": "audiobook",
    "archive_dir": "/var/backups/audiobook-organizer",
    "checkpoint_dir": "",
    "offline_env": "HOME=/var/lib/audiobook-organizer",
    "service_group": "",
    "stage_bin": "/home/alfred/audiobook-organizer",
    "stage_local_conf": "",
}


class InstallHostConf(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        # resolved: /var is a symlink on macOS and the source path must have none
        self.tmp = Path(self._tmp.name).resolve()
        self.root = self.tmp / "root"
        self.systemd = self.root / "etc" / "systemd" / "system"
        self.unit_dir = self.systemd / f"{UNIT}.d"
        self.dest = self.unit_dir / "host.conf"
        self.systemd.mkdir(parents=True)
        conf_dir = self.root / "etc" / "audiobook-organizer"
        conf_dir.mkdir()
        (conf_dir / "rollback.conf").write_text(IH.render_conf(CONF_VALUES))
        for d in (
            self.root,
            self.root / "etc",
            self.root / "etc" / "systemd",
            self.systemd,
            conf_dir,
        ):
            d.chmod(0o755)
        (conf_dir / "rollback.conf").chmod(0o644)
        self.systemctl = self.tmp / "systemctl"
        self.systemctl.write_text(FAKE_SYSTEMCTL)
        self.systemctl.chmod(0o755)
        self.log = self.tmp / "systemctl.log"
        self.src_dir = self.tmp / "src"
        self.src_dir.mkdir(mode=0o755)
        self.src = self.src_dir / "host.conf"
        self.src.write_text(GOOD)
        self.src.chmod(0o644)
        # The tool runs from a copy under the temp dir (see test_install_helpers).
        co = self.tmp / "co"
        helpers = co / "deploy" / "rollback-helpers"
        helpers.mkdir(parents=True)
        for f in HELPERS.iterdir():
            if f.is_file():
                shutil.copy(f, helpers / f.name)
        for d in (co, co / "deploy", helpers):
            d.chmod(0o755)
        self.tool = helpers / "install_helpers.py"

    # -- helpers ---------------------------------------------------------------

    def run_tool(
        self, *extra: str, source: Path | str | None = None, env: dict | None = None, unit=UNIT
    ) -> subprocess.CompletedProcess:
        e = {k: v for k, v in os.environ.items() if k != "SUDO_UID"}
        e.update(
            SYSTEMCTL_LOG=str(self.log),
            HOST_CONF_DEST=str(self.dest),
            **(env or {}),
        )
        return subprocess.run(
            [
                sys.executable,
                str(self.tool),
                f"--host-conf={self.src if source is None else source}",
                f"--unit={unit}",
                f"--root={self.root}",
                f"--systemctl={self.systemctl}",
                *extra,
            ],
            env=e,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def tree(self) -> list[tuple[str, int, int]]:
        """Every path under the root with its mode and, for a file, its mtime."""
        out = []
        for p in sorted(self.root.rglob("*")):
            st = p.lstat()
            out.append((str(p.relative_to(self.root)), stat.S_IMODE(st.st_mode), st.st_mtime_ns))
        return out

    def reset(self) -> None:
        """Back to the state setUp made, so each subTest stands alone (a refusal
        that wrongly installed must not make the cases after it fail too)."""
        for p in (self.log, self.dest):
            p.unlink(missing_ok=True)
        if self.unit_dir.is_dir():
            self.unit_dir.rmdir()

    def calls(self) -> list[str]:
        return self.log.read_text().splitlines() if self.log.exists() else []

    def assert_untouched(self, before: list, p: subprocess.CompletedProcess) -> None:
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.tree(), before, "a refusal changes nothing")
        self.assertEqual(self.calls(), [], "a refusal runs no systemctl")

    # -- the dry run -----------------------------------------------------------

    def test_dry_run_on_a_good_file_shows_the_plan_and_changes_nothing(self) -> None:
        before = self.tree()
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.tree(), before)
        self.assertFalse(self.dest.exists())
        self.assertEqual(self.calls(), [], "a dry run runs no systemctl at all")
        self.assertIn("accepted", p.stdout)
        self.assertIn("create", p.stdout)
        self.assertIn(str(self.dest), p.stdout)
        self.assertIn("dry run", p.stdout)
        self.assertIn("APPLY=1", p.stdout)

    def test_dry_run_shows_the_diff_against_the_installed_file(self) -> None:
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        self.dest.write_text(GOOD.replace("MemoryMax=30G", "MemoryMax=12G"))
        self.dest.chmod(0o644)
        before = self.tree()
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("replace", p.stdout)
        self.assertIn("-MemoryMax=12G", p.stdout)
        self.assertIn("+MemoryMax=30G", p.stdout)
        self.assertEqual(self.tree(), before)

    def test_the_committed_example_passes_as_it_stands(self) -> None:
        shutil.copy(REPO / "deploy" / "host.conf.example", self.src)
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("accepted", p.stdout)
        self.assertNotIn("sets no ExecStart=", p.stdout, "the example sets an ExecStart=")

    def test_a_file_without_an_execstart_is_accepted_with_a_notice(self) -> None:
        self.src.write_text("[Service]\nMemoryMax=30G\n")
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("sets no ExecStart=", p.stdout)

    # -- apply -----------------------------------------------------------------

    def test_apply_installs_it_and_reloads_but_never_restarts(self) -> None:
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.dest.read_bytes(), GOOD.encode())
        self.assertEqual(stat.S_IMODE(self.dest.stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.unit_dir.stat().st_mode), 0o755)
        self.assertEqual(
            self.calls(), ["daemon-reload", f"show -p DropInPaths {UNIT}"], "reload, then show"
        )
        for call in self.calls():
            self.assertNotIn(call.split()[0], ("restart", "start", "stop", "try-restart"), call)
            self.assertNotIn("restart", call)
        self.assertIn(f"DropInPaths={self.dest}", p.stdout, "prints the line that proves it")
        self.assertNotIn("NOTICE: " + str(self.dest), p.stdout)
        self.assertIn("NOT restarted", p.stdout)
        leftovers = [x.name for x in self.unit_dir.iterdir() if x.name != "host.conf"]
        self.assertEqual(leftovers, [], "no temporary file is left behind")

    def test_what_it_wrote_is_a_host_conf_the_helper_accepts(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        rh = load("rollback_helper_for_host_conf", HELPERS / "rollback_helper.py")
        rh.CONFIG = str(self.root / "etc" / "audiobook-organizer" / "rollback.conf")
        rh.SYSTEMD_DIR = str(self.systemd)
        rh._check_host_conf(rh.load_config())  # raises RefusedError when it refuses

    def test_a_second_apply_reports_unchanged_and_rewrites_nothing(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        before = self.tree()
        self.log.unlink()
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("unchanged", p.stdout)
        self.assertNotIn("wrote", p.stdout)
        self.assertEqual(self.tree(), before, "same bytes, mode and mtime")
        self.assertIn(
            "daemon-reload", self.calls(), "a reload that failed last time is not skipped now"
        )

    def test_a_changed_source_replaces_it(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.src.write_text(GOOD.replace("MemoryMax=30G", "MemoryMax=12G"))
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("replace", p.stdout)
        self.assertIn("MemoryMax=12G", self.dest.read_text())

    def test_a_wrong_mode_is_fixed(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.dest.chmod(0o640)
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(stat.S_IMODE(self.dest.stat().st_mode), 0o644)

    def test_a_tight_umask_does_not_close_the_drop_in_directory(self) -> None:
        old = os.umask(0o077)
        self.addCleanup(os.umask, old)
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        self.assertEqual(stat.S_IMODE(self.unit_dir.stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(self.dest.stat().st_mode), 0o644)

    def test_a_symlink_at_the_destination_is_replaced_not_followed(self) -> None:
        victim = self.tmp / "victim"
        victim.write_text("keep\n")
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        self.dest.symlink_to(victim)
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(victim.read_text(), "keep\n")
        self.assertFalse(self.dest.is_symlink())
        self.assertEqual(self.dest.read_bytes(), GOOD.encode())

    def test_a_planted_temporary_symlink_is_not_written_through(self) -> None:
        victim = self.tmp / "victim"
        victim.write_text("keep\n")
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        (self.unit_dir / "host.conf.tmp").symlink_to(victim)
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(victim.read_text(), "keep\n")

    def test_a_failed_daemon_reload_is_reported(self) -> None:
        p = self.run_tool("--apply", env={"SYSTEMCTL_EXIT": "1"})
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("daemon-reload exited 1", p.stderr)
        self.assertEqual(
            self.calls(), ["daemon-reload"], "never reports a drop-in it could not load"
        )

    # -- refusals: the helper's own rules --------------------------------------

    def test_each_refused_line_is_refused_by_file_and_line_without_its_text(self) -> None:
        bad = {
            "an extra Exec line": f"ExecStartPre=/bin/{CANARY}",
            "a + prefix": f"ExecStart=+/bin/{CANARY}",
            "a ! prefix": f"ExecStart=!/bin/{CANARY}",
            # a comment line is skipped, so a canary there never reaches the logical
            # line; each canary sits in the refused line's own text
            "User=root": f"User=root_{CANARY}",
            "Group=root": f"Group=root_{CANARY}",
            "a second command": f"ExecStart=/bin/true ; /bin/{CANARY}",
            # the one rule with no free text in its line: its own test below
            # pins the whole message
            "Type=oneshot": "Type=oneshot",
            "PermissionsStartOnly": f"PermissionsStartOnly={CANARY}",
            "AmbientCapabilities": f"AmbientCapabilities={CANARY}",
            "a continuation hiding a prefix": f"ExecStart=\\\n+/bin/{CANARY}",
        }
        for name, line in bad.items():
            with self.subTest(name):
                self.reset()
                self.src.write_text(GOOD + line + "\n")
                before = self.tree()
                p = self.run_tool("--apply")
                self.assert_untouched(before, p)
                out = p.stdout + p.stderr
                self.assertIn(str(self.src), out, "names the file")
                self.assertIn("line 8", out, "names the line (the first physical one)")
                self.assertNotIn(CANARY, out, "never the text of a line")
                self.assertIn("nothing installed", out)

    def test_the_oneshot_refusal_says_exactly_the_rule_and_nothing_else(self) -> None:
        self.src.write_text(GOOD + "Type=oneshot\n")
        p = self.run_tool("--apply")
        self.assertNotEqual(p.returncode, 0)
        self.assertRegex(
            p.stderr,
            r"^install_helpers: " + re.escape(str(self.src)) + r" line 8 sets Type=oneshot, which "
            r"runs every command an ExecStart= line holds; [^;\n]*; nothing installed\n$",
        )

    def test_a_refusal_in_the_dry_run_too(self) -> None:
        self.src.write_text(GOOD + "ExecStartPost=/bin/x\n")
        before = self.tree()
        p = self.run_tool()
        self.assert_untouched(before, p)
        self.assertIn("line 8", p.stderr)

    def test_a_user_other_than_the_one_in_rollback_conf_is_refused(self) -> None:
        self.src.write_text(GOOD + "User=alfred\n")
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("service_user", p.stderr + p.stdout)

    def test_the_service_users_own_name_is_accepted(self) -> None:
        self.src.write_text(GOOD + "User=audiobook\nGroup=audiobook\n")
        self.assertEqual(self.run_tool().returncode, 0)

    def test_control_characters_nul_and_cr_are_refused(self) -> None:
        for text in (GOOD + "MemoryMax=1G\r\n", GOOD + "MemoryMax=1G\0\n", GOOD + "X\x0b=1\n"):
            with self.subTest(text=repr(text[-8:])):
                self.reset()
                self.src.write_bytes(text.encode())
                before = self.tree()
                self.assert_untouched(before, self.run_tool("--apply"))

    def test_a_file_over_the_helpers_size_limit_is_refused(self) -> None:
        self.src.write_text(GOOD + "# " + "x" * (256 << 10) + "\n")
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("bytes", p.stderr)

    # -- refusals: the source ---------------------------------------------------

    def test_a_symlinked_source_is_refused(self) -> None:
        link = self.src_dir / "link.conf"
        link.symlink_to(self.src)
        before = self.tree()
        p = self.run_tool("--apply", source=link)
        self.assert_untouched(before, p)
        self.assertIn("symlink", p.stderr)

    def test_a_source_below_a_symlinked_directory_is_refused(self) -> None:
        linked = self.tmp / "linked"
        linked.symlink_to(self.src_dir)
        before = self.tree()
        p = self.run_tool("--apply", source=linked / "host.conf")
        self.assert_untouched(before, p)
        self.assertIn("symlink", p.stderr)

    def test_a_directory_and_a_fifo_are_refused(self) -> None:
        fifo = self.src_dir / "fifo.conf"
        os.mkfifo(fifo)
        for source in (self.src_dir, fifo):
            with self.subTest(source=source.name):
                before = self.tree()
                self.assert_untouched(before, self.run_tool("--apply", source=source))

    def test_a_source_with_a_second_hard_link_is_refused(self) -> None:
        os.link(self.src, self.src_dir / "other-name")
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("hard link", p.stderr)

    def test_a_source_owned_by_someone_other_than_who_ran_sudo_is_refused(self) -> None:
        before = self.tree()
        p = self.run_tool("--apply", env={"SUDO_UID": str(os.getuid() + 1)})
        self.assert_untouched(before, p)
        self.assertIn("owned by uid", p.stderr)

    def test_a_missing_source_and_a_relative_path_are_refused(self) -> None:
        for source in (self.src_dir / "absent.conf", "host.conf"):
            with self.subTest(source=str(source)):
                before = self.tree()
                self.assert_untouched(before, self.run_tool("--apply", source=source))

    # -- refusals: the destination ----------------------------------------------

    def test_a_group_writable_drop_in_directory_is_refused(self) -> None:
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o775)
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("group- or world-writable", p.stderr)
        self.assertFalse(self.dest.exists())

    def test_a_world_writable_systemd_directory_is_refused(self) -> None:
        self.systemd.chmod(0o777)
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("group- or world-writable", p.stderr)

    def test_a_symlinked_drop_in_directory_is_refused(self) -> None:
        elsewhere = self.tmp / "elsewhere"
        elsewhere.mkdir(mode=0o755)
        self.unit_dir.symlink_to(elsewhere)
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertEqual(list(elsewhere.iterdir()), [])

    def test_a_missing_rollback_conf_says_to_install_the_helpers_first(self) -> None:
        (self.root / "etc" / "audiobook-organizer" / "rollback.conf").unlink()
        before = self.tree()
        p = self.run_tool("--apply")
        self.assert_untouched(before, p)
        self.assertIn("make install-rollback-helpers", p.stderr)

    def test_a_unit_other_than_the_one_in_rollback_conf_is_refused(self) -> None:
        before = self.tree()
        p = self.run_tool("--apply", unit="other.service")
        self.assert_untouched(before, p)
        self.assertIn("other.service", p.stderr)

    # -- running as root from a checkout that is not root's ---------------------

    def test_a_root_run_from_a_checkout_that_is_not_roots_is_refused_before_anything_loads(
        self,
    ) -> None:
        """The refusal comes BEFORE rollback_helper.py is loaded: that file runs
        as root from the checkout, so refusing after loading it is too late.
        The staging tests above never see an untrusted checkout, so this patches
        check_sources, geteuid and load, and calls main as the real run would."""
        for argv in (["--apply"], []):  # the dry run as root is refused too
            with self.subTest(argv=argv), contextlib.ExitStack() as stack:
                patch = unittest.mock.patch.object
                stack.enter_context(patch(IH, "check_sources", return_value=["/home/x/checkout"]))
                stack.enter_context(patch(IH.os, "geteuid", return_value=0))
                loaded = stack.enter_context(patch(IH, "load"))
                cm = stack.enter_context(self.assertRaises(SystemExit))
                IH.main([f"--host-conf={self.src}", *argv])
            msg = str(cm.exception.code)
            self.assertIn("REFUSING", msg)
            self.assertIn("/home/x/checkout", msg)
            self.assertIn("make install-host-conf APPLY=1", msg)
            loaded.assert_not_called()

    def test_an_ordinary_users_dry_run_from_a_checkout_that_is_not_roots_is_not_refused(
        self,
    ) -> None:
        if os.geteuid() == 0:
            self.skipTest("this is the root case, refused above")
        with contextlib.ExitStack() as stack:
            patch = unittest.mock.patch.object
            stack.enter_context(patch(IH, "check_sources", return_value=["/home/x/checkout"]))
            loaded = stack.enter_context(patch(IH, "load", wraps=IH.load))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            rc = IH.main([f"--host-conf={self.src}", f"--root={self.root}"])
        self.assertEqual(rc, 0)
        loaded.assert_called_once()

    # -- the test seams are not a root run's to take from its arguments ---------

    def run_bare(self, *extra: str) -> subprocess.CompletedProcess:
        """The tool without --root: how a real run is called."""
        env = {k: v for k, v in os.environ.items() if k != "SUDO_UID"}
        env.update(SYSTEMCTL_LOG=str(self.log), HOST_CONF_DEST=str(self.dest))
        return subprocess.run(
            [sys.executable, str(self.tool), f"--host-conf={self.src}", f"--unit={UNIT}", *extra],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def test_systemctl_without_root_is_refused(self) -> None:
        for extra in ([], ["--apply"]):
            with self.subTest(extra=extra):
                p = self.run_bare(f"--systemctl={self.systemctl}", *extra)
                self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertIn("test seam", p.stderr)
                self.assertIn("--root", p.stderr)
                self.assertEqual(self.calls(), [], "the named systemctl was never run")

    def test_a_unit_name_that_is_not_one_is_refused_before_anything_else(self) -> None:
        for unit in ('x.service" --systemctl="/tmp/evil', "x.service; y", "a b.service", "x"):
            with self.subTest(unit=unit):
                before = self.tree()
                p = self.run_tool("--apply", unit=unit)
                self.assert_untouched(before, p)
                self.assertIn("not a .service unit name", p.stderr)

    # -- what the tool checked is what it wrote (the in-process seams) ----------

    def run_main_inprocess(self, patch_rh) -> tuple[int, str, str]:
        """IH.main with the helper module patched by patch_rh(rh), as a real
        --apply run would call it (no --root-less sudo re-exec, no root)."""
        rh = load("rollback_helper_inprocess", HELPERS / "rollback_helper.py")
        patch_rh(rh)
        argv = [
            f"--host-conf={self.src}",
            f"--unit={UNIT}",
            f"--root={self.root}",
            f"--systemctl={self.systemctl}",
            "--apply",
        ]
        out, err = io.StringIO(), io.StringIO()
        env = {"SYSTEMCTL_LOG": str(self.log), "HOST_CONF_DEST": str(self.dest)}
        self.addCleanup(setattr, IH, "STAGING", IH.STAGING)
        with contextlib.ExitStack() as stack:
            patch = unittest.mock.patch.object
            stack.enter_context(patch(IH, "check_sources", return_value=[]))
            stack.enter_context(patch(IH, "load", return_value=rh))
            stack.enter_context(unittest.mock.patch.dict(os.environ, env))
            os.environ.pop("SUDO_UID", None)
            stack.enter_context(contextlib.redirect_stdout(out))
            stack.enter_context(contextlib.redirect_stderr(err))
            rc = IH.main(argv)
        return rc, out.getvalue(), err.getvalue()

    def test_the_bytes_written_are_the_bytes_checked_not_a_second_read_of_the_path(self) -> None:
        evil = f"[Service]\nExecStartPre=/bin/{CANARY}\nUser=root\n"

        def swap_after_open(rh: Any) -> None:
            real = rh._read_staged

            def read_then_swap(path: str, limit: int | None):
                got = real(path, limit)  # the fd the installer will read
                other = path + ".evil"
                Path(other).write_text(evil)
                os.replace(other, path)  # the path now names a file nothing checked
                return got

            rh._read_staged = read_then_swap

        rc, out, err = self.run_main_inprocess(swap_after_open)
        self.assertEqual(rc, 0, out + err)
        self.assertEqual(self.dest.read_bytes(), GOOD.encode(), "what was checked, not the swap")
        self.assertNotIn(CANARY, self.dest.read_text())

    def test_the_helpers_own_check_runs_on_what_was_written(self) -> None:
        def refuses(rh: Any) -> None:
            def boom(cfg: dict) -> None:
                raise rh.RefusedError("host.conf refused after the write (test)")

            rh._check_host_conf = boom

        rc, out, err = self.run_main_inprocess(refuses)
        self.assertNotEqual(rc, 0, out + err)
        self.assertIn("refused after the write", err)
        self.assertEqual(self.calls(), [], "no reload for a file the helper would refuse")

    def test_text_that_differs_from_the_bytes_read_is_refused(self) -> None:
        def reads_other_text(rh: Any) -> None:
            real = rh._read_root_conf

            def other(path: str) -> str:
                text = real(path)
                return text + "# tampered\n" if "aorg-host-conf" in path else text

            rh._read_root_conf = other

        rc, out, err = self.run_main_inprocess(reads_other_text)
        self.assertNotEqual(rc, 0, out + err)
        self.assertIn("changed while it was being checked", err)
        self.assertFalse(self.dest.exists())
        self.assertEqual(self.calls(), [])

    def test_the_private_copy_is_made_where_scratch_dir_says(self) -> None:
        mine = self.tmp / "scratch"
        mine.mkdir(mode=0o700)
        seen: list[Any] = []
        real = tempfile.TemporaryDirectory

        def spy(*args: Any, **kw: Any):
            seen.append(kw.get("dir"))
            return real(*args, **kw)

        rh = load("rollback_helper_scratch", HELPERS / "rollback_helper.py")
        cfg = {"service_user": "audiobook", "service_group": "audiobook"}
        with contextlib.ExitStack() as stack:
            stack.enter_context(
                unittest.mock.patch.object(IH, "scratch_dir", return_value=str(mine))
            )
            stack.enter_context(unittest.mock.patch.object(IH.tempfile, "TemporaryDirectory", spy))
            IH.checked_host_conf(rh, cfg, GOOD.encode(), str(self.src))
        self.assertEqual(seen, [str(mine)])

    def test_as_root_the_private_copy_goes_in_the_root_made_export(self) -> None:
        with unittest.mock.patch.object(IH.os, "geteuid", return_value=0):
            self.assertEqual(IH.scratch_dir(), str(IH.REPO))
        with unittest.mock.patch.object(IH.os, "geteuid", return_value=1000):
            self.assertIsNone(IH.scratch_dir())

    # -- the running service ----------------------------------------------------

    def test_a_changed_resource_setting_is_called_out_before_it_is_applied(self) -> None:
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        self.dest.write_text(GOOD.replace("MemoryMax=30G", "MemoryMax=12G"))
        self.dest.chmod(0o644)
        p = self.run_tool()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("applies resource-control settings to the RUNNING service", p.stdout)
        self.assertIn("This changes: MemoryMax.", p.stdout)

    def test_a_new_file_that_sets_one_is_called_out_too(self) -> None:
        p = self.run_tool()
        self.assertIn("This changes: MemoryMax.", p.stdout)

    def test_a_removed_resource_setting_is_called_out(self) -> None:
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        self.dest.write_text(GOOD + "TasksMax=100\n")
        self.dest.chmod(0o644)
        p = self.run_tool()
        self.assertIn("This changes: TasksMax.", p.stdout)

    def test_a_change_that_waits_for_a_restart_is_not_called_out(self) -> None:
        self.unit_dir.mkdir()
        self.unit_dir.chmod(0o755)
        self.dest.write_text(GOOD.replace("TimeoutStopSec=90", "TimeoutStopSec=120"))
        self.dest.chmod(0o644)
        p = self.run_tool()
        self.assertIn("replace", p.stdout)
        self.assertNotIn("This changes", p.stdout)

    def test_an_unchanged_file_is_not_called_out(self) -> None:
        self.assertEqual(self.run_tool("--apply").returncode, 0)
        p = self.run_tool("--apply")
        self.assertNotIn("This changes", p.stdout)

    def test_what_the_apply_says_about_the_running_service_is_true(self) -> None:
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("was not restarted", p.stdout)
        self.assertIn("ExecStart=, WorkingDirectory= and TimeoutStopSec= take effect", p.stdout)
        self.assertIn("resource-control settings", p.stdout)
        self.assertNotIn("keeps its old settings", p.stdout)

    # -- the installed bundle judges host.conf at the next deploy ---------------

    def install_bundle_copy(self, data: bytes) -> None:
        lib = self.root / RH_LIBDIR.lstrip("/")
        lib.mkdir(parents=True)
        (lib / "rollback_helper.py").write_bytes(data)

    def test_a_missing_installed_bundle_is_noticed(self) -> None:
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("installed helper bundle is missing", p.stdout)
        self.assertIn("make install-rollback-helpers", p.stdout)

    def test_an_installed_bundle_that_differs_is_noticed(self) -> None:
        self.install_bundle_copy(b"# an older helper\n")
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("installed helper bundle differs from this checkout", p.stdout)
        self.assertIn("the next deploy judges host.conf with the installed one", p.stdout)

    def test_an_installed_bundle_that_matches_is_not_noticed(self) -> None:
        self.install_bundle_copy((HELPERS / "rollback_helper.py").read_bytes())
        p = self.run_tool("--apply")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("installed helper bundle", p.stdout)

    # -- the make target --------------------------------------------------------

    def make(self, *args: str, dry: bool = True, source: Path | str | None = None):
        if shutil.which("make") is None:
            self.skipTest("make is not installed")
        return subprocess.run(
            [
                "make",
                *(["-n"] if dry else []),
                "install-host-conf",
                f"HOST_CONF={self.src if source is None else source}",
                *args,
            ],
            cwd=REPO,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def make_n(self, *args: str) -> str:
        p = self.make(*args)
        self.assertEqual(p.returncode, 0, p.stderr)
        return p.stdout

    def test_make_dry_run_uses_no_sudo(self) -> None:
        out = self.make_n()
        self.assertIn("install_helpers.py", out)
        self.assertIn('--host-conf="$hc"', out)
        self.assertNotIn("--apply", out.split("else", 1)[1])
        self.assertNotIn("sudo", out.split("else", 1)[1])

    def test_make_apply_runs_a_root_made_export_with_sudo_inside_the_recipe(self) -> None:
        out = self.make_n("APPLY=1")
        for needed in (
            "sudo mktemp -d",
            "git -c tar.umask=0022 archive HEAD deploy/rollback-helpers",
            "sudo tar -x -f - --no-same-owner",
            'sudo python3 -I -B "$exp/deploy/rollback-helpers/install_helpers.py"',
            "--apply",
        ):
            self.assertIn(needed, out)
        self.assertNotIn("restart", out)
        self.assertNotIn("sudoers", out)

    def test_make_variables_are_never_spliced_into_the_shell_line(self) -> None:
        hostile = 'x.service" --systemctl="/tmp/evil'
        for args in (["APPLY=1"], []):
            out = self.make_n(
                *args,
                f"DEPLOY_UNIT={hostile}",
                "ROLLBACK_EXPORT_DIR=/opt; touch /tmp/pwned",
            )
            self.assertNotIn("--systemctl", out)
            self.assertNotIn("/tmp/evil", out)
            self.assertNotIn("touch", out)
            self.assertIn('--unit="$AORG_HC_UNIT"', out)

    def test_make_hands_a_hostile_unit_to_the_installer_as_one_refused_argument(self) -> None:
        p = self.make('DEPLOY_UNIT=x.service" --systemctl="/tmp/evil', dry=False, source=self.src)
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertIn("not a .service unit name", p.stderr)
        self.assertNotIn("test seam", p.stderr, "it was one argument, not two")

    def test_make_takes_a_path_with_spaces(self) -> None:
        p = self.make(dry=False, source=self.tmp / "no such dir" / "host conf")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn(f"{self.tmp}/no such dir/host conf does not exist", p.stdout + p.stderr)


if __name__ == "__main__":
    unittest.main()
