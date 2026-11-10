# file: scripts/tests/test_record_r0_answers.py
# version: 1.1.0
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
        # x-return-tokens changes the ABS /login body: a replay must send it too.
        self.assertEqual(
            r.recorded_request_headers("none", {"x-return-tokens": "true"})["x-return-tokens"],
            "true",
        )
        self.assertNotIn("cookie", r.recorded_request_headers("none", {"cookie": "a=b"}))


class VolatileFieldsTest(unittest.TestCase):
    def test_parse_path(self):
        self.assertEqual(
            r.parse_path('data.api_keys[?name=="r2-answers"]'),
            [("key", "data"), ("key", "api_keys"), ("where", ("name", "r2-answers"))],
        )
        self.assertEqual(
            r.parse_path("libraries[*].folders[*].addedAt"),
            [("key", "libraries"), ("all", None), ("key", "folders"), ("all", None),
             ("key", "addedAt")],
        )  # fmt: skip
        with self.assertRaises(ValueError):
            r.parse_path("data..x[")

    def test_normalize_and_drop(self):
        body = {
            "data": {
                "count": 2,
                "api_keys": [
                    {"name": "scoped-view", "days_since_last_use": 3},
                    {"name": "r2-answers", "days_since_last_use": 0},
                ],
            }
        }
        n = r.apply_rule(body, r.parse_path('data.api_keys[?name=="r2-answers"]'), "drop")
        self.assertEqual(n, 1)
        r.apply_rule(body, r.parse_path("data.api_keys[*].days_since_last_use"), "normalize")
        self.assertEqual(
            body["data"]["api_keys"], [{"name": "scoped-view", "days_since_last_use": r.VOLATILE}]
        )
        self.assertEqual(r.apply_rule(body, r.parse_path("data.missing"), "normalize"), 0)

    def test_request_patterns(self):
        self.assertTrue(
            r._request_matches("POST /api/items/*/play", "POST", "/api/items/abc-1/play")
        )
        self.assertFalse(r._request_matches("POST /api/items/*/play", "GET", "/api/items/abc/play"))
        self.assertTrue(
            r._request_matches(
                "GET /api/v1/auth/api-keys?all=true", "GET", "/api/v1/auth/api-keys?all=true"
            )
        )

    def test_a_rule_that_matches_nothing_fails(self):
        files = {name: {"exchanges": []} for name, _ in r.ANSWERS}
        with self.assertRaises(r.w.CorpusError):
            r.normalize_answers(files)
        # A normalize rule fails as a drop rule does.
        ex = {"request": {"method": "GET", "path": "/x"}, "response": {"body": {"a": 1}}}
        files = {"search": {"exchanges": [ex]}}
        rule = {
            "answer": "search",
            "request": "GET /x",
            "path": "b",
            "action": "normalize",
            "why": "t",
        }
        with self.assertRaises(r.w.CorpusError):
            r.normalize_answers(files, (rule,))
        r.normalize_answers(files, ({**rule, "path": "a"},))
        self.assertEqual(ex["response"]["body"], {"a": r.VOLATILE})

    def test_prefix_predicate_narrows_to_import_rows(self):
        body = {"data": {"items": [
            {"file_path": r.IMPORT_PREFIX + "Selina Kyle/Hush/Hush.flac", "updated_at": "t1"},
            {"file_path": "/tmp/r2corpus/stage1/library/Selina Kyle/Hush/Hush.flac", "updated_at": "t0"},
            {"updated_at": "t2"},
        ]}}  # fmt: skip
        tokens = r.parse_path(f"data.items[?file_path^={json.dumps(r.IMPORT_PREFIX)}].updated_at")
        self.assertEqual(tokens[2][0], "prefix")
        self.assertEqual(r.apply_rule(body, tokens, "normalize"), 1)
        self.assertEqual([i["updated_at"] for i in body["data"]["items"]], [r.VOLATILE, "t0", "t2"])
        with self.assertRaises(ValueError):
            r.parse_path("data[?file_path^=3]")

    def test_session_ids_are_masked_in_request_paths(self):
        self.assertEqual(
            r.mask_path("POST /api/session/*/sync", "/api/session/3216421d-333e/sync"),
            "/api/session/<volatile>/sync",
        )
        ex = {"request": {"method": "POST", "path": "/api/session/s-1/close"},
              "response": {"body": {"__non_json_body__": "OK"}}}  # fmt: skip
        rules = (
            {"answer": "abs", "request": "POST /api/session/*/close", "target": "request.path",
             "path": "", "action": "mask", "why": "t"},
            {"answer": "abs", "request": "POST /api/session/*/close", "path": "__non_json_body__",
             "action": "normalize", "why": "t"},
        )  # fmt: skip
        # The mask is listed first but applied last, so the response rule still
        # matches the path as it was sent.
        r.normalize_answers({"abs": {"exchanges": [ex]}}, rules)
        self.assertEqual(ex["request"]["path"], "/api/session/<volatile>/close")
        self.assertEqual(ex["response"]["body"]["__non_json_body__"], r.VOLATILE)

    def test_every_rule_parses_and_names_a_recorded_answer(self):
        names = {name for name, _ in r.ANSWERS}
        for rule in r.VOLATILE_FIELDS:
            self.assertIn(rule["answer"], names, rule)
            self.assertTrue(rule["why"], rule)
            if rule.get("target") == "request.path":
                # A path mask: each * in the request pattern is the masked part.
                self.assertEqual(rule["action"], "mask", rule)
                self.assertEqual(rule["path"], "", rule)
                self.assertIn("*", rule["request"], rule)
                continue
            self.assertNotIn("target", rule, rule)
            self.assertIn(rule["action"], ("normalize", "drop"), rule)
            r.parse_path(rule["path"])


