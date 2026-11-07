# file: scripts/tests/test_deadcode_ratchet.py
# version: 1.0.0
# guid: 6fe9f117-94bd-4108-86b2-efeb0a64cc75
# last-edited: 2026-11-06
"""Tests for the deadcode ratchet's command line (09 §9.4 R1, MOD2-19).

`-filter` replaces deadcode's default, so a filter that named only core's trees
would drop every root finding and `--prune` would then empty the baseline. These
pin the filter as a union of the root module and core's internal/kernel trees,
and hello's main joining the program only when it exists.
"""

from __future__ import annotations

import importlib.util
import re
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location(
    "deadcode_ratchet", ROOT / "scripts/deadcode-ratchet.py"
)
dr = importlib.util.module_from_spec(_spec)
sys.modules["deadcode_ratchet"] = dr
_spec.loader.exec_module(dr)

ROOT_MOD = "example.com/app"


def _tree(hello_files: list[str] | None) -> Path:
    d = Path(tempfile.mkdtemp())
    (d / "go.mod").write_text(f"// header\nmodule {ROOT_MOD}\n\ngo 1.27.0\n")
    if hello_files is not None:
        h = d.joinpath(*dr.HELLO_DIR)
        h.mkdir(parents=True)
        for f in hello_files:
            (h / f).write_text("package main\n")
    return d


def _filter(args: list[str]) -> re.Pattern[str]:
    return re.compile(args[args.index("-filter") + 1])


class FilterTests(unittest.TestCase):
    def test_filter_keeps_every_root_package(self) -> None:
        f = _filter(dr.deadcode_args(_tree(None)))
        for pkg in (ROOT_MOD, f"{ROOT_MOD}/internal/server", f"{ROOT_MOD}/pkg/ops"):
            self.assertTrue(f.search(pkg), pkg)

    def test_filter_adds_core_internal_and_kernel_only(self) -> None:
        f = _filter(dr.deadcode_args(_tree(None)))
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

    def test_filter_is_anchored(self) -> None:
        f = _filter(dr.deadcode_args(_tree(None)))
        self.assertFalse(f.search(f"vendor/{ROOT_MOD}/x"))
        self.assertFalse(f.search(f"{ROOT_MOD}-other/x"))

    def test_real_root_module_is_read_from_go_mod(self) -> None:
        self.assertEqual(
            dr.root_module(ROOT),
            re.search(r"^module (\S+)", (ROOT / "go.mod").read_text(), re.M).group(1),
        )


class MainsTests(unittest.TestCase):
    def test_root_mains_only_without_hello(self) -> None:
        self.assertEqual(dr.deadcode_args(_tree(None))[2:], ["./..."])
        self.assertEqual(dr.deadcode_args(_tree([]))[2:], ["./..."])  # directory, no Go file

    def test_hello_main_joins_when_present(self) -> None:
        args = dr.deadcode_args(_tree(["main.go"]))
        self.assertEqual(args[2:], ["./...", "github.com/falkcorp/mediacore/examples/hello"])


if __name__ == "__main__":
    unittest.main()
