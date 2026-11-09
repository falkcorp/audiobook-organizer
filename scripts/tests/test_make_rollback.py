# file: scripts/tests/test_make_rollback.py
# version: 1.0.0
# guid: 17d15b8e-ad9f-4804-af7c-e1f0e4b72b13
# last-edited: 2026-11-09

"""`make rollback` and `make rollback-restore` against a fake host.

09 §9.4 R1 Verification ("scripts/tests/test_make_rollback.py on its fake-ssh
harness"), 05-storage §5S.3.7 "The binary-swap guard" and its test row
`TestRollbackGuardRefusesAfterCutover`, §5S.3.12 row `make rollback-restore`;
R1-27.

Each test runs the real Makefile target. `ssh` is scripts/tests/fake_ssh.py:
systemctl answers are scripted; every other command (the sidecar reads, the
in-flight `find`, the binaries, `cp`, the scratch `rm`) runs for real inside a
temporary sandbox that plays the host's filesystem. The application binaries
are fake ones whose answers are baked into their own text, so the `cp` that
installs `.prev` carries its identity with it. Every host variable is passed
on the make command line, so a developer's Makefile.local can never point a
test at a real host.

The mutation probes at the end run a refusal scenario against a copy of the
guard with one safety check broken (ROLLBACK_GUARD names the copy) and assert
that the recipe then goes ahead, which proves the scenario's assertions would
catch that regression.

Run with:
    python3 -m unittest discover -s scripts -p 'test_make_rollback.py' -v
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import fake_ssh  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
GUARD = REPO_ROOT / "scripts" / "storage_format_guard.py"
HOST = "wayne-manor.invalid"
USER = "alfred"
CHECKPOINT = "20261101T020304Z-0badc0de"
R1 = {"books": 0, "core": 0}
R2 = {"books": 1, "core": 1}
READY = (200, '{"status":"ok","ready":true}')
UNIT_SHOW = r"^systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus "
STOPPED_SHOW = r"^systemctl show -p ActiveState,SubState "


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
        from_release="v0.41.0",
        pointer=True,
        rules=None,
        stopped=None,
        shows=None,
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
        self.archive = self.root / "archives" / "batcave-20261101.tar.gz"
        self.archive.parent.mkdir()
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
        if pointer:
            Path(f"{self.db}.migration-checkpoint").write_text(
                json.dumps({"name": CHECKPOINT, "state": "completed"})
            )
            area = self.db.parent / ".mediacore-checkpoints"
            area.mkdir()
            (area / CHECKPOINT / "db").mkdir(parents=True)
            (area / f"{CHECKPOINT}.json").write_text(
                json.dumps(
                    {
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
                )
            )
        self.server = fake_ssh.HealthServer([READY])
        tc.addCleanup(self.server.close)
        all_rules = list(rules or [])
        all_rules += [
            {"match": UNIT_SHOW, "responses": shows or [fake_ssh.show("active")]},
            {"match": STOPPED_SHOW, "responses": stopped or [unit_state("inactive")]},
            {
                "match": r"^sudo -n systemctl (restart|start|stop) audiobook-organizer\.service$",
                "responses": [{}],
            },
            {"match": r"systemctl daemon-reload", "responses": [{}]},
            {"match": r"zz-rollback-type-simple", "responses": [{}]},
        ]
        self.h = fake_ssh.install(self.root, all_rules)

    def make(
        self, target: str, guard: Path | None = None, **overrides
    ) -> subprocess.CompletedProcess:
        variables = {
            "DEPLOY_HOST": HOST,
            "DEPLOY_BIN": str(self.bin),
            "DEPLOY_DB": str(self.db),
            "DEPLOY_URL": self.server.url,
            "DEPLOY_STATE_DIR": str(self.state),
            "DEPLOY_SERVICE_USER": USER,
            "DEPLOY_READY_TIMEOUT": "5",
            "DEPLOY_CHECKPOINT_DIR": "",
            "ROLLBACK_IGNORE_FORMAT": "",
            "ROLLBACK_ARCHIVE": "",
            "RELEASE": "",
            "ROLLBACK_GUARD": str(guard or GUARD),
        }
        variables.update(overrides)
        env = dict(self.h["env"])
        env["DEPLOY_WAIT_INTERVAL"] = "0.05"
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
        """Every ssh command that changes the host: a binary copy, a unit
        stop/start/restart, or an in-place restore."""
        out = []
        for c in self.ssh():
            if (
                " cp " in f" {c}"
                or "systemctl stop" in c
                or "systemctl start" in c
                or "systemctl restart" in c
                or "restore-offline" in c
                and "--into" not in c
            ):
                out.append(c)
        return out

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
        self.assertGreater(host.index("systemctl restart"), host.index(" cp "))
        self.assertIn("--print-storage-levels", " ".join(host.ssh()))
        self.assertTrue(
            any(
                "systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus" in c
                for c in host.ssh()
            ),
            "the wait reads the unit",
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
                    "match": r"/usr/bin/find ",
                    "responses": [{"stderr": "sudo: a password is required\n", "exit": 1}],
                }
            ],
        )
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sudoers", out(p))
        self.assertIn(f"sudo -n /usr/bin/find {host.state} -maxdepth 3", out(p))
        self.assertEqual(host.changes(), [])

    def test_refuses_when_state_dir_is_wrong(self) -> None:
        host = FakeHost(self)
        p = host.make("rollback", DEPLOY_STATE_DIR=str(host.root / "no-such-state"))
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("DEPLOY_STATE_DIR", out(p))
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
        """No .storage-levels (a store no core build opened): HEAD's
        .storage-format rule decides, as before."""
        ok = FakeHost(self, store_levels=None, prev={"levels": None})
        self.assertEqual(ok.make("rollback").returncode, 0)
        refused = FakeHost(self, store_levels=None, store_format="2\n", prev={"levels": None})
        p = refused.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("make rollback-restore", out(p))
        self.assertEqual(refused.changes(), [])

    def test_exit_78_after_the_swap_is_named(self) -> None:
        host = FakeHost(self, shows=[fake_ssh.show("failed", "failed", status=78)])
        p = host.make("rollback")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("install the release the log names first", out(p))


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
            "/usr/bin/find",  # in-flight probe, service still running
            ".migration-checkpoint",  # the source
            f"{CHECKPOINT}.json",
            ".prev --print-storage-levels",  # the level check
            "systemctl stop",
            "systemctl show -p ActiveState,SubState audiobook",
            "storage restore-offline",
            ".prev --print-capabilities",
            "cp ",
            "systemctl start",
            "systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus",
        )
        self.assertEqual(
            sum("/usr/bin/find" in c for c in host.ssh()), 2, "probe before and after the stop"
        )
        restore = next(c for c in host.ssh() if "restore-offline" in c)
        self.assertIn(f"--checkpoint {CHECKPOINT} --accept-loss", restore)
        self.assertTrue(
            restore.startswith(
                f"sudo -n -u {USER} /usr/bin/env HOME={host.state} "
                f"ABK_STATE_DIR={host.state} {host.bin} "
            ),
            restore,
        )
        self.assertIn(f"--db {host.db}", restore)
        apps = [
            e
            for e in host.calls()
            if e["kind"] == "app" and e["args"][:2] == ["storage", "restore-offline"]
        ]
        self.assertEqual([a["name"] for a in apps], ["current"], "the CURRENT binary restores")
        self.assertEqual(host.installed(), "prev")
        self.assertIn("credential", out(p))
        self.assertIn("2026-11-01T02:03:04Z", out(p))

    def test_archive_restore_reads_the_archived_store_and_accepts_loss(self) -> None:
        host = FakeHost(self, pointer=False)
        p = host.make("rollback-restore", ROLLBACK_ARCHIVE=str(host.archive))
        self.assertEqual(p.returncode, 0, out(p))
        scratch = host.db.parent / ".mediacore-checkpoints" / "scratch" / "rollback-restore-levels"
        self.assert_order(
            host,
            "/usr/bin/find",
            f"--into {scratch}",
            f"cat {scratch}.storage-levels",
            ".prev --print-storage-levels",
            "systemctl stop",
            f"--archive {host.archive} --accept-loss",
            "systemctl start",
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
        p = host.make("rollback-restore", ROLLBACK_ARCHIVE=str(host.archive))
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("archive", out(p))
        self.assertEqual(host.changes(), [])
        scratch = host.db.parent / ".mediacore-checkpoints" / "scratch" / "rollback-restore-levels"
        self.assertFalse(scratch.exists(), "the scratch copy is deleted on a refusal too")

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
            rules=[
                {
                    "match": r"^sudo -n systemctl stop ",
                    "responses": [{}],
                    "touch": [],
                }
            ],
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
        self.assertFalse(any("restore-offline" in c and "--into" not in c for c in host.ssh()))
        self.assertEqual(host.installed(), "current")

    def test_refuses_when_unit_does_not_stay_stopped(self) -> None:
        """Restart=on-failure, a timer or another operator can start the unit
        again: the recipe checks it stayed stopped before restoring."""
        host = FakeHost(self, stopped=[unit_state("activating", "start")])
        p = host.make("rollback-restore")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("did not stay stopped", out(p))
        self.assertFalse(any("restore-offline" in c and "--into" not in c for c in host.ssh()))

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
        self.assertEqual(sum("restore-offline" in c for c in host.ssh()), 1)
        self.assertEqual(host.installed(), "current")
        self.assertFalse(any("systemctl start" in c for c in host.ssh()))

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
            return {c.replace(str(h.state), "<state>") for c in h.ssh() if "/usr/bin/find" in c}

        self.assertEqual(probes(a), probes(b))
        self.assertEqual(len(probes(a)), 1)


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
                    "match": r"/usr/bin/find ",
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
        m = mutant(
            self, '    if r.rc == 0 and state in ("inactive", "failed"):\n', "    if True:\n"
        )
        self.assertEqual(host.make("rollback-restore", guard=m).returncode, 0)

    def test_accept_loss_dropped(self) -> None:
        host = FakeHost(self)
        m = mutant(
            self,
            '    cmd = o.offline(f"{src.args} --accept-loss")\n',
            "    cmd = o.offline(src.args)\n",
        )
        host.make("rollback-restore", guard=m)
        restore = [c for c in host.ssh() if "restore-offline" in c]
        self.assertTrue(restore and not any("--accept-loss" in c for c in restore))


if __name__ == "__main__":
    unittest.main()
