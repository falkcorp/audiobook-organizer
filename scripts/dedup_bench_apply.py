#!/usr/bin/env python3
# file: scripts/dedup_bench_apply.py
# version: 1.1.0
# guid: 6f4a2e8c-9d1b-4c7a-8e3f-2b6d5a9c0f14
# last-edited: 2026-07-18
"""Apply agreed AI dedup suggestions to the audiobook-organizer database.

Reads testdata/dedup-bench/agreed_suggestions.json and applies each suggestion
via the server API. Requires a running server, passed via --server or the
ABK_API_URL environment variable (required; no internal-network default).

Usage:
    python3 scripts/dedup_bench_apply.py [--dry-run] [--server URL]
    ABK_API_URL=https://<server>:8484 python3 scripts/dedup_bench_apply.py --dry-run

Actions:
    merge       → POST /api/v1/authors/merge  {keep_id, merge_ids}
    rename      → PUT  /api/v1/authors/:id/name  {name}
    split       → POST /api/v1/authors/:id/split  {names}
    reclassify  → POST /api/v1/authors/:id/reclassify-as-narrator
    alias       → POST /api/v1/authors/:id/aliases  {alias_name}
"""

import argparse
import json
import os
import sys
import time
import urllib3

# Suppress InsecureRequestWarning for self-signed certs
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

import requests

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
ROOT_DIR = os.path.dirname(SCRIPT_DIR)
AGREED_PATH = os.path.join(
    ROOT_DIR, "testdata", "dedup-bench", "agreed_suggestions.json"
)
GROUPS_PATH = os.path.join(
    ROOT_DIR, "testdata", "dedup-bench", "2026-03-07T18-56-37", "groups.json"
)

DEFAULT_SERVER = os.environ.get("ABK_API_URL")

# Known compound entries that contain multiple people and must be split
# before reclassifying. Verified against live database 2026-03-08.
# Heuristic detection is too error-prone (e.g. "LastName, FirstName" vs
# "Publisher, Narrator"), so we hardcode the known cases.
COMPOUND_IDS_NEED_SPLIT = {
    2514,  # "Actus, Peter Berkrot" — publisher + narrator
}


def is_compound_entry(author_id, name):
    """Check if an author entry is a known compound that needs splitting."""
    return author_id in COMPOUND_IDS_NEED_SPLIT


def load_json(path):
    with open(path) as f:
        return json.load(f)


def api(method, server, endpoint, body=None, dry_run=False):
    """Make an API call. Returns (success, response_data)."""
    url = f"{server}/api/v1{endpoint}"
    if dry_run:
        print(f"  [DRY RUN] {method} {url}")
        if body:
            print(f"            body: {json.dumps(body)}")
        return True, {"dry_run": True}

    try:
        resp = requests.request(method, url, json=body, verify=False, timeout=30)
        if resp.status_code in (200, 201, 202):
            return True, resp.json() if resp.text else {}
        elif resp.status_code == 404:
            print(f"  WARN 404: {url} (author may have been merged/deleted already)")
            return False, {"status": 404, "skipped": True}
        else:
            print(f"  ERROR {resp.status_code}: {resp.text[:200]}")
            return False, {"status": resp.status_code, "error": resp.text[:200]}
    except Exception as e:
        print(f"  EXCEPTION: {e}")
        return False, {"error": str(e)}


def apply_merge(server, keep_id, merge_ids, canonical_name, dry_run):
    """Merge variant authors into the canonical one."""
    # First rename the kept author to the canonical name
    ok, _ = api(
        "PUT", server, f"/authors/{keep_id}/name", {"name": canonical_name}, dry_run
    )
    if not ok:
        return False
    # Then merge the variants into it
    ok, _ = api(
        "POST",
        server,
        "/authors/merge",
        {"keep_id": keep_id, "merge_ids": merge_ids},
        dry_run,
    )
    return ok


def apply_rename(server, author_id, new_name, dry_run):
    """Rename an author."""
    ok, _ = api(
        "PUT", server, f"/authors/{author_id}/name", {"name": new_name}, dry_run
    )
    return ok


