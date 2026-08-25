#!/usr/bin/env python3
"""
# file: scripts/issue_manager.py
Unified GitHub issue management script.

This script provides comprehensive issue management functionality:
1. Process issue updates from issue_updates.json (create, update, comment, close, delete)
2. Manage Copilot review comment tickets
3. Close duplicate issues by title
4. Generate tickets for CodeQL security alerts
5. Provide unified CLI interface for all operations

Environment Variables:
  GH_TOKEN - GitHub token with repo access
  REPO - repository in owner/name format
  GITHUB_EVENT_NAME - webhook event name (for event-driven operations)
  GITHUB_EVENT_PATH - path to the event payload (for event-driven operations)

Local Usage:
  export GH_TOKEN=$(gh auth token)
  export REPO=owner/repository-name
  python issue_manager.py update-issues    # Process issue_updates.json
  python issue_manager.py copilot-tickets  # Manage Copilot review tickets
  python issue_manager.py close-duplicates # Close duplicate issues
  python issue_manager.py codeql-alerts    # Generate tickets for CodeQL alerts
  python issue_manager.py event-handler    # Handle GitHub webhook events
"""

import argparse
import json
import os
import sys
from collections import defaultdict
from typing import Any

try:
    import requests
except ImportError:
    print("Error: 'requests' module not found. Installing it now...", file=sys.stderr)
    import subprocess

    try:
        # Use --user flag to install in user directory (avoids externally-managed-environment error)
        subprocess.check_call(["uv", "pip", "install", "requests", "--quiet"])
        import requests

        print("✓ Successfully installed and imported 'requests' module")
    except subprocess.CalledProcessError as e:
        print(f"Failed to install 'requests' module: {e}", file=sys.stderr)
        print(
            "Please install it manually: pip install --user requests", file=sys.stderr
        )
        sys.exit(1)

# Configuration constants
API_VERSION = "2022-11-28"
COPILOT_USER = "github-copilot[bot]"
COPILOT_LABEL = "copilot-review"
CODEQL_LABEL = "security"
DUPLICATE_CHECK_LABEL = "duplicate-check"

AUTO_CLOSE_ON_FILE_CHANGE = False  # Set to True to automatically close CodeQL issues when their files are modified


def normalize_json_string(text: str) -> str:
    """Intelligently normalize JSON string content.

    This function handles multiple scenarios:
    1. Escaped sequences like \\n, \\t, \\" that need unescaping
    2. Already-formatted content with real newlines/tabs that should be preserved
    3. Mixed content with both escaped and real formatting

    Args:
        text: The input string from JSON

    Returns:
        Properly normalized string with escape sequences resolved where appropriate
    """
    if not isinstance(text, str):
        return text

    # Quick check: if the string doesn't contain backslashes, no escaping is needed
    if "\\" not in text:
        return text

    # Count different types of content to make intelligent decisions
    has_real_newlines = "\n" in text and "\\n" not in text
    has_escaped_newlines = "\\n" in text
    has_real_quotes = '"' in text and '\\"' not in text
    has_escaped_quotes = '\\"' in text
    has_real_tabs = "\t" in text and "\\t" not in text
    has_escaped_tabs = "\\t" in text

    # If content has ONLY escaped sequences and no real formatting, unescape it
    if (has_escaped_newlines or has_escaped_quotes or has_escaped_tabs) and not (
        has_real_newlines or has_real_quotes or has_real_tabs
    ):
        try:
            import codecs

            return codecs.decode(text, "unicode_escape")
        except (UnicodeDecodeError, ValueError):
            # If unescaping fails, try manual replacement of common sequences
            return (
                text.replace("\\n", "\n")
                .replace("\\t", "\t")
                .replace('\\"', '"')
                .replace("\\\\", "\\")
            )

    # If content has BOTH escaped and real formatting, do selective replacement
    elif has_escaped_newlines or has_escaped_quotes or has_escaped_tabs:
        # Only replace the clearly escaped sequences, preserve real formatting
        result = text
        # Replace escaped sequences but be careful not to double-process
        if "\\n" in result and not result.count("\\n") > result.count("\n") * 2:
            result = result.replace("\\n", "\n")
        if '\\"' in result:
            result = result.replace('\\"', '"')
        if "\\t" in result and not result.count("\\t") > result.count("\t") * 2:
            result = result.replace("\\t", "\t")
        if "\\\\" in result:
            result = result.replace("\\\\", "\\")
        return result

    # If content appears to be already properly formatted, leave it alone
    return text


def escape_for_json_output(data: any) -> str:
    """Safely escape content for JSON output with proper character handling.

    Args:
        data: The data to convert to JSON string

    Returns:
        Properly escaped JSON string
    """
    import json

    try:
        return json.dumps(
            data,
            separators=(",", ":"),
            ensure_ascii=False,
            escape_forward_slashes=False,
        )
    except TypeError:
        # Fallback for older Python versions that don't support escape_forward_slashes
        return json.dumps(data, separators=(",", ":"), ensure_ascii=False)


def unescape_json_string(text: str) -> str:
    """Legacy function name - redirects to normalize_json_string for backward compatibility."""
    return normalize_json_string(text)


class OperationSummary:
    """Track and format operation summaries for workflow reporting."""

    def __init__(self, operation: str):
        """Initialize operation summary tracker.

        Args:
            operation: The operation being performed (e.g., 'update-issues', 'copilot-tickets')
        """
        self.operation = operation
        self.issues_created = []
        self.issues_updated = []
        self.issues_closed = []
        self.issues_deleted = []
        self.comments_added = []
        self.duplicates_closed = []
        self.alerts_processed = []
        self.files_processed = []
        self.files_archived = []
        self.permalinks_updated = []
        self.errors = []
        self.warnings = []
        self.successes = []  # For dry-run operations

    def add_issue_created(self, issue_number: int, title: str, url: str):
        """Record an issue creation."""
        self.issues_created.append({"number": issue_number, "title": title, "url": url})

    def add_issue_updated(self, issue_number: int, title: str, url: str):
        """Record an issue update."""
        self.issues_updated.append({"number": issue_number, "title": title, "url": url})

    def add_issue_closed(self, issue_number: int, title: str, url: str):
        """Record an issue closure."""
        self.issues_closed.append({"number": issue_number, "title": title, "url": url})

    def add_issue_deleted(self, issue_number: int, title: str):
        """Record an issue deletion."""
        self.issues_deleted.append({"number": issue_number, "title": title})

    def add_comment(self, issue_number: int, comment_url: str):
        """Record a comment addition."""
        self.comments_added.append({"issue_number": issue_number, "url": comment_url})

    def add_duplicate_closed(self, issue_number: int, title: str, url: str):
        """Record a duplicate issue closure."""
        self.duplicates_closed.append(
            {"number": issue_number, "title": title, "url": url}
        )

    def add_alert_processed(
        self, alert_id: str, title: str, issue_number: int = None, issue_url: str = None
    ):
        """Record a CodeQL alert processing."""
        self.alerts_processed.append(
            {
                "alert_id": alert_id,
                "title": title,
                "issue_number": issue_number,
                "issue_url": issue_url,
            }
        )

    def add_file_processed(self, file_path: str):
        """Record a file processing."""
        self.files_processed.append(file_path)

    def add_file_archived(self, file_path: str):
        """Record a file archival."""
        self.files_archived.append(file_path)

    def add_permalink_updated(self, file_path: str):
        """Record a permalink update."""
        self.permalinks_updated.append(file_path)

    def add_error(self, message: str):
        """Record an error."""
        self.errors.append(message)

    def add_warning(self, message: str):
        """Record a warning."""
        self.warnings.append(message)

    def add_success(self, message: str):
        """Record a success (for dry-run operations)."""
        self.successes.append(message)

    def get_total_changes(self) -> int:
        """Get total number of changes made."""
        return (
            len(self.issues_created)
            + len(self.issues_updated)
            + len(self.issues_closed)
            + len(self.issues_deleted)
            + len(self.comments_added)
            + len(self.duplicates_closed)
            + len(self.alerts_processed)
        )

    def print_summary(self):
        """Print a formatted summary of the operation."""
        print(f"\n🎯 {self.operation.upper()} OPERATION SUMMARY")
        print("=" * 50)

        total_changes = self.get_total_changes()
        if total_changes == 0:
            print("ℹ️  No changes made")
        else:
            print(f"✅ Total changes: {total_changes}")

        # Issues created
        if self.issues_created:
            print(f"\n📝 Issues Created ({len(self.issues_created)}):")
            for issue in self.issues_created:
                print(f"  • #{issue['number']}: {issue['title']}")
                print(f"    🔗 {issue['url']}")

        # Issues updated
        if self.issues_updated:
            print(f"\n🔄 Issues Updated ({len(self.issues_updated)}):")
            for issue in self.issues_updated:
                print(f"  • #{issue['number']}: {issue['title']}")
                print(f"    🔗 {issue['url']}")

        # Issues closed
        if self.issues_closed:
            print(f"\n✅ Issues Closed ({len(self.issues_closed)}):")
            for issue in self.issues_closed:
                print(f"  • #{issue['number']}: {issue['title']}")
                print(f"    🔗 {issue['url']}")

        # Issues deleted
        if self.issues_deleted:
            print(f"\n🗑️  Issues Deleted ({len(self.issues_deleted)}):")
            for issue in self.issues_deleted:
                print(f"  • #{issue['number']}: {issue['title']}")

        # Comments added
        if self.comments_added:
            print(f"\n💬 Comments Added ({len(self.comments_added)}):")
            for comment in self.comments_added:
                print(f"  • Issue #{comment['issue_number']}")
                print(f"    🔗 {comment['url']}")

        # Duplicates closed
        if self.duplicates_closed:
            print(f"\n🔁 Duplicates Closed ({len(self.duplicates_closed)}):")
            for issue in self.duplicates_closed:
                print(f"  • #{issue['number']}: {issue['title']}")
                print(f"    🔗 {issue['url']}")

        # Alerts processed
        if self.alerts_processed:
            print(f"\n🔒 CodeQL Alerts Processed ({len(self.alerts_processed)}):")
            for alert in self.alerts_processed:
                print(f"  • Alert {alert['alert_id']}: {alert['title']}")
                if alert["issue_number"]:
                    print(f"    📝 Created issue #{alert['issue_number']}")
                    print(f"    🔗 {alert['issue_url']}")

        # Files processed
        if self.files_processed:
            print(f"\n📄 Files Processed ({len(self.files_processed)}):")
            for file_path in self.files_processed:
                print(f"  • {file_path}")

        # Files archived
        if self.files_archived:
            print(f"\n📦 Files Archived ({len(self.files_archived)}):")
            for file_path in self.files_archived:
                print(f"  • {file_path}")

        # Permalinks updated
        if self.permalinks_updated:
            print(f"\n🔗 Permalink Files Updated ({len(self.permalinks_updated)}):")
            for file_path in self.permalinks_updated:
                print(f"  • {file_path}")

        # Warnings
        if self.warnings:
            print(f"\n⚠️  Warnings ({len(self.warnings)}):")
            for warning in self.warnings:
                print(f"  • {warning}")

        # Errors
        if self.errors:
            print(f"\n❌ Errors ({len(self.errors)}):")
            for error in self.errors:
                print(f"  • {error}")

        print("=" * 50)

    def export_github_summary(self) -> str:
        """Export a concise, deduplicated summary in Markdown for GitHub Actions step summary."""
        lines = [f"## 🎯 {self.operation.upper()} Operation Results", ""]
        total_changes = self.get_total_changes()
        if total_changes == 0:
            lines.append("ℹ️ **No changes made**\n")
        else:
            lines.append(f"✅ **Total changes: {total_changes}**\n")

        # Helper for Markdown tables
        def table(headers, rows):
            out = [
                "| " + " | ".join(headers) + " |",
                "| " + " | ".join(["---"] * len(headers)) + " |",
            ]
            for row in rows:
                out.append("| " + " | ".join(row) + " |")
            return "\n".join(out)

        # Issues Created
        if self.issues_created:
            lines.append(f"### 📝 Issues Created ({len(self.issues_created)})")
            rows = [
                [f"#{i['number']}", i["title"], f"[Link]({i['url']})"]
                for i in {i["number"]: i for i in self.issues_created}.values()
            ]
            lines.append(table(["Number", "Title", "Link"], rows))
            lines.append("")

        # Issues Updated
        if self.issues_updated:
            lines.append(f"### 🔄 Issues Updated ({len(self.issues_updated)})")
            rows = [
                [f"#{i['number']}", i["title"], f"[Link]({i['url']})"]
                for i in {i["number"]: i for i in self.issues_updated}.values()
            ]
            lines.append(table(["Number", "Title", "Link"], rows))
            lines.append("")

        # Issues Closed
        if self.issues_closed:
            lines.append(f"### ✅ Issues Closed ({len(self.issues_closed)})")
            rows = [
                [f"#{i['number']}", i["title"], f"[Link]({i['url']})"]
                for i in {i["number"]: i for i in self.issues_closed}.values()
            ]
            lines.append(table(["Number", "Title", "Link"], rows))
            lines.append("")

        # Issues Deleted
        if self.issues_deleted:
            lines.append(f"### 🗑️ Issues Deleted ({len(self.issues_deleted)})")
            rows = [
                [f"#{i['number']}", i["title"]]
                for i in {i["number"]: i for i in self.issues_deleted}.values()
            ]
            lines.append(table(["Number", "Title"], rows))
            lines.append("")

        # Comments Added
        if self.comments_added:
            lines.append(f"### 💬 Comments Added ({len(self.comments_added)})")
            # Deduplicate by (issue_number, url)
            seen = set()
            rows = []
            for c in self.comments_added:
                key = (c["issue_number"], c["url"])
                if key not in seen:
                    seen.add(key)
                    rows.append(
                        [f"#{c['issue_number']}", f"[Comment Link]({c['url']})"]
                    )
            lines.append(table(["Issue", "Comment"], rows))
            lines.append("")

        # Duplicates Closed
        if self.duplicates_closed:
            lines.append(f"### 🔁 Duplicates Closed ({len(self.duplicates_closed)})")
            rows = [
                [f"#{i['number']}", i["title"], f"[Link]({i['url']})"]
                for i in {i["number"]: i for i in self.duplicates_closed}.values()
            ]
            lines.append(table(["Number", "Title", "Link"], rows))
            lines.append("")

        # Alerts Processed
        if self.alerts_processed:
            lines.append(
                f"### 🔒 CodeQL Alerts Processed ({len(self.alerts_processed)})"
            )
            rows = []
            for a in {a["alert_id"]: a for a in self.alerts_processed}.values():
                if a.get("issue_number"):
                    rows.append(
                        [
                            a["alert_id"],
                            a["title"],
                            f"#{a['issue_number']}",
                            f"[Link]({a['issue_url']})",
                        ]
                    )
                else:
                    rows.append([a["alert_id"], a["title"], "", ""])
            lines.append(table(["Alert ID", "Title", "Issue", "Link"], rows))
            lines.append("")

        # Files Processed (summarize count, not full list)
        if self.files_processed:
            lines.append(f"### 📄 Files Processed: {len(set(self.files_processed))}")
        # Files Archived
        if self.files_archived:
            lines.append(f"### 📦 Files Archived: {len(set(self.files_archived))}")
        # Permalinks Updated
        if self.permalinks_updated:
            lines.append(
                f"### 🔗 Files with Updated Permalinks: {len(set(self.permalinks_updated))}"
            )

        # Warnings
        if self.warnings:
            lines.append(f"### ⚠️ Warnings ({len(self.warnings)})")
            for warning in set(self.warnings):
                lines.append(f"- {warning}")
            lines.append("")

        # Errors
        if self.errors:
            lines.append(f"### ❌ Errors ({len(self.errors)})")
            for error in set(self.errors):
                lines.append(f"- {error}")
            lines.append("")

        return "\n".join(lines)


