# file: scripts/tests/test_check_deploy_env_names.py
# version: 1.0.0
# guid: 7e1b9d44-2a6c-4f08-b3d5-9c0a4e8f6b12
# last-edited: 2026-11-19
"""Tests for scripts/check_deploy_env_names.py (F3-07 e6).

The check iterates the rename-table golden for HEAD env names and fails when a
deploy file, compose file, Dockerfile, script or skill still carries one. The
tests cover the golden parse, both matching modes, the file set (including the
gitignored deploy/local.conf, which must never be read), the allowlist (and its
stale-entry failure), the exit code, and the real tree.
"""

from __future__ import annotations

import contextlib
import io
import pathlib
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "scripts"))

import check_deploy_env_names as chk  # noqa: E402

# A tiny golden: the real one is parsed in RealGolden. FILLER rows lift the
# parse over check()'s "found almost nothing" floor of 100 names.
FILLER = "".join(f"retired env FILLER_NAME_{i} -> core.x.y warn\n" for i in range(120))
GOLDEN_TEXT = (
    "target key core.paths.state_dir convert=- env=ABO_CORE_PATHS_STATE_DIR\n"
    "retired env ABK_STATE_DIR -> core.paths.state_dir refuse\n"
    "retired env WHISPER_REMOTE_URL -> modules.books.whisper.remote_url warn\n"
    "retired env OPENAI_API_KEY -> integrations.ai.openai.api_key kept\n"
    "retired env ABO_CORE_PATHS_STATE_DIR -> core.paths.state_dir warn\n"  # also a canonical env
    "retired env TOOLS -> core.tools warn\n"
    "retired env CATALOG.ENABLED -> modules.books.catalog.enabled warn\n"
    "retired env SMTP_PASSWORD -> integrations.notify.email.password refuse Refuse\n"
    + FILLER
)


def make_tree(files: dict[str, str], golden: str = GOLDEN_TEXT, git: bool = True,
              ignore: str = "") -> pathlib.Path:
    root = pathlib.Path(tempfile.mkdtemp(prefix="envcheck-"))
    (root / chk.GOLDEN).parent.mkdir(parents=True)
    (root / chk.GOLDEN).write_text(golden)
    for rel, text in files.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
    if git:
        subprocess.run(["git", "-C", str(root), "init", "-q"], check=True)
        if ignore:
            (root / ".gitignore").write_text(ignore)
    return root


class RetiredNames(unittest.TestCase):
    def setUp(self):
        self.names = chk.retired_names(GOLDEN_TEXT)

    def test_refuse_and_warn_rows_are_retired(self):
        self.assertEqual(self.names["ABK_STATE_DIR"], "core.paths.state_dir")
        self.assertIn("WHISPER_REMOTE_URL", self.names)
        self.assertIn("SMTP_PASSWORD", self.names)  # the extra Refuse column

    def test_kept_rows_are_not_retired(self):
        self.assertNotIn("OPENAI_API_KEY", self.names)

    def test_a_name_that_is_also_a_canonical_env_is_not_retired(self):
        self.assertNotIn("ABO_CORE_PATHS_STATE_DIR", self.names)

    def test_every_filler_row_parsed(self):
        self.assertEqual(sum(n.startswith("FILLER_NAME_") for n in self.names), 120)


