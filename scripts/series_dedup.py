#!/usr/bin/env python3
# file: scripts/series_dedup.py
# version: 1.2.0
# guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
# last-edited: 2026-07-18
#
# Series deduplication script for audiobook-organizer.
# Fetches all series, analyzes duplicates, and applies fixes via the API.
#
# The server base URL is REQUIRED via the ABK_API_URL environment variable
# (e.g. https://<server>:8484) — there is no internal-network default.

import argparse
import json
import os
import re
import sys
import time
import urllib3
from collections import defaultdict

import requests

# Suppress InsecureRequestWarning for self-signed certs
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

_ABK_API_URL = os.environ.get("ABK_API_URL")
if not _ABK_API_URL:
    sys.stderr.write(
        "ERROR: ABK_API_URL environment variable is required "
        "(e.g. https://<server>:8484)\n"
    )
    sys.exit(1)
BASE_URL = f"{_ABK_API_URL.rstrip('/')}/api/v1"
VERIFY_SSL = False

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_ROOT = os.path.dirname(SCRIPT_DIR)
TESTDATA_DIR = os.path.join(PROJECT_ROOT, "testdata")
DUMP_FILE = os.path.join(TESTDATA_DIR, "series_dump.json")
FIX_FILE = os.path.join(TESTDATA_DIR, "series_fix.json")


# ── Jaro-Winkler implementation ──────────────────────────────────────────────


def jaro_similarity(s1: str, s2: str) -> float:
    """Compute Jaro similarity between two strings."""
    if s1 == s2:
        return 1.0
    len1, len2 = len(s1), len(s2)
    if len1 == 0 or len2 == 0:
        return 0.0

    match_distance = max(len1, len2) // 2 - 1
    if match_distance < 0:
        match_distance = 0

    s1_matches = [False] * len1
    s2_matches = [False] * len2

    matches = 0
    transpositions = 0

    for i in range(len1):
        start = max(0, i - match_distance)
        end = min(i + match_distance + 1, len2)
        for j in range(start, end):
            if s2_matches[j] or s1[i] != s2[j]:
                continue
            s1_matches[i] = True
            s2_matches[j] = True
            matches += 1
            break

    if matches == 0:
        return 0.0

    k = 0
    for i in range(len1):
        if not s1_matches[i]:
            continue
        while not s2_matches[k]:
            k += 1
        if s1[i] != s2[k]:
            transpositions += 1
        k += 1

    return (
        matches / len1 + matches / len2 + (matches - transpositions / 2) / matches
    ) / 3


def jaro_winkler_similarity(s1: str, s2: str, p: float = 0.1) -> float:
    """Compute Jaro-Winkler similarity between two strings."""
    jaro = jaro_similarity(s1, s2)
    prefix_len = 0
    for i in range(min(len(s1), len(s2), 4)):
        if s1[i] == s2[i]:
            prefix_len += 1
        else:
            break
    return jaro + prefix_len * p * (1 - jaro)


# ── API helpers ───────────────────────────────────────────────────────────────

SESSION = requests.Session()
SESSION.verify = VERIFY_SSL


def api_get(path: str, params: dict = None):
    """GET request to the API."""
    resp = SESSION.get(f"{BASE_URL}{path}", params=params, timeout=30)
    resp.raise_for_status()
    return resp.json()


def api_post(path: str, data: dict):
    """POST request to the API."""
    resp = SESSION.post(f"{BASE_URL}{path}", json=data, timeout=60)
    resp.raise_for_status()
    return resp.json()


def api_delete(path: str):
    """DELETE request to the API."""
    resp = SESSION.delete(f"{BASE_URL}{path}", timeout=30)
    resp.raise_for_status()
    return resp.status_code


# ── Junk detection ────────────────────────────────────────────────────────────


