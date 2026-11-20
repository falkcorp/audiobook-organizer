# file: scripts/tests/test_deploy_env_check.py
# version: 0.1.0
# guid: 4e9a7c10-52d3-4b8e-a6f1-3c0d9e7b2a85
# last-edited: 2026-11-19

"""Tests for scripts/deploy_env_check.py (`make deploy-env-check`).

Run with:
    python3 -m unittest discover -s scripts -p 'test_deploy_env_check.py' -v

The retired-name list under test is a small one written here, never the real
deploy/retired-env-names.txt, so a table change cannot move these tests. The
real list is covered by TestRealList below, which only checks that the real
file parses and that the checker flags the names the brief names.
"""

from __future__ import annotations

import contextlib
import io
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

SCRIPTS = pathlib.Path(__file__).resolve().parents[1]
REPO = SCRIPTS.parent
sys.path.insert(0, str(SCRIPTS))
import deploy_env_check as chk  # noqa: E402

LIST_ENFORCED = (
    "# enforced: true\n"
    "WHISPER_REMOTE_URL\twarn\tABO_BOOKS_WHISPER_REMOTE_URL\n"
    "SMTP_FROM\twarn\t-\n"
    "DATABASE_PATH\trefuse\tABO_CORE_STORAGE_PATH\n"
)
LIST_REPORTING = LIST_ENFORCED.replace("enforced: true", "enforced: false")

EXAMPLE = (REPO / "Makefile.local.example").read_text()

# The shape of the real, gitignored Makefile.local: sudo mv, no .prev.
SUDO_MV_MAKEFILE = (
    ".PHONY: deploy deploy-debug\n"
    "deploy: build-linux\n"
    "\t@scp bin $(HOST):/tmp/bin\n"
    "\tssh $(HOST) 'sudo mv /tmp/bin /usr/local/bin/app && sudo cp /tmp/unit /etc/app.service && sudo systemctl restart app'\n"
    "deploy-debug: deploy\n"
    "\t@echo ok\n"
)


NO_UNIT = str(pathlib.Path(tempfile.gettempdir()) / "no-such-dir" / "audiobook-organizer.service")


def run(argv, environ=None):
    """chk.main, with the repo's own unit template swapped for a file that does
    not exist unless the test names one: the real template sets names the
    tests' small lists also hold."""
    argv = list(argv)
    if "--unit-template" not in argv:
        argv += ["--unit-template", NO_UNIT]
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = chk.main(argv, environ={} if environ is None else environ)
    return rc, out.getvalue(), err.getvalue()


class Tmp(unittest.TestCase):
    def setUp(self):
        self._d = tempfile.TemporaryDirectory()
        self.addCleanup(self._d.cleanup)
        self.dir = pathlib.Path(self._d.name)

    def write(self, name, text):
        p = self.dir / name
        p.write_text(text)
        return p


