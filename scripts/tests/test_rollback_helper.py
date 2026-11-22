# file: scripts/tests/test_rollback_helper.py
# version: 1.14.0
# guid: 9609b377-57c2-454a-9130-320303c2815e
# last-edited: 2026-11-18

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
import hashlib
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
HELPERS = REPO_ROOT / "deploy" / "rollback-helpers"
HELPER = HELPERS / "rollback_helper.py"
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
if "FragmentPath" in sys.argv:
    if os.path.exists({state!r} + ".show-fails"):
        sys.exit(1)
    try:
        print(open({state!r} + ".fragment-show").read().strip())
    except FileNotFoundError:
        print("FragmentPath=\\nSourcePath=")
if "Names" in sys.argv:
    if os.path.exists({state!r} + ".show-fails"):
        sys.exit(1)
    try:
        print(open({state!r} + ".names").read().strip())
    except FileNotFoundError:
        print("")
if "DropInPaths" in sys.argv:
    if os.path.exists({state!r} + ".show-fails"):
        sys.exit(1)
    try:
        print(open({state!r} + ".dropin-paths").read().strip())
    except FileNotFoundError:
        print("")
sys.exit(0)
"""


def load():
    spec = importlib.util.spec_from_file_location("rollback_helper", HELPER)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    # Registered before exec_module (the importlib recipe): dataclasses and
    # typing resolve a class's module through sys.modules, and Python 3.12
    # fails when it is absent. Each load replaces the entry with a fresh copy.
    sys.modules[spec.name] = mod
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
mod = importlib.util.module_from_spec(spec); sys.modules[spec.name] = mod
spec.loader.exec_module(mod)
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
        # The other unit directories systemd reads drop-ins from (/run,
        # /usr/lib, ...), inside the sandbox.
        self.unit_dirs = [
            h.root / "run-systemd",
            h.root / "usr-lib-systemd",
            h.root / "run-generator",
        ]
        h.mod.UNIT_DIRS = tuple(str(d) for d in self.unit_dirs)
        # Where a unit file may live; the generator directory is not one.
        h.mod.FRAGMENT_DIRS = tuple(str(d) for d in self.unit_dirs[:2])
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
        self.host_conf_path.write_text(
            HOST_CONF.replace("User=alfred", f"User={values['service_user']}")
        )
        self.host_conf_path.chmod(0o644)
        # The unit's main file, a package-style copy under /usr/lib, so the
        # /etc copy install-staged unit writes starts out absent.
        self.lib_unit = self.unit_dirs[1] / values["unit"]
        self.lib_unit.parent.mkdir(parents=True, exist_ok=True)
        user = values["service_user"]
        group = values.get("service_group") or user
        self.lib_unit.write_text(
            committed_unit(user).replace(f"Group={user}\n", f"Group={group}\n")
        )
        self.lib_unit.chmod(0o644)
        return h

    def changes(self, h: Host) -> list[list[str]]:
        """The tool calls that change something: every call but the
        read-only `systemctl show -p DropInPaths`."""
        return [t["argv"] for t in h.tools() if t["argv"][:1] != ["show"]]

    def dropin(self, h: Host, name: str, text: str, root: Path | None = None, d: str = "") -> Path:
        p = (root or h.systemd) / (d or "audiobook-organizer.service.d") / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        p.chmod(0o644)
        return p

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
        self.assertEqual(self.changes(h), [["daemon-reload"]])

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
        self.assertEqual(self.changes(h), [["daemon-reload"]])

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
                self.assertEqual(self.changes(h), [], line)

    def test_a_continued_environment_line_is_checked_as_systemd_joins_it(self):
        """A trailing backslash folds the next line into the value, so the
        joined line is what is checked and rendered: a folded User= makes it
        two words and refuses, and a value continued onto a blank line
        renders as the one line systemd reads."""
        h = self.host()
        self.stage_conf("[Service]\nEnvironment=A=1\\\nUser=root\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1, err)
        self.assertIn("line 2", err)
        self.assertFalse(self.conf_path(h).exists())
        h = self.host()
        self.stage_conf("[Service]\nEnvironment=A=1\\\n\nEnvironment=B=2\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        rendered = self.conf_path(h).read_text()
        self.assertIn("Environment=A=1\nEnvironment=B=2\n", rendered)
        self.assertNotIn("\\", rendered)

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

    def test_loader_and_runtime_injection_keys_are_refused(self):
        """Defence in depth on top of the drop-in rules: variables that make
        the dynamic loader, libc, an interpreter or the Go runtime load or
        run something else are refused, in every quoting form and any case."""
        keys = (
            "LD_PRELOAD",
            "LD_LIBRARY_PATH",
            "LD_AUDIT",
            "LD_DEBUG",
            "ld_preload",
            "GCONV_PATH",
            "GETCONF_DIR",
            "HOSTALIASES",
            "LOCALDOMAIN",
            "RES_OPTIONS",
            "MALLOC_CHECK_",
            "MALLOC_ARENA_MAX",
            "NLSPATH",
            "PYTHONPATH",
            "PYTHONSTARTUP",
            "PYTHONHOME",
            "PERL5LIB",
            "PERL5OPT",
            "NODE_OPTIONS",
            "GODEBUG",
            "TZDIR",
            "GLIBC_TUNABLES",
            "BASH_ENV",
            "SSL_CERT_FILE",
            "SSL_CERT_DIR",
            "LOCPATH",
        )
        for key in keys:
            for form in (f"{key}=/tmp/batcave", f'"{key}=/tmp/batcave"', f"'{key}=/tmp/batcave'"):
                with self.subTest(form=form):
                    h = self.host()
                    self.stage_conf(f"[Service]\nEnvironment=A=1\nEnvironment={form}\n")
                    code, _, err = h.run("install", "local-conf")
                    self.assertEqual(code, 1, err)
                    self.assertIn("line 3", err)
                    self.assertNotIn("/tmp/batcave", err)
                    self.assertFalse(self.conf_path(h).exists())

    def test_keys_that_only_look_like_injection_keys_pass(self):
        h = self.host()
        text = "[Service]\nEnvironment=LDAP_URL=x\nEnvironment=OLD_PYTHON=x\nEnvironment=TZ=UTC\nEnvironment=GOMAXPROCS=4\n"
        self.stage_conf(text)
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

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
        self.assertEqual(self.changes(h), [], "no daemon-reload")
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

    def test_the_unit_install_needs_a_good_host_conf_too(self):
        """The unit and local.conf share one host.conf check, run before
        anything is copied: a host with no host.conf (or a bad one) never
        gets the base unit installed, so it can never restart on the base
        ExecStart, whether or not the deploy has a local.conf."""
        breakers = {
            "missing": lambda p: p.unlink(),
            "symlink": lambda p: (
                p.unlink(),
                p.symlink_to(p.parent.parent / "audiobook-organizer.service"),
            ),
            "directory": lambda p: (p.unlink(), p.mkdir()),
            "group-writable": lambda p: p.chmod(0o664),
            "ExecStartPre": lambda p: p.write_text(HOST_CONF + "ExecStartPre=/usr/bin/true\n"),
            "+ExecStart": lambda p: p.write_text(
                HOST_CONF + "ExecStart=+/usr/local/bin/audiobook-organizer\n"
            ),
        }
        for name, breaker in breakers.items():
            with self.subTest(name=name):
                h = self.host()
                self.template()
                breaker(self.host_conf_path)
                code, _, err = h.run("install", "unit")
                self.assertEqual(code, 1, err)
                self.assertIn("host.conf", err)
                self.assertFalse(self.unit_path(h).exists(), "nothing copied")
                self.assertEqual(self.changes(h), [], "no daemon-reload")

    def test_the_unit_and_local_conf_share_one_host_conf_check(self):
        h = self.host()
        self.template()
        calls = []
        check = h.mod._check_host_conf
        with unittest.mock.patch.object(
            h.mod, "_check_host_conf", lambda cfg: (calls.append(cfg["unit"]), check(cfg))
        ):
            self.assertEqual(h.run("install", "unit")[0], 0)
            self.stage_conf("[Service]\nEnvironment=A=1\n")
            self.assertEqual(h.run("install", "local-conf")[0], 0)
        self.assertEqual(calls, ["audiobook-organizer.service"] * 2)

    # -- every drop-in systemd applies, not only host.conf ------------------

    def install_refused(self, h: Host, what: str) -> str:
        if what == "unit":
            self.template()
        else:
            self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", what)
        self.assertEqual(code, 1, err)
        self.assertNotIn(["daemon-reload"], self.changes(h))
        target = self.unit_path(h) if what == "unit" else self.conf_path(h)
        self.assertFalse(target.exists(), "nothing installed")
        return err

    def test_a_root_run_line_in_any_dropin_is_refused(self):
        """The LD_PRELOAD argument needs every drop-in systemd applies to run
        nothing but the service user's ExecStart: another file beside
        host.conf, the /run and /usr/lib unit directories, the dash-prefix
        directory and the type-wide service.d all count."""
        bad = "[Service]\nExecStartPre=+/usr/bin/true\n"
        places = {
            "beside host.conf": lambda h: self.dropin(h, "zz-joker.conf", bad),
            "/run": lambda h: self.dropin(h, "joker.conf", bad, self.unit_dirs[0]),
            "/usr/lib": lambda h: self.dropin(h, "joker.conf", bad, self.unit_dirs[1]),
            "dash prefix": lambda h: self.dropin(h, "joker.conf", bad, d="audiobook-.service.d"),
            "service.d": lambda h: self.dropin(
                h, "joker.conf", bad, self.unit_dirs[1], "service.d"
            ),
        }
        for what in ("unit", "local-conf"):
            for name, place in places.items():
                with self.subTest(what=what, place=name):
                    h = self.host()
                    place(h)
                    err = self.install_refused(h, what)
                    self.assertIn("joker.conf", err)
                    self.assertNotIn("/usr/bin/true", err, "a refusal never echoes a drop-in")

    def test_every_rule_applies_to_other_dropins(self):
        for line in (
            "ExecStopPost=/usr/bin/true",
            "ExecStart=!/usr/local/bin/audiobook-organizer serve",
            "PermissionsStartOnly=true",
            "AmbientCapabilities=CAP_NET_ADMIN",
            "User=root",
            "Group=joker",
        ):
            with self.subTest(line=line):
                h = self.host()
                self.dropin(h, "50-riddler.conf", f"[Service]\n{line}\n", self.unit_dirs[0])
                self.assertIn("line 2", self.install_refused(h, "local-conf"))

    def test_an_unexpected_dropin_file_or_directory_is_refused(self):
        def symlinked(h: Host) -> None:
            real = h.root / "x.conf"
            real.write_text("[Service]\n")
            link = h.systemd / "audiobook-organizer.service.d" / "x.conf"
            link.symlink_to(real)

        cases = {
            "symlink": symlinked,
            "group-writable file": lambda h: self.dropin(h, "x.conf", "[Service]\n").chmod(0o664),
            "directory named .conf": lambda h: (
                h.systemd / "audiobook-organizer.service.d" / "x.conf"
            ).mkdir(),
            "group-writable directory": lambda h: self.dropin(
                h, "x.conf", "[Service]\n", self.unit_dirs[0]
            ).parent.chmod(0o775),
            "control characters": lambda h: self.dropin(
                h, "x.conf", "[Service]\nUser=alfred\x0b\n"
            ),
        }
        for name, make in cases.items():
            with self.subTest(case=name):
                h = self.host()
                make(h)
                self.install_refused(h, "local-conf")

    def test_harmless_dropins_and_other_files_pass(self):
        h = self.host()
        self.dropin(h, "pprof.conf", "[Service]\nEnvironment=ABK_PPROF_ADDR=localhost:6060\n")
        self.dropin(h, "zz-rollback-type-simple.conf", "[Service]\nType=simple\n")
        self.dropin(
            h,
            "10-timeout-abort.conf",
            "[Service]\nTimeoutStopFailureMode=abort\n",
            self.unit_dirs[1],
            "service.d",
        )
        self.dropin(h, "README", "ExecStartPre=+/usr/bin/true\n")
        self.dropin(h, "old.conf.bak", "ExecStartPre=+/usr/bin/true\n")
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 0, err)
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def test_dropin_paths_systemd_reports_are_checked_too(self):
        """systemctl show -p DropInPaths: a drop-in outside the scanned
        directories (a generator's, say) is checked as well."""
        h = self.host()
        elsewhere = h.root / "generator" / "audiobook-organizer.service.d" / "penguin.conf"
        elsewhere.parent.mkdir(parents=True)
        elsewhere.write_text("[Service]\nExecStartPost=/usr/bin/true\n")
        Path(str(h.root / "transient-state") + ".dropin-paths").write_text(
            f"{self.host_conf_path} {elsewhere}\n"
        )
        self.assertIn("penguin.conf", self.install_refused(h, "unit"))
        shows = [t["argv"] for t in h.tools() if "DropInPaths" in t["argv"]]
        self.assertEqual(
            shows, [["show", "-p", "DropInPaths", "--value", "audiobook-organizer.service"]]
        )

    def test_an_unparsable_dropin_paths_answer_is_refused(self):
        for answer in (
            "relative/x.conf",
            "/etc/x.conf;id",
            "/etc/systemd/x.conf.d",
            "/etc/a b.conf\x0b",
        ):
            with self.subTest(answer=answer):
                h = self.host()
                Path(str(h.root / "transient-state") + ".dropin-paths").write_text(answer)
                self.assertIn("DropInPaths", self.install_refused(h, "local-conf"))

    def test_a_failed_systemctl_still_scans_the_directories(self):
        h = self.host()
        Path(str(h.root / "transient-state") + ".show-fails").write_text("")
        self.dropin(h, "joker.conf", "[Service]\nExecStartPre=/usr/bin/true\n", self.unit_dirs[0])
        self.install_refused(h, "local-conf")

    def test_the_unit_template_follows_the_same_rules(self):
        h = self.host()
        self.template(
            committed_unit().replace("[Service]\n", "[Service]\nExecStartPre=+/usr/bin/true\n", 1)
        )
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 1, err)
        self.assertFalse(self.unit_path(h).exists())

    # -- the unit's main file (its fragment), wherever systemd finds it -----

    def fragment_show(self, h: Host, text: str) -> None:
        Path(str(h.root / "transient-state") + ".fragment-show").write_text(text)

    def test_local_conf_needs_a_unit_file(self):
        h = self.host()
        self.lib_unit.unlink()
        err = self.install_refused(h, "local-conf")
        self.assertIn("unit file", err)

    def test_the_unit_install_replaces_a_bad_etc_copy(self):
        """install-staged unit is the command that fixes a bad /etc copy, so
        it does not check the copy it is about to replace; a shadowed
        /usr/lib copy must still pass."""
        bad = committed_unit().replace("[Service]\n", "[Service]\nExecStartPre=+/usr/bin/true\n", 1)
        h = self.host()
        self.unit_path(h).write_text(bad)
        self.unit_path(h).chmod(0o644)
        self.fragment_show(h, f"FragmentPath={self.unit_path(h)}\nSourcePath=")
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.unit_path(h).read_text(), committed_unit())
        h = self.host()
        self.unit_path(h).write_text(bad)
        self.unit_path(h).chmod(0o644)
        self.lib_unit.write_text(bad)
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 1, err)
        self.assertIn(str(self.lib_unit), err)
        self.assertEqual(self.unit_path(h).read_text(), bad)
        self.assertNotIn(["daemon-reload"], self.changes(h))

    def test_the_unit_install_may_create_the_etc_copy(self):
        """install-staged unit is about to write the checked template to
        /etc, so no unit file anywhere yet is fine for it."""
        h = self.host()
        self.lib_unit.unlink()
        self.template()
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 0, err)
        self.assertTrue(self.unit_path(h).exists())

    def test_a_bad_unit_file_in_any_directory_is_refused(self):
        bad = committed_unit().replace("[Service]\n", "[Service]\nExecStartPre=+/usr/bin/true\n", 1)
        rootless = committed_unit().replace("User=alfred\n", "")
        cases = {
            "/usr/lib copy": lambda h: self.lib_unit.write_text(bad),
            "/etc copy": lambda h: self.unit_path(h).write_text(bad),
            "/run copy": lambda h: (
                self.unit_dirs[0].mkdir(exist_ok=True),
                (self.unit_dirs[0] / "audiobook-organizer.service").write_text(bad),
            ),
            "no User=": lambda h: self.lib_unit.write_text(rootless),
            "group-writable": lambda h: self.lib_unit.chmod(0o664),
            "generator copy": lambda h: (
                self.unit_dirs[2].mkdir(exist_ok=True),
                (self.unit_dirs[2] / "audiobook-organizer.service").write_text(committed_unit()),
            ),
        }
        for what in ("unit", "local-conf"):
            for name, make in cases.items():
                if what == "unit" and name == "/etc copy":
                    continue  # the template replaces it: test_the_unit_install_replaces_a_bad_etc_copy
                with self.subTest(what=what, case=name):
                    h = self.host()
                    make(h)
                    for p in h.systemd.glob("audiobook-organizer.service"):
                        p.chmod(0o644)
                    err = (
                        self.install_refused(h, what)
                        if name != "/etc copy"
                        else self.local_conf_refused_keeping_etc(h)
                    )
                    self.assertNotIn("/usr/bin/true", err)

    def local_conf_refused_keeping_etc(self, h: Host) -> str:
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 1, err)
        self.assertNotIn(["daemon-reload"], self.changes(h))
        self.assertFalse(self.conf_path(h).exists())
        return err

    def test_a_symlinked_unit_file_is_refused(self):
        h = self.host()
        real = h.root / "real.service"
        real.write_text(committed_unit())
        self.lib_unit.unlink()
        self.lib_unit.symlink_to(real)
        self.install_refused(h, "local-conf")

    def test_fragment_path_outside_the_unit_directories_is_refused(self):
        h = self.host()
        elsewhere = h.root / "elsewhere" / "audiobook-organizer.service"
        elsewhere.parent.mkdir()
        elsewhere.write_text(committed_unit())
        self.fragment_show(h, f"FragmentPath={elsewhere}\nSourcePath=")
        self.assertIn("FragmentPath", self.install_refused(h, "local-conf"))

    def test_fragment_path_systemd_reports_is_checked(self):
        h = self.host()
        self.lib_unit.write_text(
            committed_unit().replace("[Service]\n", "[Service]\nExecReload=/usr/bin/true\n", 1)
        )
        self.fragment_show(h, f"FragmentPath={self.lib_unit}\nSourcePath=")
        self.install_refused(h, "local-conf")
        shows = [t["argv"] for t in h.tools() if "FragmentPath" in t["argv"]]
        self.assertEqual(
            shows,
            [["show", "-p", "FragmentPath", "-p", "SourcePath", "audiobook-organizer.service"]],
        )

    def test_a_good_reported_fragment_passes(self):
        h = self.host()
        self.fragment_show(h, f"FragmentPath={self.lib_unit}\nSourcePath=")
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def test_a_generated_unit_is_refused(self):
        h = self.host()
        self.fragment_show(h, f"FragmentPath={self.lib_unit}\nSourcePath=/etc/fstab")
        self.assertIn("SourcePath", self.install_refused(h, "local-conf"))

    def test_an_unparsable_fragment_answer_is_refused(self):
        for answer in (
            "FragmentPath=relative/audiobook-organizer.service\nSourcePath=",
            "FragmentPath=/etc/systemd/system/audiobook-organizer.service;id\nSourcePath=",
            "FragmentPath=/x\nSourcePath=\nExtra=1",
            "garbage",
        ):
            with self.subTest(answer=answer):
                h = self.host()
                self.fragment_show(h, answer)
                self.install_refused(h, "local-conf")

    def test_a_failed_systemctl_still_scans_for_unit_files(self):
        h = self.host()
        Path(str(h.root / "transient-state") + ".show-fails").write_text("")
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)
        h2 = self.host()
        Path(str(h2.root / "transient-state") + ".show-fails").write_text("")
        self.lib_unit.unlink()
        self.install_refused(h2, "local-conf")

    # -- logical lines, the way systemd joins them -------------------------

    def rules_refuse(self, text: str) -> str:
        h = self.host()
        self.dropin(h, "50-scarecrow.conf", text, self.unit_dirs[0])
        return self.install_refused(h, "local-conf")

    def rules_pass(self, text: str) -> None:
        h = self.host()
        self.dropin(h, "50-scarecrow.conf", text, self.unit_dirs[0])
        self.stage_conf("[Service]\nEnvironment=A=1\n")
        code, _, err = h.run("install", "local-conf")
        self.assertEqual(code, 0, err)

    def test_an_even_run_of_backslashes_does_not_continue(self):
        """systemd continues a line only when it ends in an ODD number of
        backslashes: `a\\\\` is a literal backslash, so the next line is a
        directive of its own, here a root Exec line."""
        err = self.rules_refuse("[Service]\nEnvironment=X=a\\\\\nExecStartPre=/bin/evil\n")
        self.assertIn("line 3", err)

    def test_a_continued_execstart_is_checked_joined(self):
        """`ExecStart=\\` then `+/bin/evil` is one line to systemd, with the
        backslash replaced by a space: a + prefix."""
        err = self.rules_refuse("[Service]\nExecStart=\\\n+/bin/evil\n")
        self.assertIn("line 2", err)

    def test_comments_inside_a_continuation_are_skipped(self):
        """A comment line inside a continuation is skipped, not joined and
        not an end: the line after it still continues the value. Read the
        other way, each of these would turn out the opposite."""
        err = self.rules_refuse("[Service]\nType=\\\n# a comment\n; another \\\noneshot\n")
        self.assertIn("line 2", err)
        self.rules_pass("[Service]\nEnvironment=A=b\\\n# a comment\nExecStartPre=/bin/evil\n")

    def test_an_empty_line_ends_a_continuation(self):
        """systemd joins an empty line as nothing and ends the line there,
        so the directive after it stands on its own."""
        err = self.rules_refuse("[Service]\nEnvironment=A=b\\\n\nExecStartPre=/bin/evil\n")
        self.assertIn("line 4", err)

    def test_an_odd_run_of_backslashes_continues(self):
        """`b\\\\\\` ends in three backslashes: the next line is part of
        the Environment= value, not a directive."""
        self.rules_pass("[Service]\nEnvironment=A=b\\\\\\\nExecStartPre=/bin/evil\n")

    def test_a_continuation_at_the_end_of_the_file_is_checked(self):
        self.rules_refuse("[Service]\nExecStart=!/bin/evil \\")

    # -- one command only: no `;` word, no Type=oneshot ---------------------

    def test_a_second_command_in_execstart_is_refused(self):
        """A `;` word separates command lines, and Type=oneshot runs each
        of them: the second command's prefix was never checked."""
        for value in (
            "/usr/local/bin/audiobook-organizer ; +/bin/evil",
            "/usr/local/bin/audiobook-organizer\t;\t/bin/evil",
            "/usr/local/bin/audiobook-organizer \\; /bin/evil",
            "/usr/local/bin/audiobook-organizer ';' /bin/evil",
            '/usr/local/bin/audiobook-organizer ";" /bin/evil',
            "/usr/local/bin/audiobook-organizer ;",
        ):
            with self.subTest(value=value):
                self.rules_refuse(f"[Service]\nExecStart=\nExecStart={value}\n")
        self.rules_refuse(
            "[Service]\nExecStart=\nExecStart=/usr/local/bin/x \\\n  --robin ; +/bin/evil\n"
        )
        self.rules_pass("[Service]\nExecStart=\nExecStart=/usr/local/bin/x --label=a;b c;\n")

    def test_type_oneshot_is_refused(self):
        for text in (
            "[Service]\nType=oneshot\n",
            "[Service]\nType = oneshot\n",
            "[Service]\nType=\\\noneshot\n",
        ):
            with self.subTest(text=text):
                self.rules_refuse(text)
        for value in ("notify", "simple", "exec", ""):
            with self.subTest(value=value):
                self.rules_pass(f"[Service]\nType={value}\n")

    # -- the template's User=, Group= and ExecStart=, as systemd reads them --

    def test_a_user_line_folded_into_a_value_is_not_the_users(self):
        """`Environment=A=b\\` then `User=...` is one Environment= line to
        systemd: the unit sets no User= and runs as root."""
        folded = committed_unit().replace("User=alfred\n", "Environment=A=b\\\nUser=alfred\n", 1)
        h = self.host()
        self.template(folded)
        code, _, err = h.run("install", "unit")
        self.assertEqual(code, 1, err)
        self.assertIn("User=", err)
        self.assertFalse(self.unit_path(h).exists())
        h = self.host()
        self.lib_unit.write_text(folded)
        self.assertIn("User=", self.install_refused(h, "local-conf"))

    def test_an_empty_assignment_resets_the_template_setting(self):
        """An empty User=, Group= or ExecStart= resets it in systemd: a
        later empty User= means root, whatever came before."""
        for key in ("User", "Group", "ExecStart"):
            with self.subTest(key=key):
                h = self.host()
                self.template(committed_unit() + f"\n[Service]\n{key}=\n")
                code, _, err = h.run("install", "unit")
                self.assertEqual(code, 1, err)
                self.assertIn(f"{key}=", err)

    # -- ExecStart prefixes: only @, - and : -------------------------------

    def test_only_known_safe_execstart_prefixes_pass(self):
        for value in (
            "|/bin/evil",
            "?/bin/evil",
            "bin/evil",
            "~/bin/evil",
            "@+/bin/evil",
            "$/bin/evil",
        ):
            with self.subTest(value=value):
                self.rules_refuse(f"[Service]\nExecStart=\nExecStart={value}\n")
        for value in (
            "",
            "/usr/local/bin/audiobook-organizer",
            "-/usr/local/bin/x",
            "@:/usr/local/bin/x name",
            "-@:/x",
        ):
            with self.subTest(value=value):
                self.rules_pass(f"[Service]\nExecStart=\nExecStart={value}\n")

    # -- alias names: their drop-in directories apply too -------------------

    def test_an_alias_dropin_directory_is_checked(self):
        """systemctl show -p Names lists the unit's aliases; systemd applies
        <alias>.d/ drop-ins as well."""
        h = self.host()
        Path(str(h.root / "transient-state") + ".names").write_text(
            "audiobook-organizer.service batcave.service"
        )
        self.dropin(
            h,
            "x.conf",
            "[Service]\nExecStartPre=+/bin/evil\n",
            self.unit_dirs[1],
            "batcave.service.d",
        )
        self.assertIn("batcave.service.d", self.install_refused(h, "local-conf"))
        shows = [t["argv"] for t in h.tools() if "Names" in t["argv"]]
        self.assertEqual(shows, [["show", "-p", "Names", "--value", "audiobook-organizer.service"]])

    def test_an_alias_symlink_in_a_unit_directory_counts_too(self):
        """An alias systemd has not loaded yet (a symlink beside the unit,
        before a reload) is found by the scan."""
        h = self.host()
        (self.unit_dirs[1] / "wayne-manor.service").symlink_to(self.lib_unit)
        self.dropin(h, "x.conf", "[Service]\nExecStartPre=+/bin/evil\n", d="wayne-manor.service.d")
        self.assertIn("wayne-manor.service.d", self.install_refused(h, "local-conf"))

    def test_an_unparsable_names_answer_is_refused(self):
        for answer in ("audiobook-organizer.service ../x.service", "x.service;id", "batcave"):
            with self.subTest(answer=answer):
                h = self.host()
                Path(str(h.root / "transient-state") + ".names").write_text(answer)
                self.assertIn("Names", self.install_refused(h, "local-conf"))

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

    # -- a staged path that is not a regular file -------------------------

    def test_a_fifo_or_directory_staged_local_conf_is_refused(self):
        for kind in ("fifo", "directory"):
            with self.subTest(kind=kind):
                h = self.host()
                p = self.stage / "audiobook-organizer-local.conf"
                os.mkfifo(p) if kind == "fifo" else p.mkdir()
                code, _, err = h.run("install", "local-conf")
                self.assertEqual(code, 1, err)
                self.assertIn("is not a regular file", err)
                self.assertFalse(self.conf_path(h).exists())
                self.assertEqual(self.changes(h), [])

    def test_a_fifo_or_directory_staged_binary_is_refused(self):
        for kind in ("fifo", "directory"):
            with self.subTest(kind=kind):
                h, b = self.bin_host()
                b.write_text("current")
                p = self.stage / "audiobook-organizer"
                os.mkfifo(p) if kind == "fifo" else p.mkdir()
                code, _, err = h.run("install", "binary")
                self.assertEqual(code, 1, err)
                self.assertIn("is not a regular file", err)
                self.assertEqual(b.read_text(), "current")
                self.assertFalse(Path(f"{b}.prev").exists())

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

    def test_prev_installs_the_staged_binary_as_prev_and_touches_nothing_else(self):
        """make rollback-restore RELEASE=<tag>: the fetched release becomes
        <bin>.prev atomically; <bin> is neither read nor changed."""
        h, b = self.bin_host()
        b.write_text("current")
        b.chmod(0o755)
        Path(f"{b}.prev").write_text("hotfix")
        (self.stage / "audiobook-organizer").write_text("fetched")
        old_ino = os.stat(b).st_ino
        code, out, err = h.run("install", "prev")
        self.assertEqual(code, 0, err)
        self.assertEqual(Path(f"{b}.prev").read_text(), "fetched")
        self.assertEqual(stat.S_IMODE(os.stat(f"{b}.prev").st_mode), 0o755)
        self.assertEqual((b.read_text(), os.stat(b).st_ino), ("current", old_ino))
        self.assertFalse(Path(f"{b}.prev.tmp").exists())
        self.assertFalse(Path(f"{b}.new").exists())
        self.assertFalse(Path(f"{b}.rolled-back").exists())
        self.assertIn(f"{b}.prev", out)
        self.assertEqual(h.tools(), [], "nothing is reloaded or restarted")

    def test_prev_syncs_the_bin_directory_after_the_rename(self):
        """F8-02 review n4: the rename of .prev is durable only once the bin
        directory is synced; a failed sync is a warning, not a refusal."""
        h, b = self.bin_host()
        (self.stage / "audiobook-organizer").write_text("fetched")
        bin_ino = os.stat(b.parent).st_ino
        real_fsync = os.fsync
        synced: list[int] = []

        def is_bin_dir(fd: int) -> bool:
            st = os.fstat(fd)
            return stat.S_ISDIR(st.st_mode) and st.st_ino == bin_ino

        def recording(fd):
            if is_bin_dir(fd):
                synced.append(fd)
            return real_fsync(fd)

        def failing(fd):
            if is_bin_dir(fd):
                raise OSError(5, "I/O error")
            return real_fsync(fd)

        with unittest.mock.patch("os.fsync", recording):
            code, _, err = h.run("install", "prev")
        self.assertEqual(code, 0, err)
        self.assertTrue(synced, "the bin directory was not synced")

        (self.stage / "audiobook-organizer").write_text("fetched again")
        with unittest.mock.patch("os.fsync", failing):
            code, out, err = h.run("install", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn("could not be synced", err)
        self.assertEqual(Path(f"{b}.prev").read_text(), "fetched again")

    def test_prev_with_no_current_binary_or_no_prev_installs_one(self):
        h, b = self.bin_host()
        (self.stage / "audiobook-organizer").write_text("fetched")
        self.assertEqual(h.run("install", "prev")[0], 0)
        self.assertEqual(Path(f"{b}.prev").read_text(), "fetched")
        self.assertFalse(b.exists(), "<bin> is not created")

    def test_prev_refuses_what_binary_refuses_and_keeps_prev(self):
        h, b = self.bin_host()
        Path(f"{b}.prev").write_text("hotfix")
        staged = self.stage / "audiobook-organizer"
        target = h.root / "elsewhere-bin"
        target.write_text("evil")
        os.symlink(target, staged)  # a symlink
        self.assertEqual(h.run("install", "prev")[0], 1)
        staged.unlink()
        other = h.root / "elsewhere"
        other.write_text("new")
        os.link(other, staged)  # a second hard link
        self.assertEqual(h.run("install", "prev")[0], 1)
        staged.unlink()
        staged.write_text("new")
        with unittest.mock.patch.dict(os.environ, {"SUDO_UID": str(os.getuid() + 1)}):
            self.assertEqual(h.run("install", "prev")[0], 1)  # another owner
        staged.unlink()
        self.assertEqual(h.run("install", "prev")[0], 1, "nothing staged")
        self.assertEqual(Path(f"{b}.prev").read_text(), "hotfix")
        self.assertFalse(Path(f"{b}.prev.tmp").exists())

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


class RollbackEnv(unittest.TestCase):
    """The service environment is part of a rollback: the unit and the
    local.conf drop-in a binary ran under are kept, per binary, as one
    generation under <bin>.envgen/ named by that binary's sha256, and
    restored with it.

    A rollback that put the binary back alone starts the previous build
    under the NEW environment: after an env-name change that means a peer
    allowlist that fails open and settings the old build never reads.

    Every state here is reached by running the deploy and rollback verbs in
    the order a host sees them, never by writing a generation by hand, except
    where a test is about a damaged file."""

    # InstallStaged's fixtures, reused without its tests. host() sets these
    # attributes on the instance; they are declared so a type checker sees them.
    host_conf_path: Path
    lib_unit: Path
    stage: Path

    host = InstallStaged.host
    template = InstallStaged.template
    changes = InstallStaged.changes
    dropin = InstallStaged.dropin
    unit_path = InstallStaged.unit_path
    conf_path = InstallStaged.conf_path
    stage_conf = InstallStaged.stage_conf

    OLD_UNIT_MARK = "# the unit the previous release ran under\n"
    OLD_CONF = "[Service]\nEnvironment=BATCAVE_PEERS=192.0.2.0/24\n"
    NEW_CONF = "[Service]\nEnvironment=ABO_CORE_PEERS=192.0.2.0/24\n"

    def env_host(self, current: str = "bat-signal-1"):
        """A host whose DEPLOY_BIN is a real file, with the previous release's
        unit and local.conf live, and the deploy's staged files ready."""
        bindir = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(shutil.rmtree, bindir, True)
        b = bindir / "audiobook-organizer"
        h = self.host(bin=str(b))
        old_bin = "/usr/local/bin/audiobook-organizer"
        self.host_conf_path.write_text(self.host_conf_path.read_text().replace(old_bin, str(b)))
        self.lib_unit.write_text(self.lib_unit.read_text().replace(old_bin, str(b)))
        self.unit_text = committed_unit().replace(old_bin, str(b))
        self.template(self.unit_text)
        b.write_text(current)
        b.chmod(0o755)
        self.b = b
        # The previous release's unit and drop-in, as installed live.
        self.live_unit = self.unit_path(h)
        self.live_unit.write_text(self.OLD_UNIT_MARK + self.unit_text)
        self.live_unit.chmod(0o644)
        self.live_conf = self.conf_path(h)
        self.live_conf.parent.mkdir(parents=True, exist_ok=True)
        self.live_conf.write_text(self.OLD_CONF)
        self.live_conf.chmod(0o644)
        return h

    def facts(self, binary: str, version="v1.0.0", levels="storage=1", enforced="0") -> list[str]:
        """What a deploy recipe passes to `install binary` about the file it
        stages: its hash, version, store levels and whether the retired-name
        list is enforced in that build."""
        return [self.text_sha(binary), version, levels, enforced]

    def deploy(
        self, h, binary: str = "bat-signal-2", conf: str | None = None, unit=True, facts=None
    ) -> None:
        """What make deploy runs through the helper, in its order."""
        (self.stage / "audiobook-organizer").write_text(binary)
        self.stage_conf(self.NEW_CONF if conf is None else conf)
        if unit:
            self.assertEqual(h.run("install", "unit")[0], 0)
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        code, _, err = h.run("install", "binary", *(facts or []))
        self.assertEqual(code, 0, err)

    def conf(self, tag: str) -> str:
        return f"[Service]\nEnvironment=GEN{tag}=1\n"

    def gdir(self) -> Path:
        return Path(f"{self.b}.envgen")

    def gens(self) -> set[str]:
        return {p.stem for p in self.gdir().glob("*.json") if len(p.stem) == 64}

    def gen(self, sha: str) -> dict:
        return json.loads((self.gdir() / f"{sha}.json").read_text())

    def prev_sha(self) -> str:
        return self.sha(Path(f"{self.b}.prev"))

    def bundle(self) -> dict:
        """The generation of the binary kept as .prev."""
        return self.gen(self.prev_sha())

    def sha(self, path: Path) -> str:
        return self.text_sha(path.read_bytes())

    @staticmethod
    def text_sha(data) -> str:
        import hashlib

        return hashlib.sha256(data if isinstance(data, bytes) else data.encode()).hexdigest()

    def roll(self, h, *argv):
        return h.run("install", "rollback", *argv)

    # -- a deploy keeps the previous environment under the previous binary ---

    def test_a_deploy_keeps_the_previous_unit_and_local_conf_by_the_replaced_binarys_hash(self):
        h = self.env_host()
        before = self.sha(self.b)
        self.deploy(h)
        self.assertEqual(stat.S_IMODE(os.stat(self.gdir()).st_mode), 0o700)
        path = self.gdir() / f"{before}.json"
        self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600, "local.conf may carry secrets")
        doc = self.bundle()
        self.assertEqual(doc["bin_sha256"], before)
        self.assertEqual(
            doc["unit"], {"present": True, "text": self.OLD_UNIT_MARK + self.unit_text}
        )
        self.assertEqual(doc["local_conf"], {"present": True, "text": self.OLD_CONF})
        # ... while the live files are the new deploy's.
        self.assertEqual(self.live_unit.read_text(), self.unit_text)
        self.assertIn("ABO_CORE_PEERS", self.live_conf.read_text())
        self.assertFalse((self.gdir() / "pending.json").exists(), "the capture was promoted")
        self.assertEqual(Path(f"{self.b}.prev").read_text(), "bat-signal-1")

    def test_nothing_is_kept_before_the_binary_is_replaced(self):
        """A generation is committed only when the binary rotates: a deploy
        that stopped after the unit leaves the kept ones intact."""
        h = self.env_host()
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.assertEqual(self.gens(), set())
        self.assertTrue((self.gdir() / "pending.json").exists())

    def test_each_deploy_keeps_its_own_generation_and_a_rollback_finds_it_by_hash(self):
        h = self.env_host()
        a = self.sha(self.b)
        self.deploy(h, "bat-signal-2", conf=self.conf("B"))
        b = self.sha(self.b)
        self.deploy(h, "bat-signal-3", conf=self.conf("C"))
        self.assertEqual(self.gens(), {a, b})
        self.assertIn("BATCAVE_PEERS", self.gen(a)["local_conf"]["text"])
        self.assertIn("GENB", self.gen(b)["local_conf"]["text"])
        # .prev is bat-signal-2: its generation is b's, not the newest or the oldest.
        self.assertEqual(self.prev_sha(), b)
        code, _, err = self.roll(h, "apply", b)
        self.assertEqual(code, 0, err)
        self.assertIn("GENB", self.live_conf.read_text())
        self.assertNotIn("BATCAVE_PEERS", self.live_conf.read_text())

    def test_a_rerun_after_a_half_done_deploy_keeps_the_real_previous_environment(self):
        h = self.env_host()
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.stage_conf(self.NEW_CONF)
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        # The binary never installed. The deploy is run again.
        self.deploy(h)
        doc = self.bundle()
        self.assertEqual(doc["unit"]["text"], self.OLD_UNIT_MARK + self.unit_text)
        self.assertEqual(doc["local_conf"]["text"], self.OLD_CONF)

    def test_a_deploy_that_changes_no_environment_keeps_it_as_it_stands(self):
        h = self.env_host()
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        self.assertEqual(h.run("install", "binary")[0], 0)
        doc = self.bundle()
        self.assertEqual(doc["unit"]["text"], self.live_unit.read_text())
        self.assertEqual(doc["local_conf"]["text"], self.OLD_CONF)

    def test_a_host_with_no_dropin_keeps_that_fact(self):
        h = self.env_host()
        self.live_conf.unlink()
        self.deploy(h, conf="[Service]\nEnvironment=A=1\n")
        self.assertEqual(self.bundle()["local_conf"], {"present": False})
        self.assertEqual(self.roll(h, "apply", self.prev_sha())[0], 0)
        self.assertFalse(self.live_conf.exists(), "the drop-in the deploy added is removed")

    def test_the_first_ever_binary_install_has_nothing_to_keep(self):
        h = self.env_host()
        self.b.unlink()
        self.deploy(h)
        self.assertFalse(Path(f"{self.b}.prev").exists())
        self.assertEqual(self.gens(), set())
        self.assertFalse((self.gdir() / "pending.json").exists())

    def test_a_capture_made_under_another_binary_is_dropped_not_trusted(self):
        """An older half-done deploy's capture, then the binary was changed
        by hand: the capture describes a binary that is not .prev."""
        h = self.env_host()
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.b.write_text("hand-swapped")
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        code, _, err = h.run("install", "binary")
        self.assertEqual(code, 0, err)
        self.assertIn("stale", err)
        self.assertEqual(self.gens(), set())
        self.assertEqual(Path(f"{self.b}.prev").read_text(), "hand-swapped")

    def test_a_new_deploy_restarts_a_capture_made_under_another_binary(self):
        """The stale capture is not carried into the next deploy: that deploy
        captures what is live now, under the binary installed now, so its
        rollback still has an environment."""
        h = self.env_host()
        self.assertEqual(h.run("install", "unit")[0], 0)  # captures the OLD unit
        self.b.write_text("hand-swapped")
        self.deploy(h, binary="bat-signal-3")
        doc = self.bundle()
        self.assertEqual(doc["bin_sha256"], self.prev_sha())
        self.assertEqual(Path(f"{self.b}.prev").read_text(), "hand-swapped")
        self.assertNotIn(self.OLD_UNIT_MARK, doc["unit"]["text"])
        self.assertFalse((self.gdir() / "pending.json").exists())

    def test_a_failed_keep_drops_only_the_generation_it_was_writing(self):
        """The generations are keyed by binary, so an older binary's stay
        valid when a later deploy cannot write beside .prev. What must go is
        the generation of the binary now kept as .prev: an earlier deploy's
        copy of it is not the environment it ran under last."""
        h = self.env_host()
        a = self.sha(self.b)
        self.deploy(h, "bat-signal-2", conf=self.conf("B"))
        b = self.sha(self.b)
        self.deploy(h, "bat-signal-3", conf=self.conf("C"))
        self.assertEqual(self.gens(), {a, b})
        # bat-signal-2 comes back as the binary (a hand rollback), is then
        # deployed over by bat-signal-4, and the keep for it fails.
        self.b.write_text("bat-signal-2")
        (self.stage / "audiobook-organizer").write_text("bat-signal-4")
        with unittest.mock.patch.object(h.mod, "_env_promote", side_effect=OSError("disk full")):
            code, _, err = h.run("install", "binary")
        self.assertEqual(code, 1, err)
        self.assertIn("disk full", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2", "the binary is unchanged")
        self.assertEqual(self.gens(), {a}, "b's stale generation is gone, a's is untouched")

    def test_an_unreadable_live_unit_does_not_block_the_deploy_but_blocks_the_rollback(self):
        h = self.env_host()
        self.live_unit.chmod(0o666)  # group- and world-writable: not root-trusted
        self.deploy(h)
        self.assertEqual(self.bundle()["unit"], {"unreadable": True})
        code, _, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 1)
        self.assertIn("could not be read", err)

    # -- a failed deploy never lets a rerun mislabel the environment --------

    def half_deploy(self, h, fail: str) -> None:
        """A deploy whose unit and local.conf are installed and whose binary
        write then fails: fail="write" fails the rename of the new binary
        (a full disk), fail="promote" fails keeping the environment."""
        self.assertEqual(h.run("install", "unit")[0], 0)
        self.stage_conf(self.NEW_CONF)
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        real_replace = os.replace
        b_new = f"{self.b}.new"

        def failing(src, dst, *a, **kw):
            if str(src) == b_new:
                raise OSError("no space left on device")
            return real_replace(src, dst, *a, **kw)

        if fail == "write":
            with unittest.mock.patch.object(h.mod.os, "replace", side_effect=failing):
                code, _, err = h.run("install", "binary")
        else:
            with unittest.mock.patch.object(
                h.mod, "_env_promote", side_effect=OSError("no space left on device")
            ):
                code, _, err = h.run("install", "binary")
        self.assertEqual(code, 1, err)
        self.assertIn("no space left", err)
        self.assertEqual(self.b.read_text(), "bat-signal-1", "the binary is unchanged")

    def test_a_failed_binary_write_does_not_let_a_rerun_label_the_new_environment_as_old(self):
        """The capture is committed before the binary is renamed in. If it
        were deleted then, the rerun would capture the NEW unit and local.conf
        the first run installed, under the OLD binary, and a rollback would
        call that pair consistent."""
        for fail in ("write", "promote"):
            with self.subTest(fail=fail):
                h = self.env_host()
                self.half_deploy(h, fail)
                self.deploy(h)  # the same deploy, run again
                doc = self.bundle()
                self.assertEqual(doc["bin_sha256"], self.prev_sha())
                self.assertEqual(Path(f"{self.b}.prev").read_text(), "bat-signal-1")
                self.assertEqual(
                    doc["unit"], {"present": True, "text": self.OLD_UNIT_MARK + self.unit_text}
                )
                self.assertEqual(doc["local_conf"], {"present": True, "text": self.OLD_CONF})
                self.assertFalse((self.gdir() / "pending.json").exists())

    def test_the_capture_outlives_a_failed_write_and_goes_with_a_good_one(self):
        h = self.env_host()
        self.half_deploy(h, "write")
        self.assertTrue((self.gdir() / "pending.json").exists())
        self.deploy(h)
        self.assertFalse((self.gdir() / "pending.json").exists())

    def test_a_full_disk_while_writing_the_new_binary_rotates_nothing(self):
        """The new bytes are written first: a failure there leaves .prev and
        the kept generations exactly as they were."""
        h = self.env_host()
        self.deploy(h, "bat-signal-2", conf=self.conf("B"))
        before_prev = Path(f"{self.b}.prev").read_bytes()
        before_gens = {s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}
        (self.stage / "audiobook-organizer").write_text("bat-signal-3")
        with unittest.mock.patch.object(
            h.mod.shutil, "copy2", side_effect=AssertionError("must not rotate")
        ):
            real_open = os.open

            def failing_open(path, *a, **kw):
                if str(path) == f"{self.b}.new":
                    raise OSError("no space left on device")
                return real_open(path, *a, **kw)

            with unittest.mock.patch.object(h.mod.os, "open", side_effect=failing_open):
                code, _, err = h.run("install", "binary")
        self.assertEqual(code, 1, err)
        self.assertEqual(Path(f"{self.b}.prev").read_bytes(), before_prev)
        self.assertEqual(
            {s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}, before_gens
        )
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    # -- the same build again rotates nothing --------------------------------

    def test_a_redeploy_of_the_same_build_rotates_nothing_and_drops_its_capture(self):
        """A hotfix of the same bytes (an environment-only change) must not
        make .prev the flip build and push the pre-flip generation out. The
        capture it made is not carried into a later rotation: it describes an
        environment the binary was already running under."""
        h = self.env_host()
        self.deploy(h, "bat-signal-2", conf=self.conf("B"))
        prev_bytes = Path(f"{self.b}.prev").read_bytes()
        gens = {s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}
        self.deploy(h, "bat-signal-2", conf=self.conf("B2"))
        self.deploy(h, "bat-signal-2", conf=self.conf("B3"))
        self.assertEqual(Path(f"{self.b}.prev").read_bytes(), prev_bytes)
        self.assertEqual({s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}, gens)
        self.assertFalse((self.gdir() / "pending.json").exists())
        self.assertIn("GENB3", self.live_conf.read_text())
        # The next real deploy keeps what bat-signal-2 last ran under.
        self.deploy(h, "bat-signal-3", conf=self.conf("C"))
        self.assertIn("GENB3", self.bundle()["local_conf"]["text"])

    def test_the_same_build_says_so(self):
        h = self.env_host()
        self.deploy(h, "bat-signal-2")
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        code, out, err = h.run("install", "binary")
        self.assertEqual(code, 0, err)
        self.assertIn("nothing replaced", out)

    # -- the flip can be undone after hotfixes ------------------------------

    def flip_then_hotfixes(self, h, hotfixes: int) -> tuple[str, str]:
        """P (pre-flip, the host's OLD environment) -> F (the flip, new
        environment, a store level up) -> hotfixes of different bytes at the
        flip's level. Returns the hash of P and of F."""
        p = self.sha(self.b)
        self.deploy(
            h,
            "flip-build",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build", "v2.0.0", "storage=2", "1"),
        )
        f = self.sha(self.b)
        for n in range(hotfixes):
            tag = f"hotfix-{n}"
            self.deploy(
                h,
                tag,
                conf=self.conf(f"H{n}"),
                facts=self.facts(tag, f"v2.0.{n + 1}", "storage=2", "1"),
            )
        return p, f

    def test_the_pre_flip_environment_survives_hotfixes_and_its_rollback_pairs_it(self):
        """P is installed THROUGH the helpers with facts (storage=1, not
        enforced), so the flip that replaces it is a known boundary. After the
        flip and four hotfixes .prev is a hotfix and P's environment is five
        generations back; it is kept as a boundary, and RELEASE=<P> installs P's
        bytes as .prev, which the rollback pairs with that environment by hash."""
        h = self.env_host()
        self.deploy(h, "pre-flip", conf=self.conf("P"), facts=self.facts("pre-flip", "v1.0.0"))
        p, f = self.flip_then_hotfixes(h, 4)
        self.assertIn(p, self.gens(), "kept: the flip replaced it across a level change")
        self.assertEqual(
            self.gen(p)["pin_reason"], "boundary", "a known boundary, not an unknown pin"
        )
        self.assertNotIn(f, self.gens(), "the flip's own generation is not pinned and aged out")
        # the newest 3, the boundary, and the original binary's unknown pin
        self.assertEqual(len(self.gens()), 5)
        # RELEASE=<pre-flip>: the same bytes arrive as .prev.
        (self.stage / "audiobook-organizer").write_text("pre-flip")
        self.assertEqual(h.run("install", "prev")[0], 0)
        self.assertEqual(self.prev_sha(), p)
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn(f"generation={p}", out)
        self.assertEqual(self.roll(h, "apply", p)[0], 0)
        self.assertEqual(self.b.read_text(), "pre-flip")
        self.assertIn("GENP", self.live_conf.read_text())
        self.assertEqual(self.live_unit.read_text(), self.unit_text)

    def test_without_a_boundary_the_oldest_generation_ages_out(self):
        """The pin is for boundaries only: deploys at one level with the list
        in one state keep the newest GEN_KEEP and no more, so the directory
        does not grow without bound."""
        h = self.env_host()
        self.b.unlink()
        self.deploy(h, "bat-signal-1", facts=self.facts("bat-signal-1"))
        first = self.sha(self.b)
        for n in range(2, 6):
            self.deploy(
                h, f"bat-signal-{n}", conf=self.conf(str(n)), facts=self.facts(f"bat-signal-{n}")
            )
        self.assertNotIn(first, self.gens())
        self.assertEqual(len(self.gens()), 3)

    def test_a_deploy_that_tells_nothing_about_the_binary_pins_what_it_replaces(self):
        """Without facts the helper cannot tell a boundary from a routine
        deploy, and one extra generation costs less than a lost one. Later
        routine deploys (with facts) age out around it."""
        h = self.env_host()
        p = self.sha(self.b)
        self.deploy(h, "bat-signal-2", conf=self.conf("2"))
        for n in range(3, 8):
            tag = f"bat-signal-{n}"
            self.deploy(h, tag, conf=self.conf(str(n)), facts=self.facts(tag))
        self.assertIn(p, self.gens())
        self.assertNotIn(self.text_sha("bat-signal-3"), self.gens(), "a routine one aged out")

    def pin_probe(self, base, nxt):
        """Install `base` (with facts), then `nxt`, then four routine deploys
        that tell the same facts as `nxt`. Returns the hash of the generation
        `base` was kept as, and the hashes still kept at the end."""
        h = self.env_host()
        self.deploy(
            h,
            "bat-signal-2",
            conf=self.conf("0"),
            facts=self.facts("bat-signal-2", "v1.0.0", *base),
        )
        b = self.sha(self.b)
        self.deploy(
            h, "bat-signal-3", conf=self.conf("1"), facts=self.facts("bat-signal-3", "v1.0.1", *nxt)
        )
        for n in range(4, 9):
            tag = f"bat-signal-{n}"
            self.deploy(h, tag, conf=self.conf(str(n)), facts=self.facts(tag, f"v1.0.{n}", *nxt))
        return b, self.gens()

    def test_a_store_level_raise_alone_pins_the_replaced_generation(self):
        b, kept = self.pin_probe(("storage=1", "0"), ("storage=2", "0"))
        self.assertIn(b, kept)

    def test_a_retired_name_list_turning_enforced_alone_pins_the_replaced_generation(self):
        b, kept = self.pin_probe(("storage=1", "0"), ("storage=1", "1"))
        self.assertIn(b, kept)

    def test_routine_deploys_with_the_same_facts_pin_nothing(self):
        b, kept = self.pin_probe(("storage=1", "0"), ("storage=1", "0"))
        self.assertNotIn(b, kept)

    def test_a_run_of_deploys_that_measured_nothing_cannot_push_the_pre_flip_generation_out(self):
        """The flip is a known boundary; later deploys whose facts are missing
        (an ssh timeout, an old Makefile.local) pin what they replace, and
        those pins are bounded among themselves, not against the boundary."""
        h = self.env_host()
        # The owner's deploy through the helpers before the flip: the build
        # the flip replaces has recorded facts.
        self.deploy(h, "pre-flip", conf=self.conf("P"), facts=self.facts("pre-flip"))
        p, _ = self.flip_then_hotfixes(h, 1)
        for n in range(8):
            self.deploy(h, f"blind-{n}", conf=self.conf(f"B{n}"))
        self.assertIn(p, self.gens())
        self.assertLessEqual(len(self.gens()), 1 + 3 + 3, "boundary + newest 3 + 3 unknown pins")

    # -- flip, rollback, flip again ------------------------------------------

    def flip_and_roll_back(self, h):
        """P (facts known, storage=1, not enforced) -> F (the flip) -> rollback
        to P. Returns P's hash."""
        self.deploy(h, "pre-flip", conf=self.conf("P"), facts=self.facts("pre-flip", "v1.0.0"))
        p = self.sha(self.b)
        self.deploy(
            h,
            "flip-build",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build", "v2.0.0", "storage=2", "1"),
        )
        self.assertEqual(self.roll(h, "apply", p)[0], 0)
        self.assertEqual(self.b.read_text(), "pre-flip")
        return p

    def test_check_reports_the_enforced_flag_of_the_binary_being_restored(self):
        """The recipe compares it with the environment ROLLBACK_ENV names."""
        h = self.env_host()
        self.deploy(h, "pre-flip", conf=self.conf("P"), facts=self.facts("pre-flip", "v1.0.0"))
        p = self.sha(self.b)
        self.deploy(
            h,
            "flip-build",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build", "v2.0.0", "storage=2", "1"),
        )
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn("binary_enforced=0", out, ".prev is the pre-flip build")
        f = self.sha(self.b)
        self.deploy(
            h,
            "hotfix",
            conf=self.NEW_CONF,
            facts=self.facts("hotfix", "v2.0.1", "storage=2", "1"),
        )
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn("binary_enforced=1", out, ".prev is the flip build")
        code, out, err = self.roll(h, "check", "ab" * 32, p)
        self.assertEqual(code, 0, err)
        self.assertIn("binary_enforced=none", out, "no generation of its own")
        path = self.gdir() / f"{f}.json"
        doc = json.loads(path.read_text())
        doc["binary"]["enforced"] = None
        path.write_text(json.dumps(doc))
        path.chmod(0o600)
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn("binary_enforced=unrecorded", out)

    def assert_p_is_still_the_boundary(self, p: str) -> None:
        g = self.gen(p)
        self.assertEqual(g["pin_reason"], "boundary")
        self.assertEqual(g["binary"]["version"], "v1.0.0")
        self.assertEqual(g["binary"]["levels"], {"storage": 1})
        self.assertIs(g["binary"]["enforced"], False)

    def test_a_rollback_records_the_restored_binary_as_the_installed_one(self):
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        cur = json.loads((self.gdir() / "current.json").read_text())
        self.assertEqual(cur["bin_sha256"], p)
        self.assertEqual(cur["format"], 2, "the schema format")
        self.assertNotIn(
            "version", cur, "the binary's version is under binary, not beside the format"
        )
        self.assertEqual(cur["binary"]["version"], "v1.0.0")
        self.assertEqual(cur["binary"]["levels"], {"storage": 1})
        self.assertIs(cur["binary"]["enforced"], False)

    def test_a_rollback_by_override_records_no_facts_for_the_restored_binary(self):
        h = self.env_host()
        self.deploy(h, "pre-flip", conf=self.conf("P"), facts=self.facts("pre-flip", "v1.0.0"))
        p = self.sha(self.b)
        self.deploy(h, "flip-build", conf=self.NEW_CONF, facts=self.facts("flip-build", "v2.0.0"))
        # a release built elsewhere installed as .prev, named by P's generation
        (self.stage / "audiobook-organizer").write_text("a-release-built-elsewhere")
        self.assertEqual(h.run("install", "prev")[0], 0)
        elsewhere = self.prev_sha()
        self.assertEqual(self.roll(h, "apply", elsewhere, p)[0], 0)
        cur = json.loads((self.gdir() / "current.json").read_text())
        self.assertEqual(cur["bin_sha256"], elsewhere)
        self.assertNotIn("binary", cur, "the generation's facts are about another binary")

    def test_flip_rollback_reflip_keeps_the_pre_flip_boundary_and_its_facts(self):
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        self.deploy(
            h,
            "flip-build-2",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build-2", "v2.0.1", "storage=2", "1"),
        )
        self.assert_p_is_still_the_boundary(p)
        for n in range(8):  # eviction pressure, facts unknown
            self.deploy(h, f"blind-{n}", conf=self.conf(f"B{n}"))
        self.assertIn(p, self.gens())
        self.assert_p_is_still_the_boundary(p)

    def test_new_measurements_win_over_the_facts_a_generation_already_held(self):
        """Merging keeps old facts only where the new ones are unknown."""
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        path = self.gdir() / f"{p}.json"
        doc = json.loads(path.read_text())
        doc["binary"]["version"] = "stale-v0"
        path.write_text(json.dumps(doc))
        path.chmod(0o600)
        self.deploy(
            h,
            "flip-build-2",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build-2", "v2.0.1", "storage=2", "1"),
        )
        self.assertEqual(self.gen(p)["binary"]["version"], "v1.0.0", "what the deploy measured")
        self.assertEqual(self.gen(p)["pin_reason"], "boundary")

    def test_the_reflip_keeps_the_boundary_even_if_the_installed_record_was_lost(self):
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        (self.gdir() / "current.json").unlink()
        self.deploy(
            h,
            "flip-build-2",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build-2", "v2.0.1", "storage=2", "1"),
        )
        self.assert_p_is_still_the_boundary(p)

    def test_a_deploy_with_no_facts_after_a_rollback_does_not_downgrade_the_boundary(self):
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        (self.gdir() / "current.json").unlink()
        self.deploy(h, "flip-build-2", conf=self.NEW_CONF)  # the recipe passed no facts
        self.assert_p_is_still_the_boundary(p)
        for n in range(8):
            self.deploy(h, f"blind-{n}", conf=self.conf(f"B{n}"))
        self.assertIn(p, self.gens())

    def test_the_flip_is_a_boundary_even_as_the_first_deploy_through_the_helper(self):
        """The build it replaces was installed by hand, so no facts about it
        exist: a build that enforces the retired-name list is still the first
        one the previous release cannot read."""
        h = self.env_host()
        p = self.sha(self.b)
        self.deploy(
            h,
            "flip-build",
            conf=self.NEW_CONF,
            facts=self.facts("flip-build", "v2.0.0", "storage=2", "1"),
        )
        self.assertEqual(self.gen(p)["pin_reason"], "boundary")
        for n in range(8):
            self.deploy(h, f"blind-{n}", conf=self.conf(f"B{n}"))
        self.assertIn(p, self.gens())

    def test_a_failed_keep_after_a_rollback_leaves_the_boundary_generation_in_place(self):
        """ENOSPC while the generation is rewritten: the handler used to
        delete P's generation, the one a rollback to P needs."""
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        (self.stage / "audiobook-organizer").write_text("flip-build-2")
        real = h.mod._write_env_json

        def full(path, doc):
            if str(path).endswith(f"{p}.json"):
                raise OSError(28, "No space left on device")
            return real(path, doc)

        with unittest.mock.patch.object(h.mod, "_write_env_json", side_effect=full):
            code, _, err = h.run(
                "install", "binary", *self.facts("flip-build-2", "v2.0.1", "storage=2", "1")
            )
        self.assertEqual(code, 1)
        self.assertIn("keeping the environment", err)
        self.assertEqual(self.b.read_text(), "pre-flip", "the binary is unchanged")
        self.assert_p_is_still_the_boundary(p)

    def test_a_stale_capture_after_a_rollback_leaves_the_boundary_generation_in_place(self):
        h = self.env_host()
        p = self.flip_and_roll_back(h)
        (self.gdir() / "pending.json").write_text(
            json.dumps({"format": 2, "bin_sha256": "f" * 64, "local_conf": {"present": False}})
        )
        (self.gdir() / "pending.json").chmod(0o600)
        (self.stage / "audiobook-organizer").write_text("flip-build-2")
        code, _, err = h.run(
            "install", "binary", *self.facts("flip-build-2", "v2.0.1", "storage=2", "1")
        )
        self.assertEqual(code, 0, err)
        self.assertIn("stale", err)
        self.assert_p_is_still_the_boundary(p)

    def test_a_capture_left_behind_by_a_finished_deploy_does_not_cost_the_next_generation(self):
        """The new binary is in place but the capture could not be removed:
        the next deploy (binary only) must still keep the generation for the
        binary it replaces."""
        h = self.env_host()
        self.deploy(h, "bat-signal-2", conf=self.conf("2"), facts=self.facts("bat-signal-2"))
        real = h.mod._forget_env

        def stuck(*paths):
            if any(str(x).endswith("pending.json") for x in paths):
                raise OSError(5, "Input/output error")
            return real(*paths)

        (self.stage / "audiobook-organizer").write_text("bat-signal-3")
        self.stage_conf(self.conf("3"))
        self.assertEqual(h.run("install", "local-conf")[0], 0)
        with unittest.mock.patch.object(h.mod, "_forget_env", side_effect=stuck):
            code, _, err = h.run("install", "binary", *self.facts("bat-signal-3"))
        self.assertEqual(code, 0, err)
        self.assertIn("could not remove the finished capture", err)
        three = self.sha(self.b)
        (self.stage / "audiobook-organizer").write_text("bat-signal-4")
        code, _, err = h.run("install", "binary", *self.facts("bat-signal-4"))
        self.assertEqual(code, 0, err)
        self.assertNotIn("stale", err)
        self.assertIn(three, self.gens(), "the generation for the binary just replaced is kept")

    def test_eviction_waits_for_the_new_binary_to_be_in_place(self):
        h = self.env_host()
        for n in range(2, 7):
            tag = f"bat-signal-{n}"
            self.deploy(h, tag, conf=self.conf(str(n)), facts=self.facts(tag))
        before = self.gens()
        (self.stage / "audiobook-organizer").write_text("bat-signal-7")
        real = h.mod.os.replace

        def failing(src, dst, *a, **kw):
            if str(dst) == str(self.b):
                raise OSError(28, "No space left on device")
            return real(src, dst, *a, **kw)

        with unittest.mock.patch.object(h.mod.os, "replace", side_effect=failing):
            code, _, _ = h.run("install", "binary", *self.facts("bat-signal-7"))
        self.assertEqual(code, 1)
        self.assertTrue(
            before <= self.gens(), "a deploy that failed before the rename evicted nothing"
        )

    def test_pins_are_bounded(self):
        h = self.env_host()
        for n in range(2, 14):
            self.deploy(h, f"bat-signal-{n}", conf=self.conf(str(n)))
        self.assertLessEqual(len(self.gens()), 6, "GEN_KEEP + GEN_KEEP_PINNED")

    def test_a_damaged_generation_blocks_eviction_not_the_deploy(self):
        h = self.env_host()
        self.deploy(h, "bat-signal-2", conf=self.conf("2"), facts=self.facts("bat-signal-2"))
        (self.gdir() / ("0" * 64 + ".json")).write_text("{")
        for n in range(3, 8):
            tag = f"bat-signal-{n}"
            self.deploy(h, tag, conf=self.conf(str(n)), facts=self.facts(tag))
        self.assertIn(
            self.text_sha("bat-signal-3"),
            self.gens(),
            "a routine generation that would have aged out stays while a file is unreadable",
        )

    # -- facts the recipe passes ---------------------------------------------

    def test_the_recipes_facts_are_recorded_and_listed_without_content(self):
        h = self.env_host()
        self.deploy(
            h, "bat-signal-2", facts=self.facts("bat-signal-2", "v1.2.3", "storage=1,books=4", "0")
        )
        self.deploy(
            h,
            "bat-signal-3",
            conf=self.conf("C"),
            facts=self.facts("bat-signal-3", "v1.2.4", "storage=1,books=4", "0"),
        )
        code, out, err = self.roll(h, "list")
        self.assertEqual(code, 0, err)
        b = self.sha(Path(f"{self.b}.prev"))
        self.assertIn(f"generation sha256={b}", out)
        self.assertIn("version=v1.2.3 levels=books=4,storage=1", out)
        self.assertIn(f"prev sha256={b} generation={b}", out)
        self.assertIn("unit=present local_conf=present", out)
        self.assertNotIn("BATCAVE_PEERS", out + err, "a listing never prints the environment")
        self.assertNotIn("GENC", out + err)
        # the first generation's binary was installed with no facts
        self.assertIn("version=unrecorded levels=unrecorded", out)

    def test_check_prints_the_hash_generation_version_and_levels(self):
        h = self.env_host()
        self.deploy(h, "bat-signal-2", facts=self.facts("bat-signal-2", "v1.2.3", ".", "1"))
        self.deploy(
            h,
            "bat-signal-3",
            conf=self.conf("C"),
            facts=self.facts("bat-signal-3", "v1.2.4", "storage=2", "1"),
        )
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        b = self.prev_sha()
        self.assertIn(f"current_prev_sha256={b}", out)
        self.assertIn(f"generation={b}", out)
        self.assertIn("version=v1.2.3 levels=. pinned=", out)

    def test_facts_about_another_file_than_the_one_installed_are_not_recorded(self):
        h = self.env_host()
        self.deploy(h, "bat-signal-2")
        (self.stage / "audiobook-organizer").write_text("bat-signal-3")
        code, _, err = h.run("install", "binary", *self.facts("not-the-staged-file", "v9.9.9"))
        self.assertEqual(code, 0, err)
        self.assertIn("another file", err)
        self.assertNotIn("v9.9.9", Path(f"{self.gdir()}/current.json").read_text())

    def test_malformed_facts_are_refused_before_anything_changes(self):
        h = self.env_host()
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        good = self.facts("bat-signal-2")
        bad = {
            "too few": good[:3],
            "too many": good + ["x"],
            "bad sha": ["XYZ", *good[1:]],
            "bad version": [good[0], "v1; rm -rf /", good[2], good[3]],
            "bad levels": [good[0], good[1], "storage=one", good[3]],
            "levels with a space": [good[0], good[1], "a=1, b=2", good[3]],
            "bad owner": [good[0], good[1], "Storage=1", good[3]],
            "bad enforced": [good[0], good[1], good[2], "yes"],
        }
        for name, argv in bad.items():
            with self.subTest(name):
                self.assertEqual(h.run("install", "binary", *argv)[0], 2)
                self.assertEqual(self.b.read_text(), "bat-signal-1")
                self.assertFalse(Path(f"{self.b}.prev").exists())

    def test_a_bare_install_binary_still_works(self):
        h = self.env_host()
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        self.assertEqual(h.run("install", "binary")[0], 0)

    # -- the rollback restores all three, or none ---------------------------

    def test_rollback_restores_the_binary_the_unit_and_local_conf_together(self):
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        h_calls_before = len(h.tools())
        code, out, err = self.roll(h, "apply", prev)
        self.assertEqual(code, 0, err)
        self.assertEqual(self.b.read_text(), "bat-signal-1")
        self.assertEqual(self.live_unit.read_text(), self.OLD_UNIT_MARK + self.unit_text)
        self.assertIn("BATCAVE_PEERS", self.live_conf.read_text())
        self.assertNotIn("ABO_CORE_PEERS", self.live_conf.read_text())
        self.assertEqual(Path(f"{self.b}.rolled-back").read_text(), "bat-signal-2")
        self.assertEqual(stat.S_IMODE(os.stat(self.b).st_mode), 0o755)
        self.assertEqual(
            [t["argv"] for t in h.tools()][h_calls_before:].count(["daemon-reload"]),
            1,
            "systemd is told once, after all three are in place",
        )
        self.assertNotIn("BATCAVE_PEERS=192", out + err, "a rollback never prints the environment")
        for leftover in (".new", ".rolled-back.tmp"):
            self.assertFalse(Path(f"{self.b}{leftover}").exists(), leftover)
        self.assertEqual([p.name for p in self.live_unit.parent.glob("*.tmp")], [])

    def test_rollback_is_idempotent_and_can_be_finished_after_an_interruption(self):
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        self.assertEqual(self.roll(h, "apply", prev)[0], 0)
        kept = Path(f"{self.b}.rolled-back")
        self.assertEqual(kept.read_text(), "bat-signal-2")
        self.assertEqual(self.roll(h, "apply", prev)[0], 0)
        self.assertEqual(self.b.read_text(), "bat-signal-1")
        self.assertEqual(
            kept.read_text(),
            "bat-signal-2",
            "a rerun after the binary rename keeps the rolled-back binary, not the restored one",
        )

    def test_apply_removes_the_capture_of_a_deploy_that_never_finished(self):
        h = self.env_host()
        self.deploy(h)
        self.assertEqual(h.run("install", "local-conf")[0], 0)  # a capture, no binary after it
        self.assertTrue((self.gdir() / "pending.json").exists())
        self.assertEqual(self.roll(h, "apply", self.prev_sha())[0], 0)
        self.assertFalse((self.gdir() / "pending.json").exists())

    def test_a_missing_local_conf_is_removed_before_the_binary_is_replaced(self):
        h = self.env_host()
        self.live_conf.unlink()
        self.deploy(h)
        self.assertTrue(self.live_conf.exists())
        prev = self.prev_sha()
        real_replace = os.replace

        def failing(src, dst, *a, **kw):
            if str(dst) == str(self.b):
                raise OSError("disk full")
            return real_replace(src, dst, *a, **kw)

        with unittest.mock.patch.object(h.mod.os, "replace", side_effect=failing):
            code, _, err = self.roll(h, "apply", prev)
        self.assertNotEqual(code, 0)
        self.assertIn("run `install rollback apply` again", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2", "the binary is still the last rename")
        self.assertFalse(
            self.live_conf.exists(), "the drop-in went with the rest of the environment"
        )
        self.assertEqual(self.roll(h, "apply", prev)[0], 0)
        self.assertEqual(self.b.read_text(), "bat-signal-1")
        self.assertFalse(self.live_conf.exists())

    def test_a_staging_refusal_leaves_the_kept_rolled_back_binary_alone(self):
        """`nothing changed` has to be true: the kept copy of the running
        binary is replaced only after everything is staged."""
        h = self.env_host()
        self.deploy(h)
        kept = Path(f"{self.b}.rolled-back")
        kept.write_text("an-earlier-rolled-back-binary")
        with unittest.mock.patch.object(h.mod.os, "makedirs", side_effect=OSError("read-only")):
            code, _, err = self.roll(h, "apply", self.prev_sha())
        self.assertEqual(code, 1)
        self.assertIn("nothing changed", err)
        self.assertEqual(kept.read_text(), "an-earlier-rolled-back-binary")
        self.assertEqual(self.b.read_text(), "bat-signal-2")
        for leftover in (".new", ".rolled-back.tmp"):
            self.assertFalse(Path(f"{self.b}{leftover}").exists(), leftover)

    def test_a_prev_swapped_between_its_hash_and_its_copy_is_caught_on_the_copy(self):
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        real = h.mod._copy_hashed

        def swapping(src, dst):
            Path(src).write_text("not-the-binary-that-was-checked")
            return real(src, dst)

        with unittest.mock.patch.object(h.mod, "_copy_hashed", side_effect=swapping):
            code, _, err = self.roll(h, "apply", prev)
        self.assertEqual(code, 1)
        self.assertIn("changed while it was being copied", err)
        self.assertIn("nothing changed", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")
        self.assertIn("ABO_CORE_PEERS", self.live_conf.read_text())
        self.assertFalse(Path(f"{self.b}.new").exists())
        self.assertFalse(Path(f"{self.b}.rolled-back").exists())

    def test_the_copy_of_prev_refuses_a_symlink(self):
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        p = Path(f"{self.b}.prev")
        target = p.parent / "elsewhere"
        target.write_text(p.read_text())
        p.unlink()
        p.symlink_to(target)
        code, _, err = self.roll(h, "apply", prev)
        self.assertEqual(code, 1, err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    # -- one install or rollback at a time ----------------------------------

    def hold_lock(self):
        import fcntl

        fd = os.open(self.gdir() / "lock", os.O_RDWR | os.O_CREAT, 0o600)
        self.addCleanup(os.close, fd)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return fd

    def test_a_held_helper_lock_refuses_every_verb_that_changes_something(self):
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        (self.stage / "audiobook-organizer").write_text("bat-signal-3")
        self.hold_lock()
        for argv in (
            ("install", "binary"),
            ("install", "prev"),
            ("install", "unit"),
            ("install", "local-conf"),
            ("install", "rollback", "apply", prev),
        ):
            with self.subTest(argv=argv):
                code, _, err = h.run(*argv)
                self.assertEqual(code, 1)
                self.assertIn("another install or rollback is running", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")
        self.assertEqual(self.prev_sha(), prev)

    def test_a_held_helper_lock_does_not_stop_the_read_only_verbs(self):
        h = self.env_host()
        self.deploy(h)
        self.hold_lock()
        self.assertEqual(self.roll(h, "list")[0], 0)
        self.assertEqual(self.roll(h, "check", "prev")[0], 0)

    def test_the_lock_is_released_when_a_verb_ends(self):
        h = self.env_host()
        self.deploy(h)
        self.assertEqual(self.roll(h, "apply", self.prev_sha())[0], 0)
        self.hold_lock()  # would raise if the finished apply still held it

    def test_a_failed_daemon_reload_after_the_restore_says_to_run_make_rollback_again(self):
        h = self.env_host()
        self.deploy(h)
        with unittest.mock.patch.object(
            h.mod,
            "_daemon_reload",
            side_effect=h.mod.RefusedError("systemctl daemon-reload exited 1"),
        ):
            code, out, err = self.roll(h, "apply", self.prev_sha())
        self.assertEqual(code, 1)
        self.assertIn("already restored", err)
        self.assertIn("run make rollback again", err)
        self.assertNotIn(
            "run systemctl daemon-reload", err, "the deploy user has no such sudo line"
        )
        self.assertEqual(self.b.read_text(), "bat-signal-1")
        self.assertEqual(self.roll(h, "apply", self.prev_sha())[0], 0, "and the rerun finishes")

    def test_a_generation_of_the_largest_allowed_files_full_of_escapes_reads_back(self):
        """json.dumps writes a control character as six bytes: the read limit
        has to hold two UNIT_LIMIT texts of them, or the helper writes a
        generation it then reports as unreadable."""
        h = self.env_host()
        big = "\x01" * h.mod.UNIT_LIMIT
        self.live_unit.write_text(big)
        self.live_conf.write_text(big)
        (self.stage / "audiobook-organizer").write_text("bat-signal-2")
        self.assertEqual(h.run("install", "binary")[0], 0)  # no capture: both parts as they stand
        code, out, err = self.roll(h, "list")
        self.assertEqual(code, 0, err)
        self.assertIn("generations=1 unreadable=0", out)
        self.assertGreater(
            (self.gdir() / f"{self.prev_sha()}.json").stat().st_size, 2 * h.mod.UNIT_LIMIT + 4096
        )

    def test_a_generation_over_the_read_limit_is_never_written(self):
        h = self.env_host()
        path = str(self.gdir().parent / "too-big.json")
        with self.assertRaises(h.mod.RefusedError):
            h.mod._write_env_json(path, {"text": "x" * (h.mod.ENV_LIMIT + 1)})
        self.assertFalse(Path(path).exists())

    def test_a_bin_directory_open_to_group_or_others_is_refused_on_restore(self):
        """The old snapshot read checked the directory holding <bin>: a rollback
        copies .prev into it as root, so whoever can rename files there could
        swap what it copies."""
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        self.b.parent.chmod(0o777)
        self.addCleanup(self.b.parent.chmod, 0o700)
        for argv in (("check", "prev"), ("apply", prev)):
            with self.subTest(argv=argv):
                code, _, err = self.roll(h, *argv)
                self.assertEqual(code, 1)
                self.assertIn("group or world writable", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_a_malformed_generation_is_listed_as_unreadable_not_a_traceback(self):
        h = self.env_host()
        self.deploy(h)
        bad = "0" * 64
        (self.gdir() / f"{bad}.json").write_text(
            json.dumps({"format": 2, "bin_sha256": bad, "seq": 99, "unit": "text", "local_conf": 1})
        )
        (self.gdir() / f"{bad}.json").chmod(0o600)
        code, out, err = self.roll(h, "list")
        self.assertEqual(code, 0, err)
        self.assertIn("unreadable=1", out)
        self.assertNotIn(bad, out, "a malformed generation is never listed as usable")

    def test_boundary_generations_are_never_evicted_and_a_long_run_is_warned_about(self):
        h = self.env_host()
        cap = h.mod.GEN_WARN_BOUNDARY
        self.deploy(
            h,
            "bat-signal-0",
            conf=self.conf("0"),
            facts=self.facts("bat-signal-0", "v1.0.0", "storage=0"),
        )
        shas = []
        for n in range(1, cap + 4):
            shas.append(self.sha(self.b))
            tag = f"bat-signal-{n}"
            self.deploy(
                h, tag, conf=self.conf(str(n)), facts=self.facts(tag, f"v1.0.{n}", f"storage={n}")
            )
        self.assertEqual(
            sum(1 for g in self.gens() if self.gen(g)["pin_reason"] == "boundary"), cap + 3
        )
        for every in shas:
            self.assertIn(every, self.gens(), "the oldest boundary is the pre-flip one: kept")
        (self.stage / "audiobook-organizer").write_text("bat-signal-last")
        code, _, err = h.run(
            "install", "binary", *self.facts("bat-signal-last", "v1.0.99", "storage=99")
        )
        self.assertEqual(code, 0, err)
        self.assertIn("boundary generations are kept", err)
        self.assertIn("none is evicted", err)

    def test_a_generation_directory_open_to_group_or_others_is_refused(self):
        """_read_generation's directory check: the generations are root-only
        plaintext copies of local.conf, and a directory others can write to
        could swap one."""
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        self.gdir().chmod(0o770 | 0o007)
        self.addCleanup(self.gdir().chmod, 0o700)
        for argv in (("check", "prev"), ("apply", prev)):
            with self.subTest(argv=argv):
                code, _, err = self.roll(h, *argv)
                self.assertEqual(code, 1)
                self.assertIn("closed to group and others", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_a_generation_directory_that_is_a_symlink_is_refused(self):
        h = self.env_host()
        self.deploy(h)
        real = Path(f"{self.b}.envgen")
        moved = real.parent / "moved-gens"
        real.rename(moved)
        real.symlink_to(moved)
        code, _, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 1)
        self.assertIn("is not a directory owned by root", err)

    def test_an_interrupted_rollback_has_not_replaced_the_binary_yet(self):
        """The environment is renamed in first and the binary last, so a
        failure part way leaves the running binary alone (the rollback is then
        finished by running apply again), never the old binary under files the
        rollback has not restored."""
        h = self.env_host()
        self.deploy(h)
        prev = self.prev_sha()
        conf_dest = str(self.live_conf)
        real_replace = os.replace

        def failing(src, dst, *a, **kw):
            if str(dst) == conf_dest:
                raise OSError("disk full")
            return real_replace(src, dst, *a, **kw)

        with unittest.mock.patch.object(h.mod.os, "replace", side_effect=failing):
            code, _, err = self.roll(h, "apply", prev)
        self.assertNotEqual(code, 0)
        self.assertIn("run `install rollback apply` again", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2", "the binary is the last rename")
        self.assertEqual(self.roll(h, "apply", prev)[0], 0)
        self.assertEqual(self.b.read_text(), "bat-signal-1")
        self.assertIn("Environment=BATCAVE_PEERS=192.0.2.0/24", self.live_conf.read_text())

    def test_rollback_check_changes_nothing_and_names_the_hash_apply_must_hold(self):
        h = self.env_host()
        self.deploy(h)
        before = {
            p: p.read_bytes()
            for p in (self.b, self.live_unit, self.live_conf, Path(f"{self.b}.prev"))
        }
        gens = {s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}
        changed = self.changes(h)
        code, out, err = self.roll(h, "check", "prev")
        self.assertEqual(code, 0, err)
        self.assertIn(f"current_prev_sha256={self.prev_sha()}", out)
        self.assertIn("environment_lines=1", out)
        self.assertEqual({p: p.read_bytes() for p in before}, before)
        self.assertEqual({s: (self.gdir() / f"{s}.json").read_bytes() for s in self.gens()}, gens)
        self.assertEqual(self.changes(h), changed, "check reloads nothing")
        self.assertFalse(Path(f"{self.b}.rolled-back").exists())

    def test_check_can_ask_about_a_binary_that_is_not_prev_yet(self):
        """RELEASE= asks before it installs anything: the hash of the staged
        file, no .prev needed."""
        h = self.env_host()
        a = self.sha(self.b)
        self.deploy(h)
        (self.stage / "audiobook-organizer").write_text("a-release-built-elsewhere")
        other = self.text_sha("a-release-built-elsewhere")
        code, _, err = self.roll(h, "check", other)
        self.assertEqual(code, 1)
        self.assertIn("no kept environment for the binary", err)
        self.assertEqual(self.roll(h, "check", a)[0], 0, "the replaced binary's hash has one")
        Path(f"{self.b}.prev").unlink()
        self.assertEqual(self.roll(h, "check", a)[0], 0, "and it needs no .prev to ask")

    def test_rollback_refuses_without_a_kept_environment_and_changes_nothing(self):
        h = self.env_host()
        Path(f"{self.b}.prev").write_text("bat-signal-0")  # an old-style deploy's .prev
        before = (self.b.read_bytes(), self.live_unit.read_bytes(), self.live_conf.read_bytes())
        for argv in (("check", "prev"), ("apply", self.prev_sha())):
            with self.subTest(argv=argv):
                code, _, err = self.roll(h, *argv)
                self.assertEqual(code, 1)
                self.assertIn("binary alone", err)
                self.assertIn("none are kept", err)
        self.assertEqual(
            (self.b.read_bytes(), self.live_unit.read_bytes(), self.live_conf.read_bytes()), before
        )
        self.assertFalse(Path(f"{self.b}.rolled-back").exists())
        self.assertNotIn(["daemon-reload"], [t["argv"] for t in h.tools()])

    def test_rollback_refuses_when_prev_changed_since_the_check(self):
        """A make deploy between the recipe's check and the swap rotated .prev:
        the hash the recipe checked is no longer the file there."""
        h = self.env_host()
        self.deploy(h)
        checked = self.prev_sha()
        self.deploy(h, binary="bat-signal-3")  # rotates .prev
        code, _, err = self.roll(h, "apply", checked)
        self.assertEqual(code, 1)
        self.assertIn("landed in between", err)
        self.assertEqual(self.b.read_text(), "bat-signal-3")
        self.assertIn("ABO_CORE_PEERS", self.live_conf.read_text())

    def test_a_prev_no_generation_belongs_to_is_refused_with_the_list_and_how_to_name_one(self):
        """make rollback-restore RELEASE=<tag> installs a fetched binary as
        .prev. A release built elsewhere never has the hash of the build that
        was deployed, so no generation matches it by hash: the rollback says
        which are kept and how to name one."""
        h = self.env_host()
        a = self.sha(self.b)
        self.deploy(h)
        (self.stage / "audiobook-organizer").write_text("release-binary")
        self.assertEqual(h.run("install", "prev")[0], 0)
        fetched = self.prev_sha()
        for argv in (("check", "prev"), ("apply", fetched)):
            with self.subTest(argv=argv):
                code, _, err = self.roll(h, *argv)
                self.assertEqual(code, 1)
                self.assertIn("ROLLBACK_ENV=<sha256>", err)
                self.assertIn(a, err, "the kept generations are listed by hash")
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_naming_a_generation_by_its_hash_restores_it_with_a_prev_of_another_hash(self):
        h = self.env_host()
        a = self.sha(self.b)
        self.deploy(h)
        (self.stage / "audiobook-organizer").write_text("release-binary")
        self.assertEqual(h.run("install", "prev")[0], 0)
        fetched = self.prev_sha()
        code, out, err = self.roll(h, "check", "prev", a)
        self.assertEqual(code, 0, err)
        self.assertIn(f"generation={a}", out)
        code, _, err = self.roll(h, "apply", fetched, a)
        self.assertEqual(code, 0, err)
        self.assertEqual(self.b.read_text(), "release-binary")
        self.assertIn("BATCAVE_PEERS", self.live_conf.read_text())

    def test_naming_a_generation_that_does_not_exist_is_refused(self):
        h = self.env_host()
        self.deploy(h)
        nope = "ab" * 32
        for argv in (("check", "prev", nope), ("apply", self.prev_sha(), nope)):
            with self.subTest(argv=argv):
                code, _, err = self.roll(h, *argv)
                self.assertEqual(code, 1)
                self.assertIn("no kept environment with that sha256", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_rollback_refuses_a_kept_unit_that_fails_the_unit_rules(self):
        """The generation is root-only, but a rollback writes a unit systemd
        runs as root: it is held to the rules install unit holds a template to."""
        h = self.env_host()
        self.deploy(h)
        doc = self.bundle()
        doc["unit"]["text"] = doc["unit"]["text"].replace("User=alfred", "User=root")
        (self.gdir() / f"{doc['bin_sha256']}.json").write_text(json.dumps(doc))
        before = (self.b.read_bytes(), self.live_unit.read_bytes(), self.live_conf.read_bytes())
        code, _, err = self.roll(h, "apply", doc["bin_sha256"])
        self.assertEqual(code, 1)
        self.assertIn("the kept unit", err)
        self.assertEqual(
            (self.b.read_bytes(), self.live_unit.read_bytes(), self.live_conf.read_bytes()), before
        )

    def test_rollback_holds_the_kept_local_conf_to_the_deploy_grammar(self):
        h = self.env_host()
        self.deploy(h)
        for bad in (
            "[Service]\nEnvironment=LD_PRELOAD=/tmp/x.so\n",
            "[Service]\nExecStartPre=/bin/evil\n",
            "[Service]\nEnvironment=A=1\nUser=root\n",
        ):
            with self.subTest(bad=bad):
                doc = self.bundle()
                doc["local_conf"] = {"present": True, "text": bad}
                (self.gdir() / f"{doc['bin_sha256']}.json").write_text(json.dumps(doc))
                code, _, err = self.roll(h, "apply", doc["bin_sha256"])
                self.assertEqual(code, 1)
                self.assertIn("the kept local.conf", err)
                self.assertEqual(self.b.read_text(), "bat-signal-2")
                self.assertNotIn("LD_PRELOAD", self.live_conf.read_text())

    def test_rollback_refuses_a_malformed_or_unsafe_generation(self):
        h = self.env_host()
        self.deploy(h)
        sha = self.prev_sha()
        gfile = self.gdir() / f"{sha}.json"
        good = gfile.read_text()
        cases = {
            "not json": "{",
            "a list": "[]",
            "wrong format": json.dumps({**json.loads(good), "format": 99}),
            "another binary's hash": json.dumps({**json.loads(good), "bin_sha256": "ab" * 32}),
            "no seq": json.dumps({k: v for k, v in json.loads(good).items() if k != "seq"}),
            "no unit": json.dumps({k: v for k, v in json.loads(good).items() if k != "unit"}),
            "unit absent": json.dumps({**json.loads(good), "unit": {"present": False}}),
            "text not a string": json.dumps(
                {**json.loads(good), "unit": {"present": True, "text": 7}}
            ),
        }
        for name, text in cases.items():
            with self.subTest(name):
                gfile.write_text(text)
                code, _, err = self.roll(h, "apply", sha)
                self.assertEqual(code, 1, err)
                self.assertEqual(self.b.read_text(), "bat-signal-2")
        gfile.write_text(good)
        gfile.chmod(0o666)
        self.assertEqual(self.roll(h, "apply", sha)[0], 1, "group-writable")
        gfile.chmod(0o600)
        gfile.unlink()
        os.symlink(self.live_unit, gfile)
        self.assertEqual(self.roll(h, "apply", sha)[0], 1, "a symlink")
        self.assertEqual(self.b.read_text(), "bat-signal-2")
        gfile.unlink()
        gfile.write_text(good)
        os.chmod(self.gdir(), 0o777)
        self.assertEqual(self.roll(h, "apply", sha)[0], 1, "a world-writable directory")
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_rollback_refuses_when_there_is_no_prev(self):
        h = self.env_host()
        self.deploy(h)
        sha = self.prev_sha()
        Path(f"{self.b}.prev").unlink()
        code, _, err = self.roll(h, "apply", sha)
        self.assertEqual(code, 1)
        self.assertIn("nothing to roll back to", err)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_rollback_arguments_are_checked_before_anything_is_read(self):
        h = self.env_host()
        self.deploy(h)
        sha = self.prev_sha()
        for argv in (
            (),
            ("check",),
            ("check", "bogus"),
            ("check", "paired"),
            ("check", "prev", "nothex"),
            ("check", "prev", sha, "extra"),
            ("apply",),
            ("apply", "paired", sha),
            ("apply", "nothex"),
            ("apply", sha.upper()),
            ("apply", sha + "0"),
            ("apply", "prev"),
            ("apply", sha, "snapshot"),
            ("apply", sha, sha, "extra"),
            ("list", "extra"),
            ("explode", sha),
        ):
            with self.subTest(argv=argv):
                self.assertEqual(self.roll(h, *argv)[0], 2)
        self.assertEqual(self.b.read_text(), "bat-signal-2")

    def test_the_other_install_verbs_still_take_exactly_one_argument(self):
        h = self.env_host()
        for argv in (("binary", "x"), ("unit", "x"), ("local-conf", "x"), ("prev", "x"), ()):
            with self.subTest(argv=argv):
                self.assertEqual(h.run("install", *argv)[0], 2)

    def test_there_is_no_swap_prev_verb_any_more(self):
        """The binary alone starts the previous build under the new
        environment. The verb, its stub and its sudoers grant are gone; the
        recovery after a failed swap is `rollback apply` again."""
        h = self.env_host()
        self.deploy(h)
        self.assertEqual(h.run("swap-prev")[0], 2)
        self.assertEqual(self.b.read_text(), "bat-signal-2")
        self.assertNotIn("swap-prev", h.mod.STUBS)
        self.assertFalse((HELPERS / "swap-prev").exists())

    def test_the_helper_never_executes_a_binary(self):
        """The facts come from the recipe: nothing the helper does runs the
        staged, installed or kept binary, as root or otherwise."""
        h = self.env_host()
        self.b.write_text("#!/bin/sh\ntouch " + str(self.b) + ".ran\n")
        self.b.chmod(0o755)
        self.deploy(h, "#!/bin/sh\ntouch " + str(self.b) + ".ran2\n", facts=None)
        self.roll(h, "list")
        self.roll(h, "check", "prev")
        self.roll(h, "apply", self.prev_sha())
        self.assertEqual(sorted(p.name for p in self.b.parent.glob("*.ran*")), [])
        self.assertEqual(
            {t["argv"][0] for t in h.tools()} - {"daemon-reload", "show"},
            set(),
            "only systemctl is run, never the binary",
        )


KEYRING = b"batcave keyring export v1\n" + bytes(range(32))
KEY = b"alfred-encryption-key-0123456789abcdef"
CONFIG_YAML = b"core:\n  paths:\n    state_dir: /var/lib/batcave\n"
STAMP = "20261122T010203Z"

# A fake binary: `secrets export-keyring --db D --out F` writes KEYRING to F,
# mode 0600 (BIN_MODE), after logging its argv and environment.
_FAKE_BIN = """#!{python}
import json, os, sys
args = sys.argv[1:]
with open({log!r}, "a") as f:
    f.write(json.dumps({{"argv": args, "env": {{k: os.environ.get(k, "") for k in
        ("HOME", "ABK_STATE_DIR", "ABO_CORE_PATHS_STATE_DIR")}}}}) + "\\n")
if {exit_code!r}:
    sys.exit({exit_code!r})
out = args[args.index("--out") + 1]
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, {mode!r})
os.write(fd, {data!r})
os.close(fd)
os.chmod(out, {mode!r})
"""


class Export(unittest.TestCase):
    """export-keyring and export-install-secrets: the service user's files,
    handed to the deploy user only as files in export_dir."""

    def setUp(self):
        self.h = h = Host(self)
        self.export = h.root / "export"
        self.export.mkdir(mode=0o700)
        self.export.chmod(0o700)
        with open(h.mod.CONFIG, "a") as f:
            f.write(f"export_dir = {self.export}\n")
        self.tmp = h.root / "tmp"
        self.tmp.mkdir()
        self.bin_log = h.root / "bin-calls.log"
        self.mode_file = h.root / "fake-run-mode"
        self.set_bin()
        # The helper copy the fake systemd-run runs as _export-worker, with its
        # config path pointed into the sandbox (a unit really runs the installed one).
        src = HELPER.read_text()
        before = 'CONFIG = "/etc/audiobook-organizer/rollback.conf"'
        assert src.count(before) == 1
        self.patched = h.root / "patched" / "rollback_helper.py"
        self.patched.parent.mkdir()
        self.patched.write_text(src.replace(before, f"CONFIG = {h.mod.CONFIG!r}"))
        body = (
            "import subprocess\n"
            "args = sys.argv[1:]\n"
            'cut = args.index("--")\n'
            "opts, cmd = args[:cut], args[cut + 1:]\n"
            "env = dict(os.environ)\n"
            "for o in opts:\n"
            '    if o.startswith("--setenv="):\n'
            '        k, _, v = o[len("--setenv="):].partition("=")\n'
            "        env[k] = v\n"
            f'cmd = [{str(self.patched)!r} if c.endswith("rollback_helper.py") else c for c in cmd]\n'
            f"mode = open({str(self.mode_file)!r}).read().strip() if os.path.exists({str(self.mode_file)!r}) else 'normal'\n"
            'if mode == "fail":\n'
            "    sys.exit(7)\n"
            "p = subprocess.run(cmd, env=env, stdout=subprocess.PIPE)\n"
            "data = p.stdout\n"
            'if mode == "prefix": data = b"warning: hello\\n" + data\n'
            'if mode == "truncate": data = data[:-3]\n'
            'if mode == "flip": data = data[:-1] + bytes([data[-1] ^ 1])\n'
            'if mode == "extra": data = data + b"x"\n'
            'if mode == "big": data = b"A" * (2 << 20)\n'
            'if mode == "twice": data = data + data\n'
            "sys.stdout.buffer.write(data)\n"
            "sys.stdout.buffer.flush()\n"
            "sys.exit(p.returncode)\n"
        )
        h.mod.TOOLS["systemd-run"] = h.tool("systemd-run", body)
        h.mod._export_stamp = lambda: STAMP
        patch = unittest.mock.patch.dict(os.environ, {"TMPDIR": str(self.tmp)})
        patch.start()
        self.addCleanup(patch.stop)
        patch = unittest.mock.patch.object(tempfile, "tempdir", str(self.tmp))
        patch.start()
        self.addCleanup(patch.stop)
        self.put(h.state / ".encryption_key", KEY)
        self.put(h.db.parent / "config.yaml", CONFIG_YAML)

    def put(self, path: Path, data: bytes, mode: int = 0o600) -> Path:
        path.write_bytes(data)
        path.chmod(mode)
        return path

    def set_bin(self, data: bytes = KEYRING, mode: int = 0o600, exit_code: int = 0):
        h = self.h
        h.bin.write_text(
            _FAKE_BIN.format(
                python=sys.executable,
                log=str(self.bin_log),
                data=data,
                mode=mode,
                exit_code=exit_code,
            )
        )
        h.bin.chmod(0o755)

    def systemd_runs(self) -> list[list[str]]:
        return [t["argv"] for t in self.h.tools() if t["tool"] == "systemd-run"]

    def exported(self) -> list[str]:
        return sorted(p.name for p in self.export.iterdir())

    def lines(self, out: str) -> list[list[str]]:
        return [ln.split(" ") for ln in out.splitlines()]

    def refused(self, *argv: str, code: int = 1, contains: str = "") -> str:
        before = self.exported()
        got, out, err = self.h.run(*argv)
        self.assertEqual(got, code, err)
        self.assertEqual(out, "", "a refusal prints no EXPORTED line")
        self.assertIn(contains, err)
        self.assertEqual(self.exported(), before, "a refusal leaves export_dir as it was")
        return err

    # -- what it does ---------------------------------------------------------

    def test_export_keyring_delivers_one_file_and_names_it(self):
        h = self.h
        code, out, err = h.run("export-keyring")
        self.assertEqual(code, 0, err)
        name = f"aorg-keyring-{STAMP}"
        self.assertEqual(self.exported(), [name])
        path = self.export / name
        self.assertEqual(path.read_bytes(), KEYRING)
        self.assertEqual(
            self.lines(out),
            [["EXPORTED", name, str(len(KEYRING)), hashlib.sha256(KEYRING).hexdigest()]],
        )
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(path.stat().st_uid, os.getuid())

    def test_export_keyring_runs_the_binary_as_the_service_user_not_into_export_dir(self):
        h = self.h
        self.assertEqual(h.run("export-keyring")[0], 0)
        runs = self.systemd_runs()
        self.assertEqual(len(runs), 1)
        argv = runs[0]
        cut = argv.index("--")
        self.assertEqual(
            argv[:cut],
            [
                "--pipe",
                "--wait",
                "--quiet",
                "--collect",
                "--unit=audiobook-organizer-rollback-export",
                "--service-type=exec",
                "--uid=alfred",
                "--property=PrivateTmp=yes",
                f"--setenv=HOME={h.state}",
                f"--setenv=ABK_STATE_DIR={h.state}",
            ],
        )
        self.assertEqual(
            argv[cut + 1 :], [sys.executable, "-I", str(HELPER), "_export-worker", "keyring"]
        )
        self.assertFalse(
            any("StandardOutput" in a or "restore.log" in a for a in argv),
            "the secret never goes through the group-readable restore log",
        )
        calls = [json.loads(ln) for ln in self.bin_log.read_text().splitlines()]
        self.assertEqual(len(calls), 1)
        a = calls[0]["argv"]
        self.assertEqual(a[:4], ["secrets", "export-keyring", "--db", str(h.db)])
        self.assertEqual(a[4], "--out")
        out = a[5]
        self.assertTrue(out.startswith(str(self.tmp) + "/aorg-keyring-"), out)
        self.assertFalse(out.startswith(str(self.export)))
        self.assertEqual(calls[0]["env"]["ABK_STATE_DIR"], str(h.state))
        self.assertEqual(list(self.tmp.iterdir()), [], "the scratch copy is removed")
        self.assertFalse(h.restore_log().exists(), "no restore log is written")

    def test_export_keyring_takes_no_stopped_unit(self):
        # It works while the service runs: only the restore/export units matter,
        # never the service's own.
        h = self.h
        self.assertEqual(h.run("export-keyring")[0], 0)
        self.assertFalse(
            any(t["argv"][:1] == ["stop"] for t in h.tools() if t["tool"] == "systemctl")
        )
        self.assertEqual(
            [t["argv"] for t in h.tools() if t["tool"] == "systemctl"],
            [
                [
                    "show",
                    "-p",
                    "ActiveState",
                    "--value",
                    "audiobook-organizer-rollback-restore.service",
                ],
                [
                    "show",
                    "-p",
                    "ActiveState",
                    "--value",
                    "audiobook-organizer-rollback-export.service",
                ],
                [
                    "show",
                    "-p",
                    "ActiveState",
                    "--value",
                    "audiobook-organizer-rollback-export.service",
                ],
            ],
        )

    def test_install_secrets_delivers_the_key_and_the_config(self):
        h = self.h
        code, out, err = h.run("export-install-secrets")
        self.assertEqual(code, 0, err)
        key, cfg = f"aorg-encryption-key-{STAMP}", f"aorg-config-{STAMP}.yaml"
        self.assertEqual(self.exported(), sorted([key, cfg]))
        self.assertEqual((self.export / key).read_bytes(), KEY)
        self.assertEqual((self.export / cfg).read_bytes(), CONFIG_YAML)
        self.assertEqual(
            self.lines(out),
            [
                ["EXPORTED", key, str(len(KEY)), hashlib.sha256(KEY).hexdigest()],
                ["EXPORTED", cfg, str(len(CONFIG_YAML)), hashlib.sha256(CONFIG_YAML).hexdigest()],
            ],
        )
        for n in (key, cfg):
            self.assertEqual(stat.S_IMODE((self.export / n).stat().st_mode), 0o600, n)
        kinds = [r[-1] for r in self.systemd_runs()]
        self.assertEqual(kinds, ["key", "config"])

    def test_key_beside_the_store_when_the_state_dir_has_none(self):
        h = self.h
        (h.state / ".encryption_key").unlink()
        self.put(h.db.parent / ".encryption_key", b"legacy-key-bytes")
        self.assertEqual(h.run("export-install-secrets")[0], 0)
        self.assertEqual(
            (self.export / f"aorg-encryption-key-{STAMP}").read_bytes(), b"legacy-key-bytes"
        )

    def test_a_missing_config_yaml_is_reported_absent_not_an_error(self):
        h = self.h
        (h.db.parent / "config.yaml").unlink()
        code, out, err = h.run("export-install-secrets")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.exported(), [f"aorg-encryption-key-{STAMP}"])
        self.assertEqual(out.splitlines()[-1], "ABSENT config")

    def test_a_missing_key_refuses_and_delivers_nothing(self):
        h = self.h
        (h.state / ".encryption_key").unlink()
        self.refused("export-install-secrets", contains="exited")

    def test_the_file_is_the_deploy_users_by_fchown_with_sudo_ids(self):
        calls = []
        env = {"SUDO_UID": str(os.getuid()), "SUDO_GID": "4343"}
        with contextlib.ExitStack() as stack:
            stack.enter_context(unittest.mock.patch.dict(os.environ, env))
            stack.enter_context(
                unittest.mock.patch.object(os, "fchown", lambda fd, u, g: calls.append((u, g)))
            )
            self.assertEqual(self.h.run("export-keyring")[0], 0)
        self.assertEqual(calls, [(os.getuid(), 4343)])

    def test_the_file_is_0600_whatever_the_umask(self):
        old = os.umask(0)
        self.addCleanup(os.umask, old)
        self.assertEqual(self.h.run("export-keyring")[0], 0)
        self.assertEqual(
            stat.S_IMODE((self.export / f"aorg-keyring-{STAMP}").stat().st_mode), 0o600
        )

    # -- refusals -------------------------------------------------------------

    def test_unset_export_dir_refuses_before_anything_runs(self):
        h = self.h
        text = Path(h.mod.CONFIG).read_text().replace(f"export_dir = {self.export}\n", "")
        Path(h.mod.CONFIG).write_text(text)
        err = self.refused("export-keyring", contains="export_dir is not set")
        self.assertEqual(self.systemd_runs(), [])
        self.assertNotIn(str(self.export), err)

    def test_bad_export_dir_values_refuse_in_the_config(self):
        for bad in ("relative/dir", "/home/alfred/ex port", "/home/../etc", "/home/alfred/x;id"):
            h = Host(self)
            with open(h.mod.CONFIG, "a") as f:
                f.write(f"export_dir = {bad}\n")
            code, _, err = h.run("export-keyring")
            self.assertEqual(code, 1, bad)
            self.assertIn("export_dir", err)
            self.assertEqual(h.tools(), [], bad)

    def test_symlinked_export_dir_refuses(self):
        h = self.h
        real = h.root / "real-export"
        os.rename(self.export, real)
        os.symlink(real, self.export)
        before = sorted(p.name for p in real.iterdir())
        code, out, err = h.run("export-keyring")
        self.assertEqual(code, 1, err)
        self.assertIn("cannot open the export directory", err)
        self.assertEqual(sorted(p.name for p in real.iterdir()), before)
        self.assertEqual(self.systemd_runs(), [])

    def test_symlink_in_a_parent_of_export_dir_refuses(self):
        h = self.h
        real = h.root / "real-parent"
        real.mkdir()
        (real / "export").mkdir(mode=0o700)
        os.symlink(real, h.root / "linked-parent")
        text = (
            Path(h.mod.CONFIG)
            .read_text()
            .replace(f"export_dir = {self.export}", f"export_dir = {h.root}/linked-parent/export")
        )
        Path(h.mod.CONFIG).write_text(text)
        code, _, err = h.run("export-keyring")
        self.assertEqual(code, 1, err)
        self.assertIn("cannot open the export directory", err)
        self.assertEqual(list((real / "export").iterdir()), [])
        self.assertEqual(self.systemd_runs(), [])

    def test_export_dir_that_is_a_file_refuses(self):
        h = self.h
        self.export.rmdir()
        self.put(self.export, b"not a directory")
        code, _, err = h.run("export-keyring")
        self.assertEqual(code, 1, err)
        self.assertEqual(self.export.read_bytes(), b"not a directory")
        self.assertEqual(self.systemd_runs(), [])

    def test_export_dir_not_owned_by_the_deploy_user_refuses(self):
        # SUDO_UID is the user who ran sudo; the directory belongs to someone else.
        with unittest.mock.patch.dict(os.environ, {"SUDO_UID": str(os.getuid() + 1)}):
            err = self.refused("export-keyring", contains="not by the deploy user")
        self.assertIn(str(os.getuid() + 1), err)
        self.assertEqual(self.systemd_runs(), [])

    def test_export_dir_with_other_modes_refuses(self):
        for mode in (0o755, 0o750, 0o770, 0o777, 0o500):
            self.export.chmod(mode)
            self.addCleanup(self.export.chmod, 0o700)
            err = self.refused("export-keyring", contains="not 0700")
            self.assertIn(f"{mode:04o}", err)
        self.assertEqual(self.systemd_runs(), [])

    def test_a_planted_symlink_at_the_output_name_is_refused_and_not_followed(self):
        h = self.h
        victim = self.put(h.root / "victim", b"the deploy user's own precious file")
        os.symlink(victim, self.export / f"aorg-keyring-{STAMP}")
        code, out, err = h.run("export-keyring")
        self.assertEqual(code, 1, err)
        self.assertEqual(out, "")
        self.assertIn("never overwritten or followed", err)
        self.assertEqual(victim.read_bytes(), b"the deploy user's own precious file")
        self.assertTrue((self.export / f"aorg-keyring-{STAMP}").is_symlink())
        self.assertEqual(stat.S_IMODE(victim.stat().st_mode), 0o600)

    def test_a_dangling_symlink_at_the_output_name_is_refused_too(self):
        h = self.h
        target = h.root / "not-there"
        os.symlink(target, self.export / f"aorg-keyring-{STAMP}")
        self.assertEqual(h.run("export-keyring")[0], 1)
        self.assertFalse(
            target.exists(), "O_EXCL refuses a dangling link instead of creating its target"
        )

    def test_an_existing_file_at_the_output_name_is_never_overwritten(self):
        planted = self.put(self.export / f"aorg-keyring-{STAMP}", b"planted")
        self.assertEqual(self.h.run("export-keyring")[0], 1)
        self.assertEqual(planted.read_bytes(), b"planted")

    def test_a_second_file_that_cannot_be_made_removes_the_first(self):
        planted = self.put(self.export / f"aorg-config-{STAMP}.yaml", b"planted")
        code, out, err = self.h.run("export-install-secrets")
        self.assertEqual(code, 1, err)
        self.assertEqual(out, "")
        self.assertEqual(self.exported(), [planted.name], "the key written first is deleted again")
        self.assertEqual(planted.read_bytes(), b"planted")

    def test_arguments_are_refused(self):
        for op in ("export-keyring", "export-install-secrets"):
            for bad in [*INJECTIONS, "--out", "keyring"]:
                self.refused(op, bad, code=2, contains="usage")
            self.refused(op, "a", "b", code=2, contains="usage")
        self.assertEqual(self.systemd_runs(), [])

    def test_worker_kinds_are_refused(self):
        for argv in ([], ["bin"], ["keyring", "x"], ["../x"], ["config\n"]):
            code, out, _ = self.h.run("_export-worker", *argv)
            self.assertEqual((code, out), (2, ""), argv)

    def test_a_running_restore_or_export_unit_refuses_without_starting_anything(self):
        for state in ("active", "activating", "deactivating", "reloading", ""):
            self.h.transient_state(state)
            self.refused("export-keyring", contains="may still be running")
            self.refused("export-install-secrets", contains="may still be running")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_failed_unit_is_reset_and_runs(self):
        self.h.transient_state("failed")
        self.assertEqual(self.h.run("export-keyring")[0], 0)
        self.assertIn(
            ["reset-failed", "audiobook-organizer-rollback-export"],
            [t["argv"] for t in self.h.tools() if t["tool"] == "systemctl"],
        )

    def test_the_binary_failing_delivers_nothing(self):
        self.set_bin(exit_code=3)
        self.refused("export-keyring", contains="exited 1")

    def test_a_keyring_readable_by_others_is_refused(self):
        self.set_bin(mode=0o644)
        self.refused("export-keyring", contains="exited 1")
        self.assertEqual(list(self.tmp.iterdir()), [], "the scratch copy is removed on failure too")

    def test_a_failing_unit_delivers_nothing(self):
        self.mode_file.write_text("fail")
        self.refused("export-keyring", contains="exited 7")

    def test_output_that_is_not_exactly_one_frame_delivers_nothing(self):
        for mode in ("prefix", "truncate", "flip", "extra", "big", "twice"):
            self.mode_file.write_text(mode)
            self.refused("export-keyring", contains="", code=1)
            self.refused("export-install-secrets", contains="", code=1)

    def test_worker_refuses_unsafe_source_files(self):
        h = self.h
        key = h.state / ".encryption_key"
        second = h.root / "second-name"

        def clear():
            if key.is_symlink() or key.is_file():
                key.unlink()
            elif key.is_dir():
                key.rmdir()
            second.unlink(missing_ok=True)

        def hard_link():
            self.put(key, KEY)
            os.link(key, second)

        cases = {
            "symlink": lambda: os.symlink(h.root / "elsewhere", key),
            "empty": lambda: self.put(key, b""),
            "directory": lambda: key.mkdir(),
            "hard link": hard_link,
            "too big": lambda: self.put(key, b"k" * ((1 << 20) + 1)),
        }
        for label, setup in cases.items():
            clear()
            setup()
            code, out, err = h.run("_export-worker", "key")
            self.assertEqual(code, 1, (label, err))
            self.assertEqual(out, "", label)

    def test_worker_refuses_a_symlinked_config_yaml_and_nothing_is_delivered(self):
        h = self.h
        cfg = h.db.parent / "config.yaml"
        cfg.unlink()
        os.symlink(h.root / "elsewhere", cfg)
        self.refused("export-install-secrets", contains="exited 1")

    def test_worker_writes_one_frame_on_stdout_and_nothing_else(self):
        for kind, data in (("key", KEY), ("config", CONFIG_YAML), ("keyring", KEYRING)):
            buf = io.BytesIO()
            out = unittest.mock.Mock(buffer=buf)
            with unittest.mock.patch.object(sys, "stdout", out):
                code = self.h.mod.main(["_export-worker", kind])
            self.assertEqual(code, 0, kind)
            self.assertEqual(
                buf.getvalue(),
                f"AORGEXPORT1 {len(data)} {hashlib.sha256(data).hexdigest()}\n".encode() + data,
                kind,
            )

    def test_the_new_operations_are_registered_with_stubs(self):
        mod = self.h.mod
        for op in ("export-keyring", "export-install-secrets"):
            self.assertEqual(mod.STUBS[op], op)
            self.assertIn(op, mod.OPS)
        self.assertIn("_export-worker", mod.OPS)
        self.assertNotIn("_export-worker", mod.STUBS, "the worker is never a sudoers stub")


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
