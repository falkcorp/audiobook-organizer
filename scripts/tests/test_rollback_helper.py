# file: scripts/tests/test_rollback_helper.py
# version: 1.6.0
# guid: 9609b377-57c2-454a-9130-320303c2815e
# last-edited: 2026-11-09

"""Tests for deploy/rollback-helpers/rollback_helper.py, the root-owned
helpers the rollback recipes run through sudo (R1-27).

The deploy user may pass these helpers any arguments (sudoers grants them
with no argument list), so each test hands a helper the injection forms a
hostile deploy user would try and asserts that it refuses them before it
runs anything, and that for good arguments it runs exactly the argv it
should. The module's fixed paths (CONFIG, TOOLS, SYSTEMD_DIR, RUN_DIR) are
pointed into a temporary directory; the tools are fake scripts that log
their argv.

Run with:
    python3 -m unittest discover -s scripts -p 'test_rollback_helper.py' -v
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
HELPER = REPO_ROOT / "deploy" / "rollback-helpers" / "rollback_helper.py"
NAME = "20261101T020304Z-0badc0de"
INJECTIONS = [
    "x; touch /tmp/pwned-r127",
    "$(touch /tmp/pwned-r127)",
    "../../etc/shadow",
    "-rf",
    "--config=/tmp/evil.yaml",
    "",
    "a" * 300,
    NAME + "\n",
    NAME + " ",
    " " + NAME,
    "a/b",
    ".hidden",
    "*",
]

_FAKE_TOOL = """#!{python}
import json, os, sys
with open({log!r}, "a") as f:
    f.write(json.dumps({{"tool": {name!r}, "argv": sys.argv[1:]}}) + "\\n")
{body}
"""

# A fake systemd-run: runs what follows `--` with the --setenv variables, its
# output appended to the StandardOutput= file, and exits with its status.
_SYSTEMD_RUN_BODY = """
args = sys.argv[1:]
cut = args.index("--")
opts, cmd = args[:cut], args[cut + 1:]
env = dict(os.environ)
out = None
for o in opts:
    if o.startswith("--setenv="):
        k, _, v = o[len("--setenv="):].partition("=")
        env[k] = v
    if o.startswith("--property=StandardOutput=append:"):
        out = o.split("append:", 1)[1]
import subprocess
with open(out, "ab") as f:
    rc = subprocess.run(cmd, env=env, stdout=f, stderr=f).returncode
sys.exit(rc)
"""


# A fake systemctl: `show -p ActiveState --value` prints the transient
# unit's state from a file (absent: inactive, as systemd answers for a unit
# that is not loaded); everything else succeeds.
_SYSTEMCTL_BODY = """
if "ActiveState" in sys.argv:
    try:
        print(open({state!r}).read().strip())
    except FileNotFoundError:
        print("inactive")
