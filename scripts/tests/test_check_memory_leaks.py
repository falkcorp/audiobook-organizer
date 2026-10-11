# file: scripts/tests/test_check_memory_leaks.py
# version: 1.0.0
# guid: 3f7c2a91-6d4e-4b8a-9c15-e2a07b5d4c63
# last-edited: 2026-10-11
"""Tests for scripts/check-memory-leaks.py's untracked-timer heuristic."""

import importlib.util
import pathlib
import unittest

_SPEC = importlib.util.spec_from_file_location(
    "check_memory_leaks",
    pathlib.Path(__file__).resolve().parents[1] / "check-memory-leaks.py",
)
leaks = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(leaks)


def _scan(code: str) -> list:
    scanner = leaks.MemoryLeakDetector(web_src_dir=".")
    scanner.check_untracked_timers("web/src/components/Example.tsx", code)
    return scanner.issues


# A long effect: the cleanup sits more than 20 lines after the timer, outside
# the useEffect lookahead, so only the assignment rule can clear it.
_FILLER = "\n".join(f"    const x{i} = {i};" for i in range(40))


class UntrackedTimerTest(unittest.TestCase):
    def test_timer_assigned_to_plain_timer_is_tracked(self):
        code = f"useEffect(() => {{\n{_FILLER}\n    const timer = setTimeout(run, 10);\n    return () => clearTimeout(timer);\n}});"
        self.assertEqual(_scan(code), [])

    def test_timer_assigned_to_plain_timeout_is_tracked(self):
        code = f"function f() {{\n{_FILLER}\n  const timeout = setTimeout(run, 10);\n}}"
        self.assertEqual(_scan(code), [])

    def test_prefixed_names_still_tracked(self):
        code = "function f() {\n  modeTimerRef.current = setTimeout(run, 10);\n  const pollTimer = setTimeout(run, 5);\n}"
        self.assertEqual(_scan(code), [])

    def test_bare_timer_call_is_flagged(self):
        code = f"function f() {{\n{_FILLER}\n  setTimeout(run, 10);\n}}"
        self.assertEqual(len(_scan(code)), 1)

    def test_lookalike_name_is_not_tracked(self):
        # "timerless" is not an assignment to a timer variable.
        code = f"function f() {{\n{_FILLER}\n  const timerless = 1; setTimeout(run, 10);\n}}"
        self.assertEqual(len(_scan(code)), 1)


if __name__ == "__main__":
    unittest.main()