def apply_split(server, author_id, names, dry_run):
    """Split a composite author entry."""
    body = {"names": names} if names else None
    ok, _ = api("POST", server, f"/authors/{author_id}/split", body, dry_run)
    return ok


def apply_reclassify(server, author_id, dry_run):
    """Reclassify an author as narrator/publisher (moves to narrator)."""
    ok, _ = api(
        "POST", server, f"/authors/{author_id}/reclassify-as-narrator", dry_run=dry_run
    )
    return ok


def apply_alias(server, keep_id, alias_names, dry_run):
    """Create aliases for an author."""
    all_ok = True
    for alias_name in alias_names:
        ok, _ = api(
            "POST",
            server,
            f"/authors/{keep_id}/aliases",
            {"alias_name": alias_name},
            dry_run,
        )
        if not ok:
            all_ok = False
    return all_ok


# ---------------------------------------------------------------------------
# g51_groups: suggestions reference group_index → groups.json for author IDs
# ---------------------------------------------------------------------------


def apply_g51_groups(agreed, groups, server, dry_run, id_to_name=None):
    """Apply gpt-5.1 groups suggestions (174 items)."""
    if id_to_name is None:
        id_to_name = {}
    stats = {"ok": 0, "fail": 0, "skip": 0}

    for s in agreed:
        idx = s["group_index"]
        action = s["action"]
        canonical_name = s.get("canonical_name", "")

        if idx >= len(groups):
            print(f"  SKIP group_index {idx} out of range")
            stats["skip"] += 1
            continue

        group = groups[idx]
        canonical_id = group["canonical"]["id"]
        variant_ids = [v["id"] for v in group.get("variants", [])]
        variant_names = [v["name"] for v in group.get("variants", [])]

        print(f"  [{action.upper():12s}] group {idx}: {canonical_name}")

        ok = False
        if action == "merge":
            ok = apply_merge(server, canonical_id, variant_ids, canonical_name, dry_run)

        elif action == "split":
            # Split means "leave them as separate" — no merge needed.
            # But if the canonical needs renaming, do that.
            if canonical_name and canonical_name != group["canonical"]["name"]:
                ok = apply_rename(server, canonical_id, canonical_name, dry_run)
            else:
                print(f"           (no action needed — already separate)")
                ok = True

        elif action == "alias":
            # Keep canonical, add variant names as aliases
            # First rename to canonical_name if needed
            if canonical_name and canonical_name != group["canonical"]["name"]:
                apply_rename(server, canonical_id, canonical_name, dry_run)
            # Merge variants into canonical
            if variant_ids:
                ok = apply_merge(
                    server, canonical_id, variant_ids, canonical_name, dry_run
                )
            else:
                ok = True

        elif action == "reclassify":
            # Reclassify all IDs (canonical + variants) as narrator/publisher.
            # But first, split any compound entries so real authors aren't lost.
            all_entries = [(canonical_id, group["canonical"].get("name", ""))]
            all_entries += [
                (v["id"], v.get("name", "")) for v in group.get("variants", [])
            ]
            ok = True
            for aid, aname in all_entries:
                live_name = id_to_name.get(aid, aname)
                if is_compound_entry(aid, live_name):
                    print(
                        f'           splitting compound entry first: id={aid} "{live_name}"'
                    )
                    apply_split(server, aid, None, dry_run)
                    # After split, the original ID may no longer exist — skip reclassify
                    continue
                if not apply_reclassify(server, aid, dry_run):
                    ok = False

        else:
            print(f"           unknown action: {action}")
            stats["skip"] += 1
            continue

        stats["ok" if ok else "fail"] += 1
        time.sleep(0.1)  # gentle rate limit

    return stats


# ---------------------------------------------------------------------------
# o4_full: suggestions have author_ids directly
# ---------------------------------------------------------------------------