class GitHubAPI:
    """GitHub API client with common functionality."""

    def __init__(self, token: str, repo: str):
        """
        Initialize GitHub API client.

        Args:
            token: GitHub personal access token
            repo: Repository in owner/name format
        """
        self.token = token
        self.repo = repo
        self.headers = self._get_headers()

    def _get_headers(self) -> dict[str, str]:
        """Return HTTP headers for the GitHub API."""
        # Detect token type and set appropriate authorization header
        if self.token.startswith("github_pat_"):
            auth_header = f"token {self.token}"
        else:
            auth_header = f"Bearer {self.token}"

        return {
            "Authorization": auth_header,
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": API_VERSION,
        }

    def test_access(self) -> bool:
        """Test API access and permissions."""
        try:
            url = f"https://api.github.com/repos/{self.repo}"
            response = requests.get(url, headers=self.headers, timeout=10)

            if response.status_code == 401:
                print("Error: Invalid or expired GitHub token", file=sys.stderr)
                return False
            elif response.status_code == 404:
                print(
                    f"Error: Repository '{self.repo}' not found or not accessible",
                    file=sys.stderr,
                )
                return False

            response.raise_for_status()
            print("✓ GitHub API access verified")
            return True
        except requests.RequestException as e:
            print(f"Error testing GitHub API access: {e}", file=sys.stderr)
            return False

    def create_issue(
        self, title: str, body: str, labels: list[str] = None
    ) -> dict[str, Any] | None:
        """Create a new GitHub issue."""
        url = f"https://api.github.com/repos/{self.repo}/issues"
        data = {"title": title, "body": body}
        if labels:
            data["labels"] = labels

        try:
            response = requests.post(url, headers=self.headers, json=data, timeout=10)
            if response.status_code == 201:
                issue = response.json()
                print(f"Created issue #{issue['number']}: {title}")
                return issue
            else:
                print(
                    f"Failed to create issue: {response.status_code}", file=sys.stderr
                )
                print(response.text, file=sys.stderr)
                return None
        except requests.RequestException as e:
            print(f"Network error creating issue: {e}", file=sys.stderr)
            return None

    def update_issue(self, issue_number: int, **kwargs) -> bool:
        """Update an existing GitHub issue."""
        url = f"https://api.github.com/repos/{self.repo}/issues/{issue_number}"
        try:
            response = requests.patch(
                url, headers=self.headers, json=kwargs, timeout=10
            )
            if response.status_code == 200:
                print(f"Updated issue #{issue_number}")
                return True
            else:
                print(
                    f"Failed to update issue #{issue_number}: {response.status_code}",
                    file=sys.stderr,
                )
                print(response.text, file=sys.stderr)
                return False
        except requests.RequestException as e:
            print(f"Network error updating issue #{issue_number}: {e}", file=sys.stderr)
            return False

    def close_issue(self, issue_number: int, state_reason: str = "completed") -> bool:
        """Close an issue."""
        return self.update_issue(
            issue_number, state="closed", state_reason=state_reason
        )

    def add_comment(self, issue_number: int, body: str) -> bool:
        """Add a comment to an issue."""
        url = f"https://api.github.com/repos/{self.repo}/issues/{issue_number}/comments"
        try:
            response = requests.post(
                url, headers=self.headers, json={"body": body}, timeout=10
            )
            if response.status_code == 201:
                print(f"Added comment to issue #{issue_number}")
                return True
            else:
                print(
                    f"Failed to add comment to issue #{issue_number}: {response.status_code}",
                    file=sys.stderr,
                )
                print(response.text, file=sys.stderr)
                return False
        except requests.RequestException as e:
            print(
                f"Network error adding comment to issue #{issue_number}: {e}",
                file=sys.stderr,
            )
            return False

    def search_issues(self, query: str) -> list[dict[str, Any]]:
        """Search for issues using GitHub's search API with fallback to list API."""
        url = "https://api.github.com/search/issues"
        params = {"q": f"repo:{self.repo} {query}"}
        try:
            response = requests.get(
                url, headers=self.headers, params=params, timeout=10
            )
            if response.status_code == 403:
                # Search API forbidden, fall back to listing all issues and filtering
                print(
                    "⚠️  Search API access denied, falling back to issue listing",
                    file=sys.stderr,
                )
                return self._search_issues_fallback(query)
            response.raise_for_status()
            return response.json().get("items", [])
        except requests.RequestException as e:
            print(f"Network error searching for issues: {e}", file=sys.stderr)
            # Try fallback method
            return self._search_issues_fallback(query)

    def _search_issues_fallback(self, query: str) -> list[dict[str, Any]]:
        """Fallback method to search issues by listing all and filtering."""
        try:
            # Extract title from query if it contains 'in:title'
            if "in:title" in query and '"' in query:
                title_start = query.find('"')
                title_end = query.rfind('"')
                if title_start != -1 and title_end != -1 and title_start < title_end:
                    target_title = query[title_start + 1 : title_end]

                    # Get all issues and filter by title
                    all_issues = self.get_all_issues(state="all")
                    matching_issues = []
                    for issue in all_issues:
                        if target_title.lower() in issue.get("title", "").lower():
                            matching_issues.append(issue)
                    return matching_issues

            # For other queries, return empty list
            return []
        except Exception as e:
            print(f"Error in fallback search: {e}", file=sys.stderr)
            return []

    def get_all_issues(self, state: str = "open") -> list[dict[str, Any]]:
        """Fetch all issues with pagination support."""
        all_issues = []
        page = 1
        per_page = 100

        while True:
            url = f"https://api.github.com/repos/{self.repo}/issues"
            params = {"state": state, "per_page": per_page, "page": page}

            try:
                response = requests.get(
                    url, headers=self.headers, params=params, timeout=10
                )
                response.raise_for_status()

                issues = response.json()
                if not issues:
                    break

                # Filter out pull requests
                issues = [issue for issue in issues if "pull_request" not in issue]
                all_issues.extend(issues)

                if len(issues) < per_page:
                    break

                page += 1
            except requests.RequestException as e:
                print(f"Error fetching issues page {page}: {e}", file=sys.stderr)
                break

        return all_issues

    def get_issue(self, issue_number: int) -> dict[str, Any] | None:
        """
        Fetch a single issue by its number.

        Args:
            issue_number: The issue number to fetch

        Returns:
            Issue data as a dictionary, or None if not found or error occurred
        """
        url = f"https://api.github.com/repos/{self.repo}/issues/{issue_number}"
        try:
            response = requests.get(url, headers=self.headers, timeout=10)
            if response.status_code == 200:
                return response.json()
            elif response.status_code == 404:
                print(f"Issue #{issue_number} not found", file=sys.stderr)
                return None
            else:
                print(
                    f"Failed to fetch issue #{issue_number}: {response.status_code}",
                    file=sys.stderr,
                )
                print(response.text, file=sys.stderr)
                return None
        except requests.RequestException as e:
            print(f"Network error fetching issue #{issue_number}: {e}", file=sys.stderr)
            return None

    def get_codeql_alerts(self, state: str = "open") -> list[dict[str, Any]]:
        """Fetch every CodeQL security alert, following pagination.

        This used to issue a single request with per_page=100 and return the
        first page. Measured 2026-08-24 against this repo: 327 open alerts, so
        it returned 100 and process_codeql_alerts filed issues for 100 while
        printing "Found 100 open CodeQL alerts". A truncated list still
        produces a plausible number, which is why nobody noticed.

        Same page-walk as get_all_issues above -- a short page ends the walk,
        because the API cannot hand back fewer than per_page items unless it
        has run out.
        """
        all_alerts: list[dict[str, Any]] = []
        page = 1
        per_page = 100

        while True:
            url = f"https://api.github.com/repos/{self.repo}/code-scanning/alerts"
            params = {"state": state, "per_page": per_page, "page": page}

            try:
                response = requests.get(
                    url, headers=self.headers, params=params, timeout=10
                )
                response.raise_for_status()

                alerts = response.json()
                if not alerts:
                    break

                all_alerts.extend(alerts)

                if len(alerts) < per_page:
                    break

                page += 1
            except requests.RequestException as e:
                # Return what we have rather than nothing: a partial alert list
                # still files real issues, whereas [] silently reports "no
                # alerts" and looks identical to a clean scan.
                print(
                    f"Error fetching CodeQL alerts (page {page}): {e}",
                    file=sys.stderr,
                )
                break

        return all_alerts


