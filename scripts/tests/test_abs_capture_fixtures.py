# file: scripts/tests/test_abs_capture_fixtures.py
# version: 1.0.0
# guid: 9c766f9e-15e7-40d9-88b0-3c823780f6f5
# last-edited: 2026-09-11
"""Tests for the request-header capture in scripts/abs_capture_fixtures.py (TASK-009).

Fixtures are committed to a public repo. After login the harness's session carries
``Authorization: Bearer <token>`` on EVERY request, so the request-header capture
is one allowlist away from publishing live credentials. These tests build real
``requests`` PreparedRequests (session defaults merged with per-call headers, the
same object the harness reads) without sending anything over the network, and
check the rendered fixture file text, not just its dict keys, so a value
leaking through any path fails the test.

Run: python3 -m unittest scripts.tests.test_abs_capture_fixtures -v
"""

from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest import mock

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "abs_capture_fixtures.py"

HAVE_REQUESTS = importlib.util.find_spec("requests") is not None

# RFC 5737 documentation address; nothing is ever sent to it.
BASE = "http://192.0.2.10:13378"

ACCESS_TOKEN = "sentinel-access-token-value-7f3a"
REFRESH_TOKEN = "sentinel-refresh-token-value-91c2"
COOKIE_VALUE = "sentinel-cookie-value-44d8"
USER_AGENT = "conformance-capture/1.0 (Watch; wearable-probe)"


def _load_module():
    spec = importlib.util.spec_from_file_location("abs_capture_fixtures", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class _FakeResponse:
    """Just enough of requests.Response for write_fixture."""

    def __init__(self, prepared, status=200, body=None, headers=None):
        self.request = prepared
        self.status_code = status
        self.headers = headers or {"Content-Type": "application/json; charset=utf-8"}
        self._body = body if body is not None else {"ok": True}
        self.text = json.dumps(self._body)
        self.ok = 200 <= status < 400

    def json(self):
        return self._body


@unittest.skipUnless(HAVE_REQUESTS, "requests is not installed")
class RequestHeaderCaptureTest(unittest.TestCase):
    def setUp(self):
        import requests

        self.requests = requests
        self.mod = _load_module()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = mock.patch.object(self.mod, "FIXTURE_DIR", pathlib.Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)

    def _write(self, method, path, body, prepared):
        self.mod.write_fixture(method, path, body, _FakeResponse(prepared))
        out = pathlib.Path(self.tmp.name) / self.mod.slugify(method, path)
        text = out.read_text()
        return text, json.loads(text)

    def _authed_session(self):
        sess = self.requests.Session()
        sess.headers["Authorization"] = f"Bearer {ACCESS_TOKEN}"
        sess.headers["User-Agent"] = USER_AGENT
        sess.cookies.set("connect.sid", COOKIE_VALUE)
        return sess

    def test_refresh_call_records_user_agent_and_no_credentials(self):
        # The /auth/refresh call is the worst case: session Authorization plus a
        # per-call x-refresh-token, with a cookie jar and x-return-tokens on top.
        sess = self._authed_session()
        prepared = sess.prepare_request(
            self.requests.Request(
                "POST",
                f"{BASE}/auth/refresh",
                headers={"x-refresh-token": REFRESH_TOKEN, "x-return-tokens": "true"},
            )
        )
        # Precondition: the credentials really are on the wire-bound request, so
        # the assertions below prove the filter works rather than passing vacuously.
        self.assertEqual(prepared.headers["Authorization"], f"Bearer {ACCESS_TOKEN}")
        self.assertEqual(prepared.headers["x-refresh-token"], REFRESH_TOKEN)
        self.assertIn(COOKIE_VALUE, prepared.headers["Cookie"])

        text, fixture = self._write("POST", "/auth/refresh", None, prepared)

        req_headers = fixture["request"]["headers"]
        self.assertEqual(req_headers["user-agent"], USER_AGENT)
        self.assertLessEqual(set(req_headers), set(self.mod.REQUEST_HEADERS_TO_KEEP))
        for secret in (ACCESS_TOKEN, REFRESH_TOKEN, COOKIE_VALUE, "Bearer"):
            self.assertNotIn(secret, text)
        lowered = text.lower()
        for name in ("authorization", "cookie", "x-refresh-token", "x-return-tokens"):
            self.assertNotIn(f'"{name}"', lowered)
        # The response half keeps its existing shape.
        self.assertEqual(
            fixture["response"]["headers"], {"content-type": "application/json; charset=utf-8"}
        )

    def test_json_post_records_merged_session_and_call_headers(self):
        sess = self._authed_session()
        prepared = sess.prepare_request(
            self.requests.Request("POST", f"{BASE}/api/items/x/play", json={"a": 1})
        )
        _, fixture = self._write("POST", "/api/items/x/play", {"a": 1}, prepared)
        req_headers = fixture["request"]["headers"]
        self.assertEqual(req_headers["user-agent"], USER_AGENT)  # session default
        self.assertEqual(req_headers["content-type"], "application/json")  # per call

    def test_default_user_agent_is_recorded_when_session_sets_none(self):
        prepared = self.requests.Session().prepare_request(
            self.requests.Request("GET", f"{BASE}/ping")
        )
        _, fixture = self._write("GET", "/ping", None, prepared)
        self.assertTrue(fixture["request"]["headers"]["user-agent"].startswith("python-requests/"))

    def test_credential_header_is_redacted_even_if_allowlisted(self):
        widened = (*self.mod.REQUEST_HEADERS_TO_KEEP, "authorization", "cookie")
        with mock.patch.object(self.mod, "REQUEST_HEADERS_TO_KEEP", widened):
            kept = self.mod.kept_request_headers(
                {"Authorization": f"Bearer {ACCESS_TOKEN}", "Cookie": COOKIE_VALUE}
            )
        self.assertEqual(
            kept, {"authorization": self.mod.REDACTED, "cookie": self.mod.REDACTED}
        )

    def test_allowlist_never_names_a_credential_header(self):
        self.assertFalse(
            set(self.mod.REQUEST_HEADERS_TO_KEEP) & self.mod.CREDENTIAL_HEADERS
        )

    def test_empty_or_missing_headers_still_write_a_fixture(self):
        self.assertEqual(self.mod.kept_request_headers({}), {})
        self.assertEqual(self.mod.kept_request_headers(None), {})
        prepared = self.requests.Request("GET", f"{BASE}/status").prepare()
        prepared.headers.clear()
        _, fixture = self._write("GET", "/status", None, prepared)
        self.assertEqual(fixture["request"]["headers"], {})
        self.assertEqual(fixture["response"]["status"], 200)


if __name__ == "__main__":
    unittest.main()
