#!/usr/bin/env python3
"""Regression tests for CodeQL alert pagination in issue_manager.

The bug: get_codeql_alerts issued one request with per_page=100 and returned
the first page. Measured against this repo, 327 open alerts became 100 -- and
the caller printed "Found 100 open CodeQL alerts" and filed issues for 100.
A capped fetch still yields a plausible number, so nothing looked wrong.
"""

import os
import sys
import unittest
from unittest.mock import MagicMock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from issue_manager import GitHubAPI  # noqa: E402


def _page(n):
    r = MagicMock()
    r.json.return_value = [{"number": i} for i in range(n)]
    r.raise_for_status.return_value = None
    return r


class TestCodeQLAlertPagination(unittest.TestCase):
    def setUp(self):
        self.api = GitHubAPI("token", "falkcorp/audiobook-organizer")

    @patch("issue_manager.requests.get")
    def test_walks_every_page(self, mock_get):
        # 327 alerts: the real measured count. 100 + 100 + 100 + 27.
        mock_get.side_effect = [_page(100), _page(100), _page(100), _page(27)]
        alerts = self.api.get_codeql_alerts()
        self.assertEqual(len(alerts), 327, "must not stop at the first page")
        self.assertEqual(mock_get.call_count, 4)
        # Page numbers must advance; a fixed page=1 would loop forever.
        pages = [c.kwargs["params"]["page"] for c in mock_get.call_args_list]
        self.assertEqual(pages, [1, 2, 3, 4])

    @patch("issue_manager.requests.get")
    def test_short_page_ends_the_walk(self, mock_get):
        mock_get.side_effect = [_page(42)]
        self.assertEqual(len(self.api.get_codeql_alerts()), 42)
        self.assertEqual(mock_get.call_count, 1, "a short page means stop")

    @patch("issue_manager.requests.get")
    def test_exact_multiple_terminates_on_empty_page(self, mock_get):
        # 100 is the boundary case: a full page followed by an empty one.
        # Getting this wrong either truncates or loops.
        mock_get.side_effect = [_page(100), _page(0)]
        self.assertEqual(len(self.api.get_codeql_alerts()), 100)
        self.assertEqual(mock_get.call_count, 2)

    @patch("issue_manager.requests.get")
    def test_no_alerts(self, mock_get):
        mock_get.side_effect = [_page(0)]
        self.assertEqual(self.api.get_codeql_alerts(), [])

    @patch("issue_manager.requests.get")
    def test_midwalk_error_returns_partial_not_empty(self, mock_get):
        # Returning [] on a page-2 failure would report "no alerts" -- which
        # reads exactly like a clean scan. Keep what we already have.
        import requests as _requests

        mock_get.side_effect = [_page(100), _requests.RequestException("boom")]
        alerts = self.api.get_codeql_alerts()
        self.assertEqual(len(alerts), 100)

    @patch("issue_manager.requests.get")
    def test_state_is_forwarded_on_every_page(self, mock_get):
        mock_get.side_effect = [_page(100), _page(1)]
        self.api.get_codeql_alerts(state="closed")
        for call in mock_get.call_args_list:
            self.assertEqual(call.kwargs["params"]["state"], "closed")


if __name__ == "__main__":
    unittest.main()
