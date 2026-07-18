#!/usr/bin/env python3
# file: scripts/dedup_bench_pass2.py
# version: 1.1.0
# guid: 3d7f9a1c-5e2b-4c8d-9a6f-0b3e5d7c1a94
# last-edited: 2026-07-18
"""Second-pass enrichment for uncertain dedup suggestions.

Takes medium/low confidence results from a first-pass groups run,
enriches them with book title data from the server, and resubmits
for a more informed decision.

Usage:
    python3 scripts/dedup_bench_pass2.py \
        --server https://<server>:8484 \
        --results testdata/dedup-bench/2026-03-07T20-11-15/runs/gpt-5.1_baseline_t0.0_groups/batch_output.jsonl \
        --groups testdata/dedup-bench/2026-03-07T20-11-15/groups.json \
        --model gpt-5.1

Loads API keys from scripts/.env automatically.
"""

import argparse
import json
import os
import ssl
import sys
import time
import urllib.request
from datetime import datetime
from pathlib import Path

# Load .env from scripts/ directory
env_path = Path(__file__).parent / ".env"
if env_path.exists():
    for line in env_path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

from openai import OpenAI

SYSTEM_PROMPT = """You are an expert audiobook metadata reviewer performing a SECOND-PASS verification.

You previously reviewed author groups and flagged some as uncertain. Now you are given:
1. The original group of author name variants
2. Your previous suggestion and reasoning
3. NEW DATA: Book titles for each author variant, so you can determine if they are the same person

Use the book titles to make a more informed decision:
- If both variants have books in the same series or genre, they are likely the same person → merge
- If they have completely unrelated books, they are likely different people → split
- Narrators tend to have books across many unrelated genres/authors
- Publishers have many unrelated books

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {"suggestions": [{"group_index": N, "action": "merge|split|rename|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation using book title evidence", "confidence": "high|medium|low", "roles": {"author": {"name": "Name", "variants": ["V1"], "reason": "why"}, "narrator": {"name": "Name", "ids": [], "reason": "why"}, "publisher": {"name": "Name", "ids": [], "reason": "why"}}}]}"""


