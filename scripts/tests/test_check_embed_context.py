# file: scripts/tests/test_check_embed_context.py
# version: 1.0.3
# guid: 65991395-4743-4674-ac13-5ce6e1a54129
# last-edited: 2026-11-10
"""Tests for the embed-context check, core's half of it, the image probe and the
docker-image workflow (R1-04; 03 §3.2 item 4; 09 §9.2.1 docker-image row).

The check runs the real ``go list -e -json`` over small fixture trees (a temp
repository root with a root module, a core module, a .dockerignore, a Dockerfile
and the two output lists), so what the tests prove is what CI runs:

  (a) the real tree passes;
  (b) an ``//go:embed docs/x.json`` fails, naming the file and the .dockerignore line;
  (c) a tree with no built ``web/dist`` passes (a listed output);
  (d) an ``all:`` pattern in a non-root package is normalised (MOD8-11);
  (e) a listed output with no ``COPY --from`` into a go build stage fails, and a
      runtime-stage copy is ignored (MOD7-5);
  plus moby's .dockerignore semantics, core's half, the tag-set list against
  core/archtest/tags.go, image_probe.sh against stub ``curl`` and ``docker``, and
  the shape of .github/workflows/docker-image.yml.

Stdlib only; run with ``python3 -m unittest scripts.tests.test_check_embed_context``
(CI's ``unittest discover -s scripts`` finds it) or with pytest.
"""

from __future__ import annotations

import importlib.util
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]


def _load(name: str, rel: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / rel)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


cec = _load("check_embed_context", "scripts/ci/check_embed_context.py")
ceo = sys.modules["check_embed_outputs"]  # loaded by check_embed_context from core/

DOCKERIGNORE = textwrap.dedent(
    """\
    .git
    docs
    node_modules
    **/node_modules
    web/dist
    testdata
    *.md
    core/web/packages/*/dist
    core/examples/*/web/node_modules
    """
)

DOCKERFILE = textwrap.dedent(
    """\
    FROM node:26-alpine AS frontend-builder
    WORKDIR /build/web
    COPY web/ ./
    RUN npm run build

    FROM golang:1.27-alpine AS go-builder
    WORKDIR /build
    COPY go.mod go.sum ./
    COPY core/go.mod core/go.sum ./core/
    RUN go mod download
    COPY . .
    COPY --from=frontend-builder /build/web/dist ./web/dist
    RUN CGO_ENABLED=1 go build -tags "embed_frontend" -o app .

    FROM alpine:3.24
    WORKDIR /app
    COPY --from=go-builder --chown=app:app /build/app /app/
    """
)

ROOT_MOD = "module example.test/app\n\ngo 1.21\n"
CORE_MOD = "module example.test/core\n\ngo 1.21\n"

WEB_EMBED = (
    '//go:build embed_frontend\n\npackage app\n\nimport "embed"\n\n'
    "//go:embed web/dist\nvar dist embed.FS\n"
)


def embedder(pkg: str, directive: str) -> str:
    return f'package {pkg}\n\nimport "embed"\n\n{directive}\nvar f embed.FS\n'


def group(m: re.Match[str] | None, what: str, n: int = 1) -> str:
    """Group ``n`` of a match, failing the test (not raising AttributeError) when there is none."""
    if m is None:
        raise AssertionError(f"no match for {what}")
    return m.group(n)


class Tree:
    """A fixture repository root."""

    def __init__(
        self,
        *,
        dockerfile: str = DOCKERFILE,
        dockerignore: str = DOCKERIGNORE,
        outputs: str = "web/dist\n",
        core_outputs: str = "",
    ) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._tmp.name)
        self.write("go.mod", ROOT_MOD)
        self.write("core/go.mod", CORE_MOD)
        self.write(".dockerignore", dockerignore)
        self.write("Dockerfile", dockerfile)
        self.write("Dockerfile.build-cgo", dockerfile)
        self.write("scripts/ci/embed_build_outputs.txt", outputs)
        self.write("core/scripts/ci/embed_build_outputs.txt", core_outputs)

    def write(self, rel: str, text: str) -> None:
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def check(self) -> list[str]:
        return cec.check(self.root)

    def close(self) -> None:
        self._tmp.cleanup()


