#!/usr/bin/env python3
# file: scripts/test_ci_remote.py
# version: 1.0.0
# guid: b886cb9e-2020-4cfc-806d-6002504520df
# last-edited: 2026-09-26

"""Unit tests for scripts/ci_remote.py: CI_NODES parsing, decode routing,
sharding, scheduling, coverage merging and the generated remote scripts.

No network, no ssh, no Go toolchain. Run with:
    python3 -m unittest discover -s scripts -p 'test_ci_remote.py' -v
"""

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("ci_remote", _HERE / "ci_remote.py")
cr = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
sys.modules["ci_remote"] = cr  # dataclasses resolve string annotations via sys.modules
_spec.loader.exec_module(cr)


class ParseNodesTest(unittest.TestCase):
    def test_flags(self):
        nodes = cr.parse_ci_nodes("user@node-a.example:max=2  user@192.0.2.10:prod,nodecode,max=3 node-c.example")
        self.assertEqual(len(nodes), 3)
        a, b, c = nodes
        self.assertEqual((a.user, a.host, a.max_jobs, a.prod, a.nodecode), ("user", "node-a.example", 2, False, False))
        self.assertEqual((b.host, b.max_jobs, b.prod, b.nodecode), ("192.0.2.10", 3, True, True))
        self.assertEqual(b.target, "user@192.0.2.10")
        self.assertEqual((c.user, c.host, c.max_jobs), ("", "node-c.example", 1))
        self.assertEqual(c.target, "node-c.example")

    def test_unknown_flag_is_an_error(self):
        # A typo in `nodecode` must not silently allow decode on a banned node.
        with self.assertRaises(ValueError):
            cr.parse_ci_nodes("user@node-a.example:nodecod")

    def test_bad_max(self):
        for bad in ("max=0", "max=x", "max=-1"):
            with self.assertRaises(ValueError, msg=bad):
                cr.parse_ci_nodes(f"user@node-a.example:{bad}")

    def test_empty(self):
        self.assertEqual(cr.parse_ci_nodes("  \n "), [])


class MakefileLookupTest(unittest.TestCase):
    def test_assignment_forms(self):
        for op in ("=", "?=", ":=", "::="):
            text = f"FOO = 1\nCI_NODES {op} user@node-a.example:max=2 user@node-b.example  # comment\n"
            self.assertEqual(cr.ci_nodes_from_makefile(text), "user@node-a.example:max=2 user@node-b.example", op)

    def test_continuation_and_append(self):
        text = "CI_NODES ?= user@node-a.example \\\n   user@node-b.example:nodecode\nCI_NODES += user@node-c.example\n"
        self.assertEqual(
            cr.ci_nodes_from_makefile(text),
            "user@node-a.example user@node-b.example:nodecode user@node-c.example",
        )

    def test_missing(self):
        self.assertIsNone(cr.ci_nodes_from_makefile("DEPLOY_HOST ?= x\n"))

    def test_env_wins_then_first_makefile(self):
        with tempfile.TemporaryDirectory() as td:
            wt = Path(td) / "wt.mk"
            primary = Path(td) / "primary.mk"
            primary.write_text("CI_NODES ?= user@node-p.example\n")
            spec, src = cr.find_ci_nodes({}, [wt, primary])
            self.assertEqual((spec, src), ("user@node-p.example", str(primary)))
            wt.write_text("CI_NODES = user@node-w.example\n")
            self.assertEqual(cr.find_ci_nodes({}, [wt, primary])[0], "user@node-w.example")
            self.assertEqual(
                cr.find_ci_nodes({"CI_NODES": "user@node-e.example"}, [wt, primary]),
                ("user@node-e.example", "environment"),
            )


class DecodeDetectionTest(unittest.TestCase):
    def test_positive(self):
        for src in (
            'if _, err := exec.LookPath("ffmpeg"); err != nil { t.Skip() }',
            'p, _ := exec.LookPath( "fpcalc" )',
            'cmd := exec.Command("ffprobe", "-v", "quiet", path)',
            'cmd := exec.CommandContext(ctx, "ffmpeg", "-i", in)',
            'bin := "/usr/bin/ffprobe"',
            'bin := "/opt/homebrew/bin/fpcalc"',
        ):
            self.assertTrue(cr.test_file_needs_decode(src), src)

    def test_negative(self):
        for src in (
            'cfg.FFmpegPath = "ffmpeg-custom"',
            '// mentions ffmpeg in a comment only',
            'exec.Command("git", "status")',
            'name := "ffmpeg"',
        ):
            self.assertFalse(cr.test_file_needs_decode(src), src)

    def test_scan_package_dir_only_reads_test_files(self):
        with tempfile.TemporaryDirectory() as td:
            d = Path(td)
            (d / "a.go").write_text('exec.LookPath("ffmpeg")\n')
            (d / "a_test.go").write_text("package a\n")
            size, decode = cr.scan_package_dir(d)
            self.assertFalse(decode)
            self.assertEqual(size, len("package a\n"))
            (d / "b_test.go").write_text('exec.Command("fpcalc", f)\n')
            self.assertTrue(cr.scan_package_dir(d)[1])


