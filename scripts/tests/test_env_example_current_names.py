# file: scripts/tests/test_env_example_current_names.py
# version: 0.1.0
# guid: 7b3e5a92-1c4d-4f08-9e26-d5a0c8f1b374
# last-edited: 2026-11-21

"""Guard: .env.example must not name an env variable the flip retired.

Run with:
    python3 -m unittest discover -s scripts -p 'test_env_example_current_names.py' -v

deploy/retired-env-names.txt lists every retired name. A `refuse` name stops the
service from starting when it is set, and a `warn` name is ignored, so a template
that still carries one either breaks a copy-paste setup or silently configures
nothing (the old basic-auth block left the UI with no gate). Commented-out
assignments (`# NAME=value`) count too: a reader uncomments them.
"""

from __future__ import annotations

import pathlib
import re
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
LIST = REPO / "deploy" / "retired-env-names.txt"
TEMPLATE = REPO / ".env.example"

ASSIGNMENT = re.compile(r"^\s*#?\s*([A-Z][A-Z0-9_]*)=")


def retired_names() -> dict[str, tuple[str, str]]:
    out: dict[str, tuple[str, str]] = {}
    for line in LIST.read_text().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        name, verdict, replacement = line.split("\t")
        out[name] = (verdict, replacement)
    return out


def template_names() -> list[tuple[int, str]]:
    names = []
    for no, line in enumerate(TEMPLATE.read_text().splitlines(), 1):
        m = ASSIGNMENT.match(line)
        if m:
            names.append((no, m.group(1)))
    return names


class TestEnvExampleCurrentNames(unittest.TestCase):
    def test_lists_parse(self) -> None:
        self.assertGreater(len(retired_names()), 100)
        self.assertGreater(len(template_names()), 10)

    def test_no_retired_name_in_template(self) -> None:
        retired = retired_names()
        hits = [
            f"{TEMPLATE.name}:{no}: {n} is retired ({retired[n][0]}); use {retired[n][1]}"
            for no, n in template_names()
            if n in retired
        ]
        self.assertEqual(hits, [], "\n" + "\n".join(hits))

    def test_guard_catches_a_retired_name(self) -> None:
        self.assertIsNotNone(ASSIGNMENT.match("# ROOT_DIR=/x"))
        self.assertIn("ROOT_DIR", retired_names())


if __name__ == "__main__":
    unittest.main()