class TreeCase(unittest.TestCase):
    def tree(self, **kw) -> Tree:
        t = Tree(**kw)
        self.addCleanup(t.close)
        return t


# -- (a) the real tree ---------------------------------------------------------


class RealTreeTests(unittest.TestCase):
    def test_real_tree_passes(self):
        self.assertEqual(cec.check(ROOT), [])

    def test_real_dockerignore_keeps_its_exclusions_and_adds_the_core_ones(self):
        lines = {
            ln.strip()
            for ln in (ROOT / ".dockerignore").read_text().splitlines()
            if ln.strip() and not ln.startswith("#")
        }
        # 09 §9.4 R1: nothing is dropped (docs and root *.md stay excluded), three are added.
        for want in ("docs", "*.md", "web/dist", "node_modules", "testdata", ".git", ".github"):
            self.assertIn(want, lines)
        for want in (
            "**/node_modules",
            "core/web/packages/*/dist",
            "core/examples/*/web/node_modules",
        ):
            self.assertIn(want, lines)

    def test_real_core_list_is_empty_and_core_half_passes(self):
        core_list = ROOT / "core/scripts/ci/embed_build_outputs.txt"
        self.assertTrue(core_list.is_file())
        self.assertEqual(core_list.stat().st_size, 0)
        self.assertEqual(ceo.core_problems(ROOT / "core", ceo.read_outputs(core_list)), [])

    def test_real_root_list_is_web_dist_and_both_dockerfiles_copy_core_mod(self):
        self.assertEqual(
            cec.core_lib.read_outputs(ROOT / "scripts/ci/embed_build_outputs.txt"), ["web/dist"]
        )
        for name in ("Dockerfile", "Dockerfile.build-cgo"):
            lines = (ROOT / name).read_text().splitlines()
            self.assertIn("COPY go.mod go.sum ./", lines, name)
            self.assertIn("COPY core/go.mod core/go.sum ./core/", lines, name)
            # core's go.mod is copied before the (retried) module download that needs it
            self.assertLess(
                lines.index("COPY core/go.mod core/go.sum ./core/"),
                next(i for i, ln in enumerate(lines) if ln.startswith("RUN ") and "go mod download" in ln),
                name,
            )

    def test_tag_sets_equal_the_go_list_in_core_archtest(self):
        src = (ROOT / "core/archtest/tags.go").read_text()
        body = src[src.index("func TagSets()") : src.index("func DefaultTagSet()")]
        go_sets = re.findall(r'\{Name: "([^"]+)"([^}]*)\}', body)
        py = {t.name: t for t in cec.TAG_SETS}
        self.assertEqual([n for n, _ in go_sets], [t.name for t in cec.TAG_SETS])
        for name, rest in go_sets:
            tags_m = re.search(r"Tags: \[\]string\{([^}]*)", rest + "}")
            tags = tuple(re.findall(r'"([^"]+)"', tags_m.group(1) if tags_m else ""))
            self.assertEqual(py[name].tags, tags, name)
            goos = re.search(r'GOOS: "([^"]+)"', rest)
            self.assertEqual(py[name].goos, goos.group(1) if goos else "", name)


# -- (b) (c) (d) (e) the fixtures ------------------------------------------------