def _pkg(name, size=4000, decode=False):
    return cr.Package(name, "/x/" + name, size, decode)


class ShardingTest(unittest.TestCase):
    def test_lpt_balances(self):
        items = [("a", 10.0), ("b", 9.0), ("c", 5.0), ("d", 4.0), ("e", 1.0), ("f", 1.0)]
        bins = cr.partition_lpt(items, 2)
        loads = sorted(sum(w for _, w in b) for b in bins)
        self.assertEqual(loads, [15.0, 15.0])
        self.assertEqual(sorted(n for b in bins for n, _ in b), sorted(n for n, _ in items))

    def test_lpt_drops_empty_bins(self):
        self.assertEqual(len(cr.partition_lpt([("a", 1.0)], 5)), 1)
        self.assertEqual(cr.partition_lpt([], 3), [])

    def test_lpt_deterministic(self):
        items = [(f"p{i}", float(i % 3)) for i in range(20)]
        self.assertEqual(cr.partition_lpt(items, 4), cr.partition_lpt(list(reversed(items)), 4))

    def test_decode_packages_only_in_decode_shards(self):
        pkgs = [_pkg(f"p{i}") for i in range(10)] + [_pkg("dec1", decode=True), _pkg("dec2", decode=True)]
        legs = cr.build_shards(pkgs, cr.Timings(None), ["darwin"], ["darwin", "linux"], 2, 4)
        decode_legs = [l for l in legs if l.needs_decode]
        plain_legs = [l for l in legs if not l.needs_decode]
        self.assertEqual(sorted(p for l in decode_legs for p in l.packages), ["dec1", "dec2"])
        self.assertFalse(any(p.startswith("dec") for l in plain_legs for p in l.packages))
        # every package lands in exactly one shard
        all_pkgs = [p for l in legs for p in l.packages]
        self.assertEqual(sorted(all_pkgs), sorted(p.path for p in pkgs))
        self.assertEqual(len(plain_legs), 4)

    def test_measured_timing_beats_heuristic_and_is_per_goos(self):
        t = cr.Timings(None)
        t.record_pkg("darwin", "slow", 500.0)
        t.record_pkg("linux", "slow", 30.0)
        p = _pkg("slow", size=0)
        self.assertAlmostEqual(cr.package_weight(p, t, ["darwin"]), 501.0)
        self.assertAlmostEqual(cr.package_weight(p, t, ["linux"]), 31.0)
        self.assertAlmostEqual(cr.package_weight(p, t, ["darwin", "linux"]), 266.0)
        # unmeasured: size heuristic
        q = _pkg("new", size=40000)
        self.assertAlmostEqual(cr.package_weight(q, t, ["linux"]), 3.0 + cr.heuristic_seconds(40000))

    def test_timings_roundtrip_and_ewma(self):
        with tempfile.TemporaryDirectory() as td:
            path = Path(td) / "sub" / "timings.json"
            t = cr.Timings(path)
            t.record_pkg("linux", "p", 10.0)
            t.record_pkg("linux", "p", 20.0)
            t.record_leg("linux", "staticcheck", 100.0)
            t.save()
            t2 = cr.Timings(path)
            self.assertEqual(t2.pkg(["linux"], "p"), 15.0)
            self.assertEqual(t2.leg(["linux"], "staticcheck"), 100.0)
            self.assertIsNone(t2.pkg(["darwin"], "p"))

    def test_build_legs_covers_make_ci(self):
        legs = cr.build_legs([], cr.Timings(None), ["linux"])
        names = {l.name for l in legs}
        # every non-test prerequisite of `make ci` (coverage-check runs after the merge)
        for want in ("vet", "staticcheck", "errcheck-ratchet", "sdkguard", "bench-check",
                     "fmt-check", "web-test", "mocks-check"):
            self.assertIn(want, names)
        self.assertTrue(next(l for l in legs if l.name == "mocks-check").local_only)