class PickAbsItemTest(unittest.TestCase):
    def test_prefers_the_item_with_progress(self):
        # The list view reports numAudioFiles 0 for every item, as R0 does.
        zero = {"media": {"numAudioFiles": 0}}
        results = [{"id": "a", **zero}, {"id": "b", **zero}, {"id": "c", **zero}]
        self.assertEqual(r.pick_abs_item(results, {"b"}), "b")
        self.assertEqual(r.pick_abs_item(results, {"zz", "c"}), "c")
        self.assertEqual(r.pick_abs_item(results, set()), "a")
        self.assertIsNone(r.pick_abs_item([], {"a"}))


class LapseGuardTest(unittest.TestCase):
    def test_refuses_past_the_earliest_answer_lapse(self):
        utc = r.w._dt.timezone.utc
        m = {"lapses": {"answers_valid_until": "2027-11-10T11:04:19Z"}}
        r.check_answers_still_valid(m, r.w._dt.datetime(2027, 11, 10, 11, 4, 18, tzinfo=utc))
        with self.assertRaises(r.w.CorpusError):
            r.check_answers_still_valid(m, r.w._dt.datetime(2027, 11, 10, 11, 4, 19, tzinfo=utc))
        with self.assertRaises(r.w.CorpusError):
            r.check_answers_still_valid({"lapses": {}})

    def test_committed_manifest_names_the_lapses(self):
        lapses = json.loads(MANIFEST.read_text(encoding="utf-8"))["lapses"]
        for key in ("api_keys", "invites", "abs_sessions", "web_sessions", "answers_valid_until"):
            self.assertIn(key, lapses)
        visible = min(lapses["api_keys"], lapses["invites"], lapses["abs_sessions"])
        self.assertEqual(lapses["answers_valid_until"], visible)


class CommittedNormalizationTest(unittest.TestCase):
    def _answer(self, name):
        a = next(a for a in answers() if a["name"] == name)
        return json.loads((REPO_ROOT / a["file"]).read_text(encoding="utf-8"))

    def test_manifest_lists_the_volatile_fields(self):
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        self.assertEqual(manifest["volatile_fields"], [dict(x) for x in r.VOLATILE_FIELDS])

    def test_recorder_artefacts_are_not_in_the_answers(self):
        ident = self._answer("identity")
        for ex in ident["exchanges"]:
            self.assertNotIn('"name": "r2-answers"', json.dumps(ex["response"]["body"]))
        abs_ = self._answer("abs")
        for ex in abs_["exchanges"]:
            if ex["request"]["path"] == "/api/me/sessions":
                for sess in ex["response"]["body"]["sessions"]:
                    self.assertFalse(sess.get("current"), sess)

    def test_abs_playback_is_last(self):
        ex = self._answer("abs")["exchanges"]
        paths = [e["request"]["path"] for e in ex]
        self.assertTrue(paths[-3].endswith("/play"), paths[-3:])
        self.assertTrue(paths[-1].endswith("/close"), paths[-3:])

    def test_no_playback_session_id_or_start_date_survives(self):
        for e in self._answer("abs")["exchanges"]:
            p = e["request"]["path"]
            if p.startswith("/api/session/"):
                self.assertIn("/<volatile>/", p)
            if p.endswith("/play"):
                for k in ("id", "date", "dayOfWeek", "startedAt", "updatedAt"):
                    self.assertEqual(e["response"]["body"][k], r.VOLATILE, k)

    def test_root_dir_books_keep_their_stored_updated_at(self):
        lib = self._answer("library")["exchanges"][0]["response"]["body"]["data"]["items"]
        for b in lib:
            if b["file_path"].startswith(r.IMPORT_PREFIX):
                self.assertEqual(b["updated_at"], r.VOLATILE, b["file_path"])
            else:
                self.assertNotEqual(b["updated_at"], r.VOLATILE, b["file_path"])

    def test_abs_per_item_requests_read_stored_progress(self):
        for e in self._answer("abs")["exchanges"]:
            p = e["request"]["path"]
            if p.startswith("/api/me/progress/") or p.startswith("/api/me/bookmarks/"):
                self.assertEqual(e["response"]["status"], 200, p)


if __name__ == "__main__":
    unittest.main()