class RetiredInTheUnitTemplate(Tmp):
    """r1 m5: the repo's unit template is environment too: every deploy installs
    a copy of it, so a retired name in it is held to the list like local.conf."""

    UNIT = '[Service]\nUser=audiobook\nEnvironment="WHISPER_REMOTE_URL=http://192.0.2.7:8000"\n'

    def check(self, unit, lst, conf="[Service]\nEnvironment=BATCAVE=1\n"):
        c = self.write("local.conf", conf)
        u = self.write("audiobook-organizer.service", unit)
        lp = self.write("retired.txt", lst)
        return run(
            ["retired", "--local-conf", str(c), "--list", str(lp), "--unit-template", str(u)]
        )

    def test_a_retired_name_in_the_unit_refuses_when_enforced(self):
        rc, _, err = self.check(self.UNIT, LIST_ENFORCED)
        self.assertEqual(rc, 1, err)
        self.assertIn("audiobook-organizer.service", err)
        self.assertIn("WHISPER_REMOTE_URL", err)

    def test_it_only_reports_while_the_list_is_not_enforced(self):
        rc, out, _ = self.check(self.UNIT, LIST_REPORTING)
        self.assertEqual(rc, 0)
        self.assertIn("WHISPER_REMOTE_URL", out)

    def test_a_unit_with_only_current_names_passes(self):
        rc, out, _ = self.check("[Service]\nEnvironment=BATCAVE=1\n", LIST_ENFORCED)
        self.assertEqual(rc, 0)
        self.assertIn("sets no retired env name", out)

    def test_both_files_are_reported(self):
        conf = "[Service]\nEnvironment=SMTP_FROM=robin@example.invalid\n"
        rc, _, err = self.check(self.UNIT, LIST_ENFORCED + "SMTP_FROM\trefuse\t-\n", conf)
        self.assertEqual(rc, 1, err)
        self.assertIn("SMTP_FROM", err)
        self.assertIn("WHISPER_REMOTE_URL", err)

    def test_the_real_unit_template_is_scanned_by_default(self):
        out, err = io.StringIO(), io.StringIO()
        lp = self.write("retired.txt", LIST_ENFORCED + "DATABASE_PATH\trefuse\t-\n")
        c = self.write("local.conf", "[Service]\nEnvironment=BATCAVE=1\n")
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = chk.main(["retired", "--local-conf", str(c), "--list", str(lp)], environ={})
        self.assertEqual(rc, 1, err.getvalue())
        self.assertIn("DATABASE_PATH", err.getvalue())


class Retired(Tmp):
    def check(self, conf, lst=LIST_ENFORCED):
        c = self.write("local.conf", conf)
        lp = self.write("retired.txt", lst)
        return run(["retired", "--local-conf", str(c), "--list", str(lp)])

    def test_a_retired_name_in_the_conf_refuses_when_enforced(self):
        rc, _, err = self.check("[Service]\nEnvironment=WHISPER_REMOTE_URL=http://192.0.2.7:8000\n")
        self.assertEqual(rc, 1)
        self.assertIn("WHISPER_REMOTE_URL", err)
        self.assertIn("ABO_BOOKS_WHISPER_REMOTE_URL", err)
        self.assertIn("Nothing was uploaded", err)

    def test_a_refuse_verdict_says_the_boot_fails(self):
        rc, _, err = self.check("[Service]\nEnvironment=DATABASE_PATH=/x\n")
        self.assertEqual(rc, 1)
        self.assertIn("refuses to boot", err)

    def test_a_name_that_became_no_setting_says_so(self):
        rc, _, err = self.check("[Service]\nEnvironment=SMTP_FROM=bruce@example.com\n")
        self.assertEqual(rc, 1)
        self.assertIn("became no setting", err)

    def test_a_conf_with_only_current_names_passes(self):
        rc, out, _ = self.check(
            "[Service]\nEnvironment=ABO_BOOKS_WHISPER_REMOTE_URL=http://192.0.2.7\n"
        )
        self.assertEqual(rc, 0)
        self.assertIn("no retired env name", out)

    def test_not_enforced_reports_and_passes(self):
        rc, out, err = self.check("[Service]\nEnvironment=SMTP_FROM=x\n", LIST_REPORTING)
        self.assertEqual(rc, 0, err)
        self.assertIn("SMTP_FROM", out)
        self.assertIn("still reads them", out)

    def test_several_names_on_one_line_and_quoted_assignments(self):
        rc, _, err = self.check('[Service]\nEnvironment=A=1 "SMTP_FROM=x y" B=2\n')
        self.assertEqual(rc, 1)
        self.assertIn("SMTP_FROM", err)

    def test_a_continued_environment_line(self):
        rc, _, err = self.check("[Service]\nEnvironment=A=1 \\\n  DATABASE_PATH=/x\n")
        self.assertEqual(rc, 1)
        self.assertIn("DATABASE_PATH", err)

    def test_a_commented_or_non_environment_line_is_not_a_setting(self):
        rc, _, _ = self.check(
            "[Service]\n# Environment=SMTP_FROM=x\nExecStart=/bin/app --db SMTP_FROM=y\nEnvironmentFile=/etc/DATABASE_PATH=z\n"
        )
        self.assertEqual(rc, 0)

    def test_a_name_that_only_contains_a_retired_one_is_not_flagged(self):
        rc, _, _ = self.check("[Service]\nEnvironment=MY_SMTP_FROM=x SMTP_FROM_EXTRA=y\n")
        self.assertEqual(rc, 0)

    def test_missing_conf_is_nothing_to_check(self):
        lp = self.write("retired.txt", LIST_ENFORCED)
        rc, out, _ = run(["retired", "--local-conf", str(self.dir / "absent"), "--list", str(lp)])
        self.assertEqual(rc, 0)
        self.assertIn("no environment to check", out)

    def test_a_list_without_the_marker_is_unusable_not_a_pass(self):
        rc, _, err = self.check("[Service]\n", "SMTP_FROM\twarn\t-\n")
        self.assertEqual(rc, 2)
        self.assertIn("unusable", err)

    def test_a_malformed_row_is_unusable(self):
        rc, _, err = self.check("[Service]\n", "# enforced: true\nSMTP_FROM warn\n")
        self.assertEqual(rc, 2)

    def test_a_missing_list_is_unusable(self):
        c = self.write("local.conf", "[Service]\n")
        rc, _, err = run(["retired", "--local-conf", str(c), "--list", str(self.dir / "none.txt")])
        self.assertEqual(rc, 2)

    def test_an_undecodable_conf_refuses(self):
        c = self.dir / "local.conf"
        c.write_bytes(b"[Service]\nEnvironment=\xff\xfe\n")
        lp = self.write("retired.txt", LIST_ENFORCED)
        rc, _, err = run(["retired", "--local-conf", str(c), "--list", str(lp)])
        self.assertEqual(rc, 1)
        self.assertIn("cannot read", err)


