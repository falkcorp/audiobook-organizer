# file: scripts/tests/test_whisper_wire_text_guard.py
# version: 1.0.0
# guid: 7c1e5a93-0d46-4b28-8f3a-5e9b2d7a4c60
# last-edited: 2026-11-17
#
# A source-level guard on what the Whisper workers put in an HTTP body.
#
# The workers have no authentication, so no error text that leaves them may carry what a
# failure said (an exception message, an ffmpeg stderr line, a path). The runtime tests
# (test_whisper_server_cpu.py, test_whisper_mlx_pins.py) drive the routes that exist
# today; this one fails on a route written tomorrow that builds its error some other
# way, before anything runs. The rule it enforces is the one in
# scripts/whisper_path_pipeline.py ("What leaves the process"): an error in a body is
# wire_error(...) or a fixed string, and nothing else.
#
# Stdlib only (ast + unittest), so it runs in the Repo Guards job, which has no
# fastapi. It parses the scripts; it never imports them.
#
# What is checked, in every worker script:
#   1. a dict literal with an "error" or "detail" key, and the detail= of an
#      HTTPException, holds a constant, a wire_error(...) call, an `.wire` attribute (a
#      PathJobError's sanitized text), or an f-string that interpolates only module
#      constants. A dict handed straight to a pipe `send(...)` is the decode child's own
#      protocol, not a response, and is exempt.
#   2. a "ffmpeg" key in a body is a comparison (a bool), never the path.
#   3. every ChildFaultError is built with a category= (the fixed word /health shows).

import ast
import pathlib
import re
import unittest

SCRIPTS = pathlib.Path(__file__).resolve().parents[1]
WORKER_SCRIPTS = (
    "whisper_path_pipeline.py",
    "whisper_server.py",
    "whisper_mlx_server.py",
    "whisper_decode_child.py",
)
ERROR_KEYS = ("error", "detail")
_UPPER = re.compile(r"^[A-Z][A-Z0-9_]*$")


def _is_const_name(node):
    return isinstance(node, ast.Name) and bool(_UPPER.match(node.id))


def _safe_text(node):
    """True when `node` cannot carry what a failure said."""
    if isinstance(node, ast.Constant):
        return True
    if isinstance(node, ast.Call):
        func = node.func
        name = func.id if isinstance(node.func, ast.Name) else getattr(func, "attr", "")
        return name == "wire_error"
    if isinstance(node, ast.Attribute):
        # PathJobError.wire: built from the code alone.
        return node.attr == "wire"
    if isinstance(node, ast.JoinedStr):
        for part in node.values:
            if isinstance(part, ast.FormattedValue):
                inner = part.value
                # A module constant, or the middleware's own configured limit.
                if not (
                    _is_const_name(inner)
                    or (isinstance(inner, ast.Attribute) and inner.attr == "limit")
                ):
                    return False
        return True
    return False


def _call_name(node):
    func = node.func
    return func.id if isinstance(func, ast.Name) else getattr(func, "attr", "")


def violations(source, filename="<source>"):
    """Every place `source` could put local detail in a response, as 'file:line: why'."""
    tree = ast.parse(source, filename)
    parents = {}
    for parent in ast.walk(tree):
        for child in ast.iter_child_nodes(parent):
            parents[child] = parent
    found = []

    def report(node, why):
        found.append(f"{filename}:{node.lineno}: {why}")

    for node in ast.walk(tree):
        if isinstance(node, ast.Dict):
            parent = parents.get(node)
            piped = isinstance(parent, ast.Call) and _call_name(parent) == "send"
            for key, value in zip(node.keys, node.values):
                if not (isinstance(key, ast.Constant) and isinstance(key.value, str)):
                    continue
                if key.value in ERROR_KEYS and not piped and not _safe_text(value):
                    report(
                        value, f'"{key.value}" in a response is built from {ast.unparse(value)!r}'
                    )
                if key.value == "ffmpeg" and not isinstance(value, ast.Compare):
                    report(value, f'"ffmpeg" must report found-or-not, not {ast.unparse(value)!r}')
        elif isinstance(node, ast.Call):
            name = _call_name(node)
            if name == "HTTPException":
                for kw in node.keywords:
                    if kw.arg == "detail" and not _safe_text(kw.value):
                        report(kw.value, f"HTTPException detail is {ast.unparse(kw.value)!r}")
            elif name == "JSONResponse" and node.args and not isinstance(node.args[0], ast.Dict):
                report(node, "JSONResponse body must be a dict literal this guard can read")
            elif name == "ChildFaultError" and not any(k.arg == "category" for k in node.keywords):
                report(node, "ChildFaultError without a category=")
    return found


class WireTextGuard(unittest.TestCase):
    def test_no_worker_script_builds_a_response_from_failure_text(self):
        problems = []
        for name in WORKER_SCRIPTS:
            path = SCRIPTS / name
            self.assertTrue(path.is_file(), f"{name} is not where the guard expects it")
            problems += violations(path.read_text(encoding="utf-8"), name)
        self.assertEqual([], problems, "\n".join(problems))

    def test_the_guard_looks_at_something(self):
        # A guard that matches nothing passes vacuously; the shared module alone has
        # several response dicts and the Mac worker's category faults.
        counts = {}
        for name in WORKER_SCRIPTS:
            tree = ast.parse((SCRIPTS / name).read_text(encoding="utf-8"))
            counts[name] = sum(
                1
                for n in ast.walk(tree)
                if isinstance(n, ast.Dict)
                and any(isinstance(k, ast.Constant) and k.value in ERROR_KEYS for k in n.keys)
            )
        self.assertGreaterEqual(counts["whisper_path_pipeline.py"], 4, counts)
        for name in WORKER_SCRIPTS:
            self.assertGreaterEqual(counts[name], 1, counts)

    def test_the_guard_flags_each_way_to_leak(self):
        leaks = {
            "an exception's text": 'def f(e):\n    return {"text": "", "error": str(e)}\n',
            "an f-string of one": 'def f(e):\n    return {"error": f"failed: {e}"}\n',
            "a name": 'def f(msg):\n    return {"error": msg}\n',
            "a detail dict": 'def f(e):\n    return {"detail": str(e)}\n',
            "an HTTPException": (
                "def f(e):\n    raise HTTPException(status_code=500, detail=str(e))\n"
            ),
            "the ffmpeg path": 'def f():\n    return {"ffmpeg": shutil.which("ffmpeg")}\n',
            "an opaque response": "def f(e):\n    return JSONResponse(make(e), 500)\n",
            "an uncategorised fault": ('def f():\n    raise ChildFaultError("x", starting=True)\n'),
        }
        for why, source in leaks.items():
            with self.subTest(why):
                self.assertTrue(violations(source), f"the guard missed {why}")

    def test_the_guard_accepts_the_sanctioned_shapes(self):
        fine = (
            "def f(e):\n"
            '    a = {"text": "", "error": wire_error(ERR_WHISPER)}\n'
            '    b = {"text": "", "error": e.wire}\n'
            '    c = {"text": text, "error": None}\n'
            '    d = {"detail": f"at most {MAX_JOBS_PER_REQUEST} jobs per request"}\n'
            '    e = {"ffmpeg": shutil.which("ffmpeg") is not None}\n'
            '    conn.send({"id": 1, "error": f"{type(e).__name__}: {e}"})\n'
            '    raise HTTPException(status_code=422, detail="fixed")\n'
            '    raise ChildFaultError("x", starting=True, category="start_timeout")\n'
        )
        self.assertEqual([], violations(fine))


if __name__ == "__main__":
    unittest.main()
