# file: scripts/tests/test_whisper_wire_text_guard.py
# version: 1.2.0
# guid: 7c1e5a93-0d46-4b28-8f3a-5e9b2d7a4c60
# last-edited: 2026-11-17
#
# A source-level tripwire on what the Whisper workers put in an HTTP body.
#
# The workers have no authentication, so no error text that leaves them may carry what a
# failure said (an exception message, an ffmpeg stderr line, a path). The GUARANTEE that
# none does is the runtime tests (test_whisper_server_cpu.py, test_whisper_mlx_pins.py),
# which drive every route that exists today with a secret planted in every failure and
# look for it in the body. This file is the cheaper, narrower layer: it fails, before
# anything runs, on a route written tomorrow in one of the shapes below. It does NOT
# prove that no other shape leaks (a body assembled from a variable, a dict built by
# update() or a subscript assignment, a key other than "error" or "detail", a helper
# that returns the text), and the runtime tests, not this file, are what covers those.
# The rule it enforces is the one in scripts/whisper_path_pipeline.py ("What leaves the
# process"): an error in a body is wire_error(...) or a fixed string.
#
# Stdlib only (ast + unittest), so it runs in the Repo Guards job, which has no
# fastapi. It parses the scripts; it never imports them.
#
# Every scripts/whisper_*.py is read (a glob, so a new worker script is covered the day
# it is added). What is checked:
#   1. a dict literal with an "error" or "detail" key holds a constant, a
#      wire_error(...) call, an `.wire` attribute (a PathJobError's sanitized text), or an
#      f-string that interpolates only module constants. A dict handed straight to a pipe
#      `send(...)` is the decode child's own protocol, not a response, and is exempt.
#   2. an HTTPException's detail, given as detail= OR as the second positional argument,
#      is held to the same rule.
#   3. the content of every `*Response` class (JSONResponse, PlainTextResponse, Response,
#      ...), positional or content=, is a dict literal (checked by rule 1) or the same
#      kind of safe text. A body built elsewhere and passed by name is refused.
#   4. a "ffmpeg" key in a body is a comparison (a bool), never the path.
#   5. the argument of `_note_start(...)` (what /health shows as child_start_error) is
#      None, a wire_error(...) call or a CAT_* constant.
#   6. every ChildFaultError and every PathJobError of a code that has a closed category
#      set (the keys of CODE_CATEGORIES in whisper_path_pipeline.py) is built with a
#      category= that is a CAT_* constant (or a conditional of two, or
#      ffmpeg_stderr_category(...), or a ChildFaultError's own already-validated .category).
#      A bare string there is refused, so a typo cannot reach the fault path unnoticed.
#   7. that category is also a MEMBER of its code's set. The sets are read from
#      CODE_CATEGORIES (and START_CATEGORIES, which a ChildFaultError is held to) in
#      whisper_path_pipeline.py, so PathJobError(ERR_FFMPEG, ..., category=CAT_IO_FAULT)
#      or a CAT_ name nothing defines is refused here, not by a ValueError that the
#      request handler's `except Exception` would turn into the wrong answer.

import ast
import pathlib
import re
import unittest

SCRIPTS = pathlib.Path(__file__).resolve().parents[1]
# The scripts this guard must find. It reads every scripts/whisper_*.py; this is only the
# floor, so a rename or a deletion of one of these cannot make the guard quietly read less.
EXPECTED_SCRIPTS = (
    "whisper_path_pipeline.py",
    "whisper_server.py",
    "whisper_mlx_server.py",
    "whisper_decode_child.py",
)
ERROR_KEYS = ("error", "detail")
_UPPER = re.compile(r"^[A-Z][A-Z0-9_]*$")
_CAT = re.compile(r"^CAT_[A-Z0-9_]+$")


def worker_scripts():
    return sorted(SCRIPTS.glob("whisper_*.py"))


