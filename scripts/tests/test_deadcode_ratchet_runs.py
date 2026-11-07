# file: scripts/tests/test_deadcode_ratchet_runs.py
# version: 2.2.0
# guid: 6fe9f117-94bd-4108-86b2-efeb0a64cc75
# last-edited: 2026-11-07
"""Tests for the deadcode ratchet's runs and how it combines them (09 §9.4 R1, MOD2-19).

core/ is its own module, so the analysis is two deadcode runs, each in its own
module context: the root run (`./...` from the repo root) and, once hello
exists, the core run (hello's mains, cwd core/). A function is dead only if
every run that loaded its package reports it. The core run also roots a
synthetic main that references core's public API (tools/deadcode/surface), in
a scratch copy of core/. `-filter` replaces deadcode's
default, so each run's filter is pinned too: a filter naming only core's trees
would drop every root finding and `--prune` would then empty the baseline.
"""

from __future__ import annotations

import contextlib
import importlib.util
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location(
    "deadcode_ratchet_runs", ROOT / "scripts/deadcode-ratchet.py"
)
dr = importlib.util.module_from_spec(_spec)
sys.modules["deadcode_ratchet_runs"] = dr
_spec.loader.exec_module(dr)

ROOT_MOD = "example.com/app"


def _tree(hello_files: list[str] | None) -> Path:
    d = Path(tempfile.mkdtemp())
    (d / "go.mod").write_text(f"// header\nmodule {ROOT_MOD}\n\ngo 1.27.0\n")
    if hello_files is not None:
        h = d.joinpath(*dr.HELLO_DIR)
        h.mkdir(parents=True)
        for f in hello_files:
            p = h / f
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text("package main\n")
    return d


def _filter(spec: dr.RunSpec) -> re.Pattern[str]:
    return re.compile(spec.args[spec.args.index("-filter") + 1])


def _root_spec(root: Path) -> dr.RunSpec:
    return dr.plan(root)[0]


class FilterTests(unittest.TestCase):
    def test_root_filter_keeps_every_root_package(self) -> None:
        f = _filter(_root_spec(_tree(None)))
        for pkg in (ROOT_MOD, f"{ROOT_MOD}/internal/server", f"{ROOT_MOD}/pkg/ops"):
            self.assertTrue(f.search(pkg), pkg)

    def test_root_filter_adds_core_internal_and_kernel_only(self) -> None:
        f = _filter(_root_spec(_tree(None)))
        for pkg in (
            "github.com/falkcorp/mediacore/internal/jsonschemax",
            "github.com/falkcorp/mediacore/internal",
            "github.com/falkcorp/mediacore/kernel/boot",
        ):
            self.assertTrue(f.search(pkg), pkg)
        # Exported SDK packages are out of scope, as pkg/ is.
        for pkg in (
            "github.com/falkcorp/mediacore/auth",
            "github.com/falkcorp/mediacore/storage/keys",
            "github.com/falkcorp/mediacore/examples/hello",
            "github.com/falkcorp/mediacore",
        ):
            self.assertFalse(f.search(pkg), pkg)

    def test_root_filter_is_anchored(self) -> None:
        f = _filter(_root_spec(_tree(None)))
        self.assertFalse(f.search(f"vendor/{ROOT_MOD}/x"))
        self.assertFalse(f.search(f"{ROOT_MOD}-other/x"))

    def test_core_filter_is_core_internal_and_kernel_only(self) -> None:
        f = _filter(dr.plan(_tree(["main.go"]))[1])
        for pkg in (
            "github.com/falkcorp/mediacore/internal/jsonschemax",
            "github.com/falkcorp/mediacore/kernel",
        ):
            self.assertTrue(f.search(pkg), pkg)
        for pkg in (
            ROOT_MOD,
            f"{ROOT_MOD}/internal/server",
            "github.com/falkcorp/mediacore/httpapi",
            "github.com/falkcorp/mediacore/examples/hello",
            "github.com/falkcorp/mediacore-other/internal/x",
        ):
            self.assertFalse(f.search(pkg), pkg)

    def test_real_root_module_is_read_from_go_mod(self) -> None:
        self.assertEqual(
            dr.root_module(ROOT),
            re.search(r"^module (\S+)", (ROOT / "go.mod").read_text(), re.M).group(1),
        )


