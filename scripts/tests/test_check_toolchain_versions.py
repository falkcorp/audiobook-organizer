# file: scripts/tests/test_check_toolchain_versions.py
# version: 1.4.1
# guid: 741ea392-1f28-423c-ae7a-45e56620c26c
# last-edited: 2026-10-11
"""Tests for scripts/check_toolchain_versions.py (CI-04, CI-03).

The inline shell check this replaced truncated go.mod to major.minor, read only
the first ``go-version:`` in ci.yml, never looked at ``.envrc``, the Dockerfiles
or ``.vscode``, and only warned. Each drift test below is a case it let through
green. ``test_security_yml_node_20x_fails`` is the pre-fix ``security.yml``
(CI-03): the new gate must reject it.

Every test copies the real toolchain files into a temp dir and mutates the copy,
so the fixtures track the tree and nothing touches the checkout itself.
"""

from __future__ import annotations

import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]


def _read_pin() -> str:
    """The Makefile's GOTOOLCHAIN patch (e.g. ``1.27.2``), read once at import."""
    text = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
    m = re.search(r"^export GOTOOLCHAIN := go(\d+\.\d+\.\d+)$", text, re.MULTILINE)
    if m is None:
        raise RuntimeError("Makefile has no 'export GOTOOLCHAIN := goX.Y.Z' line")
    return m.group(1)


PIN = _read_pin()
_major, _minor, _patch = PIN.split(".")
# A patch that is one above the pin: wrong in the "drifted past the pin" tests.
WRONG_PATCH = f"{_major}.{_minor}.{int(_patch) + 1}"
PIN_RE = re.escape(PIN)
# A patch below the pin (the pin is never .0 while a security patch is pending).
LOWER_PATCH = f"{_major}.{_minor}.0"
SCRIPT = REPO_ROOT / "scripts" / "check_toolchain_versions.py"
COPIES = (
    "Makefile",
    ".envrc",
    ".vscode/settings.json",
    "go.mod",
    ".github/repository-config.yml",
    "scripts/ci_remote.py",
)


class CheckToolchainVersionsTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._tmp.name)
        for rel in COPIES:
            dst = self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / rel, dst)
        for src in REPO_ROOT.glob("Dockerfile*"):
            if src.is_file():
                shutil.copy2(src, self.root / src.name)
        wp = self.root / ".woodpecker"
        wp.mkdir(parents=True, exist_ok=True)
        for src in (REPO_ROOT / ".woodpecker").glob("*.y*ml"):
            shutil.copy2(src, wp / src.name)
        wf = self.root / ".github" / "workflows"
        wf.mkdir(parents=True, exist_ok=True)
        for src in (REPO_ROOT / ".github" / "workflows").glob("*.y*ml"):
            shutil.copy2(src, wf / src.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def run_check(self, *extra: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--root", str(self.root), *extra],
            capture_output=True,
            text=True,
            check=False,
        )

    def mutate(self, rel: str, old: str, new: str, *, occurrence: int = 1) -> None:
        """Replace the Nth occurrence of ``old`` in ``rel``; fail if it is absent."""
        path = self.root / rel
        text = path.read_text(encoding="utf-8")
        idx = -1
        for _ in range(occurrence):
            idx = text.find(old, idx + 1)
            self.assertNotEqual(
                idx, -1, f"{old!r} occurrence {occurrence} not in {rel}; fixture drifted"
            )
        path.write_text(text[:idx] + new + text[idx + len(old) :], encoding="utf-8")

    def assert_fails(self, needle: str, *extra: str) -> None:
        res = self.run_check(*extra)
        self.assertEqual(res.returncode, 1, res.stdout + res.stderr)
        self.assertIn("::error", res.stdout)
        self.assertIn(needle, res.stdout)

    def test_current_tree_passes(self) -> None:
        res = self.run_check("--action-node-output", "26")
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("Toolchain versions consistent", res.stdout)

    def test_envrc_patch_drift_fails(self) -> None:
        self.mutate(".envrc", f"GOTOOLCHAIN=go{PIN}", f"GOTOOLCHAIN=go{WRONG_PATCH}")
        self.assert_fails(f"go{WRONG_PATCH} != Makefile pin")

    def test_vscode_second_pin_drift_fails(self) -> None:
        self.mutate(".vscode/settings.json", f'"go{PIN}"', f'"go{LOWER_PATCH}"', occurrence=2)
        self.assert_fails(f"go{LOWER_PATCH} != Makefile pin")

    def test_dockerfile_patch_drift_fails(self) -> None:
        self.mutate(
            "Dockerfile.build-cgo", f"FROM golang:{PIN}-alpine", f"FROM golang:{WRONG_PATCH}-alpine"
        )
        self.assert_fails(f"golang:{WRONG_PATCH} != Makefile pin")

    def _dockerfile_digest(self) -> str:
        """The golang stage's current digest, so mutations track dependabot bumps."""
        text = (self.root / "Dockerfile").read_text(encoding="utf-8")
        found = re.findall(rf"FROM golang:{PIN_RE}-alpine@sha256:([0-9a-f]{{64}})", text)
        self.assertEqual(
            len(found), 1, "fixture drifted: expected one digest-pinned golang stage in Dockerfile"
        )
        return found[0]

    def test_dockerfile_digest_mismatch_fails(self) -> None:
        digest = self._dockerfile_digest()
        other = ("1" if digest[0] == "0" else "0") + digest[1:]
        self.mutate(
            "Dockerfile",
            f"golang:{PIN}-alpine@sha256:{digest}",
            f"golang:{PIN}-alpine@sha256:{other}",
        )
        self.assert_fails("digests differ")

    def test_dockerfile_unpinned_golang_stage_fails(self) -> None:
        # The old check stored a digest only when one was present and then
        # compared the set, so a stage that dropped its @sha256: passed.
        path = self.root / "Dockerfile"
        text = path.read_text(encoding="utf-8")
        unpinned, n = re.subn(rf"(FROM golang:{PIN_RE}-alpine)@sha256:[0-9a-f]+", r"\1", text)
        self.assertEqual(
            n, 1, "fixture drifted: expected one digest-pinned golang stage in Dockerfile"
        )
        path.write_text(unpinned, encoding="utf-8")
        line = unpinned[: unpinned.index(f"FROM golang:{PIN}-alpine")].count("\n") + 1
        self.assert_fails(
            f"::error file=Dockerfile,line={line}::golang:{PIN} stage has no @sha256: digest"
        )

    def test_two_stages_in_one_file_with_different_digests_fails(self) -> None:
        # The old check kept ONE digest per file (the last stage's), so an
        # earlier stage with a different digest was overwritten and never
        # compared. Prepend such a stage; the last stage still matches the
        # other Dockerfile, which is exactly the case the old check passed.
        other = "0" * 64
        self.mutate(
            "Dockerfile",
            f"FROM golang:{PIN}-alpine@sha256:",
            f"FROM golang:{PIN}-alpine@sha256:{other} AS early\n\nFROM golang:{PIN}-alpine@sha256:",
        )
        self.assert_fails("digests differ across stages")
        self.assert_fails(f"Dockerfile:{self._line_of_first('Dockerfile', other)}={other[:12]}")

    def test_malformed_golang_reference_fails(self) -> None:
        # A stage whose reference the strict parser rejects must be an error,
        # not silently skipped (which would also skip its version check).
        digest = self._dockerfile_digest()
        self.mutate(
            "Dockerfile",
            f"golang:{PIN}-alpine@sha256:{digest}",
            f"golang:{PIN}-alpine@sha256:Z{digest[1:]}",
        )
        self.assert_fails("is not 'golang:<version>[-<variant>]@sha256:<64 hex>'")

    def _line_of_first(self, rel: str, needle: str) -> int:
        text = (self.root / rel).read_text(encoding="utf-8")
        return text[: text.index(needle)].count("\n") + 1

    def test_gomod_requiring_more_than_pin_fails(self) -> None:
        self.mutate("go.mod", "\ngo 1.27.0\n", f"\ngo {WRONG_PATCH}\n")
        self.assert_fails("requires more than the pin")

    def test_gomod_toolchain_directive_fails(self) -> None:
        self.mutate("go.mod", "\ngo 1.27.0\n", f"\ngo 1.27.0\n\ntoolchain go{PIN}\n")
        self.assert_fails("toolchain")

    def test_later_ci_go_version_drift_fails(self) -> None:
        # The old check read only `head -1` of ci.yml; drift a later job.
        self.mutate(
            ".github/workflows/ci.yml", f"go-version: '{PIN}'", "go-version: '1.28'", occurrence=2
        )
        self.assert_fails("go-version '1.28'")

    def test_makefile_pin_bump_alone_fails(self) -> None:
        self.mutate(
            "Makefile", f"export GOTOOLCHAIN := go{PIN}", f"export GOTOOLCHAIN := go{WRONG_PATCH}"
        )
        self.assert_fails(f"!= Makefile pin go{WRONG_PATCH}")

    def test_security_yml_node_20x_fails(self) -> None:
        self.mutate(".github/workflows/security.yml", "node-version: '26'", "node-version: '20.x'")
        self.assert_fails("node-version '20.x'")

    def test_top_level_versions_node_drift_fails(self) -> None:
        # repository-config.yml has two versions: blocks; the old grep -A1
        # read only the first. Drift the top-level one alone.
        self.mutate(
            ".github/repository-config.yml",
            "versions:\n  node: ['26']",
            "versions:\n  node: ['20']",
        )
        self.assert_fails("versions.node blocks disagree")

    def test_action_output_mismatch_fails(self) -> None:
        self.assert_fails("action node-version '20'", "--action-node-output", "20")

    def test_workflow_minor_only_go_version_fails(self) -> None:
        # The floating minor let runners move to a newer patch than every other
        # copy (2026-10-08); the full patch is now required in workflows too.
        self.mutate(
            ".github/workflows/ci.yml", f"go-version: '{PIN}'", f"go-version: '{_major}.{_minor}'"
        )
        self.assert_fails(f"go-version '{_major}.{_minor}' != '{PIN}'")

    def test_workflow_expression_go_version_is_skipped(self) -> None:
        self.mutate(
            ".github/workflows/ci.yml", f"go-version: '{PIN}'", "go-version: ${{ matrix.go }}"
        )
        res = self.run_check()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

    def test_woodpecker_gotoolchain_mismatch_fails(self) -> None:
        self.mutate(
            ".woodpecker/test-rest.yaml", f"GOTOOLCHAIN: go{PIN}", f"GOTOOLCHAIN: go{WRONG_PATCH}"
        )
        self.assert_fails("::error file=.woodpecker/test-rest.yaml,line=")
        self.assert_fails(f"GOTOOLCHAIN go{WRONG_PATCH} != Makefile pin")

    def _woodpecker_image_digest(self) -> str:
        text = (self.root / ".woodpecker" / "checks-lint.yaml").read_text(encoding="utf-8")
        found = re.findall(rf"&go_image golang:{PIN_RE}-[a-z]+@sha256:([0-9a-f]{{64}})", text)
        self.assertEqual(
            len(found),
            1,
            "fixture drifted: expected one digest-pinned &go_image in checks-lint.yaml",
        )
        return found[0]

    def test_woodpecker_image_digest_mismatch_fails(self) -> None:
        digest = self._woodpecker_image_digest()
        # 63 hex digits: not a sha256, so the strict reference parser rejects it.
        self.mutate(".woodpecker/checks-lint.yaml", f"@sha256:{digest}", f"@sha256:{digest[:63]}")
        self.assert_fails("::error file=.woodpecker/checks-lint.yaml,line=")
        self.assert_fails("is not 'golang:<version>[-<variant>]@sha256:<64 hex>'")

    def test_woodpecker_image_without_digest_fails(self) -> None:
        digest = self._woodpecker_image_digest()
        self.mutate(".woodpecker/checks-lint.yaml", f"@sha256:{digest}", "")
        self.assert_fails("::error file=.woodpecker/checks-lint.yaml,line=")
        self.assert_fails("image has no @sha256: digest")

    def test_woodpecker_image_version_drift_fails(self) -> None:
        self.mutate(
            ".woodpecker/checks-lint.yaml",
            f"&go_image golang:{PIN}-",
            f"&go_image golang:{WRONG_PATCH}-",
        )
        self.assert_fails(f"golang:{WRONG_PATCH} != Makefile pin")

    def test_ci_remote_constant_mismatch_fails(self) -> None:
        self.mutate(
            "scripts/ci_remote.py", f'GO_TOOLCHAIN = "go{PIN}"', f'GO_TOOLCHAIN = "go{WRONG_PATCH}"'
        )
        self.assert_fails("::error file=scripts/ci_remote.py,line=")
        self.assert_fails(f"GO_TOOLCHAIN go{WRONG_PATCH} != Makefile pin")

    def test_ci_remote_constant_missing_fails(self) -> None:
        # A renamed constant must not make the check vacuous.
        self.mutate("scripts/ci_remote.py", "GO_TOOLCHAIN =", "GO_TOOLCHAIN_X =")
        self.assert_fails("::error file=scripts/ci_remote.py::expected exactly one")

    def test_output_lists_new_sections(self) -> None:
        res = self.run_check()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("--- Woodpecker", res.stdout)
        self.assertIn("--- ci_remote.py", res.stdout)

    def test_comments_and_expressions_are_ignored(self) -> None:
        path = self.root / ".github" / "workflows" / "zz-extra.yml"
        path.write_text(
            "jobs:\n  a:\n    steps:\n"
            "      # go-version: '1.99'\n"
            "      - uses: x/y@0000000000000000000000000000000000000000\n"
            "        with:\n          node-version: ${{ inputs.node }}\n",
            encoding="utf-8",
        )
        res = self.run_check()
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)


if __name__ == "__main__":
    unittest.main()