class Matching(unittest.TestCase):
    def setUp(self):
        self.find = chk.build_matcher(chk.retired_names(GOLDEN_TEXT))

    def test_underscore_name_matches_anywhere_including_prose(self):
        self.assertEqual(self.find("# set WHISPER_REMOTE_URL on the host"), ["WHISPER_REMOTE_URL"])
        self.assertEqual(self.find("Environment=ABK_STATE_DIR=/x"), ["ABK_STATE_DIR"])
        self.assertEqual(self.find('"-e", "SMTP_PASSWORD"'), ["SMTP_PASSWORD"])

    def test_dotted_name_matches_as_a_token(self):
        self.assertEqual(self.find("CATALOG.ENABLED=true"), ["CATALOG.ENABLED"])
        self.assertEqual(self.find("see CATALOG.ENABLED in the table"), ["CATALOG.ENABLED"])

    def test_canonical_name_containing_a_retired_tail_does_not_match(self):
        self.assertEqual(self.find("Environment=ABO_CORE_PATHS_STATE_DIR=/x"), [])
        self.assertEqual(self.find("X_ABK_STATE_DIR and ABK_STATE_DIR_X"), [])
        self.assertEqual(self.find("ABO_BOOKS_WHISPER_REMOTE_URL=http://192.0.2.5"), [])

    def test_single_word_name_matches_only_in_environment_syntax(self):
        self.assertEqual(self.find("TOOLS=1"), ["TOOLS"])
        self.assertEqual(self.find("<key>TOOLS</key>"), ["TOOLS"])
        self.assertEqual(self.find("ENV TOOLS /opt"), ["TOOLS"])
        self.assertEqual(self.find("TOOLS = {"), [])  # a Python name
        self.assertEqual(self.find('TOOLS["find"]'), [])
        self.assertEqual(self.find("the TOOLS directory"), [])
        self.assertEqual(self.find("MY_TOOLS=1"), [])

    def test_a_line_with_two_names_reports_both(self):
        self.assertEqual(
            sorted(self.find("ABK_STATE_DIR=1 WHISPER_REMOTE_URL=2")),
            ["ABK_STATE_DIR", "WHISPER_REMOTE_URL"],
        )


class FileSet(unittest.TestCase):
    def run_check(self, files, allow=(), **kw):
        root = make_tree(files, **kw)
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))
        return chk.check(root, allow)

    def test_each_shipped_file_kind_is_scanned(self):
        files = {
            "deploy/unit.service": "Environment=ABK_STATE_DIR=/x\n",
            "deploy/sub/readme.md": "set WHISPER_REMOTE_URL\n",
            "docker-compose.yml": "    environment:\n      - ABK_STATE_DIR\n",
            "Dockerfile": "ENV ABK_STATE_DIR=/x\n",
            "Dockerfile.build-cgo": "ENV ABK_STATE_DIR /x\n",
            "scripts/run.sh": "export ABK_STATE_DIR=/x\n",
            ".claude/skills/s/SKILL.md": "use SMTP_PASSWORD\n",
        }
        got = self.run_check(files)
        for rel in files:
            self.assertTrue(any(p.startswith(rel + ":") for p in got), (rel, got))
        self.assertEqual(len(got), len(files))

    def test_finding_names_file_line_and_canonical_path(self):
        got = self.run_check({"deploy/a.conf": "# x\n# y\nABK_STATE_DIR=1\n"})
        self.assertEqual(got, ["deploy/a.conf:3: ABK_STATE_DIR is retired; use core.paths.state_dir's canonical env name"])

    def test_clean_tree_has_no_findings(self):
        self.assertEqual(self.run_check({"deploy/u.service": "Environment=ABO_CORE_PATHS_STATE_DIR=/x\n"}), [])

    def test_gitignored_local_conf_is_never_read(self):
        got = self.run_check({"deploy/local.conf": "ABK_STATE_DIR=/real\n"}, ignore="deploy/local.conf\n")
        self.assertEqual(got, [])

    def test_any_gitignored_file_is_skipped(self):
        got = self.run_check({"deploy/host.conf": "ABK_STATE_DIR=/real\n"}, ignore="deploy/host.conf\n")
        self.assertEqual(got, [])

    def test_local_conf_is_skipped_without_git_too(self):
        got = self.run_check({"deploy/local.conf": "ABK_STATE_DIR=/real\n", "deploy/b.conf": "ABK_STATE_DIR=1\n"}, git=False)
        self.assertEqual([p.split(":")[0] for p in got], ["deploy/b.conf"])

    def test_files_outside_the_set_are_not_scanned(self):
        self.assertEqual(self.run_check({"docs/x.md": "ABK_STATE_DIR\n", "internal/x.go": "ABK_STATE_DIR\n"}), [])