class PlanTests(unittest.TestCase):
    def test_single_root_run_without_hello(self) -> None:
        for tree in (_tree(None), _tree([])):  # no directory / directory, no Go file
            specs = dr.plan(tree)
            self.assertEqual([s.name for s in specs], ["root"])
            self.assertEqual(specs[0].cwd, tree)
            self.assertEqual(specs[0].patterns, ("./...",))

    def test_testdata_go_files_do_not_count_as_hello(self) -> None:
        self.assertEqual(len(dr.plan(_tree(["testdata/x/y.go"]))), 1)

    def test_core_run_joins_when_hello_present(self) -> None:
        tree = _tree(["main.go"])
        specs = dr.plan(tree)
        self.assertEqual([s.name for s in specs], ["root", "core"])
        # The root run never names hello: root's go.sum cannot load core's
        # third-party dependencies, which is why the runs are split.
        self.assertEqual(specs[0].patterns, ("./...",))
        self.assertEqual(specs[1].cwd, tree / "core")
        self.assertEqual(specs[1].patterns, ("./examples/hello/...",))
        self.assertEqual((specs[1].base, specs[1].surface), ("core", True))
        self.assertEqual((specs[0].base, specs[0].surface), ("", False))

    def test_hello_in_a_subdirectory_counts(self) -> None:
        self.assertEqual(len(dr.plan(_tree(["cmd/hello/main.go"]))), 2)


class ParseNormalisationTests(unittest.TestCase):
    def test_core_run_paths_become_repo_relative(self) -> None:
        out = "kernel/kernel.go:180:18: unreachable func: Kernel.Handler\n"
        self.assertEqual(dr.parse(out, "core"), {"core/kernel.Kernel.Handler": "core/kernel"})

    def test_same_function_from_both_runs_gets_one_identifier(self) -> None:
        a = dr.parse("core/kernel/kernel.go:1:1: unreachable func: F\n")
        b = dr.parse("kernel/kernel.go:1:1: unreachable func: F\n", "core")
        self.assertEqual(a, b)

    def test_exclusions_apply_to_normalised_paths(self) -> None:
        # `pkg/` is a root-relative prefix; core's kernel/pkg is not that.
        out = "kernel/pkg/a.go:1:1: unreachable func: F\n"
        self.assertEqual(set(dr.parse(out, "core")), {"core/kernel/pkg.F"})
        self.assertEqual(dr.parse("internal/testutil/t.go:1:1: unreachable func: F\n", "core"), {})
        self.assertEqual(dr.parse("x_test.go:1:1: unreachable func: F\n", "core"), {})


def _res(name: str, dead: list[str], loaded: list[str] | None) -> dr.RunResult:
    dirs = {i: i.rsplit(".", 1)[0] for i in dead}
    return dr.RunResult(name, dirs, None if loaded is None else frozenset(loaded))