class MakefileLocal(Tmp):
    def check(self, text, environ=None, lst=LIST_REPORTING):
        """The list is pinned (not the repo's) so these tests do not change
        when the flip's wiring flips the real marker."""
        m = self.write("Makefile.local", text)
        args = ["makefile-local", "--makefile-local", str(m)]
        if lst is not None:
            args += ["--list", str(self.write("retired.txt", lst))]
        return run(args, environ)

    def test_the_example_passes(self):
        rc, out, err = self.check(EXAMPLE)
        self.assertEqual(rc, 0, err)

    def test_a_sudo_mv_deploy_is_refused_and_says_what_to_do(self):
        rc, _, err = self.check(SUDO_MV_MAKEFILE)
        self.assertEqual(rc, 1)
        self.assertIn("sudo mv/cp", err)
        self.assertIn("install-staged binary", err)
        self.assertIn("Makefile.local.example", err)
        self.assertIn("DEPLOY_ALLOW_NO_ROLLBACK=1", err)
        self.assertNotIn("sudo systemctl", err)  # no root command is handed over

    def test_the_owner_steps_are_in_order_helper_bundle_first(self):
        """Syncing Makefile.local first makes its first step call a helper
        that is not there: the helper bundle goes in first."""
        rc, _, err = self.check(SUDO_MV_MAKEFILE)
        self.assertEqual(rc, 1)
        self.assertIn("make install-rollback-helpers", err)
        self.assertLess(err.index("FIRST"), err.index("THEN"))
        self.assertLess(err.index("make install-rollback-helpers"), err.index("THEN"))
        self.assertLess(err.index("THEN"), err.index("Makefile.local.example"))

    def test_the_override_deploys_with_a_warning_that_names_the_release_rollback_reaches(self):
        rc, _, err = self.check(SUDO_MV_MAKEFILE, {"DEPLOY_ALLOW_NO_ROLLBACK": "1"})
        self.assertEqual(rc, 0)
        self.assertIn("WARNING", err)
        # Not "rollback will refuse": with an older helper deploy's pair kept,
        # it goes back to THAT release, skipping the one this deploy replaces.
        self.assertNotIn("will refuse after this deploy", err)
        self.assertIn("goes back to THAT release", err)
        self.assertIn("skipping the release this deploy replaces", err)
        self.assertIn("if none did, `make rollback` refuses", err)

    def test_the_override_is_refused_on_the_build_that_carries_the_flip(self):
        """enforced: true is the flip build, which the previous release cannot
        run under the new environment: the override would strand it."""
        rc, out, err = self.check(
            SUDO_MV_MAKEFILE, {"DEPLOY_ALLOW_NO_ROLLBACK": "1"}, lst=LIST_ENFORCED
        )
        self.assertEqual(rc, 1)
        self.assertIn("is not honoured for this build", err)
        self.assertIn("enforced: true", err)
        self.assertIn("make install-rollback-helpers", err)
        self.assertNotIn("WARNING", err)
        self.assertNotIn("deploying although", out + err)

    def test_the_override_is_refused_when_the_list_is_malformed(self):
        rc, _, err = self.check(
            SUDO_MV_MAKEFILE, {"DEPLOY_ALLOW_NO_ROLLBACK": "1"}, "# enforced: maybe\n"
        )
        self.assertEqual(rc, 1, err)
        self.assertIn("is not honoured for this build", err)
        self.assertIn("unusable", err)

    def test_the_override_is_refused_when_the_list_file_is_absent(self):
        m = self.write("Makefile.local", SUDO_MV_MAKEFILE)
        rc, _, err = run(
            ["makefile-local", "--makefile-local", str(m), "--list", str(self.dir / "nope.txt")],
            {"DEPLOY_ALLOW_NO_ROLLBACK": "1"},
        )
        self.assertEqual(rc, 1)
        self.assertIn("is not honoured for this build", err)

    def test_a_compliant_makefile_passes_on_the_flip_build_without_the_override(self):
        rc, _, err = self.check(EXAMPLE, lst=LIST_ENFORCED)
        self.assertEqual(rc, 0, err)

    def test_any_other_override_value_does_not_count(self):
        rc, _, _ = self.check(SUDO_MV_MAKEFILE, {"DEPLOY_ALLOW_NO_ROLLBACK": "yes"})
        self.assertEqual(rc, 1)

    def test_a_deploy_that_never_runs_install_staged_binary_is_refused(self):
        rc, _, err = self.check("deploy:\n\tssh h 'sudo -n /x/install-staged unit'\n")
        self.assertEqual(rc, 1)
        self.assertIn("never runs `install-staged binary`", err)

    def test_sudo_without_n_is_refused(self):
        text = "deploy:\n\tssh h 'sudo /x/install-staged binary /tmp/b'\n"
        rc, _, err = self.check(text)
        self.assertEqual(rc, 1)
        self.assertIn("without -n", err)

    def test_an_echo_that_mentions_sudo_mv_is_not_a_command(self):
        text = "deploy:\n\t@echo 'we used to sudo mv here'\n\tssh h 'sudo -n /x/install-staged binary /tmp/b'\n"
        rc, _, err = self.check(text)
        self.assertEqual(rc, 0, err)

    def test_deploy_debug_with_its_own_sudo_cp_recipe_is_refused(self):
        text = (
            "deploy:\n\tssh h 'sudo -n /x/install-staged binary /tmp/b'\n"
            "deploy-debug:\n\tssh h 'sudo cp /tmp/b /usr/local/bin/app'\n"
        )
        rc, _, err = self.check(text)
        self.assertEqual(rc, 1)
        self.assertIn("`deploy-debug`", err)

    def test_continuation_lines_are_joined(self):
        text = "deploy:\n\tssh h \\\n\t  'sudo -n /x/install-staged binary /tmp/b'\n"
        rc, _, err = self.check(text)
        self.assertEqual(rc, 0, err)

    def test_the_example_passes_the_generation_facts_so_there_is_no_note(self):
        rc, out, err = self.check(EXAMPLE)
        self.assertEqual(rc, 0, err)
        self.assertNotIn("NOTE", out)

    def test_a_deploy_that_passes_no_facts_passes_with_a_note(self):
        text = "deploy:\n\tssh h 'sudo -n /x/install-staged binary'\n"
        rc, out, err = self.check(text)
        self.assertEqual(rc, 0, err)
        self.assertIn("NOTE", out)
        self.assertIn("deploy_generation_args", out)

    def test_no_makefile_local_is_nothing_to_check(self):
        rc, out, _ = run(["makefile-local", "--makefile-local", str(self.dir / "absent")])
        self.assertEqual(rc, 0)
        self.assertIn("no deploy recipe", out)


