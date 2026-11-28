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

import contextlib
import os
import sys
import types
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import service_control_offhost as scm  # noqa: E402
from test_make_rollback import FakeHost, guard, out, unit_state  # noqa: E402

UNIT = "audiobook-organizer.service"
STOP = f"sudo -n /usr/bin/systemctl stop {UNIT}"
START = f"sudo -n /usr/bin/systemctl start {UNIT}"


def timeout_rules(start: str = "5min", stop: str = "1min") -> list[dict]:
    return [
        {
            "match": rf"^systemctl show -p {prop} ",
            "responses": [{"stdout": f"{prop}={value}\n", "exit": 0}],
        }
        for prop, value in (("TimeoutStartUSec", start), ("TimeoutStopUSec", stop))
    ]


class ServiceTargets(unittest.TestCase):
    def host(self, *states: dict, rules=None) -> FakeHost:
        host = FakeHost(self, stopped=list(states), rules=[*(rules or []), *timeout_rules()])
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
            sum(1 for c in host.ssh() if c.startswith("systemctl show -p ActiveState")),
            3,
            "look, then wait twice",
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

    def test_the_wait_comes_from_the_units_own_timeout_plus_a_margin(self) -> None:
        for target, prop, now, shown, seconds_ in (
            ("service-start", "TimeoutStartUSec", "inactive", "5min", 360),
            ("service-stop", "TimeoutStopUSec", "active", "1min", 120),
            ("service-start", "TimeoutStartUSec", "inactive", "1min 30s", 150),
        ):
            host = self.host(
                unit_state(now),
                rules=timeout_rules(start=shown, stop=shown),
            )
            host.h["env"].pop("AORG_OH_WAIT_SEC")
            p = host.make(target, DRY_RUN="1")
            self.assertEqual(p.returncode, 0, out(p))
            self.assertIn(f"({prop} {shown}, so it may take up to {seconds_} s)", p.stdout)
            self.assertIn(f"wait up to {seconds_} s for", p.stdout)

    def test_a_very_long_timeout_caps_the_wait_but_not_the_command(self) -> None:
        host = self.host(unit_state("inactive"), rules=timeout_rules(start="2h"))
        host.h["env"].pop("AORG_OH_WAIT_SEC")
        p = host.make("service-start", DRY_RUN="1")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("so it may take up to 7260 s", p.stdout)
        self.assertIn("wait up to 3600 s for", p.stdout)

    def test_a_timeout_that_cannot_be_read_changes_nothing(self) -> None:
        for target, prop in (
            ("service-start", "TimeoutStartUSec"),
            ("service-stop", "TimeoutStopUSec"),
        ):
            now = "inactive" if target == "service-start" else "active"
            host = self.host(
                unit_state(now),
                rules=[{"match": rf"^systemctl show -p {prop} ", "responses": [{"exit": 1}]}],
            )
            p = host.make(target)
            self.assertNotEqual(p.returncode, 0, out(p))
            self.assertIn(f"cannot read {prop}", out(p))
            self.assertEqual(self.sudos(host), [])

    def test_an_infinite_timeout_uses_the_fallback(self) -> None:
        host = self.host(unit_state("inactive"), rules=timeout_rules(start="infinity"))
        host.h["env"].pop("AORG_OH_WAIT_SEC")
        p = host.make("service-start", DRY_RUN="1")
        self.assertEqual(p.returncode, 0, out(p))
        self.assertIn("is infinity, so 900 s", p.stdout)
        self.assertIn("wait up to 960 s", p.stdout)

    def test_the_commands_are_the_guards_stop_and_start_keys(self) -> None:
        o = guard.Opts(host="h", db="", bin="", unit=UNIT)
        self.assertEqual(guard.commands(o)["stop"], STOP)
        self.assertEqual(guard.commands(o)["start"], START)


class TimespanAndLimits(unittest.TestCase):
    def test_systemd_timespans(self) -> None:
        for text, want in (
            ("5min", 300),
            ("1min 30s", 90),
            ("250ms", 0.25),
            ("1h 2min 3.5s", 3723.5),
            ("90s", 90),
            ("infinity", None),
        ):
            self.assertEqual(scm.timespan_seconds(text), want, text)
        for bad in ("", "soon", "5 min", "5minutes", "-1s", "1min,30s"):
            with self.assertRaises(ValueError, msg=bad):
                scm.timespan_seconds(bad)

    def run_main(self, what: str, states: list[str], start: str = "5min", **extra: str):
        """main() in this process, against a guard whose ssh is canned; returns
        (exit status, the timeouts the commands were run with, the commands)."""
        calls: list[tuple[str, float | None]] = []
        states = list(states)

        def run_remote(host, cmd, timeout=120, stream=False):
            calls.append((cmd, timeout))
            if cmd.startswith("systemctl show -p ActiveState"):
                state = states.pop(0) if len(states) > 1 else states[0]
                return types.SimpleNamespace(rc=0, stdout=f"ActiveState={state}\n", stderr="")
            if cmd.startswith("systemctl show -p Timeout"):
                prop = cmd.split()[3]
                return types.SimpleNamespace(rc=0, stdout=f"{prop}={start}\n", stderr="")
            return types.SimpleNamespace(rc=0, stdout="", stderr="")

        real = guard
        proxy = types.SimpleNamespace(
            Opts=real.Opts,
            _validate=real._validate,
            commands=real.commands,
            clean=real.clean,
            run_remote=run_remote,
        )
        env = {"AORG_OH_WHAT": what, "AORG_OH_HOST": "alfred@batcave", "AORG_OH_POLL_SEC": "0"}
        env.update(extra)
        keep = {k: v for k, v in os.environ.items() if not k.startswith("AORG_OH_")}
        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.dict(os.environ, {**keep, **env}, clear=True))
            stack.enter_context(mock.patch.object(scm, "load_guard", return_value=proxy))
            stack.enter_context(mock.patch.object(scm.time, "sleep"))
            rc = scm.main()
        return rc, calls

    def test_the_command_is_given_the_units_timeout_plus_the_margin(self) -> None:
        for what, states, shown, want in (
            ("start", ["inactive", "active"], "5min", 360),
            ("stop", ["active", "inactive"], "40s", 100),
        ):
            rc, calls = self.run_main(what, states, start=shown)
            self.assertEqual(rc, 0, what)
            (change,) = [t for c, t in calls if c.startswith("sudo")]
            self.assertEqual(change, want, what)

    def test_the_wait_after_the_command_is_derived_unless_given(self) -> None:
        # The unit never gets there: the wait ends after limit + margin seconds of
        # polling, which is counted here by the monotonic clock being advanced.
        clock = {"now": 0.0}

        def monotonic() -> float:
            clock["now"] += 1.0
            return clock["now"]

        with mock.patch.object(scm.time, "monotonic", monotonic):
            rc, calls = self.run_main("start", ["inactive", "activating"], start="2min")
        self.assertEqual(rc, 1)
        looks = sum(1 for c, _ in calls if c.startswith("systemctl show -p ActiveState"))
        # 120 + 60 clock ticks (one per loop pass, plus the deadline setup) of polling.
        self.assertGreater(looks, 150)
        self.assertLess(looks, 200)
        clock["now"] = 0.0
        with mock.patch.object(scm.time, "monotonic", monotonic):
            rc, calls = self.run_main(
                "start", ["inactive", "activating"], start="2min", AORG_OH_WAIT_SEC="5"
            )
        self.assertEqual(rc, 1)
        looks = sum(1 for c, _ in calls if c.startswith("systemctl show -p ActiveState"))
        self.assertLess(looks, 12)


if __name__ == "__main__":
    unittest.main()