def apply_o4_full(agreed, server, dry_run):
    """Apply o4-mini full suggestions (115 items)."""
    stats = {"ok": 0, "fail": 0, "skip": 0}

    for s in agreed:
        action = s["action"]
        author_ids = s.get("author_ids", [])
        canonical_name = s.get("canonical_name", "")
        roles = s.get("roles", {})

        print(
            f"  [{action.upper():12s}] ids={author_ids}: {canonical_name or '(compound)'}"
        )

        ok = False
        if action == "rename":
            if len(author_ids) == 1 and canonical_name:
                ok = apply_rename(server, author_ids[0], canonical_name, dry_run)
            else:
                print(f"           unexpected rename shape: {author_ids}")
                stats["skip"] += 1
                continue

        elif action == "merge":
            if len(author_ids) >= 2 and canonical_name:
                # First ID is the one to keep, rest merge in
                keep_id = author_ids[0]
                merge_ids = author_ids[1:]
                ok = apply_merge(server, keep_id, merge_ids, canonical_name, dry_run)
            else:
                print(f"           unexpected merge shape: {author_ids}")
                stats["skip"] += 1
                continue

        elif action == "split":
            if len(author_ids) >= 1:
                # Extract names from roles if available
                names = []
                if "authors" in roles:
                    names = [a["name"] for a in roles["authors"] if "name" in a]
                ok = apply_split(server, author_ids[0], names, dry_run)
            else:
                print(f"           no author_ids for split")
                stats["skip"] += 1
                continue

        elif action == "reclassify":
            ok = True
            for aid in author_ids:
                if not apply_reclassify(server, aid, dry_run):
                    ok = False

        elif action == "alias":
            if len(author_ids) >= 1:
                keep_id = author_ids[0]
                # Get variant names from roles
                alias_names = []
                if "author" in roles and "variants" in roles["author"]:
                    alias_names = roles["author"]["variants"]
                # Merge all IDs into the first, then add aliases
                if len(author_ids) > 1:
                    ok = apply_merge(
                        server, keep_id, author_ids[1:], canonical_name or "", dry_run
                    )
                if alias_names:
                    apply_alias(server, keep_id, alias_names, dry_run)
                ok = True
            else:
                stats["skip"] += 1
                continue

        else:
            print(f"           unknown action: {action}")
            stats["skip"] += 1
            continue

        stats["ok" if ok else "fail"] += 1
        time.sleep(0.1)

    return stats


# ---------------------------------------------------------------------------
# missed_items: ad-hoc suggestions from cross-validation
# ---------------------------------------------------------------------------
#
# These are hand-curated based on cross-validation output + manual verification
# against the live database. The raw suggestions contain compound author entries
# (e.g. "GraphicAudio [R.A. Salvatore]") that must be SPLIT, not merged.
#
# Safe operations (verified 2026-03-08):
#   1. GraphicAudio: merge pure-GA variants, split compound entries
#   2. Marvel: reclassify pure-Marvel entries, split compound entries
#   3. C. B. Lee: merge two spacing variants
#   4. Michael-Scott Earle: medium confidence → skip

# IDs verified against live database:
#   GraphicAudio pure variants (safe to merge into 142):
GRAPHICAUDIO_PURE_IDS = [646, 2639, 3175, 625, 1401]
#     646  = "Graphic Audio, LLC"
#     2639 = "Wolverine (GraphicAudio)"
#     3175 = "GraphicAudio [James Axler]"  — compound but Axler is a house name
#     625  = "Graphic Audio"
#     1401 = "Marvel - Graphic Audio"
GRAPHICAUDIO_KEEP_ID = 142  # "GraphicAudio"

# GraphicAudio compound entries (need split, not merge — real authors inside):
GRAPHICAUDIO_SPLIT_IDS = [4227, 4250, 4258, 4262, 544, 679, 981]
#     4227 = "GraphicAudio Dan Abnett"        → split to "Dan Abnett" + "GraphicAudio"
#     4250 = "GraphicAudio G. Willow Wilson"  → split to "G. Willow Wilson" + "GraphicAudio"
#     4258 = "GraphicAudio Alisa Kwitney"     → split to "Alisa Kwitney" + "GraphicAudio"
#     4262 = "GraphicAudio Jason Starr"       → split to "Jason Starr" + "GraphicAudio"
#     544  = "GraphicAudio [Peter David]"     → split to "Peter David" + "GraphicAudio"
#     679  = "GraphicAudio [R.A. Salvatore]"  → split to "R.A. Salvatore" + "GraphicAudio"
#     981  = "GraphicAudio [Joseph Nassise]"  → split to "Joseph Nassise" + "GraphicAudio"