class MissingDocstring(unittest.TestCase):
    """python -OO drops docstrings, so __doc__ is None. The gate must not
    crash with an AttributeError (which a make recipe reads as a refusal for
    the wrong reason) and must not pass: it says why and exits 2."""

    def test_summary_is_none_without_a_docstring(self):
        with mock.patch.object(chk, "__doc__", None):
            self.assertIsNone(chk.summary())

    def test_main_fails_closed_with_a_clear_message(self):
        with mock.patch.object(chk, "__doc__", None):
            rc, out, err = run([])
        self.assertEqual(rc, 2)
        self.assertIn("docstring is missing", err)
        self.assertIn("-OO", err)
        self.assertIn("Nothing was checked", err)
        self.assertEqual(out, "")

    def test_it_checks_nothing_in_that_case(self):
        with mock.patch.object(chk, "__doc__", None), mock.patch.object(
            chk, "check_retired"
        ) as r, mock.patch.object(chk, "check_makefile_local") as m:
            chk.main([], environ={})
        r.assert_not_called()
        m.assert_not_called()

    def test_python_dash_dash_optimize_really_exits_2(self):
        p = subprocess.run(
            [sys.executable, "-OO", str(SCRIPTS / "deploy_env_check.py")],
            capture_output=True,
            text=True,
            cwd=tempfile.gettempdir(),
        )
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("docstring is missing", p.stderr)

    def test_the_summary_is_the_first_docstring_line(self):
        summary = chk.summary()
        self.assertIsNotNone(summary)
        assert summary is not None
        self.assertTrue(summary.startswith("`make deploy-env-check`"))


