# file: scripts/tests/test_cutover_blockers_helper.py
# version: 0.1.0
# guid: 3f7b0c4e-8a52-4d1e-9c60-5b2e7a41d9f3
# last-edited: 2026-11-22

"""Tests for the cutover-blockers ops of deploy/rollback-helpers/rollback_helper.py
(F8-04 Path B): cutover-blockers-list and cutover-blockers-apply.

Each takes no arguments. They refuse unless the service is stopped, run
`storage cutover-blockers` as the service user in the export unit (a fake
systemd-run here, running a copy of the helper as the worker, with a fake
binary), and deliver into export_dir as the deploy user's 0600 files. The apply
op reads one fixed-name request file, strictly, and hands it to the worker on
stdin. Fixtures use Batman names only.

Run with:
    python3 -m unittest scripts.tests.test_cutover_blockers_helper -v
"""

from __future__ import annotations

import base64
import contextlib
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_rollback_helper import HELPER, HELPERS, REPO_ROOT, Host  # noqa: E402

STAMP = "20261122T010203Z"
THIS_CALL = "bat-call-0001"
REQUEST = "aorg-cutover-blockers-request.json"
ROBIN = b"robin".hex()
BATGIRL = b"batgirl".hex()
SECRET = b"alfred-pennyworth-credential-hash"
SECRET_B64 = base64.b64encode(SECRET).decode()
LIST_TEXT = (
    f"users step=books reason=bad-hash keyhex={ROBIN}\n"
    f"users step=books reason=bad-hash keyhex={BATGIRL}\n"
    "2 blocking row(s). Fix or delete each with its keyhex (hex:<keyhex>), then run the upgrade "
    "again.\n"
)
ROWS = json.dumps(
    [{"key": ROBIN, "value": SECRET_B64}, {"key": BATGIRL, "value": SECRET_B64}], indent=2
).encode()
NONE_TEXT = "No blocking rows: the cutover preflight is clean.\n"

# The fake systemctl: ActiveState per unit from a file named for the unit in
# STATE_DIR (absent: inactive; `<unit>.fail` present: the query fails, an
# unreadable state), and the export unit's Environment from a file.
_SYSTEMCTL_BODY = """
a = sys.argv[1:]
d = @@SDIR@@
if "ActiveState" in a:
    unit = a[-1]
    if os.path.exists(os.path.join(d, unit + ".fail")):
        sys.exit(1)
    try:
        print(open(os.path.join(d, unit)).read().strip())
    except FileNotFoundError:
        print("inactive")
if "Environment" in a:
    try:
        print(open(os.path.join(d, "export-env")).read().strip())
    except FileNotFoundError:
        print("")
sys.exit(0)
"""

# The fake systemd-run: runs the patched helper copy (the worker) with the
# --setenv variables and stdin as the helper gave it, and re-emits its stdout;
# the mode file picks a fault. It records what kind of file its stdin was.
_SYSTEMD_RUN_BODY = """
import stat as _stat, subprocess, time
args = sys.argv[1:]
cut = args.index("--")
opts, cmd = args[:cut], args[cut + 1:]
env = dict(os.environ)
for o in opts:
    if o.startswith("--setenv="):
        k, _, v = o[len("--setenv="):].partition("=")
        env[k] = v
cmd = [@@PATCHED@@ if c.endswith("rollback_helper.py") else c for c in cmd]
mode = open(@@MODE@@).read().strip() if os.path.exists(@@MODE@@) else "normal"
envfile = @@ENVFILE@@
mm = os.fstat(0).st_mode
with open(@@STDIN_KIND@@, "w") as f:
    f.write("fifo" if _stat.S_ISFIFO(mm) else "chr" if _stat.S_ISCHR(mm) else "other")
open(envfile, "w").write(" ".join(o[len("--setenv="):] for o in opts if o.startswith("--setenv=")))
stdin_data = sys.stdin.buffer.read()
with open(@@STDIN_DATA@@, "wb") as f:
    f.write(stdin_data)
if mode == "fail":
    os.remove(envfile)
    sys.exit(7)
if mode == "hang":
    time.sleep(5)
if mode == "raw":
    sys.stdout.buffer.write(open(@@RAW@@, "rb").read())
    sys.stdout.buffer.flush()
    os.remove(envfile)
    sys.exit(0)
p = subprocess.run(cmd, env=env, stdout=subprocess.PIPE, input=stdin_data)
sys.stdout.buffer.write(p.stdout)
sys.stdout.buffer.flush()
os.remove(envfile)
sys.exit(p.returncode)
"""

# The fake binary: `storage cutover-blockers list|fix|delete`. It logs its argv,
# its cwd and the content of a --from file, then acts per the mode file.
_BIN_TEMPLATE = """#!@@PYTHON@@
import json, os, sys
args = sys.argv[1:]
mf = @@MODE@@
mode = open(mf).read().strip() if os.path.exists(mf) else "normal"
rec = {"argv": args, "cwd": os.getcwd(),
       "env": {k: os.environ.get(k, "") for k in ("HOME", "ABK_STATE_DIR", "ABO_CORE_PATHS_STATE_DIR")}}
if "--from" in args:
    p = args[args.index("--from") + 1]
    rec["from_path"] = p
    rec["from_text"] = open(p).read()
    rec["from_mode"] = stat_mode = oct(os.stat(p).st_mode & 0o777)
with open(@@LOG@@, "a") as f:
    f.write(json.dumps(rec) + "\\n")
if args[:2] != ["storage", "cutover-blockers"]:
    sys.exit(9)
if mode == "fail":
    print("storage cutover-blockers: the cutover has started")
    sys.stderr.write("Error: the cutover has started\\n")
    sys.exit(3)
verb = args[2]
if verb == "list":
    out = args[args.index("--out") + 1]
    if mode == "none":
        sys.stdout.write(@@NONE@@)
        sys.exit(0)
    sys.stdout.write(@@LIST@@)
    if mode == "quiet-none":
        sys.exit(0)
    fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.write(fd, @@ROWS@@)
    os.close(fd)
    if mode == "rows-open":
        os.chmod(out, 0o644)
    if mode == "rows-empty":
        os.truncate(out, 0)
    print("Wrote 2 row(s) with their values to " + out + " (mode 0600).")
elif verb == "fix":
    print("Fixed " + args[3] + " (step books, record 7). Run storage cutover-blockers list.")
elif verb == "delete":
    print("Deleted " + args[3] + " (step books, record 8; 2 id(s): a, b).")
if mode == "swap-request":
    req = @@REQUEST@@
    os.unlink(req)
    fd = os.open(req, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.write(fd, b"{}")
    os.close(fd)
if mode == "symlink-request":
    req = @@REQUEST@@
    os.rename(req, req + ".orig")
    os.symlink(req + ".orig", req)
if mode == "plant-result":
    fd = os.open(@@PLANT@@, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
"""