class CapacityTest(unittest.TestCase):
    def probe(self, **kw):
        base = dict(ok=True, goos="linux", ncpu=48, load1=4.0, mem_avail_gb=60.0,
                    tools=frozenset({"go", "make", "git", "ffmpeg", "ffprobe", "fpcalc"}))
        base.update(kw)
        return cr.Probe(**base)

    def test_prod_caps_to_half_cores(self):
        n = cr.parse_ci_nodes("u@node-b.example:prod,nodecode,max=2")[0]
        cap = cr.node_capacity(n, self.probe())
        self.assertEqual((cap.slots, cap.par, cap.gomaxprocs), (2, 12, 12))
        self.assertEqual(cap.skip, "")

    def test_prod_skipped_when_loaded_or_low_memory(self):
        n = cr.parse_ci_nodes("u@node-b.example:prod")[0]
        self.assertIn("load", cr.node_capacity(n, self.probe(load1=30.0)).skip)
        self.assertIn("MemAvailable", cr.node_capacity(n, self.probe(mem_avail_gb=2.0)).skip)

    def test_unreachable_and_missing_tools(self):
        n = cr.parse_ci_nodes("u@node-a.example")[0]
        self.assertTrue(cr.node_capacity(n, cr.Probe(ok=False, error="x")).skip)
        self.assertIn("go", cr.node_capacity(n, self.probe(tools=frozenset({"make", "git"}))).skip)

    def test_busy_ollama_halves_slots(self):
        n = cr.parse_ci_nodes("u@node-a.example:max=4")[0]
        ps1 = json.dumps({"models": [{"name": "m", "expires_at": "2026-09-26T08:00:00Z"}]})
        ps2 = json.dumps({"models": [{"name": "m", "expires_at": "2026-09-26T08:00:05Z"}]})
        busy = self.probe(ncpu=10, ollama_local=True, ollama_ps=(ps1, ps2))
        self.assertEqual(cr.node_capacity(n, busy).slots, 2)
        idle = self.probe(ncpu=10, ollama_local=True, ollama_ps=(ps1, ps1))
        self.assertEqual(cr.node_capacity(n, idle).slots, 4)
        cpu = self.probe(ncpu=10, ollama_local=True, ollama_ps=(ps1, ps1), ollama_cpu=250.0)
        self.assertEqual(cr.node_capacity(n, cpu).slots, 2)

    def test_tunnelled_ollama_is_not_local_load(self):
        ps1 = json.dumps({"models": [{"name": "m", "expires_at": "a"}]})
        ps2 = json.dumps({"models": [{"name": "m", "expires_at": "b"}]})
        self.assertFalse(cr.ollama_busy(self.probe(ollama_local=False, ollama_ps=(ps1, ps2), ollama_cpu=900)))

    def test_can_decode(self):
        a = cr.parse_ci_nodes("u@node-a.example")[0]
        b = cr.parse_ci_nodes("u@node-b.example:nodecode")[0]
        self.assertTrue(cr.can_decode(a, self.probe()))
        self.assertFalse(cr.can_decode(b, self.probe()))
        self.assertFalse(cr.can_decode(a, self.probe(tools=frozenset({"go", "make", "git", "ffmpeg"}))))

    def test_parse_probe(self):
        out = "goos=darwin\nncpu=10\nload1=2.5\nmemkb=-1\ntools= go make git ffmpeg\nbare=1\nollama_local=0\n"
        p = cr.parse_probe(out)
        self.assertTrue(p.ok)
        self.assertEqual((p.goos, p.ncpu, p.load1, p.mem_avail_gb), ("darwin", 10, 2.5, -1.0))
        self.assertIn("bare", p.tools)
        self.assertIn("ffmpeg", p.tools)
        self.assertFalse(cr.parse_probe("").ok)


