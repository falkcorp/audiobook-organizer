# file: scripts/tests/test_cutover_blockers_offhost.py
# version: 0.1.0
# guid: a2e65d90-4c17-4f38-8b0d-71f3c9e2a564
# last-edited: 2026-11-22

"""Tests for scripts/cutover_blockers_offhost.py, the Mac side of
`make cutover-blockers-list` and `make cutover-blockers-apply`.

`Parse` and `CheckRequest` cover the pure parts. `Targets` runs the real recipe,
the real script, the real helpers and a fake ssh/scp in a sandbox (the harness of
test_make_rollback.py, whose FakeHost lays out the host); nothing here touches a
real host. Fixtures use Batman names only.

Run with:
    python3 -m unittest scripts.tests.test_cutover_blockers_offhost -v
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import stat
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import cutover_blockers_offhost as cbo  # noqa: E402
from test_make_rollback import (  # noqa: E402
    USER,
    FakeHost,
    granted,
    guard,
    out,
    parse_sudoers,
)

STAMP = "20261122T010203Z"
SHA = "a" * 64
ROBIN = b"robin".hex()
BATGIRL = b"batgirl".hex()
SECRET = b"alfred-pennyworth-credential-hash"
SECRET_B64 = base64.b64encode(SECRET).decode()
REQUEST = "aorg-cutover-blockers-request.json"
LIST_TEXT = (
    f"users step=books reason=bad-hash keyhex={ROBIN}\n"
    f"users step=books reason=bad-hash keyhex={BATGIRL}\n"
    "2 blocking row(s)."
)
ROWS = json.dumps([{"key": ROBIN, "value": SECRET_B64}, {"key": BATGIRL, "value": SECRET_B64}])

_STAT_SHIM = """#!{python}
import os, sys
a = sys.argv[1:]
st = os.stat(a[-1])
print(a[1].replace("%a", oct(st.st_mode & 0o7777)[2:]).replace("%s", str(st.st_size)).replace("%u", str(st.st_uid)))
"""
_SHA_SHIM = """#!{python}
import hashlib, sys
p = sys.argv[-1]
print(hashlib.sha256(open(p, "rb").read()).hexdigest() + "  " + p)
"""


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


class Parse(unittest.TestCase):
    def test_a_list_with_and_without_rows(self) -> None:
        txt, js = f"aorg-cutover-blockers-{STAMP}.txt", f"aorg-cutover-blockers-{STAMP}.json"
        out_ = f"EXPORTED {txt} 10 {SHA}\nEXPORTED {js} 20 {SHA}\n"
        got = cbo.parse(out_, ("list",), ("rows",), removed=False)
        self.assertEqual(sorted(got), ["list", "rows"])
        got = cbo.parse(f"EXPORTED {txt} 10 {SHA}\nABSENT rows\n", ("list",), ("rows",), False)
        self.assertEqual(sorted(got), ["list"])

    def test_an_apply_result_and_the_removal_line(self) -> None:
        res = f"aorg-cutover-blockers-result-{STAMP}.txt"
        good = f"EXPORTED {res} 10 {SHA}\nREQUEST-REMOVED {REQUEST}\n"
        self.assertEqual(sorted(cbo.parse(good, ("result",), (), removed=True)), ["result"])
        self.assertIsInstance(
            cbo.parse(f"EXPORTED {res} 10 {SHA}\n", ("result",), (), removed=True), str
        )
        # A failed apply may print the result without the removal line.
        self.assertEqual(
            sorted(cbo.parse(f"EXPORTED {res} 10 {SHA}\n", ("result",), (), removed=False)),
            ["result"],
        )

    def test_everything_else_is_a_refusal(self) -> None:
        txt, js = f"aorg-cutover-blockers-{STAMP}.txt", f"aorg-cutover-blockers-{STAMP}.json"
        res = f"aorg-cutover-blockers-result-{STAMP}.txt"
        for label, text, want, opt in (
            ("empty", "", ("list",), ("rows",)),
            ("rows neither present nor absent", f"EXPORTED {txt} 1 {SHA}", ("list",), ("rows",)),
            (
                "rows both present and absent",
                f"EXPORTED {txt} 1 {SHA}\nEXPORTED {js} 1 {SHA}\nABSENT rows",
                ("list",),
                ("rows",),
            ),
            (
                "absent for a required part",
                f"ABSENT list\nEXPORTED {js} 1 {SHA}",
                ("list",),
                ("rows",),
            ),
            ("traversal", f"EXPORTED ../../etc/passwd 5 {SHA}", ("list",), ("rows",)),
            ("shell metacharacters", f"EXPORTED {txt};id 5 {SHA}", ("list",), ("rows",)),
            ("a result name for a list", f"EXPORTED {res} 5 {SHA}", ("list",), ("rows",)),
            ("a list name for an apply", f"EXPORTED {txt} 5 {SHA}", ("result",), ()),
            ("a name with a suffix", f"EXPORTED {txt}.extra 5 {SHA}", ("list",), ("rows",)),
            # The same mistakes with the other parts complete, so a refusal here
            # can only come from the check named (not from a missing part).
            (
                "a name with a suffix, rows reported absent",
                f"EXPORTED {txt}.extra 5 {SHA}\nABSENT rows",
                ("list",),
                ("rows",),
            ),
            (
                "a required part reported both present and absent",
                f"EXPORTED {txt} 1 {SHA}\nABSENT list\nABSENT rows",
                ("list",),
                ("rows",),
            ),
            (
                "a result file nobody asked for in a list",
                f"EXPORTED {txt} 1 {SHA}\nEXPORTED {res} 1 {SHA}\nABSENT rows",
                ("list",),
                ("rows",),
            ),
            (
                "a list file nobody asked for in an apply",
                f"EXPORTED {res} 1 {SHA}\nEXPORTED {txt} 1 {SHA}",
                ("result",),
                (),
            ),
            ("uppercase hash", f"EXPORTED {txt} 1 {'A' * 64}", ("list",), ("rows",)),
            ("trailing text", f"EXPORTED {txt} 1 {SHA} extra", ("list",), ("rows",)),
            (
                "the same part twice",
                f"EXPORTED {txt} 1 {SHA}\nEXPORTED {txt} 1 {SHA}",
                ("list",),
                (),
            ),
            ("noise", f"hello\nEXPORTED {txt} 1 {SHA}", ("list",), ("rows",)),
            (
                "a removal line for a list",
                f"EXPORTED {txt} 1 {SHA}\nABSENT rows\nREQUEST-REMOVED {REQUEST}",
                ("list",),
                ("rows",),
            ),
            (
                "the removal of another name",
                f"EXPORTED {res} 1 {SHA}\nREQUEST-REMOVED ../{REQUEST}",
                ("result",),
                (),
            ),
            (
                "the removal line twice",
                f"EXPORTED {res} 1 {SHA}\nREQUEST-REMOVED {REQUEST}\nREQUEST-REMOVED {REQUEST}",
                ("result",),
                (),
            ),
        ):
            self.assertIsInstance(cbo.parse(text, want, opt, removed=False), str, label)


class Pull(unittest.TestCase):
    def test_a_local_file_that_exists_is_never_overwritten_and_nothing_is_fetched(self) -> None:
        import tempfile
        from unittest import mock

        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        dest = Path(tmp.name)
        name = f"aorg-cutover-blockers-{STAMP}.txt"
        (dest / name).write_text("mine")
        with mock.patch.object(cbo.subprocess, "run", side_effect=AssertionError("scp ran")):
            code, why = cbo.pull(None, "h", "/x", dest, {"list": (name, 4, SHA)})
        self.assertEqual(code, 1)
        self.assertIn("already exists", why)
        self.assertEqual((dest / name).read_text(), "mine")


class CheckRequest(unittest.TestCase):
    def setUp(self) -> None:
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()
        self.path = self.root / "request.json"

    def put(self, data: bytes, mode: int = 0o600) -> Path:
        self.path.unlink(missing_ok=True)
        self.path.write_bytes(data)
        self.path.chmod(mode)
        return self.path

    def test_a_good_request(self) -> None:
        data = json.dumps({"action": "fix", "key": ROBIN, "value": SECRET_B64}).encode()
        got = cbo.check_request(str(self.put(data)))
        self.assertEqual(got, (self.path, data))
        got = cbo.check_request(
            str(self.put(json.dumps({"action": "delete", "key": ROBIN}).encode()))
        )
        self.assertIsInstance(got, tuple)

    def test_refusals_name_no_value(self) -> None:
        good = json.dumps({"action": "delete", "key": ROBIN}).encode()
        self.assertIn("not set", cbo.check_request(""))
        self.assertIn("cannot read", cbo.check_request(str(self.root / "missing.json")))
        for mode in (0o640, 0o644, 0o604):
            self.assertIn("chmod 600", cbo.check_request(str(self.put(good, mode))), oct(mode))
        os.symlink(self.put(good), self.root / "link.json")
        self.assertIn("not a regular file", cbo.check_request(str(self.root / "link.json")))
        self.assertIn("1 to", cbo.check_request(str(self.put(b""))))
        for label, data in {
            "not json": b"batman",
            "unknown field": json.dumps({"action": "delete", "key": ROBIN, "x": 1}).encode(),
            "hex prefix": json.dumps({"action": "delete", "key": "hex:" + ROBIN}).encode(),
            "fix without a value": json.dumps({"action": "fix", "key": ROBIN}).encode(),
            "bad base64": json.dumps({"action": "fix", "key": ROBIN, "value": "!!"}).encode(),
            "duplicate field": b'{"action":"fix","action":"delete","key":"aa"}',
        }.items():
            msg = cbo.check_request(str(self.put(data)))
            self.assertIsInstance(msg, str, label)
            self.assertIn("is not a valid request", msg, label)
            self.assertNotIn(SECRET_B64, msg, label)


class Targets(unittest.TestCase):
    """`make cutover-blockers-list` and `make cutover-blockers-apply`."""

    def host(self, **kw) -> FakeHost:
        current = {
            "blockers_list": LIST_TEXT,
            "blockers_rows": ROWS,
            **kw.pop("current", {}),
        }
        host = FakeHost(self, current=current, **kw)
        host.dest = host.root / "offhost" / "run1"
        host.h["env"]["HOME"] = str(host.root / "home")
        bindir = host.root / "fake-bin"
        for name, body in (("stat", _STAT_SHIM), ("sha256sum", _SHA_SHIM)):
            p = bindir / name
            p.write_text(body.format(python=sys.executable))
            p.chmod(0o755)
        host.requests = host.root / "mine"
        host.requests.mkdir(mode=0o700)
        return host

    def request(self, host: FakeHost, doc=None, mode: int = 0o600) -> Path:
        doc = doc if doc is not None else {"action": "fix", "key": ROBIN, "value": SECRET_B64}
        p = host.requests / "request.json"
        p.write_bytes(doc if isinstance(doc, bytes) else json.dumps(doc).encode())
        p.chmod(mode)
        return p

    def sudos(self, host: FakeHost) -> list[str]:
        return [c for c in host.ssh() if c.startswith("sudo")]

    def remote_files(self, host: FakeHost) -> list[str]:
        return sorted(p.name for p in host.export.iterdir())

    def blocker_calls(self, host: FakeHost) -> list[dict]:
        return [
            e
            for e in host.calls()
            if e["kind"] == "app" and e["args"][:2] == ["storage", "cutover-blockers"]
        ]

    def assert_nothing_copied(self, host: FakeHost, p) -> None:
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertEqual(list(host.dest.glob("*")) if host.dest.exists() else [], [])
        self.assertEqual([e for e in host.scps() if e.get("direction") == "down"], [])

    # -- list -----------------------------------------------------------------

    def test_list_brings_both_files_here_and_deletes_the_remote_copies(self) -> None:
        host = self.host()
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assertEqual(p.returncode, 0, out(p))
        files = {f.suffix: f for f in host.dest.iterdir()}
        self.assertEqual(sorted(files), [".json", ".txt"])
        for f in files.values():
            self.assertRegex(f.name, r"^aorg-cutover-blockers-[0-9]{8}T[0-9]{6}Z\.(txt|json)$")
            self.assertEqual(stat.S_IMODE(f.stat().st_mode), 0o600, f.name)
        self.assertTrue(files[".txt"].read_text().startswith(LIST_TEXT))
        self.assertEqual(files[".json"].read_text(), ROWS)
        self.assertEqual(stat.S_IMODE(host.dest.stat().st_mode), 0o700)
        self.assertEqual(self.remote_files(host), [], "the remote copies are deleted")
        self.assertIn(LIST_TEXT, p.stdout, "the list is printed")
        self.assertNotIn(SECRET_B64, p.stdout, "a value is never printed")
        self.assertIn(sha(files[".json"].read_bytes()), p.stdout)
        ssh = host.ssh()
        self.assertEqual(
            [c for c in ssh if "cutover-blockers-" in c and c.startswith("sudo")],
            [f"sudo -n {guard.HELPER_DIR}/cutover-blockers-list"],
            "one sudo, the fixed line, no arguments",
        )
        rm = [c for c in ssh if c.startswith("rm ")]
        self.assertEqual(
            rm,
            ["rm -f -- " + " ".join(f"{host.export}/{files[s].name}" for s in (".txt", ".json"))],
        )
        self.assertEqual(len(self.sudos(host)), 1)
        self.assertEqual(
            [c["args"][2] for c in self.blocker_calls(host)], ["list"], "the command ran once"
        )
        self.assertEqual([p.name for p in host.dest.glob("*.part")], [])

    def test_list_with_nothing_blocking_brings_only_the_text(self) -> None:
        host = self.host(current={"blockers_rows": ""})
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assertEqual(p.returncode, 0, out(p))
        self.assertEqual([f.suffix for f in host.dest.iterdir()], [".txt"])
        self.assertIn("No blocking rows", p.stdout)
        self.assertEqual(self.remote_files(host), [])

    def test_list_refuses_while_the_service_is_not_stopped_and_copies_nothing(self) -> None:
        host = self.host()
        host.transient_state.write_text("active\n")
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assert_nothing_copied(host, p)
        self.assertIn("not inactive", out(p))
        self.assertEqual(self.blocker_calls(host), [])
        self.assertEqual(self.remote_files(host), [])

    def test_list_command_refusal_copies_nothing(self) -> None:
        host = self.host(current={"blockers_exit": 3})
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assert_nothing_copied(host, p)
        self.assertEqual(self.remote_files(host), [])

    def test_no_export_dir_refuses_before_any_sudo(self) -> None:
        host = self.host(config={"export_dir": None})
        for target, extra in (("cutover-blockers-list", {}), ("cutover-blockers-apply", {})):
            req = self.request(host)
            p = host.make(target, OFFHOST_DIR=str(host.dest), REQUEST=str(req), **extra)
            self.assert_nothing_copied(host, p)
            self.assertIn("no export_dir", out(p))
        self.assertEqual(self.sudos(host), [])
        self.assertEqual(host.scps(), [])

    def test_the_default_local_directory_is_made_0700(self) -> None:
        host = self.host()
        p = host.make("cutover-blockers-list")
        self.assertEqual(p.returncode, 0, out(p))
        (made,) = list((host.root / "home" / "aorg-offhost").iterdir())
        self.assertEqual(stat.S_IMODE(made.stat().st_mode), 0o700)

    def test_a_local_directory_others_can_enter_is_refused(self) -> None:
        host = self.host()
        host.dest.mkdir(parents=True, mode=0o755)
        host.dest.chmod(0o755)
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertEqual(self.sudos(host), [], "refused before the host is touched")

    def test_a_bad_copy_keeps_the_remote_files_and_says_so(self) -> None:
        for label, rule in (
            ("scp fails", {"exit": 1, "stderr": "scp: connection lost\n"}),
            ("scp writes nothing", {"exit": 0}),
        ):
            host = self.host(
                rules=[{"match": r"^scp .*aorg-cutover-blockers-.*\.txt", "responses": [rule]}]
            )
            p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
            self.assertNotEqual(p.returncode, 0, label)
            self.assertEqual(list(host.dest.iterdir()), [], f"{label}: no part or file is left")
            self.assertEqual([c for c in host.ssh() if c.startswith("rm ")], [], label)
            self.assertEqual(len(self.remote_files(host)), 2, label)
            self.assertIn("KEPT", out(p), label)

    def test_a_bad_second_copy_leaves_no_part_file_of_the_first(self) -> None:
        host = self.host(
            rules=[
                {
                    "match": r"^scp .*aorg-cutover-blockers-.*\.json ",
                    "responses": [{"exit": 1, "stderr": "scp: connection lost\n"}],
                }
            ]
        )
        p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertEqual(
            list(host.dest.iterdir()), [], "the list's .part file is removed with the rest"
        )
        self.assertEqual(len(self.remote_files(host)), 2, "the remote files are KEPT")

    def test_names_the_host_prints_are_held_to_a_grammar(self) -> None:
        for label, stdout in (
            ("traversal", f"EXPORTED ../../etc/passwd 5 {SHA}\n"),
            ("shell metacharacters", f"EXPORTED aorg-cutover-blockers-{STAMP}.txt;id 5 {SHA}\n"),
            ("a result name", f"EXPORTED aorg-cutover-blockers-result-{STAMP}.txt 5 {SHA}\n"),
            ("nothing", ""),
            ("noise", f"hello\nEXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}\nABSENT rows\n"),
        ):
            host = self.host(
                rules=[{"match": r"cutover-blockers-list$", "responses": [{"stdout": stdout}]}]
            )
            p = host.make("cutover-blockers-list", OFFHOST_DIR=str(host.dest))
            self.assert_nothing_copied(host, p)
            self.assertEqual([c for c in host.ssh() if c.startswith("rm ")], [], label)

    # -- apply ------------------------------------------------------------------

    def test_apply_fix_sends_the_request_runs_the_helper_and_brings_the_result(self) -> None:
        host = self.host()
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertEqual(p.returncode, 0, out(p))
        (call,) = self.blocker_calls(host)
        a = call["args"]
        self.assertEqual(a[:4], ["storage", "cutover-blockers", "fix", f"hex:{ROBIN}"])
        self.assertEqual(a[4], "--db")
        self.assertIn("--from", a)
        self.assertNotIn("--confirm", a)
        self.assertEqual(json.loads(call["from_text"]), [{"key": ROBIN, "value": SECRET_B64}])
        self.assertEqual(call["from_mode"], "0o600")
        (result,) = list(host.dest.iterdir())
        self.assertRegex(result.name, r"^aorg-cutover-blockers-result-[0-9]{8}T[0-9]{6}Z\.txt$")
        self.assertIn("Fixed hex:" + ROBIN, result.read_text())
        self.assertEqual(stat.S_IMODE(result.stat().st_mode), 0o600)
        self.assertIn("Fixed hex:" + ROBIN, p.stdout)
        self.assertNotIn(SECRET_B64, p.stdout)
        self.assertEqual(self.remote_files(host), [], "the request and the result are gone")
        uploads = [e for e in host.scps() if e.get("direction") != "down"]
        self.assertEqual(len(uploads), 1)
        self.assertEqual(uploads[0]["src"], str(req))
        self.assertEqual(uploads[0]["dest"], f"{host.export}/{REQUEST}")
        self.assertEqual(
            self.sudos(host),
            [f"sudo -n {guard.HELPER_DIR}/cutover-blockers-apply"],
        )
        self.assertTrue(req.exists(), "the local request is the caller's to delete")

    def test_the_request_is_made_0600_on_the_host_whatever_scp_left(self) -> None:
        host = self.host()
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertEqual(p.returncode, 0, out(p))
        chmods = [c for c in host.ssh() if c.startswith("chmod -- 600 ")]
        self.assertEqual(len(chmods), 1)
        self.assertIn(f"{host.export}/{REQUEST}", chmods[0])
        self.assertLess(
            host.index("chmod -- 600"),
            host.index("cutover-blockers-apply"),
            "set before the helper runs",
        )

    def test_apply_delete_sends_a_request_with_no_value(self) -> None:
        host = self.host()
        req = self.request(host, {"action": "delete", "key": BATGIRL})
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertEqual(p.returncode, 0, out(p))
        (call,) = self.blocker_calls(host)
        self.assertEqual(
            call["args"][2:7], ["delete", f"hex:{BATGIRL}", "--db", str(host.db), "--confirm"]
        )
        self.assertNotIn("from_text", call)

    def test_a_bad_local_request_refuses_before_the_host_is_touched(self) -> None:
        host = self.host()
        link = host.requests / "link.json"
        os.symlink(self.request(host), link)
        for label, req in {
            "unset": "",
            "missing": str(host.requests / "nope.json"),
            "group readable": str(self.request(host, mode=0o640)),
            "symlink": str(link),
            "not a request": str(self.request(host, b"batman", mode=0o600)),
            "hex prefix": str(self.request(host, {"action": "delete", "key": "hex:" + ROBIN})),
        }.items():
            p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=req)
            self.assert_nothing_copied(host, p)
            self.assertEqual(self.sudos(host), [], label)
            self.assertEqual(host.scps(), [], label)
            self.assertEqual(self.blocker_calls(host), [], label)

    def test_a_request_already_on_the_host_is_never_replaced(self) -> None:
        host = self.host()
        (host.export / REQUEST).write_text('{"action":"delete","key":"aa"}')
        (host.export / REQUEST).chmod(0o600)
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assert_nothing_copied(host, p)
        self.assertIn("already has a request", out(p))
        self.assertIn(f"rm -f -- {host.export}/{REQUEST}", out(p), "the exact remove command")
        self.assertEqual(host.scps(), [])
        self.assertEqual(self.sudos(host), [])
        self.assertEqual((host.export / REQUEST).read_text(), '{"action":"delete","key":"aa"}')

    def test_apply_refuses_while_the_service_is_not_stopped_and_leaves_the_request_to_remove(
        self,
    ) -> None:
        host = self.host()
        host.transient_state.write_text("active\n")
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assert_nothing_copied(host, p)
        self.assertIn("not inactive", out(p))
        self.assertEqual(self.blocker_calls(host), [])
        self.assertEqual(self.remote_files(host), [REQUEST], "the sent request is still there")

    def test_a_success_that_does_not_report_removing_the_request_is_a_failure(self) -> None:
        host = self.host(
            rules=[
                {
                    "match": r"cutover-blockers-apply$",
                    "responses": [
                        {"stdout": f"EXPORTED aorg-cutover-blockers-result-{STAMP}.txt 5 {SHA}\n"}
                    ],
                }
            ]
        )
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("did not report removing the request", out(p))

    def test_a_failed_apply_says_it_may_have_applied_and_keeps_the_request(self) -> None:
        host = self.host(current={"blockers_exit": 3})
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertNotEqual(p.returncode, 0, out(p))
        # The recipe's own sentence (the helper's stderr also says "MAY have been applied").
        self.assertIn("the apply helper exited 1", out(p))
        self.assertNotIn("the list helper exited", out(p))
        self.assertIn(REQUEST, out(p))
        self.assertEqual(self.remote_files(host), [REQUEST], "a failed apply keeps the request")
        self.assertEqual(list(host.dest.glob("*")) if host.dest.exists() else [], [])

    def test_a_bad_upload_is_refused_and_named(self) -> None:
        host = self.host(rules=[{"match": r"^scp [^ ]*request\.json ", "responses": [{"exit": 1}]}])
        req = self.request(host)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
        self.assertNotEqual(p.returncode, 0, out(p))
        self.assertIn("scp of the request failed", out(p))
        self.assertEqual(self.sudos(host), [])
        self.assertEqual(self.blocker_calls(host), [])

    def test_an_upload_that_is_not_what_was_sent_is_refused_on_each_of_mode_size_and_sha(
        self,
    ) -> None:
        data = json.dumps({"action": "fix", "key": ROBIN, "value": SECRET_B64}).encode()
        right = hashlib.sha256(data).hexdigest()
        for label, mode, size, sha in (
            ("mode", "644", len(data), right),
            ("size", "600", len(data) + 1, right),
            ("sha256", "600", len(data), "0" * 64),
        ):
            forged = f"{mode} {size} {os.getuid()}\n{sha}  {REQUEST}\n"
            host = self.host(
                rules=[{"match": r"^chmod -- 600 ", "responses": [{"stdout": forged}]}]
            )
            req = self.request(host)
            self.assertEqual(req.read_bytes(), data)
            p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(req))
            self.assertNotEqual(p.returncode, 0, f"{label}\n{out(p)}")
            self.assertIn("is not the file that was sent", out(p), label)
            self.assertEqual(self.sudos(host), [], f"{label}: the helper must not run")

    def test_the_helper_gets_longer_than_its_own_deadline(self) -> None:
        import inspect

        self.assertGreater(cbo.OP_TIMEOUT, cbo.load_helper().BLOCKERS_DEADLINE_SEC)
        self.assertIn("timeout=OP_TIMEOUT", inspect.getsource(cbo.main))

    def test_the_sudo_lines_it_runs_are_exactly_the_two_granted_ones(self) -> None:
        o = guard.Opts(
            host="h",
            db="/path/to/audiobooks.pebble",
            bin="/usr/local/bin/audiobook-organizer",
            user=USER,
        )
        text = guard.sudoers(o, "deploy")
        entries = parse_sudoers(text)
        seen = set()
        for target in ("cutover-blockers-list", "cutover-blockers-apply"):
            host = self.host()
            req = self.request(host)
            p = host.make(target, OFFHOST_DIR=str(host.dest), REQUEST=str(req))
            self.assertEqual(p.returncode, 0, out(p))
            for c in self.sudos(host):
                seen.add(c)
                self.assertTrue(granted(entries, c), f"not granted: {c}\n{text}")
        self.assertEqual(
            seen,
            {
                f"sudo -n {guard.HELPER_DIR}/cutover-blockers-list",
                f"sudo -n {guard.HELPER_DIR}/cutover-blockers-apply",
            },
        )

    def test_the_request_path_reaches_no_shell_text(self) -> None:
        host = self.host()
        weird = host.requests / "req ;touch pwned; `id` 'q'.json"
        weird.write_text(json.dumps({"action": "delete", "key": ROBIN}))
        weird.chmod(0o600)
        p = host.make("cutover-blockers-apply", OFFHOST_DIR=str(host.dest), REQUEST=str(weird))
        self.assertEqual(p.returncode, 0, out(p))
        self.assertFalse((Path(os.getcwd()) / "pwned").exists())
        for c in host.ssh():
            self.assertNotIn("touch pwned", c)
            self.assertNotIn(weird.name, c)


if __name__ == "__main__":
    unittest.main()