class EmbedContextTests(TreeCase):
    def test_clean_fixture_passes(self):
        t = self.tree()
        t.write("app.go", "package app\n")
        self.assertEqual(t.check(), [])

    def test_embedding_a_docs_path_fails_naming_file_and_line(self):  # (b)
        t = self.tree()
        t.write("docs/x.json", "{}\n")
        t.write("docs.go", embedder("app", "//go:embed docs/x.json"))
        out = t.check()
        self.assertTrue(
            any("docs/x.json" in ln and ".dockerignore:2" in ln and "'docs'" in ln for ln in out),
            out,
        )

    def test_docs_fails_even_beside_a_listed_pending_output(self):
        # go list reports no EmbedFiles for a package with a "no matching files" error:
        # the other embed of that package must still be judged.
        t = self.tree()
        t.write("docs/x.json", "{}\n")
        t.write("web_embed.go", embedder("app", "//go:embed web/dist\n//go:embed docs/x.json"))
        out = t.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn("docs/x.json", out[0])
        self.assertIn(".dockerignore:2", out[0])

    def test_markdown_below_the_root_stays_in_the_context(self):
        # `*.md` has no **/ so it excludes only root-level Markdown (09 §9.4 R1).
        t = self.tree()
        t.write("internal/x/notes.md", "n\n")
        t.write("internal/x/x.go", embedder("x", "//go:embed notes.md"))
        self.assertEqual(t.check(), [])
        t.write("README.md", "r\n")
        t.write("readme.go", embedder("app", "//go:embed README.md"))
        out = t.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn("README.md", out[0])
        self.assertIn("'*.md'", out[0])

    def test_testdata_below_the_root_is_in_the_context(self):
        t = self.tree()
        t.write("internal/server/testdata/op_ids.golden", "x\n")
        t.write("internal/server/s.go", embedder("server", "//go:embed testdata/op_ids.golden"))
        self.assertEqual(t.check(), [])

    def test_unbuilt_listed_output_passes(self):  # (c)
        t = self.tree()
        t.write("web_embed.go", WEB_EMBED)
        self.assertFalse((t.root / "web/dist").exists())
        self.assertEqual(t.check(), [])

    def test_built_listed_output_passes(self):  # (c)
        t = self.tree()
        t.write("web_embed.go", WEB_EMBED)
        t.write("web/dist/index.html", "<html></html>\n")
        self.assertEqual(t.check(), [])

    def test_unbuilt_unlisted_output_fails(self):
        t = self.tree()
        t.write("web_embed.go", WEB_EMBED.replace("web/dist", "ui/dist"))
        out = t.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn("ui/dist", out[0])
        self.assertIn("matches no file", out[0])

    def test_missing_unlisted_input_that_dockerignore_also_matches_names_the_line(self):
        t = self.tree()
        t.write("a.go", embedder("app", "//go:embed docs/gone.json"))
        out = t.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn(".dockerignore:2", out[0])

    def test_all_pattern_in_a_non_root_package_is_normalised(self):  # (d)
        t = self.tree(core_outputs="examples/hello/web/dist\n")
        t.write("core/examples/hello/hello.go", embedder("hello", "//go:embed all:web/dist"))
        self.assertEqual(t.check(), [])
        # unlisted: the normalised path is named
        t2 = self.tree()
        t2.write("core/examples/hello/hello.go", embedder("hello", "//go:embed all:web/dist"))
        out = t2.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn("core/examples/hello/web/dist", out[0])

    def test_normalise_pattern(self):
        n = ceo.normalise_pattern
        self.assertEqual(n("examples/hello", "all:web/dist"), "examples/hello/web/dist")
        self.assertEqual(n("", "web/dist"), "web/dist")
        self.assertEqual(n("", "all:web/dist"), "web/dist")
        self.assertEqual(n("a/b", "./c/../d"), "a/b/d")

    def test_embed_built_in_core_under_dockerignore_is_flagged(self):
        t = self.tree()
        t.write("core/ui/dist/index.html", "x\n")
        t.write("core/ui/ui.go", embedder("ui", "//go:embed dist"))
        # core/ui/dist is not matched by any pattern: in the context
        self.assertEqual(t.check(), [])
        t.write("core/web/packages/shell/dist/a.js", "x\n")
        t.write("core/web/packages/shell/p.go", embedder("shell", "//go:embed dist"))
        out = t.check()
        self.assertTrue(
            any(
                "core/web/packages/shell/dist/a.js" in ln and "core/web/packages/*/dist" in ln
                for ln in out
            ),
            out,
        )

    def test_other_embed_error_fails(self):
        t = self.tree()
        t.write("bad.go", embedder("app", "//go:embed ../x"))
        out = t.check()
        self.assertEqual(len(out), 1, out)
        self.assertIn("pattern", out[0])

    def test_windows_set_applies_only_where_the_pattern_directory_exists(self):
        t = self.tree()
        t.write("cmd/mtls-bridge/main.go", "package main\n\nfunc main() {}\n")
        t.write("cmd/mtls-bridge/e.go", embedder("main", "//go:embed docs.txt"))
        t.write("cmd/mtls-bridge/docs.txt", "x\n")
        self.assertEqual(t.check(), [])

    def test_main_exit_codes(self):
        t = self.tree()
        t.write("a.go", "package app\n")
        self.assertEqual(cec.main(["--root", str(t.root)]), 0)
        t.write("docs/x.json", "{}\n")
        t.write("docs.go", embedder("app", "//go:embed docs/x.json"))
        self.assertEqual(cec.main(["--root", str(t.root)]), 1)