class SchedulingTest(unittest.TestCase):
    def setUp(self):
        self.a = cr.parse_ci_nodes("u@node-a.example:max=2")[0]
        self.b = cr.parse_ci_nodes("u@node-b.example:nodecode,prod,max=2")[0]
        self.wa = cr.Worker("node-a", self.a, True, 4)
        self.wb = cr.Worker("node-b", self.b, False, 12, 12)
        self.local = cr.Worker("local", None, True, 4)

    def queue(self):
        return [
            cr.Leg("test-decode-01", "", 300, needs_decode=True, packages=("d",)),
            cr.Leg("test-go-01", "", 500, packages=("p",)),
            cr.Leg("staticcheck", "make staticcheck", 400),
            cr.Leg("mocks-check", "make mocks-check", 60, local_only=True),
        ]

    def test_nodecode_never_gets_decode(self):
        q = [l for l in self.queue() if l.needs_decode]
        self.assertIsNone(cr.pick_leg(q, self.wb, [self.wa, self.wb], 0, -1))

    def test_decode_node_takes_decode_first(self):
        self.assertEqual(cr.pick_leg(self.queue(), self.wa, [self.wa, self.wb], 0, -1).name, "test-decode-01")

    def test_other_node_takes_heaviest(self):
        self.assertEqual(cr.pick_leg(self.queue(), self.wb, [self.wa, self.wb], 0, -1).name, "test-go-01")

    def test_remote_never_takes_local_only(self):
        q = [l for l in self.queue() if l.local_only]
        self.assertIsNone(cr.pick_leg(q, self.wa, [self.wa, self.wb], 0, -1))

    def test_local_takes_local_only_then_nothing(self):
        q = self.queue()
        self.assertEqual(cr.pick_leg(q, self.local, [self.wa, self.wb], 0, -1).name, "mocks-check")
        q = [l for l in q if not l.local_only]
        self.assertIsNone(cr.pick_leg(q, self.local, [self.wa, self.wb], 0, -1))

    def test_local_takes_decode_when_no_live_decode_node(self):
        q = [l for l in self.queue() if not l.local_only]
        self.wa.alive = False
        self.assertEqual(cr.pick_leg(q, self.local, [self.wa, self.wb], 0, -1).name, "test-decode-01")

    def test_local_after(self):
        q = [l for l in self.queue() if not l.local_only and not l.needs_decode]
        for l in q:
            l.queued_at = 100.0
        self.assertIsNone(cr.pick_leg(q, self.local, [self.wa, self.wb], 150.0, 60))
        self.assertIsNotNone(cr.pick_leg(q, self.local, [self.wa, self.wb], 161.0, 60))


class GoOutputTest(unittest.TestCase):
    def test_parse_lines(self):
        P = cr.parse_go_test_line
        self.assertEqual(P("ok  \texample.com/m/a\t12.345s\tcoverage: 40.0% of statements"), ("ok", "example.com/m/a", 12.345))
        self.assertEqual(P("FAIL\texample.com/m/b\t3.2s"), ("FAIL", "example.com/m/b", 3.2))
        self.assertEqual(P("FAIL\texample.com/m/c [build failed]"), ("FAIL", "example.com/m/c", None))
        self.assertEqual(P("ok  \texample.com/m/d\t(cached)\tcoverage: 1.0% of statements"), ("ok", "example.com/m/d", None))
        self.assertEqual(P("?   \texample.com/m/e\t[no test files]"), ("?", "example.com/m/e", None))
        self.assertEqual(P("\texample.com/m/f\t\tcoverage: 0.0% of statements"), ("?", "example.com/m/f", None))
        self.assertIsNone(P("--- FAIL: TestX (0.00s)"))
        self.assertIsNone(P("PASS"))


class CoverageMergeTest(unittest.TestCase):
    def test_merge_disjoint(self):
        a = "mode: atomic\nm/a/x.go:1.1,2.2 1 5\nm/a/x.go:3.1,4.2 2 0\n"
        b = "mode: atomic\nm/b/y.go:1.1,2.2 1 1\n"
        merged = cr.merge_coverage_profiles([a, b])
        lines = merged.splitlines()
        self.assertEqual(lines[0], "mode: atomic")
        self.assertEqual(len(lines), 4)
        self.assertIn("m/b/y.go:1.1,2.2 1 1", lines)

    def test_duplicate_blocks_summed(self):
        a = "mode: atomic\nm/a/x.go:1.1,2.2 1 5\n"
        self.assertIn("m/a/x.go:1.1,2.2 1 10", cr.merge_coverage_profiles([a, a]))

    def test_mode_mismatch_and_empty(self):
        with self.assertRaises(ValueError):
            cr.merge_coverage_profiles(["mode: set\n", "mode: atomic\n"])
        with self.assertRaises(ValueError):
            cr.merge_coverage_profiles([""])


