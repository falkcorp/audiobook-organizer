#!/usr/bin/env python3
# file: scripts/test_check_memory_leaks.py
# version: 1.0.0
# guid: 8f3a1c26-5d94-4e7b-a2c8-91b0d6e4f752
# last-edited: 2026-08-14

"""Regression tests for check-memory-leaks.py's listener pairing (LEAKSCAN-SCOPE).

Run with:  python3 scripts/test_check_memory_leaks.py
"""

import importlib.util

import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "check_memory_leaks", Path(__file__).with_name("check-memory-leaks.py")
)
assert _spec is not None and _spec.loader is not None
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)


def listener_issues(content: str):
    scanner = _mod.MemoryLeakDetector(web_src_dir=".")
    scanner.check_untracked_listeners("fixture.ts", content)
    return scanner.issues


class TestListenerPairing(unittest.TestCase):
    def test_named_handler_add_nested_two_levels_below_remove(self):
        """THE 2026-08-11 regression: add two brace levels below a
        function-level `finally` remove. The old depth-abort look-ahead
        abandoned the search at the two closing braces and reported a
        false leak (hit for real in web/src/utils/apiFetch.ts)."""
        content = """
function fetchWithAbort(url: string) {
  const controller = new AbortController();
  const onAbort = () => controller.abort();
  try {
    if (someCondition) {
      if (otherCondition) {
        window.addEventListener('beforeunload', onAbort);
      } else {
        doSomethingElse();
      }
    }
    return doFetch(url);
  } finally {
    window.removeEventListener('beforeunload', onAbort);
  }
}
"""
        self.assertEqual(listener_issues(content), [])

    def test_named_handler_never_removed_is_flagged(self):
        content = """
function attach() {
  window.addEventListener('resize', onResize);
}
"""
        issues = listener_issues(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("resize", issues[0][2])

    def test_named_handler_removed_for_different_event_is_flagged(self):
        """Identity pairing must match BOTH event and handler."""
        content = """
function attach() {
  window.addEventListener('scroll', onScroll);
  window.removeEventListener('resize', onScroll);
}
"""
        issues = listener_issues(content)
        self.assertEqual(len(issues), 1)
        self.assertIn("scroll", issues[0][2])

    def test_named_handler_same_event_different_handler_is_flagged(self):
        content = """
function attach() {
  window.addEventListener('scroll', onScrollA);
}
function detachOther() {
  window.removeEventListener('scroll', onScrollB);
}
"""
        issues = listener_issues(content)
        self.assertEqual(len(issues), 1)

    def test_dotted_handler_identity_pairs(self):
        content = """
class Widget {
  mount() {
    window.addEventListener('resize', this.onResize);
  }
  unmount() {
    window.removeEventListener('resize', this.onResize);
  }
}
"""
        self.assertEqual(listener_issues(content), [])

    def test_anonymous_handler_with_nearby_remove_still_pairs(self):
        """Anonymous handlers keep the original event-name look-ahead."""
        content = """
function attach() {
  window.addEventListener('resize', () => rerender());
  return () => {
    window.removeEventListener('resize', handlerRef.current);
  };
}
"""
        self.assertEqual(listener_issues(content), [])

    def test_inline_function_expression_treated_as_anonymous(self):
        content = """
function attach() {
  window.addEventListener('resize', function () { rerender(); });
}
"""
        issues = listener_issues(content)
        self.assertEqual(len(issues), 1)


if __name__ == "__main__":
    unittest.main()