class DockerfileCrossCheckTests(TreeCase):
    def test_listed_output_without_a_copy_from_fails(self):  # (e)
        t = self.tree(outputs="web/dist\nextra/dist\n")
        out = t.check()
        # once per Dockerfile
        hits = [ln for ln in out if "extra/dist" in ln]
        self.assertEqual(len(hits), 2, out)
        self.assertTrue(all("no COPY --from=" in ln for ln in hits), out)

    def test_copy_from_without_a_listed_output_fails(self):  # (e)
        t = self.tree(outputs="")
        out = t.check()
        self.assertEqual(len(out), 2, out)  # one per Dockerfile
        self.assertTrue(all("web/dist" in ln and "is not listed" in ln for ln in out), out)
        self.assertTrue(any(ln.startswith("Dockerfile:12:") for ln in out), out)

    def test_runtime_stage_binary_copy_is_ignored(self):  # (e) MOD7-5
        copies = cec.dockerfile_outputs(DOCKERFILE)
        self.assertEqual([c.dest for c in copies], ["web/dist"])
        self.assertEqual(cec.dockerfile_problems("Dockerfile", DOCKERFILE, ["web/dist"]), [])

    def test_copy_into_a_stage_that_does_not_build_go_is_ignored(self):
        df = (
            DOCKERFILE
            + "\nFROM alpine:3.24 AS other\nWORKDIR /w\nCOPY --from=go-builder /build/app ./tools/app\n"
        )
        self.assertEqual([c.dest for c in cec.dockerfile_outputs(df)], ["web/dist"])

    def test_copy_outside_the_workdir_is_ignored(self):
        df = DOCKERFILE.replace(
            "COPY --from=frontend-builder /build/web/dist ./web/dist",
            "COPY --from=frontend-builder /build/web/dist ./web/dist\n"
            "COPY --from=frontend-builder /usr/bin/tool /usr/local/bin/tool",
        )
        self.assertEqual([c.dest for c in cec.dockerfile_outputs(df)], ["web/dist"])

    def test_workdir_in_effect_at_the_copy_decides_the_destination(self):
        df = textwrap.dedent(
            """\
            FROM x AS fe
            FROM y AS go-builder
            WORKDIR /build
            WORKDIR core
            COPY --from=fe /o ./web/dist
            WORKDIR /build
            RUN go build ./...
            """
        )
        self.assertEqual([c.dest for c in cec.dockerfile_outputs(df)], ["web/dist"])

    def test_go_build_after_the_copy_in_a_continued_run_still_marks_the_stage(self):
        df = textwrap.dedent(
            """\
            FROM x AS fe
            FROM y AS go-builder
            WORKDIR /build
            COPY --from=fe /o ./web/dist
            # a comment inside a continuation
            RUN set -ex \\
                # inner comment
                && CGO_ENABLED=1 go build \\
                -o app .
            """
        )
        self.assertEqual([c.dest for c in cec.dockerfile_outputs(df)], ["web/dist"])

    def test_two_dockerfiles_are_each_compared(self):
        t = self.tree()
        t.write("Dockerfile.build-cgo", DOCKERFILE.replace("./web/dist", "./other/dist"))
        out = t.check()
        self.assertTrue(
            any(ln.startswith("Dockerfile.build-cgo") and "other/dist" in ln for ln in out), out
        )
        self.assertTrue(
            any(ln.startswith("Dockerfile.build-cgo: listed output web/dist") for ln in out), out
        )
        self.assertFalse(any(ln.startswith("Dockerfile:") for ln in out), out)