def is_junk_series(name: str) -> bool:
    """Detect series names that are junk."""
    trimmed = name.strip()
    if not trimmed:
        return True
    # Pure numbers
    if re.match(r"^\d+$", trimmed):
        return True
    # Single character
    if len(trimmed) <= 1:
        return True
    # Pure punctuation
    if re.match(r"^[^a-zA-Z0-9]+$", trimmed):
        return True
    return False


def looks_like_author_name(name: str) -> bool:
    """Heuristic: detect series names that look like author names."""
    trimmed = name.strip()
    # "Last, First" pattern
    if re.match(r"^[A-Z][a-z]+,\s*[A-Z][a-z]+", trimmed):
        return True
    return False


# ── Book count fetching ───────────────────────────────────────────────────────


def fetch_all_book_series_ids() -> dict[int, int]:
    """Fetch all audiobooks and count how many belong to each series.

    Returns dict mapping series_id -> book_count.
    Much faster than querying per-series.
    """
    print("Fetching all audiobooks to count series membership...")
    series_counts = defaultdict(int)
    offset = 0
    limit = 500
    total_books = 0

    while True:
        data = api_get("/audiobooks", params={"limit": limit, "offset": offset})
        items = data.get("items", [])
        if not items:
            break
        for book in items:
            sid = book.get("series_id")
            if sid is not None:
                series_counts[sid] += 1
        total_books += len(items)
        offset += limit
        if len(items) < limit:
            break
        if total_books % 2000 == 0:
            print(f"  Fetched {total_books} books so far...")

    print(f"  Total books fetched: {total_books}")
    print(f"  Books with series: {sum(series_counts.values())}")
    return dict(series_counts)


# ── Analysis ──────────────────────────────────────────────────────────────────


def fetch_series() -> list[dict]:
    """Fetch all series from the API."""
    print("Fetching all series...")
    data = api_get("/series")
    series = data.get("items", [])
    print(f"  Found {len(series)} series")
    return series


