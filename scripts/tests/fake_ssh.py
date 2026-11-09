#!/usr/bin/env python3
# file: scripts/tests/fake_ssh.py
# version: 1.1.0
# guid: e299f5fd-6b61-4552-a861-35f0c11b511d
# last-edited: 2026-11-09

"""A fake deploy host for the deploy and rollback recipes' tests.

Installed on PATH as `ssh` (a symlink to this file), it stands in for the
real `ssh HOST 'command'` the Makefile, scripts/deploy-wait-ready.sh and
scripts/storage_format_guard.py run. No network, no real host: every
command runs inside a sandbox directory the test owns.

The scenario is a JSON file named by FAKE_SSH_SCENARIO:

    {
      "root":  "<sandbox directory>",
      "log":   "<invocation log, one JSON object per line>",
      "state": "<cursor file>",
      "map":   [["<host path>", "<sandbox path>"], ...],
      "rules": [
        {"match": "<regex searched in the command>",
         "responses": [{"stdout": "", "stderr": "", "exit": 0}, ...],
         "touch": ["<path to create when the rule answers>"]}
      ]
    }

A command is the remote command line, the arguments after the host joined
by spaces, as ssh sends it. The first rule whose regex matches answers; its
responses are played in order, one per call, and the last one repeats (the
cursor lives in the state file, so a `systemctl show` sequence advances
across processes). A command no rule matches runs for real under /bin/sh,
with every `sudo` prefix (`sudo`, `-n`, `-u USER`, `-H`) stripped, but only
when each absolute path in it lies inside the sandbox or is one of a few
system tools; anything else exits 255 naming the command, so a recipe can
never reach outside the sandbox. Before that check, each `map` pair rewrites
a host path to its sandbox stand-in (the rollback helpers installed under
/usr/local/libexec on a real host, their config in /etc); rules still match
the command as sent. Every invocation is logged first.

`write_fake_app` writes a fake application binary: a small Python script
that answers the fast-exit flags from an identity baked into its own text,
so a `cp` of it on the "host" carries its identity with it, and logs each
run to the same log.

Standard library only.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path

# System tools a sandboxed command may name by absolute path.
_ALLOWED_PATHS = {"/usr/bin/env", "/usr/bin/find", "/dev/null", "/bin/sh"}

# A sudo prefix at the start of the line or after a shell operator.
_SUDO = re.compile(r"(^|&&\s*|\|\|\s*|\|\s*|;\s*)sudo(?:\s+-n|\s+-H|\s+-u\s+\S+)*\s+")


def _load(path: str) -> dict:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def log_line(log: str, entry: dict) -> None:
    """Append one JSON line to the invocation log."""
    with open(log, "a", encoding="utf-8") as f:
        f.write(json.dumps(entry) + "\n")


def read_log(log: str | Path) -> list[dict]:
    """Return every logged invocation, in order."""
    p = Path(log)
    if not p.exists():
        return []
    return [json.loads(ln) for ln in p.read_text(encoding="utf-8").splitlines() if ln.strip()]


def _split_args(argv: list[str]) -> tuple[str, str]:
    """Return (host, command) from ssh's arguments: options, host, command."""
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in ("-o", "-p", "-i", "-l", "-F", "-J"):
            i += 2
            continue
        if a.startswith("-"):
            i += 1
            continue
        return a, " ".join(argv[i + 1 :])
    return "", ""


def _cursor(state: str, key: str) -> int:
    data = _load(state) if os.path.exists(state) else {}
    n = int(data.get(key, 0))
    data[key] = n + 1
    with open(state, "w", encoding="utf-8") as f:
        json.dump(data, f)
    return n


def _sandboxed(root: str, command: str) -> str | None:
    """Return the command with sudo prefixes stripped, or None when it names
    an absolute path outside the sandbox."""
    stripped = _SUDO.sub(r"\1", command)
    try:
        tokens = shlex.split(stripped)
    except ValueError:
        return None
    root = os.path.realpath(root)
    for t in tokens:
        # A path argument, or the value of VAR=/path or --flag=/path.
        value = t.split("=", 1)[1] if "=" in t and not t.startswith("/") else t
        if not value.startswith("/") or value in _ALLOWED_PATHS:
            continue
        real = os.path.realpath(value)
        if real != root and not real.startswith(root + os.sep):
            return None
    return stripped


def main(argv: list[str]) -> int:
    scenario_path = os.environ.get("FAKE_SSH_SCENARIO")
    if not scenario_path:
        print("fake_ssh: FAKE_SSH_SCENARIO is not set", file=sys.stderr)
        return 255
    sc = _load(scenario_path)
    host, command = _split_args(argv)
    log_line(sc["log"], {"kind": "ssh", "host": host, "command": command})
    for i, rule in enumerate(sc.get("rules", [])):
        if re.search(rule["match"], command):
            responses = rule.get("responses") or [{}]
            n = _cursor(sc["state"], f"rule{i}")
            r = responses[min(n, len(responses) - 1)]
            for p in rule.get("touch", []):
                Path(p).parent.mkdir(parents=True, exist_ok=True)
                Path(p).touch()
            sys.stdout.write(r.get("stdout", ""))
            sys.stderr.write(r.get("stderr", ""))
            return int(r.get("exit", 0))
    mapped = command
    for host_path, sandbox_path in sc.get("map", []):
        mapped = mapped.replace(host_path, sandbox_path)
    run = _sandboxed(sc["root"], mapped)
    if run is None:
        print(
            f"fake_ssh: no rule for, and refusing to run outside the sandbox: {command}",
            file=sys.stderr,
        )
        return 255
    proc = subprocess.run(["/bin/sh", "-c", run], check=False)
    return proc.returncode