sys.exit(0)
"""


def load():
    spec = importlib.util.spec_from_file_location("rollback_helper", HELPER)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class Host:
    """A temporary host: config, store, binaries, fake tools."""

    def __init__(self, tc: unittest.TestCase, **config):
        self._tmp = tempfile.TemporaryDirectory()
        tc.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()
        self.log = self.root / "tools.log"
        self.db = self.root / "data" / "audiobooks.pebble"
        self.db.mkdir(parents=True)
        self.state = self.root / "state"
        self.state.mkdir()
        self.archives = self.root / "archives"
        self.archives.mkdir()
        self.bin = self.root / "bin" / "audiobook-organizer"
        self.bin.parent.mkdir()
        self.cp_area = self.db.parent / ".mediacore-checkpoints"
        self.cp_area.mkdir()
        self.etc = self.root / "etc"
        self.etc.mkdir(mode=0o755)
        self.run_dir = self.root / "run"
        self.systemd = self.root / "systemd"
        self.mod = load()
        self.mod.CONFIG = str(self.etc / "rollback.conf")
        self.mod.SYSTEMD_DIR = str(self.systemd)
        self.mod.RUN_DIR = str(self.run_dir)
        self.mod.TOOLS = {
            "find": self.tool(
                "find",
                "import subprocess; sys.exit(subprocess.run(['/usr/bin/find', *sys.argv[1:]]).returncode)",
            ),
            "systemctl": self.tool(
                "systemctl", _SYSTEMCTL_BODY.format(state=str(self.root / "transient-state"))
            ),
            "systemd-run": self.tool("systemd-run", _SYSTEMD_RUN_BODY),
            "python3": sys.executable,
        }
        values = {
            "bin": str(self.bin),
            "db": str(self.db),
            "state_dir": str(self.state),
            "unit": "audiobook-organizer.service",
            "service_user": "alfred",
            "archive_dir": str(self.archives),
            "offline_env": f"HOME={self.state} ABK_STATE_DIR={self.state}",
        }
        values.update(config)
        self.write_config(values)

    def tool(self, name: str, body: str) -> str:
        p = self.root / "tools" / name
        p.parent.mkdir(exist_ok=True)
        p.write_text(
            _FAKE_TOOL.format(python=sys.executable, log=str(self.log), name=name, body=body)
        )
        p.chmod(0o755)
        return str(p)

    def write_config(self, values: dict, mode: int = 0o644) -> None:
        text = "".join(f"{k} = {v}\n" for k, v in values.items() if v is not None)
        p = Path(self.mod.CONFIG)
        p.write_text("# rollback helpers\n" + text)
        p.chmod(mode)

    def tools(self) -> list[dict]:
        if not self.log.exists():
            return []
        return [json.loads(ln) for ln in self.log.read_text().splitlines() if ln.strip()]

    def run(self, *argv: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = self.mod.main(list(argv))
        return code, out.getvalue(), err.getvalue()

    def prefix(self) -> list[str]:
        log = f"{self.run_dir}/restore.log"
        return [
            "--wait",
            "--quiet",
            "--unit=audiobook-organizer-rollback-restore",
            "--service-type=exec",
            "--uid=alfred",
            f"--property=StandardOutput=append:{log}",
            f"--property=StandardError=append:{log}",
            f"--setenv=HOME={self.state}",
            f"--setenv=ABK_STATE_DIR={self.state}",
            "--",
            sys.executable,
            "-I",
            str(HELPER),
            "_report",
            "--",
        ]

    def transient_state(self, state: str) -> None:
        (self.root / "transient-state").write_text(state + "\n")

    def restore_log(self) -> Path:
        return self.run_dir / "restore.log"


class Config(unittest.TestCase):
    def test_good_config_loads(self):
        h = Host(self)
        code, _, err = h.run("probe")
        self.assertEqual(code, 0, err)

    def test_missing_config_refuses(self):
        h = Host(self)
        os.unlink(h.mod.CONFIG)
        code, _, err = h.run("probe")
        self.assertEqual(code, 1)
        self.assertIn("rollback.conf", err)
        self.assertEqual(h.tools(), [])

    def test_group_or_world_writable_config_refuses(self):
        for mode in (0o664, 0o646):
            h = Host(self)
            os.chmod(h.mod.CONFIG, mode)
            code, _, err = h.run("probe")
            self.assertEqual(code, 1, oct(mode))
            self.assertIn("writable", err)
            self.assertEqual(h.tools(), [])

    def test_writable_config_directory_refuses(self):
        h = Host(self)
        h.etc.chmod(0o777)
        self.addCleanup(h.etc.chmod, 0o755)
        code, _, err = h.run("probe")
        self.assertEqual(code, 1)
        self.assertIn("writable", err)

    def test_symlinked_config_refuses(self):
        h = Host(self)
        real = h.root / "elsewhere.conf"
        os.replace(h.mod.CONFIG, real)
        os.symlink(real, h.mod.CONFIG)
        code, _, _ = h.run("probe")
        self.assertEqual(code, 1)

    def test_bad_values_refuse(self):
        for key, value in (
            ("db", "/data/my db"),
            ("db", "relative/db"),
            ("db", "/data/../etc"),
            ("bin", "/usr/local/bin/x;id"),
            ("state_dir", "/state/$(id)"),
            ("service_user", "root"),
            ("service_user", "Alfred Pennyworth"),
            ("unit", "x.service; id"),
            ("unit", "audiobook-organizer"),
            ("unit", "batman:robin.service"),
            ("offline_env", "HOME=/x LD_PRELOAD=/tmp/evil.so"),
            ("offline_env", "HOME=/x;id"),
            ("archive_dir", "/srv/arch ives"),
        ):
            h = Host(self, **{key: value})
            code, _, err = h.run("probe")
            self.assertEqual(code, 1, (key, value, err))
            self.assertIn(key, err)
            self.assertEqual(h.tools(), [], (key, value))

    def test_unknown_or_duplicate_key_refuses(self):
        h = Host(self)
        with open(h.mod.CONFIG, "a") as f:
            f.write("exec_start_pre = /bin/sh\n")
        self.assertEqual(h.run("probe")[0], 1)
        h = Host(self)
        with open(h.mod.CONFIG, "a") as f:
            f.write(f"db = {h.root}/other\n")
        self.assertEqual(h.run("probe")[0], 1)

    def test_missing_required_key_refuses(self):
        h = Host(self, archive_dir=None)
        code, _, err = h.run("probe")
        self.assertEqual(code, 1)
        self.assertIn("archive_dir", err)


class Probe(unittest.TestCase):
    def test_runs_the_literal_probe(self):
        h = Host(self)
        code, _, err = h.run("probe")
        self.assertEqual(code, 0, err)
        sd = str(h.state)
        self.assertEqual(
            h.tools(),
            [
                {
                    "tool": "find",
                    "argv": [
                        sd,
                        "-maxdepth",
                        "3",
                        "-path",
                        f"{sd}/restore/*/inflight.json",
                        "-print",
                    ],
                }
            ],
        )

    def test_reports_inflight(self):
        h = Host(self)
        p = h.state / "restore" / "20261109T101010Z-0a1b2c3d" / "inflight.json"
        p.parent.mkdir(parents=True)
        p.write_text("{}")
        # find writes to the real stdout (fd 1), not sys.stdout: run it as a process
        import subprocess

        proc = subprocess.run(
            [
                sys.executable,
                "-c",
                _RUN_MODULE,
                str(HELPER),
                h.mod.CONFIG,
                json.dumps(h.mod.TOOLS),
                "probe",
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), str(p))

    def test_takes_no_arguments(self):
        for extra in (["x"], ["-o", "-exec", "/bin/sh", ";"], [""], ["--"]):
            h = Host(self)
            code, _, err = h.run("probe", *extra)
            self.assertEqual(code, 2, extra)
            self.assertEqual(h.tools(), [], extra)


# Runs the helper's main in a child process with CONFIG and TOOLS replaced.
_RUN_MODULE = """
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("rollback_helper", sys.argv[1])
mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)
mod.CONFIG = sys.argv[2]; mod.TOOLS = json.loads(sys.argv[3])
sys.exit(mod.main(sys.argv[4:]))
"""


class Record(unittest.TestCase):
    def write_record(self, h: Host, doc) -> Path:
        p = h.cp_area / f"{NAME}.json"
        p.write_text(doc if isinstance(doc, str) else json.dumps(doc))
        return p

    def test_prints_the_record(self):
        h = Host(self)
        self.write_record(h, {"status": "completed", "from_vector": {"levels": {"books": 0}}})
        code, out, err = h.run("record", NAME)
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out)["status"], "completed")

    def test_checkpoint_dir_setting(self):
        h = Host(self)
        cp = h.root / "checkpoints"
        (cp / ".mediacore-checkpoints").mkdir(parents=True)
        h.write_config(
            {
                "bin": str(h.bin),
                "db": str(h.db),
                "state_dir": str(h.state),
                "unit": "audiobook-organizer.service",
                "service_user": "alfred",
                "archive_dir": str(h.archives),
                "checkpoint_dir": str(cp),
            }
        )
        (cp / ".mediacore-checkpoints" / f"{NAME}.json").write_text('{"status": "open"}')
        code, out, err = h.run("record", NAME)
        self.assertEqual(code, 0, err)
        self.assertIn("open", out)

    def test_injection_forms_are_refused(self):
        for bad in INJECTIONS:
            h = Host(self)
            code, out, _ = h.run("record", bad)
            self.assertEqual((code, out), (2, ""), repr(bad))
        h = Host(self)
        self.assertEqual(h.run("record")[0], 2)
        self.assertEqual(h.run("record", NAME, NAME)[0], 2)

    def test_symlinked_record_is_refused(self):
        h = Host(self)
        secret = h.root / "encryption.key"
        secret.write_text('{"key": "the batcave combination"}')
        os.symlink(secret, h.cp_area / f"{NAME}.json")
        code, out, _ = h.run("record", NAME)
        self.assertEqual(code, 1)
        self.assertNotIn("batcave", out)

    def test_not_json_or_too_large_is_refused(self):
        h = Host(self)
        self.write_record(h, "not json")
        self.assertEqual(h.run("record", NAME)[0], 1)
        h = Host(self)
        self.write_record(h, json.dumps({"pad": "x" * (2 << 20)}))
        self.assertEqual(h.run("record", NAME)[0], 1)

    def test_missing_record_says_so(self):
        h = Host(self)
        code, _, err = h.run("record", NAME)
        self.assertEqual(code, 1)
        self.assertIn("No such file", err)


class Restore(unittest.TestCase):
    def fake_bin(self, h: Host, body: str) -> None:
        h.bin.write_text(f"#!{sys.executable}\nimport os, sys\nargs = sys.argv[1:]\n{body}\n")
        h.bin.chmod(0o755)

    def test_checkpoint_runs_the_exact_argv_as_the_service_user(self):
        h = Host(self)
        self.fake_bin(h, "print('restored', os.environ.get('ABK_STATE_DIR'))")
        code, out, err = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 0, err)
        runs = [t for t in h.tools() if t["tool"] == "systemd-run"]
        self.assertEqual(len(runs), 1)
        self.assertEqual(
            runs[0]["argv"],
            h.prefix()
            + [
                str(h.bin),
                "storage",
                "restore-offline",
                "--db",
                str(h.db),
                "--checkpoint",
                NAME,
                "--accept-loss",
            ],
        )
        self.assertIn(f"restored {h.state}", out, "the restore's output is relayed")

    def test_archive_resolves_inside_the_archive_dir(self):
        h = Host(self)
        self.fake_bin(h, "sys.exit(0)")
        code, _, err = h.run("restore", "archive", "batcave-20261101.tar.gz")
        self.assertEqual(code, 0, err)
        argv = [t for t in h.tools() if t["tool"] == "systemd-run"][0]["argv"]
        self.assertEqual(
            argv[len(h.prefix()) :],
            [
                str(h.bin),
                "storage",
                "restore-offline",
                "--db",
                str(h.db),
                "--archive",
                f"{h.archives}/batcave-20261101.tar.gz",
                "--accept-loss",
            ],
        )

    def test_restore_failure_passes_the_status_through(self):
        h = Host(self)
        self.fake_bin(
            h, "sys.stderr.write('Error: a service is running or starting\\n'); sys.exit(1)"
        )
        code, out, _ = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 1)
        self.assertIn("running or starting", out)

    def test_injection_forms_are_refused(self):
        cases = [["checkpoint", bad] for bad in INJECTIONS]
        cases += [["archive", bad] for bad in INJECTIONS]
        cases += [["into-check", bad] for bad in INJECTIONS]
        cases += [
            [],
            ["checkpoint"],
            ["checkpoint", NAME, "--config=/tmp/evil.yaml"],
            ["checkpoint", NAME, "--db", "/other"],
            ["archive", "x.tar.gz", "--into", "/tmp/x"],
            ["Checkpoint", NAME],
            ["--checkpoint", NAME],
            ["inplace", NAME],
        ]
        for argv in cases:
            h = Host(self)
            code, _, _ = h.run("restore", *argv)
            self.assertEqual(code, 2, argv)
            self.assertEqual(h.tools(), [], argv)

    def test_into_check_runs_the_worker_as_the_service_user(self):
        h = Host(self)
        code, _, _ = h.run("restore", "into-check", "batcave.tar.gz")
        argv = [t for t in h.tools() if t["tool"] == "systemd-run"][0]["argv"]
        self.assertEqual(
            argv[len(h.prefix()) :],
            [sys.executable, "-I", str(HELPER), "_into-worker", "batcave.tar.gz"],
        )


class TransientRun(unittest.TestCase):
    """The restore's transient unit: never two at once, the log appended and
    never truncated, readable by the deploy user without sudo, and the
    restore's exit status written into it by the unit itself (so it is there
    when the ssh session, and this helper with it, is gone)."""

    def fake_bin(self, h: Host, body: str) -> None:
        h.bin.write_text(f"#!{sys.executable}\nimport os, sys\n{body}\n")
        h.bin.chmod(0o755)

    def test_a_running_restore_is_refused_and_its_log_untouched(self):
        for state in ("active", "activating", "deactivating", "reloading", ""):
            h = Host(self)
            h.run_dir.mkdir()
            h.restore_log().write_text("=== robin's restore, still running ===\n")
            h.transient_state(state)
            code, _, err = h.run("restore", "checkpoint", NAME)
            self.assertEqual(code, 1, state)
            self.assertIn("audiobook-organizer-rollback-restore", err, state)
            self.assertEqual(
                h.restore_log().read_text(), "=== robin's restore, still running ===\n", state
            )
            self.assertEqual([t for t in h.tools() if t["tool"] == "systemd-run"], [], state)

    def test_a_failed_leftover_unit_is_reset_before_the_run(self):
        h = Host(self)
        h.transient_state("failed")
        self.fake_bin(h, "sys.exit(0)")
        code, _, err = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 0, err)
        calls = [t for t in h.tools() if t["tool"] in ("systemctl", "systemd-run")]
        resets = [i for i, t in enumerate(calls) if t["argv"][:1] == ["reset-failed"]]
        runs = [i for i, t in enumerate(calls) if t["tool"] == "systemd-run"]
        self.assertEqual(len(resets), 1)
        self.assertEqual(
            calls[resets[0]]["argv"], ["reset-failed", "audiobook-organizer-rollback-restore"]
        )
        self.assertLess(resets[0], runs[0])

    def test_an_inactive_unit_is_not_reset(self):
        h = Host(self)
        self.fake_bin(h, "sys.exit(0)")
        code, _, err = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 0, err)
        self.assertFalse(any(t["argv"][:1] == ["reset-failed"] for t in h.tools()))

    def test_the_log_is_appended_never_truncated(self):
        h = Host(self)
        h.run_dir.mkdir()
        h.restore_log().write_text("joker's earlier restore output\n")
        self.fake_bin(h, "print('restored the batcave')")
        code, out, err = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 0, err)
        text = h.restore_log().read_text()
        self.assertTrue(text.startswith("joker's earlier restore output\n"), text)
        self.assertIn("restored the batcave", text)
        self.assertIn("restored the batcave", out)
        self.assertNotIn("joker", out, "only this run's output is relayed")

    def test_each_run_starts_with_a_marker(self):
        h = Host(self)
        self.fake_bin(h, "print('restored')")
        h.run("restore", "checkpoint", NAME)
        first = h.restore_log().read_text().splitlines()[0]
        self.assertRegex(
            first, r"^=== audiobook-organizer-rollback-restore started \d{4}-\d\d-\d\dT\S+Z ===$"
        )

    def test_the_exit_status_is_written_into_the_log(self):
        for rc in (0, 3):
            h = Host(self)
            self.fake_bin(h, f"print('restoring'); sys.exit({rc})")
            code, _, _ = h.run("restore", "checkpoint", NAME)
            self.assertEqual(code, rc)
            last = h.restore_log().read_text().splitlines()[-1]
            self.assertEqual(last, f"{h.mod.RESTORE_DONE}exit {rc}")

    def test_the_unit_is_not_collected(self):
        h = Host(self)
        self.fake_bin(h, "sys.exit(0)")
        h.run("restore", "checkpoint", NAME)
        argv = [t for t in h.tools() if t["tool"] == "systemd-run"][0]["argv"]
        self.assertNotIn("--collect", argv, "a failed unit keeps its Result for systemctl status")

    def test_the_run_dir_and_log_are_readable_by_the_deploy_users_group(self):
        gid = os.getgroups()[-1]
        h = Host(self)
        h.run_dir.mkdir(mode=0o700)
        h.restore_log().write_text("")
        h.restore_log().chmod(0o600)
        self.fake_bin(h, "sys.exit(0)")
        with unittest.mock.patch.dict(os.environ, {"SUDO_GID": str(gid)}):
            code, _, err = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 0, err)
        d, f = os.stat(h.run_dir), os.stat(h.restore_log())
        self.assertEqual(stat.S_IMODE(d.st_mode), 0o750)
        self.assertEqual(stat.S_IMODE(f.st_mode), 0o640)
        self.assertEqual((d.st_gid, f.st_gid), (gid, gid))

    def test_a_symlinked_log_is_refused(self):
        h = Host(self)
        h.run_dir.mkdir()
        target = h.root / "penguin.txt"
        target.write_text("untouched\n")
        h.restore_log().symlink_to(target)
        self.fake_bin(h, "sys.exit(0)")
        code, _, _ = h.run("restore", "checkpoint", NAME)
        self.assertEqual(code, 1)
        self.assertEqual(target.read_text(), "untouched\n")
        self.assertEqual([t for t in h.tools() if t["tool"] == "systemd-run"], [])


class Report(unittest.TestCase):
    """_report: the wrapper the transient unit runs as the service user."""

    def run_report(self, *cmd: str) -> tuple[int, str]:
        p = subprocess.run(
            [sys.executable, "-I", str(HELPER), "_report", "--", *cmd],
            capture_output=True,
            text=True,
            check=False,
        )
        return p.returncode, p.stdout

    def test_exit_status_is_passed_through_and_printed_last(self):
        mod = load()
        code, out = self.run_report(sys.executable, "-c", "print('alfred'); raise SystemExit(4)")
        self.assertEqual(code, 4)
        self.assertEqual(out.splitlines(), ["alfred", f"{mod.RESTORE_DONE}exit 4"])

    def test_a_signal_is_named(self):
        mod = load()
        code, out = self.run_report(
            sys.executable, "-c", "import os, signal; os.kill(os.getpid(), signal.SIGKILL)"
        )
        self.assertEqual(code, 128 + 9)
        self.assertEqual(out.splitlines()[-1], f"{mod.RESTORE_DONE}signal 9")

    def test_a_command_that_cannot_start_still_ends_the_log(self):
        mod = load()
        code, out = self.run_report("/nonexistent/arkham-asylum")
        self.assertEqual(code, 127)
        self.assertEqual(out.splitlines()[-1], f"{mod.RESTORE_DONE}exit 127 (could not start)")

    def test_usage(self):
        for argv in ([], ["--"], ["true"], ["true", "--"]):
            p = subprocess.run(
                [sys.executable, "-I", str(HELPER), "_report", *argv],
                capture_output=True,
                check=False,
            )
            self.assertEqual(p.returncode, 2, argv)


class IntoWorker(unittest.TestCase):
    def setup_host(self, levels="books 1\ncore 1\n", exit_code=0):
        h = Host(self)
        (h.archives / "batcave.tar.gz").write_bytes(b"x" * 1000)
        h.bin.write_text(
            f"#!{sys.executable}\nimport os, sys\nargs = sys.argv[1:]\n"
            f"into = args[args.index('--into') + 1]\n"
            f"if {exit_code}: sys.exit({exit_code})\n"
            "os.makedirs(into); os.makedirs(into + '.state')\n"
            f"open(into + '.storage-levels', 'w').write({levels!r})\n"
        )
        h.bin.chmod(0o755)
        return h

    def test_prints_the_copys_levels_and_deletes_the_copy(self):
        h = self.setup_host()
        code, out, err = h.run("_into-worker", "batcave.tar.gz")
        self.assertEqual(code, 0, err)
        self.assertIn("LEVELS-BEGIN\nbooks 1\ncore 1\nLEVELS-END", out)
        scratch = h.cp_area / "scratch"
        self.assertEqual(sorted(p.name for p in scratch.iterdir()), [], "the copy holds secrets")

    def test_failure_deletes_the_copy_too(self):
        h = self.setup_host(exit_code=3)
        code, _, _ = h.run("_into-worker", "batcave.tar.gz")
        self.assertEqual(code, 1)
        scratch = h.cp_area / "scratch"
        self.assertFalse(scratch.exists() and any(scratch.iterdir()))

    def test_refuses_without_the_free_space_to_extract(self):
        h = self.setup_host()
        h.mod.free_bytes = lambda path: 10
        code, _, err = h.run("_into-worker", "batcave.tar.gz")
        self.assertEqual(code, 1)
        self.assertIn("free", err)
        self.assertFalse((h.cp_area / "scratch" / "rollback-restore-levels").exists())

    def test_injection_forms_are_refused(self):
        for bad in INJECTIONS:
            h = self.setup_host()
            self.assertEqual(h.run("_into-worker", bad)[0], 2, repr(bad))


class UnitType(unittest.TestCase):
    def dropin(self, h: Host) -> Path:
        return h.systemd / "audiobook-organizer.service.d" / "zz-rollback-type-simple.conf"

    def test_simple_writes_the_fixed_dropin_and_reloads(self):
        h = Host(self)
        code, _, err = h.run("unit-type", "simple")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.dropin(h).read_text(), "[Service]\nType=simple\n")
        self.assertEqual([t["argv"] for t in h.tools()], [["daemon-reload"]])

    def test_notify_removes_it_and_reloads(self):
        h = Host(self)
        h.run("unit-type", "simple")
        code, _, err = h.run("unit-type", "notify")
        self.assertEqual(code, 0, err)
        self.assertFalse(self.dropin(h).exists())
        self.assertEqual(h.run("unit-type", "notify")[0], 0, "already absent is fine")

    def test_anything_else_is_refused(self):
        for argv in ([], ["Simple"], ["simple\n"], ["simple", "User=root"], ["exec"], ["../x"]):
            h = Host(self)
            code, _, _ = h.run("unit-type", *argv)
            self.assertEqual(code, 2, argv)
            self.assertEqual(h.tools(), [], argv)
            self.assertFalse(h.systemd.exists(), argv)


class SwapPrev(unittest.TestCase):
    def setup_host(self) -> Host:
        h = Host(self)
        h.bin.write_text("current")
        h.bin.chmod(0o755)
        prev = Path(f"{h.bin}.prev")
        prev.write_text("prev")
        prev.chmod(0o755)
        return h

    def test_installs_prev_atomically_and_keeps_the_current_binary(self):
        h = self.setup_host()
        code, _, err = h.run("swap-prev")
        self.assertEqual(code, 0, err)
        self.assertEqual(h.bin.read_text(), "prev")
        self.assertEqual(Path(f"{h.bin}.rolled-back").read_text(), "current")
        self.assertTrue(os.stat(h.bin).st_mode & stat.S_IXUSR)
        self.assertFalse(Path(f"{h.bin}.new").exists())

    def test_the_running_binarys_inode_is_never_written(self):
        """A rename over the old binary, never a write into it: the running
        process's open file (its text) still reads the old content."""
        h = self.setup_host()
        before = os.stat(h.bin).st_ino
        fd = os.open(h.bin, os.O_RDONLY)
        self.addCleanup(os.close, fd)
        code, _, err = h.run("swap-prev")
        self.assertEqual(code, 0, err)
        self.assertEqual(os.pread(fd, 64, 0), b"current")
        self.assertNotEqual(os.stat(h.bin).st_ino, before)
        self.assertEqual(h.bin.read_text(), "prev")

    def test_without_prev_nothing_changes(self):
        h = self.setup_host()
        os.unlink(f"{h.bin}.prev")
        code, _, err = h.run("swap-prev")
        self.assertEqual(code, 1)
        self.assertIn(".prev", err)
        self.assertEqual(h.bin.read_text(), "current")

    def test_symlinked_prev_is_refused(self):
        h = self.setup_host()
        os.unlink(f"{h.bin}.prev")
        target = h.root / "joker-binary"
        target.write_text("joker")
        target.chmod(0o755)
        os.symlink(target, f"{h.bin}.prev")
        self.assertEqual(h.run("swap-prev")[0], 1)
        self.assertEqual(h.bin.read_text(), "current")
        self.assertFalse(Path(f"{h.bin}.rolled-back").exists())

    def test_takes_no_arguments(self):
        for extra in (["x"], ["/bin/sh"], [""]):
            h = self.setup_host()
            self.assertEqual(h.run("swap-prev", *extra)[0], 2, extra)
            self.assertEqual(h.bin.read_text(), "current")


