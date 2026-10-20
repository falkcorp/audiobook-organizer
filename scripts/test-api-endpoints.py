#!/usr/bin/env python3
# file: scripts/test-api-endpoints.py
# version: 1.1.2
# guid: a1b2c3d4-e5f6-7890-abcd-1234567890ab

"""
Comprehensive manual API endpoint testing script.
Tests all endpoints defined in the MVP specification and server implementation.
"""

import json
import sys
import time
from dataclasses import dataclass
from typing import Any

import requests


@dataclass
class TestResult:
    """Result of an endpoint test."""

    endpoint: str
    method: str
    status_code: int
    success: bool
    response: dict[str, Any] | None
    error: str | None
    duration_ms: float


class APITester:
    """Automated API endpoint tester."""

    def __init__(self, base_url: str = "http://localhost:8484"):
        self.base_url = base_url
        self.results: list[TestResult] = []
        self.session = requests.Session()

    def test_endpoint(
        self,
        method: str,
        path: str,
        expected_status: int = 200,
        json_data: dict | None = None,
        params: dict | None = None,
        description: str = "",
    ) -> TestResult:
        """Test a single endpoint."""
        url = f"{self.base_url}{path}"
        print(f"\n{'=' * 80}")
        print(f"Testing: {method} {path}")
        if description:
            print(f"Description: {description}")

        start = time.time()
        try:
            if method == "GET":
                response = self.session.get(url, params=params, timeout=10)
            elif method == "POST":
                response = self.session.post(url, json=json_data, params=params, timeout=10)
            elif method == "PUT":
                response = self.session.put(url, json=json_data, params=params, timeout=10)
            elif method == "DELETE":
                response = self.session.delete(url, params=params, timeout=10)
            else:
                raise ValueError(f"Unsupported method: {method}")

            duration_ms = (time.time() - start) * 1000

            # Try to parse JSON response
            try:
                response_json = response.json()
            except ValueError:  # requests' JSONDecodeError subclasses ValueError
                response_json = {"raw": response.text[:200]}

            success = response.status_code == expected_status
            result = TestResult(
                endpoint=path,
                method=method,
                status_code=response.status_code,
                success=success,
                response=response_json,
                error=None
                if success
                else f"Expected {expected_status}, got {response.status_code}",
                duration_ms=duration_ms,
            )

            status_icon = "✅" if success else "❌"
            print(f"{status_icon} Status: {response.status_code} (expected {expected_status})")
            print(f"⏱️  Duration: {duration_ms:.2f}ms")
            print(f"Response: {json.dumps(response_json, indent=2)[:500]}")

        except Exception as e:
            duration_ms = (time.time() - start) * 1000
            result = TestResult(
                endpoint=path,
                method=method,
                status_code=0,
                success=False,
                response=None,
                error=str(e),
                duration_ms=duration_ms,
            )
            print(f"❌ Error: {e}")

        self.results.append(result)
        return result

    def run_all_tests(self):
        """Run all endpoint tests."""
        print(f"\n{'#' * 80}")
        print("# Starting Comprehensive API Endpoint Tests")
        print(f"# Base URL: {self.base_url}")
        print(f"# Time: {time.strftime('%Y-%m-%d %H:%M:%S')}")
        print(f"{'#' * 80}\n")

        # Health check
        self.test_endpoint("GET", "/api/health", description="Health check endpoint")

        # Real-time events (SSE) - just test connection
        print("\n" + "=" * 80)
        print("Note: /api/events (SSE) requires special handling - skipping in basic test")

        # Audiobook endpoints
        self.test_endpoint(
            "GET",
            "/api/v1/audiobooks",
            description="List all audiobooks with pagination",
            params={"limit": 10, "offset": 0},
        )

        # We need an audiobook ID for these tests - we'll try with a dummy ID
        # In real tests, we'd create an audiobook first
        self.test_endpoint(
            "GET",
            "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ",
            expected_status=404,  # Expect not found for dummy ID
            description="Get specific audiobook (using dummy ID)",
        )

        self.test_endpoint(
            "PUT",
            "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ",
            expected_status=404,  # Expect not found for dummy ID
            json_data={"title": "Test Update"},
            description="Update audiobook (using dummy ID)",
        )

        self.test_endpoint(
            "DELETE",
            "/api/v1/audiobooks/01HXZ123456789ABCDEFGHJ",
            expected_status=404,  # Expect not found for dummy ID
            description="Delete audiobook (using dummy ID)",
        )

        self.test_endpoint(
            "POST",
            "/api/v1/audiobooks/batch",
            json_data={"audiobooks": []},
            description="Batch update audiobooks (empty batch)",
        )

        # Author and series endpoints
        self.test_endpoint("GET", "/api/v1/authors", description="List all authors")
        self.test_endpoint("GET", "/api/v1/series", description="List all series")

        # Filesystem endpoints
        self.test_endpoint(
            "GET",
            "/api/v1/filesystem/browse",
            params={"path": "/"},
            description="Browse filesystem root",
        )

        # Library folder endpoints
        self.test_endpoint("GET", "/api/v1/import-paths", description="List import paths")

        # Note: We won't actually add/remove folders in test mode
        print("\n" + "=" * 80)
        print("Note: Skipping POST /api/v1/import-paths (would modify system)")
        print("Note: Skipping DELETE /api/v1/import-paths/:id (would modify system)")

        # Operation endpoints (read-only tests)
        # Note: We won't start actual scans in test mode
        print("\n" + "=" * 80)
        print("Note: Skipping POST /api/v1/operations/scan (would start actual scan)")
        print("Note: Skipping POST /api/v1/operations/organize (would modify files)")

        self.test_endpoint(
            "GET",
            "/api/v1/operations/01HXZ123456789ABCDEFGHJ/status",
            expected_status=404,  # Expect not found for dummy ID
            description="Get operation status (using dummy ID)",
        )

        self.test_endpoint(
            "GET",
            "/api/v1/operations/01HXZ123456789ABCDEFGHJ/logs",
            expected_status=404,  # Expect not found for dummy ID or empty
            description="Get operation logs (using dummy ID)",
        )

        # System endpoints
        self.test_endpoint("GET", "/api/v1/system/status", description="Get system status")
        self.test_endpoint("GET", "/api/v1/system/logs", description="Get system logs")
        self.test_endpoint("GET", "/api/v1/config", description="Get configuration")

        # Note: We won't update config in test mode
        print("\n" + "=" * 80)
        print("Note: Skipping PUT /api/v1/config (would modify configuration)")

        # Backup endpoints
        self.test_endpoint("GET", "/api/v1/backup/list", description="List backups")

        # Note: We won't create/restore/delete backups in test mode
        print("\n" + "=" * 80)
        print("Note: Skipping POST /api/v1/backup/create (would create backup)")
        print("Note: Skipping POST /api/v1/backup/restore (would restore database)")
        print("Note: Skipping DELETE /api/v1/backup/:filename (would delete backup)")

        # Metadata endpoints
        self.test_endpoint(
            "POST",
            "/api/v1/metadata/batch-update",
            json_data={"updates": [], "validate": True},
            description="Batch update metadata (empty batch)",
        )

        self.test_endpoint(
            "POST",
            "/api/v1/metadata/validate",
            json_data={"updates": {"title": "Test Book", "author": "Test Author"}},
            description="Validate metadata",
        )

        self.test_endpoint("GET", "/api/v1/metadata/export", description="Export metadata")

        # Note: We won't import metadata in test mode
        print("\n" + "=" * 80)
        print("Note: Skipping POST /api/v1/metadata/import (would modify database)")

        # Work endpoints - comprehensive CRUD testing
        print("\n" + "=" * 80)
        print("Testing Work entity endpoints...")

        # List works (should be empty initially or have existing ones)
        self.test_endpoint(
            "GET",
            "/api/v1/works",
            description="List all works",
        )

        # Create a work
        work_create_result = self.test_endpoint(
            "POST",
            "/api/v1/works",
            expected_status=201,
            json_data={"title": "API Test Work"},
            description="Create a new work",
        )

        # Extract work ID if creation succeeded
        work_id = None
        if work_create_result.success and work_create_result.response:
            work_id = work_create_result.response.get("id")
            print(f"Created work ID: {work_id}")

        if work_id:
            # Get work by ID
            self.test_endpoint(
                "GET",
                f"/api/v1/works/{work_id}",
                description=f"Get work by ID: {work_id}",
            )

            # Update work
            self.test_endpoint(
                "PUT",
                f"/api/v1/works/{work_id}",
                json_data={"title": "API Test Work (Updated)"},
                description="Update work title",
            )

            # List books by work (should be empty)
            self.test_endpoint(
                "GET",
                f"/api/v1/works/{work_id}/books",
                description=f"List books for work {work_id}",
            )

            # Delete work
            self.test_endpoint(
                "DELETE",
                f"/api/v1/works/{work_id}",
                expected_status=204,
                description=f"Delete work {work_id}",
            )

        # Test error cases
        self.test_endpoint(
            "POST",
            "/api/v1/works",
            expected_status=400,
            json_data={},
            description="Create work with missing title (expect 400)",
        )

        self.test_endpoint(
            "GET",
            "/api/v1/works/01HXXXXXXXXXXXXXXXXXXX",
            expected_status=404,
            description="Get non-existent work (expect 404)",
        )

        # Exclusion endpoints - test with safe dummy data
        print("\n" + "=" * 80)
        print("Note: Skipping filesystem exclusion tests (would modify filesystem)")

    def print_summary(self):
        """Print test summary."""
        print(f"\n{'#' * 80}")
        print("# Test Summary")
        print(f"{'#' * 80}\n")

        passed = sum(1 for r in self.results if r.success)
        failed = sum(1 for r in self.results if not r.success)
        total = len(self.results)

        print(f"Total Tests: {total}")
        print(f"✅ Passed: {passed}")
        print(f"❌ Failed: {failed}")
        print(f"Success Rate: {(passed / total * 100) if total > 0 else 0:.1f}%")

        if failed > 0:
            print(f"\n{'=' * 80}")
            print("Failed Tests:")
            for result in self.results:
                if not result.success:
                    print(f"  ❌ {result.method} {result.endpoint}")
                    print(f"     Status: {result.status_code}, Error: {result.error}")

        avg_duration = (
            sum(r.duration_ms for r in self.results) / len(self.results) if self.results else 0
        )
        print(f"\nAverage Response Time: {avg_duration:.2f}ms")

        # Performance analysis
        slow_requests = [r for r in self.results if r.duration_ms > 1000]
        if slow_requests:
            print("\n⚠️  Slow Requests (>1s):")
            for r in slow_requests:
                print(f"  {r.method} {r.endpoint}: {r.duration_ms:.2f}ms")

    def save_results(self, filename: str = "test-results.json"):
        """Save test results to JSON file."""
        results_data = {
            "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
            "base_url": self.base_url,
            "total_tests": len(self.results),
            "passed": sum(1 for r in self.results if r.success),
            "failed": sum(1 for r in self.results if not r.success),
            "results": [
                {
                    "endpoint": r.endpoint,
                    "method": r.method,
                    "status_code": r.status_code,
                    "success": r.success,
                    "error": r.error,
                    "duration_ms": r.duration_ms,
                    "response": r.response,
                }
                for r in self.results
            ],
        }

        with open(filename, "w") as f:
            json.dump(results_data, f, indent=2)

        print(f"\n📄 Results saved to: {filename}")


def main():
    """Main entry point."""
    # Check if custom URL provided
    base_url = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8484"

    print(f"""
╔══════════════════════════════════════════════════════════════════════════════╗
║                   Audiobook Organizer API Test Suite                        ║
╚══════════════════════════════════════════════════════════════════════════════╝

This script tests all API endpoints defined in the MVP specification.
Some destructive tests (create, delete, modify) are skipped by default.

Base URL: {base_url}
    """)

    tester = APITester(base_url)

    try:
        tester.run_all_tests()
    except KeyboardInterrupt:
        print("\n\n⚠️  Tests interrupted by user")
    except Exception as e:
        print(f"\n\n❌ Fatal error: {e}")
        import traceback

        traceback.print_exc()
    finally:
        tester.print_summary()
        tester.save_results()

    # Exit with non-zero if any tests failed
    failed = sum(1 for r in tester.results if not r.success)
    sys.exit(1 if failed > 0 else 0)


if __name__ == "__main__":
    main()
