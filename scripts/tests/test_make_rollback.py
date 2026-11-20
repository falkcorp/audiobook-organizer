# file: scripts/tests/test_make_rollback.py
# version: 1.5.0
# guid: 17d15b8e-ad9f-4804-af7c-e1f0e4b72b13
# last-edited: 2026-11-18

"""`make rollback` and `make rollback-restore` against a fake host.

09 §9.4 R1 Verification ("scripts/tests/test_make_rollback.py on its fake-ssh
harness"), 05-storage §5S.3.7 "The binary-swap guard" and its test row
`TestRollbackGuardRefusesAfterCutover`, §5S.3.12 row `make rollback-restore`;
R1-27.

Each test runs the real Makefile target. `ssh` is scripts/tests/fake_ssh.py:
systemctl answers are scripted; every other command runs for real inside a
temporary sandbox that plays the host's filesystem, including the real
rollback helpers (deploy/rollback-helpers/), installed in the sandbox with
their fixed paths pointed into it, behind a fake `systemd-run` that runs the
restore in place and a fake `systemctl` for their daemon-reload. The
application binaries are fake ones whose answers are baked into their own
text, so the swap that installs `.prev` carries its identity with it. Every
host variable is passed on the make command line, so a developer's
Makefile.local can never point a test at a real host.

The mutation probes at the end run a refusal scenario against a copy of the
guard with one safety check broken (ROLLBACK_GUARD names the copy) and assert
that the recipe then goes ahead, which proves the scenario's assertions would
catch that regression. SudoersDrift checks every sudo command the recipes
run against the drop-in the guard renders, and the doc's block against it.

Run with:
    python3 -m unittest discover -s scripts -p 'test_make_rollback.py' -v
"""

from __future__ import annotations

import ast
import hashlib
import importlib.util
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import fake_ssh  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
GUARD = REPO_ROOT / "scripts" / "storage_format_guard.py"
HELPERS = REPO_ROOT / "deploy" / "rollback-helpers"
HOST = "wayne-manor.invalid"
USER = "alfred"
CHECKPOINT = "20261101T020304Z-0badc0de"
ARCHIVE = "batcave-20261101.tar.gz"
R1 = {"books": 0, "core": 0}
R2 = {"books": 1, "core": 1}
# The real /health shape: the API envelope (httputil.RespondWithOK).
READY = (200, '{"data":{"status":"ok","ready":true,"memdb_ready":true,"degraded":[]}}')
UNIT_SHOW = r"^systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus,InvocationID "
STOPPED_SHOW = r"^systemctl show -p ActiveState,SubState "
INVOCATION_SHOW = r"^systemctl show -p InvocationID --value "
OLD_INVOCATION = "0ld1nv0c"

# A name of its own, so it never replaces test_storage_format_guard's copy
# of the same file in sys.modules when discovery runs both in one process.
_spec = importlib.util.spec_from_file_location("storage_format_guard_for_make_rollback", GUARD)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
# Registered before exec_module (the importlib recipe): dataclasses, under
# `from __future__ import annotations`, looks the class's module up in
# sys.modules while the module body runs, and Python 3.12 (CI's) fails
# with AttributeError when it is absent.
sys.modules[_spec.name] = guard
_spec.loader.exec_module(guard)

# The fake host's tools, logged to the invocation log.
_TOOL = """#!{python}
import json, os, subprocess, sys
with open({log!r}, "a") as f:
    f.write(json.dumps({{"kind": "tool", "tool": {name!r}, "argv": sys.argv[1:]}}) + "\\n")
{body}
"""
_SYSTEMD_RUN = """args = sys.argv[1:]
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
with open(out, "ab") as f:
    sys.exit(subprocess.run(cmd, env=env, stdout=f, stderr=f).returncode)
"""


def unit_state(active: str, sub: str = "dead") -> dict:
    return {"stdout": f"ActiveState={active}\nSubState={sub}\n", "exit": 0}