# A local.conf shaped like a real host's, Environment= lines only: a JSON
# value in single quotes, a double-quoted value with spaces, comments with
# an em dash. Values are made up.
REAL_SHAPED_LOCAL_CONF = """# Wayne Manor — the Batcave's environment
[Service]
Environment='WHISPER_ENDPOINTS=[{"url":"http://127.0.0.1:19848","priority":50}]'
Environment=OWNER_EMAIL=alfred@wayne-manor.invalid
Environment="SMTP_FROM=Alfred Pennyworth <alfred@wayne-manor.invalid>"
  # an indented comment
Environment=HOME=/srv/batcave/.appdata
"""

RENDERED_REAL_SHAPED = (
    "[Service]\n"
    'Environment=\'WHISPER_ENDPOINTS=[{"url":"http://127.0.0.1:19848","priority":50}]\'\n'
    "Environment=OWNER_EMAIL=alfred@wayne-manor.invalid\n"
    'Environment="SMTP_FROM=Alfred Pennyworth <alfred@wayne-manor.invalid>"\n'
    "Environment=HOME=/srv/batcave/.appdata\n"
)

# Each of these lines in a deployed local.conf is refused: only [Service]
# and one-assignment Environment= lines are deployed. The non-Environment
# ones run, write or grant something as root, or change how the service
# runs, and belong in host.conf, which the owner installs as root.
REFUSED_LINES = [
    "User=root",
    "User=",
    "Group=root",
    "ExecStart=",
    "ExecStart=/usr/local/bin/audiobook-organizer serve",
    "ExecStartPre=/bin/sh -c 'id'",
    "StandardOutput=file:/etc/sudoers.d/batman",
    "AmbientCapabilities=CAP_SYS_ADMIN",
    "SupplementaryGroups=root",
    "EnvironmentFile=/etc/shadow",
    "WorkingDirectory=/srv/batcave",
    "MemoryMax=30G",
    "TimeoutStopSec=90",
    "[Unit]",
    "[Install]",
    "[Service ]",
    "Environment",
    "Environment =A=1",
    "environment=A=1",
    "Environment=A=1 B=2",
    "Environment=1A=1",
    "Environment=A-B=1",
    "Environment=A",
    'Environment="A=1',
    "Environment='A=1\"",
    'Environment="A=1" "B=2"',
    "Environment=A=1\\",
    "Environment=A=\\x",
    'Environment="A=a\\"b"',
    ".include /etc/evil.conf",
    "not a directive",
]