def analyze_duplicates(series_list: list[dict], book_counts: dict[int, int]):
    """Analyze series for duplicates and issues."""
    actions = []
    processed_ids = set()

    # ── 1. Exact name duplicates ──────────────────────────────────────────
    print("\n=== Exact Name Duplicates ===")
    exact_groups = defaultdict(list)
    for s in series_list:
        exact_groups[s["name"]].append(s)

    exact_dup_count = 0
    for name, group in sorted(exact_groups.items()):
        if len(group) < 2:
            continue
        exact_dup_count += 1
        # Pick the one with most books as canonical
        group_sorted = sorted(
            group, key=lambda x: book_counts.get(x["id"], 0), reverse=True
        )
        canonical = group_sorted[0]
        dupes = group_sorted[1:]
        print(
            f"  '{name}': {len(group)} copies -> keep ID {canonical['id']} ({book_counts.get(canonical['id'], 0)} books)"
        )
        for d in dupes:
            if d["id"] not in processed_ids:
                actions.append(
                    {
                        "action": "merge",
                        "keep_id": canonical["id"],
                        "merge_ids": [d["id"]],
                        "reason": f"exact duplicate of '{name}'",
                        "keep_name": canonical["name"],
                        "merge_name": d["name"],
                    }
                )
                processed_ids.add(d["id"])
        processed_ids.add(canonical["id"])

    print(f"  Total exact duplicate groups: {exact_dup_count}")

    # ── 2. Case-insensitive duplicates ────────────────────────────────────
    print("\n=== Case-Insensitive Duplicates ===")
    ci_groups = defaultdict(list)
    for s in series_list:
        ci_groups[s["name"].strip().lower()].append(s)

    ci_dup_count = 0
    for key, group in sorted(ci_groups.items()):
        if len(group) < 2:
            continue
        # Check if there are unprocessed members
        unprocessed = [s for s in group if s["id"] not in processed_ids]
        if len(unprocessed) < 1:
            continue
        # Find canonical (prefer already-processed one with most books, or pick best)
        group_sorted = sorted(
            group, key=lambda x: book_counts.get(x["id"], 0), reverse=True
        )
        canonical = group_sorted[0]
        dupes = [d for d in group_sorted[1:] if d["id"] not in processed_ids]
        if not dupes:
            continue

        ci_dup_count += 1
        names = [s["name"] for s in group]
        print(f"  {names} -> keep '{canonical['name']}' (ID {canonical['id']})")
        for d in dupes:
            actions.append(
                {
                    "action": "merge",
                    "keep_id": canonical["id"],
                    "merge_ids": [d["id"]],
                    "reason": f"case-insensitive duplicate: '{d['name']}' -> '{canonical['name']}'",
                    "keep_name": canonical["name"],
                    "merge_name": d["name"],
                }
            )
            processed_ids.add(d["id"])
        processed_ids.add(canonical["id"])

    print(f"  Total case-insensitive duplicate groups: {ci_dup_count}")

    # ── 3. Fuzzy matches (Jaro-Winkler >= 0.90) ──────────────────────────
    print("\n=== Fuzzy Matches (Jaro-Winkler >= 0.90) ===")
    remaining = [s for s in series_list if s["id"] not in processed_ids]
    print(f"  Comparing {len(remaining)} remaining series...")

    # Build normalized name index for faster comparison
    remaining_norm = [(s, s["name"].strip().lower()) for s in remaining]
    fuzzy_checked = set()
    fuzzy_count = 0

    # For large sets, only compare series that share at least a 3-char prefix
    # to avoid O(n^2) with 16k entries
    prefix_buckets = defaultdict(list)
    for idx, (s, norm) in enumerate(remaining_norm):
        if len(norm) >= 3:
            prefix_buckets[norm[:3]].append(idx)
        # Also bucket by first 4 chars for longer names
        if len(norm) >= 4:
            prefix_buckets[norm[:4]].append(idx)

    # Additionally, group by similar-length names (within 3 chars diff)
    # and same first letter
    first_char_buckets = defaultdict(list)
    for idx, (s, norm) in enumerate(remaining_norm):
        if norm:
            first_char_buckets[norm[0]].append(idx)

    # Use prefix buckets to find candidates
    for idx, (s1, n1) in enumerate(remaining_norm):
        if s1["id"] in fuzzy_checked:
            continue
        if len(n1) < 3:
            continue

        # Get candidate indices from prefix buckets
        candidates = set()
        for plen in [3, 4]:
            if len(n1) >= plen:
                for cidx in prefix_buckets.get(n1[:plen], []):
                    if cidx > idx:
                        candidates.add(cidx)

        # Also check same-first-letter candidates within similar length
        for cidx in first_char_buckets.get(n1[0], []):
            if cidx > idx:
                s2, n2 = remaining_norm[cidx]
                if abs(len(n1) - len(n2)) <= 3:
                    candidates.add(cidx)

        group = [s1]
        for cidx in candidates:
            s2, n2 = remaining_norm[cidx]
            if s2["id"] in fuzzy_checked:
                continue
            score = jaro_winkler_similarity(n1, n2)
            if score >= 0.90:
                group.append(s2)
                fuzzy_checked.add(s2["id"])

        if len(group) > 1:
            fuzzy_checked.add(s1["id"])
            group_sorted = sorted(
                group, key=lambda x: book_counts.get(x["id"], 0), reverse=True
            )
            canonical = group_sorted[0]
            dupes = group_sorted[1:]
            cn = canonical["name"].strip().lower()

            names_with_scores = []
            for d in dupes:
                score = jaro_winkler_similarity(cn, d["name"].strip().lower())
                names_with_scores.append(f"'{d['name']}' ({score:.3f})")

            if fuzzy_count < 50:  # Don't flood output
                print(
                    f"  Keep '{canonical['name']}' (ID {canonical['id']}, {book_counts.get(canonical['id'], 0)} books)"
                )
                print(f"    Merge: {', '.join(names_with_scores)}")
            fuzzy_count += 1

            for d in dupes:
                score = jaro_winkler_similarity(cn, d["name"].strip().lower())
                actions.append(
                    {
                        "action": "merge",
                        "keep_id": canonical["id"],
                        "merge_ids": [d["id"]],
                        "reason": f"fuzzy match (JW={score:.3f}): '{d['name']}' -> '{canonical['name']}'",
                        "keep_name": canonical["name"],
                        "merge_name": d["name"],
                    }
                )
                processed_ids.add(d["id"])
            processed_ids.add(canonical["id"])

    if fuzzy_count > 50:
        print(f"  ... ({fuzzy_count - 50} more groups not shown)")
    print(f"  Total fuzzy match groups: {fuzzy_count}")

    # ── 4. Series with 0 books ────────────────────────────────────────────
    print("\n=== Series with 0 Books ===")
    empty_count = 0
    for s in series_list:
        if book_counts.get(s["id"], 0) == 0 and s["id"] not in processed_ids:
            empty_count += 1
            actions.append(
                {
                    "action": "delete",
                    "series_id": s["id"],
                    "series_name": s["name"],
                    "reason": "0 books attached",
                }
            )
            processed_ids.add(s["id"])
    print(f"  Total empty series: {empty_count}")

    # ── 5. Junk / author-name series ──────────────────────────────────────
    print("\n=== Junk / Author-Name Series ===")
    junk_count = 0
    author_name_count = 0
    for s in series_list:
        if s["id"] in processed_ids:
            continue
        if is_junk_series(s["name"]):
            junk_count += 1
            actions.append(
                {
                    "action": "delete",
                    "series_id": s["id"],
                    "series_name": s["name"],
                    "reason": f"junk series name: '{s['name']}'",
                }
            )
            processed_ids.add(s["id"])
        elif looks_like_author_name(s["name"]):
            author_name_count += 1
            if author_name_count <= 20:
                print(
                    f"  Possible author name (skipping): '{s['name']}' (ID {s['id']}, {book_counts.get(s['id'], 0)} books)"
                )

    if author_name_count > 20:
        print(f"  ... ({author_name_count - 20} more possible author names not shown)")
    print(f"  Junk series to delete: {junk_count}")
    print(f"  Possible author names (not auto-deleted): {author_name_count}")

    return actions


