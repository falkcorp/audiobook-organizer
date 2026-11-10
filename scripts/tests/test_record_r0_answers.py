# file: scripts/tests/test_record_r0_answers.py
# version: 1.0.0
# guid: 0317d881-99be-4394-8279-b5df7ce3cfa7
# last-edited: 2026-11-10
"""Tests for scripts/r2corpus/record_r0_answers.py and its committed answers (F2-12).

Every answer file manifest.json names must exist, parse and record its requests,
and no answer file may hold an API key's secret (each is ``<api-key:NAME>``).

Run: python3 -m pytest scripts/tests/test_record_r0_answers.py -q
"""

from __future__ import annotations

import importlib.util
import json
import pathlib
import re
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "r2corpus" / "record_r0_answers.py"
CORPUS = REPO_ROOT / "internal" / "books" / "testdata" / "r2corpus"
MANIFEST = CORPUS / "manifest.json"
ABK_RE = re.compile(r"abk_[A-Za-z0-9]{16,}")


def load_recorder():
    name = "record_r0_answers"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod  # before exec_module
    spec.loader.exec_module(mod)
    return mod


r = load_recorder()


def answers():
    return json.loads(MANIFEST.read_text(encoding="utf-8"))["answers"]


class CommittedAnswersTest(unittest.TestCase):
    def test_the_four_oracle_inputs_are_present(self):
        names = {a["name"] for a in answers()}
        for need in ("effective_config", "schedules", "search", "abs"):
            self.assertIn(need, names)
        self.assertGreaterEqual(len(names), 4)

    def test_every_answer_file_exists_parses_and_records_its_requests(self):
        for a in answers():
            path = REPO_ROOT / a["file"]
            self.assertTrue(path.is_file(), a["file"])
            data = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(data["name"], a["name"])
            self.assertTrue(a["what"])
            self.assertTrue(data["exchanges"], f"{a['file']} records no request")
            for ex in data["exchanges"]:
                self.assertIn(ex["request"]["method"], ("GET", "POST", "PATCH", "PUT", "DELETE"))
                self.assertTrue(ex["request"]["path"].startswith("/"))
                self.assertIsInstance(ex["response"]["status"], int)

    def test_no_answer_file_holds_an_api_key(self):
        files = sorted((CORPUS / "answers").glob("*.json"))
        self.assertTrue(files)
        for p in files:
            self.assertIsNone(ABK_RE.search(p.read_text(encoding="utf-8")), p.name)

    def test_api_key_auth_is_the_placeholder(self):
        seen = False
        for a in answers():
            data = json.loads((REPO_ROOT / a["file"]).read_text(encoding="utf-8"))
            for ex in data["exchanges"]:
                bearer = ex["request"]["headers"].get("authorization")
                if bearer is None:
                    continue
                self.assertRegex(bearer, r"^Bearer (<api-key:[a-z0-9-]+>|<redacted>)$")
                seen = seen or bearer.startswith("Bearer <api-key:")
        self.assertTrue(seen, "no answer request uses the recorder's API key")

    def test_api_key_requests_were_answered(self):
        for a in answers():
            data = json.loads((REPO_ROOT / a["file"]).read_text(encoding="utf-8"))
            for ex in data["exchanges"]:
                if ex["request"]["headers"].get("authorization", "").startswith("Bearer <api-key:"):
                    self.assertNotEqual(ex["response"]["status"], 401, ex["request"]["path"])

    def test_answers_cover_the_search_query_list(self):
        search = next(a for a in answers() if a["name"] == "search")
        data = json.loads((REPO_ROOT / search["file"]).read_text(encoding="utf-8"))
        self.assertEqual(len(data["exchanges"]), len(r.SEARCH_QUERIES))


class RedactTest(unittest.TestCase):
    KEY = "abk_" + "Q" * 40

    def test_known_key_becomes_its_placeholder(self):
        out = r.redact({"data": {"token": self.KEY, "note": f"key {self.KEY}"}}, {self.KEY: "x"})
        self.assertEqual(out["data"]["token"], "<api-key:x>")
        self.assertEqual(out["data"]["note"], "key <api-key:x>")

    def test_unknown_key_and_other_credentials(self):
        out = r.redact(
            {"api_key": self.KEY, "user": {"accessToken": "jwt.value.here", "password": "pw"}},
            {},
        )
        self.assertEqual(out["api_key"], "<api-key:unknown>")
        self.assertEqual(out["user"]["accessToken"], "<redacted>")
        self.assertEqual(out["user"]["password"], "<redacted>")

    def test_types_are_preserved(self):
        out = r.redact({"count": 3, "items": [1, None, True], "token": ""}, {})
        self.assertEqual(out, {"count": 3, "items": [1, None, True], "token": ""})

    def test_request_headers(self):
        self.assertEqual(
            r.recorded_request_headers("key:r2-answers", None)["authorization"],
            "Bearer <api-key:r2-answers>",
        )
        self.assertEqual(
            r.recorded_request_headers("abs:robin", None)["authorization"], "Bearer <redacted>"
        )
        self.assertNotIn("x-return-tokens", r.recorded_request_headers("none", {"x-return-tokens": "1"}))


if __name__ == "__main__":
    unittest.main()
