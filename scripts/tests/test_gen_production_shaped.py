# file: scripts/tests/test_gen_production_shaped.py
# version: 1.1.0
# guid: 6f5d38ad-dc4d-42f5-b83c-f79c1e1d4f7c
# last-edited: 2026-11-10
"""Tests for scripts/r2corpus/gen_production_shaped.py and the Go row writer (F2-14).

The generator runs for real over the committed stage-1 corpus with a tiny counts
file; what it wrote is read back with the row writer's independent ``-mode scan``
(a separate code path from the one that wrote the rows).

Run: python3 -m pytest scripts/tests/test_gen_production_shaped.py -q
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "r2corpus" / "gen_production_shaped.py"
CORPUS = REPO_ROOT / "internal" / "books" / "testdata" / "r2corpus"
COUNTS = CORPUS / "p1_counts.json"


def load_generator():
    name = "gen_production_shaped"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod  # before exec_module: dataclasses look the module up
    spec.loader.exec_module(mod)
    return mod


gen = load_generator()

# OWNER-04's recorded P1 line (2026-11-10, issue #4382): counts and prefix names
# only. p1_counts.json must hold exactly these rows.
P1_LINE = (
    "act:change:=191853/11127474/67098504; act:debug:=28/1596/15388; act:digest:=154/8932/24267453; "
    "act:info:=7/392/2476; act:lvl:=192043/12866892/10370280; act:op:=20314/1645434/1096918; "
    "act:src:=192043/13263229/10370280; act:typ:=192043/13340477/10370280; apikey:=584/19272/269439; "
    "idx:=4839/301695/19836; invite:=1/71/288; oauthid:=1/34/231; opv2:act:=1/35/0; "
    "opv2:def:=277/10122/189736; opv2:done:=23304/1328328/0; opv2:err:=54878/3073168/12493222; "
    "opv2:log:=43745789/2930967930/11934321749; opv2:op:=23305/792370/131302081; opv2:open:=1/36/0; "
    "opv2:row:=141002/9090200/146669252; opv2:rowix:=141002/9372204/3999922; "
    "opv2:state:=791/29267/4687105; opv2:strike:=3622/328917/933467; opv3:ckpt:=503/18108/5949566; "
    "opv3:evt:=39942/2516346/4890275; opv3:ix:def:=23305/1863950/0; opv3:ix:done:=23304/1398240/0; "
    "opv3:ix:state:=23305/1163469/0; opv3:meta:=1/16/5449; opv3:prog:=23305/838980/9118613; "
    "opv3:run:=23305/815675/114846881; role:=4/46/1419; setting:=153/5325/29705; "
    "setting:repairs_last_apply_op:=16/930/2098; setting:repairs_last_completed_plan_op:=1/73/146; "
    "setting:repairs_last_plan_op:=23/1298/2977; u:=4/112/1380"
)


def run_gen(*argv: str) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = gen.main(list(argv))
    return rc, out.getvalue(), err.getvalue()


class P1CountsFile(unittest.TestCase):
    """p1_counts.json is the owner's P1 rows verbatim."""

    def test_rows_are_the_recorded_p1_line(self):
        doc = json.loads(COUNTS.read_text(encoding="utf-8"))
        want = []
        for item in P1_LINE.split("; "):
            prefix, nums = item.rsplit("=", 1)
            k, kb, vb = (int(n) for n in nums.split("/"))
            want.append({"prefix": prefix, "keys": k, "raw_key_bytes": kb, "raw_value_bytes": vb})
        self.assertEqual(len(want), 37)
        self.assertEqual(doc["families"], want)

    def test_header_and_provenance(self):
        doc = json.loads(COUNTS.read_text(encoding="utf-8"))
        self.assertEqual(doc["_header"]["file"], "internal/books/testdata/r2corpus/p1_counts.json")
        self.assertEqual(set(doc["_header"]), {"file", "version", "guid", "last-edited"})
        self.assertEqual(doc["source"], "OWNER-04 P1")
        self.assertEqual(doc["recorded"], "2026-11-10")
        # P1 branch A: every key is in a registered family.
        self.assertEqual(doc["unregistered_keys"], 0)
        self.assertEqual(set(doc), {"_header", "source", "recorded", "unregistered_keys", "families"})

    def test_load_counts_accepts_the_file_and_rejects_bad_shapes(self):
        self.assertEqual(len(gen.load_counts(COUNTS)), 37)
        with tempfile.TemporaryDirectory() as tmp:
            for name, fams in {
                "empty": [],
                "extra field": [{"prefix": "a:", "keys": 1, "raw_key_bytes": 2, "raw_value_bytes": 3, "value": "x"}],
                "negative": [{"prefix": "a:", "keys": -1, "raw_key_bytes": 2, "raw_value_bytes": 3}],
                "duplicate": [{"prefix": "a:", "keys": 1, "raw_key_bytes": 2, "raw_value_bytes": 3}] * 2,
            }.items():
                p = pathlib.Path(tmp) / f"{name}.json"
                p.write_text(json.dumps({"families": fams}), encoding="utf-8")
                with self.assertRaises(gen.GenerateError, msg=name):
                    gen.load_counts(p)