class IssueUpdateProcessor:
    """Processes issue updates from issue_updates.json."""

    def __init__(self, github_api: GitHubAPI, dry_run: bool = False):
        # Use a mapping from guid to (repo, issue_number) for multi-repo support
        self.api = github_api
        self.repo = github_api.repo  # Fix: add repo attribute for compatibility
        self.summary = OperationSummary("Issue Update Processing")
        self.guid_issue_map: dict[str, tuple[str, int]] = {}
        self.dry_run = dry_run

        # Load configuration from environment variables
        self.enable_duplicate_prevention = (
            os.getenv("ENABLE_DUPLICATE_PREVENTION", "true").lower() == "true"
        )
        self.enable_duplicate_closure = (
            os.getenv("ENABLE_DUPLICATE_CLOSURE", "true").lower() == "true"
        )
        self.duplicate_prevention_method = os.getenv(
            "DUPLICATE_PREVENTION_METHOD", "guid_and_title"
        )
        self.max_duplicate_check_issues = int(
            os.getenv("MAX_DUPLICATE_CHECK_ISSUES", "1000")
        )

    def process_updates(
        self,
        updates_file: str = "issue_updates.json",
        updates_directory: str = ".github/issue-updates",
    ) -> bool:
        """
        Process issue updates from both legacy JSON file and new distributed directory format
        with GUID tracking and proper file state management.

        Args:
            updates_file: Path to legacy issue updates file
            updates_directory: Path to directory containing individual update files

        Returns:
            True if any updates were processed, False otherwise
        """
        all_updates = []
        processed_files = []

        # Check if legacy file has been processed before
        legacy_already_processed = self._is_legacy_file_processed(updates_file)

        # Load legacy file if it exists and hasn't been processed
        if not legacy_already_processed:
            legacy_updates = self._load_legacy_file(updates_file)
            if legacy_updates:
                all_updates.extend(legacy_updates)
                print(
                    f"📄 Loaded {len(legacy_updates)} NEW updates from legacy file: {updates_file}"
                )
        else:
            print(f"📄 Legacy file {updates_file} already processed, skipping")

        # Load distributed files from directory (only unprocessed files)
        distributed_updates, update_files = self._load_distributed_files(
            updates_directory
        )
        if distributed_updates:
            all_updates.extend(distributed_updates)
            processed_files.extend(update_files)
            print(
                f"📁 Loaded {len(distributed_updates)} updates from {len(update_files)} files in: {updates_directory}"
            )

        if not all_updates:
            print("📝 No new updates to process")
            return True

        # Process create actions first so later updates can reference issue numbers
        create_updates = [u for u in all_updates if u.get("action") == "create"]
        other_updates = [u for u in all_updates if u.get("action") != "create"]
        ordered_updates = create_updates + other_updates

        print(f"🚀 Processing {len(all_updates)} total updates...")
        success_count = 0

        for i, update in enumerate(ordered_updates, 1):
            action = update.get("action", "unknown")
            source = update.get("_source_file", "unknown")
            guid = update.get("guid", "no-guid")
            print(
                f"\n📋 Update {i}/{len(ordered_updates)}: {action} (from {source}, guid: {guid})"
            )

            result = self._process_single_update(update)
            if result:
                success_count += 1
            else:
                print(f"❌ Failed to process update {i}")

        print(f"\n✅ Successfully processed {success_count}/{len(all_updates)} updates")

        # Move processed distributed files to processed subdirectory (skip in dry-run)
        if not self.dry_run and processed_files and success_count > 0:
            for file_path in processed_files:
                self._fill_numbers_in_file(file_path)
            self._archive_processed_files(processed_files, updates_directory)
        elif self.dry_run and processed_files:
            print(
                f"[DRY RUN] Would move {len(processed_files)} files to processed/ directory"
            )

        # Mark legacy file as processed if we processed it successfully (skip in dry-run)
        if not self.dry_run and not legacy_already_processed and success_count > 0:
            # Check if we had legacy updates to process
            legacy_updates = self._load_legacy_file(updates_file)
            if legacy_updates:
                self._mark_legacy_file_processed(updates_file)
        elif self.dry_run and not legacy_already_processed:
            legacy_updates = self._load_legacy_file(updates_file)
            if legacy_updates:
                print(f"[DRY RUN] Would mark legacy file {updates_file} as processed")

        # Add file tracking to summary
        if processed_files:
            for file_path in processed_files:
                self.summary.add_file_processed(file_path)

        # Print operation summary
        self.summary.print_summary()

        # Export summary for GitHub Actions
        github_summary = self.summary.export_github_summary()
        summary_file = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary_file:
            try:
                with open(summary_file, "a", encoding="utf-8") as f:
                    f.write(github_summary + "\n")
            except Exception as e:
                print(f"⚠️  Failed to write to GitHub step summary: {e}")

        return success_count > 0

    def _update_file_with_permalinks(
        self,
        updates_file: str,
        original_data: dict[str, Any],
        permalinks: list[dict[str, Any]],
        format_type: str,
    ) -> None:
        """Update the issue updates file with permalinks to processed issues."""
        try:
            # Add processing metadata
            if format_type == "grouped":
                # Add a processed section to track what was done
                if "processed" not in original_data:
                    original_data["processed"] = []

                # Add new processed items
                for permalink_info in permalinks:
                    original_data["processed"].append(
                        {
                            "timestamp": permalink_info.get("timestamp"),
                            "action": permalink_info.get("action"),
                            "guid": permalink_info.get("guid"),
                            "issue_number": permalink_info.get("issue_number"),
                            "permalink": permalink_info.get("permalink"),
                            "workflow_run": os.environ.get("GITHUB_RUN_ID", "unknown"),
                        }
                    )
            else:
                # For flat format, add a simple processed list
                if not isinstance(original_data, dict):
                    original_data = {"updates": original_data, "processed": []}

                if "processed" not in original_data:
                    original_data["processed"] = []

                for permalink_info in permalinks:
                    original_data["processed"].append(permalink_info)

            # Write updated file
            with open(updates_file, "w", encoding="utf-8") as f:
                json.dump(original_data, f, indent=2)

            print(f"🔗 Updated {updates_file} with {len(permalinks)} permalinks")

        except Exception as e:
            print(
                f"⚠️  Failed to update {updates_file} with permalinks: {e}",
                file=sys.stderr,
            )

    def _load_legacy_file(self, updates_file: str) -> list[dict[str, Any]]:
        """Load updates from the legacy issue_updates.json file."""
        if not os.path.exists(updates_file):
            return []

        try:
            with open(updates_file, encoding="utf-8") as f:
                updates_data = json.load(f)
        except (OSError, json.JSONDecodeError) as e:
            print(f"❌ Error reading {updates_file}: {e}", file=sys.stderr)
            return []

        updates = []

        # Handle both old flat format and new grouped format
        if isinstance(updates_data, list):
            # Old flat format - items already have action property
            print("⚠️  Using legacy flat format. Consider upgrading to grouped format.")
            updates = updates_data
        else:
            # New grouped format - process in order: create, update, comment, close, delete
            for action_type in ["create", "update", "comment", "close", "delete"]:
                if action_type in updates_data and updates_data[action_type]:
                    for item in updates_data[action_type]:
                        item["action"] = action_type
                        updates.append(item)

        # Add source file information for tracking
        for update in updates:
            update["_source_file"] = updates_file

        return updates

    def _load_distributed_files(
        self, updates_directory: str
    ) -> tuple[list[dict[str, Any]], list[str]]:
        """
        Load updates from individual JSON files in the updates directory.

        Returns:
            Tuple of (updates_list, processed_files_list)
        """
        if not os.path.exists(updates_directory):
            return [], []

        updates = []
        processed_files = []

        try:
            # Find all JSON files except README.json
            json_files = []
            for filename in os.listdir(updates_directory):
                if filename.endswith(".json") and filename != "README.json":
                    file_path = os.path.join(updates_directory, filename)
                    if os.path.isfile(file_path):
                        json_files.append(file_path)

            json_files.sort()  # Process in consistent order

            for file_path in json_files:
                try:
                    with open(file_path, encoding="utf-8") as f:
                        try:
                            data = json.load(f)
                        except json.JSONDecodeError as e:
                            self.summary.add_error(f"Error reading {file_path}: {e}")
                            continue

                    # Handle both single objects and arrays of operations
                    if isinstance(data, list):
                        for item in data:
                            item["_source_file"] = file_path
                            updates.append(item)
                    elif isinstance(data, dict):
                        data["_source_file"] = file_path
                        updates.append(data)
                    else:
                        self.summary.add_error(f"Invalid update format in {file_path}")
                        continue
                    processed_files.append(file_path)
                except (OSError, json.JSONDecodeError) as e:
                    self.summary.add_error(f"Error reading {file_path}: {e}")
                    continue

        except OSError as e:
            print(
                f"⚠️  Error accessing directory {updates_directory}: {e}",
                file=sys.stderr,
            )
            return [], []

        return updates, processed_files

    def _archive_processed_files(
        self, processed_files: list[str], updates_directory: str
    ) -> None:
        """Move processed files to a 'processed' subdirectory."""
        if not processed_files:
            return

        processed_dir = os.path.join(updates_directory, "processed")

        try:
            # Create processed directory if it doesn't exist
            os.makedirs(processed_dir, exist_ok=True)

            # Move each processed file
            for file_path in processed_files:
                if os.path.exists(file_path):
                    filename = os.path.basename(file_path)
                    destination = os.path.join(processed_dir, filename)

                    # If destination exists, add timestamp to avoid conflicts
                    if os.path.exists(destination):
                        import time

                        timestamp = int(time.time())
                        name, ext = os.path.splitext(filename)
                        destination = os.path.join(
                            processed_dir, f"{name}_{timestamp}{ext}"
                        )

                    os.rename(file_path, destination)
                    print(f"📦 Moved {filename} to processed/")

        except OSError as e:
            print(f"⚠️  Error archiving processed files: {e}", file=sys.stderr)

    def _fill_numbers_in_file(self, file_path: str) -> None:
        """Update actions with parent GUIDs using resolved issue numbers."""
        try:
            with open(file_path, encoding="utf-8") as f:
                data = json.load(f)
        except Exception as e:
            self.summary.add_error(f"Failed to read file {file_path}: {e}")
            return

        modified = False

        def update_action(action: dict[str, Any]) -> None:
            parent_guid = action.get("parent_guid")
            if parent_guid:
                mapping = self.guid_issue_map.get(parent_guid)
                if mapping:
                    repo, number = mapping
                    action["number"] = number
                    action["repo"] = repo
            if action.get("number") and not action.get("issue_url"):
                repo = action.get("repo", self.api.repo)
                number = action.get("number")
                action["issue_url"] = f"https://github.com/{repo}/issues/{number}"

        if isinstance(data, list):
            for item in data:
                if isinstance(item, dict) and "action" in item:
                    update_action(item)
        elif isinstance(data, dict):
            if "action" in data:
                update_action(data)
            else:
                for v in data.values():
                    if isinstance(v, dict) and "action" in v:
                        update_action(v)
                    elif isinstance(v, list):
                        for item in v:
                            if isinstance(item, dict) and "action" in item:
                                update_action(item)

        if modified:
            try:
                with open(file_path, "w", encoding="utf-8") as f:
                    json.dump(data, f, indent=2)
            except Exception as e:
                self.summary.add_error(f"Failed to write file {file_path}: {e}")

    def _is_legacy_file_processed(self, updates_file: str) -> bool:
        """Check if legacy file has been processed before."""
        processed_marker = f"{updates_file}.processed"
        return os.path.exists(processed_marker)

    def _mark_legacy_file_processed(self, updates_file: str) -> None:
        """Mark legacy file as processed."""
        processed_marker = f"{updates_file}.processed"
        try:
            with open(processed_marker, "w", encoding="utf-8") as f:
                f.write(f"Processed on {os.environ.get('GITHUB_RUN_ID', 'unknown')}\n")
            print(f"🏷️  Marked {updates_file} as processed")
        except Exception as e:
            print(f"⚠️  Failed to mark {updates_file} as processed: {e}")

    def _check_guid_in_existing_issues(self, guid: str) -> dict[str, Any] | None:
        """Check if an issue with the given GUID already exists."""
        if not guid:
            return None

        try:
            # Search in all issues (open and closed) for the GUID
            all_issues = self.api.get_all_issues(state="all")
            guid_marker = f"<!-- guid:{guid} -->"

            for issue in all_issues:
                if guid_marker in issue.get("body", ""):
                    print(
                        f"🔍 Found existing issue #{issue['number']} with GUID: {guid}"
                    )
                    return issue

            return None
        except Exception as e:
            print(f"⚠️  Error checking for GUID {guid}: {e}")
            return None

    def _extract_guids(self, update: dict[str, Any]) -> tuple[str | None, str | None]:
        """Extract both primary GUID (UUID) and legacy GUID from update data.

        Returns:
            Tuple of (primary_guid, legacy_guid) where either may be None
        """
        primary_guid = update.get("guid")
        legacy_guid = update.get("legacy_guid")

        # Handle legacy format where only one GUID field exists
        if primary_guid and not legacy_guid:
            # Check if the guid field contains a UUID format
            import re

            uuid_pattern = (
                r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
            )
            if re.match(uuid_pattern, primary_guid, re.IGNORECASE):
                # It's already a UUID, keep as primary
                pass
            else:
                # It's a legacy GUID, treat as legacy and clear primary
                legacy_guid = primary_guid
                primary_guid = None

        return primary_guid, legacy_guid

    def _check_duplicate_by_guids(
        self,
        primary_guid: str | None,
        legacy_guid: str | None,
        existing_issues: list[dict[str, Any]],
    ) -> dict[str, Any] | None:
        """Check if any existing issue matches either GUID.

        Returns:
            The matching issue dict if found, None otherwise
        """
        for issue in existing_issues:
            body = issue.get("body", "")

            # Check primary GUID first
            if primary_guid and f"<!-- guid:{primary_guid} -->" in body:
                return issue

            # Check legacy GUID as fallback
            if legacy_guid and f"<!-- guid:{legacy_guid} -->" in body:
                return issue

        return None

    def _process_single_update(self, update: dict[str, Any]) -> bool:
        """Process a single update action with dual-GUID tracking."""
        action = update.get("action")

        # Check for missing action field - this indicates a malformed file
        if action is None:
            self.summary.add_error(f"Missing action field in update: {update}")
            # Mark this file as malformed for proper handling
            source_file = update.get("_source_file")
            if source_file:
                self._mark_file_as_malformed(source_file, "Missing action field")
            return False

        primary_guid, legacy_guid = self._extract_guids(update)

        # Check for duplicate operations using either GUID
        guid_to_check = primary_guid or legacy_guid
        if guid_to_check and self._is_duplicate_operation(
            action, guid_to_check, update
        ):
            self.summary.add_warning(
                f"Duplicate operation for GUID {guid_to_check} skipped."
            )
            # Mark this file as already processed since it's a duplicate
            source_file = update.get("_source_file")
            if source_file:
                self._mark_file_as_processed(
                    source_file, f"Duplicate GUID: {guid_to_check}"
                )
            return False

        try:
            # Fix: Use self.api.repo everywhere instead of self.repo
            if action == "create":
                return self._create_issue(update)
            elif action == "update":
                return self._update_issue(update)
            elif action == "comment":
                # Defensive: check for valid issue number
                issue_number = update.get("number")
                if not issue_number or issue_number == 0:
                    self.summary.add_error(
                        f"No issue number found for comment: {update}"
                    )
                    return False
                return self._add_comment(update)
            elif action == "close":
                return self._close_issue(update)
            elif action == "delete":
                return self._delete_issue(update)
            else:
                self.summary.add_error(f"Unknown action: {action} in update: {update}")
                # Mark this file as malformed for unknown action
                source_file = update.get("_source_file")
                if source_file:
                    self._mark_file_as_malformed(
                        source_file, f"Unknown action: {action}"
                    )
                return False
        except Exception as e:
            self.summary.add_error(f"Error processing {action} action: {e}")
            return False

    def _mark_file_as_processed(self, file_path: str, reason: str) -> None:
        """Move a file to the processed directory with reason."""
        if not file_path or not os.path.exists(file_path):
            return

        try:
            updates_dir = os.path.dirname(file_path)
            processed_dir = os.path.join(updates_dir, "processed")
            os.makedirs(processed_dir, exist_ok=True)

            filename = os.path.basename(file_path)
            destination = os.path.join(processed_dir, filename)

            # If destination exists, add timestamp to avoid conflicts
            if os.path.exists(destination):
                import time

                timestamp = int(time.time())
                name, ext = os.path.splitext(filename)
                destination = os.path.join(processed_dir, f"{name}_{timestamp}{ext}")

            os.rename(file_path, destination)
            print(f"📦 Moved {filename} to processed/ (reason: {reason})")

        except OSError as e:
            print(f"⚠️  Error moving file to processed: {e}", file=sys.stderr)

    def _mark_file_as_malformed(self, file_path: str, reason: str) -> None:
        """Move a file to the malformed directory with reason."""
        if not file_path or not os.path.exists(file_path):
            return

        try:
            updates_dir = os.path.dirname(file_path)
            malformed_dir = os.path.join(updates_dir, "malformed")
            os.makedirs(malformed_dir, exist_ok=True)

            filename = os.path.basename(file_path)
            destination = os.path.join(malformed_dir, filename)

            # If destination exists, add timestamp to avoid conflicts
            if os.path.exists(destination):
                import time

                timestamp = int(time.time())
                name, ext = os.path.splitext(filename)
                destination = os.path.join(malformed_dir, f"{name}_{timestamp}{ext}")

            os.rename(file_path, destination)
            print(f"🚨 Moved {filename} to malformed/ (reason: {reason})")

        except OSError as e:
            print(f"⚠️  Error moving file to malformed: {e}", file=sys.stderr)

    def _is_duplicate_operation(
        self, action: str, guid: str, update: dict[str, Any]
    ) -> bool:
        """Check if an operation with the same GUID was already performed."""
        # Check for duplicates even in dry-run mode to provide accurate feedback

        if action == "comment":
            # For comments, check if GUID exists in issue comments
            issue_number = update.get("number")
            if issue_number:
                return self._comment_guid_exists(issue_number, guid, update.get("repo"))
        elif action == "create":
            # For creates, use comprehensive GUID checking across all issues
            primary_guid, legacy_guid = self._extract_guids(update)
            return not self._check_guid_uniqueness_for_duplicate_check(
                primary_guid, legacy_guid, update.get("repo")
            )

        # For update, close, delete - assume no duplicates for now
        return False

    def _comment_guid_exists(
        self, issue_number: int, guid: str, repo: str = None
    ) -> bool:
        """Check if a comment with the given GUID already exists on the issue."""
        try:
            api = (
                self.api
                if not repo or repo == self.api.repo
                else GitHubAPI(self.api.token, repo)
            )
            url = f"https://api.github.com/repos/{api.repo}/issues/{issue_number}/comments"
            response = requests.get(url, headers=api.headers, timeout=10)

            if response.status_code != 200:
                return False

            comments = response.json()

            # Check for GUID in HTML comments
            guid_marker = f"<!-- guid:{guid} -->"
            for comment in comments:
                if guid_marker in comment.get("body", ""):
                    return True

            return False

        except Exception as e:
            print(f"⚠️  Error checking for duplicate comment GUID: {e}")
            return False

    def _check_guid_uniqueness(
        self,
        guid: str,
        legacy_guid: str,
        action: str,
        context: dict[str, Any],
        repo: str = None,
    ) -> bool:
        """Check if a GUID is unique across all issues.

        Args:
            guid: Primary GUID (UUID format)
            legacy_guid: Legacy GUID for backward compatibility
            action: The action being performed (e.g., 'create')
            context: Additional context (e.g., title for create action)

        Returns:
            True if the GUID is unique (operation can proceed), False otherwise
        """
        # If neither GUID is provided, allow the operation (no uniqueness check)
        if not guid and not legacy_guid:
            return True

        # In dry-run mode, skip actual uniqueness checking
        if self.dry_run:
            return True

        try:
            api = (
                self.api
                if not repo or repo == self.api.repo
                else GitHubAPI(self.api.token, repo)
            )
            # Search for existing issues with either GUID
            all_issues = api.get_all_issues(state="all")

            # Check both GUIDs
            for issue in all_issues:
                body = issue.get("body", "")

                # Check primary GUID
                if guid and f"<!-- guid:{guid} -->" in body:
                    self.summary.add_error(
                        f"Duplicate GUID found: {guid} already exists in issue #{issue['number']}"
                    )
                    print(
                        f"❌ GUID {guid} already exists in issue #{issue['number']}: {issue['title']}"
                    )
                    return False

                # Check legacy GUID
                if legacy_guid and f"<!-- guid:{legacy_guid} -->" in body:
                    self.summary.add_error(
                        f"Duplicate legacy GUID found: {legacy_guid} already exists in issue #{issue['number']}"
                    )
                    print(
                        f"❌ Legacy GUID {legacy_guid} already exists in issue #{issue['number']}: {issue['title']}"
                    )
                    return False

            # GUIDs are unique
            return True

        except Exception as e:
            # On error, log but allow operation to proceed
            self.summary.add_warning(f"Could not verify GUID uniqueness: {e}")
            print(f"⚠️  Could not verify GUID uniqueness: {e}")
            return True

    def _check_guid_uniqueness_for_duplicate_check(
        self,
        guid: str,
        legacy_guid: str,
        repo: str = None,
    ) -> bool:
        """Check if a GUID is unique across all issues for duplicate detection.

        This version works even in dry-run mode to provide accurate feedback.

        Args:
            guid: Primary GUID (UUID format)
            legacy_guid: Legacy GUID for backward compatibility
            repo: Repository to check (defaults to self.api.repo)

        Returns:
            True if the GUID is unique (operation can proceed), False if duplicate exists
        """
        # If neither GUID is provided, allow the operation (no uniqueness check)
        if not guid and not legacy_guid:
            return True

        try:
            api = (
                self.api
                if not repo or repo == self.api.repo
                else GitHubAPI(self.api.token, repo)
            )
            # Search for existing issues with either GUID
            all_issues = api.get_all_issues(state="all")

            # Check both GUIDs
            for issue in all_issues:
                body = issue.get("body", "")

                # Check primary GUID
                if guid and f"<!-- guid:{guid} -->" in body:
                    if not self.dry_run:
                        self.summary.add_error(
                            f"Duplicate GUID found: {guid} already exists in issue #{issue['number']}"
                        )
                    print(
                        f"🔍 GUID {guid} already exists in issue #{issue['number']}: {issue['title']}"
                    )
                    return False

                # Check legacy GUID
                if legacy_guid and f"<!-- guid:{legacy_guid} -->" in body:
                    if not self.dry_run:
                        self.summary.add_error(
                            f"Duplicate legacy GUID found: {legacy_guid} already exists in issue #{issue['number']}"
                        )
                    print(
                        f"🔍 Legacy GUID {legacy_guid} already exists in issue #{issue['number']}: {issue['title']}"
                    )
                    return False

            # GUIDs are unique
            return True

        except Exception as e:
            # On error, log but allow operation to proceed
            if not self.dry_run:
                self.summary.add_warning(f"Could not verify GUID uniqueness: {e}")
            print(f"⚠️  Could not verify GUID uniqueness: {e}")
            return True

    def _create_guid_exists(
        self, guid: str, update: dict[str, Any], repo: str = None
    ) -> bool:
        """Check if an issue with the given GUID was already created."""
        title = unescape_json_string(update.get("title", ""))
        try:
            api = (
                self.api
                if not repo or repo == self.repo
                else GitHubAPI(self.api.token, repo)
            )
            # Search for existing issues with similar title
            existing = api.search_issues(f'is:issue in:title "{title}"')

            guid_marker = f"<!-- guid:{guid} -->"
            for issue in existing:
                if guid_marker in issue.get("body", ""):
                    return True

            return False

        except Exception:
            return False

    def _check_duplicate_by_title(self, title: str, api: GitHubAPI) -> bool:
        """Check if an issue with the same title already exists.

        Args:
            title: The issue title to check
            api: GitHub API instance for the target repository

        Returns:
            True if duplicate found, False if unique
        """
        try:
            # Search for issues with exact title match
            search_query = f'is:issue in:title "{title}"'
            existing_issues = api.search_issues(search_query)

            # Check for exact title matches (case-insensitive)
            title_lower = title.lower().strip()
            for issue in existing_issues[: self.max_duplicate_check_issues]:
                existing_title = issue.get("title", "").lower().strip()
                if existing_title == title_lower:
                    print(
                        f"🔍 Found duplicate title in issue #{issue['number']}: {issue['title']}"
                    )
                    return True

            return False

        except Exception as e:
            print(f"⚠️  Error checking for duplicate titles: {e}")
            return False

    def _create_issue(self, update: dict[str, Any]) -> bool:
        """Create a new issue with dual-GUID tracking and enhanced duplicate prevention."""
        title = unescape_json_string(update.get("title", ""))
        body = unescape_json_string(update.get("body", ""))
        labels = update.get("labels", [])
        guid = update.get("guid")
        legacy_guid = update.get("legacy_guid")
        repo = update.get("repo", self.api.repo)
        parent_issue = update.get("parent_issue")
        api = self.api if repo == self.api.repo else GitHubAPI(self.api.token, repo)

        if not title:
            self.summary.add_error("Missing title for create action")
            return False

        # Format body with parent issue reference if provided
        if parent_issue:
            parent_url = f"https://github.com/{repo}/issues/{parent_issue}"
            body = f"Sub-issue of #{parent_issue}\n\nParent issue: {parent_url}\n\n---\n\n{body}"
            if "sub-issue" not in labels:
                labels.append("sub-issue")

        # Enhanced duplicate prevention
        if self.enable_duplicate_prevention:
            if self.duplicate_prevention_method in ["guid_and_title", "guid_only"]:
                # Check for duplicate by GUID
                if not self._check_guid_uniqueness(
                    guid, legacy_guid, "create", {"title": title}, repo=repo
                ):
                    print(f"🚫 Duplicate GUID detected for issue: {title}")
                    return False

            if self.duplicate_prevention_method in ["guid_and_title", "title_only"]:
                # Check for duplicate by title
                if self._check_duplicate_by_title(title, api):
                    print(f"🚫 Duplicate title detected for issue: {title}")
                    return False

        guid_to_embed = guid or legacy_guid
        if guid_to_embed:
            body += f"\n\n<!-- guid:{guid_to_embed} -->"

        if self.dry_run:
            print(f"[DRY RUN] Would create issue in {repo}: {title}")
            print(f"[DRY RUN] Labels: {labels}")
            print(f"[DRY RUN] GUID: {guid_to_embed}")
            self.summary.add_success(f"[DRY RUN] Would create issue: {title}")
            return True

        print(f"Creating issue in {repo}: {title}")
        issue = api.create_issue(title, body, labels)

        if issue:
            self.summary.add_issue_created(
                issue["number"], issue["title"], issue["html_url"]
            )
            if guid:
                self.guid_issue_map[guid] = (repo, issue["number"])
            if legacy_guid:
                self.guid_issue_map[legacy_guid] = (repo, issue["number"])
            if parent_issue:
                # Add a comment to parent issue referencing this sub-issue
                parent_comment = f"Created sub-issue #{issue['number']}: {issue['html_url']}\n\n<!-- guid:{guid_to_embed} -->"
                parent_api = (
                    self.api
                    if repo == self.api.repo
                    else GitHubAPI(self.api.token, repo)
                )
                parent_api.add_comment(parent_issue, parent_comment)
            return True
        else:
            self.summary.add_error(f"Failed to create issue: {title}")
            return False

    def _update_issue(self, update: dict[str, Any]) -> bool:
        """Update an existing issue with dual-GUID tracking."""
        issue_number = update.get("number")
        repo = update.get("repo", self.api.repo)
        primary_guid, legacy_guid = self._extract_guids(update)
        parent_guid = update.get("parent_guid")
        if not issue_number and parent_guid:
            mapping = self.guid_issue_map.get(parent_guid)
            if mapping:
                repo, issue_number = mapping
        if not issue_number:
            self.summary.add_error(f"No issue number found for update: {update}")
            return False
        update_data = {
            k: v
            for k, v in update.items()
            if k not in ["action", "number", "guid", "legacy_guid", "permalink"]
        }

        # Unescape string fields that might contain escape sequences
        for field in ["title", "body"]:
            if field in update_data:
                update_data[field] = unescape_json_string(update_data[field])

        guid_to_embed = primary_guid or legacy_guid
        if guid_to_embed and "body" in update_data:
            update_data["body"] += f"\n\n<!-- guid:{guid_to_embed} -->"

        if self.dry_run:
            print(f"[DRY RUN] Would update issue #{issue_number} in {repo}")
            print(f"[DRY RUN] Update fields: {list(update_data.keys())}")
            print(f"[DRY RUN] GUID: {guid_to_embed}")
            self.summary.add_success(f"[DRY RUN] Would update issue #{issue_number}")
            return True

        api = (
            self.api
            if not repo or repo == self.api.repo
            else GitHubAPI(self.api.token, repo)
        )
        try:
            success = api.update_issue(issue_number, **update_data)
            if success:
                issue_url = f"https://github.com/{api.repo}/issues/{issue_number}"
                self.summary.add_issue_updated(
                    issue_number, update_data.get("title", ""), issue_url
                )
                return True
            else:
                self.summary.add_error(f"Failed to update issue #{issue_number}")
                return False
        except Exception as e:
            print(f"❌ Error updating issue #{issue_number}: {e}")
            self.summary.add_error(f"Error updating issue #{issue_number}: {e}")
            return False

    def _add_comment(self, update: dict[str, Any]) -> bool:
        """Add a comment to an issue with dual-GUID tracking."""
        issue_number = update.get("number")
        repo = update.get("repo", self.api.repo)
        body = unescape_json_string(update.get("body", ""))
        primary_guid, legacy_guid = self._extract_guids(update)

        # Handle parent resolution - check both "parent" and "parent_guid"
        parent_guid = update.get("parent_guid") or update.get("parent")
        if not issue_number and parent_guid:
            mapping = self.guid_issue_map.get(parent_guid)
            if mapping:
                repo, issue_number = mapping

        if not issue_number:
            self.summary.add_error(f"No issue number found for comment: {update}")
            return False
        if not body:
            self.summary.add_error(f"No body found for comment: {update}")
            return False
        guid_to_embed = primary_guid or legacy_guid
        if guid_to_embed:
            body += f"\n\n<!-- guid:{guid_to_embed} -->"

        if self.dry_run:
            print(f"[DRY RUN] Would add comment to issue #{issue_number} in {repo}")
            print(f"[DRY RUN] Comment body length: {len(body)} characters")
            print(f"[DRY RUN] GUID: {guid_to_embed}")
            self.summary.add_success(
                f"[DRY RUN] Would add comment to issue #{issue_number}"
            )
            return True

        api = (
            self.api
            if not repo or repo == self.api.repo
            else GitHubAPI(self.api.token, repo)
        )
        try:
            comment = api.add_comment(issue_number, body)
            if comment:
                comment_url = comment.get(
                    "html_url",
                    f"https://github.com/{repo}/issues/{issue_number}#issuecomment-{comment.get('id', '')}",
                )
                self.summary.add_comment(issue_number, comment_url)
                return True
            else:
                self.summary.add_error(
                    f"Failed to add comment to issue #{issue_number}"
                )
                return False
        except Exception as e:
            print(f"❌ Error adding comment to issue #{issue_number}: {e}")
            self.summary.add_error(
                f"Error adding comment to issue #{issue_number}: {e}"
            )
            return False

    def _close_issue(self, update: dict[str, Any]) -> bool:
        """Close an issue with dual-GUID tracking."""
        issue_number = update.get("number")
        repo = update.get("repo", self.api.repo)
        state_reason = update.get("state_reason", "completed")
        primary_guid, legacy_guid = self._extract_guids(update)
        if not issue_number:
            self.summary.add_error(f"No issue number found for close: {update}")
            return False

        if self.dry_run:
            print(f"[DRY RUN] Would close issue #{issue_number} in {repo}")
            print(f"[DRY RUN] State reason: {state_reason}")
            self.summary.add_success(f"[DRY RUN] Would close issue #{issue_number}")
            return True

        api = (
            self.api
            if not repo or repo == self.repo
            else GitHubAPI(self.api.token, repo)
        )
        try:
            # Get issue details before closing
            issue_data = api.get_issue(issue_number)
            title = (
                issue_data.get("title", f"Issue {issue_number}")
                if issue_data
                else f"Issue {issue_number}"
            )
            issue_url = f"https://github.com/{repo}/issues/{issue_number}"

            success = api.close_issue(issue_number, state_reason=state_reason)
            if success:
                self.summary.add_issue_closed(issue_number, title, issue_url)
                return True
            else:
                self.summary.add_error(f"Failed to close issue #{issue_number}")
                return False
        except Exception as e:
            print(f"❌ Error closing issue #{issue_number}: {e}")
            self.summary.add_error(f"Error closing issue #{issue_number}: {e}")
            return False

    def update_permalinks(
        self,
        updates_file: str = "issue_updates.json",
        updates_directory: str = ".github/issue-updates",
    ) -> bool:
        """
        Update issue files with permalinks to processed issues.

        This method searches for processed issues and updates the individual JSON files
        with permalink fields directly in each action object that has a GUID.

        Args:
            updates_file: Path to legacy issue updates file (not used for permalink updates)
            updates_directory: Path to directory containing individual update files

        Returns:
            True if any permalinks were updated, False otherwise
        """
        print("🔗 Updating permalinks for processed issues...")

        updated_count = 0

        # Process distributed files in processed directory
        if os.path.exists(updates_directory):
            processed_dir = os.path.join(updates_directory, "processed")
            if os.path.exists(processed_dir):
                try:
                    json_files = [
                        f
                        for f in os.listdir(processed_dir)
                        if f.endswith(".json") and f != "README.json"
                    ]

                    print(f"� Found {len(json_files)} processed files to update")

                    for filename in json_files:
                        file_path = os.path.join(processed_dir, filename)

                        with open(file_path, encoding="utf-8") as f:
                            file_data = json.load(f)

                        # Track if this file was modified
                        file_modified = False

                        # Handle both single objects and arrays of operations
                        if isinstance(file_data, list):
                            for action in file_data:
                                if self._update_action_permalink(action):
                                    file_modified = True
                                    updated_count += 1
                        elif isinstance(file_data, dict) and "action" in file_data:
                            if self._update_action_permalink(file_data):
                                file_modified = True
                                updated_count += 1

                        # Write back the file if it was modified
                        if file_modified:
                            with open(file_path, "w", encoding="utf-8") as f:
                                json.dump(file_data, f, indent=2)
                            print(f"📝 Updated permalinks in {filename}")

                except Exception as e:
                    print(f"⚠️  Error processing processed files: {e}")

        if updated_count > 0:
            print(f"✅ Updated {updated_count} permalink entries")
            return True
        else:
            print("📝 No permalink updates needed")
            return False

    def _update_action_permalink(self, action: dict[str, Any]) -> bool:
        """
        Update a single action with permalink information if it has a GUID and no existing permalink.

        Args:
            action: The action object to potentially update

        Returns:
            True if the action was modified, False otherwise
        """
        # Skip if no GUID or already has permalink
        guid = action.get("guid")
        if not guid or "permalink" in action:
            return False

        # Try to find the issue for this action
        issue = self._find_issue_by_guid(guid, action.get("repo"))
        if issue:
            action["permalink"] = issue["html_url"]
            print(f"🔗 Added permalink for GUID {guid}: {issue['html_url']}")
            return True

        # For actions that reference an issue number, try to construct permalink
        issue_number = action.get("number")
        if issue_number:
            repo = action.get("repo", self.api.repo)
            # Construct the permalink directly since we know the issue number
            permalink = f"https://github.com/{repo}/issues/{issue_number}"
            action["permalink"] = permalink
            print(
                f"🔗 Added constructed permalink for issue #{issue_number}: {permalink}"
            )
            return True

        print(f"⚠️  Could not find issue for GUID {guid}")
        return False

    def _find_permalinks_for_updates(
        self, updates: list[dict[str, Any]]
    ) -> list[dict[str, Any]]:
        """
        Find permalinks for a list of updates by searching for matching issues.

        Args:
            updates: List of update operations

        Returns:
            List of permalink information dictionaries
        """
        permalinks = []

        for update in updates:
            action = update.get("action", "unknown")
            guid = update.get("guid")
            title = update.get("title", "")

            # Try to find the corresponding issue
            issue = None

            if guid:
                # Search by GUID first
                issue = self._find_issue_by_guid(guid)

            if not issue and title and action == "create":
                # Fall back to title search for create operations
                issues = self.api.search_issues(f'is:issue in:title "{title}"')
                if issues:
                    issue = issues[0]

            if issue:
                permalink_info = {
                    "timestamp": issue.get("created_at"),
                    "action": action,
                    "guid": guid,
                    "issue_number": issue["number"],
                    "permalink": issue["html_url"],
                    "title": issue["title"],
                }
                permalinks.append(permalink_info)
                print(f"🔍 Found issue #{issue['number']} for {action} operation")

        return permalinks

    def _find_permalinks_for_file_updates(self, file_data: Any) -> list[dict[str, Any]]:
        """
        Find permalinks for updates in a single file.

        Args:
            file_data: The JSON data from a processed update file

        Returns:
            List of permalink information dictionaries
        """
        updates = []

        if isinstance(file_data, list):
            updates = file_data
        elif isinstance(file_data, dict) and "action" in file_data:
            updates = [file_data]

        return self._find_permalinks_for_updates(updates)

    def _find_issue_by_guid(self, guid: str, repo: str = None) -> dict[str, Any] | None:
        """
        Find an issue by its GUID marker in the body.

        Args:
            guid: The GUID to search for

        Returns:
            Issue data if found, None otherwise
        """
        if not guid:
            return None

        try:
            api = (
                self.api
                if not repo or repo == self.repo
                else GitHubAPI(self.api.token, repo)
            )
            # Search all issues for the GUID marker
            all_issues = api.get_all_issues(state="all")
            guid_marker = f"<!-- guid:{guid} -->"

            for issue in all_issues:
                if guid_marker in issue.get("body", ""):
                    return issue

            return None
        except Exception as e:
            print(f"⚠️  Error searching for GUID {guid}: {e}")
            return None

    def _has_permalink_metadata(self, file_data: Any) -> bool:
        """
        Check if a file already has permalink metadata.

        Args:
            file_data: The JSON data from a file

        Returns:
            True if permalink metadata is present
        """
        if isinstance(file_data, dict):
            return "permalink" in file_data or "processed_at" in file_data
        return False

    def _add_permalink_metadata(
        self, file_path: str, file_data: Any, permalinks: list[dict[str, Any]]
    ) -> None:
        """
        Add permalink metadata to a processed file.

        Args:
            file_path: Path to the file to update
            file_data: Current file data
            permalinks: List of permalink information
        """
        try:
            # Add metadata to the file
            if isinstance(file_data, dict):
                file_data["_permalink_metadata"] = {
                    "processed_at": os.environ.get("GITHUB_RUN_ID", "manual"),
                    "permalinks": permalinks,
                }
            elif isinstance(file_data, list) and len(file_data) == 1:
                # Single item array, convert to object with metadata
                file_data = {
                    "update": file_data[0],
                    "_permalink_metadata": {
                        "processed_at": os.environ.get("GITHUB_RUN_ID", "manual"),
                        "permalinks": permalinks,
                    },
                }

            # Write updated file
            with open(file_path, "w", encoding="utf-8") as f:
                json.dump(file_data, f, indent=2)

            print(f"🔗 Added permalink metadata to {os.path.basename(file_path)}")

        except Exception as e:
            print(f"⚠️  Failed to add permalink metadata to {file_path}: {e}")

    def _delete_issue(self, update: dict[str, Any]) -> bool:
        """
        Delete an issue (requires GraphQL API and admin permissions).

        Note: Issue deletion requires admin permissions on the repository.
        For most GitHub tokens, this will fall back to closing the issue instead.
        Only repository owners or users with admin collaborator permissions can delete issues.
        """
        issue_number = update.get("number")
        repo = update.get("repo", self.api.repo)
        api = self.api if repo == self.api.repo else GitHubAPI(self.api.token, repo)

        if not issue_number:
            print("Delete action missing issue number", file=sys.stderr)
            self.summary.add_error("Delete action missing issue number")
            return False

        if self.dry_run:
            print(f"[DRY RUN] Would delete issue #{issue_number} in {repo}")
            print(
                "[DRY RUN] Note: Requires admin permissions, would fallback to close if needed"
            )
            self.summary.add_success(f"[DRY RUN] Would delete issue #{issue_number}")
            return True

        # Get issue data before deletion for summary
        try:
            issue_data = api.get_issue(issue_number)
            title = (
                issue_data.get("title", f"Issue {issue_number}")
                if issue_data
                else f"Issue {issue_number}"
            )
        except Exception:
            title = f"Issue {issue_number}"

        print(f"🎯 Processing delete request for issue #{issue_number}: {title}")
        print(
            "   Note: Issue deletion requires admin permissions. Will fallback to closing if needed."
        )

        # Get node_id for GraphQL deletion
        try:
            print(f"🔍 Getting node_id for issue #{issue_number}...")
            url = f"https://api.github.com/repos/{repo}/issues/{issue_number}"
            response = requests.get(url, headers=api.headers, timeout=10)
            response.raise_for_status()
            issue_data = response.json()
            node_id = issue_data["node_id"]
            print(f"📋 Found node_id: {node_id}")

            # Check if issue is already closed/locked (might affect deletion)
            issue_state = issue_data.get("state", "unknown")
            issue_locked = issue_data.get("locked", False)
            print(f"📋 Issue state: {issue_state}, locked: {issue_locked}")

            # Use GraphQL to delete
            print(f"🗑️  Attempting to delete issue #{issue_number} via GraphQL...")
            mutation = {
                "query": f'mutation{{deleteIssue(input:{{issueId:"{node_id}"}}){{clientMutationId}}}}'
            }

            print(f"📤 GraphQL mutation: {mutation}")

            response = requests.post(
                "https://api.github.com/graphql",
                headers=api.headers,
                json=mutation,
                timeout=10,
            )

            print(f"📥 HTTP status code: {response.status_code}")

            if response.status_code == 200:
                # Parse the GraphQL response to check for errors
                result = response.json()
                print(f"📥 GraphQL response: {result}")

                # Check for GraphQL errors
                if "errors" in result:
                    error_details = result["errors"]
                    print(f"❌ GraphQL errors: {error_details}", file=sys.stderr)

                    # Check for specific error types
                    for error in error_details:
                        error_message = str(error).lower()
                        if (
                            "permission" in error_message
                            or "forbidden" in error_message
                            or "unauthorized" in error_message
                        ):
                            print(
                                f"⚠️  Permission denied to delete issue #{issue_number}. This is normal for most GitHub tokens."
                            )
                            print(
                                "   Issues can usually only be deleted by repository owners with admin permissions."
                            )
                            print("   Falling back to closing the issue instead...")
                            return self._close_issue_as_fallback(update, title)
                        elif "not found" in error_message:
                            print(
                                f"⚠️  Issue #{issue_number} not found - it may have already been deleted or doesn't exist."
                            )
                            return True  # Consider this a success since the goal (issue removal) is achieved

                    error_msg = f"GraphQL errors deleting issue #{issue_number}: {error_details}"
                    self.summary.add_error(error_msg)
                    return False

                # Check if the mutation was successful
                if (
                    "data" in result
                    and result["data"]
                    and "deleteIssue" in result["data"]
                ):
                    print(f"✅ Successfully deleted issue #{issue_number}: {title}")
                    self.summary.add_issue_deleted(issue_number, title)
                    return True
                else:
                    error_msg = f"Unexpected GraphQL response deleting issue #{issue_number}: {result}"
                    print(error_msg, file=sys.stderr)
                    self.summary.add_error(error_msg)
                    return False
            else:
                print(f"📥 HTTP response: {response.status_code} - {response.text}")

                # Check if it's a permission error - if so, fallback to closing
                if response.status_code in [401, 403]:
                    print(
                        f"⚠️  HTTP {response.status_code} - Insufficient permissions to delete issue #{issue_number}."
                    )
                    print(
                        "   This usually means the token doesn't have admin rights to delete issues."
                    )
                    print("   Falling back to closing the issue instead...")
                    return self._close_issue_as_fallback(update, title)
                elif response.status_code == 404:
                    print(
                        f"⚠️  Issue #{issue_number} not found (HTTP 404) - it may have already been deleted."
                    )
                    return True  # Consider this a success since the goal is achieved

                error_msg = f"HTTP error deleting issue #{issue_number}: {response.status_code} - {response.text}"
                print(error_msg, file=sys.stderr)
                self.summary.add_error(error_msg)
                return False

        except requests.RequestException as e:
            error_msg = f"Error deleting issue #{issue_number}: {e}"
            print(error_msg, file=sys.stderr)
            self.summary.add_error(error_msg)
            return False

    def _close_issue_as_fallback(self, update: dict[str, Any], title: str) -> bool:
        """Close an issue as a fallback when deletion is not permitted."""
        issue_number = update.get("number")
        repo = update.get("repo", self.api.repo)
        api = self.api if repo == self.api.repo else GitHubAPI(self.api.token, repo)
        if not issue_number:
            return False

        try:
            if api.close_issue(issue_number, "not_planned"):
                issue_url = f"https://github.com/{repo}/issues/{issue_number}"
                print(
                    f"✅ Successfully closed issue #{issue_number} as fallback: {title}"
                )
                # Add to summary as closed rather than deleted
                self.summary.add_issue_closed(issue_number, title, issue_url)
                return True
            else:
                error_msg = f"Failed to close issue #{issue_number} as fallback"
                print(error_msg, file=sys.stderr)
                self.summary.add_error(error_msg)
                return False
        except Exception as e:
            error_msg = f"Error closing issue #{issue_number} as fallback: {e}"
            print(error_msg, file=sys.stderr)
            self.summary.add_error(error_msg)
            return False