class CombineTests(unittest.TestCase):
    ROOT_PKGS = ["internal/a", "core/kernel", "core/internal/x"]
    CORE_PKGS = ["core/kernel", "core/internal/x", "core/internal/y"]

    def test_single_run_is_returned_unchanged(self) -> None:
        r = _res("root", ["internal/a.F"], None)
        self.assertEqual(dr.combine([r]), {"internal/a.F"})

    def test_dead_in_both_runs_is_dead(self) -> None:
        root = _res("root", ["core/kernel.F"], self.ROOT_PKGS)
        core = _res("core", ["core/kernel.F"], self.CORE_PKGS)
        self.assertEqual(dr.combine([root, core]), {"core/kernel.F"})

    def test_reached_by_either_run_is_live(self) -> None:
        # Both runs load core/kernel; only the root run is silent about F.
        core_only = _res("core", ["core/kernel.F"], self.CORE_PKGS)
        root = _res("root", [], self.ROOT_PKGS)
        self.assertEqual(dr.combine([root, core_only]), set())
        root_only = _res("root", ["core/kernel.G"], self.ROOT_PKGS)
        core = _res("core", [], self.CORE_PKGS)
        self.assertEqual(dr.combine([root_only, core]), set())

    def test_root_module_function_judged_by_the_root_run_alone(self) -> None:
        # The core run cannot load root packages, so its silence is no vote.
        root = _res("root", ["internal/a.F"], self.ROOT_PKGS)
        core = _res("core", [], self.CORE_PKGS)
        self.assertEqual(dr.combine([root, core]), {"internal/a.F"})

    def test_core_package_loaded_by_one_run_is_judged_by_that_run(self) -> None:
        # internal/y: hello loads it, root does not; internal/x the reverse.
        root = _res("root", ["core/internal/z.G"], [*self.ROOT_PKGS, "core/internal/z"])
        core = _res("core", ["core/internal/y.F"], self.CORE_PKGS)
        self.assertEqual(dr.combine([root, core]), {"core/internal/y.F", "core/internal/z.G"})

    def test_package_loaded_by_neither_run_is_invisible(self) -> None:
        root = _res("root", [], self.ROOT_PKGS)
        core = _res("core", [], self.CORE_PKGS)
        self.assertEqual(dr.combine([root, core]), set())

    def test_untrustworthy_loaded_set_is_an_error(self) -> None:
        root = _res(
            "root", ["core/kernel.F"], ["internal/a"]
        )  # reports a package it "did not load"
        core = _res("core", [], self.CORE_PKGS)
        with self.assertRaises(dr.ToolError):
            dr.combine([root, core])

    def test_missing_loaded_set_in_a_multi_run_analysis_is_an_error(self) -> None:
        with self.assertRaises(dr.ToolError):
            dr.combine([_res("root", [], None), _res("core", [], self.CORE_PKGS)])


class RunDeadcodeTests(unittest.TestCase):
    """run_deadcode with the subprocess layer faked (no Go needed)."""

    def _drive(self, tree: Path) -> tuple[list[dr.RunResult], list[tuple[str, str, Path]]]:
        calls: list[tuple[str, str, Path]] = []

        def fake_run(cmd, spec, env, what):
            calls.append((what, spec.name, spec.cwd))
            self.assertEqual(
                (env["GOOS"], env["GOARCH"], env["CGO_ENABLED"]), ("linux", "amd64", "0")
            )
            cwd = os.path.realpath(spec.cwd)
            if what == "go list":
                # Dirs as `go list` prints them: absolute, under the run's cwd.
                core = (
                    os.path.join(cwd, "kernel") if spec.base else os.path.join(cwd, "core/kernel")
                )
                dirs = [os.path.join(cwd, "internal/a"), "/go/pkg/mod/x.org/m@v1", core]
                return "\n".join(dirs[1:] if spec.name == "core" else dirs)
            if what == "surface":
                return ""
            if spec.name == "root":
                return (
                    "internal/a/a.go:1:1: unreachable func: F\n"
                    "core/kernel/k.go:1:1: unreachable func: K\n"
                )
            return "kernel/k.go:1:1: unreachable func: K\nkernel/k.go:2:1: unreachable func: L\n"

        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(dr, "find_binary", return_value="/x/deadcode"))
            stack.enter_context(mock.patch.object(dr, "check_binary_version"))
            stack.enter_context(
                mock.patch.object(dr, "build_surface_helper", return_value="/x/surface")
            )
            stack.enter_context(mock.patch.object(dr, "_run", side_effect=fake_run))
            return dr.run_deadcode(tree), calls

    def test_hello_absent_is_one_run_no_go_list_no_surface_no_copy(self) -> None:
        tree = _tree(None)
        results, calls = self._drive(tree)
        self.assertEqual(calls, [("deadcode", "root", tree)])
        self.assertEqual(dr.combine(results), {"internal/a.F", "core/kernel.K"})

    def test_hello_present_runs_both_and_intersects(self) -> None:
        tree = _tree(["main.go"])
        results, calls = self._drive(tree)
        self.assertEqual(
            [(w, n) for w, n, _ in calls],
            [
                ("deadcode", "root"),
                ("go list", "root"),
                ("surface", "core"),
                ("deadcode", "core"),
                ("go list", "core"),
            ],
        )
        # The root run executes in the real tree; the core run in a scratch
        # copy of core/, never the real core/.
        self.assertEqual(calls[0][2], tree)
        self.assertNotEqual(calls[3][2], tree / "core")
        self.assertEqual(calls[2][2], calls[3][2])
        # F: root module, judged by the root run alone. K: both runs load
        # core/kernel and both report it. L: reported by the core run only; the
        # root run loads core/kernel too and does not report L, so it reaches L.
        self.assertEqual(dr.combine(results), {"internal/a.F", "core/kernel.K"})
        self.assertFalse((tree / "core" / dr.SURFACE_DIR).exists())  # real tree untouched


