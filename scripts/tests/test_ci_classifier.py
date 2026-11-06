# file: scripts/tests/test_ci_classifier.py
# version: 1.0.0
# guid: bdb67332-0e68-4d5a-ba7f-f83eb036cb28
# last-edited: 2026-11-06
"""Gate tests for the ``changes`` classifier and the required-check workflows (09 §9.2.1).

The classifier is the ``Classify changed paths`` step of ci.yml's ``changes`` job.
It decides which jobs a PR runs; a docs glob that hides a file a Go test or a
``//go:embed`` reads would skip a job that could have failed (OPS5-4). These tests:

  * run the step's real ``run:`` script under bash with a stub ``git`` on PATH that
    prints a given file list, and assert the ``go`` / ``web`` outputs;
  * scan the Go sources for every Markdown file a test reads (``os.ReadFile`` /
    ``os.Open`` of a ``.md`` literal) or embeds (``//go:embed`` matching ``*.md``)
    and require ``go=true`` for each, and prove the scan fires on a fixture that
    adds one without a classifier case;
  * require extraction-gates.yml's copy of the step to be identical to ci.yml's;
  * require ``merge_group`` in the ``on:`` of every workflow that defines a job
    named in 09 §9.2.1's required-check list (a queued PR otherwise waits forever
    for a check that never runs on the queue's commit);
  * require every job that ``needs: changes`` to skip through a job-level ``if:``
    (a skipped job reports success; a trigger-level filter leaves the check
    "expected"), and every extraction-gates job to set ``timeout-minutes`` <= 30
    (09 §9.2.10).

Stdlib only: the workflows are read as text, not parsed as YAML.
"""

from __future__ import annotations

import glob as globlib
import os
import posixpath
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github/workflows"
CI_YML = WORKFLOWS / "ci.yml"
EXTRACTION_YML = WORKFLOWS / "extraction-gates.yml"
STEP_NAME = "Classify changed paths"

# The job names 09 §9.2.1's required-checks table lists. A workflow that defines
# one of them (by job id or by `name:`) must also run on the merge queue. Later
# rows add their job here by existing: R1-04 `docker-image`, R1-28 `unit-lifecycle`.
REQUIRED_CHECKS = (
    "e2e",
    "extraction-gates",
    "vet-darwin",
    "vet-integration",
    "unit-lifecycle",
    "docker-image",
    "core-ci",
)

SKIP_DIRS = {".git", "node_modules", ".worktrees", "vendor", "dist"}


# -- reading workflows as text -------------------------------------------------


def extract_step(workflow: Path, step_name: str) -> tuple[str, str]:
    """(the step's whole text, its ``run:`` script dedented) from a workflow file."""
    lines = workflow.read_text().splitlines()
    start = next(
        (
            i
            for i, ln in enumerate(lines)
            if re.match(rf"^\s*- name: {re.escape(step_name)}\s*$", ln)
        ),
        None,
    )
    if start is None:
        raise AssertionError(f"{workflow.name}: no step named {step_name!r}")
    indent = len(lines[start]) - len(lines[start].lstrip())
    end = len(lines)
    for i in range(start + 1, len(lines)):
        ln = lines[i]
        if ln.strip() and (len(ln) - len(ln.lstrip())) <= indent:
            end = i
            break
    block = lines[start:end]
    run_at = next(
        (i for i, ln in enumerate(block) if re.match(r"^\s*run: \|\s*$", ln)),
        None,
    )
    if run_at is None:
        raise AssertionError(f"{workflow.name}: step {step_name!r} has no `run: |` block")
    run_indent = len(block[run_at]) - len(block[run_at].lstrip())
    script = [
        ln
        for ln in block[run_at + 1 :]
        if not ln.strip() or len(ln) - len(ln.lstrip()) > run_indent
    ]
    return "\n".join(block).rstrip() + "\n", textwrap.dedent("\n".join(script)).rstrip() + "\n"


def on_block(workflow: Path) -> str:
    """The text of the top-level ``on:`` mapping."""
    lines = workflow.read_text().splitlines()
    out: list[str] = []
    inside = False
    for ln in lines:
        if re.match(r"^(on|'on'|\"on\"):", ln):
            inside = True
            out.append(ln)
            continue
        if inside:
            if ln.strip() and not ln.startswith((" ", "#")):
                break
            out.append(ln)
    return "\n".join(out)