def _frozenset_names(node, env):
    """The names in a frozenset expression of the shared module: frozenset({A, B}), a name
    bound earlier to one (START_CATEGORIES), or a `|` of those. None when unreadable."""
    if isinstance(node, ast.Name):
        return env.get(node.id)
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.BitOr):
        left, right = _frozenset_names(node.left, env), _frozenset_names(node.right, env)
        return None if left is None or right is None else left | right
    if (
        isinstance(node, ast.Call)
        and isinstance(node.func, ast.Name)
        and node.func.id == "frozenset"
        and len(node.args) == 1
        and isinstance(node.args[0], ast.Set)
        and all(isinstance(e, ast.Name) for e in node.args[0].elts)
    ):
        return frozenset(e.id for e in node.args[0].elts)
    return None


def category_sets():
    """{ERR_* name: the CAT_* names that code may carry}, read from CODE_CATEGORIES in the
    shared module's source, plus {"START": START_CATEGORIES} for a ChildFaultError. Every
    CAT_* constant must be defined once (a name resolves to one word)."""
    tree = ast.parse((SCRIPTS / "whisper_path_pipeline.py").read_text(encoding="utf-8"))
    env, words, table = {}, {}, None
    for node in tree.body:
        if not isinstance(node, ast.Assign) or len(node.targets) != 1:
            continue
        target = node.targets[0]
        if not isinstance(target, ast.Name):
            continue
        if _CAT.match(target.id) and isinstance(node.value, ast.Constant):
            words[target.id] = node.value.value
        names = _frozenset_names(node.value, env)
        if names is not None:
            env[target.id] = names
        if target.id == "CODE_CATEGORIES" and isinstance(node.value, ast.Dict):
            table = node.value
    if table is None:
        raise AssertionError("whisper_path_pipeline.py has no CODE_CATEGORIES dict literal")
    sets = {}
    for key, value in zip(table.keys, table.values):
        names = _frozenset_names(value, env)
        if not isinstance(key, ast.Name) or names is None:
            raise AssertionError(f"CODE_CATEGORIES entry {ast.dump(key)} is not readable")
        undefined = sorted(n for n in names if n not in words)
        if undefined:
            raise AssertionError(f"{key.id} lists {undefined}, which are not defined CAT_ names")
        sets[key.id] = names
    if len(set(words.values())) != len(words):
        raise AssertionError("two CAT_ constants share one word")
    if "START_CATEGORIES" not in env:
        raise AssertionError("whisper_path_pipeline.py has no START_CATEGORIES frozenset")
    sets["START"] = env["START_CATEGORIES"]
    return sets


def tabled_codes():
    """The ERR_* names that have a category set: the keys of CODE_CATEGORIES."""
    return frozenset(k for k in category_sets() if k != "START")


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


def _category_ok(node):
    """A category= that is a CAT_* constant, a conditional of two, a call of the one
    function that maps stderr onto the closed table, or a ChildFaultError's own .category
    (validated against the closed set when that error was built)."""
    if isinstance(node, ast.Name):
        return bool(_CAT.match(node.id))
    if isinstance(node, ast.IfExp):
        return _category_ok(node.body) and _category_ok(node.orelse)
    if isinstance(node, ast.Call):
        return _call_name(node) == "ffmpeg_stderr_category"
    if isinstance(node, ast.Attribute):
        return node.attr == "category"
    return False


def _membership_problems(node, allowed, owner):
    """Why `node` (a category= that _category_ok accepts) is not one of `allowed`, the CAT_
    names `owner` may carry; [] when it is. A conditional is checked branch by branch.
    ffmpeg_stderr_category(...) returns table rows, all of which are ERR_FFMPEG's words
    (a test in test_whisper_path_pipeline.py holds the table to that); an `.category` is a
    ChildFaultError's, validated against START_CATEGORIES when it was built, so it fits
    only a set that contains all of them."""
    if isinstance(node, ast.Name):
        if node.id in allowed:
            return []
        return [f"{node.id} is not a category of {owner}"]
    if isinstance(node, ast.IfExp):
        return _membership_problems(node.body, allowed, owner) + _membership_problems(
            node.orelse, allowed, owner
        )
    if isinstance(node, ast.Call):
        return [] if owner == "ERR_FFMPEG" else [f"{owner} does not take ffmpeg's stderr words"]
    if isinstance(node, ast.Attribute):
        return (
            []
            if owner in ("ERR_WORKER_MISCONFIGURED", "ChildFaultError")
            else [f"{owner} does not take a ChildFaultError's category"]
        )
    return [f"cannot read {ast.unparse(node)!r}"]


