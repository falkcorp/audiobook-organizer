# file: scripts/tests/test_export_offhost.py
# version: 0.1.0
# guid: 7d2c9e41-5b83-4f06-a1d7-3e8b6c0f2a95
# last-edited: 2026-11-22

"""Unit tests for scripts/export_offhost.py, the Mac side of `make export-keyring`
and `make export-install-secrets`. The end-to-end runs (the real recipe, the
real helpers, a fake ssh and scp) are `ExportTargets` in test_make_rollback.py;
these cover what a fake ssh cannot make happen: a copy whose bytes are wrong,
a half-finished pair, a failing delete, and the parsing and directory rules on
their own. No ssh, no scp, no network: both are replaced.

Run with:
    python3 -m unittest discover -s scripts -p 'test_export_offhost.py' -v
"""

from __future__ import annotations

import contextlib
import hashlib
import importlib.util
import io
import os
import stat
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = REPO_ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS))
import export_offhost as eo  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "storage_format_guard_for_export_offhost", SCRIPTS / "storage_format_guard.py"
)
guard = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
sys.modules[_spec.name] = guard
_spec.loader.exec_module(guard)

STAMP = "20261122T010203Z"
SHA = "a" * 64
KEYRING = b"batcave keyring"
KEY = b"alfred-key"
CONFIG = b"core: {}\n"


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def exported(name: str, data: bytes) -> str:
    return f"EXPORTED {name} {len(data)} {sha(data)}"


class Parse(unittest.TestCase):
    def test_keyring_line(self) -> None:
        name = f"aorg-keyring-{STAMP}"
        got = eo.parse(exported(name, KEYRING) + "\n", ("keyring",), ())
        self.assertEqual(got, {"keyring": (name, len(KEYRING), sha(KEYRING))})

    def test_install_secrets_with_and_without_a_config(self) -> None:
        key, cfg = f"aorg-encryption-key-{STAMP}", f"aorg-config-{STAMP}.yaml"
        both = exported(key, KEY) + "\n" + exported(cfg, CONFIG) + "\n"
        got = eo.parse(both, ("encryption-key",), ("config",))
        self.assertEqual(sorted(got), ["config", "encryption-key"])
        got = eo.parse(exported(key, KEY) + "\nABSENT config\n", ("encryption-key",), ("config",))
        self.assertEqual(sorted(got), ["encryption-key"])

    def test_everything_else_is_a_refusal(self) -> None:
        key, cfg = f"aorg-encryption-key-{STAMP}", f"aorg-config-{STAMP}.yaml"
        for label, out, want, opt in (
            ("empty", "", ("encryption-key",), ("config",)),
            ("no config, not absent", exported(key, KEY), ("encryption-key",), ("config",)),
            (
                "config both present and absent",
                exported(key, KEY) + "\n" + exported(cfg, CONFIG) + "\nABSENT config",
                ("encryption-key",),
                ("config",),
            ),
            ("name with a slash", f"EXPORTED a/../{key} 1 {SHA}", ("encryption-key",), ()),
            ("name with a space", f"EXPORTED {key} x 1 {SHA}", ("encryption-key",), ()),
            ("uppercase hash", f"EXPORTED {key} 1 {'A' * 64}", ("encryption-key",), ()),
            ("short hash", f"EXPORTED {key} 1 {'a' * 63}", ("encryption-key",), ()),
            ("bad stamp", f"EXPORTED aorg-encryption-key-2026 1 {SHA}", ("encryption-key",), ()),
            ("trailing text", f"EXPORTED {key} 1 {SHA} extra", ("encryption-key",), ()),
            (
                "the same kind twice",
                exported(key, KEY) + "\n" + exported(key, KEY),
                ("encryption-key",),
                (),
            ),
            (
                "a kind not asked for",
                exported(f"aorg-keyring-{STAMP}", KEYRING),
                ("encryption-key",),
                (),
            ),
            ("a line of noise", "hello\n" + exported(key, KEY), ("encryption-key",), ()),
        ):
            self.assertIsInstance(eo.parse(out, want, opt), str, label)