class CopilotTicketManager:
    """Manages tickets for Copilot review comments."""

    def __init__(self, github_api: GitHubAPI):
        self.api = github_api
        self.summary = OperationSummary("copilot-tickets")

    def handle_event(self, event_name: str, event_data: dict[str, Any]) -> None:
        """Handle GitHub webhook events related to Copilot comments."""
        action = event_data.get("action")
        print(f"Processing {event_name} event with action: {action}")

        try:
            if event_name == "pull_request_review_comment":
                self._handle_review_comment(action, event_data)
            elif event_name == "pull_request_review":
                self._handle_review(action, event_data)
            elif event_name == "pull_request" and action == "closed":
                self._handle_pr_closed(event_data)
            elif event_name == "push":
                self._handle_push(event_data)
            else:
                print(f"Unhandled event: {event_name} with action: {action}")
        except Exception as e:
            print(f"Error handling {event_name} event: {e}", file=sys.stderr)
            self.summary.add_error(f"Error handling {event_name} event: {e}")
        finally:
            # Always print summary at the end
            self._print_summary()

    def _print_summary(self):
        """Print the operation summary."""
        self.summary.print_summary()

        # Export summary for GitHub Actions
        github_summary = self.summary.export_github_summary()
        summary_file = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary_file:
            try:
                with open(summary_file, "a", encoding="utf-8") as f:
                    f.write(github_summary + "\n")
            except Exception as e:
                print(f"⚠️  Failed to write to GitHub step summary: {e}")

    def _handle_review_comment(self, action: str, event_data: dict[str, Any]) -> None:
        """Handle review comment events."""
        comment = event_data.get("comment", {})

        if comment.get("user", {}).get("login") != COPILOT_USER:
            print("Not a Copilot comment; skipping")
            return

        if action == "created":
            self._create_or_update_ticket(comment)
        elif action == "deleted":
            self._handle_comment_deleted(comment)

    def _handle_review(self, action: str, event_data: dict[str, Any]) -> None:
        """Handle review events (minimal action currently)."""
        review = event_data.get("review", {})
        if review.get("user", {}).get("login") == COPILOT_USER:
            print(f"Copilot review {action}")

    def _handle_pr_closed(self, event_data: dict[str, Any]) -> None:
        """Close all Copilot tickets for a merged PR."""
        pr = event_data.get("pull_request", {})
        if not pr.get("merged", False):
            print("PR not merged, skipping")
            return

        pr_number = pr["number"]
        print(f"Processing merged PR #{pr_number}")

        # Search for all open copilot issues mentioning this PR
        issues = self.api.search_issues(f"label:{COPILOT_LABEL} state:open {pr_number}")
        print(f"Found {len(issues)} open Copilot issues for PR #{pr_number}")

        closed_count = 0
        for issue in issues:
            if self.api.close_issue(issue["number"]):
                closed_count += 1

        if closed_count > 0:
            print(f"Closed {closed_count} Copilot issues for merged PR #{pr_number}")

    def _handle_push(self, event_data: dict[str, Any]) -> None:
        """Handle pushes to main branch - comprehensive issue analysis."""
        ref = event_data.get("ref", "")
        if not ref.endswith("/main") and not ref.endswith("/master"):
            print(f"Push to {ref} - not main/master, skipping")
            return

        print(f"Processing push to {ref}")

        # Get all open Copilot issues and analyze them
        issues = self.api.search_issues(f"label:{COPILOT_LABEL} state:open")
        print(f"Found {len(issues)} open Copilot issues")

        # Here you could implement file change analysis and stale issue cleanup
        # For now, just log the activity
        print("Push analysis complete")

    def _create_or_update_ticket(self, comment: dict[str, Any]) -> None:
        """Create or update a ticket for a Copilot comment."""
        comment_body = comment.get("body", "").strip()
        key = comment_body.split("\n", 1)[0]  # First line as key

        existing = self.api.search_issues(f"label:{COPILOT_LABEL} state:open {key}")

        line_info = {
            "id": comment["id"],
            "path": comment.get("path", ""),
            "line": comment.get("line", 0),
            "url": comment.get("html_url", ""),
        }

        if existing:
            # Update existing issue
            issue = existing[0]
            print(f"Updating existing Copilot issue #{issue['number']}")
            # Implementation would parse existing body and update it
            self.summary.add_issue_updated(
                issue["number"], issue["title"], issue["html_url"]
            )
        else:
            # Create new issue
            title = f"Copilot Review: {key[:50]}..."
            body = self._build_ticket_body(comment, [line_info])
            result = self.api.create_issue(title, body, [COPILOT_LABEL])
            if result:
                self.summary.add_issue_created(
                    result["number"], title, result["html_url"]
                )
            else:
                self.summary.add_error(f"Failed to create Copilot ticket: {title}")

    def _handle_comment_deleted(self, comment: dict[str, Any]) -> None:
        """Handle deletion of a Copilot comment."""
        comment_id = comment["id"]
        search_key = f"id:{comment_id}"

        issues = self.api.search_issues(
            f"label:{COPILOT_LABEL} state:open {search_key}"
        )
        if not issues:
            print(f"No issue found for deleted comment {comment_id}")
            return

        issue = issues[0]
        # Implementation would update or close the issue based on remaining comments
        print(
            f"Handling deletion of comment {comment_id} from issue #{issue['number']}"
        )

    def _build_ticket_body(
        self, comment: dict[str, Any], lines: list[dict[str, Any]]
    ) -> str:
        """Build the issue body from comment text and metadata."""
        snippet = comment["body"]
        bullet_lines = [
            f"- id:{item['id']} [{item['path']}#L{item['line']}]({item['url']})"
            for item in lines
        ]
        data = {"comments": lines}
        json_block = escape_for_json_output(data)

        return (
            f"Generated from [Copilot review comment]({comment['url']}).\n\n"
            f"```text\n{snippet}\n```\n\n"
            + "\n".join(bullet_lines)
            + f"\n\n<!-- copilot-data:{json_block} -->"
        )