def _note_start_ok(node):
    return (
        (isinstance(node, ast.Constant) and node.value is None)
        or (isinstance(node, ast.Call) and _call_name(node) == "wire_error")
        or (isinstance(node, ast.Name) and bool(_CAT.match(node.id)))
    )


def violations(source, filename="<source>", sets=None):
    """Every place `source` could put local detail in a response, as 'file:line: why'.
    `sets` is category_sets() (the shared module's closed sets), read when not given."""
    sets = category_sets() if sets is None else sets
    tabled = frozenset(k for k in sets if k != "START")
    tree = ast.parse(source, filename)
    parents = {}
    for parent in ast.walk(tree):
        for child in ast.iter_child_nodes(parent):
            parents[child] = parent
    found = []

    def report(node, why):
        found.append(f"{filename}:{node.lineno}: {why}")

    def keyword(node, name):
        return next((k.value for k in node.keywords if k.arg == name), None)

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
                detail = keyword(node, "detail")
                if detail is None and len(node.args) > 1:
                    detail = node.args[1]
                if detail is not None and not _safe_text(detail):
                    report(detail, f"HTTPException detail is {ast.unparse(detail)!r}")
            elif name.endswith("Response"):
                content = keyword(node, "content")
                if content is None and node.args:
                    content = node.args[0]
                if (
                    content is not None
                    and not isinstance(content, ast.Dict)
                    and not _safe_text(content)
                ):
                    report(
                        node,
                        f"{name} content is {ast.unparse(content)!r}, not a dict literal "
                        "this guard can read or safe text",
                    )
            elif name == "_note_start":
                if len(node.args) != 1 or not _note_start_ok(node.args[0]):
                    report(node, "_note_start takes None, a wire_error(...) call or a CAT_* name")
            elif name == "ChildFaultError":
                category = keyword(node, "category")
                if category is None:
                    report(node, "ChildFaultError without a category=")
                elif not _category_ok(category):
                    report(category, f"category= is {ast.unparse(category)!r}, not a CAT_* name")
                else:
                    for why in _membership_problems(category, sets["START"], "ChildFaultError"):
                        report(category, f"ChildFaultError category: {why}")
            elif name == "PathJobError":
                category = keyword(node, "category")
                code = node.args[0] if node.args else None
                if not (isinstance(code, ast.Name) and code.id.startswith("ERR_")):
                    report(node, "PathJobError's code must be an ERR_* name this guard can read")
                elif code.id in tabled and category is None:
                    report(node, f"PathJobError({code.id}) without a category=")
                if category is not None and not _category_ok(category):
                    report(category, f"category= is {ast.unparse(category)!r}, not a CAT_* name")
                elif category is not None and isinstance(code, ast.Name):
                    for why in _membership_problems(
                        category, sets.get(code.id, frozenset()), code.id
                    ):
                        report(category, f"PathJobError({code.id}): {why}")
    return found


