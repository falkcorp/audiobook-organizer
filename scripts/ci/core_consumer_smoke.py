#!/usr/bin/env python3
# file: scripts/ci/core_consumer_smoke.py
# version: 1.0.0
# guid: fbd790b2-fc59-446f-bb3b-c225118cc91f
# last-edited: 2026-11-08
"""SC3: a second consumer builds against core with nothing but a dependency on it (12 §12.3 SC3).

Generates an out-of-tree module in a temp directory with an unrelated module
path, example.com/mediacore-smoke: a go.mod with one ``require
github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000`` and one
``replace`` to this checkout's core/, a copy of core/examples/hello's main.go,
and a test that runs ``hello openapi`` into a temp file. It then builds, vets
and tests the module, so it proves nothing in core assumes it is built from
inside this repository (a relative go:embed, a ../ path, a root-module tool).

It also runs the negative compile check: a file importing a
github.com/falkcorp/mediacore/internal/... package must fail with
"use of internal package ... not allowed".

Pre-split only: "only a version pin", the form an app uses after the split,
cannot be tested before a version is published (12 §12.8.2 step 8), and cutover
step 6 drops or rewrites this script. The core-side half of SC3 (go build, vet
and test ./examples/... inside core) runs in core's own merge gate.

Stdlib only.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
from typing import Callable

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "core" / "scripts" / "ci"))
import core_ci_lib as lib  # noqa: E402

TITLE = "Second consumer builds against core with nothing but a dependency on it"
SMOKE_MODULE = "example.com/mediacore-smoke"
ZERO_VERSION = "v0.0.0-00010101000000-000000000000"
HELLO_MAIN = pathlib.Path("examples/hello/cmd/hello/main.go")

# A package main test in the copied main.go's package: hello's own `run` writes
# core's OpenAPI document through app.Describe, which needs the whole kernel.
SMOKE_TEST = """package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSmoke_OpenAPI(t *testing.T) {
	out := filepath.Join(t.TempDir(), "core.json")
	var stdout bytes.Buffer
	if code, err := run(t.Context(), []string{"openapi", "--out", out}, &stdout); code != 0 || err != nil {
		t.Fatalf("hello openapi = %d, %v", code, err)
	}
	got, err := os.ReadFile(out)
	if err != nil || len(got) == 0 || got[0] != '{' {
		t.Fatalf("openapi document = %q, %v; want a JSON object", got, err)
	}
}
"""

Runner = Callable[[list[str], pathlib.Path], tuple[int, str]]


def render_go_mod(core: pathlib.Path) -> str:
    """The smoke module's go.mod: one require and one replace, on core's own go line."""
    go = re.search(r"^go\s+(\S+)\s*$", (core / "go.mod").read_text(encoding="utf-8"), re.M)
    if not go:
        raise ValueError(f"{core / 'go.mod'}: no go directive")
    path = lib.module_path(core)
    return (
        f"module {SMOKE_MODULE}\n\n"
        f"go {go.group(1)}\n\n"
        f"require {path} {ZERO_VERSION}\n\n"
        f"replace {path} => {core}\n"
    )


def internal_import(core: pathlib.Path) -> str:
    """An import path of one internal package of core (the negative check imports it)."""
    for directory in sorted((core / "internal").glob("*/")):
        if any(not f.name.endswith("_test.go") for f in directory.glob("*.go")):
            return f"{lib.module_path(core)}/internal/{directory.name}"
    raise ValueError(
        "core/internal holds no package to import (the negative check would be vacuous)"
    )


def negative_source(core: pathlib.Path) -> str:
    return f'package negative\n\nimport _ "{internal_import(core)}"\n'


def run_command(argv: list[str], cwd: pathlib.Path) -> tuple[int, str]:
    res = subprocess.run(
        argv, cwd=cwd, env=lib.go_env(), capture_output=True, text=True, check=False
    )
    return res.returncode, (res.stdout + res.stderr).strip()


def smoke(core: pathlib.Path, workdir: pathlib.Path, run: Runner = run_command) -> lib.Result:
    core = core.resolve()
    details: list[str] = []
    main_go = core / HELLO_MAIN
    if not main_go.is_file():
        return lib.Result("SC3", TITLE, lib.ERROR, f"core/{HELLO_MAIN.as_posix()} is missing")
    (workdir / "go.mod").write_text(render_go_mod(core), encoding="utf-8")
    shutil.copyfile(main_go, workdir / "main.go")
    (workdir / "smoke_test.go").write_text(SMOKE_TEST, encoding="utf-8")

    steps = [
        ("go mod tidy", ["go", "mod", "tidy"]),
        ("go build ./...", ["go", "build", "./..."]),
        ("go vet ./...", ["go", "vet", "./..."]),
        ("go test ./...", ["go", "test", "-count=1", "./..."]),
    ]
    for name, argv in steps:
        code, output = run(argv, workdir)
        details.append(f"{name}: " + ("ok" if code == 0 else f"exit {code}"))
        if code != 0:
            details += [f"    {line}" for line in output.splitlines()[-20:]]
            return lib.Result(
                "SC3", TITLE, lib.FAIL, f"{name} failed in the out-of-tree module", details
            )

    negative = workdir / "negative"
    negative.mkdir()
    (negative / "negative.go").write_text(negative_source(core), encoding="utf-8")
    code, output = run(["go", "build", "./negative"], workdir)
    if code == 0:
        details.append("negative check: the internal import compiled")
        return lib.Result(
            "SC3", TITLE, lib.FAIL, "an out-of-tree import of core/internal/... compiled", details
        )
    if not re.search(r"use of internal package .* not allowed", output):
        details.append("negative check: failed for another reason")
        details += [f"    {line}" for line in output.splitlines()[-20:]]
        return lib.Result(
            "SC3",
            TITLE,
            lib.FAIL,
            "the internal import failed, but not with 'use of internal package ... not allowed'",
            details,
        )
    details.append("negative check: internal import refused, as it must be")
    return lib.Result(
        "SC3",
        TITLE,
        lib.PASS,
        f"builds, vets and tests in {SMOKE_MODULE}; internal import refused",
        details,
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--core",
        type=pathlib.Path,
        default=REPO_ROOT / "core",
        help="the core module (default: this checkout's core/)",
    )
    parser.add_argument(
        "--keep", action="store_true", help="keep the temp module (its path is printed)"
    )
    parser.add_argument(
        "--out", type=pathlib.Path, help="write sc3.json here (split-readiness.yml)"
    )
    parser.add_argument(
        "--report", action="store_true", help="report only: exit 0 on FAIL or ERROR"
    )
    args = parser.parse_args(argv)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix="core-consumer-smoke-"))
    try:
        result = smoke(args.core, workdir)
    except (OSError, ValueError) as e:
        result = lib.Result("SC3", TITLE, lib.ERROR, str(e))
    finally:
        if args.keep:
            print(f"kept {workdir}")
        else:
            shutil.rmtree(workdir, ignore_errors=True)
    return lib.finish(result, args.out, args.report)


if __name__ == "__main__":
    sys.exit(main())