def frame(part: str, data: bytes) -> bytes:
    return f"AORGBLOCKERS1 {part} {len(data)} {hashlib.sha256(data).hexdigest()}\n".encode() + data


def render(template: str, **kw: object) -> str:
    for k, v in kw.items():
        template = template.replace(
            f"@@{k.upper()}@@", v if isinstance(v, str) and k == "python" else repr(v)
        )
    return template


class Blockers(unittest.TestCase):
    """The service-user cutover-blockers command, behind two no-argument ops."""

    def setUp(self):
        self.h = h = Host(self)
        self.export = h.root / "export"
        self.export.mkdir()
        self.export.chmod(0o700)
        with open(h.mod.CONFIG, "a") as f:
            f.write(f"export_dir = {self.export}\n")
        self.tmp = h.root / "tmp"
        self.tmp.mkdir()
        self.sdir = h.root / "unit-state"
        self.sdir.mkdir()
        self.bin_log = h.root / "bin-calls.log"
        self.bin_mode = h.root / "bin-mode"
        self.run_mode = h.root / "fake-run-mode"
        self.raw_file = h.root / "fake-run-raw"
        self.stdin_kind = h.root / "stdin-kind"
        self.stdin_data = h.root / "stdin-data"
        self.request = self.export / REQUEST
        h.bin.write_text(
            render(
                _BIN_TEMPLATE,
                python=sys.executable,
                mode=str(self.bin_mode),
                log=str(self.bin_log),
                none=NONE_TEXT,
                list=LIST_TEXT,
                rows=ROWS,
                request=str(self.request),
                plant=str(self.export / f"aorg-cutover-blockers-result-{STAMP}.txt"),
            )
        )
        h.bin.chmod(0o755)
        h.mod.TOOLS["systemctl"] = h.tool("systemctl", render(_SYSTEMCTL_BODY, sdir=str(self.sdir)))
        src = HELPER.read_text()
        before = 'CONFIG = "/etc/audiobook-organizer/rollback.conf"'
        assert src.count(before) == 1
        self.patched = h.root / "patched" / "rollback_helper.py"
        self.patched.parent.mkdir()
        self.patched.write_text(src.replace(before, f"CONFIG = {h.mod.CONFIG!r}"))
        h.mod.TOOLS["systemd-run"] = h.tool(
            "systemd-run",
            render(
                _SYSTEMD_RUN_BODY,
                patched=str(self.patched),
                mode=str(self.run_mode),
                envfile=str(self.sdir / "export-env"),
                stdin_kind=str(self.stdin_kind),
                stdin_data=str(self.stdin_data),
                raw=str(self.raw_file),
            ),
        )
        h.mod._export_stamp = lambda: STAMP
        h.mod._export_token = lambda: THIS_CALL
        for patch in (
            unittest.mock.patch.dict(os.environ, {"TMPDIR": str(self.tmp)}),
            unittest.mock.patch.object(tempfile, "tempdir", str(self.tmp)),
        ):
            patch.start()
            self.addCleanup(patch.stop)
        os.environ.pop("SUDO_UID", None)

    # -- helpers --------------------------------------------------------------

    def set_state(self, unit: str, state: str) -> None:
        (self.sdir / unit).write_text(state + "\n")

    def put_request(self, doc, mode: int = 0o600) -> Path:
        data = doc if isinstance(doc, bytes) else json.dumps(doc).encode()
        self.request.unlink(missing_ok=True)
        self.request.write_bytes(data)
        self.request.chmod(mode)
        return self.request

    def fix_request(self, key: str = ROBIN, value: str = SECRET_B64) -> Path:
        return self.put_request({"action": "fix", "key": key, "value": value})

    def mode(self, run: str = "", bin_: str = "") -> None:
        for path, text in ((self.run_mode, run), (self.bin_mode, bin_)):
            if text:
                path.write_text(text)

    def bin_calls(self) -> list[dict]:
        if not self.bin_log.exists():
            return []
        return [json.loads(ln) for ln in self.bin_log.read_text().splitlines()]

    def systemd_runs(self) -> list[list[str]]:
        return [t["argv"] for t in self.h.tools() if t["tool"] == "systemd-run"]

    def exported(self) -> list[str]:
        return sorted(p.name for p in self.export.iterdir())

    def lines(self, out: str) -> list[list[str]]:
        return [ln.split(" ") for ln in out.splitlines()]

    def ok(self, *argv: str) -> tuple[str, str]:
        code, out, err = self.h.run(*argv)
        self.assertEqual(code, 0, err)
        return out, err

    def refused(self, *argv: str, code: int = 1, contains: str = "") -> str:
        before = self.exported()
        got, out, err = self.h.run(*argv)
        self.assertEqual(got, code, err)
        self.assertEqual(out, "", "a refusal prints no EXPORTED line")
        self.assertIn(contains, err)
        self.assertEqual(self.exported(), before, "a refusal leaves export_dir as it was")
        return err

    def worker(self, op: str, stdin: bytes = b""):
        """The worker op run directly (as the unit would run it)."""
        return subprocess.run(
            [sys.executable, "-I", str(self.patched), "_blockers-worker", op],
            input=stdin,
            capture_output=True,
            check=False,
        )

    def assert_clean_tmp(self):
        self.assertEqual(list(self.tmp.iterdir()), [], "the scratch directory is removed")

    # -- cutover-blockers-list ------------------------------------------------

    def test_list_delivers_both_files_and_names_them(self):
        out, _ = self.ok("cutover-blockers-list")
        txt, js = f"aorg-cutover-blockers-{STAMP}.txt", f"aorg-cutover-blockers-{STAMP}.json"
        self.assertEqual(self.exported(), sorted([txt, js]))
        text = (self.export / txt).read_bytes()
        self.assertTrue(text.startswith(LIST_TEXT.encode()), "what the command printed")
        self.assertIn(b"Wrote 2 row(s) with their values to ", text)
        self.assertEqual((self.export / js).read_bytes(), ROWS)
        self.assertEqual(
            self.lines(out),
            [
                ["EXPORTED", txt, str(len(text)), hashlib.sha256(text).hexdigest()],
                ["EXPORTED", js, str(len(ROWS)), hashlib.sha256(ROWS).hexdigest()],
            ],
        )
        for n in (txt, js):
            self.assertEqual(stat.S_IMODE((self.export / n).stat().st_mode), 0o600, n)
            self.assertEqual((self.export / n).stat().st_uid, os.getuid(), n)
        self.assert_clean_tmp()

    def test_list_runs_the_command_as_the_service_user_in_the_export_unit(self):
        h = self.h
        self.ok("cutover-blockers-list")
        runs = self.systemd_runs()
        self.assertEqual(len(runs), 1)
        argv = runs[0]
        cut = argv.index("--")
        self.assertEqual(
            argv[:cut],
            [
                "--pipe",
                "--wait",
                "--quiet",
                "--collect",
                "--unit=audiobook-organizer-rollback-export",
                "--service-type=exec",
                "--uid=alfred",
                "--property=PrivateTmp=yes",
                "--property=RuntimeMaxSec=600",
                f"--setenv=HOME={h.state}",
                f"--setenv=ABO_CORE_PATHS_STATE_DIR={h.state}",
                f"--setenv=AORG_EXPORT_TOKEN={THIS_CALL}",
            ],
        )
        self.assertEqual(
            argv[cut + 1 :], [sys.executable, "-I", str(HELPER), "_blockers-worker", "list"]
        )
        self.assertFalse(any("StandardOutput" in a or "restore.log" in a for a in argv))
        self.assertEqual(self.stdin_kind.read_text(), "chr", "a list unit's stdin is /dev/null")
        (call,) = self.bin_calls()
        a = call["argv"]
        self.assertEqual(a[:4], ["storage", "cutover-blockers", "list", "--db"])
        self.assertEqual(a[4], str(h.db))
        self.assertEqual(a[5], "--out")
        self.assertRegex(a[6], re.escape(str(self.tmp)) + r"/aorg-blockers-[^/]+/rows\.json$")
        self.assertEqual(len(a), 7, "list takes only --db and --out")
        self.assertEqual(call["env"]["ABO_CORE_PATHS_STATE_DIR"], str(h.state))
        self.assertEqual(call["env"]["ABK_STATE_DIR"], "")
        self.assertFalse(h.restore_log().exists())

    def test_list_with_nothing_blocking_delivers_the_text_and_reports_the_rows_absent(self):
        self.mode(bin_="none")
        out, _ = self.ok("cutover-blockers-list")
        self.assertEqual(self.exported(), [f"aorg-cutover-blockers-{STAMP}.txt"])
        self.assertEqual(out.splitlines()[-1], "ABSENT rows")
        self.assertIn(b"No blocking rows", (self.export / self.exported()[0]).read_bytes())

    def test_list_that_printed_blockers_but_wrote_no_rows_refuses(self):
        self.mode(bin_="quiet-none")
        self.refused("cutover-blockers-list", contains="exited 1. Nothing was delivered")
        self.assert_clean_tmp()
        p = self.worker("list")
        self.assertEqual(p.returncode, 1)
        self.assertIn(b"did not say there are no blocking rows", p.stderr)
        self.assertEqual(p.stdout, b"", "no frame is written for a list that cannot be trusted")

    def test_list_refuses_a_rows_file_others_can_read(self):
        self.mode(bin_="rows-open")
        self.refused("cutover-blockers-list", contains="exited 1")
        self.assertIn(b"readable by group or others", self.worker("list").stderr)

    def test_list_refuses_an_empty_rows_file(self):
        self.mode(bin_="rows-empty")
        self.refused("cutover-blockers-list", contains="exited 1")
        self.assertIn(b"non-empty regular file", self.worker("list").stderr)

    def test_list_takes_no_arguments(self):
        for extra in (["x"], ["--out", "/etc/shadow"], [""], ["hex:00"]):
            self.refused("cutover-blockers-list", *extra, code=2, contains="usage")
        self.assertEqual(self.systemd_runs(), [])
        self.assertEqual(self.bin_calls(), [])

    def test_list_command_failure_delivers_nothing_and_forwards_its_output(self):
        self.mode(bin_="fail")
        self.refused("cutover-blockers-list", contains="exited 1. Nothing was delivered")
        self.assert_clean_tmp()
        p = self.worker("list")
        self.assertEqual(p.returncode, 1)
        self.assertEqual(p.stdout, b"", "no frame for a failed command")
        self.assertIn(b"the cutover has started", p.stderr, "the command's output is forwarded")
        self.assertIn(b"`storage cutover-blockers list` exited 3", p.stderr)

    # -- the service must be stopped, and no other unit running ----------------

    def test_service_not_inactive_refuses_before_anything_runs(self):
        for state in ("active", "activating", "deactivating", "reloading", "failed"):
            for op in ("cutover-blockers-list", "cutover-blockers-apply"):
                self.set_state("audiobook-organizer.service", state)
                self.fix_request()
                self.refused(op, contains=f"audiobook-organizer.service is {state}, not inactive")
                self.assertEqual(self.systemd_runs(), [], (state, op))
                self.assertTrue(self.request.exists(), "the request is untouched")
                self.request.unlink()

    def test_service_state_that_cannot_be_read_refuses(self):
        (self.sdir / "audiobook-organizer.service.fail").write_text("")
        for op in ("cutover-blockers-list", "cutover-blockers-apply"):
            self.refused(op, contains="in a state that cannot be read")
        self.assertEqual(self.systemd_runs(), [])

    def test_the_helper_never_stops_or_starts_the_service(self):
        self.set_state("audiobook-organizer.service", "active")
        self.h.run("cutover-blockers-list")
        self.set_state("audiobook-organizer.service", "inactive")
        self.ok("cutover-blockers-list")
        self.fix_request()
        self.ok("cutover-blockers-apply")
        verbs = [t["argv"][0] for t in self.h.tools() if t["tool"] == "systemctl"]
        self.assertEqual(set(verbs), {"show"}, "only reads state: no stop, start or restart")

    def test_restore_or_export_unit_running_refuses(self):
        for unit in (
            "audiobook-organizer-rollback-restore.service",
            "audiobook-organizer-rollback-export.service",
        ):
            for state in ("active", "activating", "deactivating"):
                self.set_state(unit, state)
                self.fix_request()
                for op in ("cutover-blockers-list", "cutover-blockers-apply"):
                    self.refused(op, contains=f"{unit} is {state}")
                self.assertEqual(self.systemd_runs(), [], (unit, state))
                self.assertTrue(self.request.exists())
            (self.sdir / unit).unlink()
            self.request.unlink()

    def test_a_failed_export_unit_is_reset_and_does_not_block(self):
        self.set_state("audiobook-organizer-rollback-export.service", "failed")
        self.ok("cutover-blockers-list")
        resets = [
            t["argv"]
            for t in self.h.tools()
            if t["tool"] == "systemctl" and t["argv"][0] == "reset-failed"
        ]
        self.assertEqual(resets, [["reset-failed", "audiobook-organizer-rollback-export"]])

    # -- export_dir -----------------------------------------------------------

    def test_export_dir_problems_refuse_before_anything_runs(self):
        h = self.h
        for op in ("cutover-blockers-list", "cutover-blockers-apply"):
            self.export.chmod(0o755)
            self.refused(op, contains="has mode 0755, not 0700")
            self.export.chmod(0o700)
            real = h.root / "real-export"
            os.rename(self.export, real)
            os.symlink(real, self.export)
            self.refused(op, contains="cannot open the export directory")
            os.unlink(self.export)
            os.rename(real, self.export)
            with unittest.mock.patch.object(h.mod, "_deploy_uid", return_value=os.getuid() + 1):
                self.refused(op, contains="not by the deploy user")
        text = Path(h.mod.CONFIG).read_text().replace(f"export_dir = {self.export}\n", "")
        Path(h.mod.CONFIG).write_text(text)
        for op in ("cutover-blockers-list", "cutover-blockers-apply"):
            self.refused(op, contains="export_dir is not set")
        self.assertEqual(self.systemd_runs(), [])

    def test_the_refusals_say_nothing_was_run_not_nothing_was_exported(self):
        self.export.chmod(0o755)
        err = self.refused("cutover-blockers-list", contains="nothing was run")
        self.assertNotIn("exported", err)

    # -- delivery -------------------------------------------------------------

    def test_delivered_files_are_the_deploy_users_by_fchown_before_any_byte(self):
        events: list[tuple[str, int, tuple]] = []
        real_write = os.write

        def write(fd, data):
            events.append(("write", fd, ()))
            return real_write(fd, data)

        env = {"SUDO_UID": str(os.getuid()), "SUDO_GID": "4343"}
        with contextlib.ExitStack() as stack:
            stack.enter_context(unittest.mock.patch.dict(os.environ, env))
            stack.enter_context(
                unittest.mock.patch.object(
                    os, "fchown", lambda fd, u, g: events.append(("fchown", fd, (u, g)))
                )
            )
            stack.enter_context(unittest.mock.patch.object(os, "write", write))
            self.ok("cutover-blockers-list")
        owned = [(fd, ids) for kind, fd, ids in events if kind == "fchown"]
        self.assertEqual([ids for _, ids in owned], [(os.getuid(), 4343)] * 2)
        for fd, _ in owned:
            seq = [kind for kind, f, _ in events if f == fd]
            self.assertEqual(seq[0], "fchown", "owned by SUDO_UID before the first byte lands")
            self.assertIn("write", seq)

    def test_delivered_files_are_0600_whatever_the_umask(self):
        old = os.umask(0)
        self.addCleanup(os.umask, old)
        self.ok("cutover-blockers-list")
        for n in self.exported():
            self.assertEqual(stat.S_IMODE((self.export / n).stat().st_mode), 0o600, n)

    def test_list_is_all_or_nothing_a_planted_second_name_removes_the_first(self):
        planted = self.export / f"aorg-cutover-blockers-{STAMP}.json"
        planted.write_bytes(b"batcave")
        self.refused("cutover-blockers-list", contains="cannot create")
        self.assertEqual(self.exported(), [planted.name], "the .txt written first is removed again")
        self.assertEqual(planted.read_bytes(), b"batcave", "a planted name is never overwritten")

    def test_a_planted_symlink_is_not_followed(self):
        target = self.h.root / "wayne-manor-secrets"
        target.write_bytes(b"bruce")
        os.symlink(target, self.export / f"aorg-cutover-blockers-{STAMP}.txt")
        self.refused("cutover-blockers-list", contains="cannot create")
        self.assertEqual(target.read_bytes(), b"bruce")
        self.assertEqual(self.exported(), [f"aorg-cutover-blockers-{STAMP}.txt"])

    # -- the unit's output is frames, and only frames ---------------------------

    def raw(self, data: bytes) -> None:
        self.raw_file.write_bytes(data)
        self.mode(run="raw")

    def test_frame_faults_each_refuse_with_their_own_message(self):
        good = frame("list", LIST_TEXT.encode()) + frame("rows", ROWS)
        flipped = good[:-1] + bytes([good[-1] ^ 1])
        cases = {
            "nothing at all": (b"", "framed nothing"),
            "text before the header": (b"warning: hello\n" + good, "did not put a frame header"),
            "garbage after the frames": (good + b"x", "did not put a frame header"),
            "truncated": (good[:-3], "followed (cut short)"),
            "bytes flipped": (flipped, "sha256 is not the one it stated"),
            "empty list part": (frame("list", b""), "framed an empty list part"),
            "empty rows part": (frame("list", b"x") + frame("rows", b""), "empty rows part"),
            "list twice": (frame("list", b"a") + frame("list", b"b"), "framed list twice"),
            "rows alone": (frame("rows", ROWS), "framed rows; it must frame"),
            "rows before list": (frame("rows", ROWS) + frame("list", b"a"), "framed rows, list"),
            "a result part in a list": (frame("result", b"a"), "framed result; it must frame"),
            "unknown part": (b"AORGBLOCKERS1 admin 1 " + b"0" * 64 + b"\nx", "did not put a frame"),
            "export magic": (
                b"AORGEXPORT1 1 " + hashlib.sha256(b"x").hexdigest().encode() + b"\nx",
                "did not put a frame",
            ),
        }
        for name, (data, contains) in cases.items():
            self.raw(data)
            err = self.refused("cutover-blockers-list", contains=contains)
            self.assertIn("Nothing was delivered", err, name)

    def test_a_part_over_the_limit_refuses(self):
        self.h.mod.BLOCKERS_PART_LIMIT = 50
        self.raw(frame("list", b"A" * 51))
        self.refused(
            "cutover-blockers-list", contains="framed 51 bytes for list, over the 50 limit"
        )

    def test_output_over_the_read_cap_refuses(self):
        self.h.mod.BLOCKERS_READ_CAP = 100
        self.raw(b"A" * 200)
        self.refused("cutover-blockers-list", contains="wrote more than 100 bytes")

    def test_unit_exit_status_refuses_and_delivers_nothing(self):
        self.mode(run="fail")
        self.refused("cutover-blockers-list", contains="exited 7. Nothing was delivered")

    def test_a_hung_unit_is_stopped_after_the_deadline(self):
        self.h.mod.BLOCKERS_DEADLINE_SEC = 1
        self.mode(run="hang")
        self.refused("cutover-blockers-list", contains="did not finish within 1 seconds")
        stops = [
            t["argv"] for t in self.h.tools() if t["tool"] == "systemctl" and t["argv"][0] == "stop"
        ]
        self.assertEqual(stops, [["stop", "audiobook-organizer-rollback-export"]])

    # -- cutover-blockers-apply: the request file ---------------------------------

    def test_apply_takes_no_arguments(self):
        self.fix_request()
        for extra in (["x"], [str(self.request)], ["--confirm"], ["fix"]):
            self.refused("cutover-blockers-apply", *extra, code=2, contains="usage")
        self.assertEqual(self.systemd_runs(), [])
        self.assertTrue(self.request.exists())

    def test_a_missing_request_refuses(self):
        self.refused("cutover-blockers-apply", contains="cannot open the request")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_symlinked_request_refuses(self):
        real = self.h.root / "elsewhere.json"
        real.write_text(json.dumps({"action": "delete", "key": ROBIN}))
        real.chmod(0o600)
        os.symlink(real, self.request)
        self.refused("cutover-blockers-apply", contains="cannot open the request")
        self.assertEqual(self.systemd_runs(), [])
        self.assertTrue(real.exists())

    def test_a_hard_linked_request_refuses(self):
        self.put_request({"action": "delete", "key": ROBIN})
        os.link(self.request, self.h.root / "second-name")
        self.refused("cutover-blockers-apply", contains="has 2 hard links")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_request_owned_by_someone_else_refuses(self):
        self.put_request({"action": "delete", "key": ROBIN})
        uid = os.getuid()
        with unittest.mock.patch.object(self.h.mod, "_deploy_uid", side_effect=[uid, uid + 1]):
            self.refused("cutover-blockers-apply", contains="not by the deploy user")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_request_with_any_mode_but_0600_refuses(self):
        for mode in (0o640, 0o644, 0o666, 0o400, 0o700, 0o620):
            self.put_request({"action": "delete", "key": ROBIN}, mode=mode)
            self.refused("cutover-blockers-apply", contains=f"has mode {mode:04o}, not 0600")
        self.assertEqual(self.systemd_runs(), [])

    def test_an_empty_or_oversized_request_refuses(self):
        self.put_request(b"")
        self.refused("cutover-blockers-apply", contains="is 0 bytes")
        self.h.mod.BLOCKERS_PART_LIMIT = 64
        self.put_request(b" " * 65)
        self.refused("cutover-blockers-apply", contains="is 65 bytes")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_request_that_is_not_a_regular_file_refuses(self):
        os.mkfifo(self.request, 0o600)
        self.refused("cutover-blockers-apply", contains="is not a regular file")
        self.request.unlink()
        self.request.mkdir(mode=0o700)
        self.refused("cutover-blockers-apply", contains="is not a regular file")
        self.assertEqual(self.systemd_runs(), [])

    def test_a_request_is_read_by_name_inside_the_checked_directory(self):
        calls: list[tuple[str, object]] = []
        real_open = os.open

        def spy(path, flags, mode=0o777, *, dir_fd=None):
            if "request" in str(path):
                calls.append((str(path), dir_fd))
            return real_open(path, flags, mode, dir_fd=dir_fd)

        self.put_request({"action": "delete", "key": ROBIN})
        with unittest.mock.patch.object(os, "open", spy):
            self.ok("cutover-blockers-apply")
        self.assertEqual(len(calls), 1)
        name, dir_fd = calls[0]
        self.assertEqual(name, REQUEST, "a bare name, never a path")
        self.assertIsNotNone(dir_fd, "relative to the export directory's fd")

    # -- cutover-blockers-apply: the request's content ----------------------------

    def test_a_bad_request_refuses_names_no_value_and_keeps_the_file(self):
        v = SECRET_B64
        cases = {
            "not json": (b"batman", "strict JSON"),
            "not utf-8": (b'{"action":"delete","key":"\xff"}', "strict JSON"),
            "a list": (b"[]", "not a JSON object"),
            "a string": (b'"fix"', "not a JSON object"),
            "unknown field": (
                {"action": "delete", "key": ROBIN, "confirm": True},
                "unknown field(s) 'confirm'",
            ),
            "two actions as fields": (
                {"action": "fix", "delete": ROBIN, "key": ROBIN, "value": v},
                "unknown field(s) 'delete'",
            ),
            "duplicate action": (
                b'{"action":"fix","action":"delete","key":"' + ROBIN.encode() + b'"}',
                "appears twice",
            ),
            "duplicate key": (
                b'{"action":"delete","key":"aa","key":"' + ROBIN.encode() + b'"}',
                "appears twice",
            ),
            "action list": ({"action": ["fix", "delete"], "key": ROBIN}, "exactly"),
            "action upper": ({"action": "FIX", "key": ROBIN, "value": v}, "exactly"),
            "action unknown": ({"action": "restore", "key": ROBIN}, "exactly"),
            "action missing": ({"key": ROBIN}, "exactly"),
            "action null": ({"action": None, "key": ROBIN}, "exactly"),
            "key missing": ({"action": "delete"}, "lowercase hex"),
            "key upper": ({"action": "delete", "key": ROBIN.upper()}, "lowercase hex"),
            "key hex prefix": ({"action": "delete", "key": "hex:" + ROBIN}, "lowercase hex"),
            "key raw bytes": ({"action": "delete", "key": "robin"}, "lowercase hex"),
            "key odd length": ({"action": "delete", "key": ROBIN + "a"}, "lowercase hex"),
            "key empty": ({"action": "delete", "key": ""}, "lowercase hex"),
            "key too long": ({"action": "delete", "key": "ab" * 1025}, "lowercase hex"),
            "key not text": ({"action": "delete", "key": 7261}, "lowercase hex"),
            "key list": ({"action": "delete", "key": [ROBIN]}, "lowercase hex"),
            "key spaced": ({"action": "delete", "key": ROBIN + " "}, "lowercase hex"),
            "key newline": ({"action": "delete", "key": ROBIN + "\n"}, "lowercase hex"),
            "fix without value": ({"action": "fix", "key": ROBIN}, "needs its value"),
            "fix empty value": ({"action": "fix", "key": ROBIN, "value": ""}, "needs its value"),
            "fix null value": ({"action": "fix", "key": ROBIN, "value": None}, "needs its value"),
            "fix number value": ({"action": "fix", "key": ROBIN, "value": 5}, "needs its value"),
            "value not base64": (
                {"action": "fix", "key": ROBIN, "value": "!!!!"},
                "not valid base64",
            ),
            "value unpadded": (
                {"action": "fix", "key": ROBIN, "value": "QUJDRA"},
                "not valid base64",
            ),
            "value spaced": (
                {"action": "fix", "key": ROBIN, "value": "QUJD RA=="},
                "not valid base64",
            ),
            "value newline": (
                {"action": "fix", "key": ROBIN, "value": "QUJD\nRA=="},
                "not valid base64",
            ),
            "value url-safe": (
                {"action": "fix", "key": ROBIN, "value": "_-8="},
                "not valid base64",
            ),
            "delete with value": ({"action": "delete", "key": ROBIN, "value": v}, "takes no value"),
            "delete with null value": (
                {"action": "delete", "key": ROBIN, "value": None},
                "takes no value",
            ),
        }
        for name, (doc, contains) in cases.items():
            self.put_request(doc)
            err = self.refused("cutover-blockers-apply", contains=contains)
            self.assertNotIn(SECRET_B64, err, name)
            self.assertNotIn(SECRET.decode(), err, name)
            self.assertTrue(self.request.exists(), name)
            self.assertEqual(self.systemd_runs(), [], f"{name}: nothing ran")
            self.assertEqual(self.bin_calls(), [], name)

    def test_an_oversized_fix_value_refuses(self):
        self.h.mod.BLOCKERS_PART_LIMIT = 8192 + 8
        self.put_request({"action": "fix", "key": ROBIN, "value": "QUJD" * 4})
        self.refused("cutover-blockers-apply", contains="over 8 base64 characters")

    # -- cutover-blockers-apply: what it does ---------------------------------

    def test_apply_fix_runs_the_command_with_a_rows_file_and_no_confirm(self):
        self.fix_request()
        out, err = self.ok("cutover-blockers-apply")
        (call,) = self.bin_calls()
        a = call["argv"]
        self.assertEqual(a[:5], ["storage", "cutover-blockers", "fix", f"hex:{ROBIN}", "--db"])
        self.assertEqual(a[5], str(self.h.db))
        self.assertEqual(a[6], "--from")
        self.assertRegex(
            a[7], re.escape(str(self.tmp)) + r"/aorg-blockers-[^/]+/request-rows\.json$"
        )
        self.assertEqual(len(a), 8)
        self.assertNotIn("--confirm", a, "--confirm is for delete only")
        self.assertEqual(json.loads(call["from_text"]), [{"key": ROBIN, "value": SECRET_B64}])
        self.assertEqual(call["from_mode"], "0o600")
        self.assertNotIn(str(self.export), a[7], "the rows file is in the unit's PrivateTmp")
        self.assertFalse(os.path.exists(a[7]) and True, "the rows file is removed again")
        self.assert_clean_tmp()
        self.assertEqual(self.stdin_kind.read_text(), "fifo", "the request travels on stdin")

    def test_apply_delete_adds_confirm_itself_and_writes_no_rows_file(self):
        self.put_request({"action": "delete", "key": BATGIRL})
        self.ok("cutover-blockers-apply")
        (call,) = self.bin_calls()
        self.assertEqual(
            call["argv"],
            [
                "storage",
                "cutover-blockers",
                "delete",
                f"hex:{BATGIRL}",
                "--db",
                str(self.h.db),
                "--confirm",
            ],
        )
        self.assertNotIn("from_text", call)
        self.assertNotIn("--from", call["argv"])

    def test_apply_runs_as_the_service_user_in_the_export_unit_with_a_longer_runtime(self):
        self.fix_request()
        self.ok("cutover-blockers-apply")
        (argv,) = self.systemd_runs()
        cut = argv.index("--")
        self.assertIn("--uid=alfred", argv[:cut])
        self.assertIn("--unit=audiobook-organizer-rollback-export", argv[:cut])
        self.assertIn("--property=RuntimeMaxSec=600", argv[:cut])
        self.assertEqual(
            argv[cut + 1 :], [sys.executable, "-I", str(HELPER), "_blockers-worker", "apply"]
        )

    def test_the_request_never_reaches_a_command_line_but_the_workers_stdin(self):
        self.fix_request()
        self.ok("cutover-blockers-apply")
        for t in self.h.tools():
            joined = " ".join(t["argv"])
            self.assertNotIn(SECRET_B64, joined, t["tool"])
            self.assertNotIn(ROBIN, joined, t["tool"])
            self.assertNotIn(REQUEST, joined, t["tool"])
            self.assertNotIn(str(self.export), joined, t["tool"])

    def test_apply_delivers_the_result_and_removes_the_request(self):
        self.fix_request()
        out, _ = self.ok("cutover-blockers-apply")
        result = f"aorg-cutover-blockers-result-{STAMP}.txt"
        self.assertEqual(self.exported(), [result], "the request is gone, the result is there")
        text = (self.export / result).read_bytes()
        self.assertIn(b"Fixed hex:" + ROBIN.encode(), text)
        self.assertEqual(
            self.lines(out),
            [
                ["EXPORTED", result, str(len(text)), hashlib.sha256(text).hexdigest()],
                ["REQUEST-REMOVED", REQUEST],
            ],
        )
        self.assertEqual(stat.S_IMODE((self.export / result).stat().st_mode), 0o600)
        self.assertEqual((self.export / result).stat().st_uid, os.getuid())
        self.assertNotIn(SECRET.decode(), text.decode())
        self.assertNotIn(SECRET_B64, text.decode())
        self.assertNotIn(SECRET_B64, out)

    def test_the_value_is_re_encoded_canonically(self):
        # "QR==" decodes to b"A" with non-zero trailing bits; the command is given "QQ==".
        self.put_request({"action": "fix", "key": ROBIN, "value": "QR=="})
        self.ok("cutover-blockers-apply")
        (call,) = self.bin_calls()
        self.assertEqual(json.loads(call["from_text"])[0]["value"], "QQ==")

    def test_the_request_stays_when_the_command_fails_and_the_message_says_to_look(self):
        self.fix_request()
        self.mode(bin_="fail")
        err = self.refused("cutover-blockers-apply", contains="exited 1. The unit started")
        self.assertIn("MAY have been applied", err)
        self.assertIn(str(self.request), err)
        p = self.worker("apply", self.request.read_bytes())
        self.assertIn(b"the cutover has started", p.stderr, "the command's own output is forwarded")
        self.assertTrue(self.request.exists(), "a failed apply keeps the request")
        self.assertEqual(self.exported(), [REQUEST], "and delivers no result")
        self.assert_clean_tmp()

    def test_the_request_stays_on_a_unit_failure_a_timeout_and_bad_frames(self):
        self.fix_request()
        self.mode(run="fail")
        self.refused("cutover-blockers-apply", contains="exited 7. The unit started")
        self.h.mod.BLOCKERS_DEADLINE_SEC = 1
        self.mode(run="hang")
        self.refused(
            "cutover-blockers-apply",
            contains="did not finish within 1 seconds; it was stopped. The unit",
        )
        self.h.mod.BLOCKERS_DEADLINE_SEC = 630
        for data, contains in (
            (b"", "framed nothing"),
            (frame("list", b"x"), "framed list; it must frame"),
            (frame("result", b"x") + frame("rows", b"y"), "framed result, rows"),
            (frame("result", b"x") + b"?", "did not put a frame header"),
        ):
            self.raw(data)
            err = self.refused("cutover-blockers-apply", contains=contains)
            self.assertIn("MAY have been applied", err)
            self.assertTrue(self.request.exists(), contains)
        self.assertEqual(self.exported(), [REQUEST])

    def test_the_request_is_removed_only_when_it_is_still_the_file_that_was_read(self):
        self.fix_request()
        self.mode(bin_="swap-request")
        code, out, err = self.h.run("cutover-blockers-apply")
        self.assertEqual(code, 1)
        self.assertIn("The apply succeeded, but the request", err)
        self.assertIn("was not removed", err)
        self.assertEqual(
            self.request.read_bytes(), b"{}", "the new file under that name is left alone"
        )
        self.assertIn(f"aorg-cutover-blockers-result-{STAMP}.txt", self.exported())
        self.assertIn("EXPORTED", out)
        self.assertNotIn("REQUEST-REMOVED", out)

    def test_a_symlink_swapped_in_for_the_request_is_not_followed_to_remove_it(self):
        # The original file is moved aside and a link to it takes its name: the
        # link points at the very inode that was read, so only an lstat-style
        # check can tell that the name is no longer that file.
        self.fix_request()
        self.mode(bin_="symlink-request")
        code, out, err = self.h.run("cutover-blockers-apply")
        self.assertEqual(code, 1)
        self.assertIn("was not removed", err)
        self.assertTrue(self.request.is_symlink(), "the link is left for the owner to see")
        self.assertNotIn("REQUEST-REMOVED", out)

    def test_the_unit_gets_the_request_in_canonical_form_not_as_written(self):
        value = SECRET_B64
        self.put_request(
            f'{{ "value" : "{value}",\n  "key": "{ROBIN}", "action": "fix" }}'.encode()
        )
        self.ok("cutover-blockers-apply")
        want = json.dumps({"action": "fix", "key": ROBIN, "value": value}, separators=(",", ":"))
        self.assertEqual(self.stdin_data.read_bytes(), want.encode())

    def test_a_request_that_grows_past_the_limit_while_it_is_read_refuses(self):
        self.fix_request()
        limit = self.h.mod.BLOCKERS_PART_LIMIT
        real = self.h.mod._read_all
        # Only the request read is told it grew (the config is read the same way).
        self.h.mod._read_all = lambda fd, cap: (
            b" " * (limit + 1) if cap == limit + 1 else real(fd, cap)
        )
        self.refused("cutover-blockers-apply", contains="grew past")
        self.assertEqual(self.systemd_runs(), [])

    def test_the_apply_worker_refuses_a_request_on_stdin_over_the_cap(self):
        limit = self.h.mod.BLOCKERS_PART_LIMIT
        p = self.worker("apply", stdin=b" " * (limit + 4096 + 1))
        self.assertNotEqual(p.returncode, 0)
        self.assertIn(b"the request on stdin is too large", p.stderr)
        self.assertEqual(self.bin_calls(), [])

    def test_a_result_that_cannot_be_written_still_removes_the_request_and_says_so(self):
        self.fix_request()
        self.mode(bin_="plant-result")
        code, out, err = self.h.run("cutover-blockers-apply")
        self.assertEqual(code, 1)
        self.assertIn("the apply succeeded but its result could not be written", err)
        self.assertIn("Fixed hex:", err, "the command's output is printed instead")
        self.assertFalse(self.request.exists(), "the applied request does not linger")
        self.assertIn("REQUEST-REMOVED", out)

    def test_a_successful_apply_does_not_remove_other_files(self):
        other = self.export / "aorg-keyring-20261122T000000Z"
        other.write_bytes(b"keep")
        self.put_request({"action": "delete", "key": ROBIN})
        self.ok("cutover-blockers-apply")
        self.assertEqual(
            self.exported(), sorted([other.name, f"aorg-cutover-blockers-result-{STAMP}.txt"])
        )

    # -- the worker -----------------------------------------------------------

    def test_the_worker_is_not_a_stub_and_takes_exactly_list_or_apply(self):
        mod = self.h.mod
        self.assertNotIn("_blockers-worker", mod.STUBS)
        self.assertNotIn("_blockers-worker", mod.STUBS.values())
        self.assertFalse((HELPERS / "_blockers-worker").exists())
        for argv in ([], ["x"], ["list", "x"], ["export"], ["apply", "x"]):
            code, _, err = self.h.run("_blockers-worker", *argv)
            self.assertEqual(code, 2, argv)
            self.assertIn("usage", err)

    def test_the_worker_re_checks_the_request_it_is_handed(self):
        for stdin in (
            b"",
            b"not json",
            json.dumps({"action": "delete", "key": "HEX:" + ROBIN}).encode(),
            json.dumps({"action": "fix", "key": ROBIN, "value": "!"}).encode(),
        ):
            p = subprocess.run(
                [sys.executable, "-I", str(self.patched), "_blockers-worker", "apply"],
                input=stdin,
                capture_output=True,
                check=False,
            )
            self.assertEqual(p.returncode, 1, stdin)
            self.assertEqual(p.stdout, b"", "nothing on stdout but frames")
        self.assertEqual(self.bin_calls(), [], "the command was never run")

    def test_the_stubs_are_the_two_no_argument_files_and_nothing_else_is_added(self):
        mod = self.h.mod
        self.assertEqual(mod.STUBS["cutover-blockers-list"], "cutover-blockers-list")
        self.assertEqual(mod.STUBS["cutover-blockers-apply"], "cutover-blockers-apply")
        self.assertEqual(
            sorted(k for k in mod.STUBS if "blockers" in k),
            ["cutover-blockers-apply", "cutover-blockers-list"],
        )
        for name in ("cutover-blockers-list", "cutover-blockers-apply"):
            self.assertTrue((HELPERS / name).is_file(), name)

    def test_the_none_sentence_is_the_one_the_command_prints(self):
        src = (REPO_ROOT / "core" / "app" / "offline_cutover_blockers.go").read_text()
        self.assertIn(f'"{self.h.mod.BLOCKERS_NONE}', src)
        self.assertTrue(NONE_TEXT.startswith(self.h.mod.BLOCKERS_NONE))

    def test_the_part_limit_is_the_commands_own_cap(self):
        src = (REPO_ROOT / "core" / "app" / "offline_cutover_blockers.go").read_text()
        m = re.search(r"cutoverBlockersMaxFile\s*=\s*(\d+)\s*<<\s*(\d+)", src)
        self.assertIsNotNone(m)
        self.assertEqual(self.h.mod.BLOCKERS_PART_LIMIT, int(m.group(1)) << int(m.group(2)))


if __name__ == "__main__":
    unittest.main()