class WireTextGuard(unittest.TestCase):
    def test_no_worker_script_builds_a_response_from_failure_text(self):
        problems = []
        for path in worker_scripts():
            problems += violations(path.read_text(encoding="utf-8"), path.name)
        self.assertEqual([], problems, "\n".join(problems))

    def test_every_expected_script_is_read(self):
        found = {p.name for p in worker_scripts()}
        for name in EXPECTED_SCRIPTS:
            self.assertIn(name, found, f"{name} is not where the guard expects it")

    def test_the_guard_looks_at_something(self):
        # A guard that matches nothing passes vacuously; the shared module alone has
        # several response dicts, the category faults and the start-error notes.
        counts = {}
        calls = {"_note_start": 0, "ChildFaultError": 0, "PathJobError": 0}
        for path in worker_scripts():
            tree = ast.parse(path.read_text(encoding="utf-8"))
            counts[path.name] = sum(
                1
                for n in ast.walk(tree)
                if isinstance(n, ast.Dict)
                and any(isinstance(k, ast.Constant) and k.value in ERROR_KEYS for k in n.keys)
            )
            for n in ast.walk(tree):
                if isinstance(n, ast.Call) and _call_name(n) in calls:
                    calls[_call_name(n)] += 1
        self.assertGreaterEqual(counts["whisper_path_pipeline.py"], 4, counts)
        for name in EXPECTED_SCRIPTS:
            self.assertGreaterEqual(counts[name], 1, counts)
        self.assertGreaterEqual(calls["_note_start"], 4, calls)
        self.assertGreaterEqual(calls["ChildFaultError"], 10, calls)
        self.assertGreaterEqual(calls["PathJobError"], 30, calls)

    def test_the_category_sets_are_read_from_the_shared_module(self):
        sets = category_sets()
        self.assertIn("CAT_SPAWN_FAILED", sets["ERR_WORKER_MISCONFIGURED"])
        self.assertNotIn("CAT_SPAWN_FAILED", sets["ERR_FFMPEG"])
        self.assertIn("CAT_IO_FAULT", sets["ERR_FFMPEG_TIMEOUT"])
        # A ChildFaultError is held to the start words, a strict subset of its code's set.
        self.assertTrue(sets["START"] < sets["ERR_WORKER_MISCONFIGURED"])
        self.assertNotIn("CAT_SPAWN_FAILED", sets["START"])

    def test_the_tabled_codes_are_read_from_the_shared_module(self):
        tabled = tabled_codes()
        self.assertTrue({"ERR_FFMPEG", "ERR_PATH_UNMAPPED", "ERR_WORKER_MISCONFIGURED"} <= tabled)
        self.assertNotIn("ERR_NOT_FOUND", tabled)

    def test_the_guard_flags_each_way_to_leak(self):
        leaks = {
            "an exception's text": 'def f(e):\n    return {"text": "", "error": str(e)}\n',
            "an f-string of one": 'def f(e):\n    return {"error": f"failed: {e}"}\n',
            "a name": 'def f(msg):\n    return {"error": msg}\n',
            "a detail dict": 'def f(e):\n    return {"detail": str(e)}\n',
            "an HTTPException keyword": (
                "def f(e):\n    raise HTTPException(status_code=500, detail=str(e))\n"
            ),
            "an HTTPException positional": "def f(e):\n    raise HTTPException(500, str(e))\n",
            "the ffmpeg path": 'def f():\n    return {"ffmpeg": shutil.which("ffmpeg")}\n',
            "an opaque response": "def f(e):\n    return JSONResponse(make(e), 500)\n",
            "a response content keyword": (
                "def f(body):\n    return JSONResponse(content=body, status_code=500)\n"
            ),
            "a plain-text response": "def f(e):\n    return PlainTextResponse(str(e))\n",
            "a bare response content keyword": (
                "def f(e):\n    return Response(content=str(e), status_code=500)\n"
            ),
            "a start note of text": "def f(self, e):\n    self._note_start(str(e))\n",
            "a start note of an f-string": (
                'def f(self, e):\n    self._note_start(f"worker_misconfigured: {e}")\n'
            ),
            "an uncategorised fault": 'def f():\n    raise ChildFaultError("x", starting=True)\n',
            "a bare-string fault category": (
                'def f():\n    raise ChildFaultError("x", starting=True, category="start_timeout")\n'
            ),
            "a bare-string job category": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x", category="invalid_data")\n'
            ),
            "a typo'd category name": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x", category=invalid_data)\n'
            ),
            "a categorised code without one": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x")\n'
            ),
            "a computed code": 'def f(code):\n    raise PathJobError(code, "x")\n',
            "another code's category": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x", category=CAT_IO_FAULT)\n'
            ),
            "a category in one branch of a conditional that is another code's": (
                'def f(a):\n    raise PathJobError(ERR_DECODER_KILLED, "x",'
                " category=CAT_NOT_REAPED if a else CAT_DIED_ON_CLIP)\n"
            ),
            "a category nothing defines": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x", category=CAT_INVALID_DATAA)\n'
            ),
            "a category on a code with a fixed sentence": (
                'def f():\n    raise PathJobError(ERR_NOT_FOUND, "x", category=CAT_OTHER)\n'
            ),
            "a worker-side word under ffmpeg_error": (
                'def f():\n    raise PathJobError(ERR_FFMPEG, "x", category=CAT_SPAWN_FAILED)\n'
            ),
            "stderr words under another code": (
                'def f(e):\n    raise PathJobError(ERR_PATH_UNMAPPED, "x",'
                " category=ffmpeg_stderr_category(e))\n"
            ),
            "a fault category outside the start words": (
                'def f():\n    raise ChildFaultError("x", starting=True, category=CAT_DIED_ON_CLIP)\n'
            ),
            "a fault category that is a job word": (
                'def f():\n    raise ChildFaultError("x", starting=True, category=CAT_SPAWN_FAILED)\n'
            ),
            "a fault category an ffmpeg word": (
                'def f():\n    raise ChildFaultError("x", starting=True, category=CAT_OTHER)\n'
            ),
            "an inherited category under another code": (
                'def f(e):\n    raise PathJobError(ERR_FFMPEG, "x", category=e.category)\n'
            ),
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
            '    raise HTTPException(422, "fixed")\n'
            "    raise HTTPException(422, wire_error(ERR_BAD_REQUEST, CAT_JOB_NOT_OBJECT))\n"
            '    return JSONResponse({"detail": wire_error(ERR_INTERNAL)}, status_code=500)\n'
            '    return JSONResponse(content={"detail": "fixed"}, status_code=500)\n'
            '    return PlainTextResponse("fixed")\n'
            "    self._note_start(None)\n"
            "    self._note_start(wire_error(ERR_WORKER_MISCONFIGURED, e.category))\n"
            "    self._note_start(CAT_START_TIMEOUT)\n"
            '    raise ChildFaultError("x", starting=True, category=CAT_START_TIMEOUT)\n'
            '    raise PathJobError(ERR_NOT_FOUND, "x")\n'
            '    raise PathJobError(ERR_FFMPEG, "x", category=CAT_OTHER)\n'
            '    raise PathJobError(ERR_FFMPEG, "x", category=ffmpeg_stderr_category(err))\n'
            '    raise PathJobError(ERR_DECODER_KILLED, "x",'
            " category=CAT_NOT_REAPED if code is None else CAT_KILLED_BY_SIGNAL)\n"
            "    raise PathJobError(ERR_WORKER_MISCONFIGURED, str(e), category=e.category)\n"
            '    raise PathJobError(ERR_WORKER_MISCONFIGURED, "x", category=CAT_SPAWN_FAILED)\n'
            '    raise PathJobError(ERR_WORKER_MISCONFIGURED, "x", category=CAT_FFMPEG_MISSING)\n'
            '    raise PathJobError(ERR_FFMPEG_TIMEOUT, "x", category=CAT_IO_FAULT)\n'
            '    raise PathJobError(ERR_PATH_UNMAPPED, "x", category=CAT_SOURCE_UNREADABLE)\n'
            '    raise ChildFaultError("x", starting=True, category=CAT_FFMPEG_MISSING)\n'
        )
        self.assertEqual([], violations(fine))


if __name__ == "__main__":
    unittest.main()
