#!/usr/bin/env python3
# file: scripts/test-tag-roundtrip.py
# version: 1.1.0
# guid: 5c8e2a4f-1d6b-4f9c-a3e7-8b2d0f6c4a19
# last-edited: 2026-07-18
"""Test tag write/read round-trip against a running audiobook-organizer server.

Usage:
    python3 scripts/test-tag-roundtrip.py [--host HOST] [--book-id BOOK_ID]

The server host:port is REQUIRED via --host or the ABK_API_URL environment
variable (e.g. <server>:8484) — there is no internal-network default.

This script:
1. Reads current tags from a book via the /tags API
2. Triggers a write-back via POST /write-back
3. Reads tags again and verifies the round-trip
4. Reports any fields that were lost or corrupted
"""

import argparse
import json
import os
import sys
import time
import urllib.request
import ssl


def api(host, method, path, data=None):
    """Make an API call to the audiobook-organizer server."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    url = f"https://{host}/api/v1{path}"
    headers = {"Content-Type": "application/json"}

    if data is not None:
        req = urllib.request.Request(
            url, data=json.dumps(data).encode(), headers=headers, method=method
        )
    else:
        req = urllib.request.Request(url, headers=headers, method=method)

    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode()
        print(f"  HTTP {e.code}: {body[:200]}")
        return None


def main():
    parser = argparse.ArgumentParser(description="Test tag write/read round-trip")
    parser.add_argument(
        "--host",
        default=os.environ.get("ABK_API_URL"),
        help="Server host:port, e.g. <server>:8484 (required; defaults to $ABK_API_URL)",
    )
    parser.add_argument(
        "--book-id", help="Book ID to test (uses first book if not specified)"
    )
    args = parser.parse_args()

    if not args.host:
        parser.error(
            "--host is required (or set the ABK_API_URL environment variable "
            "to your server host:port, e.g. <server>:8484)"
        )

    host = args.host
    book_id = args.book_id

    # Find a book to test with
    if not book_id:
        print("Finding a book to test with...")
        result = api(host, "GET", "/audiobooks?limit=1&offset=0")
        if not result or not result.get("items"):
            print("ERROR: No books found")
            sys.exit(1)
        book_id = result["items"][0]["id"]
        print(f"  Using book: {result['items'][0].get('title', 'Unknown')} ({book_id})")

    # Step 1: Read current tags
    print("\n1. Reading current tags from file...")
    tags_before = api(host, "GET", f"/audiobooks/{book_id}/tags")
    if not tags_before:
        print("ERROR: Failed to read tags")
        sys.exit(1)

    tag_data = tags_before.get("tags", {})
    print(f"  Found {len(tag_data)} tag fields")

    # Collect file values before write-back
    file_values_before = {}
    db_values = {}
    for field, entry in tag_data.items():
        fv = entry.get("file_value")
        sv = entry.get("stored_value")
        if fv is not None:
            file_values_before[field] = fv
        if sv is not None:
            db_values[field] = sv

    print(f"  File values: {len(file_values_before)} fields have data")
    print(f"  DB values: {len(db_values)} fields have data")

    # Show fields where DB has value but file doesn't
    missing_in_file = []
    for field, sv in db_values.items():
        fv = file_values_before.get(field)
        if (
            (fv is None or fv == "" or fv == "\u2014")
            and sv
            and sv != ""
            and sv != "\u2014"
        ):
            missing_in_file.append(field)

    if missing_in_file:
        print(f"\n  Fields in DB but missing from file: {', '.join(missing_in_file)}")
    else:
        print(f"\n  All DB fields present in file tags")

    # Step 2: Trigger write-back
    print("\n2. Triggering write-back...")
    wb_result = api(
        host, "POST", f"/audiobooks/{book_id}/write-back", {"rename": False}
    )
    if wb_result:
        print(f"  Write-back result: {wb_result.get('message', 'OK')}")
    else:
        print("  WARNING: Write-back may have failed")

    # Wait for write-back to complete
    time.sleep(2)

    # Step 3: Read tags after write-back
    print("\n3. Reading tags after write-back...")
    tags_after = api(host, "GET", f"/audiobooks/{book_id}/tags")
    if not tags_after:
        print("ERROR: Failed to read tags after write-back")
        sys.exit(1)

    tag_data_after = tags_after.get("tags", {})
    file_values_after = {}
    for field, entry in tag_data_after.items():
        fv = entry.get("file_value")
        if fv is not None:
            file_values_after[field] = fv

    # Step 4: Compare
    print("\n4. Comparing results...")
    issues = []

    # Check: DB values should now be in file
    for field, sv in db_values.items():
        fv_after = file_values_after.get(field)
        if field in missing_in_file:
            if fv_after and str(fv_after) != "" and str(fv_after) != "\u2014":
                print(f"  FIXED: {field} now has file value: {fv_after}")
            else:
                issues.append(
                    f"STILL MISSING: {field} - DB has '{sv}' but file still empty"
                )

    # Check: fields that had values before shouldn't lose them
    for field, fv_before in file_values_before.items():
        fv_after = file_values_after.get(field)
        if fv_before and str(fv_before) != "\u2014":
            if not fv_after or str(fv_after) == "\u2014" or str(fv_after) == "":
                issues.append(
                    f"LOST: {field} had '{fv_before}' but now empty after write-back"
                )
            elif str(fv_before) != str(fv_after):
                # Value changed — not necessarily bad (could be normalization)
                print(f"  CHANGED: {field}: '{fv_before}' -> '{fv_after}'")

    # Check: new fields that appeared after write-back
    new_fields = set(file_values_after.keys()) - set(file_values_before.keys())
    if new_fields:
        for field in sorted(new_fields):
            print(f"  NEW: {field} = {file_values_after[field]}")

    # Report
    print(f"\n{'=' * 60}")
    if issues:
        print(f"RESULT: {len(issues)} issue(s) found:")
        for issue in issues:
            print(f"  - {issue}")
        sys.exit(1)
    else:
        print("RESULT: All tags round-tripped successfully!")
        print(f"  {len(file_values_after)} fields with file values after write-back")
        sys.exit(0)


if __name__ == "__main__":
    main()