class Allowlist(unittest.TestCase):
    def run_check(self, files, allow):
        root = make_tree(files)
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))
        return chk.check(root, allow)

    def test_glob_with_no_names_allows_every_name(self):
        allow = (chk.Allow("scripts/r2/**", frozenset(), "why"),)
        files = {"scripts/r2/a/b.py": "ABK_STATE_DIR=1 WHISPER_REMOTE_URL=2\n"}
        self.assertEqual(self.run_check(files, allow), [])

    def test_named_entry_allows_only_those_names(self):
        allow = (chk.Allow("scripts/x.py", frozenset({"ABK_STATE_DIR"}), "why"),)
        got = self.run_check({"scripts/x.py": "ABK_STATE_DIR=1 WHISPER_REMOTE_URL=2\n"}, allow)
        self.assertEqual(len(got), 1)
        self.assertIn("WHISPER_REMOTE_URL", got[0])

    def test_allowlist_does_not_leak_to_other_files(self):
        allow = (chk.Allow("scripts/x.py", frozenset(), "why"),)
        got = self.run_check({"scripts/x.py": "ABK_STATE_DIR\n", "scripts/y.py": "ABK_STATE_DIR\n"}, allow)
        self.assertEqual([p.split(":")[0] for p in got], ["scripts/y.py"])

    def test_stale_entry_fails(self):
        allow = (chk.Allow("scripts/gone.py", frozenset(), "reason text"),)
        got = self.run_check({"deploy/ok.conf": "ABO_CORE_PATHS_STATE_DIR=1\n"}, allow)
        self.assertEqual(len(got), 1)
        self.assertIn("matched nothing", got[0])
        self.assertIn("reason text", got[0])

    def test_every_real_entry_has_a_reason(self):
        for a in chk.ALLOW:
            self.assertGreater(len(a.reason.split()), 3, a.glob)


class Golden(unittest.TestCase):
    def test_a_parse_that_finds_almost_nothing_fails_loudly(self):
        root = make_tree({"deploy/ok.conf": "x\n"}, golden="# empty\n")
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))
        got = chk.check(root, ())
        self.assertEqual(len(got), 1)
        self.assertIn("golden format changed", got[0])


class RealGolden(unittest.TestCase):
    def setUp(self):
        self.names = chk.retired_names((REPO_ROOT / chk.GOLDEN).read_text())

    def test_the_real_table_is_iterated(self):
        self.assertGreater(len(self.names), 300)
        for n in ("ABK_STATE_DIR", "WHISPER_REMOTE_URL", "ACOUSTID_API_KEY", "BASIC_AUTH_PASSWORD"):
            self.assertIn(n, self.names)

    def test_ecosystem_names_are_kept(self):
        for n in ("OPENAI_API_KEY", "OPENAI_BASE_URL", "OTEL_EXPORTER_OTLP_ENDPOINT",
                  "OTEL_METRIC_EXPORT_INTERVAL", "ABK_PPROF_ADDR"):
            self.assertNotIn(n, self.names)


class Main(unittest.TestCase):
    def run_main(self, files):
        root = make_tree(files)
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = chk.main(["--root", str(root)], ())
        return code, out.getvalue(), err.getvalue()

    def test_exit_one_with_findings_printed(self):
        code, out, err = self.run_main({"deploy/a.conf": "ABK_STATE_DIR=1\n"})
        self.assertEqual(code, 1)
        self.assertIn("deploy/a.conf:1: ABK_STATE_DIR", out)
        self.assertIn("1 finding", err)

    def test_exit_zero_when_clean(self):
        code, out, err = self.run_main({"deploy/a.conf": "ABO_CORE_PATHS_STATE_DIR=1\n"})
        self.assertEqual((code, out, err), (0, "", ""))


class RealTree(unittest.TestCase):
    def test_the_repository_carries_no_retired_env_name(self):
        self.assertEqual(chk.check(REPO_ROOT), [])

    def test_the_script_exits_zero_on_the_repository(self):
        r = subprocess.run([sys.executable, str(REPO_ROOT / "scripts/check_deploy_env_names.py")],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)


if __name__ == "__main__":
    unittest.main()
