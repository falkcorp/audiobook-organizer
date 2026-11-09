# file: scripts/tests/test_unit_lifecycle.py
# version: 1.0.0
# guid: 42763f48-1a7c-4d74-8f41-6d9477d4162e
# last-edited: 2026-11-09

"""The unit-lifecycle job's two cells (09 §9.2.1 unit-lifecycle row; 09 §9.4 R1
Verification; 05-health §5H.4; R1-28).

It runs on GitHub's Ubuntu runner VM, where systemd is PID 1. It installs the
COMMITTED unit (deploy/audiobook-organizer.service) with the drop-in
scripts/tests/unit_lifecycle/dropin.conf, which overrides only ExecStart, the
database path and PrivateNetwork=yes, and runs a binary built with
`-tags unitlifecycle` (core/kernel/fakemigration_hook.go).

Cell 1, test_1: a 12 minute fake migration (MEDIACORE_FAKE_MIGRATION=12m, one
EXTEND_TIMEOUT_USEC per one-minute step) under the committed unit's
TimeoutStartSec=300 is not killed, and scripts/deploy-wait-ready.sh, pointed at
the local systemd through the fake-ssh shim, reports ready.

Cell 2, test_2: after a live restore is staged through the API (the server
exits 75), the unit restarts itself (Restart=on-failure) and reports ready.

PrivateNetwork=yes keeps the service's port off the runner, so a
systemd-socket-proxyd pair (JoinsNamespaceOf the service, the documented
pattern) forwards 127.0.0.1:PROXY_PORT into the service's namespace; the test
and deploy-wait-ready.sh reach /health and the API through it.

On a developer machine the cells skip with the reason. They need Linux, systemd
as PID 1, root, and a GitHub Actions runner (or UNIT_LIFECYCLE_ALLOW_HOST=1: the
test installs a unit, a user and directories). With UNIT_LIFECYCLE_REQUIRED=1,
which the job sets, a missing prerequisite FAILS instead, so the job cannot pass
by skipping.

Run in the job with:
    sudo -E python3 -m unittest -v scripts/tests/test_unit_lifecycle.py
"""

from __future__ import annotations

import http.cookiejar
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import fake_ssh  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
UNIT_SRC = REPO_ROOT / "deploy" / "audiobook-organizer.service"
DROPIN_SRC = Path(__file__).resolve().parent / "unit_lifecycle" / "dropin.conf"
WAIT_READY = REPO_ROOT / "scripts" / "deploy-wait-ready.sh"

UNIT = "audiobook-organizer.service"
UNIT_DIR = Path("/etc/systemd/system")
DROPIN_DIR = UNIT_DIR / (UNIT + ".d")
BIN_DEST = Path("/usr/local/bin/abo-unit-lifecycle")
SERVICE_USER = "audiobook"
STATE_DIR = Path("/var/lib/audiobook-organizer")
EXTRA_DIRS = (Path("/var/lib/audiobooks"), Path("/var/log/audiobook-organizer"))
PROXY = "abo-unit-lifecycle-proxy"
PROXY_PORT = 18484
BASE_URL = f"http://127.0.0.1:{PROXY_PORT}"
SSH_HOST = "unit-lifecycle-host.invalid"
FAKE_MIGRATION_ENV = "MEDIACORE_FAKE_MIGRATION"
FAKE_MIGRATION = "12m"
FAKE_MIGRATION_SECONDS = 12 * 60

REQUIRED = os.environ.get("UNIT_LIFECYCLE_REQUIRED") == "1"
BINARY = Path(os.environ.get("UNIT_LIFECYCLE_BINARY", "/tmp/abo"))


def _socket_proxyd() -> str:
    for p in ("/usr/lib/systemd/systemd-socket-proxyd", "/lib/systemd/systemd-socket-proxyd"):
        if os.path.exists(p):
            return p
    return ""


def unsupported_reason() -> str:
    """Why the cells cannot run here, or "" when they can."""
    if not sys.platform.startswith("linux"):
        return "not Linux"
    if not os.path.isdir("/run/systemd/system"):
        return "systemd is not PID 1 on this machine (no /run/systemd/system)"
    if os.geteuid() != 0:
        return "not root (the job runs the test with sudo)"
    if (
        os.environ.get("GITHUB_ACTIONS") != "true"
        and os.environ.get("UNIT_LIFECYCLE_ALLOW_HOST") != "1"
    ):
        return (
            "not a GitHub Actions runner: the test installs a systemd unit, a user and "
            "directories (set UNIT_LIFECYCLE_ALLOW_HOST=1 on a disposable VM)"
        )
    if not BINARY.is_file():
        return f"no test binary at {BINARY} (go build -tags unitlifecycle -o {BINARY} .)"
    if not _socket_proxyd():
        return "systemd-socket-proxyd is not installed"
    for p in (UNIT_DIR / UNIT, DROPIN_DIR, BIN_DEST):
        if p.exists():
            return f"{p} already exists: this is not a disposable machine"
    return ""