class Generator(unittest.TestCase):
    """The generator over the committed stage-1 store with a tiny counts file."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = pathlib.Path(tempfile.mkdtemp(prefix="shaped-test-"))
        cls.exe = gen.build_row_writer("go", cls.tmp)
        # Families of every shape stage 1 holds: JSON values (opv3:run:, u:),
        # empty values (opv3:ix:done:, opv2:done:), a family that is only in
        # the overlay store (opv2:row:), one with no template anywhere
        # (act:digest:) and one no registry knows (batcave:).
        cls.counts = {
            "opv3:run:": (80, 80 * 36, 80 * 520),
            "u:": (20, 20 * 30, 20 * 400),
            "opv3:ix:done:": (70, 70 * 60, 0),
            "opv2:done:": (90, 90 * 57, 0),
            "opv2:row:": (30, 30 * 64, 30 * 1000),
            "act:digest:": (6, 6 * 58, 6 * 5000),
            "batcave:": (5, 5 * 20, 5 * 100),
        }
        cls.counts_file = cls.tmp / "counts.json"
        cls.counts_file.write_text(
            json.dumps(
                {
                    "families": [
                        {"prefix": p, "keys": k, "raw_key_bytes": kb, "raw_value_bytes": vb}
                        for p, (k, kb, vb) in cls.counts.items()
                    ]
                }
            ),
            encoding="utf-8",
        )
        cls.out = cls.tmp / "shaped-store"
        cls.rc, cls.stdout, cls.stderr = run_gen(
            "--counts", str(cls.counts_file), "--out", str(cls.out), "--report", str(cls.tmp / "report.json")
        )
        cls.report = {f["prefix"]: f for f in json.loads((cls.tmp / "report.json").read_text())["families"]} if cls.rc == 0 else {}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def scan(self, store: pathlib.Path) -> dict[str, dict]:
        proc = subprocess.run(
            [str(self.exe), "-mode", "scan", "-store", str(store / "db" / "audiobooks.pebble"), "-counts", str(self.counts_file)],
            capture_output=True, text=True, check=True,
        )
        return {f["prefix"]: f for f in json.loads(proc.stdout)["families"]}

    def test_exits_zero_and_lists_every_family(self):
        self.assertEqual(self.rc, 0, self.stderr)
        for prefix in self.counts:
            self.assertIn(prefix, self.stdout)
        self.assertIn(f"{len(self.counts)} families", self.stdout)

    def test_requested_key_count_per_family(self):
        got = self.scan(self.out)
        # Grown to exactly the requested count; stage 1's own rows are never
        # deleted, so none of these families may shrink (u: starts at 7).
        for prefix in ("opv3:run:", "u:", "opv3:ix:done:", "opv2:done:", "opv2:row:"):
            self.assertEqual(got[prefix]["keys"], self.counts[prefix][0], prefix)

    def test_average_sizes_within_ten_percent(self):
        got = self.scan(self.out)
        for prefix in ("opv3:run:", "u:", "opv3:ix:done:", "opv2:done:", "opv2:row:"):
            keys, kb, vb = self.counts[prefix]
            self.assertAlmostEqual(got[prefix]["raw_key_bytes"] / keys, kb / keys, delta=0.1 * kb / keys, msg=prefix)
            if vb:
                self.assertAlmostEqual(got[prefix]["raw_value_bytes"] / keys, vb / keys, delta=0.1 * vb / keys, msg=prefix)
            else:
                self.assertEqual(got[prefix]["raw_value_bytes"], 0, prefix)

    def test_unknown_families_are_reported_not_invented(self):
        got = self.scan(self.out)
        for prefix in ("act:digest:", "batcave:"):
            self.assertEqual(got[prefix]["keys"], 0, f"{prefix} was invented")
            self.assertTrue(self.report[prefix]["skipped"], prefix)
            self.assertIn("SKIPPED", [ln for ln in self.stdout.splitlines() if ln.startswith(prefix)][0])
        self.assertIn("skipped families (2): act:digest:, batcave:", self.stdout)

    def test_every_generated_row_still_decodes(self):
        got = self.scan(self.out)
        self.assertTrue(all(f["invalid_json"] == 0 for f in got.values()), got)

    def test_template_source_is_named(self):
        self.assertEqual(self.report["opv3:run:"]["template_source"], "stage1")
        self.assertEqual(self.report["opv2:row:"]["template_source"], "overlay")

    def test_store_manifest_lists_every_file_but_itself(self):
        listed = json.loads((self.out / "store_manifest.json").read_text(encoding="utf-8"))
        on_disk = sorted(
            p.relative_to(self.out).as_posix()
            for p in self.out.rglob("*")
            if p.is_file() and p.name != "store_manifest.json"
        )
        self.assertEqual([e["path"] for e in listed], on_disk)
        for e in listed:
            f = self.out / e["path"]
            self.assertEqual(e["size"], f.stat().st_size, e["path"])
            self.assertEqual(e["sha256"], gen.sha256_file(f), e["path"])
            self.assertEqual(set(e), {"path", "size", "sha256"})
        self.assertEqual(gen.check_store(self.out, listed + [{"path": "store_manifest.json", "size": 0, "sha256": ""}]),
                         [f"store_manifest.json: size {(self.out / 'store_manifest.json').stat().st_size}, manifest says 0"])

    def test_stage_one_is_left_as_committed(self):
        manifest = json.loads((CORPUS / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(gen.check_store(CORPUS / "stage1", manifest["stores"][gen.STAGE1_KEY]), [])
        # The copy still holds every stage-1 file the generator did not rewrite.
        for rel in ("state/.encryption_key", "db/config.yaml"):
            self.assertEqual((self.out / rel).read_bytes(), (CORPUS / "stage1" / rel).read_bytes(), rel)


class Stage1MustMatchTheManifest(unittest.TestCase):
    """A stage-1 store that differs from manifest.json stops the generator."""

    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="shaped-stage1-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.stage1 = self.tmp / "stage1"
        shutil.copytree(CORPUS / "stage1", self.stage1)
        self.out = self.tmp / "out"

    def generate(self) -> tuple[int, str]:
        rc, _out, err = run_gen("--out", str(self.out), "--stage1", str(self.stage1), "--no-overlay", "--scale", "0.001")
        return rc, err

    def test_untouched_copy_passes(self):
        rc, err = self.generate()
        self.assertEqual(rc, 0, err)

    def test_missing_file(self):
        victim = next((self.stage1 / "db" / "audiobooks.pebble").glob("*.sst"))
        victim.unlink()
        rc, err = self.generate()
        self.assertNotEqual(rc, 0)
        self.assertIn(f"{victim.name}: in the manifest but not on disk", err)
        self.assertFalse(self.out.exists() and any(self.out.iterdir()), "wrote a store from a bad stage 1")

    def test_changed_file(self):
        victim = self.stage1 / "db" / "config.yaml"
        victim.write_bytes(victim.read_bytes() + b"#")
        rc, err = self.generate()
        self.assertNotEqual(rc, 0)
        self.assertIn("db/config.yaml: size", err)

    def test_same_size_changed_file(self):
        victim = self.stage1 / "state" / ".encryption_key"
        data = bytearray(victim.read_bytes())
        data[0] ^= 1
        victim.write_bytes(bytes(data))
        rc, err = self.generate()
        self.assertNotEqual(rc, 0)
        self.assertIn("state/.encryption_key: sha256 differs", err)

    def test_extra_file(self):
        (self.stage1 / "db" / "stray.txt").write_text("x", encoding="utf-8")
        rc, err = self.generate()
        self.assertNotEqual(rc, 0)
        self.assertIn("db/stray.txt: on disk but not in the manifest", err)

    def test_refuses_a_non_empty_out(self):
        self.out.mkdir()
        (self.out / "keep").write_text("x", encoding="utf-8")
        rc, err = self.generate()
        self.assertNotEqual(rc, 0)
        self.assertIn("is not empty", err)


WORKFLOWS = REPO_ROOT / ".github" / "workflows"


def run_true_patterns(text: str, start_marker: str) -> list[list[str]]:
    """The `a|b|c) run=true ;;` pattern lists after start_marker, one list per case line."""
    body = text[text.index(start_marker):]
    return [ln.strip()[: -len(") run=true ;;")].split("|")
            for ln in body.splitlines() if ln.strip().endswith(") run=true ;;") and "|" in ln][:2]


class ShapedStoreJobTrigger(unittest.TestCase):
    """The job runs on unit-lifecycle's triggers (09 §9.2.1), not on a second list of its own."""

    @classmethod
    def setUpClass(cls):
        cls.lifecycle = (WORKFLOWS / "unit-lifecycle.yml").read_text(encoding="utf-8")
        cls.gates = (WORKFLOWS / "extraction-gates.yml").read_text(encoding="utf-8")
        cls.nightly = (WORKFLOWS / "nightly.yml").read_text(encoding="utf-8")

    def test_path_triggers_are_unit_lifecycles(self):
        theirs, _ = run_true_patterns(self.lifecycle, "name: Classify changed paths")
        ours, own = run_true_patterns(self.gates, "name: Classify shaped-store paths")
        self.assertGreaterEqual(len(theirs), 8)
        self.assertEqual(ours, theirs, "extraction-gates.yml's shaped-store paths drifted from unit-lifecycle.yml's")
        # What the job adds is its own files only: the workflows that run it,
        # the generator, the row writer, the test, the shared script, the corpus
        # and F2-15's cutover suite with its shared-family golden.
        self.assertIn(".github/workflows/extraction-gates.yml", own)
        self.assertIn(".github/workflows/nightly.yml", own)
        for path in own:
            self.assertTrue(
                path.startswith((".github/workflows/", "scripts/r2corpus/", "scripts/ci/run_production_shaped_store.sh",
                                 "scripts/tests/test_gen_production_shaped.py", "internal/books/production_shaped_test.go",
                                 "internal/books/testdata/r2corpus/", "internal/books/cutover_suite_test.go",
                                 "internal/books/testdata/shared_families.golden")),
                f"{path} is not one of the job's own files",
            )

    def job(self, text: str) -> str:
        start = text.index("  production-shaped-store:\n")
        nxt = text.find("\n  # ", start)
        return text[start:] if nxt < 0 else text[start:nxt]

    def test_jobs_are_bounded_and_upload_the_measurement(self):
        for name, text in (("extraction-gates", self.gates), ("nightly", self.nightly)):
            job = self.job(text)
            self.assertIn("timeout-minutes: 30", job, name)
            self.assertIn("scripts/ci/run_production_shaped_store.sh", job, name)
            self.assertIn("R2_SHAPED_OUT", job, name)
            self.assertIn("actions/upload-artifact@", job, name)
            self.assertRegex(job, r"uses: [\w./-]+@[0-9a-f]{40} ", name)  # every action pinned to a SHA
            self.assertNotRegex(job, r"uses: [\w./-]+@v\d", name)

    def test_extraction_gates_job_is_gated_and_required(self):
        job = self.job(self.gates)
        self.assertIn("needs.changes.outputs.shaped == 'true'", job)
        needs = self.gates[self.gates.index("  extraction-gates:\n"):]
        self.assertIn("production-shaped-store", needs.split("if:")[0])

    def test_the_script_runs_generator_then_the_named_test(self):
        script = (REPO_ROOT / "scripts" / "ci" / "run_production_shaped_store.sh").read_text(encoding="utf-8")
        self.assertLess(script.index("gen_production_shaped.py"), script.index("TestProductionShapedStore_BootTime"))
        self.assertNotIn("--scale", script, "CI runs at scale 1")
        for env in ("R2_SHAPED_STORE", "R2_SHAPED_OUT"):
            self.assertIn(env, script)


if __name__ == "__main__":
    unittest.main()