# ── Fix application ──────────────────────────────────────────────────────────


def apply_fixes(actions: list[dict], dry_run: bool = False):
    """Apply the fix actions via the API."""
    prefix = "DRY RUN - " if dry_run else ""
    merge_actions = [a for a in actions if a["action"] == "merge"]
    delete_actions = [a for a in actions if a["action"] == "delete"]

    print(f"\n{'=' * 60}")
    print(
        f"{prefix}Applying {len(actions)} fixes ({len(merge_actions)} merges, {len(delete_actions)} deletes)"
    )
    print(f"{'=' * 60}")

    merge_count = 0
    delete_count = 0
    error_count = 0

    # Group merges by keep_id for efficiency (send all merge_ids at once)
    merge_groups = defaultdict(list)
    merge_names = {}
    for action in merge_actions:
        keep_id = action["keep_id"]
        merge_groups[keep_id].extend(action["merge_ids"])
        merge_names[keep_id] = action.get("keep_name", str(keep_id))

    # Apply merges
    for keep_id, merge_ids in merge_groups.items():
        unique_merge_ids = sorted(set(merge_ids))
        print(
            f"  MERGE: '{merge_names.get(keep_id, keep_id)}' (ID {keep_id}) <- {len(unique_merge_ids)} duplicates"
        )
        if not dry_run:
            try:
                result = api_post(
                    "/series/merge",
                    {
                        "keep_id": keep_id,
                        "merge_ids": unique_merge_ids,
                    },
                )
                op_id = result.get("operation_id", result.get("id", "?"))
                print(f"    -> OK (operation: {op_id})")
                merge_count += 1
                # Small delay to not overwhelm the server
                time.sleep(0.1)
            except requests.exceptions.HTTPError as e:
                print(f"    -> ERROR: {e}")
                if hasattr(e, "response") and e.response is not None:
                    try:
                        print(f"       Detail: {e.response.json()}")
                    except Exception:
                        pass
                error_count += 1
            except Exception as e:
                print(f"    -> ERROR: {e}")
                error_count += 1
        else:
            merge_count += 1

    # Apply deletes
    for i, action in enumerate(delete_actions):
        sid = action["series_id"]
        if i < 20 or i == len(delete_actions) - 1:
            print(
                f"  DELETE: series {sid} ('{action['series_name']}') - {action['reason']}"
            )
        elif i == 20:
            print(f"  ... ({len(delete_actions) - 20} more deletes)")

        if not dry_run:
            try:
                api_delete(f"/series/{sid}")
                delete_count += 1
                if (i + 1) % 100 == 0:
                    print(f"    Deleted {i + 1}/{len(delete_actions)}...")
                    time.sleep(0.05)
            except requests.exceptions.HTTPError as e:
                if (
                    hasattr(e, "response")
                    and e.response is not None
                    and e.response.status_code == 404
                ):
                    delete_count += 1  # Already gone
                else:
                    error_count += 1
            except Exception:
                error_count += 1
        else:
            delete_count += 1

    print(
        f"\n{prefix}Summary: {merge_count} merges, {delete_count} deletes, {error_count} errors"
    )


