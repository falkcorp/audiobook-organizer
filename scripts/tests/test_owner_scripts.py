# file: scripts/tests/test_owner_scripts.py
# version: 0.1.0
# guid: 4f8b2d17-6a93-4c50-b1e8-d07a5c39e6f2
# last-edited: 2026-11-22

"""Tests for the owner's scripts in deploy/owner/ (R2 runbook: P6 and Step 0).

Each script runs for real, as bash, in a sandbox: the host's rollback.conf, the
rollback helpers (the real ones, laid out by test_make_rollback.FakeHost), a
home directory, and a PATH whose first entries are small shims for the tools a
Linux host has and this machine may not (`stat -c`, `sudo`, `systemctl`). The
scripts hold fixed paths (/etc/..., /usr/local/libexec/...), so a test runs a
copy with those constants rewritten to the sandbox; every rewrite is asserted to
have matched exactly once, so a script that renames a constant fails here
instead of being tested against the wrong path. Nothing touches a real host.
Fixtures use Batman names only.

Run with:
    python3 -m unittest scripts.tests.test_owner_scripts -v
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_make_rollback import REPO_ROOT, FakeHost  # noqa: E402

OWNER = REPO_ROOT / "deploy" / "owner"
CONF_REAL = "/etc/audiobook-organizer/rollback.conf"
LIBDIR_REAL = "/usr/local/libexec/audiobook-organizer/rollback"
ROBIN = b"robin".hex()
BATGIRL = b"batgirl".hex()
SECRET = b"alfred-pennyworth-credential-hash"
SECRET_B64 = base64.b64encode(SECRET).decode()
REQUEST = "aorg-cutover-blockers-request.json"
LIST_TEXT = f"users step=books reason=bad-hash keyhex={ROBIN}\n1 blocking row(s)."
ROWS = json.dumps([{"key": ROBIN, "value": SECRET_B64}])
SHA = "b" * 64
STAMP = "20261122T010203Z"
STUBS = (
    "export-keyring",
    "export-install-secrets",
    "cutover-blockers-list",
    "cutover-blockers-apply",
)

_PY = sys.executable
_STAT = f"""#!{_PY}
import os, sys
a = [x for x in sys.argv[1:] if x != "--"]
st = os.stat(a[-1])
uid = str(st.st_uid)
if os.environ.get("SHIM_FAKE_UID_FOR") and os.environ["SHIM_FAKE_UID_FOR"] in a[-1]:
    uid = "4242"
print(a[1].replace("%a", oct(st.st_mode & 0o7777)[2:]).replace("%s", str(st.st_size)).replace("%u", uid))
"""

# install(1) that, when SHIM_RACE_FILE is set, makes that file right after installing:
# a stand-in for another process creating the fixed request name mid-run.
_INSTALL = f"""#!{_PY}
import os, subprocess, sys
r = subprocess.run(["/usr/bin/install", *sys.argv[1:]]).returncode
corrupt = os.environ.get("SHIM_INSTALL_CORRUPT")
if corrupt and r == 0 and corrupt in sys.argv[-1]:
    # Same size, different bytes: only a hash check can notice.
    data = bytearray(open(sys.argv[-1], "rb").read())
    data[0] ^= 1
    with open(sys.argv[-1], "wb") as f:
        f.write(data)
race = os.environ.get("SHIM_RACE_FILE")
if race and r == 0:
    with open(race, "w") as f:
        f.write("RACE")
    os.chmod(race, 0o600)
sys.exit(r)
"""
_SUDO = f"""#!{_PY}
import os, sys
a = sys.argv[1:]
with open(os.environ["SHIM_LOG"], "a") as f:
    f.write("sudo " + " ".join(a) + "\\n")
if a[:1] == ["-n"]:
    a = a[1:]
if a == ["-l"]:
    p = os.path.join(os.environ["SHIM_DIR"], "sudo-l")
    sys.stdout.write(open(p).read() if os.path.exists(p) else "")
    sys.exit(0)
os.environ["SUDO_UID"], os.environ["SUDO_GID"] = str(os.getuid()), str(os.getgid())
os.execv(a[0], a)
"""
_SYSTEMCTL = f"""#!{_PY}
import os, sys
a = sys.argv[1:]
d = os.environ["SHIM_DIR"]
if "ActiveState" in a:
    p = os.path.join(d, "state")
    if os.path.exists(p + ".fail"):
        sys.exit(1)
    print(open(p).read().strip() if os.path.exists(p) else "inactive")
elif "DropInPaths" in a:
    print("DropInPaths=/etc/systemd/system/audiobook-organizer.service.d/host.conf")
elif a[:1] in (["stop"], ["start"]):
    # What `sudo -n systemctl stop|start UNIT` does: log it, fail on request, and
    # leave the unit where change.result says (else in the state the verb means).
    with open(os.environ["SHIM_LOG"], "a") as f:
        f.write("systemctl " + " ".join(a) + "\\n")
    if os.path.exists(os.path.join(d, "change.fail")):
        sys.exit(1)
    res = os.path.join(d, "change.result")
    with open(os.path.join(d, "state"), "w") as f:
        f.write((open(res).read().strip() if os.path.exists(res) else ("inactive" if a[0] == "stop" else "active")) + "\\n")