_APP_TEMPLATE = """#!/usr/bin/env python3
# A fake audiobook-organizer binary written by scripts/tests/fake_ssh.py.
# fake-app: {name}
import json, os, sys
IDENT = json.loads({ident!r})
args = sys.argv[1:]
with open(IDENT["log"], "a", encoding="utf-8") as f:
    f.write(json.dumps({{"kind": "app", "name": IDENT["name"], "path": sys.argv[0], "args": args,
                        "env": {{k: os.environ.get(k, "") for k in ("HOME", "ABK_STATE_DIR")}}}}) + "\\n")
def unknown(flag):
    sys.stderr.write("Error: unknown flag: " + flag + "\\n")
    sys.exit(1)
if args == ["--print-storage-levels"]:
    if IDENT.get("levels_exit"):
        sys.exit(IDENT["levels_exit"])
    if IDENT.get("levels") is None:
        unknown("--print-storage-levels")
    print(json.dumps(IDENT["levels"]))
    sys.exit(0)
if args == ["--print-storage-format"]:
    if IDENT.get("format") is None:
        unknown("--print-storage-format")
    print(IDENT["format"])
    sys.exit(0)
if args == ["--print-capabilities"]:
    if IDENT.get("capabilities") is None:
        unknown("--print-capabilities")
    print(IDENT["capabilities"])
    sys.exit(0)
if args[:2] == ["storage", "restore-offline"]:
    rest = args[2:]
    into = rest[rest.index("--into") + 1] if "--into" in rest else ""
    if into:
        if IDENT.get("into_exit"):
            sys.stderr.write("restore-offline --into failed\\n")
            sys.exit(IDENT["into_exit"])
        os.makedirs(into)
        os.makedirs(into + ".state")
        with open(into + ".storage-levels", "w", encoding="utf-8") as f:
            f.write(IDENT.get("into_levels", ""))
        print("Copied the archive into " + into)
        sys.exit(0)
    if IDENT.get("restore_exit"):
        sys.stderr.write(IDENT.get("restore_stderr", "Error: restore refused") + "\\n")
        sys.exit(IDENT["restore_exit"])
    print("Restored (journal 20261109T000000Z-0badc0de); start the service.")
    sys.exit(0)
sys.stderr.write("fake app: unexpected arguments " + repr(args) + "\\n")
sys.exit(3)
"""


class HealthServer:
    """A local HTTP server answering GET /health from a scripted sequence of
    (status, body) pairs, one per request, the last repeating."""

    def __init__(self, answers: list[tuple[int, str]]):
        import http.server
        import threading

        self.answers = list(answers)
        self.requests = 0
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):  # noqa: N802 (http.server's name)
                if self.path != "/health":
                    self.send_response(404)
                    self.end_headers()
                    return
                status, body = outer.answers[min(outer.requests, len(outer.answers) - 1)]
                outer.requests += 1
                data = body.encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self._srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self._srv.server_address[1]}"
        self._thread = threading.Thread(target=self._srv.serve_forever, daemon=True)
        self._thread.start()

    def close(self) -> None:
        self._srv.shutdown()
        self._srv.server_close()


def install(
    sandbox: str | Path, rules: list[dict], path_map: list[list[str]] | None = None
) -> dict:
    """Prepare sandbox for a test: a bin directory whose `ssh` is this file,
    and a scenario with rules. Returns the environment to run commands with
    (PATH prefixed, FAKE_SSH_SCENARIO set) under the key "env", and the log
    path under "log"."""
    sb = Path(sandbox)
    bindir = sb / "fake-bin"
    bindir.mkdir(parents=True, exist_ok=True)
    link = bindir / "ssh"
    if not link.exists():
        link.symlink_to(Path(__file__).resolve())
    scenario = sb / "scenario.json"
    log = sb / "invocations.log"
    scenario.write_text(
        json.dumps(
            {
                "root": str(sb),
                "log": str(log),
                "state": str(sb / "cursors.json"),
                "rules": rules,
                "map": path_map or [],
            }
        ),
        encoding="utf-8",
    )
    env = dict(os.environ)
    env["PATH"] = f"{bindir}{os.pathsep}{env.get('PATH', '')}"
    env["FAKE_SSH_SCENARIO"] = str(scenario)
    return {"env": env, "log": log}


def show(
    active: str,
    sub: str = "running",
    restarts: int = 0,
    status: int = 0,
    invocation: str = "1nv0c4710n",
) -> dict:
    """One `systemctl show -p
    ActiveState,SubState,NRestarts,ExecMainStatus,InvocationID` answer."""
    return {
        "stdout": (
            f"ActiveState={active}\nSubState={sub}\nNRestarts={restarts}\n"
            f"ExecMainStatus={status}\nInvocationID={invocation}\n"
        ),
        "exit": 0,
    }


def write_fake_app(path: str | Path, ident: dict) -> None:
    """Write an executable fake application binary with identity ident.

    ident keys: name (logged), log (the invocation log), levels (a dict the
    binary prints for --print-storage-levels, None for "unknown flag"),
    levels_exit (exit with this status and no output instead), format (for
    --print-storage-format, None for "unknown flag"), capabilities (None for
    "unknown flag"), restore_exit, restore_stderr, into_exit, into_levels (the
    .storage-levels text an --into copy gets).
    """
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(
        _APP_TEMPLATE.format(ident=json.dumps(ident), name=ident.get("name", "")), encoding="utf-8"
    )
    p.chmod(0o755)


def fake_app_name(path: str | Path) -> str:
    """The name write_fake_app baked into the binary at path."""
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if line.startswith("# fake-app: "):
            return line[len("# fake-app: ") :]
    return ""


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