class DuplicateIssueManager:
    """Manages duplicate issue detection and closure with enhanced configuration."""

    def __init__(self, github_api: GitHubAPI):
        self.api = github_api
        self.summary = OperationSummary("close-duplicates")

        # Load configuration from environment variables
        self.enable_duplicate_closure = (
            os.getenv("ENABLE_DUPLICATE_CLOSURE", "true").lower() == "true"
        )
        self.max_duplicate_check_issues = int(
            os.getenv("MAX_DUPLICATE_CHECK_ISSUES", "1000")
        )

    def close_duplicates(self, dry_run: bool = False) -> int:
        """
        Close duplicate issues by title with enhanced configuration support.

        Args:
            dry_run: If True, only print what would be done

        Returns:
            Number of duplicate issues that were (or would be) closed
        """
        if not self.enable_duplicate_closure:
            print("🚫 Duplicate closure is disabled by configuration")
            self.summary.add_warning("Duplicate closure disabled by configuration")
            return 0

        print("Fetching open issues...")
        issues = self.api.get_all_issues(state="open")

        # Limit the number of issues checked to prevent API rate limiting
        if len(issues) > self.max_duplicate_check_issues:
            print(
                f"⚠️  Limiting duplicate check to {self.max_duplicate_check_issues} issues (found {len(issues)})"
            )
            issues = issues[: self.max_duplicate_check_issues]

        print(f"Found {len(issues)} open issues to check")

        # Group issues by title
        title_groups = self._group_by_title(issues)

        closed_count = 0
        duplicates_found = False

        for title, issue_list in title_groups.items():
            if len(issue_list) > 1:
                duplicates_found = True
                print(f"Found {len(issue_list)} issues with title: '{title}'")

                if dry_run:
                    self._print_duplicate_plan(issue_list)
                    closed_count += len(issue_list) - 1
                else:
                    closed_count += self._close_duplicate_group(issue_list)

        if not duplicates_found:
            print("✅ No duplicate issues found")
            self.summary.add_success("No duplicate issues found")

        # Print operation summary
        self.summary.print_summary()

        # Export summary for GitHub Actions
        github_summary = self.summary.export_github_summary()
        summary_file = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary_file:
            try:
                with open(summary_file, "a", encoding="utf-8") as f:
                    f.write(github_summary + "\n")
            except Exception as e:
                print(f"⚠️  Failed to write to GitHub step summary: {e}")

        return closed_count

    def _group_by_title(
        self, issues: list[dict[str, Any]]
    ) -> dict[str, list[dict[str, Any]]]:
        """Group issues by their title."""
        title_groups = defaultdict(list)

        for issue in issues:
            title = issue["title"].strip()
            title_groups[title].append(
                {"number": issue["number"], "title": title, "url": issue["html_url"]}
            )

        return title_groups

    def _close_duplicate_group(self, issue_list: list[dict[str, Any]]) -> int:
        """Close duplicate issues, keeping the lowest numbered one."""
        issue_list.sort(key=lambda x: x["number"])
        canonical = issue_list[0]
        duplicates = issue_list[1:]

        print(f"Keeping issue #{canonical['number']} as canonical")

        closed_count = 0
        for duplicate in duplicates:
            print(
                f"Closing issue #{duplicate['number']} as duplicate of #{canonical['number']}"
            )

            if self.api.close_issue(duplicate["number"]):
                # Add duplicate comment
                comment_body = f"Duplicate of #{canonical['number']}"
                self.api.add_comment(duplicate["number"], comment_body)
                # Record in summary
                self.summary.add_duplicate_closed(
                    duplicate["number"], duplicate["title"], duplicate["url"]
                )
                closed_count += 1

        return closed_count

    def _print_duplicate_plan(self, issue_list: list[dict[str, Any]]) -> None:
        """Print what would be done in dry-run mode."""
        issue_list.sort(key=lambda x: x["number"])
        canonical = issue_list[0]
        duplicates = issue_list[1:]

        print(f"  📌 Would keep issue #{canonical['number']} as canonical")

        for duplicate in duplicates:
            print(f"  🚫 Would close issue #{duplicate['number']} as duplicate")


