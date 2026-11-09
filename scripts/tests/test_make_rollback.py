# file: scripts/tests/test_make_rollback.py
# version: 1.2.0
# guid: 17d15b8e-ad9f-4804-af7c-e1f0e4b72b13
# last-edited: 2026-11-09

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

import importlib.util
import json
import re
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

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

_spec = importlib.util.spec_from_file_location("storage_format_guard", GUARD)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
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
        self._install_helpers(config or {})
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
        fakes = {}
        for name, body in (("systemd-run", _SYSTEMD_RUN), ("systemctl", "sys.exit(0)")):
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
            "swap-prev",
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
                "swap-prev" in c
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
        self.assertGreater(host.index("systemctl restart"), host.index("swap-prev"))
        self.assertGreater(host.index("swap-prev"), host.index("systemctl show -p InvocationID"))
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
            ".prev --print-capabilities",  # unit type, before the stop
            "unit-type notify",
            "systemctl show -p InvocationID",
            "systemctl stop",
            "systemctl show -p ActiveState,SubState audiobook",
            f"restore-offline checkpoint {CHECKPOINT}",
            "swap-prev",
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

    def test_release_refuses_naming_f8(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback-restore", RELEASE="v0.41.0")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("needs F8's updater fetch-release", out(p))
        self.assertEqual(host.ssh(), [], "refused before any ssh")

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
                    "match": r"cp \S+\.prev |swap-prev",
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


class SudoersDrift(unittest.TestCase):
    """The drop-in is rendered from the commands the recipes run; this
    checks it grants every one of them, has no `*`, and that the doc's
    block is its output."""

    def test_every_sudo_command_the_recipes_run_is_granted(self) -> None:
        commands = set()
        for target, kw, host_kw in (
            ("rollback", {}, {"prev": {"capabilities": None}}),
            ("rollback", {}, {}),
            ("rollback-restore", {}, {}),
            ("rollback-restore", {"ROLLBACK_ARCHIVE": ARCHIVE}, {"pointer": False}),
        ):
            host = FakeHost(self, **host_kw)
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
            '        return None, f"`{o.bin}.prev --print-storage-levels` failed ({_cause(r)})"\n',
            '        return {}, "guessed"\n',
        )
        self.assertEqual(host.make("rollback", guard=m).returncode, 0)

    def test_restore_equality_removed(self) -> None:
        host = FakeHost(self, prev={"levels": R2})
        m = mutant(self, "    diff = differs(src.levels, prev)\n", "    diff = []\n")
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)
        self.assertNotEqual(host.changes(), [])

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