class FakeHost:
    """A sandbox laid out as a deploy host, plus the make invocation."""

    def __init__(
        self,
        tc: unittest.TestCase,
        *,
        store_levels="books 0\ncore 0\n",
        store_format="1\n",
        current=None,
        prev=None,
        checkpoint_levels=None,
        record=None,
        from_release="v0.41.0",
        pointer=True,
        rules=None,
        stopped=None,
        shows=None,
        config=None,
        releases=None,
        env_snapshot=True,
    ):
        self._tmp = tempfile.TemporaryDirectory()
        tc.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()
        self.db = self.root / "data" / "audiobooks.pebble"
        self.db.mkdir(parents=True)
        self.state = self.root / "state"
        self.state.mkdir()
        self.bin = self.root / "bin" / "audiobook-organizer"
        self.prev = Path(str(self.bin) + ".prev")
        self.archives = self.root / "archives"
        self.archives.mkdir()
        self.archive = self.archives / ARCHIVE
        self.archive.write_bytes(b"an archive")
        self.log = self.root / "invocations.log"
        cur = {
            "name": "current",
            "levels": R1,
            "format": 1,
            "capabilities": "sd_notify",
            "into_levels": "books 0\ncore 0\n",
        }
        cur.update(current or {})
        # releases: tag -> identity overrides of the fake binary that
        # `updater fetch-release --version tag` writes (RELEASE=<tag>).
        self.stage = self.root / "home" / "audiobook-organizer"
        self.stage.parent.mkdir()
        cur["releases"] = {
            tag: fake_ssh.fake_app_text(
                {
                    "name": f"fetched-{tag}",
                    "levels": R1,
                    "format": 1,
                    "capabilities": "sd_notify",
                    **over,
                    "log": str(self.log),
                }
            )
            for tag, over in (releases or {}).items()
        }
        old = {"name": "prev", "levels": R1, "format": 1, "capabilities": "sd_notify"}
        old.update(prev or {})
        fake_ssh.write_fake_app(self.bin, {**cur, "log": str(self.log)})
        fake_ssh.write_fake_app(self.prev, {**old, "log": str(self.log)})
        if store_levels is not None:
            Path(f"{self.db}.storage-levels").write_text(store_levels)
        if store_format is not None:
            Path(f"{self.db}.storage-format").write_text(store_format)
        self.cp_area = self.db.parent / ".mediacore-checkpoints"
        self.cp_area.mkdir()
        if pointer:
            Path(f"{self.db}.migration-checkpoint").write_text(
                json.dumps({"name": CHECKPOINT, "state": "completed"})
            )
            (self.cp_area / CHECKPOINT / "db").mkdir(parents=True)
            doc = {
                "install_id": "gotham-1",
                "from_vector": {
                    "versions": {"books": 66},
                    "levels": checkpoint_levels or R1,
                    "format": 13,
                },
                "from_release": from_release,
                "taken_at": "2026-11-01T02:03:04Z",
                "status": "completed",
            }
            doc.update(record or {})
            (self.cp_area / f"{CHECKPOINT}.json").write_text(json.dumps(doc))
        self._install_helpers({"stage_bin": str(self.stage), **(config or {})})
        self._install_environment(env_snapshot)
        self.server = fake_ssh.HealthServer([READY])
        tc.addCleanup(self.server.close)
        all_rules = list(rules or [])
        all_rules += [
            {"match": UNIT_SHOW, "responses": shows or [fake_ssh.show("active")]},
            {"match": STOPPED_SHOW, "responses": stopped or [unit_state("inactive")]},
            {"match": INVOCATION_SHOW, "responses": [{"stdout": OLD_INVOCATION + "\n"}]},
            {
                "match": r"^sudo -n /usr/bin/systemctl (restart|start|stop) audiobook-organizer\.service$",
                "responses": [{}],
            },
        ]
        self.h = fake_ssh.install(
            self.root,
            all_rules,
            path_map=[
                [guard.HELPER_DIR, str(self.libexec)],
                [guard.HOST_CONFIG, str(self.config)],
                ["/usr/bin/cat ", "cat "],
                [
                    f"systemctl show -p ActiveState --value {guard.TRANSIENT_UNIT}",
                    f"{self.host_tools}/systemctl show -p ActiveState --value {guard.TRANSIENT_UNIT}",
                ],
            ],
        )

    def _install_helpers(self, config: dict) -> None:
        """The real helpers, with their fixed paths pointed into the sandbox."""
        self.libexec = self.root / "libexec"
        self.libexec.mkdir()
        etc = self.root / "etc"
        etc.mkdir(mode=0o755)
        self.config = etc / "rollback.conf"
        tools = self.root / "host-tools"
        tools.mkdir()
        self.host_tools = tools
        fakes = {}
        # The fake systemctl answers the restore helper's `show -p
        # ActiveState` for its transient unit from a file (absent: inactive).
        self.transient_state = self.root / "transient-state"
        systemctl = (
            'if "ActiveState" in sys.argv:\n'
            f"    p = {str(self.transient_state)!r}\n"
            '    print(open(p).read().strip() if os.path.exists(p) else "inactive")\n'
            "sys.exit(0)\n"
        )
        for name, body in (("systemd-run", _SYSTEMD_RUN), ("systemctl", systemctl)):
            p = tools / name
            p.write_text(
                _TOOL.format(python=sys.executable, log=str(self.log), name=name, body=body)
            )
            p.chmod(0o755)
            fakes[name] = str(p)
        src = (HELPERS / "rollback_helper.py").read_text()
        for before, after in (
            ('CONFIG = "/etc/audiobook-organizer/rollback.conf"', f"CONFIG = {str(self.config)!r}"),
            (
                'SYSTEMD_DIR = "/etc/systemd/system"',
                f"SYSTEMD_DIR = {str(self.root / 'systemd')!r}",
            ),
            (
                'RUN_DIR = "/run/audiobook-organizer-rollback"',
                f"RUN_DIR = {str(self.root / 'run')!r}",
            ),
            ('"systemctl": "/usr/bin/systemctl"', f'"systemctl": {fakes["systemctl"]!r}'),
            ('"systemd-run": "/usr/bin/systemd-run"', f'"systemd-run": {fakes["systemd-run"]!r}'),
            ('"python3": "/usr/bin/python3"', f'"python3": {sys.executable!r}'),
        ):
            assert src.count(before) == 1, before
            src = src.replace(before, after)
        (self.libexec / "rollback_helper.py").write_text(src)
        lib_line = (
            "/usr/bin/python3 -I /usr/local/libexec/audiobook-organizer/rollback/rollback_helper.py"
        )
        for stub in (
            "inflight-probe",
            "checkpoint-record",
            "restore-offline",
            "unit-type",
            "install-staged",
        ):
            text = (HELPERS / stub).read_text()
            assert lib_line in text, stub
            p = self.libexec / stub
            p.write_text(
                text.replace(lib_line, f"{sys.executable} -I {self.libexec}/rollback_helper.py")
            )
            p.chmod(0o755)
        values = {
            "bin": str(self.bin),
            "db": str(self.db),
            "state_dir": str(self.state),
            "unit": "audiobook-organizer.service",
            "service_user": USER,
            "archive_dir": str(self.archives),
            "checkpoint_dir": "",
            "offline_env": f"HOME={self.state} ABK_STATE_DIR={self.state}",
        }
        values.update(config)
        self.config.write_text("".join(f"{k} = {v}\n" for k, v in values.items() if v is not None))
        self.config.chmod(0o644)

    # The unit and local.conf the PREVIOUS release ran under, kept beside
    # .prev, and the ones the CURRENT release runs under, live on the host.
    PREV_UNIT_MARK = "# the unit the previous release ran under\n"
    PREV_CONF = "[Service]\nEnvironment=BATCAVE_PEERS=192.0.2.0/24\n"
    CUR_CONF = "[Service]\nEnvironment=ABO_CORE_PEERS=192.0.2.0/24\n"

    def _install_environment(self, snapshot: bool) -> None:
        """The host's unit, host.conf and local.conf (the CURRENT release's),
        and, unless snapshot is False, the environment a deploy through the
        helpers kept beside .prev (the PREVIOUS release's), the file
        `make rollback` and `make rollback-restore` put back with it."""
        systemd = self.root / "systemd"
        dropins = systemd / "audiobook-organizer.service.d"
        dropins.mkdir(parents=True)
        for d in (systemd, dropins):
            d.chmod(0o755)
        unit = (REPO_ROOT / "deploy" / "audiobook-organizer.service").read_text()
        unit = (
            unit.replace("User=audiobook", f"User={USER}")
            .replace("Group=audiobook", f"Group={USER}")
            .replace("/usr/local/bin/audiobook-organizer", str(self.bin))
        )
        self.unit_text = unit
        self.unit_file = systemd / "audiobook-organizer.service"
        self.unit_file.write_text(unit)
        (dropins / "host.conf").write_text(
            f"[Service]\nExecStart=\nExecStart={self.bin} serve --db {self.db}\nUser={USER}\n"
        )
        self.local_conf = dropins / "local.conf"
        self.local_conf.write_text(self.CUR_CONF)
        for p in (self.unit_file, dropins / "host.conf", self.local_conf):
            p.chmod(0o644)
        self.envgen = Path(f"{self.bin}.envgen")
        self.prev_sha = hashlib.sha256(self.prev.read_bytes()).hexdigest()
        if snapshot:
            self.write_generation()

    def write_generation(self, sha: str | None = None, **over) -> str:
        """The environment a deploy through the helpers kept for the binary
        with this sha256 (default: the one now at .prev): the PREVIOUS
        release's unit and local.conf, with the store levels it served."""
        sha = sha or self.prev_sha
        self.envgen.mkdir(mode=0o700, exist_ok=True)
        self.envgen.chmod(0o700)
        doc = {
            "version": 2,
            "bin_sha256": sha,
            "seq": 1,
            "binary": {"version": "v0.40.0", "levels": dict(R1), "enforced": False},
            "pinned": False,
            "unit": {"present": True, "text": self.PREV_UNIT_MARK + self.unit_text},
            "local_conf": {"present": True, "text": self.PREV_CONF},
        }
        doc.update(over)
        path = self.envgen / f"{sha}.json"
        path.write_text(json.dumps(doc))
        path.chmod(0o600)
        return sha

    def conf_lines(self) -> list[str]:
        """The Environment= lines of the live local.conf (the helper
        re-renders the drop-in, so its comment header is not compared)."""
        return [
            ln for ln in self.local_conf.read_text().splitlines() if ln.startswith("Environment=")
        ]

    def environment(self) -> tuple[str, str]:
        """The live unit file and local.conf."""
        return self.unit_file.read_text(), self.local_conf.read_text()

    def make(
        self, target: str, guard: Path | None = None, **overrides
    ) -> subprocess.CompletedProcess:
        variables = {
            "DEPLOY_HOST": HOST,
            "DEPLOY_BIN": str(self.bin),
            "DEPLOY_DB": str(self.db),
            "DEPLOY_URL": self.server.url,
            "DEPLOY_UNIT": "audiobook-organizer.service",
            "DEPLOY_READY_TIMEOUT": "5",
            "ROLLBACK_IGNORE_FORMAT": "",
            "ROLLBACK_ARCHIVE": "",
            "RELEASE": "",
            "ROLLBACK_ENV": "",
            "ROLLBACK_GUARD": str(guard or GUARD),
        }
        variables.update(overrides)
        env = dict(self.h["env"])
        env["DEPLOY_WAIT_INTERVAL"] = "0.05"
        env["DEPLOY_RESTART_GRACE"] = "0.5"
        for k in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL"):
            env.pop(k, None)
        args = ["make", "-s", "--no-print-directory", "-C", str(REPO_ROOT), target]
        args += [f"{k}={v}" for k, v in variables.items()]
        return subprocess.run(args, env=env, capture_output=True, text=True, timeout=120)

    def calls(self) -> list[dict]:
        return fake_ssh.read_log(self.log)

    def ssh(self) -> list[str]:
        return [e["command"] for e in self.calls() if e["kind"] == "ssh"]

    def index(self, needle: str) -> int:
        """Position of the first ssh command containing needle, or -1."""
        return next((i for i, c in enumerate(self.ssh()) if needle in c), -1)

    def changes(self) -> list[str]:
        """Every ssh command that changes the host: the binary swap, a unit
        stop/start/restart, or an in-place restore."""
        out = []
        for c in self.ssh():
            if (
                "install-staged rollback apply" in c
                or "systemctl stop" in c
                or "systemctl start" in c
                or "systemctl restart" in c
                or "restore-offline checkpoint" in c
                or "restore-offline archive" in c
            ):
                out.append(c)
        return out

    def restores(self) -> list[dict]:
        """The application runs of `storage restore-offline` in place."""
        return [
            e
            for e in self.calls()
            if e["kind"] == "app"
            and e["args"][:2] == ["storage", "restore-offline"]
            and "--into" not in e["args"]
        ]

    def installed(self) -> str:
        """The fake identity of the binary at DEPLOY_BIN."""
        return fake_ssh.fake_app_name(self.bin)

    def inflight(self, rid: str = "20261109T101010Z-0a1b2c3d") -> Path:
        p = self.state / "restore" / rid / "inflight.json"
        p.parent.mkdir(parents=True)
        p.write_text("{}")
        return p


def out(p: subprocess.CompletedProcess) -> str:
    return p.stdout + p.stderr