class CodeQLAlertManager:
    """Manages CodeQL security alert tickets."""

    def __init__(self, github_api: GitHubAPI):
        """
        Initialize CodeQL alert manager.

        Args:
            github_api: GitHubAPI instance for repository operations
        """
        self.api = github_api
        self.summary = OperationSummary("codeql-alerts")

    def process_codeql_alerts(self, dry_run: bool = False) -> int:
        """
        Process CodeQL security alerts and create issues for new alerts.

        Args:
            dry_run: If True, only print what would be done

        Returns:
            Number of alerts processed
        """
        print("Fetching CodeQL security alerts...")
        alerts = self.api.get_codeql_alerts(state="open")
        print(f"Found {len(alerts)} open CodeQL alerts")

        if not alerts:
            print("No open CodeQL alerts found")
            return 0

        processed_count = 0

        for alert in alerts:
            if self._should_process_alert(alert):
                if dry_run:
                    self._print_alert_plan(alert)
                    processed_count += 1
                else:
                    if self._create_issue_for_alert(alert):
                        processed_count += 1

        print(f"Processed {processed_count} CodeQL alerts")

        # Print operation summary
        self.summary.print_summary()

        # Export summary for GitHub Actions
        github_summary = self.summary.export_github_summary()
        summary_file = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary_file:
            try:
                with open(summary_file, "a", encoding="utf-8") as f:
                    f.write(github_summary + "\n")
            except Exception as e:
                print(f"⚠️  Failed to write to GitHub step summary: {e}")

        return processed_count

    def _should_process_alert(self, alert: dict[str, Any]) -> bool:
        """
        Determine if an alert should be processed based on existing issues.

        Args:
            alert: CodeQL alert data

        Returns:
            True if alert should be processed
        """
        alert_id = str(alert.get("number", alert.get("id", "unknown")))

        # Check if issue already exists for this alert
        search_query = f"label:{CODEQL_LABEL} CodeQL Alert #{alert_id}"
        existing_issues = self.api.search_issues(search_query)

        if existing_issues:
            print(
                f"⏭️  Skipping alert #{alert_id} - issue already exists: #{existing_issues[0]['number']}"
            )
            return False

        return True

    def _create_issue_for_alert(self, alert: dict[str, Any]) -> bool:
        """
        Create a GitHub issue for a CodeQL security alert.

        Args:
            alert: CodeQL alert data

        Returns:
            True if issue was created successfully
        """
        alert_id = str(alert.get("number", alert.get("id", "unknown")))
        rule_id = alert.get("rule", {}).get("id", "unknown-rule")
        severity = alert.get("rule", {}).get("severity", "unknown")

        title = f"CodeQL Security Alert #{alert_id}: {rule_id}"
        body = self._build_alert_body(alert)
        labels = [CODEQL_LABEL, f"severity-{severity.lower()}"]

        try:
            issue = self.api.create_issue(title, body, labels)
            if issue:
                print(
                    f"✅ Created issue #{issue['number']} for CodeQL alert #{alert_id}"
                )
                self.summary.add_alert_processed(
                    alert_id, title, issue["number"], issue["html_url"]
                )
                return True
            else:
                print(f"❌ Failed to create issue for CodeQL alert #{alert_id}")
                self.summary.add_error(f"Failed to create issue for alert #{alert_id}")
                return False
        except Exception as e:
            print(f"❌ Error creating issue for alert #{alert_id}: {e}")
            self.summary.add_error(f"Error creating issue for alert #{alert_id}: {e}")
            return False

    def _build_alert_body(self, alert: dict[str, Any]) -> str:
        """
        Build the issue body from CodeQL alert data.

        Args:
            alert: CodeQL alert data

        Returns:
            Formatted issue body
        """
        alert_id = str(alert.get("number", alert.get("id", "unknown")))
        rule = alert.get("rule", {})
        rule_id = rule.get("id", "unknown-rule")
        rule_name = rule.get("name", "Unknown Rule")
        severity = rule.get("severity", "unknown")
        description = rule.get("description", "No description available")

        # Get the most recent instance for location info
        instances = alert.get("instances", [])
        location_info = ""
        if instances:
            instance = instances[0]  # Most recent instance
            location = instance.get("location", {})
            path = location.get("path", "unknown")
            start_line = location.get("start_line", "unknown")
            location_info = f"\n\n**Location**: `{path}` (line {start_line})"

        body_parts = [
            f"## CodeQL Security Alert #{alert_id}",
            "",
            f"**Rule**: {rule_id}",
            f"**Name**: {rule_name}",
            f"**Severity**: {severity.upper()}",
            "",
            f"**Description**: {description}",
            location_info,
            "",
            f"**Alert URL**: {alert.get('html_url', 'Not available')}",
            "",
            "---",
            "",
            "This issue was automatically created from a CodeQL security alert.",
            "Please review the alert and take appropriate action to resolve the security vulnerability.",
            "",
            f"<!-- codeql-alert-id:{alert_id} -->",
        ]

        return "\n".join(body_parts)

    def _print_alert_plan(self, alert: dict[str, Any]) -> None:
        """Print what would be done for an alert in dry-run mode."""
        alert_id = str(alert.get("number", alert.get("id", "unknown")))
        rule_id = alert.get("rule", {}).get("id", "unknown-rule")
        severity = alert.get("rule", {}).get("severity", "unknown")

        print(f"  🔒 Would create issue for CodeQL alert #{alert_id}")
        print(f"     Rule: {rule_id} (Severity: {severity})")