class DockerignoreSemanticsTests(unittest.TestCase):
    def ig(self, text: str, path: str):
        hit = cec.ignored_by(path, cec.parse_dockerignore(text))
        return hit.text if hit else None

    def test_patterns_are_anchored_at_the_context_root(self):
        self.assertEqual(self.ig("testdata\n", "testdata/a"), "testdata")
        self.assertIsNone(self.ig("testdata\n", "internal/testdata/a"))
        self.assertIsNone(self.ig("node_modules\n", "web/node_modules/x"))
        self.assertEqual(self.ig("**/node_modules\n", "web/node_modules/x"), "**/node_modules")
        self.assertEqual(self.ig("**/node_modules\n", "node_modules/x"), "**/node_modules")

    def test_star_does_not_cross_a_slash_and_a_matching_parent_excludes_the_children(self):
        self.assertEqual(self.ig("*.md\n", "README.md"), "*.md")
        self.assertIsNone(self.ig("*.md\n", "docs/a.md"))
        self.assertEqual(self.ig("docs\n", "docs/a/b.md"), "docs")
        self.assertEqual(self.ig("web/dist\n", "web/dist/assets/x.js"), "web/dist")
        self.assertEqual(
            self.ig("core/web/packages/*/dist\n", "core/web/packages/shell/dist/a.js"),
            "core/web/packages/*/dist",
        )
        self.assertIsNone(self.ig("core/web/packages/*/dist\n", "core/web/packages/shell/src/a.js"))

    def test_question_mark_and_double_star_forms(self):
        self.assertEqual(self.ig("a?c\n", "abc"), "a?c")
        self.assertIsNone(self.ig("a?c\n", "a/c"))
        self.assertEqual(self.ig("**\n", "any/where"), "**")
        self.assertEqual(self.ig("a/**/z\n", "a/z"), "a/**/z")
        self.assertEqual(self.ig("a/**/z\n", "a/b/c/z"), "a/**/z")

    def test_exclusion_re_includes_and_order_matters(self):
        text = "docs\n!docs/keep.json\n"
        self.assertEqual(self.ig(text, "docs/other.json"), "docs")
        self.assertIsNone(self.ig(text, "docs/keep.json"))
        # an exclusion before the pattern it would undo has nothing to undo yet
        self.assertEqual(self.ig("!docs/keep.json\ndocs\n", "docs/keep.json"), "docs")

    def test_comments_blank_lines_leading_slash_and_dot_slash(self):
        pats = cec.parse_dockerignore("# c\n\n/docs\n./logs\n")
        self.assertEqual([(p.line, p.text) for p in pats], [(3, "docs"), (4, "logs")])

    def test_line_numbers_name_the_deciding_pattern(self):
        hit = cec.ignored_by("docs/x.json", cec.parse_dockerignore(DOCKERIGNORE))
        self.assertEqual((hit.line, hit.text), (2, "docs"))