class RemoteScriptTest(unittest.TestCase):
    """The generated bash must at least parse, and the lock logic must work."""

    def bash_n(self, script):
        p = subprocess.run(["bash", "-n"], input=script, text=True, capture_output=True)
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_scripts_parse(self):
        self.bash_n(cr.setup_script("$HOME/ci/jobs/abc-1", "a" * 40, nodecode=True))
        self.bash_n(cr.setup_script("$HOME/ci/jobs/abc-1", "a" * 40, nodecode=False))
        self.bash_n(cr.leg_script("$HOME/ci/jobs/j", "$HOME/ci/locks/slots", 2, "me", "go test ./x", True, True, 12, "c.out"))
        self.bash_n(cr.cleanup_script("$HOME/ci/jobs/j", "refs/ci-jobs/1", failed=True))
        self.bash_n(cr.cleanup_script("$HOME/ci/jobs/j", "refs/ci-jobs/1", failed=False))
        self.bash_n(cr.PROBE_SCRIPT)

    def test_lock_slots_and_busy_exit(self):
        with tempfile.TemporaryDirectory() as td:
            job = Path(td) / "job"
            job.mkdir()
            locks = Path(td) / "locks"
            script = cr.leg_script(str(job), str(locks), 1, "owner-a", "echo ran", False, False, None, None)
            env = dict(os.environ, HOME=td)
            p = subprocess.run(["bash", "-s"], input=script, text=True, capture_output=True, env=env)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertIn("ran", p.stdout)
            self.assertFalse((locks / "slot-0").exists(), "lock must be released on exit")
            # a fresh lock held by someone else -> busy
            (locks / "slot-0").mkdir()
            p = subprocess.run(["bash", "-s"], input=script, text=True, capture_output=True, env=env)
            self.assertEqual(p.returncode, cr.LOCK_BUSY_RC)
            # a stale one is expired and taken over
            old = 1_000_000_000
            os.utime(locks / "slot-0", (old, old))
            p = subprocess.run(["bash", "-s"], input=script, text=True, capture_output=True, env=env)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertIn("expired stale lock", p.stderr)

    def test_coverage_markers_and_exit_code(self):
        with tempfile.TemporaryDirectory() as td:
            job = Path(td) / "job"
            job.mkdir()
            script = cr.leg_script(str(job), str(Path(td) / "l"), 1, "o",
                                   "printf 'mode: atomic\\n' > c.out; exit 3", False, False, None, "c.out")
            p = subprocess.run(["bash", "-s"], input=script, text=True, capture_output=True, env=dict(os.environ, HOME=td))
            self.assertEqual(p.returncode, 3)
            self.assertIn(cr.COVERAGE_BEGIN + "\nmode: atomic\n" + cr.COVERAGE_END, p.stdout)
            self.assertFalse((job / "c.out").exists())

    def test_nodecode_path_filter(self):
        """Build a PATH dir holding a fake ffmpeg; setup must filter it out and
        keep the other executables in the same dir reachable."""
        with tempfile.TemporaryDirectory() as td:
            bindir = Path(td) / "bin"
            bindir.mkdir()
            for name in ("ffmpeg", "ffprobe", "fpcalc", "keepme"):
                f = bindir / name
                f.write_text("#!/bin/sh\necho " + name + "\n")
                f.chmod(0o755)
            job = Path(td) / "job"
            job.mkdir()
            script = cr.setup_script(str(job), "0" * 40, nodecode=True)
            # skip the git checkout part: run only the filtering tail
            tail = script.split('echo "checked out', 1)[1].split("\n", 1)[1]
            env = dict(os.environ, HOME=td, PATH=f"{bindir}:/usr/bin:/bin")
            if any(Path(d, t).exists() for d in ("/usr/bin", "/bin") for t in cr.DECODE_TOOLS):
                self.skipTest("host has a decoder in /usr/bin; filter covers it but the assertion below would too")
            p = subprocess.run(["bash", "-c", "set -eu\n" + tail], text=True, capture_output=True, env=env)
            self.assertEqual(p.returncode, 0, p.stderr)
            path = (job / ".ci-remote-nodecode-path").read_text()
            check = subprocess.run(
                ["bash", "-c", "command -v ffmpeg || command -v fpcalc || command -v ffprobe; keepme"],
                text=True, capture_output=True, env=dict(os.environ, PATH=path),
            )
            self.assertEqual(check.stdout.strip(), "keepme")


if __name__ == "__main__":
    unittest.main()