class RealList(unittest.TestCase):
    def setUp(self):
        self.enforced, self.rows = chk.parse_list(
            (REPO / "deploy" / "retired-env-names.txt").read_text()
        )

    def test_the_names_the_flip_retires_are_listed(self):
        for name in (
            "WHISPER_REMOTE_URL",
            "WHISPER_ENDPOINTS",
            "SMTP_FROM",
            "ABS_AUTH_PROBE",
            "CF_ACCOUNT_ID",
            "CF_GUESTS_POLICY_ID",
            "CF_GUEST_SERVICE_TOKENS",
            "CF_SERVICE_AUTH_POLICY_ID",
        ):
            self.assertEqual(self.rows[name][0], "warn", name)

    def test_the_flag_is_a_bool(self):
        self.assertIsInstance(self.enforced, bool)

    def test_a_sample_conf_with_a_listed_name_is_flagged(self):
        with tempfile.TemporaryDirectory() as d:
            c = pathlib.Path(d) / "local.conf"
            c.write_text("[Service]\nEnvironment=WHISPER_REMOTE_URL=http://192.0.2.7:8000\n")
            rc, out, err = run(["retired", "--local-conf", str(c)])
        self.assertEqual(rc, 1 if self.enforced else 0, out + err)
        self.assertIn("WHISPER_REMOTE_URL", out + err)


class PreflightRunsTheCheck(Tmp):
    """deploy-preflight.sh is the one thing a not-yet-synced Makefile.local
    still calls, so it must run the checker, in the caller's directory."""

    def test_the_script_ends_by_running_it(self):
        text = (SCRIPTS / "deploy-preflight.sh").read_text()
        self.assertIn('python3 "$(dirname "$0")/deploy_env_check.py"', text)

    def test_the_example_deploy_targets_depend_on_the_target(self):
        self.assertIn("deploy: deploy-env-check", EXAMPLE)
        self.assertIn("deploy-debug: deploy-env-check pprof-dropin deploy", EXAMPLE)


if __name__ == "__main__":
    unittest.main()