# -- core's half ---------------------------------------------------------------


class CoreHalfTests(TreeCase):
    def core(self, t: Tree) -> list[str]:
        d = t.root / "core"
        return ceo.core_problems(d, ceo.read_outputs(d / ceo.OUTPUTS_FILE))

    def test_listed_unbuilt_output_passes_with_all_prefix(self):
        t = self.tree(core_outputs="examples/hello/web/dist\n")
        t.write("core/examples/hello/hello.go", embedder("hello", "//go:embed all:web/dist"))
        self.assertEqual(self.core(t), [])

    def test_unlisted_unbuilt_output_fails(self):
        t = self.tree()
        t.write("core/examples/hello/hello.go", embedder("hello", "//go:embed all:web/dist"))
        out = self.core(t)
        self.assertEqual(len(out), 1, out)
        self.assertIn("examples/hello/web/dist", out[0])

    def test_real_files_and_other_errors(self):
        t = self.tree()
        t.write("core/x/x.go", embedder("x", "//go:embed data.json"))
        t.write("core/x/data.json", "{}\n")
        self.assertEqual(self.core(t), [])
        t.write("core/y/y.go", embedder("y", "//go:embed ../z"))
        self.assertEqual(len(self.core(t)), 1)

    def test_cli_runs_from_core_alone(self):
        # an export of core/ alone: copy core/ to a temp dir and run its script there
        with tempfile.TemporaryDirectory() as tmp:
            dst = pathlib.Path(tmp) / "core"
            shutil.copytree(ROOT / "core", dst, ignore=shutil.ignore_patterns("node_modules"))
            proc = subprocess.run(
                [sys.executable, "scripts/ci/check_embed_outputs.py"],
                cwd=dst,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_missing_list_file_fails(self):
        t = self.tree()
        (t.root / "core/scripts/ci/embed_build_outputs.txt").unlink()
        self.assertEqual(ceo.main(["--module-dir", str(t.root / "core")]), 1)


# -- image_probe.sh ------------------------------------------------------------


class ImageProbeTests(unittest.TestCase):
    """Run the real script with stub curl and docker first on PATH."""

    SCRIPT = ROOT / "scripts/ci/image_probe.sh"

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.bin = pathlib.Path(self._tmp.name) / "bin"
        self.bin.mkdir()
        self.state = pathlib.Path(self._tmp.name) / "state"
        self.state.mkdir()

    def stub(self, name: str, body: str) -> None:
        p = self.bin / name
        p.write_text("#!/bin/sh\n" + body)
        p.chmod(p.stat().st_mode | stat.S_IEXEC)

    def curl(
        self, health: str = '{"status":"ok","ready":true}', header: str = "", html: str = ""
    ) -> None:
        (self.state / "health").write_text(health)
        (self.state / "headers").write_text(header)
        (self.state / "html").write_text(html)
        self.stub(
            "curl",
            textwrap.dedent(f"""\
            S={self.state}
            url=
            hdr=
            out=
            while [ $# -gt 0 ]; do
              case $1 in
                -D) hdr=$2; shift ;;
                -o) out=$2; shift ;;
                http*) url=$1 ;;
              esac
              shift
            done
            case $url in
              */health) cat $S/health ;;
              *) [ -n "$hdr" ] && cp $S/headers "$hdr"; if [ -n "$out" ]; then cp $S/html "$out"; else cat $S/html; fi ;;
            esac
            """),
        )

    def docker(
        self, running: str = "true", levels: str = '{"levels":[]}', levels_rc: int = 0
    ) -> None:
        self.stub(
            "docker",
            textwrap.dedent(f"""\
            case $1 in
              inspect) echo {running} ;;
              exec) echo '{levels}'; exit {levels_rc} ;;
            esac
            """),
        )

    def run_probe(self, *args: str, **env: str) -> subprocess.CompletedProcess:
        e = {"PATH": f"{self.bin}{os.pathsep}/usr/bin{os.pathsep}/bin", "READY_TIMEOUT": "1", **env}
        return subprocess.run(
            ["sh", str(self.SCRIPT), *args], env=e, capture_output=True, text=True, check=False
        )

    def test_is_executable_posix_sh_with_a_version_header(self):
        self.assertTrue(os.access(self.SCRIPT, os.X_OK))
        head = self.SCRIPT.read_text().splitlines()[:5]
        self.assertEqual(head[0], "#!/bin/sh")
        self.assertTrue(head[1].startswith("# file: scripts/ci/image_probe.sh"))
        shell = shutil.which("shellcheck")
        if shell:
            proc = subprocess.run(
                [shell, "-s", "sh", str(self.SCRIPT)], capture_output=True, text=True, check=False
            )
            self.assertEqual(proc.returncode, 0, proc.stdout)

    def test_ready_passes_and_optional_probes_stay_off(self):
        self.curl()
        self.docker(levels_rc=7)  # would fail if the level probe ran
        proc = self.run_probe("c1", "http://127.0.0.1:1/")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("ready", proc.stdout)

    def test_not_ready_times_out_with_one_line(self):
        self.curl(health='{"status":"ok","ready":false}')
        self.docker()
        proc = self.run_probe("c1", "http://127.0.0.1:1")
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(len(proc.stderr.strip().splitlines()), 1, proc.stderr)
        self.assertIn('no "ready":true', proc.stderr)

    def test_exited_container_fails_fast(self):
        self.curl(health="")
        self.docker(running="false")
        proc = self.run_probe("c1", "http://127.0.0.1:1", READY_TIMEOUT="60")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("exited", proc.stderr)

    def test_usage_error(self):
        proc = self.run_probe("only-one")
        self.assertEqual(proc.returncode, 1)
        self.assertIn("usage", proc.stderr)

    def test_storage_levels_probe(self):
        self.curl()
        self.docker(levels='{"levels":[1,2]}')
        self.assertEqual(self.run_probe("c", "http://h", PROBE_STORAGE_LEVELS="1").returncode, 0)
        self.docker(levels="not json")
        bad = self.run_probe("c", "http://h", PROBE_STORAGE_LEVELS="1")
        self.assertEqual(bad.returncode, 1)
        self.assertIn("did not print JSON", bad.stderr)
        self.docker(levels="{}", levels_rc=3)
        self.assertEqual(self.run_probe("c", "http://h", PROBE_STORAGE_LEVELS="1").returncode, 1)

    def test_build_id_probe(self):
        meta = '<html><head><meta name="mediacore-build" content="abc123"></head></html>'
        self.curl(header="HTTP/1.1 200 OK\r\nX-App-Build: abc123\r\n\r\n", html=meta)
        self.docker()
        ok = self.run_probe("c", "http://h", PROBE_BUILD_ID="1")
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.curl(header="HTTP/1.1 200 OK\r\nx-app-build: zzz\r\n\r\n", html=meta)
        bad = self.run_probe("c", "http://h", PROBE_BUILD_ID="1")
        self.assertEqual(bad.returncode, 1)
        self.assertIn("differs", bad.stderr)
        self.curl(header="HTTP/1.1 200 OK\r\n\r\n", html=meta)
        self.assertIn("no X-App-Build", self.run_probe("c", "http://h", PROBE_BUILD_ID="1").stderr)
        self.curl(header="X-App-Build: abc123\r\n", html="<html></html>")
        self.assertIn("no <meta", self.run_probe("c", "http://h", PROBE_BUILD_ID="1").stderr)
        # attribute order does not matter
        self.curl(header="X-App-Build: q\r\n", html='<meta content="q" name="mediacore-build">')
        self.assertEqual(self.run_probe("c", "http://h", PROBE_BUILD_ID="1").returncode, 0)


# -- the docker-image workflow -------------------------------------------------


def _workflow() -> str:
    return (ROOT / ".github/workflows/docker-image.yml").read_text()


class DockerImageWorkflowTests(unittest.TestCase):
    def test_trigger_includes_merge_group_and_pull_request(self):  # D15
        on = re.search(r"^on:\n((?:[ \t]+.*\n|\n)+?)(?=^\S)", _workflow(), re.M)
        triggers = group(on, "the on: block of docker-image.yml")
        self.assertIn("merge_group", triggers)
        self.assertIn("pull_request", triggers)

    def test_classifier_step_is_a_verbatim_copy_of_ci_ymls(self):
        spec = importlib.util.spec_from_file_location(
            "tcc", ROOT / "scripts/tests/test_ci_classifier.py"
        )
        assert spec and spec.loader
        tcc = importlib.util.module_from_spec(spec)
        sys.modules["tcc"] = tcc
        spec.loader.exec_module(tcc)
        ci_block, ci_script = tcc.extract_step(tcc.CI_YML, tcc.STEP_NAME)
        dk_block, dk_script = tcc.extract_step(tcc.WORKFLOWS / "docker-image.yml", tcc.STEP_NAME)
        self.assertEqual(ci_script, dk_script)
        self.assertEqual(textwrap.dedent(ci_block), textwrap.dedent(dk_block))

    def test_required_job_shape(self):
        w = _workflow()
        job = re.search(r"^  docker-image:\n((?:    .*\n|\n)+)", w, re.M)
        text = group(job, "job id docker-image")
        self.assertRegex(text, r"(?m)^    name: docker-image$")
        self.assertRegex(text, r"(?m)^    needs: changes$")
        cond = re.search(r"^    if:\s*(.+)$", text, re.M)
        condition = group(cond, "the job-level if:")
        # a web-only change rebuilds the image too: both classifier outputs gate it
        self.assertIn("needs.changes.outputs.go == 'true'", condition)
        self.assertIn("needs.changes.outputs.web == 'true'", condition)
        self.assertRegex(text, r"(?m)^    timeout-minutes: 30$")

    def test_every_action_is_pinned_to_a_full_sha(self):
        uses = re.findall(r"^\s*uses:\s*(\S+)", _workflow(), re.M)
        self.assertTrue(uses)
        for u in uses:
            self.assertRegex(u, r"^[\w./-]+@[0-9a-f]{40}$", u)

    def test_builds_both_dockerfiles_and_runs_the_probe_twice(self):
        w = _workflow()
        self.assertIn("file: Dockerfile\n", w)
        self.assertIn("file: Dockerfile.build-cgo", w)
        self.assertIn("type=local,dest=dist/", w)
        self.assertIn("scripts/ci/image_probe.sh", w)
        self.assertIn("/config/.encryption_key", w)
        self.assertIn("--db /config/x.pebble", w)
        self.assertIn("docker logs", w)
        # both optional probes are on in both runs: the build-ID one since
        # R1-19, the storage-levels one since R1-23 added the flag
        self.assertEqual(len(re.findall(r"PROBE_STORAGE_LEVELS:\s*'1'", w)), 2)
        self.assertIsNone(re.search(r"PROBE_STORAGE_LEVELS:\s*['\"]?0\b", w))
        self.assertEqual(len(re.findall(r"PROBE_BUILD_ID:\s*'1'", w)), 2)
        self.assertIsNone(re.search(r"PROBE_BUILD_ID:\s*['\"]?0\b", w))

    def test_no_machine_specific_values(self):
        w = _workflow()
        self.assertNotIn("/Users/", w)
        self.assertIsNone(re.search(r"/home/\w+", w))
        # loopback and the wildcard bind address are the only IPs it may spell
        ips = set(re.findall(r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", w))
        self.assertLessEqual(ips, {"127.0.0.1", "0.0.0.0"})


if __name__ == "__main__":
    unittest.main()