# NOT GraphicAudio (excluded from original suggestion):
#     344  = "Nicholas Briggs"  — real author, wrongly included
#     581  = "Simon Archer"     — real author, wrongly included

# Marvel pure entries (safe to reclassify):
MARVEL_PURE_IDS = [90, 969]
#     90   = "Marvel Press"
#     969  = "Marvel"

# Marvel compound entries (need split, not reclassify):
MARVEL_SPLIT_IDS = [2147, 2910, 4629, 6035]
#     2147 = "Stan Lee, Marvel"           → split
#     2910 = "Robert Greenberger, Marvel" → split
#     4629 = "Marvel, Stuart Moore"       → split
#     6035 = "Marv Wolfman, Marvel"       → split

# NOT Marvel (excluded):
#     3401 = does not exist in DB
#     4227, 4250, 4258, 4262 = GraphicAudio entries, not Marvel

# C. B. Lee: simple merge
CB_LEE_KEEP = 2732  # "C. B. Lee"
CB_LEE_MERGE = [922]  # "C.B. Lee"


def apply_missed_items(items, server, dry_run):
    """Apply missed items caught by cross-validation (4 items).

    Uses hardcoded verified IDs rather than the raw suggestion data,
    because the raw data contains compound entries that would be
    destructive if merged/reclassified blindly.
    """
    stats = {"ok": 0, "fail": 0, "skip": 0}

    # --- 1. GraphicAudio: merge pure variants ---
    print(
        f"  [MERGE       ] GraphicAudio: merge {len(GRAPHICAUDIO_PURE_IDS)} pure variants into id={GRAPHICAUDIO_KEEP_ID}"
    )
    ok = apply_merge(
        server, GRAPHICAUDIO_KEEP_ID, GRAPHICAUDIO_PURE_IDS, "GraphicAudio", dry_run
    )
    stats["ok" if ok else "fail"] += 1
    time.sleep(0.1)

    # --- 2. GraphicAudio: split compound entries ---
    for aid in GRAPHICAUDIO_SPLIT_IDS:
        print(f"  [SPLIT       ] GraphicAudio compound: split author id={aid}")
        ok = apply_split(server, aid, None, dry_run)  # auto-detect split names
        stats["ok" if ok else "fail"] += 1
        time.sleep(0.1)

    # --- 3. GraphicAudio: reclassify the main entry as publisher ---
    print(
        f"  [RECLASSIFY  ] GraphicAudio: reclassify id={GRAPHICAUDIO_KEEP_ID} as narrator/publisher"
    )
    ok = apply_reclassify(server, GRAPHICAUDIO_KEEP_ID, dry_run)
    stats["ok" if ok else "fail"] += 1
    time.sleep(0.1)

    # --- 4. Marvel: reclassify pure entries ---
    for aid in MARVEL_PURE_IDS:
        print(f"  [RECLASSIFY  ] Marvel: reclassify id={aid} as narrator/publisher")
        ok = apply_reclassify(server, aid, dry_run)
        stats["ok" if ok else "fail"] += 1
        time.sleep(0.1)

    # --- 5. Marvel: split compound entries ---
    for aid in MARVEL_SPLIT_IDS:
        print(f"  [SPLIT       ] Marvel compound: split author id={aid}")
        ok = apply_split(server, aid, None, dry_run)
        stats["ok" if ok else "fail"] += 1
        time.sleep(0.1)

    # --- 6. C. B. Lee: merge spacing variants ---
    print(f"  [MERGE       ] C. B. Lee: merge id={CB_LEE_MERGE} into id={CB_LEE_KEEP}")
    ok = apply_merge(server, CB_LEE_KEEP, CB_LEE_MERGE, "C. B. Lee", dry_run)
    stats["ok" if ok else "fail"] += 1
    time.sleep(0.1)

    # --- 7. Michael-Scott Earle: skip (medium confidence) ---
    print(
        f"  [SKIP        ] Michael-Scott Earle: medium confidence, needs manual review"
    )
    stats["skip"] += 1

    return stats


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main():
    parser = argparse.ArgumentParser(
        description="Apply agreed AI dedup suggestions to the database"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print what would be done without making changes",
    )
    parser.add_argument(
        "--server",
        default=DEFAULT_SERVER,
        help="Server URL, e.g. https://<server>:8484 "
        "(required; defaults to $ABK_API_URL)",
    )
    parser.add_argument(
        "--agreed", default=AGREED_PATH, help="Path to agreed_suggestions.json"
    )
    parser.add_argument(
        "--groups",
        default=GROUPS_PATH,
        help="Path to groups.json for g51 group index lookups",
    )
    parser.add_argument(
        "--section",
        choices=["g51", "o4", "missed", "all"],
        default="all",
        help="Which section to apply (default: all)",
    )
    args = parser.parse_args()

    if not args.server:
        parser.error(
            "--server is required (or set the ABK_API_URL environment variable "
            "to your server URL, e.g. https://<server>:8484)"
        )

    print(f"Server:  {args.server}")
    print(f"Agreed:  {args.agreed}")
    print(f"Groups:  {args.groups}")
    print(f"Dry run: {args.dry_run}")
    print(f"Section: {args.section}")
    print()

    agreed = load_json(args.agreed)
    groups = load_json(args.groups)

    # Build id→name lookup from live server for compound entry detection
    id_to_name = {}
    try:
        print("Fetching current authors from server...")
        resp = requests.get(
            f"{args.server}/api/v1/authors?limit=10000", verify=False, timeout=60
        )
        if resp.status_code == 200:
            for a in resp.json().get("items", []):
                id_to_name[a["id"]] = a["name"]
            print(f"Loaded {len(id_to_name)} authors from server")
        else:
            print(
                f"WARNING: Could not fetch authors (HTTP {resp.status_code}), compound detection disabled"
            )
    except Exception as e:
        print(f"WARNING: Could not fetch authors ({e}), compound detection disabled")
    print()

    all_stats = {}

    if args.section in ("g51", "all"):
        print("=" * 60)
        print(
            f"Applying gpt-5.1 groups agreements ({len(agreed['g51_groups_agreed'])} items)"
        )
        print("=" * 60)
        all_stats["g51_groups"] = apply_g51_groups(
            agreed["g51_groups_agreed"], groups, args.server, args.dry_run, id_to_name
        )
        print()

    if args.section in ("o4", "all"):
        print("=" * 60)
        print(
            f"Applying o4-mini full agreements ({len(agreed['o4_full_agreed'])} items)"
        )
        print("=" * 60)
        all_stats["o4_full"] = apply_o4_full(
            agreed["o4_full_agreed"], args.server, args.dry_run
        )
        print()

    if args.section in ("missed", "all"):
        print("=" * 60)
        print(f"Applying missed items ({len(agreed['missed_items'])} items)")
        print("=" * 60)
        all_stats["missed"] = apply_missed_items(
            agreed["missed_items"], args.server, args.dry_run
        )
        print()

    # Summary
    print("=" * 60)
    print("SUMMARY")
    print("=" * 60)
    total_ok = total_fail = total_skip = 0
    for section, stats in all_stats.items():
        print(
            f"  {section:20s}  ok={stats['ok']:3d}  fail={stats['fail']:3d}  skip={stats['skip']:3d}"
        )
        total_ok += stats["ok"]
        total_fail += stats["fail"]
        total_skip += stats["skip"]
    print(
        f"  {'TOTAL':20s}  ok={total_ok:3d}  fail={total_fail:3d}  skip={total_skip:3d}"
    )

    if total_fail > 0:
        print(f"\n⚠️  {total_fail} operations failed. Check output above for details.")
        sys.exit(1)
    elif args.dry_run:
        print(f"\nDry run complete. Re-run without --dry-run to apply changes.")
    else:
        print(f"\nAll operations applied successfully.")


if __name__ == "__main__":
    main()