sys.exit(0)
"""


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def script_copy(dest: Path, name: str, **sub: str) -> Path:
    """deploy/owner/<name> with fixed values rewritten for the sandbox; every
    one must match exactly one line."""
    text = (OWNER / name).read_text()
    for key, value in sub.items():
        pattern = re.compile(rf"^{key}=.*$", re.M)
        found = pattern.findall(text)
        if len(found) != 1:
            raise AssertionError(f"{name}: {key}= appears {len(found)} times, not once")
        text = pattern.sub(lambda _m, k=key, v=value: f'{k}="{v}"', text)
    out = dest / name
    out.write_text(text)
    out.chmod(0o755)
    return out


class Sandbox:
    """A home, a shim PATH and a copy of one script."""

    def __init__(self, tc: unittest.TestCase) -> None:
        self.tc = tc
        self._tmp = tempfile.TemporaryDirectory()
        tc.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()
        self.home = self.root / "home"
        (self.home / "aorg-owner").mkdir(parents=True)
        self.shims = self.root / "shims"
        self.shims.mkdir()
        self.log = self.root / "shim.log"
        for name, body in (
            ("stat", _STAT),
            ("sudo", _SUDO),
            ("systemctl", _SYSTEMCTL),
            ("install", _INSTALL),
        ):
            p = self.shims / name
            p.write_text(body)
            p.chmod(0o755)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()

    def set_state(self, state: str | None) -> None:
        p = self.shims / "state"
        Path(f"{p}.fail").unlink(missing_ok=True)
        if state is None:
            p.write_text("x")
            Path(f"{p}.fail").write_text("")
        else:
            p.write_text(state + "\n")

    def run(
        self, script: Path, *args: str, extra: dict | None = None, timeout: int = 120
    ) -> subprocess.CompletedProcess:
        env = {
            **(extra or {}),
            "PATH": f"{self.shims}{os.pathsep}{os.environ['PATH']}",
            "HOME": str(self.home),
            "SHIM_DIR": str(self.shims),
            "SHIM_LOG": str(self.log),
            "LC_ALL": "C",
            "GIT_CONFIG_GLOBAL": str(self.root / "gitconfig"),
            "GIT_CONFIG_NOSYSTEM": "1",
        }
        return subprocess.run(
            ["bash", str(script), *args], env=env, capture_output=True, text=True, timeout=timeout
        )

    def sudo_calls(self) -> list[str]:
        if not self.log.exists():
            return []
        return [ln for ln in self.log.read_text().splitlines() if ln.startswith("sudo")]


def text(p: subprocess.CompletedProcess) -> str:
    return f"exit {p.returncode}\n--- stdout\n{p.stdout}\n--- stderr\n{p.stderr}"


class BlockersBase(unittest.TestCase):
    """The real helpers on a FakeHost, driven through the owner scripts."""

    SCRIPT = ""

    def setUp(self) -> None:
        self.box = Sandbox(self)

    def host(self, config=None, **blockers) -> FakeHost:
        current = {"blockers_list": LIST_TEXT, "blockers_rows": ROWS, **blockers}
        host = FakeHost(self, current=current, config=config)
        self.fh = host
        self.script = script_copy(
            self.box.scripts, self.SCRIPT, CONF=str(host.config), LIBDIR=str(host.libexec)
        )
        self.blocks = self.box.home / "aorg-owner" / "blockers"
        return host

    def run_script(self, *args: str, extra: dict | None = None) -> subprocess.CompletedProcess:
        return self.box.run(self.script, *args, extra=extra)

    def exported(self) -> list[str]:
        return sorted(p.name for p in self.fh.export.iterdir())

    def delivered(self) -> list[Path]:
        return sorted(self.blocks.glob("*/*")) if self.blocks.exists() else []

    def app_calls(self) -> list[dict]:
        return [
            e
            for e in self.fh.calls()
            if e["kind"] == "app" and e["args"][:2] == ["storage", "cutover-blockers"]
        ]

    def printed(self, name: str, data: bytes = b"batman\n") -> str:
        """Make the file in export_dir and return the EXPORTED line that is true of it."""
        path = self.fh.export / name
        path.write_bytes(data)
        path.chmod(0o600)
        return f"EXPORTED {name} {len(data)} {sha(data)}"

    def stub_prints(self, name: str, *lines: str) -> None:
        self.fake_stub(name, "cat <<'EOF'\n" + "\n".join(lines) + "\nEOF")

    def fake_stub(self, name: str, body: str) -> None:
        """Replace one helper stub with a script that prints/does body."""
        p = self.fh.libexec / name
        p.write_text(f"#!/bin/sh\n{body}\n")
        p.chmod(0o755)


class ListScript(BlockersBase):
    SCRIPT = "cutover-blockers-list.sh"

    def test_moves_both_files_checks_them_and_prints_the_list(self) -> None:
        host = self.host()
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        files = self.delivered()
        self.assertEqual(sorted(f.suffix for f in files), [".json", ".txt"])
        for f in files:
            self.assertEqual(stat.S_IMODE(f.stat().st_mode), 0o600, f.name)
        self.assertEqual(stat.S_IMODE(files[0].parent.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(self.blocks.stat().st_mode), 0o700)
        by = {f.suffix: f for f in files}
        self.assertTrue(by[".txt"].read_text().startswith(LIST_TEXT))
        self.assertEqual(by[".json"].read_text(), ROWS)
        self.assertEqual(self.exported(), [], "the copies in export_dir are deleted")
        self.assertIn(LIST_TEXT, p.stdout)
        self.assertNotIn(SECRET_B64, p.stdout, "a value is never printed")
        self.assertEqual(self.box.sudo_calls(), [f"sudo -n {host.libexec}/cutover-blockers-list"])
        self.assertEqual([c["args"][2] for c in self.app_calls()], ["list"])

    def test_nothing_blocking_leaves_only_the_text(self) -> None:
        self.host(blockers_rows="")
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertEqual([f.suffix for f in self.delivered()], [".txt"])
        self.assertIn("No blocking rows", p.stdout)

    def test_dry_run_runs_nothing_as_root(self) -> None:
        self.host()
        p = self.run_script("--dry-run")
        self.assertEqual(p.returncode, 0, text(p))
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertEqual(self.app_calls(), [])
        self.assertEqual(self.delivered(), [])
        self.assertIn("dry run only", p.stdout)

    def test_refuses_unless_the_service_is_inactive(self) -> None:
        self.host()
        for state in ("active", "activating", "failed", "deactivating", None):
            self.box.set_state(state)
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{state}\n{text(p)}")
            self.assertIn("never stops or starts it", p.stderr, str(state))
            self.assertEqual(self.box.sudo_calls(), [], str(state))
        self.box.set_state("inactive")
        self.assertEqual(self.run_script().returncode, 0)

    def test_export_directory_problems_refuse_before_sudo(self) -> None:
        host = self.host()
        host.export.chmod(0o755)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("not mode 0700", p.stderr)
        host.export.chmod(0o700)
        real = host.export.with_name("real-export")
        host.export.rename(real)
        os.symlink(real, host.export)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("symlink", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_host_without_export_dir_or_stub_points_at_p6(self) -> None:
        host = self.host(config={"export_dir": None})
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("r2-p6-reinstall.sh", p.stderr)
        host = self.host()
        (host.libexec / "cutover-blockers-list").unlink()
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not installed", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_helper_refusal_delivers_nothing(self) -> None:
        self.host(blockers_exit=3)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("Nothing was delivered", p.stderr)
        self.assertEqual(self.delivered(), [])

    def test_names_and_lines_the_helper_prints_are_held_to_a_grammar(self) -> None:
        self.host()
        bad = {
            "traversal": f"EXPORTED ../../etc/passwd 5 {SHA}",
            "a result name": f"EXPORTED aorg-cutover-blockers-result-{STAMP}.txt 5 {SHA}",
            "metacharacters": f"EXPORTED aorg-cutover-blockers-{STAMP}.txt;id 5 {SHA}",
            "nothing": "true",
            "noise": f"hi\nEXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}\nABSENT rows",
            "rows neither": f"EXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}",
            "rows both": (
                f"EXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}\n"
                f"EXPORTED aorg-cutover-blockers-{STAMP}.json 5 {SHA}\nABSENT rows"
            ),
            "the same list twice": (
                f"EXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}\n"
                f"EXPORTED aorg-cutover-blockers-{STAMP}.txt 5 {SHA}\nABSENT rows"
            ),
        }
        for label, out in bad.items():
            body = "true" if out == "true" else f"cat <<'EOF'\n{out}\nEOF"
            self.fake_stub("cutover-blockers-list", body)
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertEqual(self.delivered(), [], label)

    def test_a_lie_about_files_that_do_exist_is_refused_for_its_own_reason(self) -> None:
        # Every file named below exists with the size and hash printed for it, so
        # only the script's reading of the helper's lines can refuse them.
        self.host()
        txt = f"aorg-cutover-blockers-{STAMP}.txt"
        js = f"aorg-cutover-blockers-{STAMP}.json"
        res = f"aorg-cutover-blockers-result-{STAMP}.txt"
        for label, lines, why in (
            ("a result name as the list", lambda: [self.printed(res), "ABSENT rows"], "accept"),
            (
                "a list name with a suffix",
                lambda: [self.printed(txt + ".bak"), "ABSENT rows"],
                "accept",
            ),
            (
                "a rows name with a suffix",
                lambda: [self.printed(txt), self.printed(js + ".bak")],
                "accept",
            ),
            ("a result name as the rows", lambda: [self.printed(txt), self.printed(res)], "accept"),
            (
                "the list twice",
                lambda: [self.printed(txt), self.printed(txt), "ABSENT rows"],
                "accept",
            ),
            (
                "a line it does not know",
                lambda: [self.printed(txt), "ABSENT rows", "hello"],
                "does not recognise",
            ),
            (
                "ABSENT twice",
                lambda: [self.printed(txt), "ABSENT rows", "ABSENT rows"],
                "does not recognise",
            ),
            ("rows with no list", lambda: [self.printed(js)], "did not report the list"),
            ("nothing at all", lambda: [], "did not report the list"),
            (
                "rows both present and absent",
                lambda: [self.printed(txt), self.printed(js), "ABSENT rows"],
                "both present and absent",
            ),
            ("rows neither present nor absent", lambda: [self.printed(txt)], "neither present"),
        ):
            self.stub_prints("cutover-blockers-list", *lines())
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertIn(why, p.stderr, label)
            self.assertEqual(self.delivered(), [], label)
        self.assertIn(txt, self.exported(), "the files stay where they were")

    def test_a_helper_that_exits_non_zero_after_printing_a_good_list_delivers_nothing(
        self,
    ) -> None:
        host = self.host()
        real = host.libexec / "cutover-blockers-list"
        shutil.copy2(real, real.with_name("cutover-blockers-list.real"))
        self.fake_stub(
            "cutover-blockers-list", '"$(dirname "$0")/cutover-blockers-list.real"; exit 3'
        )
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("the helper exited 3", p.stderr)
        self.assertEqual(self.delivered(), [])
        self.assertEqual(len(self.exported()), 2, "the helper's files stay")

    def test_a_copy_that_is_not_the_file_is_refused_and_the_sources_stay(self) -> None:
        self.host()
        p = self.run_script(extra={"SHIM_INSTALL_CORRUPT": "aorg-cutover-blockers-"})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("does not match", p.stderr)
        self.assertEqual(len(self.exported()), 2, "the sources stay in export_dir")

    def test_a_file_that_is_not_what_the_helper_reported_stays_put(self) -> None:
        host = self.host()
        name = f"aorg-cutover-blockers-{STAMP}.txt"
        for label, data, reported in (
            ("wrong size", b"abc", f"EXPORTED {name} 99 {sha(b'abc')}"),
            ("wrong hash", b"abc", f"EXPORTED {name} 3 {SHA}"),
        ):
            (host.export / name).write_bytes(data)
            self.fake_stub("cutover-blockers-list", f"cat <<'EOF'\n{reported}\nABSENT rows\nEOF")
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertIn("stay in", p.stderr, label)
            self.assertEqual(self.exported(), [name], label)
            self.assertEqual(self.delivered(), [], label)

    def test_a_symlink_in_place_of_the_file_is_refused(self) -> None:
        host = self.host()
        name = f"aorg-cutover-blockers-{STAMP}.txt"
        target = host.root / "elsewhere"
        target.write_bytes(b"abc")
        os.symlink(target, host.export / name)
        self.fake_stub(
            "cutover-blockers-list",
            f"cat <<'EOF'\nEXPORTED {name} 3 {sha(b'abc')}\nABSENT rows\nEOF",
        )
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertEqual(self.delivered(), [])
        self.assertTrue(target.exists())

    def test_a_directory_that_is_not_yours_is_refused(self) -> None:
        host = self.host()
        p = self.run_script(extra={"SHIM_FAKE_UID_FOR": str(host.export)})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not owned by", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_reported_file_that_is_not_yours_stays_put(self) -> None:
        host = self.host()
        p = self.run_script(extra={"SHIM_FAKE_UID_FOR": "aorg-cutover-blockers-2"})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not owned by", p.stderr)
        self.assertEqual(self.delivered(), [])
        self.assertEqual(len(self.exported()), 2, "both files stay in export_dir")
        self.assertTrue(host.export.exists())

    def test_a_config_value_with_a_shell_character_is_refused(self) -> None:
        self.host(config={"export_dir": "/x;touch pwned"})
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("character this script will not pass on", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_arguments_other_than_dry_run_are_refused(self) -> None:
        self.host()
        for args in (["--run"], ["x"], ["--dry-run", "x"]):
            p = self.run_script(*args)
            self.assertEqual(p.returncode, 2, f"{args}\n{text(p)}")
        self.assertEqual(self.box.sudo_calls(), [])


class ApplyScript(BlockersBase):
    SCRIPT = "cutover-blockers-apply.sh"

    def request(self, doc=None, mode: int = 0o600) -> Path:
        doc = doc if doc is not None else {"action": "fix", "key": ROBIN, "value": SECRET_B64}
        p = self.box.root / "mine" / "request.json"
        p.parent.mkdir(exist_ok=True)
        p.unlink(missing_ok=True)
        p.write_bytes(doc if isinstance(doc, bytes) else json.dumps(doc).encode())
        p.chmod(mode)
        return p

    def test_fix_places_the_request_runs_the_helper_and_moves_the_result(self) -> None:
        host = self.host()
        req = self.request()
        p = self.run_script(str(req))
        self.assertEqual(p.returncode, 0, text(p))
        (call,) = self.app_calls()
        self.assertEqual(call["args"][2:4], ["fix", f"hex:{ROBIN}"])
        self.assertEqual(json.loads(call["from_text"]), [{"key": ROBIN, "value": SECRET_B64}])
        (result,) = self.delivered()
        self.assertRegex(result.name, r"^aorg-cutover-blockers-result-[0-9]{8}T[0-9]{6}Z\.txt$")
        self.assertEqual(stat.S_IMODE(result.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(result.parent.stat().st_mode), 0o700)
        self.assertIn(f"Fixed hex:{ROBIN}", result.read_text())
        self.assertIn(f"Fixed hex:{ROBIN}", p.stdout)
        self.assertNotIn(SECRET_B64, p.stdout)
        self.assertEqual(self.exported(), [], "the request and the result are gone")
        self.assertEqual(self.box.sudo_calls(), [f"sudo -n {host.libexec}/cutover-blockers-apply"])
        self.assertTrue(req.exists(), "the owner's own request file is left alone")

    def test_delete_runs_with_confirm_and_no_value(self) -> None:
        self.host()
        p = self.run_script(str(self.request({"action": "delete", "key": BATGIRL})))
        self.assertEqual(p.returncode, 0, text(p))
        (call,) = self.app_calls()
        self.assertEqual(call["args"][2:3] + call["args"][-1:], ["delete", "--confirm"])
        self.assertIn(f"hex:{BATGIRL}", call["args"])
        self.assertNotIn("from_text", call)

    def test_a_request_already_waiting_is_used_without_an_argument(self) -> None:
        host = self.host()
        (host.export / REQUEST).write_text(json.dumps({"action": "delete", "key": ROBIN}))
        (host.export / REQUEST).chmod(0o600)
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertEqual(len(self.app_calls()), 1)
        self.assertEqual(self.exported(), [])

    def test_the_same_request_in_place_is_reused_a_different_one_never_replaced(self) -> None:
        host = self.host()
        req = self.request({"action": "delete", "key": ROBIN})
        (host.export / REQUEST).write_bytes(req.read_bytes())
        (host.export / REQUEST).chmod(0o600)
        p = self.run_script(str(req))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertIn("reusing", p.stdout)
        # A different request waiting: nothing changes, nothing runs.
        (host.export / REQUEST).write_text(json.dumps({"action": "delete", "key": BATGIRL}))
        (host.export / REQUEST).chmod(0o600)
        before = (host.export / REQUEST).read_bytes()
        calls = len(self.box.sudo_calls())
        p = self.run_script(str(req))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("a different request is already in", p.stderr)
        self.assertEqual((host.export / REQUEST).read_bytes(), before)
        self.assertEqual(len(self.box.sudo_calls()), calls)

    def test_a_bad_request_file_refuses_before_anything_is_placed_or_run(self) -> None:
        host = self.host()
        link = self.box.root / "mine" / "link.json"
        link.parent.mkdir(exist_ok=True)
        os.symlink(self.request(), link)
        big = self.box.root / "mine" / "big.json"
        with open(big, "wb") as f:
            f.truncate((16 << 20) + 1)
        big.chmod(0o600)
        empty = self.box.root / "mine" / "empty.json"
        empty.write_bytes(b"")
        empty.chmod(0o600)
        for label, arg in {
            "missing": str(self.box.root / "mine" / "nope.json"),
            "group readable": str(self.request(mode=0o640)),
            "other readable": str(self.request(mode=0o604)),
            "symlink": str(link),
            "oversize": str(big),
            "empty": str(empty),
            "a directory": str(self.box.root / "mine"),
        }.items():
            p = self.run_script(arg)
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertEqual(self.exported(), [], label)
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertEqual(self.app_calls(), [])
        self.assertTrue(host.export.exists())

    def test_a_request_file_that_is_not_yours_is_refused(self) -> None:
        self.host()
        req = self.request()
        p = self.run_script(str(req), extra={"SHIM_FAKE_UID_FOR": req.name})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not owned by", p.stderr)
        self.assertEqual(self.exported(), [])
        self.assertEqual(self.box.sudo_calls(), [])

    def test_host_problems_refuse_before_anything_is_placed_or_run(self) -> None:
        host = self.host()
        req = self.request()

        def refused(why: str, **kw) -> None:
            p = self.run_script(str(req), **kw)
            self.assertNotEqual(p.returncode, 0, f"{why}\n{text(p)}")
            self.assertIn(why, p.stderr)
            self.assertEqual(self.box.sudo_calls(), [], why)
            self.assertEqual(self.app_calls(), [], why)

        host.export.chmod(0o755)
        refused("is not mode 0700")
        host.export.chmod(0o700)
        refused("is not owned by", extra={"SHIM_FAKE_UID_FOR": str(host.export)})
        stub = host.libexec / "cutover-blockers-apply"
        kept = stub.read_bytes()
        stub.unlink()
        refused("is not installed")
        stub.write_bytes(kept)
        stub.chmod(0o755)
        real = host.export.with_name("real-export")
        host.export.rename(real)
        os.symlink(real, host.export)
        refused("symlink")
        self.assertEqual(sorted(p.name for p in real.iterdir()), [], "nothing was placed")

    def test_a_request_that_is_a_symlink_is_refused(self) -> None:
        self.host()
        req = self.request()
        link = req.with_name("link.json")
        os.symlink(req, link)
        p = self.run_script(str(link))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("a symlink is refused", p.stderr)
        self.assertEqual(self.exported(), [])
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_waiting_copy_of_the_request_that_is_not_0600_is_refused_by_the_script(self) -> None:
        host = self.host()
        req = self.request()
        waiting = host.export / REQUEST
        waiting.write_bytes(req.read_bytes())
        waiting.chmod(0o644)
        p = self.run_script(str(req))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("must be mode 0600", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [], "refused here, not left to the helper")

    def test_a_placed_request_that_is_not_the_file_given_is_refused_before_the_helper(self) -> None:
        self.host()
        p = self.run_script(str(self.request()), extra={"SHIM_INSTALL_CORRUPT": ".request."})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not the file you gave", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_result_copy_that_is_not_the_file_is_refused_and_the_source_stays(self) -> None:
        host = self.host()
        p = self.run_script(
            str(self.request()), extra={"SHIM_INSTALL_CORRUPT": "aorg-cutover-blockers-result-"}
        )
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("does not match", p.stderr)
        self.assertEqual(len(list(host.export.glob("aorg-cutover-blockers-result-*"))), 1)

    def test_a_success_without_a_result_it_accepts_is_a_failure(self) -> None:
        host = self.host()
        real = host.libexec / "cutover-blockers-apply"
        shutil.copy2(real, real.with_name("cutover-blockers-apply.real"))
        evil = f"aorg-cutover-blockers-result-{STAMP}.txt.bak"
        line = self.printed(evil)
        self.fake_stub(
            "cutover-blockers-apply",
            f'"$(dirname "$0")/cutover-blockers-apply.real" >/dev/null\ncat <<\'EOF\'\n{line}\n'
            f"REQUEST-REMOVED {REQUEST}\nEOF",
        )
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("reported no result", p.stderr)
        self.assertEqual(self.delivered(), [])

    def test_a_request_that_is_still_there_after_a_reported_removal_is_a_failure(self) -> None:
        host = self.host()
        real = host.libexec / "cutover-blockers-apply"
        shutil.copy2(real, real.with_name("cutover-blockers-apply.real"))
        self.fake_stub(
            "cutover-blockers-apply",
            f'"$(dirname "$0")/cutover-blockers-apply.real"; rc=$?\n'
            f"printf '{{}}' > '{host.export}/{REQUEST}'\nexit $rc",
        )
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is still there", p.stderr)

    def test_a_waiting_request_that_is_not_yours_or_not_0600_is_refused(self) -> None:
        host = self.host()
        waiting = host.export / REQUEST
        waiting.write_text(json.dumps({"action": "delete", "key": ROBIN}))
        waiting.chmod(0o644)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("must be mode 0600", p.stderr)
        waiting.chmod(0o600)
        p = self.run_script(extra={"SHIM_FAKE_UID_FOR": REQUEST})
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not owned by", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_request_that_appears_while_placing_is_never_replaced(self) -> None:
        host = self.host()
        p = self.run_script(
            str(self.request()), extra={"SHIM_RACE_FILE": str(host.export / REQUEST)}
        )
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("could not place the request", p.stderr)
        self.assertEqual((host.export / REQUEST).read_text(), "RACE")
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertEqual([f for f in self.exported() if f != REQUEST], [], "no temp file is left")

    def test_a_config_value_with_a_shell_character_is_refused(self) -> None:
        self.host(config={"export_dir": "/x;touch pwned"})
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("character this script will not pass on", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_nothing_given_and_nothing_waiting_refuses(self) -> None:
        self.host()
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("none waiting", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_dry_run_places_nothing_and_runs_nothing(self) -> None:
        self.host()
        p = self.run_script("--dry-run", str(self.request()))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertEqual(self.exported(), [])
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertEqual(self.app_calls(), [])
        self.assertIn("dry run only", p.stdout)

    def test_refuses_unless_the_service_is_inactive_and_places_nothing(self) -> None:
        self.host()
        req = self.request()
        for state in ("active", "activating", "failed", None):
            self.box.set_state(state)
            p = self.run_script(str(req))
            self.assertNotEqual(p.returncode, 0, f"{state}\n{text(p)}")
            self.assertIn("never stops or starts it", p.stderr, str(state))
            self.assertEqual(self.exported(), [], str(state))
            self.assertEqual(self.box.sudo_calls(), [], str(state))

    def test_a_failed_apply_says_it_may_have_applied_and_keeps_the_request(self) -> None:
        self.host(blockers_exit=3)
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("MAY have been applied", p.stderr)
        self.assertIn("cutover-blockers-list.sh", p.stderr)
        self.assertEqual(REQUEST in self.exported(), True, "a failed apply keeps the request")

    def test_a_request_the_installed_helper_would_refuse_is_never_placed(self) -> None:
        self.host()
        bad = {
            "unknown action": {"action": "explode", "key": ROBIN},
            "uppercase key": {"action": "delete", "key": ROBIN.upper() + "A"},
            "fix without a value": {"action": "fix", "key": ROBIN},
            "unknown field": {"action": "delete", "key": ROBIN, "why": "x"},
        }
        for label, doc in bad.items():
            for flags in ((), ("--dry-run",)):
                p = self.run_script(*flags, str(self.request(doc)))
                self.assertNotEqual(p.returncode, 0, f"{label} {flags}\n{text(p)}")
                self.assertIn("not a request the installed helper accepts", p.stderr, label)
                self.assertEqual(self.exported(), [], label)
                self.assertEqual(self.box.sudo_calls(), [], label)
        self.assertEqual(self.app_calls(), [])

    def test_a_helper_refusal_before_it_started_takes_back_the_request_this_run_placed(self) -> None:
        host = self.host()
        host.transient_state.write_text("active\n")  # the helper's own view: not inactive
        req = self.request()
        p = self.run_script(str(req))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("NOT RUN", p.stderr)
        self.assertIn("the request this run placed was removed", p.stderr)
        self.assertNotIn("MAY have been applied", p.stderr)
        self.assertEqual(self.exported(), [], "nothing is left to get stuck")
        self.assertEqual(self.app_calls(), [])
        self.assertTrue(req.exists(), "the owner's own file is left alone")
        # and a corrected run just works
        host.transient_state.unlink()
        p = self.run_script(str(req))
        self.assertEqual(p.returncode, 0, text(p))

    def test_a_request_that_was_waiting_before_the_run_is_left_after_a_not_run(self) -> None:
        host = self.host()
        host.transient_state.write_text("active\n")
        (host.export / REQUEST).write_text(json.dumps({"action": "delete", "key": ROBIN}))
        (host.export / REQUEST).chmod(0o600)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("NOT RUN", p.stderr)
        self.assertIn("was there before this run, so it was left", p.stderr)
        self.assertEqual(self.exported(), [REQUEST])

    def test_a_not_run_needs_both_the_exit_status_and_the_line(self) -> None:
        self.host()
        req = self.request()
        for label, body in (
            ("status 4 without the line", "exit 4"),
            ("the line with status 1", "echo NOT-RUN; exit 1"),
            ("the line with status 0", "echo NOT-RUN"),
        ):
            self.fake_stub("cutover-blockers-apply", body)
            p = self.run_script(str(req))
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertNotIn("NOT RUN", p.stderr, label)
            self.assertEqual(self.exported(), [REQUEST], f"{label}: the request stays")
            (self.fh.export / REQUEST).unlink()

    def test_leftovers_of_an_interrupted_placement_are_cleared_and_never_block_a_run(self) -> None:
        host = self.host()
        req = self.request()
        # An interrupted `ln` then `rm`: the fixed name AND the temp name are one
        # file, which the helper refuses as hard-linked.
        (host.export / REQUEST).write_bytes(req.read_bytes())
        (host.export / REQUEST).chmod(0o600)
        os.link(host.export / REQUEST, host.export / ".request.4242")
        (host.export / ".request.7").write_text("x")
        p = self.run_script("--dry-run", str(req))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertIn("a real run would remove these leftovers", p.stdout)
        self.assertEqual(self.exported(), sorted([REQUEST, ".request.4242", ".request.7"]))
        p = self.run_script(str(req))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertIn("removing leftovers of an interrupted run", p.stdout)
        self.assertEqual(self.exported(), [])
        self.assertEqual(len(self.app_calls()), 1)

    def test_the_owners_copy_is_deleted_after_a_success_only_when_asked(self) -> None:
        self.host()
        req = self.request()
        p = self.run_script(str(req))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertTrue(req.exists(), "no terminal and no flag: left")
        self.assertIn("--delete-request", p.stdout)
        p = self.run_script("--delete-request", str(req))
        self.assertEqual(p.returncode, 0, text(p))
        self.assertFalse(req.exists())
        self.assertIn(f"deleted {req}", p.stdout)

    def test_the_owners_copy_is_kept_after_a_failure_even_when_asked(self) -> None:
        self.host(blockers_exit=3)
        req = self.request()
        p = self.run_script("--delete-request", str(req))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertTrue(req.exists())

    def test_success_without_the_removal_line_is_a_failure(self) -> None:
        host = self.host()
        name = f"aorg-cutover-blockers-result-{STAMP}.txt"
        body = b"Fixed it.\n"
        (host.export / name).write_bytes(body)
        self.fake_stub(
            "cutover-blockers-apply", f"cat <<'EOF'\nEXPORTED {name} {len(body)} {sha(body)}\nEOF"
        )
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("did not report removing the request", p.stderr)
        self.assertEqual([f.name for f in self.delivered()], [name], "the result is still kept")

    def test_a_result_that_is_not_what_was_reported_stays_put(self) -> None:
        host = self.host()
        name = f"aorg-cutover-blockers-result-{STAMP}.txt"
        (host.export / name).write_bytes(b"abc")
        self.fake_stub(
            "cutover-blockers-apply",
            f"cat <<'EOF'\nEXPORTED {name} 3 {SHA}\nREQUEST-REMOVED {REQUEST}\nEOF",
        )
        p = self.run_script(str(self.request()))
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn(name, self.exported())
        self.assertEqual(self.delivered(), [])

    def test_arguments(self) -> None:
        self.host()
        for args in (["--go"], ["a", "b"]):
            self.assertEqual(self.run_script(*args).returncode, 2, str(args))
        self.assertEqual(self.box.sudo_calls(), [])


class P6Base(unittest.TestCase):
    """r2-p6-reinstall.sh and r2-p6-undo.sh against a local repository whose
    Makefile records what the installer was asked."""

    SCRIPT = ""

    BEFORE = (
        "bin = {bin}\ndb = {db}\nstate_dir = {state}\nunit = audiobook-organizer.service\n"
        "service_user = alfred\narchive_dir = {arch}\nstage_bin = {sbin}\n"
        "stage_local_conf = {slc}\ncheckpoint_dir = \nservice_group = \n"
    )

    def setUp(self) -> None:
        self.box = Sandbox(self)
        r = self.box.root
        self.state = r / "state"
        self.state.mkdir()
        self.conf = r / "rollback.conf"
        self.after = r / "after.conf"
        self.installlog = r / "install.log"
        self.origin = r / "origin.git"
        work = r / "work"
        work.mkdir()
        mk = (
            "install-rollback-helpers:\n"
            "\t@printf '%s\\n' \"APPLY=$(APPLY) REPLACE_CONF=$(REPLACE_CONF) UNIT=$(DEPLOY_UNIT) "
            'BIN=$(DEPLOY_BIN) DB=$(DB_OR) ARGS=$(ROLLBACK_INSTALL_ARGS)" >> "$$INSTALL_LOG"\n'
            '\t@if [ -n "$(APPLY)" ]; then cp "$$AFTER_CONF" "$$CONF_PATH"; fi\n'
        )
        mk = mk.replace("$(DB_OR)", "$(DEPLOY_DB)")
        (work / "Makefile").write_text(mk)
        helper = work / "deploy" / "rollback-helpers" / "rollback_helper.py"
        helper.parent.mkdir(parents=True)
        helper.write_text("# the bundle of the pinned commit\n")
        self.pin_helper_sha = sha(helper.read_bytes())
        git = ["git", "-C", str(work), "-c", "user.name=Alfred", "-c", "user.email=a@wayne.invalid"]
        subprocess.run([*git[:3], "init", "-q", "-b", "main"], check=True)
        subprocess.run([*git, "add", "Makefile", "deploy"], check=True)
        subprocess.run([*git, "commit", "-q", "-m", "batcave"], check=True)
        self.pin = subprocess.run(
            [*git, "rev-parse", "HEAD"], check=True, capture_output=True, text=True
        ).stdout.strip()
        subprocess.run(["git", "clone", "-q", "--bare", str(work), str(self.origin)], check=True)
        self.write_conf(
            self.BEFORE.format(
                bin=r / "bin",
                db=r / "db",
                state=self.state,
                arch=r / "arch",
                sbin=r / "stage",
                slc=r / "stage.conf",
            )
            + self.PRE_EXTRA.format(state=self.state, export=self.export_dir)
        )
        # The helper bundle installed on the host before P6.
        self.libdir = r / "libdir"
        self.libdir.mkdir()
        self.installed_helper = self.libdir / "rollback_helper.py"
        self.installed_helper.write_text("# the bundle installed before P6\n")
        self.script = script_copy(
            self.box.scripts,
            self.SCRIPT,
            PIN=self.pin,
            CONF=str(self.conf),
            REPO=str(self.origin),
            **self.EXTRA_SUBS(),
        )

    def EXTRA_SUBS(self) -> dict[str, str]:
        return {"LIBDIR": str(self.libdir)} if self.SCRIPT == "r2-p6-reinstall.sh" else {}

    @property
    def saved(self) -> Path:
        return self.box.home / "aorg-owner" / "p6-before"

    PRE_EXTRA = ""

    @property
    def export_dir(self) -> Path:
        return self.box.home / "aorg-owner" / "export"

    def write_conf(self, body: str) -> None:
        self.conf.write_text(body)

    def run_script(self, *args: str) -> subprocess.CompletedProcess:
        self.box.shims.joinpath("sudo-l").touch()
        extra = {
            "INSTALL_LOG": str(self.installlog),
            "AFTER_CONF": str(self.after),
            "CONF_PATH": str(self.conf),
        }
        return self.box.run(self.script, *args, extra=extra)

    def installs(self) -> list[str]:
        return self.installlog.read_text().splitlines() if self.installlog.exists() else []

    def grants(self, stubs) -> None:
        # The host's own grants are always in a real listing; an empty one is a different case.
        lines = [f'    (root) NOPASSWD: {LIBDIR_REAL}/restore-offline "" ']
        lines += [f'    (root) NOPASSWD: {LIBDIR_REAL}/{s} ""' for s in stubs]
        (self.box.shims / "sudo-l").write_text("\n".join(lines) + "\n")


class ReinstallScript(P6Base):
    SCRIPT = "r2-p6-reinstall.sh"

    def after_conf(self, offline: str | None = None, export: bool = True) -> None:
        body = self.conf.read_text()
        body = re.sub(r"^offline_env.*\n", "", body, flags=re.M)
        body = re.sub(r"^export_dir.*\n", "", body, flags=re.M)
        body += f"offline_env = {offline or f'HOME={self.state} ABO_CORE_PATHS_STATE_DIR={self.state}'}\n"
        if export:
            body += f"export_dir = {self.export_dir}\n"
        self.after.write_text(body)

    def test_an_unpinned_copy_refuses(self) -> None:
        text_ = (OWNER / self.SCRIPT).read_text()
        self.assertEqual(text_.count('PIN="__PIN__"'), 1)
        plain = self.box.scripts / "plain.sh"
        plain.write_text(text_)
        p = self.box.run(plain)
        self.assertEqual(p.returncode, 1, text(p))
        self.assertIn("no pinned commit", p.stderr)

    def test_dry_run_shows_the_installer_dry_run_and_changes_nothing(self) -> None:
        self.after_conf()
        before = self.conf.read_text()
        p = self.run_script("--dry-run")
        self.assertEqual(p.returncode, 0, text(p))
        (line,) = self.installs()
        self.assertTrue(line.startswith("APPLY= REPLACE_CONF=1 "), line)
        self.assertIn(
            f"--offline-env 'HOME={self.state} ABO_CORE_PATHS_STATE_DIR={self.state}'", line
        )
        self.assertIn(f"--export-dir {self.export_dir}", line)
        self.assertNotIn("ABK_STATE_DIR", line)
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertEqual(self.conf.read_text(), before)
        self.assertEqual(stat.S_IMODE(self.export_dir.stat().st_mode), 0o700)

    def test_the_install_and_its_checks(self) -> None:
        self.after_conf()
        self.grants(STUBS)
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        dry, real = self.installs()
        self.assertTrue(dry.startswith("APPLY= "))
        self.assertTrue(real.startswith("APPLY=1 REPLACE_CONF=1 "), real)
        self.assertIn("P6 is in place", p.stdout)
        for s in STUBS:
            self.assertIn(f"ok: sudoers grants {s} with no arguments", p.stdout)
        # Run again: still fine.
        self.assertEqual(self.run_script().returncode, 0)

    def test_the_pre_p6_state_is_saved_once_before_the_install(self) -> None:
        self.after_conf()
        self.grants(())
        before_conf = self.conf.read_text()
        want = sha(self.installed_helper.read_bytes())
        p = self.run_script("--dry-run")
        self.assertEqual(p.returncode, 0, text(p))
        self.assertFalse(self.saved.exists(), "a dry run saves nothing")
        self.assertIn("a real run saves the pre-P6 state", p.stdout)
        self.grants(STUBS)
        before_sudo_l = (self.box.shims / "sudo-l").read_text()
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertEqual((self.saved / "helper.sha256").read_text().strip(), want)
        self.assertEqual((self.saved / "rollback.conf").read_text(), before_conf)
        self.assertEqual((self.saved / "sudo-l.txt").read_text(), before_sudo_l)
        self.assertTrue((self.saved / "complete").exists())
        self.assertEqual(stat.S_IMODE(self.saved.stat().st_mode), 0o700)
        for name in ("helper.sha256", "rollback.conf", "sudo-l.txt"):
            self.assertEqual(stat.S_IMODE((self.saved / name).stat().st_mode), 0o600, name)
        self.assertEqual(
            [q.name for q in self.saved.parent.glob("p6-before.tmp.*")], [], "no temp left over"
        )
        # A rerun, after P6 changed both the conf and the helper, keeps the first save.
        self.installed_helper.write_text("# the bundle P6 installed\n")
        again = self.run_script()
        self.assertEqual(again.returncode, 0, text(again))
        self.assertIn("already saved by an earlier run: kept", again.stdout)
        self.assertEqual((self.saved / "helper.sha256").read_text().strip(), want)
        self.assertEqual((self.saved / "rollback.conf").read_text(), before_conf)
        # And a dry run after a save says it will be kept.
        self.assertIn("already saved", self.run_script("--dry-run").stdout)

    def test_a_helper_that_is_not_installed_is_saved_as_absent(self) -> None:
        self.after_conf()
        self.grants(STUBS)
        self.installed_helper.unlink()
        self.assertEqual(self.run_script().returncode, 0)
        self.assertEqual((self.saved / "helper.sha256").read_text().strip(), "absent")

    def test_an_incomplete_save_is_not_trusted_or_overwritten(self) -> None:
        self.after_conf()
        self.grants(STUBS)
        self.saved.mkdir(mode=0o700)
        (self.saved / "rollback.conf").write_text("half")
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not a complete save", p.stderr)
        self.assertEqual([i for i in self.installs() if i.startswith("APPLY=1")], [])
        self.assertEqual((self.saved / "rollback.conf").read_text(), "half")

    def test_each_check_fails_the_run(self) -> None:
        for label, setup in {
            "no export_dir": lambda: (self.after_conf(export=False), self.grants(STUBS)),
            "ABK left in offline_env": lambda: (
                self.after_conf(offline=f"HOME={self.state} ABK_STATE_DIR={self.state}"),
                self.grants(STUBS),
            ),
            "ABO and ABK both in offline_env": lambda: (
                self.after_conf(
                    offline=(
                        f"HOME={self.state} ABO_CORE_PATHS_STATE_DIR={self.state} "
                        f"ABK_STATE_DIR={self.state}"
                    )
                ),
                self.grants(STUBS),
            ),
            "a stub not granted": lambda: (self.after_conf(), self.grants(STUBS[:3])),
            "no stub granted": lambda: (self.after_conf(), self.grants(())),
        }.items():
            setup()
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertIn("send this output to the agents", p.stderr, label)

    def test_a_commit_not_on_main_or_a_dirty_checkout_refuses(self) -> None:
        self.after_conf()
        self.grants(STUBS)
        self.assertEqual(self.run_script("--dry-run").returncode, 0)
        src = self.box.home / "aorg-owner" / "src"
        (src / "Makefile").write_text("# edited\n")
        p = self.run_script("--dry-run")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("local changes", p.stderr)
        # A commit that is not on origin/main.
        bogus = script_copy(
            self.box.scripts, self.SCRIPT, PIN="0" * 40, CONF=str(self.conf), REPO=str(self.origin)
        )
        p = self.box.run(bogus, "--dry-run", extra={"INSTALL_LOG": str(self.installlog)})
        self.assertNotEqual(p.returncode, 0, text(p))

    def test_a_value_with_a_shell_character_is_refused(self) -> None:
        self.after_conf()
        self.write_conf(
            self.conf.read_text().replace("stage_bin = ", "stage_bin = /x;touch pwned #")
        )
        p = self.run_script("--dry-run")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("character this script will not pass on", p.stderr)
        self.assertEqual(self.installs(), [])

    def test_an_export_directory_that_cannot_be_made_0700_refuses(self) -> None:
        self.after_conf()
        self.grants(STUBS)
        self.export_dir.mkdir(mode=0o755)
        self.export_dir.chmod(0o755)
        chmod = self.box.shims / "chmod"
        chmod.write_text("#!/bin/sh\nexit 0\n")  # a chmod that changes nothing
        chmod.chmod(0o755)
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is not mode 0700", p.stderr)
        self.assertEqual(self.installs(), [])

    def test_a_symlinked_or_foreign_export_directory_refuses(self) -> None:
        self.after_conf()
        os.symlink(self.box.root, self.export_dir)
        p = self.run_script("--dry-run")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("symlink", p.stderr)


class UndoScript(P6Base):
    SCRIPT = "r2-p6-undo.sh"
    PRE_EXTRA = (
        "offline_env = HOME={state} ABO_CORE_PATHS_STATE_DIR={state}\nexport_dir = {export}\n"
    )

    def setUp(self) -> None:
        super().setUp()
        self.export_dir.mkdir(mode=0o700)
        self.undone()
        self.save_before()
        self.grants(())  # a real listing: the host's own grants, none of the four

    def save_before(self, sha_: str | None = None, conf: str | None = None) -> None:
        """What r2-p6-reinstall.sh saved: the helper hash of the PIN checkout, the
        pre-P6 rollback.conf (what the undo should restore) and the sudo -l text."""
        shutil.rmtree(self.saved, ignore_errors=True)
        self.saved.mkdir(mode=0o700)
        (self.saved / "helper.sha256").write_text((sha_ or self.pin_helper_sha) + "\n")
        (self.saved / "rollback.conf").write_text(conf or self.after.read_text())
        (self.saved / "sudo-l.txt").write_text("")
        (self.saved / "complete").write_text("")

    def undone(self, offline: str | None = None, export: bool = False) -> None:
        body = re.sub(r"^(offline_env|export_dir).*\n", "", self.conf.read_text(), flags=re.M)
        body += f"offline_env = {offline or f'HOME={self.state} ABK_STATE_DIR={self.state}'}\n"
        if export:
            body += f"export_dir = {self.export_dir}\n"
        self.after.write_text(body)

    def test_an_unpinned_copy_refuses(self) -> None:
        text_ = (OWNER / self.SCRIPT).read_text()
        self.assertEqual(text_.count('PIN="__PIN__"'), 1)
        plain = self.box.scripts / "plain.sh"
        plain.write_text(text_)
        p = self.box.run(plain)
        self.assertEqual(p.returncode, 1, text(p))
        self.assertIn("no pinned commit", p.stderr)

    def test_the_installer_is_asked_for_the_previous_environment_and_no_export_dir(self) -> None:
        p = self.run_script("--dry-run")
        self.assertEqual(p.returncode, 0, text(p))
        (line,) = self.installs()
        self.assertTrue(line.startswith("APPLY= REPLACE_CONF=1 "), line)
        self.assertIn(f"--offline-env 'HOME={self.state} ABK_STATE_DIR={self.state}'", line)
        self.assertNotIn("ABO_", line)
        self.assertNotIn("--export-dir", line)
        self.assertEqual(self.box.sudo_calls(), [])
        self.assertTrue(self.export_dir.is_dir(), "a dry run removes nothing")

    def test_the_undo_removes_an_empty_export_directory_and_checks(self) -> None:
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertFalse(self.export_dir.exists())
        self.assertIn("rollback.conf has no export_dir", p.stdout)
        self.assertIn("offline_env names ABK_STATE_DIR only", p.stdout)
        for s in STUBS:
            self.assertIn(f"sudoers no longer grants {s}", p.stdout)
        self.assertEqual(len(self.installs()), 2)
        # Again after a success: the save was retired, so there is nothing to put back.
        again = self.run_script()
        self.assertNotEqual(again.returncode, 0, text(again))
        self.assertIn("no saved pre-P6 state", again.stderr)

    def test_a_checkout_that_is_not_the_commit_p6_replaced_is_refused_before_anything(self) -> None:
        for label, sha_, why in (
            ("another commit's helper", sha(b"# some other bundle\n"), "is not the one installed before P6"),
            ("no helper was installed", "absent", "no previous bundle to put back"),
        ):
            self.save_before(sha_=sha_)
            for flags in ((), ("--dry-run",)):
                p = self.run_script(*flags)
                self.assertNotEqual(p.returncode, 0, f"{label} {flags}\n{text(p)}")
                self.assertIn(why, p.stderr, label)
                self.assertEqual(self.installs(), [], label)
                self.assertEqual(self.box.sudo_calls(), [], label)
        self.assertTrue(self.export_dir.is_dir())

    def test_no_saved_state_means_nothing_to_put_back(self) -> None:
        shutil.rmtree(self.saved)
        p = self.run_script("--dry-run")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("no saved pre-P6 state", p.stderr)
        self.assertEqual(self.installs(), [])
        self.save_before()
        (self.saved / "complete").unlink()
        self.assertNotEqual(self.run_script("--dry-run").returncode, 0)

    def test_a_listing_that_is_empty_or_lacks_the_hosts_own_grants_is_not_proof(self) -> None:
        for label, body in (
            ("empty", ""),
            ("no restore-offline", f'    (root) NOPASSWD: {LIBDIR_REAL}/inflight-probe ""\n'),
        ):
            self.run_with_listing(body)
            p = self.last
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertIn("CANNOT TELL", p.stdout, label)
            self.assertIn("send this output to the agents", p.stderr, label)
            self.undone()
            self.save_before()
            self.export_dir.mkdir(mode=0o700, exist_ok=True)

    def run_with_listing(self, body: str) -> None:
        """Run the script with this exact `sudo -n -l` text (run_script's own
        helper would replace it)."""
        extra = {
            "INSTALL_LOG": str(self.installlog),
            "AFTER_CONF": str(self.after),
            "CONF_PATH": str(self.conf),
        }
        (self.box.shims / "sudo-l").write_text(body)
        self.last = self.box.run(self.script, extra=extra)

    def test_a_restored_conf_that_differs_from_the_saved_one_fails_with_the_diff(self) -> None:
        self.save_before(conf=self.after.read_text() + "# a line the old file had\n")
        p = self.run_script()
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("a line the old file had", p.stdout)
        self.assertIn("differs from the copy saved before P6", p.stdout)
        self.assertIn("send this output to the agents", p.stderr)
        self.assertTrue(self.saved.exists(), "a failed undo keeps the save")
        # Safe to run again after a failure: once the saved copy matches, it passes.
        self.save_before()
        again = self.run_script()
        self.assertEqual(again.returncode, 0, text(again))

    def test_a_good_undo_says_the_conf_is_what_it_was_and_retires_the_save(self) -> None:
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertIn("rollback.conf is what it was before P6", p.stdout)
        self.assertFalse(self.saved.exists())
        (retired,) = self.saved.parent.glob("p6-before.undone-*")
        self.assertTrue((retired / "complete").exists())

    def test_a_non_empty_export_directory_is_left_with_a_note(self) -> None:
        (self.export_dir / "aorg-cutover-blockers-request.json").write_text("{}")
        p = self.run_script()
        self.assertEqual(p.returncode, 0, text(p))
        self.assertTrue(self.export_dir.is_dir())
        self.assertIn("is not empty", p.stdout)

    def test_each_check_fails_the_run(self) -> None:
        for label, setup in {
            "export_dir still there": lambda: (self.undone(export=True), self.grants(())),
            "ABO left in offline_env": lambda: (
                self.undone(offline=f"HOME={self.state} ABO_CORE_PATHS_STATE_DIR={self.state}"),
                self.grants(()),
            ),
            "ABK and ABO both in offline_env": lambda: (
                self.undone(
                    offline=(
                        f"HOME={self.state} ABK_STATE_DIR={self.state} "
                        f"ABO_CORE_PATHS_STATE_DIR={self.state}"
                    )
                ),
                self.grants(()),
            ),
            "a stub still granted": lambda: (self.undone(), self.grants(STUBS[:1])),
        }.items():
            setup()
            p = self.run_script()
            self.assertNotEqual(p.returncode, 0, f"{label}\n{text(p)}")
            self.assertIn("send this output to the agents", p.stderr, label)
            self.conf.write_text(self.conf.read_text())
            self.export_dir.mkdir(mode=0o700, exist_ok=True)


class ServiceScripts(unittest.TestCase):
    """service-stop.sh and service-start.sh: the state check, the one granted
    command, and the wait."""

    UNIT = "audiobook-organizer.service"

    def setUp(self) -> None:
        self.box = Sandbox(self)
        self.conf = self.box.root / "rollback.conf"
        self.conf.write_text(f"bin = /x\nunit = {self.UNIT}\n")

    def script(self, verb: str) -> Path:
        return script_copy(
            self.box.scripts,
            f"service-{verb}.sh",
            CONF=str(self.conf),
            SYSTEMCTL=str(self.box.shims / "systemctl"),
            WAIT_SEC="2",
            POLL_SEC="0.1",
        )

    def go(self, verb: str, *args: str) -> subprocess.CompletedProcess:
        return self.box.run(self.script(verb), *args)

    def state(self) -> str:
        p = self.box.shims / "state"
        return p.read_text().strip() if p.exists() else "inactive"

    def systemctl_calls(self) -> list[str]:
        if not self.box.log.exists():
            return []
        return [ln for ln in self.box.log.read_text().splitlines() if ln.startswith("systemctl")]

    def test_it_runs_the_one_granted_command_and_waits_for_the_state(self) -> None:
        for verb, before, after in (("stop", "active", "inactive"), ("start", "inactive", "active")):
            self.box.log.unlink(missing_ok=True)
            self.box.set_state(before)
            p = self.go(verb)
            self.assertEqual(p.returncode, 0, text(p))
            self.assertEqual(self.state(), after, verb)
            self.assertEqual(
                self.box.sudo_calls(),
                [f"sudo -n {self.box.shims}/systemctl {verb} {self.UNIT}"],
                "the one command the drop-in grants, nothing else as root",
            )
            self.assertIn(f"is {after}", p.stdout, verb)

    def test_already_there_runs_nothing(self) -> None:
        for verb, now in (("stop", "inactive"), ("start", "active")):
            self.box.set_state(now)
            p = self.go(verb)
            self.assertEqual(p.returncode, 0, text(p))
            self.assertIn("nothing to do", p.stdout, verb)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_dry_run_says_what_would_run_and_runs_nothing(self) -> None:
        for verb, now in (("stop", "active"), ("start", "failed")):
            self.box.set_state(now)
            p = self.go(verb, "--dry-run")
            self.assertEqual(p.returncode, 0, text(p))
            self.assertIn("dry run only", p.stdout)
            self.assertIn(f"{verb} {self.UNIT}", p.stdout)
            self.assertEqual(self.state(), now)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_an_unreadable_state_changes_nothing(self) -> None:
        self.box.set_state(None)
        for verb in ("stop", "start"):
            p = self.go(verb)
            self.assertNotEqual(p.returncode, 0, text(p))
            self.assertIn("cannot read the state", p.stderr)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_a_command_that_fails_is_a_failure(self) -> None:
        (self.box.shims / "change.fail").write_text("")
        for verb, now in (("stop", "active"), ("start", "inactive")):
            self.box.set_state(now)
            p = self.go(verb)
            self.assertNotEqual(p.returncode, 0, text(p))
            self.assertIn(f"systemctl {verb} exited non-zero", p.stderr)

    def test_a_unit_that_never_reaches_the_state_times_out_naming_it(self) -> None:
        (self.box.shims / "change.result").write_text("deactivating")
        self.box.set_state("active")
        p = self.go("stop")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("still deactivating after 2 seconds; wanted inactive", p.stderr)

    def test_a_started_unit_that_fails_ends_the_wait_at_once(self) -> None:
        (self.box.shims / "change.result").write_text("failed")
        self.box.set_state("inactive")
        p = self.go("start")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("is failed; read its journal", p.stderr)
        self.assertNotIn("after 2 seconds", p.stderr)

    def test_a_stopped_unit_that_ends_failed_is_not_stopped(self) -> None:
        (self.box.shims / "change.result").write_text("failed")
        self.box.set_state("active")
        p = self.go("stop")
        self.assertNotEqual(p.returncode, 0, text(p))
        self.assertIn("still failed after 2 seconds; wanted inactive", p.stderr)

    def test_a_bad_config_refuses_before_anything_runs(self) -> None:
        for label, body in (
            ("no unit", "bin = /x\n"),
            ("a space", "unit = a b.service\n"),
            ("a dash first", "unit = -x.service\n"),
            ("not a service", "unit = audiobook-organizer.timer\n"),
            ("a quote", "unit = a'b.service\n"),
        ):
            self.conf.write_text(body)
            for verb in ("stop", "start"):
                p = self.go(verb)
                self.assertNotEqual(p.returncode, 0, f"{label} {verb}\n{text(p)}")
        self.conf.unlink()
        self.assertNotEqual(self.go("stop").returncode, 0)
        self.assertEqual(self.box.sudo_calls(), [])

    def test_arguments(self) -> None:
        for verb in ("stop", "start"):
            for args in (["--go"], ["a"], ["--dry-run", "x"]):
                self.assertEqual(self.go(verb, *args).returncode, 2, f"{verb} {args}")
        self.assertEqual(self.box.sudo_calls(), [])


class Hygiene(unittest.TestCase):
    NAMES = (
        "cutover-blockers-list.sh",
        "cutover-blockers-apply.sh",
        "service-stop.sh",
        "service-start.sh",
        "r2-p6-reinstall.sh",
        "r2-p6-undo.sh",
    )

    def test_every_script_is_executable_has_a_header_and_strict_mode(self) -> None:
        for name in self.NAMES:
            path = OWNER / name
            text_ = path.read_text()
            lines = text_.splitlines()
            self.assertEqual(lines[0], "#!/usr/bin/env bash", name)
            self.assertTrue(lines[1].startswith(f"# file: deploy/owner/{name}"), name)
            self.assertRegex(lines[2], r"^# version: \d+\.\d+\.\d+$", name)
            self.assertRegex(lines[3], r"^# guid: [0-9a-f-]{36}$", name)
            self.assertIn("set -euo pipefail", text_, name)
            self.assertTrue(os.access(path, os.X_OK), name)
            self.assertNotIn("sudo -S", text_, name)
            self.assertIn("--dry-run", text_, name)

    def test_the_scripts_call_only_the_granted_stubs_with_no_arguments(self) -> None:
        for name in ("cutover-blockers-list.sh", "cutover-blockers-apply.sh"):
            text_ = (OWNER / name).read_text()
            calls = re.findall(r'sudo -n "[^"]*"[^\n]*', text_)
            for c in calls:
                self.assertRegex(c, r'^sudo -n "\$LIBDIR/\$STUB"\)? ?(\|\| RC=\$\?)?$', c)
            self.assertEqual(len(calls), 1, name)
        # The service scripts run one thing as root: the granted systemctl line.
        for verb in ("stop", "start"):
            text_ = (OWNER / f"service-{verb}.sh").read_text()
            self.assertEqual(
                re.findall(r"^\s*sudo\b.*$", text_, re.M),
                [f'sudo -n "$SYSTEMCTL" {verb} "$UNIT" || fail "systemctl {verb} exited non-zero; $UNIT is now $(active_state)"'],
                verb,
            )
            self.assertIn("SYSTEMCTL=/usr/bin/systemctl", text_)

    @unittest.skipUnless(shutil.which("shellcheck"), "shellcheck is not installed")
    def test_shellcheck_is_clean(self) -> None:
        for name in self.NAMES:
            p = subprocess.run(
                ["shellcheck", str(OWNER / name)], capture_output=True, text=True, check=False
            )
            self.assertEqual(p.returncode, 0, f"{name}\n{p.stdout}")


if __name__ == "__main__":
    unittest.main()
