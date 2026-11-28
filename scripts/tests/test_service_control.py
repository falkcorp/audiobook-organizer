# file: scripts/tests/test_service_control.py
# version: 0.1.0
# guid: 77fade53-9653-47a5-938f-abe7156aff59
# last-edited: 2026-11-28

"""Tests for scripts/service_control_offhost.py: `make service-stop` and
`make service-start`. The real recipe and script run against the FakeHost of
test_make_rollback.py (a fake ssh in a sandbox); nothing here touches a real
host. The owner-side twins are tested in test_owner_scripts.py.

Run with:
    python3 -m unittest scripts.tests.test_service_control -v
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_make_rollback import FakeHost, guard, out, unit_state  # noqa: E402

UNIT = "audiobook-organizer.service"
STOP = f"sudo -n /usr/bin/systemctl stop {UNIT}"
START = f"sudo -n /usr/bin/systemctl start {UNIT}"


class ServiceTargets(unittest.TestCase):
    def host(self, *states: dict, rules=None) -> FakeHost:
        host = FakeHost(self, stopped=list(states), rules=rules)
        host.h["env"]["AORG_OH_POLL_SEC"] = "0"
        host.h["env"]["AORG_OH_WAIT_SEC"] = "1"
        return host

    def sudos(self, host: FakeHost) -> list[str]:
        return [c for c in host.ssh() if c.startswith("sudo")]

    def test_stop_runs_the_one_granted_command_and_waits_until_inactive(self) -> None:
        host = self.host(
            unit_state("active"), unit_state("deactivating", "stop-sigterm"), unit_state("inactive")
        )
        p = host.make("service-stop")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(self.sudos(host), [STOP])
        self.assertIn(f"{UNIT} is inactive", p.stdout)
        self.assertEqual(
            sum(1 for c in host.ssh() if c.startswith("systemctl show")), 3, "look, then wait twice"
        )

    def test_start_runs_the_one_granted_command_and_waits_until_active(self) -> None:
        host = self.host(
            unit_state("inactive"),
            unit_state("activating", "start"),
            unit_state("active", "running"),
        )
        p = host.make("service-start")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual(self.sudos(host), [START])
        self.assertIn(f"{UNIT} is active", p.stdout)

    def test_already_in_the_state_runs_nothing(self) -> None:
        for target, now in (("service-stop", "inactive"), ("service-start", "active")):
            host = self.host(unit_state(now))
            p = host.make(target)
            self.assertEqual(p.returncode, 0, out(p))
            self.assertIn("nothing to do", p.stdout, target)
            self.assertEqual(self.sudos(host), [], target)

    def test_dry_run_changes_nothing(self) -> None:
        for target, now, cmd in (
            ("service-stop", "active", STOP),
            ("service-start", "failed", START),
        ):
            host = self.host(unit_state(now))
            p = host.make(target, DRY_RUN="1")
            self.assertEqual(p.returncode, 0, out(p))
            self.assertIn("dry run only", p.stdout)
            self.assertIn(cmd, p.stdout)
            self.assertEqual(self.sudos(host), [])

    def test_an_unreadable_state_changes_nothing(self) -> None:
        for target in ("service-stop", "service-start"):
            host = self.host(rules=[{"match": r"^systemctl show ", "responses": [{"exit": 1}]}])
            p = host.make(target)
            self.assertNotEqual(p.returncode, 0, out(p))
            self.assertIn("cannot read the state", out(p))
            self.assertEqual(self.sudos(host), [])

    def test_a_command_that_fails_is_a_failure(self) -> None:
        for target, now in (("service-stop", "active"), ("service-start", "inactive")):
            host = self.host(
                unit_state(now),
                rules=[
                    {
                        "match": r"^sudo -n /usr/bin/systemctl (stop|start) ",
                        "responses": [{"exit": 1}],
                    }
                ],
            )
            p = host.make(target)
            self.assertNotEqual(p.returncode, 0, out(p))
            self.assertIn("exited 1", out(p))

    def test_a_unit_that_never_gets_there_times_out_naming_the_state(self) -> None:
        host = self.host(unit_state("active"), unit_state("deactivating"))
        host.h["env"]["AORG_OH_WAIT_SEC"] = "0"
        p = host.make("service-stop")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("still deactivating after 0 seconds; wanted inactive", out(p))

    def test_a_started_unit_that_fails_ends_the_wait_at_once(self) -> None:
        host = self.host(unit_state("inactive"), unit_state("failed", "failed"))
        host.h["env"]["AORG_OH_WAIT_SEC"] = "3600"
        p = host.make("service-start")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("is failed; read its journal", out(p))

    def test_a_stopped_unit_that_ends_failed_is_not_stopped(self) -> None:
        host = self.host(unit_state("active"), unit_state("failed", "failed"))
        host.h["env"]["AORG_OH_WAIT_SEC"] = "0"
        p = host.make("service-stop")
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("still failed after 0 seconds; wanted inactive", out(p))

    def test_bad_values_are_refused_before_ssh(self) -> None:
        for label, kw, env in (
            ("no host", {"DEPLOY_HOST": ""}, {}),
            ("a host that is an ssh option", {"DEPLOY_HOST": "-oProxyCommand=x"}, {}),
            ("a unit with a space", {"DEPLOY_UNIT": "a b.service"}, {}),
            ("a unit that is not a service", {"DEPLOY_UNIT": "a.timer"}, {}),
            ("a wait that is not a number", {}, {"AORG_OH_WAIT_SEC": "soon"}),
            ("a negative poll", {}, {"AORG_OH_POLL_SEC": "-1"}),
        ):
            host = self.host(unit_state("active"))
            host.h["env"].update(env)
            p = host.make("service-stop", **kw)
            self.assertNotEqual(p.returncode, 0, f"{label}\n{out(p)}")
            self.assertEqual(host.ssh(), [], label)

    def test_the_commands_are_the_guards_stop_and_start_keys(self) -> None:
        o = guard.Opts(host="h", db="", bin="", unit=UNIT)
        self.assertEqual(guard.commands(o)["stop"], STOP)
        self.assertEqual(guard.commands(o)["start"], START)


if __name__ == "__main__":
    unittest.main()