def committed_unit(user: str = "alfred") -> str:
    text = (REPO_ROOT / "deploy" / "audiobook-organizer.service").read_text()
    return text.replace("User=audiobook", f"User={user}").replace(
        "Group=audiobook", f"Group={user}"
    )


# The owner's root-written host.conf: the ExecStart= override and the
# host's other settings, which local.conf no longer carries.
HOST_CONF = (
    "# host settings, written by the owner as root\n"
    "[Service]\n"
    "ExecStart=\n"
    "ExecStart=/usr/local/bin/audiobook-organizer serve --host 0.0.0.0 --port 8484 \\\n"
    "      --db /srv/batcave/audiobooks.pebble \\\n"
    "      --tls-cert /srv/batcave/server.crt --tls-key /srv/batcave/server.key\n"
    "WorkingDirectory=/srv/batcave\n"
    "MemoryMax=12G\n"
    "TimeoutStopSec=60\n"
    "User=alfred\n"
)


class InstallStaged(unittest.TestCase):
    """install-staged installs the deploy's binary, the unit and local.conf.

    systemd reads the unit and its drop-ins as root, so the helper accepts
    no free-form content for them: the unit is the copy of
    deploy/audiobook-organizer.service the owner installed root-owned in
    LIBDIR with the helpers, and local.conf is rendered by the helper from
    the staged file's Environment= lines, each checked against one strict
    grammar, so systemd only ever reads lines the helper wrote."""

    def host(self, **extra) -> Host:
        h = Host(self)
        self.stage = h.root / "home"
        self.stage.mkdir()
        self.libdir = h.root / "libexec"
        self.libdir.mkdir()
        h.mod.LIBDIR = str(self.libdir)
        values = {
            "bin": "/usr/local/bin/audiobook-organizer",
            "db": str(h.db),
            "state_dir": str(h.state),
            "unit": "audiobook-organizer.service",
            "service_user": "alfred",
            "archive_dir": str(h.archives),
            "stage_bin": str(self.stage / "audiobook-organizer"),
            "stage_local_conf": str(self.stage / "audiobook-organizer-local.conf"),
        }
        values.update(extra)
        h.write_config(values)
        self.host_conf_path = h.systemd / f"{values['unit']}.d" / "host.conf"
        self.host_conf_path.parent.mkdir(parents=True)
        self.host_conf_path.write_text(HOST_CONF)
        self.host_conf_path.chmod(0o644)
        return h

    def template(self, text: str | None = None, mode: int = 0o644) -> Path:
        p = self.libdir / "audiobook-organizer.service"
        p.write_text(committed_unit() if text is None else text)
        p.chmod(mode)
        return p

    def unit_path(self, h: Host) -> Path:
        return h.systemd / "audiobook-organizer.service"

    def conf_path(self, h: Host) -> Path:
        return h.systemd / "audiobook-organizer.service.d" / "local.conf"

    def stage_conf(self, text: str) -> None:
        (self.stage / "audiobook-organizer-local.conf").write_text(text)

    # -- the unit: the owner's root-owned copy, nothing staged --------------

    def test_the_unit_is_the_owners_template_and_reloads(self):
        h = self.host()
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.unit_path(h).read_text(), committed_unit())
        self.assertEqual(stat.S_IMODE(os.stat(self.unit_path(h)).st_mode), 0o644)
        self.assertEqual([t["argv"] for t in h.tools()], [["daemon-reload"]])

    def test_the_committed_unit_installs_verbatim_for_its_own_user(self):
        h = self.host(service_user="audiobook")
        text = (REPO_ROOT / "deploy" / "audiobook-organizer.service").read_text()
        self.template(text)
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.assertEqual(self.unit_path(h).read_text(), text)

    def test_a_staged_unit_is_never_read(self):
        h = self.host()
        self.template()
        (self.stage / "audiobook-organizer.service").write_text("[Service]\nUser=root\n")
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.assertNotIn("User=root", self.unit_path(h).read_text())

    def test_stage_unit_is_no_longer_a_setting(self):
        h = self.host(stage_unit="/srv/batcave/x.service")
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 1)
        self.assertIn("stage_unit", err)

    def test_the_template_must_be_owner_installed(self):
        for mode in (0o664, 0o646):
            with self.subTest(mode=oct(mode)):
                h = self.host()
                self.template(mode=mode)
                self.assertEqual(h.run("install", "unit")[0], 1)
                self.assertFalse(self.unit_path(h).exists())
        h = self.host()
        real = h.root / "real.service"
        real.write_text(committed_unit())
        os.symlink(real, self.libdir / "audiobook-organizer.service")
        self.assertEqual(h.run("install", "unit")[0], 1)
        h = self.host()
        self.assertEqual(h.run("install", "unit")[0], 1, "no template installed")

    def test_the_template_must_match_the_config(self):
        for text in (
            committed_unit("batman"),
            committed_unit().replace("Group=alfred", "Group=batman"),
            committed_unit().replace(
                "ExecStart=/usr/local/bin/audiobook-organizer ", "ExecStart=/opt/x "
            ),
            committed_unit().replace("User=alfred\n", ""),
        ):
            with self.subTest(text=text[-80:]):
                h = self.host()
                self.template(text)
                code, _, err = h.run("install", "unit")
                self.assertEqual(code, 1, err)
                self.assertFalse(self.unit_path(h).exists())

    # -- local.conf: rendered from Environment= lines ------------------------

    def test_local_conf_is_rendered_from_its_environment_lines(self):
        h = self.host()
        self.stage_conf(REAL_SHAPED_LOCAL_CONF)
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        out = self.conf_path(h).read_text()
        self.assertTrue(out.endswith(RENDERED_REAL_SHAPED), out)
        body = [ln for ln in out.splitlines() if ln and not ln.startswith("#")]
        self.assertEqual(body, RENDERED_REAL_SHAPED.splitlines())
        self.assertTrue(out.isascii())
        self.assertEqual([t["argv"] for t in h.tools()], [["daemon-reload"]])

    def test_the_committed_local_conf_example_renders(self):
        h = self.host()
        self.stage_conf((REPO_ROOT / "deploy" / "local.conf.example").read_text())
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        self.assertIn("Environment=", self.conf_path(h).read_text())

    def test_every_other_line_is_refused(self):
        for line in REFUSED_LINES:
            with self.subTest(line=line):
                h = self.host()
                self.stage_conf(f"[Service]\nEnvironment=A=1\n{line}\n")
                code, _, err = h.run("install", "local-conf")
                self.assertEqual(code, 1, (line, err))
                self.assertIn("line 3", err)
                self.assertFalse(self.conf_path(h).exists(), line)
                self.assertEqual(h.tools(), [], line)

    def test_environment_before_service_is_refused(self):
        h = self.host()
        self.stage_conf("Environment=A=1\n[Service]\n")
        self.assertEqual(h.run("install", "local-conf")[0], 1)

    def test_character_classes_outside_comments_are_refused(self):
        """Python's str.strip() and splitlines() take these for whitespace
        or line breaks and systemd does not; outside a comment only printable
        ASCII and tab pass, and the rendered file is all ASCII."""
        for ch in (
            "\v",
            "\f",
            "\x1c",
            "\x1d",
            "\x1e",
            "\x1f",
            "\x85",
            " ",
            " ",
            "\xa0",
            "\x7f",
            "\x1b",
            "\x01",
            "é",
            "​",
        ):
            for text in (
                f"[Service]\nEnvironment=A=1{ch}\n",
                f"[Service]\nEnvironment=A=1\n{ch}User=root\n",
                f"[Service]{ch}\nEnvironment=A=1\n",
            ):
                with self.subTest(ch=repr(ch), text=repr(text)):
                    h = self.host()
                    self.stage_conf(text)
                    code, _, err = h.run("install", "local-conf")
                    self.assertEqual(code, 1, (repr(ch), err))
                    self.assertFalse(self.conf_path(h).exists())

    def test_cr_and_nul_are_refused_anywhere(self):
        for text in (
            "[Service]\r\nEnvironment=A=1\r\n",
            "# x\rUser=root\n[Service]\n",
            "# \x00\n[Service]\n",
        ):
            with self.subTest(text=repr(text)):
                h = self.host()
                self.stage_conf(text)
                self.assertEqual(h.run("install", "local-conf")[0], 1)

    def test_a_comment_with_a_trailing_backslash_is_dropped_not_joined(self):
        """Comments are dropped, and only lines the helper writes reach
        systemd, so a comment can hide nothing."""
        h = self.host()
        self.stage_conf("[Service]\n# x \\\nEnvironment=A=1\n")
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        self.assertIn("Environment=A=1\n", self.conf_path(h).read_text())

    def test_no_refusal_echoes_the_file(self):
        h = self.host()
        self.stage_conf("[Service]\nEnvironment=A=1\nhunter2=x\nBatSignal=hunter2\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1)
        self.assertNotIn("hunter2", err)
        self.assertNotIn("BatSignal", err)

    def test_a_short_read_still_installs_the_whole_file(self):
        """read(2) may return less than asked (a network-mounted home):
        the helper reads to the end, never installing a truncated drop-in."""
        h = self.host()
        self.stage_conf(REAL_SHAPED_LOCAL_CONF)
        real = os.read
        with unittest.mock.patch.object(h.mod.os, "read", lambda fd, n: real(fd, min(n, 7))):
            code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        self.assertTrue(self.conf_path(h).read_text().endswith(RENDERED_REAL_SHAPED))

    # -- the staged files: who owns them and how they are reached ------------

    def test_a_symlinked_oversize_or_missing_source_is_refused(self):
        h = self.host()
        conf = self.stage / "audiobook-organizer-local.conf"
        os.symlink("/etc/hosts", conf)
        self.assertEqual(h.run("install", "local-conf")[0], 1)
        conf.unlink()
        conf.write_text("[Service]\n" + "Environment=A=" + "x" * (300 << 10) + "\n")
        self.assertEqual(h.run("install", "local-conf")[0], 1)
        conf.unlink()
        self.assertEqual(h.run("install", "local-conf")[0], 1)
        self.assertFalse(self.conf_path(h).exists())

    def test_a_symlink_anywhere_in_the_stage_path_is_refused(self):
        """O_NOFOLLOW guards only the last component: each directory on the
        way is opened with O_NOFOLLOW too, so a staging directory swapped
        for a link to another directory is refused."""
        for what, name, body in (
            ("local-conf", "audiobook-organizer-local.conf", "[Service]\nEnvironment=A=1\n"),
            ("binary", "audiobook-organizer", "new"),
        ):
            with self.subTest(what=what):
                h = self.host()
                real = h.root / "real-home"
                real.mkdir()
                (real / name).write_text(body)
                os.rmdir(self.stage)
                os.symlink(real, self.stage)
                code, _, err = h.run("install", what)
                self.assertEqual(code, 1, err)
                self.assertIn("symlink", err)

    def test_an_unset_stage_key_refuses(self):
        h = self.host(stage_local_conf=None)
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1)
        self.assertIn("stage_local_conf", err)

    def test_a_staged_file_with_another_link_or_owner_is_refused(self):
        """Root opens the staged file: a hard link (to /etc/shadow, say) is
        refused, and so is a file the deploy user (SUDO_UID) does not own."""
        h = self.host()
        conf = self.stage / "audiobook-organizer-local.conf"
        other = h.root / "elsewhere.conf"
        other.write_text("[Service]\nEnvironment=A=1\n")
        os.link(other, conf)
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1, err)
        self.assertIn("link", err)
        conf.unlink()
        conf.write_text("[Service]\nEnvironment=A=1\n")
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        with unittest.mock.patch.dict(os.environ, {"SUDO_UID": str(os.getuid() + 1)}):
            code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1, err)
        self.assertIn("own", err)

    def test_stage_paths_are_validated_like_the_others(self):
        for bad in ("relative/path", "/a/../etc/shadow", "/a b", "/x/"):
            with self.subTest(bad=bad):
                h = self.host(stage_local_conf=bad)
                self.assertEqual(h.run("install", "local-conf")[0], 1, bad)

    def test_service_group_root_is_refused(self):
        h = self.host(service_group="root")
        self.template()
        self.assertEqual(h.run("install", "unit")[0], 1)

    def test_a_service_group_other_than_the_user(self):
        h = self.host(service_group="batfamily")
        self.template(committed_unit().replace("Group=alfred", "Group=batfamily"))
        self.assertEqual(h.run("install", "unit")[0], 0)

    def test_the_committed_config_example_loads(self):
        h = Host(self)
        Path(h.mod.CONFIG).write_text(
            (REPO_ROOT / "deploy" / "rollback-helpers" / "rollback.conf.example").read_text()
        )
        cfg = h.mod.load_config()
        self.assertEqual(cfg["service_group"], cfg["service_user"])
        for key in ("stage_bin", "stage_local_conf"):
            self.assertTrue(cfg[key].startswith("/"), key)
        self.assertNotIn("stage_unit", cfg)

    # -- the binary -----------------------------------------------------------

    # -- host.conf: local.conf is deployed only beside the owner's host.conf

    def assert_local_conf_refused(self, h: Host, needle: str = "host.conf") -> str:
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1, err)
        self.assertIn(needle, err)
        self.assertFalse(self.conf_path(h).exists(), "nothing installed")
        self.assertEqual(h.tools(), [], "no daemon-reload")
        return err

    def test_local_conf_needs_host_conf(self):
        h = self.host()
        self.host_conf_path.unlink()
        err = self.assert_local_conf_refused(h)
        self.assertIn("ExecStart", err, "it says why: the base unit's ExecStart would run")

    def test_a_symlinked_host_conf_is_refused(self):
        h = self.host()
        real = h.root / "real-host.conf"
        real.write_text(HOST_CONF)
        self.host_conf_path.unlink()
        self.host_conf_path.symlink_to(real)
        self.assert_local_conf_refused(h)

    def test_a_host_conf_that_is_not_a_regular_file_is_refused(self):
        h = self.host()
        self.host_conf_path.unlink()
        self.host_conf_path.mkdir()
        self.assert_local_conf_refused(h)

    def test_a_group_or_world_writable_host_conf_is_refused(self):
        for mode in (0o664, 0o646, 0o666):
            with self.subTest(mode=oct(mode)):
                h = self.host()
                self.host_conf_path.chmod(mode)
                self.assert_local_conf_refused(h)

    def test_a_host_conf_not_owned_by_root_is_refused(self):
        h = self.host()
        ino = os.stat(self.host_conf_path).st_ino
        owner_ok = h.mod._owner_ok
        with unittest.mock.patch.object(
            h.mod, "_owner_ok", lambda st: st.st_ino != ino and owner_ok(st)
        ):
            self.assert_local_conf_refused(h)

    def test_a_good_host_conf_lets_local_conf_install(self):
        h = self.host()
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.host_conf_path.read_text(), HOST_CONF, "host.conf is never written")

    def test_host_conf_may_not_run_anything_as_root(self):
        """The LD_PRELOAD trust argument: every Environment= line reaches
        only the service user. That holds only while host.conf runs nothing
        else and nothing as root, so the helper checks it."""
        for line in (
            "ExecStartPre=/usr/bin/true",
            "ExecStartPost=/usr/bin/true",
            "ExecStopPost=/usr/bin/true",
            "ExecStop=/usr/bin/true",
            "ExecReload=/usr/bin/true",
            "ExecCondition=/usr/bin/true",
            "ExecStart=+/usr/local/bin/audiobook-organizer serve",
            "ExecStart=!/usr/local/bin/audiobook-organizer serve",
            "ExecStart=!!/usr/local/bin/audiobook-organizer serve",
            "ExecStart=-+/usr/local/bin/audiobook-organizer serve",
            "ExecStart=@:!/usr/local/bin/audiobook-organizer serve",
            "  ExecStartPre = /usr/bin/true",
            "PermissionsStartOnly=true",
            "User=root",
            "Group=root",
            "User=joker",
            "AmbientCapabilities=CAP_SYS_ADMIN",
        ):
            with self.subTest(line=line):
                h = self.host()
                self.host_conf_path.write_text(HOST_CONF + line + "\n")
                err = self.assert_local_conf_refused(h, "line 11")
                self.assertNotIn("/usr/bin/true", err, "a refusal never echoes host.conf")
                self.assertNotIn("joker", err)

    def test_host_conf_text_inside_a_continuation_is_not_a_directive(self):
        h = self.host()
        self.host_conf_path.write_text(
            HOST_CONF + "ExecStart=\nExecStart=/usr/local/bin/audiobook-organizer serve \\\n"
            "      --label ExecStartPre=nothing\n"
        )
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def test_host_conf_comments_and_the_service_group_pass(self):
        h = self.host(service_group="batfamily")
        self.host_conf_path.write_text(
            HOST_CONF + "# ExecStartPre= is not allowed here \u2014 comment only\n"
            "; User=root (a comment)\nGroup=batfamily\n"
        )
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def test_host_conf_control_characters_are_refused(self):
        for text in (
            HOST_CONF + "ExecStartPre\x0b=/usr/bin/true\n",
            HOST_CONF + "User=alfred\x1c\n",
            HOST_CONF + "MemoryMax=12G\r\n",
            HOST_CONF + "MemoryMax=12G\x00\n",
        ):
            with self.subTest(text=repr(text[-30:])):
                h = self.host()
                self.host_conf_path.write_bytes(text.encode())
                self.assert_local_conf_refused(h)

    def test_the_committed_example_host_conf_block_passes(self):
        """deploy/local.conf.example shows host.conf in comments; with the
        comment marks removed it must pass the helper's check."""
        text = (REPO_ROOT / "deploy" / "local.conf.example").read_text()
        block = text.split("# --- host.conf, NOT this file", 1)[1].split("# -----", 1)[0]
        lines = block.splitlines()
        start = next(i for i, ln in enumerate(lines) if ln == "# [Service]")
        body = "".join(ln[2:] + "\n" if ln.startswith("# ") else "\n" for ln in lines[start:])
        h = self.host(service_user="audiobook")
        self.host_conf_path.write_text(body)
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def bin_host(self) -> tuple[Host, Path]:
        bindir = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(shutil.rmtree, bindir, True)
        b = bindir / "audiobook-organizer"
        return self.host(bin=str(b)), b

    def test_binary_is_installed_atomically_and_the_old_one_kept_as_prev(self):
        h, b = self.bin_host()
        b.write_text("current")
        b.chmod(0o755)
        (self.stage / "audiobook-organizer").write_text("new")
        old = os.open(b, os.O_RDONLY)
        self.addCleanup(os.close, old)
        old_ino = os.fstat(old).st_ino
        code, _, err = h.run("install", "binary")
        self.assertEqual(code, 0, err)
        self.assertEqual(b.read_text(), "new")
        self.assertEqual(Path(f"{b}.prev").read_text(), "current")
        self.assertEqual(stat.S_IMODE(os.stat(b).st_mode), 0o755)
        self.assertFalse(Path(f"{b}.new").exists())
        # A rename, not an overwrite: the running executable's inode keeps
        # its old content (no ETXTBSY, no torn binary).
        self.assertNotEqual(os.stat(b).st_ino, old_ino)
        self.assertEqual(os.pread(old, 64, 0), b"current")
        self.assertEqual(h.tools(), [], "installing the binary reloads nothing")

    def test_first_binary_install_has_no_prev(self):
        h, b = self.bin_host()
        (self.stage / "audiobook-organizer").write_text("new")
        self.assertEqual(h.run("install", "binary")[0], 0)
        self.assertEqual(b.read_text(), "new")
        self.assertFalse(Path(f"{b}.prev").exists())

    def test_symlinked_staged_binary_is_refused(self):
        h, b = self.bin_host()
        b.write_text("current")
        target = h.root / "elsewhere-bin"
        target.write_text("evil")
        os.symlink(target, self.stage / "audiobook-organizer")
        self.assertEqual(h.run("install", "binary")[0], 1)
        self.assertEqual(b.read_text(), "current")

    def test_a_staged_binary_with_another_link_or_owner_is_refused(self):
        h, b = self.bin_host()
        b.write_text("current")
        staged = self.stage / "audiobook-organizer"
        other = h.root / "elsewhere"
        other.write_text("new")
        os.link(other, staged)
        self.assertEqual(h.run("install", "binary")[0], 1)
        self.assertEqual(b.read_text(), "current")
        staged.unlink()
        staged.write_text("new")
        with unittest.mock.patch.dict(os.environ, {"SUDO_UID": str(os.getuid() + 1)}):
            self.assertEqual(h.run("install", "binary")[0], 1)
        self.assertEqual(b.read_text(), "current")
        self.assertFalse(Path(f"{b}.prev").exists())

    def test_bad_arguments(self):
        for argv in (
            [],
            ["Unit"],
            ["unit", "x"],
            ["unit\n"],
            ["../unit"],
            ["binary", "--force"],
            *([a] for a in INJECTIONS),
        ):
            with self.subTest(argv=argv):
                h = self.host()
                self.assertEqual(h.run("install", *argv)[0], 2, argv)
                self.assertEqual(h.tools(), [], argv)
                written = sorted(p for p in h.systemd.rglob("*") if p.is_file())
                self.assertEqual(written, [self.host_conf_path], "only the owner's host.conf")