def sh(*cmd: str, check: bool = True, timeout: float = 120) -> subprocess.CompletedProcess:
    return subprocess.run(list(cmd), check=check, capture_output=True, text=True, timeout=timeout)


def unit_props(*names: str) -> dict[str, str]:
    out = sh("systemctl", "show", "-p", ",".join(names), UNIT).stdout
    return dict(line.partition("=")[::2] for line in out.splitlines() if "=" in line)


def committed_start_timeout() -> int:
    """TimeoutStartSec of the committed unit, in seconds."""
    m = re.search(r"^TimeoutStartSec=(\d+)\s*$", UNIT_SRC.read_text(), re.M)
    if not m:
        raise AssertionError("the committed unit sets no TimeoutStartSec in whole seconds")
    return int(m.group(1))


def exec_start_args(text: str) -> list[str]:
    """The arguments after the binary of the last non-empty ExecStart= line."""
    lines = [m for m in re.findall(r"^ExecStart=(.*)$", text, re.M) if m.strip()]
    return lines[-1].split()[1:] if lines else []


PROXY_SOCKET = f"""[Unit]
Description=unit-lifecycle test: forward {BASE_URL} into the service's PrivateNetwork namespace

[Socket]
ListenStream=127.0.0.1:{PROXY_PORT}

[Install]
WantedBy=sockets.target
"""

PROXY_SERVICE = f"""[Unit]
Description=unit-lifecycle test: proxy into the service's namespace
Requires={UNIT}
After={UNIT}
JoinsNamespaceOf={UNIT}

[Service]
ExecStart={_socket_proxyd() or "/usr/lib/systemd/systemd-socket-proxyd"} 127.0.0.1:8484
PrivateTmp=yes
PrivateNetwork=yes
"""


class DropInTest(unittest.TestCase):
    """Runs everywhere: the drop-in overrides only what the job allows."""

    def test_dropin_overrides_only_the_binary_the_database_path_and_private_network(self) -> None:
        text = DROPIN_SRC.read_text()
        body = [ln.strip() for ln in text.splitlines() if ln.strip() and not ln.startswith("#")]
        self.assertEqual(body[0], "[Service]")
        self.assertEqual(
            [ln for ln in body if ln.startswith("[")], ["[Service]"], "only a [Service] section"
        )
        keys = [ln.split("=", 1)[0] for ln in body[1:]]
        self.assertEqual(sorted(keys), ["Environment", "ExecStart", "ExecStart", "PrivateNetwork"])
        self.assertEqual(body[1], "ExecStart=", "ExecStart is reset before it is set")
        self.assertIn("PrivateNetwork=yes", body)
        env = [ln for ln in body if ln.startswith("Environment=")]
        self.assertEqual(len(env), 1)
        self.assertIn("DATABASE_PATH=", env[0])
        self.assertNotIn(FAKE_MIGRATION_ENV, "\n".join(body), "the cell sets the duration")

    def test_dropin_keeps_the_committed_units_arguments(self) -> None:
        """The binary is the only part of ExecStart the drop-in changes."""
        self.assertEqual(
            exec_start_args(DROPIN_SRC.read_text()), exec_start_args(UNIT_SRC.read_text())
        )

    def test_committed_start_timeout_is_shorter_than_the_fake_migration(self) -> None:
        """Otherwise cell 1 would prove nothing about EXTEND_TIMEOUT_USEC."""
        self.assertLess(committed_start_timeout() + 60, FAKE_MIGRATION_SECONDS)