class LocalDir(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()

    def test_a_new_directory_is_0700_whatever_the_umask(self) -> None:
        old = os.umask(0)
        self.addCleanup(os.umask, old)
        d = eo.local_dir(str(self.root / "a" / "b"))
        self.assertEqual(stat.S_IMODE(d.stat().st_mode), 0o700)

    def test_the_default_is_under_home_with_a_utc_stamp(self) -> None:
        with unittest.mock.patch.dict(os.environ, {"HOME": str(self.root)}):
            d = eo.local_dir("")
        self.assertEqual(d.parent, self.root / "aorg-offhost")
        self.assertRegex(d.name, r"^[0-9]{8}T[0-9]{6}Z$")
        self.assertEqual(stat.S_IMODE(d.stat().st_mode), 0o700)

    def test_an_existing_private_directory_is_reused(self) -> None:
        d = self.root / "mine"
        d.mkdir(mode=0o700)
        self.assertEqual(eo.local_dir(str(d)), d)

    def test_an_existing_directory_others_can_enter_is_refused(self) -> None:
        for mode in (0o755, 0o750, 0o701, 0o770):
            d = self.root / f"d{mode:o}"
            d.mkdir()
            d.chmod(mode)
            with self.assertRaises(SystemExit, msg=oct(mode)):
                eo.local_dir(str(d))

    def test_a_symlink_or_a_file_is_refused(self) -> None:
        real = self.root / "real"
        real.mkdir(mode=0o700)
        link = self.root / "link"
        os.symlink(real, link)
        with self.assertRaises(SystemExit):
            eo.local_dir(str(link))
        f = self.root / "file"
        f.write_text("x")
        with self.assertRaises(SystemExit):
            eo.local_dir(str(f))


class Main(unittest.TestCase):
    """main() with the host replaced: run_remote answers, scp is a function."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name).resolve()
        self.dest = self.root / "off"
        self.remote: list[str] = []
        self.scp_wrote: dict[str, bytes | None] = {}
        self.rm_rc = 0
        self.helper_out = ""
        self.helper_rc = 0
        conf = (
            "bin = /usr/local/bin/aorg\ndb = /data/db\nstate_dir = /var/lib/x\n"
            "unit = audiobook-organizer.service\nservice_user = alfred\narchive_dir = /archives\n"
            "export_dir = /home/alfred/export\n"
        )
        self.conf = conf

    def run_remote(self, host: str, cmd: str, timeout=None, stream=False):
        self.remote.append(cmd)
        if cmd.startswith("cat "):
            return guard.Result(self.conf, "", 0)
        if cmd.startswith("rm "):
            return guard.Result("", "", self.rm_rc)
        return guard.Result(self.helper_out, "", self.helper_rc)

    def scp(self, argv, **kw):
        remote, local = argv[-2], argv[-1]
        name = remote.rsplit("/", 1)[1]
        data = self.scp_wrote.get(name)
        if data is not None:
            Path(local).write_bytes(data)
        return subprocess.CompletedProcess(argv, 0 if data is not None else 1)

    def run_main(self, what: str, **env) -> tuple[int, str, str]:
        variables = {
            "AORG_OH_WHAT": what,
            "AORG_OH_HOST": "wayne-manor.invalid",
            "AORG_OH_BIN": "/usr/local/bin/aorg",
            "AORG_OH_DB": "/data/db",
            "AORG_OH_UNIT": "audiobook-organizer.service",
            "AORG_OH_GUARD": str(SCRIPTS / "storage_format_guard.py"),
            "AORG_OH_DIR": str(self.dest),
        }
        variables.update(env)
        out, err = io.StringIO(), io.StringIO()
        with contextlib.ExitStack() as stack:
            stack.enter_context(unittest.mock.patch.dict(os.environ, variables, clear=True))
            stack.enter_context(unittest.mock.patch.object(eo, "load_guard", lambda path: guard))
            stack.enter_context(unittest.mock.patch.object(guard, "run_remote", self.run_remote))
            stack.enter_context(unittest.mock.patch.object(subprocess, "run", self.scp))
            stack.enter_context(contextlib.redirect_stdout(out))
            stack.enter_context(contextlib.redirect_stderr(err))
            code = eo.main()
        return code, out.getvalue(), err.getvalue()

    def setup_keyring(self, delivered: bytes | None = KEYRING, reported: bytes = KEYRING) -> str:
        name = f"aorg-keyring-{STAMP}"
        self.helper_out = exported(name, reported) + "\n"
        self.scp_wrote[name] = delivered
        return name

    def test_a_good_copy_is_renamed_into_place_then_the_remote_is_deleted(self) -> None:
        name = self.setup_keyring()
        code, out, err = self.run_main("keyring")
        self.assertEqual(code, 0, err)
        self.assertEqual((self.dest / name).read_bytes(), KEYRING)
        self.assertEqual(stat.S_IMODE((self.dest / name).stat().st_mode), 0o600)
        self.assertEqual(self.remote[-1], f"rm -f -- /home/alfred/export/{name}")
        self.assertIn(sha(KEYRING), out)
        self.assertEqual(list(self.dest.glob("*.part")), [])

    def test_wrong_bytes_are_refused_and_the_remote_is_kept(self) -> None:
        name = self.setup_keyring(delivered=b"batcave keyring!")
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertIn("KEPT", err)
        self.assertEqual(list(self.dest.iterdir()), [])
        self.assertFalse(any(c.startswith("rm ") for c in self.remote))
        self.assertIn(f"/home/alfred/export/{name}", err)

    def test_the_right_size_with_other_bytes_is_refused(self) -> None:
        self.setup_keyring(delivered=b"x" * len(KEYRING))
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertIn("KEPT", err)
        self.assertEqual(list(self.dest.iterdir()), [])

    def test_the_wrong_hash_from_the_host_is_refused_too(self) -> None:
        self.setup_keyring(reported=b"something else")
        code, _, _ = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertEqual(list(self.dest.iterdir()), [])

    def test_a_bad_second_file_removes_the_first_local_part_and_deletes_nothing(self) -> None:
        key, cfg = f"aorg-encryption-key-{STAMP}", f"aorg-config-{STAMP}.yaml"
        self.helper_out = exported(key, KEY) + "\n" + exported(cfg, CONFIG) + "\n"
        self.scp_wrote[key] = KEY
        self.scp_wrote[cfg] = b"wrong"
        code, _, err = self.run_main("install-secrets")
        self.assertEqual(code, 1)
        self.assertEqual(list(self.dest.iterdir()), [], "the good first copy is not left behind")
        self.assertFalse(any(c.startswith("rm ") for c in self.remote))
        self.assertIn(key, err)
        self.assertIn(cfg, err)

    def test_a_failing_delete_is_an_error_that_names_the_command(self) -> None:
        name = self.setup_keyring()
        self.rm_rc = 1
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertEqual((self.dest / name).read_bytes(), KEYRING, "the copy is kept")
        self.assertIn(f"rm -f -- /home/alfred/export/{name}", err)

    def test_an_existing_local_file_is_never_overwritten(self) -> None:
        name = self.setup_keyring()
        self.dest.mkdir(mode=0o700)
        (self.dest / name).write_bytes(b"mine")
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertEqual((self.dest / name).read_bytes(), b"mine")
        self.assertIn("already exists", err)
        self.assertFalse(any(c.startswith("rm ") for c in self.remote))

    def test_a_helper_failure_copies_nothing(self) -> None:
        self.setup_keyring()
        self.helper_rc = 1
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertIn("nothing was copied", err)
        self.assertEqual(list(self.dest.iterdir()), [])

    def test_an_unknown_target_is_refused(self) -> None:
        for what in ("", "keyring; id", "everything"):
            code, _, _ = self.run_main(what)
            self.assertEqual(code, 1, what)
        self.assertEqual(self.remote, [])

    def test_a_bad_host_or_path_is_refused_before_ssh(self) -> None:
        for env in (
            {"AORG_OH_HOST": "-oProxyCommand=id"},
            {"AORG_OH_HOST": "a b"},
            {"AORG_OH_DB": "/data/my db"},
            {"AORG_OH_BIN": "relative/bin"},
            {"AORG_OH_UNIT": "x.service; id"},
        ):
            code, _, _ = self.run_main("keyring", **env)
            self.assertEqual(code, 1, env)
        self.assertEqual(self.remote, [])

    def test_a_config_without_export_dir_refuses_before_the_helper_runs(self) -> None:
        self.setup_keyring()
        self.conf = self.conf.replace("export_dir = /home/alfred/export\n", "")
        code, _, err = self.run_main("keyring")
        self.assertEqual(code, 1)
        self.assertIn("no export_dir", err)
        self.assertEqual([c for c in self.remote if not c.startswith("cat ")], [])


if __name__ == "__main__":
    unittest.main()