class Pprof(unittest.TestCase):
    def dropin(self, h: Host) -> Path:
        return h.systemd / "audiobook-organizer.service.d" / "pprof.conf"

    def test_on_writes_the_fixed_dropin_and_reloads(self):
        h = Host(self)
        code, _, err = h.run("pprof", "on")
        self.assertEqual(code, 0, err)
        self.assertEqual(
            self.dropin(h).read_text(), "[Service]\nEnvironment=ABK_PPROF_ADDR=localhost:6060\n"
        )
        self.assertEqual([t["argv"] for t in h.tools()], [["daemon-reload"]])

    def test_off_removes_it_and_reloads(self):
        h = Host(self)
        h.run("pprof", "on")
        code, _, err = h.run("pprof", "off")
        self.assertEqual(code, 0, err)
        self.assertFalse(self.dropin(h).exists())
        self.assertEqual(h.run("pprof", "off")[0], 0, "already absent is fine")

    def test_anything_else_is_refused(self):
        for argv in (
            [],
            ["On"],
            ["on\n"],
            ["on", "0.0.0.0:6060"],
            ["0.0.0.0:6060"],
            *([a] for a in INJECTIONS),
        ):
            with self.subTest(argv=argv):
                h = Host(self)
                self.assertEqual(h.run("pprof", *argv)[0], 2, argv)
                self.assertEqual(h.tools(), [], argv)
                self.assertFalse(h.systemd.exists(), argv)


class Stubs(unittest.TestCase):
    """Each sudoers-granted stub runs one fixed subcommand of the helper."""

    def test_each_stub_execs_its_one_subcommand(self):
        mod = load()
        stubs = HELPER.parent
        for name, sub in mod.STUBS.items():
            lines = (stubs / name).read_text().split("\n")
            # The whole file: a /bin/sh shebang, comments only, the one exec
            # line, and a final newline. Nothing else can run before it.
            self.assertEqual(lines[0], "#!/bin/sh", name)
            self.assertEqual(lines[-1], "", name)
            self.assertEqual(
                lines[-2],
                f'exec /usr/bin/python3 -I {mod.LIBDIR}/rollback_helper.py {sub} "$@"',
                name,
            )
            for ln in lines[1:-2]:
                self.assertTrue(ln == "#" or ln.startswith("# "), (name, ln))
            self.assertTrue(os.access(stubs / name, os.X_OK), name)


if __name__ == "__main__":
    unittest.main()