def jobs(workflow: Path) -> dict[str, dict[str, str]]:
    """{job id: {"name": ..., "text": the job's lines}} from the ``jobs:`` mapping."""
    lines = workflow.read_text().splitlines()
    try:
        at = next(i for i, ln in enumerate(lines) if ln.rstrip() == "jobs:")
    except StopIteration:
        return {}
    found: dict[str, dict[str, str]] = {}
    current: str | None = None
    buf: list[str] = []

    def flush() -> None:
        if current is not None:
            text = "\n".join(buf)
            m = re.search(r"^    name:\s*(.+?)\s*$", text, re.M)
            found[current] = {"name": m.group(1).strip("'\"") if m else "", "text": text}

    for ln in lines[at + 1 :]:
        if ln.strip() and not ln.startswith((" ", "#")):
            break
        m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", ln)
        if m:
            flush()
            current, buf = m.group(1), []
        elif current is not None:
            buf.append(ln)
    flush()
    return found


def job_names_in(workflow: Path) -> set[str]:
    out: set[str] = set()
    for job_id, info in jobs(workflow).items():
        out.add(job_id)
        if info["name"]:
            out.add(info["name"])
    return out


# -- running the classifier ----------------------------------------------------


def classify(script: str, files: list[str], event: str = "pull_request") -> dict[str, str]:
    """Run a classifier script with ``git diff --name-only`` stubbed to print ``files``."""
    bash = shutil.which("bash")
    if not bash:
        raise unittest.SkipTest("needs bash")
    with tempfile.TemporaryDirectory() as tmp:
        tmpd = Path(tmp)
        (tmpd / "files.txt").write_text("".join(f"{f}\n" for f in files))
        stub = tmpd / "git"
        stub.write_text('#!/bin/sh\nif [ "$1" = diff ]; then cat "$STUB_FILES"; else exit 1; fi\n')
        stub.chmod(0o755)
        out_file = tmpd / "github_output"
        out_file.write_text("")
        env = {
            "PATH": f"{tmpd}{os.pathsep}{os.environ['PATH']}",
            "STUB_FILES": str(tmpd / "files.txt"),
            "GITHUB_OUTPUT": str(out_file),
            "EVENT": event,
            "BASE_REF": "main",
            "BEFORE": "0" * 40,
            "AFTER": "f" * 40,
        }
        res = subprocess.run([bash, "-c", script], env=env, capture_output=True, text=True)
        if res.returncode != 0:
            raise AssertionError(f"classifier exited {res.returncode}: {res.stderr}")
        return dict(ln.split("=", 1) for ln in out_file.read_text().splitlines() if "=" in ln)


# -- scanning Go sources for Markdown inputs -----------------------------------

_READ_CALL = re.compile(r"\bos\.(?:ReadFile|Open)\(")
_STRING_LIT = re.compile(r'"([^"\n]*)"|`([^`\n]*)`')
_EMBED = re.compile(r"^\s*//go:embed\s+(.+?)\s*$")


def _go_files(root: Path):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if name.endswith(".go"):
                yield Path(dirpath) / name


def _repo_relative(root: Path, go_file: Path, literal: str) -> str:
    """Resolve a literal against the Go file's directory (tests run there) then the root."""
    for base in (go_file.parent, root):
        candidate = (base / literal).resolve()
        try:
            rel = candidate.relative_to(root.resolve())
        except ValueError:
            continue
        if candidate.is_file():
            return rel.as_posix()
    here = go_file.parent.resolve().relative_to(root.resolve()).as_posix()
    return posixpath.normpath(posixpath.join(here, literal))


def markdown_inputs(root: Path) -> set[str]:
    """Repo-relative ``.md`` paths that a Go file reads or embeds."""
    found: set[str] = set()
    for go_file in _go_files(root):
        try:
            lines = go_file.read_text(errors="replace").splitlines()
        except OSError:
            continue
        is_test = go_file.name.endswith("_test.go")
        for ln in lines:
            if is_test and _READ_CALL.search(ln):
                lits = [a or b for a, b in _STRING_LIT.findall(ln)]
                if any(s.endswith(".md") for s in lits):
                    if "filepath.Join(" in ln or "path.Join(" in ln:
                        literal = "/".join(s for s in lits if s)
                    else:
                        literal = next(s for s in reversed(lits) if s.endswith(".md"))
                    found.add(_repo_relative(root, go_file, literal))
            m = _EMBED.match(ln)
            if m:
                for pattern in re.findall(r'"([^"]+)"|(\S+)', m.group(1)):
                    pat = pattern[0] or pattern[1]
                    if pat.startswith("all:"):
                        pat = pat[4:]
                    if not pat.endswith(".md") and "*.md" not in pat:
                        continue
                    for hit in globlib.glob(str(go_file.parent / pat), recursive=True):
                        if hit.endswith(".md"):
                            found.add(Path(hit).resolve().relative_to(root.resolve()).as_posix())
    return found