class TestRollbackGuardRefusesAfterCutover(unittest.TestCase):
    """05-storage §5S.3.10 row: the guard passes an R1 store with an R0
    .prev, refuses an R2 store with an R1 .prev naming make rollback-restore,
    refuses an in-flight swap of any kind with no override, passes a pending
    intent, and refuses when the probe or a sudoers line is missing, naming
    it (OPS8-7)."""

    def test_passes_r1_store_with_r0_prev(self) -> None:
        host = FakeHost(self, prev={"levels": None, "format": None, "capabilities": None})
        p = host.make("rollback")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.installed(), "prev")
        self.assertEqual(Path(f"{host.bin}.rolled-back").exists(), True)
        self.assertGreater(host.index("systemctl restart"), host.index("rollback apply"))
        self.assertGreater(
            host.index("rollback apply"), host.index("systemctl show -p InvocationID")
        )
        self.assertIn("--print-storage-levels", " ".join(host.ssh()))
        self.assertTrue(
            any(c.startswith(UNIT_SHOW[1:]) for c in host.ssh()), "the wait reads the unit"
        )
        dropin = (
            host.root / "systemd" / "audiobook-organizer.service.d" / "zz-rollback-type-simple.conf"
        )
        self.assertEqual(
            dropin.read_text(), "[Service]\nType=simple\n", "an R0 .prev gets Type=simple"
        )

    def test_refuses_r2_store_with_r1_prev_naming_rollback_restore(self) -> None:
        host = FakeHost(self, store_levels="books 1\ncore 1\n", store_format="2\n")
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("make rollback-restore", out(p))
        self.assertEqual(host.changes(), [])
        self.assertEqual(host.installed(), "current")

    def test_refuses_one_owner_above(self) -> None:
        host = FakeHost(self, store_levels="books 1\ncore 0\n")
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("books", out(p))
        self.assertEqual(host.changes(), [])

    def test_refuses_r1_store_owner_unknown_to_prev(self) -> None:
        """An owner .prev does not know is at level 0 for it; a store level
        above 0 for that owner is above what .prev serves."""
        host = FakeHost(self, store_levels="books 0\ncore 0\nbatsignal 1\n")
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("batsignal", out(p))

    def test_refuses_in_flight_swap_with_no_override(self) -> None:
        host = FakeHost(self)
        host.inflight()
        p = host.make("rollback", ROLLBACK_IGNORE_FORMAT="1")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("in flight", out(p))
        self.assertIn("Boot the current build once", out(p))
        self.assertEqual(host.changes(), [])

    def test_passes_pending_intent(self) -> None:
        host = FakeHost(self)
        (host.state / "restore").mkdir()
        (host.state / "restore" / "pending.json").write_text("{}")
        p = host.make("rollback")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.installed(), "prev")

    def test_refuses_when_probe_sudoers_line_missing(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"inflight-probe",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sudoers", out(p))
        self.assertIn(f"sudo -n {guard.HELPER_DIR}/inflight-probe", out(p))
        self.assertEqual(host.changes(), [])

    def test_refuses_when_state_dir_is_wrong(self) -> None:
        host = FakeHost(self, config={"state_dir": "/srv/no-such-state"})
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("state_dir", out(p))
        self.assertEqual(host.changes(), [])

    def test_refuses_when_levels_sidecar_needs_a_sudoers_line(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"cat \S+\.storage-levels$",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback", ROLLBACK_IGNORE_FORMAT="1")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn(f"cat {host.db}.storage-levels", out(p))
        self.assertEqual(host.changes(), [])

    def test_refuses_when_prev_gives_no_answer(self) -> None:
        host = FakeHost(self, prev={"levels_exit": 2})
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("cannot tell which store levels", out(p))
        self.assertEqual(host.changes(), [])

    def test_pre_core_store_keeps_the_projection_rule(self) -> None:
        """No .storage-levels and no checkpoint pointer (a store no core
        build opened): HEAD's .storage-format rule decides, as before."""
        ok = FakeHost(self, store_levels=None, pointer=False, prev={"levels": None})
        self.assertEqual(ok.make("rollback").returncode, 0)
        refused = FakeHost(
            self, store_levels=None, pointer=False, store_format="2\n", prev={"levels": None}
        )
        p = refused.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("make rollback-restore", out(p))
        self.assertEqual(refused.changes(), [])

    def test_exit_78_after_the_swap_is_named(self) -> None:
        host = FakeHost(self, shows=[fake_ssh.show("failed", "failed", status=78)])
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("install the release the log names first", out(p))


class HelperConfig(unittest.TestCase):
    """Both recipes read the helpers' config first and refuse unless it
    names the binary, store and unit this make run checks."""

    def test_refuses_when_the_helpers_are_not_installed(self) -> None:
        for target in ("rollback", "rollback-restore"):
            host = FakeHost(self)
            host.config.unlink()
            p = host.make(target)
            self.assertNotEqual(p.returncode, 0, target)
            self.assertIn(guard.HOST_CONFIG, out(p))
            self.assertIn("Install the rollback helpers", out(p))
            self.assertEqual(host.changes(), [], target)

    def test_refuses_when_the_config_names_another_store(self) -> None:
        for target in ("rollback", "rollback-restore"):
            host = FakeHost(self, config={"db": "/srv/arkham/audiobooks.pebble"})
            p = host.make(target)
            self.assertNotEqual(p.returncode, 0, target)
            self.assertIn("DEPLOY_DB", out(p))
            self.assertEqual(host.changes(), [], target)


class RollbackRestoreTest(unittest.TestCase):
    """05-storage §5S.3.12 row `make rollback-restore`; 09 §9.2.1."""

    def assert_order(self, host: FakeHost, *needles: str) -> None:
        idx = [host.index(n) for n in needles]
        self.assertNotIn(-1, idx, list(zip(needles, idx)))
        self.assertEqual(
            idx, sorted(idx), f"order {list(zip(needles, idx))}\n" + "\n".join(host.ssh())
        )

    def test_checkpoint_restore_order_and_accept_loss(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback-restore")
        self.assertEqual(p.returncode, 0, out(p))
        self.assert_order(
            host,
            guard.HOST_CONFIG,
            "inflight-probe",  # in-flight probe, service still running
            ".migration-checkpoint",  # the source
            f"checkpoint-record {CHECKPOINT}",
            ".prev --print-storage-levels",  # the level check
            "install-staged rollback check prev",  # the kept environment, before the stop
            ".prev --print-capabilities",  # unit type, before the stop
            "unit-type notify",
            "systemctl show -p InvocationID",
            "systemctl stop",
            "systemctl show -p ActiveState,SubState audiobook",
            f"restore-offline checkpoint {CHECKPOINT}",
            "install-staged rollback apply ",
            "systemctl start",
            "systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus",
        )
        self.assertEqual(
            sum("inflight-probe" in c for c in host.ssh()), 2, "probe before and after the stop"
        )
        self.assertIn(
            f"sudo -n -u {USER} {guard.HELPER_DIR}/checkpoint-record {CHECKPOINT}", host.ssh()
        )
        runs = host.restores()
        self.assertEqual([a["name"] for a in runs], ["current"], "the CURRENT binary restores")
        self.assertEqual(
            runs[0]["args"],
            [
                "storage",
                "restore-offline",
                "--db",
                str(host.db),
                "--checkpoint",
                CHECKPOINT,
                "--accept-loss",
            ],
        )
        self.assertEqual(
            runs[0]["env"], {"HOME": str(host.state), "ABK_STATE_DIR": str(host.state)}
        )
        sdrun = [e for e in host.calls() if e["kind"] == "tool" and e["tool"] == "systemd-run"]
        self.assertIn(f"--uid={USER}", sdrun[0]["argv"], "as the service user, never root")
        self.assertEqual(host.installed(), "prev")
        self.assertIn("credential", out(p))
        self.assertIn("2026-11-01T02:03:04Z", out(p))

    def test_archive_restore_reads_the_archived_store_and_accepts_loss(self) -> None:
        host = FakeHost(self, pointer=False)
        p = host.make("rollback-restore", ROLLBACK_ARCHIVE=ARCHIVE)
        self.assertEqual(p.returncode, 0, out(p))
        scratch = host.cp_area / "scratch" / "rollback-restore-levels"
        self.assert_order(
            host,
            "inflight-probe",
            f"restore-offline into-check {ARCHIVE}",
            ".prev --print-storage-levels",
            "systemctl stop",
            f"restore-offline archive {ARCHIVE}",
            "systemctl start",
        )
        into = [e for e in host.calls() if e["kind"] == "app" and "--into" in e["args"]]
        self.assertEqual(
            into[0]["args"],
            [
                "storage",
                "restore-offline",
                "--db",
                str(host.db),
                "--archive",
                str(host.archive),
                "--into",
                str(scratch),
            ],
        )
        self.assertEqual(
            host.restores()[0]["args"][-3:], ["--archive", str(host.archive), "--accept-loss"]
        )
        self.assertFalse(
            scratch.exists() or Path(f"{scratch}.storage-levels").exists(),
            "the scratch copy holds secrets and is deleted",
        )
        self.assertEqual(host.installed(), "prev")

    def test_refuses_level_mismatch_before_touching_anything(self) -> None:
        """After an R2 hotfix .prev is an R2 build: its levels differ from
        the checkpoint's from_vector, and the refusal names from_release."""
        host = FakeHost(self, prev={"levels": R2})
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("v0.41.0", out(p))
        self.assertIn("books: 0, core: 0", out(p))
        self.assertEqual(host.changes(), [], "nothing stopped, restored or swapped")
        self.assertFalse(any("unit-type" in c for c in host.ssh()))
        self.assertEqual(host.installed(), "current")

    def test_refuses_store_below_prev_too(self) -> None:
        """Equality, not <=: a .prev that serves more would migrate the
        restored store again (DATA-9)."""
        host = FakeHost(self, checkpoint_levels=R1, prev={"levels": {"books": 1, "core": 0}})
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(host.changes(), [])

    def test_refuses_archive_level_mismatch(self) -> None:
        host = FakeHost(self, pointer=False, current={"into_levels": "books 1\ncore 1\n"})
        p = host.make("rollback-restore", ROLLBACK_ARCHIVE=ARCHIVE)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("archive", out(p))
        self.assertEqual(host.changes(), [])
        scratch = host.cp_area / "scratch" / "rollback-restore-levels"
        self.assertFalse(scratch.exists(), "the scratch copy is deleted on a refusal too")

    def test_archive_outside_the_archive_dir_is_refused(self) -> None:
        for bad in ("../data/audiobooks.pebble", "/tmp/joker.tar", "x; touch /tmp/pwned-r127"):
            host = FakeHost(self, pointer=False)
            p = host.make("rollback-restore", ROLLBACK_ARCHIVE=bad)
            self.assertNotEqual(p.returncode, 0, bad)
            self.assertEqual(host.ssh(), [], bad)

    def test_refuses_while_inflight_json_exists(self) -> None:
        host = FakeHost(self)
        host.inflight()
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("in flight", out(p))
        self.assertEqual(host.changes(), [], "refused before the stop: the service keeps running")

    def test_refuses_inflight_that_appears_at_the_stop(self) -> None:
        host = FakeHost(
            self,
            rules=[{"match": r"^sudo -n /usr/bin/systemctl stop ", "responses": [{}], "touch": []}],
        )
        # the stop rule creates inflight.json, as a boot caught mid-swap would
        p_dir = host.state / "restore" / "20261109T111111Z-0e0e0e0e"
        scen = Path(host.h["env"]["FAKE_SSH_SCENARIO"])
        doc = json.loads(scen.read_text())
        doc["rules"][0]["touch"] = [str(p_dir / "inflight.json")]
        scen.write_text(json.dumps(doc))
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("in flight", out(p))
        self.assertEqual(host.restores(), [])
        self.assertEqual(host.installed(), "current")
        self.assertIn("deploy-match-unit-type.sh", out(p), "how to start the current binary again")

    def test_refuses_when_unit_does_not_stay_stopped(self) -> None:
        """Restart=on-failure, a timer or another operator can start the unit
        again: the recipe checks it stayed stopped before restoring."""
        host = FakeHost(self, stopped=[unit_state("activating", "start")])
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("did not stay stopped", out(p))
        self.assertEqual(host.restores(), [])

    def test_unit_started_during_the_restore_stops_before_the_swap(self) -> None:
        host = FakeHost(self, stopped=[unit_state("inactive"), unit_state("active", "running")])
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("did not stay stopped", out(p))
        self.assertEqual(host.installed(), "current")
        self.assertFalse(any("systemctl start" in c for c in host.ssh()))

    def test_service_running_refusal_is_surfaced_not_retried(self) -> None:
        """R1-25's swap lock: restore-offline refuses while a service runs
        or starts. The recipe reports it once and does not retry."""
        host = FakeHost(
            self,
            current={
                "restore_exit": 1,
                "restore_stderr": "Error: a service is running or starting on this store",
            },
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("running or starting", out(p))
        self.assertIn("was not retried", out(p))
        self.assertEqual(sum("restore-offline checkpoint" in c for c in host.ssh()), 1)
        self.assertEqual(len(host.restores()), 1)
        self.assertEqual(host.installed(), "current")
        self.assertFalse(any("systemctl start" in c for c in host.ssh()))

    def test_restore_lost_to_ssh_says_it_keeps_running(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"restore-offline checkpoint",
                    "responses": [{"stderr": "Connection reset\n", "exit": 255}],
                }
            ],
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("keeps running on the host", out(p))
        self.assertIn(guard.TRANSIENT_UNIT, out(p))
        self.assertIn("do NOT start", out(p))
        self.assertEqual(host.installed(), "current")
        # The checks run as the deploy user: the log is group-readable and
        # carries the exit status, so they need no sudo grant.
        check = [ln for ln in out(p).splitlines() if "tail -n 5" in ln]
        self.assertEqual(len(check), 1, out(p))
        self.assertIn(f"systemctl is-active {guard.TRANSIENT_UNIT}", check[0])
        self.assertIn("tail -n 5 /run/audiobook-organizer-rollback/restore.log", check[0])
        self.assertNotIn("sudo", check[0])
        self.assertIn(f"{guard.RESTORE_DONE}exit 0", out(p))

    def test_a_restore_still_running_from_an_earlier_run_is_left_alone(self) -> None:
        host = FakeHost(self)
        host.transient_state.write_text("active\n")
        log = host.root / "run" / "restore.log"
        log.parent.mkdir()
        log.write_text("=== catwoman's restore, lost to ssh ===\n")
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("an earlier restore is still running", out(p))
        self.assertIn("do NOT start", out(p))
        self.assertEqual(log.read_text(), "=== catwoman's restore, lost to ssh ===\n")
        self.assertEqual(host.restores(), [])
        self.assertEqual(host.installed(), "current")
        self.assertFalse(any("systemctl start" in c for c in host.ssh()))

    def test_the_guard_and_the_helper_agree_on_the_restore_markers(self) -> None:
        src = (HELPERS / "rollback_helper.py").read_text()
        for name in ("RESTORE_DONE", "TRANSIENT_UNIT"):
            m = re.search(rf"^{name} = (\"[^\"]*\")$", src, re.M)
            assert m is not None, name
            self.assertEqual(ast.literal_eval(m.group(1)), getattr(guard, name), name)

    # --- RELEASE=<tag> (05-updater 5U.4 fetch-release; 09 9.2.1)

    def test_release_fetches_checks_the_levels_installs_prev_then_restores(self) -> None:
        """After an R2 hotfix .prev is an R2 build; RELEASE names the R1 tag
        the checkpoint was taken under, and make fetches it."""
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertEqual(p.returncode, 0, out(p))
        self.assert_order(
            host,
            guard.HOST_CONFIG,
            "inflight-probe",
            ".migration-checkpoint",
            f"checkpoint-record {CHECKPOINT}",
            f"updater fetch-release --version v0.41.0 --out {host.stage}",
            f"{host.stage} --print-storage-levels",
            "install-staged rollback check $(sha256sum",  # BEFORE .prev is replaced
            "install-staged prev",
            "install-staged rollback check prev",
            ".prev --print-capabilities",
            "unit-type notify",
            "systemctl stop",
            f"restore-offline checkpoint {CHECKPOINT}",
            "install-staged rollback apply ",
            "systemctl start",
        )
        fetch = next(c for c in host.ssh() if "updater fetch-release" in c)
        self.assertIn(
            "ABK_STATE_DIR=", fetch, "a scratch state directory: no access to the service's"
        )
        runs = [e for e in host.calls() if e["kind"] == "app" and e["args"][:1] == ["updater"]]
        self.assertEqual([r["name"] for r in runs], ["current"], "the CURRENT binary fetches")
        self.assertEqual(
            host.installed(), "fetched-v0.41.0", "the fetched release is what is swapped in"
        )
        self.assertFalse(host.stage.exists(), "the staged copy is deleted once installed")
        self.assertEqual(
            fake_ssh.fake_app_name(host.prev), "fetched-v0.41.0", ".prev is the fetched release"
        )
        self.assertFalse(Path(f"{host.bin}.prev.tmp").exists())

    def test_release_needs_no_prev_to_exist(self) -> None:
        host = FakeHost(self, releases={"v0.41.0": {}})
        host.prev.unlink()
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.installed(), "fetched-v0.41.0")

    def test_release_at_other_levels_than_the_source_is_refused_and_prev_kept(self) -> None:
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.42.0": {"levels": R2}})
        p = host.make("rollback-restore", RELEASE="v0.42.0")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("RELEASE=v0.42.0", out(p))
        self.assertIn("books: 0, core: 0", out(p))
        self.assertIn(
            "make rollback-restore RELEASE=v0.41.0", out(p), "names the checkpoint's own release"
        )
        self.assertEqual(host.changes(), [])
        self.assertFalse(any("install-staged prev" in c for c in host.ssh()), host.ssh())
        self.assertFalse(host.stage.exists(), "the fetched file is deleted on a refusal")
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev", ".prev is as it was")
        self.assertEqual(host.installed(), "current")

    def test_a_release_the_source_does_not_have_is_refused_and_prev_kept(self) -> None:
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        p = host.make("rollback-restore", RELEASE="v0.9.9")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("release not found", out(p))
        self.assertIn("updater fetch-release", out(p))
        self.assertEqual(host.changes(), [])
        self.assertFalse(any("install-staged prev" in c for c in host.ssh()))
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev")

    def test_release_is_fetched_only_after_the_source_is_known(self) -> None:
        """No checkpoint: the refusal comes before a download."""
        host = FakeHost(self, pointer=False, releases={"v0.41.0": {}})
        p = host.make("rollback-restore", RELEASE="v0.41.0")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(host.index("updater fetch-release"), -1, host.ssh())

    def test_release_needs_stage_bin_in_the_helpers_config(self) -> None:
        host = FakeHost(self, releases={"v0.41.0": {}}, config={"stage_bin": None})
        p = host.make("rollback-restore", RELEASE="v0.41.0")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("stage_bin", out(p))
        self.assertEqual(host.index("updater fetch-release"), -1)
        self.assertEqual(host.changes(), [])

    def test_a_refusal_after_the_fetch_deletes_the_fetched_file(self) -> None:
        """An earlier restore still running: refused after the levels agree
        and before .prev is replaced."""
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        host.transient_state.write_text("active\n")
        p = host.make("rollback-restore", RELEASE="v0.41.0")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("an earlier restore is still running", out(p))
        self.assertNotEqual(host.index("updater fetch-release"), -1)
        self.assertFalse(host.stage.exists())
        self.assertFalse(any("install-staged prev" in c for c in host.ssh()))
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev")

    def test_a_stage_bin_swapped_after_the_check_is_caught_after_the_install(self) -> None:
        """F8-02 review m2: the levels are read from stage_bin and root copies
        what stage_bin holds when it runs. A deploy upload in between would
        install a binary at other levels as .prev; it is read again and
        refused before the stop."""
        deployed = fake_ssh.fake_app_text(
            {
                "name": "deployed",
                "levels": R2,
                "format": 1,
                "capabilities": "sd_notify",
                "log": os.devnull,
            }
        )
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {"swap_to": deployed}})
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "deployed", ".prev is the swapped file")
        self.assertIn("after the install", out(p))
        self.assertIn("books: 1, core: 1", out(p))
        self.assertIn("replaced", out(p), "says the old .prev is gone")
        self.assertEqual(host.changes(), [], "nothing stopped, restored or swapped")
        self.assertEqual(host.installed(), "current")
        self.assertFalse(host.stage.exists())

    def test_refusals_after_the_install_say_prev_was_replaced(self) -> None:
        """F8-02 review n2: once install-staged prev ran, the old .prev is
        gone, and a later refusal must not say .prev is untouched."""
        host = FakeHost(
            self,
            prev={"levels": R2},
            releases={"v0.41.0": {}},
            rules=[{"match": "unit-type", "responses": [{"exit": 1, "stderr": "no\n"}]}],
        )
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("matching the unit type failed", out(p))
        self.assertIn("release v0.41.0's binary", out(p))
        self.assertIn("replaced", out(p))
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "fetched-v0.41.0")

    def test_a_fetch_that_runs_past_the_time_limit_is_refused_and_prev_kept(self) -> None:
        host = FakeHost(
            self,
            prev={"levels": R2},
            current={"fetch_sleep": 3},
            releases={"v0.41.0": {}},
        )
        # Without the limit this fetch would finish and the restore succeed.
        p = host.make("rollback-restore", RELEASE="v0.41.0", DEPLOY_FETCH_TIMEOUT="0.5")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("did not finish", out(p))
        self.assertEqual(host.changes(), [])
        self.assertFalse(any("install-staged prev" in c for c in host.ssh()))
        # (The fake's fetch outlives the killed ssh and writes stage_bin a moment
        # later, as a real host's orphaned fetch could; the file only ever
        # reaches .prev through install-staged, which was not run.)
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev")

    def test_a_bad_fetch_time_limit_is_refused(self) -> None:
        host = FakeHost(self, releases={"v0.41.0": {}})
        for bad in ("soon", "0", "-3"):
            p = host.make("rollback-restore", RELEASE="v0.41.0", DEPLOY_FETCH_TIMEOUT=bad)
            self.assertNotEqual(p.returncode, 0, bad)
            self.assertIn("DEPLOY_FETCH_TIMEOUT", out(p), bad)
            self.assertEqual(host.changes(), [], bad)

    def test_a_release_that_is_not_a_tag_is_refused_before_any_ssh(self) -> None:
        for bad in ("latest", "dev", "v1.0.0; touch /tmp/pwned-f802", "../x", "v1 2", "1.2.3\nrm"):
            host = FakeHost(self, releases={"v0.41.0": {}})
            p = host.make("rollback-restore", RELEASE=bad)
            self.assertEqual(p.returncode, 2, bad)
            self.assertEqual(host.ssh(), [], bad)

    def test_a_prev_mismatch_without_release_names_the_tag_to_pass(self) -> None:
        host = FakeHost(self, prev={"levels": R2})
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("make rollback-restore RELEASE=v0.41.0", out(p))
        self.assertEqual(host.index("updater fetch-release"), -1, "never fetches unasked")

    def test_no_checkpoint_names_rollback_archive(self) -> None:
        host = FakeHost(self, pointer=False)
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("ROLLBACK_ARCHIVE", out(p))
        self.assertEqual(host.changes(), [])

    def test_shares_the_one_probe_with_rollback(self) -> None:
        a, b = FakeHost(self), FakeHost(self)
        a.make("rollback")
        b.make("rollback-restore")

        def probes(h: FakeHost) -> set[str]:
            return {c for c in h.ssh() if "inflight-probe" in c}

        self.assertEqual(probes(a), probes(b))
        self.assertEqual(probes(a), {f"sudo -n {guard.HELPER_DIR}/inflight-probe"})