# ── Main ──────────────────────────────────────────────────────────────────────


def main():
    parser = argparse.ArgumentParser(description="Series deduplication tool")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Analyze and create fix script but don't apply changes",
    )
    parser.add_argument(
        "--analyze-only", action="store_true", help="Only analyze, don't apply fixes"
    )
    parser.add_argument(
        "--apply-only",
        action="store_true",
        help="Only apply fixes from existing series_fix.json",
    )
    args = parser.parse_args()

    os.makedirs(TESTDATA_DIR, exist_ok=True)

    if args.apply_only:
        # Just load and apply existing fix file
        if not os.path.exists(FIX_FILE):
            print(f"ERROR: {FIX_FILE} not found. Run without --apply-only first.")
            sys.exit(1)
        with open(FIX_FILE) as f:
            fix_data = json.load(f)
        apply_fixes(fix_data["actions"], dry_run=args.dry_run)
        return

    # Step 1: Fetch all series
    series_list = fetch_series()

    # Step 2: Dump raw data
    print(f"\nDumping raw data to {DUMP_FILE}")
    with open(DUMP_FILE, "w") as f:
        json.dump({"items": series_list, "count": len(series_list)}, f, indent=2)
    print(f"  Wrote {len(series_list)} series")

    # Step 3: Get book counts efficiently (batch fetch all books)
    book_counts = fetch_all_book_series_ids()

    # Step 4: Analyze
    actions = analyze_duplicates(series_list, book_counts)

    # Step 5: Save fix script
    print(f"\nSaving fix script to {FIX_FILE}")
    with open(FIX_FILE, "w") as f:
        json.dump({"actions": actions, "count": len(actions)}, f, indent=2)
    print(f"  Wrote {len(actions)} actions")

    # Step 6: Summary
    merge_actions = [a for a in actions if a["action"] == "merge"]
    delete_actions = [a for a in actions if a["action"] == "delete"]
    print(f"\n{'=' * 60}")
    print(f"ANALYSIS SUMMARY")
    print(f"{'=' * 60}")
    print(f"  Total series: {len(series_list)}")
    print(f"  Series with books: {len(book_counts)}")
    print(
        f"  Series without books: {len(series_list) - len([s for s in series_list if s['id'] in book_counts])}"
    )
    print(f"  Merge actions: {len(merge_actions)}")
    print(f"  Delete actions: {len(delete_actions)}")
    print(f"  Total actions: {len(actions)}")

    if args.analyze_only:
        print("\n--analyze-only: Skipping fix application")
        return

    # Step 7: Apply fixes
    if actions:
        apply_fixes(actions, dry_run=args.dry_run)
    else:
        print("\nNo fixes needed!")


if __name__ == "__main__":
    main()