def inputs_without_a_case(root: Path, script: str) -> list[str]:
    """Markdown inputs for which the classifier says ``go=false``."""
    return sorted(p for p in markdown_inputs(root) if classify(script, [p]).get("go") != "true")


# -- tests ---------------------------------------------------------------------


class ClassifierTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        _, cls.script = extract_step(CI_YML, STEP_NAME)

    def check(self, files, go, web):
        got = classify(self.script, files)
        self.assertEqual((got["go"], got["web"]), (go, web), f"{files}: {got}")

    def test_docs_only_pr_skips_go_and_web(self):
        self.check(
            ["docs/plans/x.md", "README.md", "changelog.d/x.md", "todo.d/x.md"], "false", "false"
        )

    def test_web_source_sets_web_only(self):
        self.check(["web/src/x.tsx"], "false", "true")

    def test_core_file_sets_go(self):
        self.check(["core/log/sanitize.go"], "true", "false")
        self.check(["core/go.mod"], "true", "false")

    def test_generated_ops_table_sets_go_and_web(self):
        self.check(["web/src/generated/ops.ts"], "true", "true")

    def test_generated_types_and_contract_goldens_set_go(self):
        self.check(["web/src/types/generated/x.ts"], "true", "true")
        self.check(["web/src/types/contract/x.json"], "true", "true")

    def test_workflow_change_sets_web_and_go(self):
        got = classify(self.script, [".github/workflows/ci.yml"])
        self.assertEqual((got["go"], got["web"]), ("true", "true"))

    def test_any_go_file_sets_go(self):
        self.check(["internal/server/x.go", "docs/x.md"], "true", "false")

    def test_no_diff_range_fails_open(self):
        got = classify(self.script, [], event="merge_group")
        self.assertEqual((got["go"], got["web"]), ("true", "true"))

    def test_every_markdown_a_go_test_or_embed_reads_sets_go(self):
        self.assertEqual(
            inputs_without_a_case(ROOT, self.script),
            [],
            "a Go test or //go:embed reads these Markdown files, but the classifier's "
            "docs glob hides them: add a `go=true` case before the docs line in ci.yml "
            "and in extraction-gates.yml (09 §9.2.1; OPS5-4)",
        )

    def test_scan_fires_on_a_fixture_with_no_classifier_case(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs").mkdir()
            (root / "docs/contract.md").write_text("# contract\n")
            (root / "pkg").mkdir()
            (root / "pkg/x_test.go").write_text(
                "package pkg\n\nfunc TestX(t *testing.T) {\n"
                '\tb, _ := os.ReadFile("../docs/contract.md")\n\t_ = b\n}\n'
            )
            (root / "pkg/embed.go").write_text(
                "package pkg\n\n//go:embed notes/*.md\nvar notes embed.FS\n"
            )
            (root / "pkg/notes").mkdir()
            (root / "pkg/notes/a.md").write_text("a\n")
            self.assertEqual(markdown_inputs(root), {"docs/contract.md", "pkg/notes/a.md"})
            self.assertEqual(
                inputs_without_a_case(root, self.script),
                ["docs/contract.md", "pkg/notes/a.md"],
            )
            # ...and passes once the classifier has a case for each.
            patched = self.script.replace(
                "web/*|docs/*|*.md|",
                "docs/contract.md|pkg/notes/*.md) go=true ;;\n                web/*|docs/*|*.md|",
                1,
            )
            self.assertNotEqual(patched, self.script)
            self.assertEqual(inputs_without_a_case(root, patched), [])

    def test_scan_ignores_a_literal_that_is_only_asserted_on(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "x_test.go").write_text(
                'package x\n\nfunc TestX(t *testing.T) { want := "docs/plans/y.md"; _ = want }\n'
            )
            self.assertEqual(markdown_inputs(root), set())

    def test_extraction_gates_copy_is_identical(self):
        ci_block, ci_script = extract_step(CI_YML, STEP_NAME)
        ex_block, ex_script = extract_step(EXTRACTION_YML, STEP_NAME)
        self.assertEqual(ci_script, ex_script)
        self.assertEqual(
            textwrap.dedent(ci_block), textwrap.dedent(ex_block), "env block and step header differ"
        )


class WorkflowShapeTests(unittest.TestCase):
    def test_required_workflows_have_merge_group(self):
        checked = 0
        for wf in sorted(WORKFLOWS.glob("*.yml")):
            required = job_names_in(wf) & set(REQUIRED_CHECKS)
            if not required:
                continue
            checked += 1
            self.assertIn(
                "merge_group",
                on_block(wf),
                f"{wf.name} defines required check(s) {sorted(required)} but has no "
                "`merge_group` trigger: a queued PR would wait forever for it (D15)",
            )
        self.assertGreaterEqual(checked, 3)  # ci.yml's vet jobs, extraction-gates, core-ci

    def test_ci_yml_runs_on_the_merge_queue(self):
        self.assertIn("merge_group", on_block(CI_YML))

    def test_merge_group_check_detects_a_missing_trigger(self):
        with tempfile.TemporaryDirectory() as tmp:
            wf = Path(tmp) / "w.yml"
            wf.write_text("name: W\non:\n  pull_request:\n\njobs:\n  vet-darwin:\n    runs-on: x\n")
            self.assertIn("vet-darwin", job_names_in(wf) & set(REQUIRED_CHECKS))
            self.assertNotIn("merge_group", on_block(wf))

    def test_jobs_that_need_changes_skip_through_a_job_level_if(self):
        for wf in (CI_YML, EXTRACTION_YML):
            for job_id, info in jobs(wf).items():
                needs = re.search(r"^    needs:\s*(.+)$", info["text"], re.M)
                if not needs or not re.search(r"\bchanges\b", needs.group(1)):
                    continue
                cond = re.search(r"^    if:\s*(.+)$", info["text"], re.M)
                self.assertIsNotNone(
                    cond, f"{wf.name}: job {job_id} needs changes but has no job-level if:"
                )
                self.assertRegex(
                    cond.group(1),
                    r"needs\.changes\.outputs|always\(\)",
                    f"{wf.name}: job {job_id}'s if: does not read the classifier",
                )

    def test_extraction_gates_jobs_are_bounded_and_parallel(self):
        found = jobs(EXTRACTION_YML)
        self.assertIn("check-commits-build", found)
        self.assertIn("extraction-gates", found)
        for job_id, info in found.items():
            m = re.search(r"^    timeout-minutes:\s*(\d+)\s*$", info["text"], re.M)
            self.assertIsNotNone(m, f"{job_id}: no timeout-minutes")
            self.assertLessEqual(
                int(m.group(1)), 30, f"{job_id}: timeout-minutes > 30 (09 §9.2.10)"
            )

    def test_extraction_gates_terminal_job_fails_on_failed_or_cancelled(self):
        found = jobs(EXTRACTION_YML)
        text = found["extraction-gates"]["text"]
        self.assertEqual(found["extraction-gates"]["name"], "extraction-gates")
        self.assertRegex(text, r"if:\s*\$\{\{\s*always\(\)\s*\}\}|if:\s*always\(\)")
        self.assertIn("failure", text)
        self.assertIn("cancelled", text)
        # every other job is a prerequisite of it, so a new job cannot be forgotten.
        needs = re.search(r"^    needs:\s*\[?([^\]\n]+)\]?", text, re.M).group(1)
        for job_id in found:
            if job_id != "extraction-gates":
                self.assertIn(job_id, needs, f"terminal job does not wait for {job_id}")

    def test_vet_jobs_exist_with_the_required_check_names(self):
        found = jobs(CI_YML)
        for name in ("vet-darwin", "vet-integration"):
            self.assertIn(name, found)
            self.assertEqual(found[name]["name"], name, "the ruleset keys on the job name")
            self.assertRegex(found[name]["text"], rf"(?m)run: make {name}\s*$")
            self.assertIn("needs.changes.outputs.go == 'true'", found[name]["text"])
            m = re.search(r"^    timeout-minutes:\s*(\d+)", found[name]["text"], re.M)
            self.assertIsNotNone(m)
            self.assertLessEqual(int(m.group(1)), 30)


if __name__ == "__main__":
    unittest.main()