class WithSurfaceTests(unittest.TestCase):
    def test_copy_gets_the_surface_main_and_the_real_tree_is_untouched(self) -> None:
        tree = _tree(["main.go"])
        (tree / "core" / "go.mod").write_text("module example.com/core\n")
        (tree / "core" / ".git").mkdir()
        (tree / "core" / ".git" / "HEAD").write_text("x")
        spec = dr.plan(tree)[1]
        seen = {}

        def fake_run(cmd, s, env, what):
            seen["cmd"], seen["spec"], seen["what"] = cmd, s, what
            return ""

        with tempfile.TemporaryDirectory() as scratch, mock.patch.object(dr, "_run", fake_run):
            got = dr.with_surface(spec, "/x/surface", scratch, {})
            self.assertNotEqual(got.cwd, spec.cwd)
            self.assertTrue((got.cwd / "go.mod").is_file())
            self.assertTrue((got.cwd / "examples/hello/main.go").is_file())
            self.assertFalse((got.cwd / ".git").exists())
            self.assertEqual(got.patterns, ("./examples/hello/...", "./" + dr.SURFACE_DIR))
            self.assertEqual((got.base, got.filter), (spec.base, spec.filter))
            self.assertEqual(
                seen["cmd"],
                ["/x/surface", "-dir", str(got.cwd), "-out", str(got.cwd / dr.SURFACE_DIR)],
            )
        self.assertFalse((tree / "core" / dr.SURFACE_DIR).exists())

    def test_loaded_dirs_are_re_rooted_with_the_spec_base(self) -> None:
        tree = _tree(["main.go"])
        spec = dr.plan(tree)[1]
        dirs = (
            f"{os.path.realpath(spec.cwd)}/kernel\n/go/pkg/mod/m@v1\n{os.path.realpath(spec.cwd)}\n"
        )
        with mock.patch.object(dr, "_run", return_value=dirs):
            self.assertEqual(dr.loaded_dirs(spec, {}), frozenset({"core/kernel", "core"}))

    def test_no_test_flag_so_test_only_code_stays_dead(self) -> None:
        for spec in dr.plan(_tree(["main.go"])):
            self.assertNotIn("-test", spec.args)


@unittest.skipUnless(shutil.which("go"), "needs the Go toolchain")
class SurfaceHelperTests(unittest.TestCase):
    """The Go helper's own tests (generation, exclusions, method expressions,
    generics) live in tools/deadcode/surface; run them here so CI sees them."""

    def test_go_tests_pass(self) -> None:
        env = {k: v for k, v in os.environ.items() if k not in ("GOOS", "GOARCH")}
        res = subprocess.run(
            ["go", "-C", str(ROOT / "tools/deadcode"), "test", "./surface"],
            capture_output=True,
            text=True,
            env=env,
            timeout=300,
        )
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)


if __name__ == "__main__":
    unittest.main()