class UnitLifecycleTest(unittest.TestCase):
    """The two cells, in order (test_0 is the unit's own verify)."""

    created_user = False
    created_dirs: list[Path] = []

    @classmethod
    def setUpClass(cls) -> None:
        reason = unsupported_reason()
        if reason:
            if REQUIRED:
                raise AssertionError(
                    f"UNIT_LIFECYCLE_REQUIRED=1 but the cells cannot run: {reason}"
                )
            raise unittest.SkipTest(reason)
        cls.created_dirs = []
        cls._tmp = tempfile.mkdtemp(prefix="unit-lifecycle-")
        try:
            cls._install()
        except BaseException:
            cls._uninstall()
            raise
        cls.addClassCleanup(cls._uninstall)

    @classmethod
    def _install(cls) -> None:
        if sh("id", SERVICE_USER, check=False).returncode != 0:
            sh("useradd", "-r", "-s", "/usr/sbin/nologin", "-d", str(STATE_DIR), SERVICE_USER)
            cls.created_user = True
        for d in (STATE_DIR, *EXTRA_DIRS):
            if not d.exists():
                cls.created_dirs.append(d)
            d.mkdir(parents=True, exist_ok=True)
            sh("chown", f"{SERVICE_USER}:{SERVICE_USER}", str(d))
        sh("install", "-m", "0755", str(BINARY), str(BIN_DEST))
        shutil.copyfile(UNIT_SRC, UNIT_DIR / UNIT)
        DROPIN_DIR.mkdir()
        shutil.copyfile(DROPIN_SRC, DROPIN_DIR / "unit-lifecycle.conf")
        (UNIT_DIR / f"{PROXY}.socket").write_text(PROXY_SOCKET)
        (UNIT_DIR / f"{PROXY}.service").write_text(PROXY_SERVICE)
        sh("systemctl", "daemon-reload")
        sh("systemctl", "start", f"{PROXY}.socket")

    @classmethod
    def _uninstall(cls) -> None:
        """Best effort and idempotent: runs in finally, whatever failed."""
        for cmd in (
            ("systemctl", "stop", f"{PROXY}.socket", f"{PROXY}.service", UNIT),
            ("systemctl", "reset-failed", UNIT),
            ("systemctl", "unset-environment", FAKE_MIGRATION_ENV),
        ):
            sh(*cmd, check=False)
        shutil.rmtree(DROPIN_DIR, ignore_errors=True)
        for p in (
            UNIT_DIR / UNIT,
            UNIT_DIR / f"{PROXY}.socket",
            UNIT_DIR / f"{PROXY}.service",
            BIN_DEST,
        ):
            p.unlink(missing_ok=True)
        sh("systemctl", "daemon-reload", check=False)
        for d in cls.created_dirs:
            shutil.rmtree(d, ignore_errors=True)
        if cls.created_user:
            sh("userdel", SERVICE_USER, check=False)
        shutil.rmtree(getattr(cls, "_tmp", ""), ignore_errors=True)

    def setUp(self) -> None:
        """Every test starts from a stopped service and an empty state."""
        sh("systemctl", "stop", f"{PROXY}.service", UNIT, check=False)
        sh("systemctl", "reset-failed", UNIT, check=False)
        sh("systemctl", "unset-environment", FAKE_MIGRATION_ENV, check=False)
        for d in (STATE_DIR, *EXTRA_DIRS[:1]):
            sh("find", str(d), "-mindepth", "1", "-delete")
        self.t0 = int(time.time())

    def journal(self) -> str:
        return sh(
            "journalctl",
            "-u",
            UNIT,
            "--no-pager",
            "-o",
            "short-precise",
            "-S",
            f"@{self.t0}",
            check=False,
        ).stdout

    def wait_ready(self, timeout: int, overall: int) -> tuple[subprocess.CompletedProcess, float]:
        """scripts/deploy-wait-ready.sh in its unit mode, over the fake-ssh shim.

        The shim runs `systemctl show -p ... UNIT` on this machine instead of
        over ssh; the URL is the proxy into the service's private namespace.
        """
        h = fake_ssh.install(Path(self._tmp), [])
        env = dict(h["env"], DEPLOY_WAIT_INTERVAL="5")
        t = time.monotonic()
        proc = subprocess.run(
            ["bash", str(WAIT_READY), BASE_URL, str(timeout), SSH_HOST, UNIT],
            env=env,
            capture_output=True,
            text=True,
            timeout=overall,
            check=False,
        )
        return proc, time.monotonic() - t

    def test_0_the_committed_unit_verifies(self) -> None:
        """systemd-analyze verify on the installed unit and drop-in (R1-27's
        unit was written on a machine without systemd)."""
        proc = sh("systemd-analyze", "verify", UNIT, check=False)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_1_fake_migration_longer_than_the_start_timeout_is_not_killed(self) -> None:
        sh("systemctl", "set-environment", f"{FAKE_MIGRATION_ENV}={FAKE_MIGRATION}")
        sh("systemctl", "start", "--no-block", UNIT)
        proc, took = self.wait_ready(timeout=180, overall=FAKE_MIGRATION_SECONDS + 600)
        journal = self.journal()
        out = f"{proc.stdout}\n{proc.stderr}\n{journal[-4000:]}"
        self.assertEqual(proc.returncode, 0, out)
        # The boot really ran the twelve one-minute steps, and outlived the
        # committed TimeoutStartSec by their extensions alone.
        self.assertGreaterEqual(took, FAKE_MIGRATION_SECONDS, out)
        self.assertGreater(took, committed_start_timeout() + 60, out)
        self.assertNotIn("timed out", journal, out)
        self.assertNotIn("Failed with result", journal, out)
        props = unit_props("ActiveState", "NRestarts")
        self.assertEqual((props["ActiveState"], props["NRestarts"]), ("active", "0"), out)

    def test_2_staged_restore_comes_back_by_itself_and_reports_ready(self) -> None:
        sh("systemctl", "start", "--no-block", UNIT)
        proc, _ = self.wait_ready(timeout=180, overall=600)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr + self.journal()[-4000:])
        pid_before = unit_props("MainPID")["MainPID"]

        answer = self.stage_restore()
        self.assertEqual(answer[0], 202, answer)
        self.assertTrue(answer[1]["data"]["restart_required"], answer)

        # The server exits 75 after staging; Restart=on-failure brings it back.
        deadline = time.monotonic() + 180
        while unit_props("NRestarts")["NRestarts"] == "0":
            self.assertLess(
                time.monotonic(), deadline, "never restarted:\n" + self.journal()[-4000:]
            )
            time.sleep(2)
        proc, _ = self.wait_ready(timeout=180, overall=600)
        journal = self.journal()
        out = f"{proc.stdout}\n{proc.stderr}\n{journal[-4000:]}"
        self.assertEqual(proc.returncode, 0, out)
        self.assertRegex(journal, r"status=75", out)
        props = unit_props("ActiveState", "NRestarts", "MainPID")
        self.assertEqual((props["ActiveState"], props["NRestarts"]), ("active", "1"), out)
        self.assertNotEqual(props["MainPID"], pid_before, out)

    def stage_restore(self) -> tuple[int, dict]:
        """Bootstrap an admin, take a backup and POST the restore.

        The restore is refused to API keys (it changes the database), so it
        goes through a signed-in session: the bootstrap exchange also returns
        the admin's generated password. Returns (HTTP status, JSON body).
        """
        token_file = STATE_DIR / ".bootstrap-token"
        deadline = time.monotonic() + 120
        while not (token_file.exists() and token_file.stat().st_size > 0):
            self.assertLess(time.monotonic(), deadline, "no bootstrap token was written")
            time.sleep(1)
        time.sleep(1)  # the file is written, then the token is registered
        token = token_file.read_text().strip()
        jar = http.cookiejar.CookieJar()
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

        def call(
            method: str, path: str, body: dict, headers: dict | None = None
        ) -> tuple[int, dict]:
            req = urllib.request.Request(
                BASE_URL + path,
                method=method,
                data=json.dumps(body).encode(),
                headers={"Content-Type": "application/json", **(headers or {})},
            )
            try:
                with opener.open(req, timeout=120) as r:
                    return r.status, json.loads(r.read().decode() or "null")
            except urllib.error.HTTPError as e:
                raw = e.read().decode()
                try:
                    return e.code, json.loads(raw)
                except ValueError:
                    return e.code, {"raw": raw}

        status, doc = call("POST", "/api/v1/auth/bootstrap", {"token": token})
        self.assertEqual(status, 200, "bootstrap exchange failed")
        admin = doc["data"]
        status, _ = call(
            "POST",
            "/api/v1/auth/login",
            {"username": admin["username"], "password": admin["generated_password"]},
        )
        self.assertEqual(status, 200, "login failed")
        status, doc = call("POST", "/api/v1/backup/create", {})
        self.assertEqual(status, 200, f"backup create failed: {doc}")
        return call(
            "POST",
            "/api/v1/backup/restore",
            {"backup_filename": doc["data"]["filename"], "verify": True},
        )


if __name__ == "__main__":
    unittest.main()