class ReviewProbes(unittest.TestCase):
    """T3 review of #4511: the recipe-level probes (P1-P7, MK7), ported from
    the reviewer's scratch probes as real tests."""

    def test_p1_rollback_restart_refused_by_sudo_is_not_ready(self) -> None:
        """sudo refuses the restart: the old process is still active and
        ready, so a wait on /health alone would report the rollback done."""
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"systemctl restart ",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
            shows=[fake_ssh.show("active", invocation=OLD_INVOCATION)],
        )
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertNotIn("Rolled back", out(p))
        self.assertIn("not restarted", out(p))
        # The fast path: a sudo refusal fails at once, naming sudo, with no
        # wait on the unit or on /health (the old process would pass both).
        self.assertIn("sudo refused", out(p))
        restart = host.index("systemctl restart ")
        self.assertGreaterEqual(restart, 0)
        self.assertFalse(any(re.search(UNIT_SHOW, c) for c in host.ssh()[restart:]), host.ssh())
        self.assertEqual(host.server.requests, 0)

    def test_p1_rollback_restart_lost_to_ssh_is_not_ready(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"systemctl restart ",
                    "responses": [
                        {"stderr": "ssh: connect to host: Connection timed out\n", "exit": 255}
                    ],
                }
            ],
            shows=[fake_ssh.show("active", invocation=OLD_INVOCATION)],
        )
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertNotIn("Rolled back", out(p))
        self.assertIn("InvocationID", out(p))

    def test_p2_unit_type_failure_stops_nothing(self) -> None:
        host = FakeHost(
            self,
            prev={"capabilities": None},
            rules=[
                {
                    "match": r"zz-rollback-type-simple|unit-type",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertFalse(any("systemctl stop" in c for c in host.ssh()), host.ssh())
        self.assertEqual(host.changes(), [])

    def test_p3_swap_failure_after_the_restore_says_what_remains(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"cp \S+\.prev |install-staged rollback apply",
                    "responses": [
                        {"stderr": "cp: error writing: No space left on device\n", "exit": 1}
                    ],
                }
            ],
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("store is restored", out(p))
        self.assertIn("do not start", out(p))
        self.assertFalse(any("systemctl start" in c for c in host.ssh()))

    def test_p4_stopped_check_ssh_failure_says_state_unknown(self) -> None:
        host = FakeHost(
            self,
            stopped=[
                {
                    "stdout": "",
                    "stderr": "ssh: connect to host: Connection timed out\n",
                    "exit": 255,
                }
            ],
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("unknown", out(p))
        self.assertNotIn("Something started it", out(p))

    def test_p5_released_checkpoint_refuses_before_the_stop(self) -> None:
        host = FakeHost(self, record={"status": "served", "delete_after": "2026-11-08T00:00:00Z"})
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("released", out(p))
        self.assertFalse(any("systemctl stop" in c for c in host.ssh()))

    def test_p6_pointer_name_injection_is_refused(self) -> None:
        """Already refused at 80f027f90 (the name grammar); pinned here."""
        host = FakeHost(self)
        Path(f"{host.db}.migration-checkpoint").write_text(
            json.dumps({"name": "x; touch /tmp/pwned-r127 #"})
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertFalse(any("touch" in c for c in host.ssh()))

    def test_p7_levels_missing_with_a_pointer_refuses_rollback(self) -> None:
        host = FakeHost(self, store_levels=None, store_format="1\n")
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("migration-checkpoint", out(p))
        self.assertEqual(host.changes(), [])

    def test_g10_record_without_levels_refuses(self) -> None:
        host = FakeHost(self, record={"from_vector": {"versions": {"books": 66}, "format": 13}})
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("from_vector levels", out(p))
        self.assertEqual(host.changes(), [])

    def test_restore_failure_says_how_to_start_the_current_binary(self) -> None:
        host = FakeHost(
            self, current={"restore_exit": 1, "restore_stderr": "Error: checkpoint copy unreadable"}
        )
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("systemctl start audiobook-organizer.service", out(p))
        self.assertIn("deploy-match-unit-type.sh", out(p))

    def test_post_restore_start_says_the_store_is_restored(self) -> None:
        host = FakeHost(self, stopped=[unit_state("inactive"), unit_state("active", "running")])
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("already restored", out(p))

    def test_mk7_rollback_restore_refuses_without_prev(self) -> None:
        host = FakeHost(self)
        host.prev.unlink()
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn(".prev", out(p))
        self.assertFalse(any("restore-offline" in c for c in host.ssh()))
        self.assertEqual(host.changes(), [])

    def test_host_control_characters_are_stripped(self) -> None:
        host = FakeHost(self)
        host.inflight("20261109T101010Z-0a1b2c3d")
        rules = json.loads(Path(host.h["env"]["FAKE_SSH_SCENARIO"]).read_text())
        rules["rules"].insert(
            0,
            {
                "match": "inflight-probe",
                "responses": [{"stdout": "/state/restore/x\x1b]0;pwned\x07/inflight.json\n"}],
            },
        )
        Path(host.h["env"]["FAKE_SSH_SCENARIO"]).write_text(json.dumps(rules))
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertNotIn("\x1b", out(p))
        self.assertNotIn("\x07", out(p))


def parse_sudoers(text: str) -> list[tuple[str, str, list[str] | None]]:
    """(runas, command path, exact args or None for any) for each entry of
    the drop-in sudoers() renders."""
    entries = []
    for line in text.replace("\\\n", "").splitlines():
        if not line.strip():
            continue
        head, _, specs = line.partition(" NOPASSWD: ")
        runas = head.split("(", 1)[1].split(")", 1)[0]
        for spec in specs.split(", "):
            tokens = shlex.split(spec.strip())
            if len(tokens) == 2 and tokens[1] == "":
                entries.append((runas, tokens[0], []))
            elif len(tokens) == 1:
                entries.append((runas, tokens[0], None))
            else:
                entries.append((runas, tokens[0], tokens[1:]))
    return entries


def granted(entries, command: str) -> bool:
    argv = shlex.split(command)
    assert argv[:2] == ["sudo", "-n"], command
    argv = argv[2:]
    runas = "root"
    if argv[0] == "-u":
        runas, argv = argv[1], argv[2:]
    for r, path, args in entries:
        if r == runas and path == argv[0] and (args is None or args == argv[1:]):
            return True
    return False


def recipe(makefile: str, target: str) -> str:
    """The recipe of `target` in makefile text, continuation lines joined."""
    m = re.search(rf"^{re.escape(target)}:[^\n]*\n((?:\t[^\n]*\n|\n(?=\t))*)", makefile, re.M)
    assert m is not None, f"no target {target}"
    return m.group(1).replace("\\\n", " ")


def sudo_commands(shell: str) -> list[str]:
    """Each `sudo ...` in a shell command line, up to its terminator."""
    out = []
    for m in re.finditer(r"(?<![\w-])sudo(?=\s)([^;&|<>()\n]*)", shell):
        out.append(" ".join(("sudo" + m.group(1)).split()))
    return out


def service_environment(host: FakeHost) -> dict[str, str]:
    """The environment systemd would give the service on its next start: the
    unit file's Environment= lines, then each drop-in's, in file-name order
    (systemd.unit(5)). Only the plain KEY=value form the helpers write."""
    env: dict[str, str] = {}
    files = [
        host.unit_file,
        *sorted((host.unit_file.parent / (host.unit_file.name + ".d")).glob("*.conf")),
    ]
    for f in files:
        for line in f.read_text().splitlines():
            line = line.strip()
            if line.startswith("Environment="):
                k, _, v = line[len("Environment=") :].strip('"').partition("=")
                env[k] = v
    return env


def boot_peers(host: FakeHost) -> str:
    """What the binary now at DEPLOY_BIN reports as its peer allowlist when it
    starts under the unit and drop-ins now on the host."""
    p = subprocess.run(
        [str(host.bin), "--print-peers"],
        env={**os.environ, **service_environment(host)},
        capture_output=True,
        text=True,
        check=True,
    )
    return p.stdout.strip()


class RollbackRestoresEnvironment(unittest.TestCase):
    """A rollback restores the unit and local.conf the previous binary ran
    under, with the binary, or refuses before it changes anything.

    After an env-name change the previous build reads only the old names: put
    back alone under the new drop-in, its peer allowlist fails open and its
    CF Access, ABS JWT and owner-email settings are gone."""

    def test_drill_the_previous_build_boots_under_the_pre_flip_environment(self) -> None:
        host = FakeHost(self)
        self.assertEqual(
            boot_peers(host), "OPEN (fails open)", "the current drop-in uses the new names"
        )
        p = host.make("rollback")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.installed(), "prev")
        self.assertEqual(boot_peers(host), "192.0.2.0/24", "the allowlist is back")
        self.assertEqual(host.conf_lines(), ["Environment=BATCAVE_PEERS=192.0.2.0/24"])
        self.assertEqual(host.environment()[0], host.PREV_UNIT_MARK + host.unit_text)

    def test_drill_control_a_binary_only_swap_boots_the_previous_build_open(self) -> None:
        """The failure the fix removes, run on the same host: the binary put
        back by itself (no helper does this any more) and nothing else."""
        host = FakeHost(self)
        shutil.copy2(host.prev, host.bin)
        self.assertEqual(host.installed(), "prev")
        self.assertEqual(boot_peers(host), "OPEN (fails open)")

    def test_no_helper_swaps_the_binary_alone(self) -> None:
        o = guard.Opts(
            host="h",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user=USER,
        )
        self.assertNotIn("swap-prev", guard.sudoers(o, "deploy"))
        self.assertFalse((HELPERS / "swap-prev").exists())

    def test_rollback_restore_puts_the_environment_back_too(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback-restore")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.installed(), "prev")
        self.assertEqual(host.conf_lines(), ["Environment=BATCAVE_PEERS=192.0.2.0/24"])
        self.assertEqual(boot_peers(host), "192.0.2.0/24")

    def test_the_environment_is_checked_before_anything_is_stopped(self) -> None:
        for target in ("rollback", "rollback-restore"):
            with self.subTest(target=target):
                host = FakeHost(self, env_snapshot=False)
                before = host.environment()
                p = host.make(target)
                self.assertNotEqual(p.returncode, 0, out(p))
                self.assertIn("binary alone", out(p))
                self.assertIn("Nothing was stopped", out(p))
                self.assertEqual(host.changes(), [], "no stop, no swap, no restore")
                self.assertEqual(host.installed(), "current")
                self.assertEqual(host.environment(), before)
                self.assertEqual(
                    sum("systemctl show -p InvocationID" in c for c in host.ssh()),
                    0,
                    "refused before the unit is even inspected",
                )

    def test_a_generation_the_helper_refuses_stops_the_rollback_before_any_change(self) -> None:
        host = FakeHost(self)
        host.write_generation(
            unit={"present": True, "text": host.unit_text.replace(f"User={USER}", "User=root")}
        )
        before = host.environment()
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("the kept unit", out(p))
        self.assertEqual(host.changes(), [])
        self.assertEqual(host.environment(), before)
        self.assertEqual(host.restores(), [])

    def test_a_release_with_no_kept_environment_is_refused_before_prev_is_replaced(self) -> None:
        """r1 M1. RELEASE= fetches a build no deploy from here ever installed,
        so no kept environment has its hash. The refusal comes BEFORE
        install-staged prev: .prev, and the environment kept for it, are as
        they were, and the message does not say otherwise."""
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        before = host.environment()
        p = host.make("rollback-restore", RELEASE="v0.41.0")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("ROLLBACK_ENV=<sha256>", out(p))
        self.assertIn(host.prev_sha[:12], out(p), "lists the kept generation it could use")
        self.assertNotIn("replaced", out(p), "nothing was replaced")
        self.assertNotIn("is now release", out(p))
        self.assertEqual(host.changes(), [], "nothing stopped or restored")
        self.assertEqual(host.environment(), before)
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev", ".prev was not replaced")
        self.assertTrue((host.envgen / f"{host.prev_sha}.json").exists(), "its environment is kept")
        self.assertEqual(host.index("install-staged prev"), -1, "never installed")
        self.assertFalse(host.stage.exists(), "the fetched file is deleted")

    def test_a_named_generation_lets_a_release_through(self) -> None:
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.conf_lines(), ["Environment=BATCAVE_PEERS=192.0.2.0/24"])
        self.assertEqual(host.installed(), "fetched-v0.41.0")
        staged = host.index("rollback check $(sha256sum")
        self.assertTrue(0 <= staged < host.index("install-staged prev"), host.ssh())

    def test_a_named_generation_that_served_other_levels_is_refused_before_the_install(
        self,
    ) -> None:
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        sha = host.write_generation(
            "ab" * 32, binary={"version": "v0.50.0", "levels": dict(R2), "enforced": False}
        )
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=sha)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("not the environment this binary needs", out(p))
        self.assertEqual(host.changes(), [])
        self.assertEqual(host.index("install-staged prev"), -1)
        self.assertEqual(fake_ssh.fake_app_name(host.prev), "prev")

    def named_generation_run(self, binary: dict) -> tuple[Any, Any]:
        host = FakeHost(self, prev={"levels": R2}, releases={"v0.41.0": {}})
        sha = host.write_generation("ab" * 32, binary=binary)
        return host, host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=sha)

    def test_a_named_generation_with_no_recorded_levels_is_warned_about_loudly(self) -> None:
        host, p = self.named_generation_run({"version": None, "levels": None, "enforced": False})
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("WARNING: ROLLBACK_ENV", out(p))
        self.assertIn("no recorded store levels", out(p))
        self.assertIn("nothing checks that", out(p))
        self.assertEqual(host.installed(), "fetched-v0.41.0")

    def test_a_named_generation_with_no_recorded_enforced_flag_is_warned_about_loudly(self) -> None:
        host, p = self.named_generation_run(
            {"version": "v0.40.0", "levels": dict(R1), "enforced": None}
        )
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("WARNING: ROLLBACK_ENV", out(p))
        self.assertIn("no recorded enforced flag", out(p))

    def test_a_named_generation_of_an_enforcing_build_is_flagged(self) -> None:
        host, p = self.named_generation_run(
            {"version": "v0.40.0", "levels": dict(R1), "enforced": True}
        )
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("NOTE: ROLLBACK_ENV", out(p))
        self.assertIn("enforces the retired-name list", out(p))
        self.assertNotIn("WARNING: ROLLBACK_ENV", out(p))

    def pair_run(self, kept_enforced, prev_enforced):
        """ROLLBACK_ENV names another kept generation while .prev has its own,
        so the recipe knows both builds' enforced flags and can compare them."""
        host = FakeHost(self)
        host.write_generation(
            host.prev_sha,
            binary={"version": "v0.40.0", "levels": dict(R1), "enforced": prev_enforced},
        )
        sha = host.write_generation(
            "ab" * 32, binary={"version": "v0.39.0", "levels": dict(R1), "enforced": kept_enforced}
        )
        before = host.environment()
        return host, before, host.make("rollback-restore", ROLLBACK_ENV=sha)

    def test_an_enforcing_environment_under_a_binary_that_does_not_enforce_is_refused(self) -> None:
        host, before, p = self.pair_run(kept_enforced=True, prev_enforced=False)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn(
            "REFUSING: ROLLBACK_ENV names the environment of a build that enforces", out(p)
        )
        self.assertIn("predates the config flip", out(p))
        self.assertEqual(host.changes(), [], "nothing stopped or restored")
        self.assertEqual(host.environment(), before)

    def test_a_pre_flip_environment_under_a_binary_that_enforces_is_refused(self) -> None:
        host, before, p = self.pair_run(kept_enforced=False, prev_enforced=True)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("does not enforce the retired-name list, but", out(p))
        self.assertIn("ignores", out(p))
        self.assertEqual(host.changes(), [])
        self.assertEqual(host.environment(), before)

    def test_two_enforcing_builds_roll_back_with_no_note_or_refusal(self) -> None:
        host, _, p = self.pair_run(kept_enforced=True, prev_enforced=True)
        self.assertEqual(p.returncode, 0, out(p))
        self.assertNotIn("NOTE: ROLLBACK_ENV", out(p))
        self.assertNotIn("WARNING: ROLLBACK_ENV", out(p))

    def test_a_binary_with_no_recorded_enforced_flag_gets_the_note_not_a_refusal(self) -> None:
        host, _, p = self.pair_run(kept_enforced=True, prev_enforced=None)
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("NOTE: ROLLBACK_ENV", out(p))
        self.assertIn("cannot tell", out(p))

    def test_a_named_generation_with_fully_recorded_facts_says_nothing_extra(self) -> None:
        host, p = self.named_generation_run(
            {"version": "v0.40.0", "levels": dict(R1), "enforced": False}
        )
        self.assertEqual(p.returncode, 0, out(p))
        self.assertNotIn("WARNING: ROLLBACK_ENV", out(p))
        self.assertNotIn("NOTE: ROLLBACK_ENV", out(p))

    def test_a_refusal_after_the_install_carries_the_state_note(self) -> None:
        """The second check runs once .prev is the fetched file: its refusal
        must say the old .prev is gone."""
        host = FakeHost(
            self,
            prev={"levels": R2},
            releases={"v0.41.0": {}},
            rules=[
                {
                    "match": r"rollback check prev",
                    "responses": [{"stderr": "rollback helper: boom\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback-restore", RELEASE="v0.41.0", ROLLBACK_ENV=host.prev_sha)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("is now release v0.41.0's binary", out(p))
        self.assertEqual(host.changes(), [])

    def test_the_swap_holds_the_hash_the_check_read(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback")
        self.assertEqual(p.returncode, 0, out(p))
        want = hashlib.sha256(host.prev.read_bytes()).hexdigest()
        apply = next(c for c in host.ssh() if "rollback apply" in c)
        self.assertTrue(apply.endswith(f"rollback apply {want}"), apply)

    def test_rollback_env_is_empty_or_a_generation_sha(self) -> None:
        for bad in ("yes", "snapshot", "ab" * 31, "AB" * 32, "ab" * 32 + "0"):
            with self.subTest(value=bad):
                host = FakeHost(self)
                p = host.make("rollback", ROLLBACK_ENV=bad)
                self.assertEqual(p.returncode, 2, out(p))
                self.assertIn("ROLLBACK_ENV", out(p))
                self.assertEqual(host.ssh(), [], "refused before the host is read")

    def test_a_rollback_can_name_the_generation_of_another_binary(self) -> None:
        host = FakeHost(self, env_snapshot=False)
        sha = host.write_generation("cd" * 32)
        p = host.make("rollback", ROLLBACK_ENV=sha)
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(host.conf_lines(), ["Environment=BATCAVE_PEERS=192.0.2.0/24"])
        apply = next(c for c in host.ssh() if "rollback apply" in c)
        self.assertTrue(apply.endswith(f" {sha}"), apply)

    def test_rollback_check_lists_the_generations_and_changes_nothing(self) -> None:
        host = FakeHost(self)
        before = host.environment()
        p = host.make("rollback-check")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn(host.prev_sha, out(p))
        self.assertIn("v0.40.0", out(p), "the version the deploy recorded")
        self.assertEqual(host.changes(), [])
        self.assertEqual(host.environment(), before)
        self.assertFalse(
            any("systemctl" in c or "swap" in c for c in host.ssh()),
            "read-only: one helper call and the config",
        )

    def test_rollback_check_refuses_without_helpers(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback-check", DEPLOY_HOST="")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("DEPLOY_HOST", out(p))

    def test_a_failed_swap_names_the_command_that_finishes_it(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"install-staged rollback apply",
                    "responses": [{"stderr": "rollback helper: boom\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("unit and local.conf failed", out(p))
        self.assertIn("boom", out(p), "the helper's own reason is shown")
        self.assertIn(
            "run make rollback again", out(p), "the command that finishes an interrupted swap"
        )
        self.assertIn(
            "deploy-match-unit-type.sh", out(p), "and the one that matches the unit type back"
        )
        self.assertNotIn("systemctl daemon-reload", out(p), "the deploy user has no such sudo line")
        self.assertFalse(any("systemctl restart" in c for c in host.ssh()))


class SudoersDrift(unittest.TestCase):
    """The drop-in is rendered from the commands the recipes run; this
    checks it grants every one of them, has no `*`, and that the doc's
    block is its output."""

    def test_every_sudo_command_the_recipes_run_is_granted(self) -> None:
        commands = set()
        cases: list[tuple[str, dict[str, str], dict[str, Any]]] = [
            ("rollback", {}, {"prev": {"capabilities": None}}),
            ("rollback", {}, {}),
            ("rollback-check", {}, {}),
            ("rollback-restore", {}, {}),
            ("rollback-restore", {"ROLLBACK_ARCHIVE": ARCHIVE}, {"pointer": False}),
            (
                "rollback-restore",
                {"RELEASE": "v0.41.0", "ROLLBACK_ENV": "@prev_sha"},
                {"releases": {"v0.41.0": {}}},
            ),
        ]
        for target, kw, host_kw in cases:
            host = FakeHost(self, **host_kw)
            kw = {k: host.prev_sha if v == "@prev_sha" else v for k, v in kw.items()}
            p = host.make(target, **kw)
            self.assertEqual(p.returncode, 0, out(p))
            commands |= {
                c.replace(str(host.db), "/path/to/audiobooks.pebble").replace(
                    str(host.bin), "/usr/local/bin/audiobook-organizer"
                )
                for c in host.ssh()
                if c.startswith("sudo")
            }
        o = guard.Opts(
            host="h",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user=USER,
        )
        text = guard.sudoers(o, "deploy")
        self.assertNotIn("*", text)
        entries = parse_sudoers(text)
        for c in sorted(commands):
            self.assertTrue(granted(entries, c), f"not granted by the drop-in: {c}\n{text}")
        self.assertGreaterEqual(len(commands), 10, sorted(commands))

    def test_every_sudo_command_deploy_runs_is_granted(self) -> None:
        """make deploy, make deploy-debug (pprof-dropin, then deploy) and
        make pprof-off are Makefile.local.example's recipes, and
        deploy-match-unit-type.sh runs inside deploy. Running them needs a
        build, so this reads every `sudo` they send over ssh from the text,
        with the make variables filled in, and checks each against the
        drop-in: `sudo -n`, granted, no `*`."""
        o = guard.Opts(
            host="h",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user=USER,
        )
        text = guard.sudoers(o, "deploy")
        entries = parse_sudoers(text)
        local = (REPO_ROOT / "Makefile.local.example").read_text()
        helpers = re.search(r"^DEPLOY_HELPERS := (\S+)$", local, re.M)
        self.assertIsNotNone(helpers, "Makefile.local.example defines DEPLOY_HELPERS")
        self.assertEqual(helpers.group(1), guard.HELPER_DIR)
        values = {
            "$(DEPLOY_BIN)": o.bin,
            "$(DEPLOY_UNIT)": o.unit,
            "$(DEPLOY_HELPERS)": guard.HELPER_DIR,
        }
        commands: list[tuple[str, str]] = []
        for target in ("deploy", "deploy-debug", "pprof-dropin", "pprof-off"):
            body = recipe(local, target)
            for remote in re.findall(r"ssh \$\(DEPLOY_HOST\) '([^']*)'", body):
                for k, v in values.items():
                    remote = remote.replace(k, v)
                commands += [(target, c) for c in sudo_commands(remote)]
        script = (REPO_ROOT / "scripts" / "deploy-match-unit-type.sh").read_text()
        helper = re.search(r"^helper=(\S+)$", script, re.M).group(1)
        for remote in re.findall(r'"(sudo [^"]*)"', script):
            commands += [
                ("deploy-match-unit-type.sh", c)
                for c in sudo_commands(remote.replace("$helper", helper))
            ]
        self.assertGreaterEqual(len(commands), 7, commands)
        for where, c in commands:
            self.assertNotIn("*", c, where)
            self.assertNotIn("$", c, f"{where}: a variable the test does not fill in: {c}")
            self.assertTrue(
                c.startswith("sudo -n "), f"{where}: sudo without -n prompts over ssh: {c}"
            )
            self.assertTrue(
                granted(entries, c), f"{where}: not granted by the drop-in: {c}\n{text}"
            )

    def test_the_doc_block_is_the_rendered_drop_in(self) -> None:
        doc = (REPO_ROOT / "docs" / "system" / "deploy-and-gpu-ops.md").read_text()
        block = doc.split("<!-- sudoers:begin -->", 1)[1].split("<!-- sudoers:end -->", 1)[0]
        body = block.split("```\n", 1)[1].rsplit("```", 1)[0]
        o = guard.Opts(
            host="h", db="/path/to/audiobooks.pebble", bin="/usr/local/bin/audiobook-organizer"
        )
        self.assertEqual(body, guard.sudoers(o, "deploy"))


def mutant(tc: unittest.TestCase, before: str, after: str) -> Path:
    """A copy of the guard with one check broken."""
    src = GUARD.read_text()
    tc.assertEqual(src.count(before), 1, f"mutation anchor not found exactly once: {before!r}")
    d = tempfile.TemporaryDirectory()
    tc.addCleanup(d.cleanup)
    p = Path(d.name) / "storage_format_guard.py"
    p.write_text(src.replace(before, after))
    return p


class MutationProbes(unittest.TestCase):
    """Each refusal scenario must notice its check being removed."""

    def test_inflight_check_removed(self) -> None:
        host = FakeHost(self)
        host.inflight()
        m = mutant(self, "    if found:\n", "    if False:\n")
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)
        self.assertNotEqual(host.changes(), [])

    def test_probe_failure_tolerated(self) -> None:
        host = FakeHost(
            self,
            rules=[
                {
                    "match": r"inflight-probe",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
        )
        m = mutant(
            self,
            '    if r.rc != 0 or r.sudo_refused:\n        return [\n            f"REFUSING: the in-flight',
            '    if False:\n        return [\n            f"REFUSING: the in-flight',
        )
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)

    def test_level_comparison_removed(self) -> None:
        host = FakeHost(self, store_levels="books 1\ncore 1\n")
        m = mutant(self, "    up = above(store, prev)\n", "    up = []\n")
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)
        self.assertEqual(host.installed(), "prev")

    def test_silent_prev_guessed(self) -> None:
        host = FakeHost(self, prev={"levels_exit": 2})
        m = mutant(
            self,
            '        return None, f"`{o.prev} --print-storage-levels` failed ({_cause(r)})"\n',
            '        return {}, "guessed"\n',
        )
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)

    def test_restore_equality_removed(self) -> None:
        host = FakeHost(self, prev={"levels": R2})
        m = mutant(self, "    diff = differs(src.levels, prev)\n", "    diff = []\n")
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)
        self.assertNotEqual(host.changes(), [])

    def test_release_equality_removed(self) -> None:
        """RELEASE's fetched binary is held to the source's levels too. With
        the guard's check removed the recipe's second look at .prev (after the
        install, F8-02 review m2) still refuses before the stop; that look is
        what keeps a restore from going ahead on the install's own result."""
        host = FakeHost(self, releases={"v0.42.0": {"levels": R2}})
        # The kept generation says R2, so the named environment passes its own
        # check and the install happens: the second look is what refuses.
        host.write_generation(binary={"version": "v0.42.0", "levels": dict(R2), "enforced": False})
        m = mutant(self, "    diff = differs(src.levels, prev)\n", "    diff = []\n")
        p = host.make("rollback-restore", guard=m, RELEASE="v0.42.0", ROLLBACK_ENV=host.prev_sha)
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertTrue(any("install-staged prev" in c for c in host.ssh()))
        self.assertIn("after the install", out(p))
        self.assertEqual(host.changes(), [])

    def test_release_tag_check_removed(self) -> None:
        """The tag goes into a remote shell line: only the charset guards it."""
        host = FakeHost(self, releases={"v0.41.0": {}})
        m = mutant(
            self,
            "    if o.release and not _RELEASE_TAG.fullmatch(o.release):\n",
            "    if False:\n",
        )
        host.make("rollback-restore", guard=m, RELEASE="v1.0.0;true")
        self.assertTrue(any("v1.0.0;true" in c for c in host.ssh()), host.ssh())

    def test_stopped_check_removed(self) -> None:
        host = FakeHost(self, stopped=[unit_state("activating", "start")])
        m = mutant(self, '    if state in ("inactive", "failed"):\n', "    if True:\n")
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)

    def test_record_levels_defaulted(self) -> None:
        """G10: a record without from_vector.levels must not read as all 0."""
        host = FakeHost(self, record={"from_vector": {"versions": {"books": 66}, "format": 13}})
        m = mutant(
            self,
            "    levels = parse_vector(json.dumps(raw_levels)) if isinstance(raw_levels, dict) else None\n",
            "    levels = parse_vector(json.dumps(raw_levels)) if isinstance(raw_levels, dict) else {}\n",
        )
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)
        self.assertNotEqual(host.restores(), [])

    def test_released_checkpoint_let_through(self) -> None:
        host = FakeHost(self, record={"status": "served", "delete_after": "2026-11-08T00:00:00Z"})
        m = mutant(
            self,
            "    if status not in _RESTORABLE or released:\n",
            "    if status not in _RESTORABLE:\n",
        )
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)

    def test_pointer_check_removed(self) -> None:
        host = FakeHost(self, store_levels=None, store_format="1\n")
        m = mutant(self, "        if not pointer.missing:\n", "        if False:\n")
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)


if __name__ == "__main__":
    unittest.main()