def fetch_books_for_authors(server_url: str, author_ids: list) -> dict:
    """Fetch book titles for specific author IDs."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    books_by_author = {}
    # Fetch all books (paginated) and filter
    offset = 0
    page_size = 1000
    while True:
        url = f"{server_url}/api/v1/audiobooks?limit={page_size}&offset={offset}"
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, context=ctx) as resp:
            page = json.loads(resp.read())

        items = page.get("items", [])
        for book in items:
            aid = book.get("author_id", 0)
            if aid in author_ids:
                if aid not in books_by_author:
                    books_by_author[aid] = []
                books_by_author[aid].append(
                    {
                        "title": book.get("title", ""),
                        "series": book.get("series", ""),
                    }
                )

        if len(items) < page_size:
            break
        offset += page_size

    return books_by_author


def main():
    parser = argparse.ArgumentParser(
        description="Second-pass enrichment for uncertain suggestions"
    )
    parser.add_argument("--server", required=True, help="Server URL")
    parser.add_argument(
        "--results", required=True, help="Path to first-pass batch_output.jsonl"
    )
    parser.add_argument(
        "--groups", required=True, help="Path to groups.json from first pass"
    )
    parser.add_argument(
        "--model", default="gpt-5.1", help="Model to use for second pass"
    )
    parser.add_argument(
        "--threshold",
        default="medium",
        choices=["medium", "low"],
        help="Include suggestions at this confidence and below (default: medium = medium+low)",
    )
    parser.add_argument(
        "--output", default="testdata/dedup-bench", help="Output base directory"
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="Build prompts but don't submit"
    )
    args = parser.parse_args()

    api_key = os.environ.get("OPENAI_API_KEY", "")
    if not api_key and not args.dry_run:
        print("ERROR: OPENAI_API_KEY not set (check scripts/.env)")
        sys.exit(1)

    # Load first-pass results
    results_path = Path(args.results)
    suggestions = []
    for line in results_path.read_text().strip().split("\n"):
        resp = json.loads(line)
        if resp.get("response", {}).get("status_code") != 200:
            continue
        content = resp["response"]["body"]["choices"][0]["message"]["content"]
        parsed = json.loads(content)
        suggestions.extend(parsed.get("suggestions", []))

    # Filter uncertain ones
    thresholds = ["low"] if args.threshold == "low" else ["medium", "low"]
    uncertain = [s for s in suggestions if s.get("confidence") in thresholds]
    print(
        f"First pass: {len(suggestions)} total, {len(uncertain)} uncertain ({', '.join(thresholds)})"
    )

    if not uncertain:
        print("No uncertain suggestions to enrich. Done.")
        return

    # Load groups
    groups = json.loads(Path(args.groups).read_text())

    # Collect all author IDs we need books for
    all_author_ids = set()
    for s in uncertain:
        gi = s.get("group_index", -1)
        if 0 <= gi < len(groups):
            group = groups[gi]
            # Extract author IDs from the group
            canonical = group.get("canonical", {})
            if canonical.get("id"):
                all_author_ids.add(canonical["id"])
            for v in group.get("variants", []):
                if v.get("id"):
                    all_author_ids.add(v["id"])

    print(f"Fetching books for {len(all_author_ids)} authors...")
    books_by_author = fetch_books_for_authors(args.server, all_author_ids)
    total_books = sum(len(b) for b in books_by_author.values())
    print(f"  Found {total_books} books across {len(books_by_author)} authors")

    # Build enriched prompts — one request per uncertain suggestion
    enriched_items = []
    for s in uncertain:
        gi = s.get("group_index", -1)
        if gi < 0 or gi >= len(groups):
            continue

        group = groups[gi]
        canonical = group.get("canonical", {})
        variants = group.get("variants", [])

        # Build book evidence
        evidence = {}
        if canonical.get("id"):
            aid = canonical["id"]
            titles = books_by_author.get(aid, [])
            evidence[canonical.get("name", f"ID {aid}")] = [
                t["title"] + (f" ({t['series']})" if t.get("series") else "")
                for t in titles[:10]
            ]
        for v in variants:
            if v.get("id"):
                aid = v["id"]
                titles = books_by_author.get(aid, [])
                evidence[v.get("name", f"ID {aid}")] = [
                    t["title"] + (f" ({t['series']})" if t.get("series") else "")
                    for t in titles[:10]
                ]

        enriched_items.append(
            {
                "group_index": gi,
                "original_group": {
                    "canonical": {
                        "name": canonical.get("name", ""),
                        "id": canonical.get("id"),
                    },
                    "variants": [
                        {"name": v.get("name", ""), "id": v.get("id")} for v in variants
                    ],
                },
                "previous_suggestion": {
                    "action": s.get("action"),
                    "canonical_name": s.get("canonical_name"),
                    "confidence": s.get("confidence"),
                    "reason": s.get("reason"),
                },
                "book_evidence": evidence,
            }
        )

    # Build the batch request
    user_content = json.dumps(enriched_items, indent=2)
    print(
        f"\nEnriched payload: {len(enriched_items)} items, ~{len(user_content) // 4} tokens"
    )

    # Create run directory
    ts = datetime.now().strftime("%Y-%m-%dT%H-%M-%S")
    run_dir = Path(args.output) / f"{ts}-pass2"
    run_dir.mkdir(parents=True, exist_ok=True)

    # Save enriched data
    (run_dir / "enriched_input.json").write_text(json.dumps(enriched_items, indent=2))
    (run_dir / "config.json").write_text(
        json.dumps(
            {
                "model": args.model,
                "mode": "pass2-enrichment",
                "first_pass_results": str(args.results),
                "threshold": args.threshold,
                "uncertain_count": len(enriched_items),
            },
            indent=2,
        )
    )

    if args.dry_run:
        print(f"Dry run — enriched data saved to {run_dir}")
        print(f"\nSample enriched item:")
        print(json.dumps(enriched_items[0], indent=2))
        return

    # Submit as batch
    is_reasoning = args.model.startswith("o") or args.model.startswith("gpt-5")

    body = {
        "model": args.model,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {
                "role": "user",
                "content": f"Review these uncertain suggestions with the new book title evidence:\n\n{user_content}",
            },
        ],
        "max_completion_tokens": 16000,
    }

    if not is_reasoning:
        body["temperature"] = 0.0
        body["top_p"] = 1.0
        body["response_format"] = {"type": "json_object"}

    req_line = (
        json.dumps(
            {
                "custom_id": f"pass2_{args.model}_{ts}",
                "method": "POST",
                "url": "/v1/chat/completions",
                "body": body,
            }
        )
        + "\n"
    )

    # Save JSONL
    (run_dir / "batch_input.jsonl").write_text(req_line)

    client = OpenAI(api_key=api_key)

    print(f"Uploading and submitting batch job...")
    file = client.files.create(file=req_line.encode(), purpose="batch")
    batch = client.batches.create(
        input_file_id=file.id,
        endpoint="/v1/chat/completions",
        completion_window="24h",
    )

    job_info = {
        "batch_id": batch.id,
        "input_file_id": file.id,
        "model": args.model,
        "mode": "pass2-enrichment",
        "uncertain_count": len(enriched_items),
    }
    (run_dir / "batch_info.json").write_text(json.dumps(job_info, indent=2))

    # Also save as batch_jobs.json for compatibility with check script
    (run_dir / "batch_jobs.json").write_text(
        json.dumps(
            [
                {
                    "batch_id": batch.id,
                    "config": {
                        "model": args.model,
                        "prompt_variant": "pass2-enrichment",
                    },
                    "mode": "pass2",
                    "run_dir": str(run_dir),
                }
            ],
            indent=2,
        )
    )

    print(f"\nSubmitted batch job: {batch.id}")
    print(f"Output: {run_dir}")
    print(f"Check: python3 scripts/dedup_bench_check.py {run_dir}")


if __name__ == "__main__":
    main()