def main():
    """Main entry point for the issue manager script."""
    parser = argparse.ArgumentParser(
        description="Unified GitHub issue management script",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python issue_manager.py update-issues --dry-run
  python issue_manager.py copilot-tickets
  python issue_manager.py close-duplicates
  python issue_manager.py codeql-alerts
  python issue_manager.py event-handler

Environment Variables:
  GITHUB_TOKEN - GitHub token with repo access (required)
  REPO - Repository in owner/name format (required)
  GITHUB_EVENT_NAME - Webhook event name (for event-driven operations)
  GITHUB_EVENT_PATH - Path to event payload (for event-driven operations)
        """,
    )

    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # Update issues command
    update_parser = subparsers.add_parser(
        "update-issues", help="Process issue updates from JSON files"
    )
    update_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without executing",
    )
    update_parser.add_argument(
        "--verbose", action="store_true", help="Enable verbose output"
    )
    update_parser.add_argument(
        "--directory",
        default=".github/issue-updates",
        help="Directory containing issue update JSON files",
    )

    # Copilot tickets command
    copilot_parser = subparsers.add_parser(
        "copilot-tickets", help="Manage Copilot review comment tickets"
    )
    copilot_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without executing",
    )

    # Close duplicates command
    duplicate_parser = subparsers.add_parser(
        "close-duplicates", help="Close duplicate issues by title"
    )
    duplicate_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without executing",
    )

    # CodeQL alerts command
    codeql_parser = subparsers.add_parser(
        "codeql-alerts", help="Generate tickets for CodeQL security alerts"
    )
    codeql_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without executing",
    )

    # Event handler command
    subparsers.add_parser("event-handler", help="Handle GitHub webhook events")

    args = parser.parse_args()

    if not args.command:
        parser.print_help()
        return 1

    # Validate required environment variables
    token = os.getenv("GITHUB_TOKEN") or os.getenv("GH_TOKEN")
    repo = os.getenv("REPO")

    if not token:
        print("❌ Error: GITHUB_TOKEN or GH_TOKEN environment variable is required")
        return 1

    if not repo:
        print("❌ Error: REPO environment variable is required (format: owner/name)")
        return 1

    try:
        # Initialize the GitHub API
        api = GitHubAPI(token, repo)

        # Execute the requested command
        if args.command == "update-issues":
            processor = IssueUpdateProcessor(api, dry_run=args.dry_run)
            success = processor.process_updates(updates_directory=args.directory)
            return 0 if success else 1

        elif args.command == "copilot-tickets":
            manager = CopilotTicketManager(api)
            success = manager.manage_tickets(dry_run=args.dry_run)
            return 0 if success else 1

        elif args.command == "close-duplicates":
            manager = DuplicateIssueManager(api)
            success = manager.close_duplicates(dry_run=args.dry_run)
            return 0 if success else 1

        elif args.command == "codeql-alerts":
            manager = CodeQLAlertManager(api)
            success = manager.create_tickets(dry_run=args.dry_run)
            return 0 if success else 1

        elif args.command == "event-handler":
            # For event handler, we need to determine what type of event this is
            event_name = os.getenv("GITHUB_EVENT_NAME")
            if not event_name:
                print(
                    "❌ Error: GITHUB_EVENT_NAME environment variable is required for event handling"
                )
                return 1

            # For now, just handle as issue updates
            processor = IssueUpdateProcessor(api)
            success = processor.process_updates(dry_run=False)
            return 0 if success else 1

        else:
            print(f"❌ Unknown command: {args.command}")
            return 1

    except Exception as e:
        print(f"❌ Error: {e}")
        if getattr(args, "verbose", False):
            import traceback

            traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())
