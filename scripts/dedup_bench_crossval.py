#!/usr/bin/env python3
# file: scripts/dedup_bench_crossval.py
# version: 1.1.0
# guid: 8b1e4d6f-2a9c-4e7b-b3d5-1f6a8c0e4b72
# last-edited: 2026-07-18
"""Cross-validation: send one model's output to another for a second opinion.

Submits batch jobs where model B reviews model A's suggestions, optionally
with the full original input data included for context.

Usage:
    python3 scripts/dedup_bench_crossval.py \
        --server https://<server>:8484 \
        --results-a testdata/dedup-bench/.../batch_output.jsonl \
        --model-a gpt-5.1 --mode-a groups \
        --input-data testdata/dedup-bench/.../groups.json \
        --model-b o4-mini \
        --variants both

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

# Max completion tokens per model
MAX_COMPLETION_TOKENS = {
    "gpt-4o": 16384,
    "gpt-4o-mini": 16384,
    "gpt-4.1": 32768,
    "gpt-4.1-mini": 32768,
    "o3-mini": 65536,
    "o4-mini": 65536,
    "gpt-5.1": 32768,
    "gpt-5-mini": 32768,
    "gpt-5-nano": 16384,
}

REVIEW_PROMPT_NO_DATA = """You are an expert audiobook metadata reviewer performing a CROSS-VALIDATION review.

Another AI model ({model_a}) analyzed author data and produced deduplication suggestions. Your job is to review each suggestion and either AGREE, DISAGREE, or MODIFY it.

You will receive ONLY the suggestions (not the original data). Use your knowledge of authors, narrators, and publishers to evaluate each one.

For each suggestion, respond with:
- "agree": The suggestion is correct as-is
- "disagree": The suggestion is wrong; explain why and provide the correct action
- "modify": The suggestion is partially right but needs adjustment

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {{"reviews": [{{"group_index": N, "original_action": "...", "original_canonical": "...", "verdict": "agree|disagree|modify", "corrected_action": "merge|split|rename|skip|alias|reclassify", "corrected_canonical": "Correct Name", "confidence": "high|medium|low", "reason": "brief explanation"}}]}}

Only include entries where you disagree or want to modify. If you agree with everything, return {{"reviews": []}}."""

REVIEW_PROMPT_WITH_DATA = """You are an expert audiobook metadata reviewer performing a CROSS-VALIDATION review.

Another AI model ({model_a}) analyzed author data and produced deduplication suggestions. Your job is to review each suggestion and either AGREE, DISAGREE, or MODIFY it.

You will receive:
1. The ORIGINAL input data (author groups or author list) that model A was given
2. Model A's suggestions

Use both the original data AND your knowledge to evaluate each suggestion. Look for:
- False merges (different people merged together)
- Missed duplicates (obvious matches that model A skipped or split)
- Wrong canonical names
- Misclassified roles (author called narrator, etc.)

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {{"reviews": [{{"group_index": N, "original_action": "...", "original_canonical": "...", "verdict": "agree|disagree|modify", "corrected_action": "merge|split|rename|skip|alias|reclassify", "corrected_canonical": "Correct Name", "confidence": "high|medium|low", "reason": "brief explanation"}}], "missed": [{{"description": "what was missed", "author_ids_or_names": [...], "suggested_action": "...", "canonical_name": "...", "confidence": "high|medium|low"}}]}}

For "reviews": only include entries where you disagree or want to modify.
For "missed": include any obvious duplicates or issues that model A completely missed."""


def fetch_books_for_authors(server_url, author_ids):
    """Fetch book titles for specific author IDs."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    books_by_author = {}
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


def load_suggestions_from_jsonl(path):
    """Load all suggestions from a batch_output.jsonl file."""
    suggestions = []
    for line in Path(path).read_text().strip().split("\n"):
        resp = json.loads(line)
        if resp.get("response", {}).get("status_code") != 200:
            continue
        content = resp["response"]["body"]["choices"][0]["message"]["content"]
        # Handle reasoning models that may wrap JSON in markdown
        c = content.strip()
        if c.startswith("```"):
            lines = c.split("\n")
            lines = lines[1:]  # skip ```json
            if lines and lines[-1].strip() == "```":
                lines = lines[:-1]
            c = "\n".join(lines)
        parsed = json.loads(c)
        suggestions.extend(parsed.get("suggestions", []))
    return suggestions


def load_suggestions_from_chunks(base_dir, pattern):
    """Load suggestions from multiple chunk directories."""
    suggestions = []
    base = Path(base_dir)
    for chunk_dir in sorted(base.glob(pattern)):
        out_file = chunk_dir / "batch_output.jsonl"
        if out_file.exists():
            suggestions.extend(load_suggestions_from_jsonl(str(out_file)))
    return suggestions


def submit_batch_job(client, model, system_prompt, user_prompt, custom_id, out_dir):
    """Build JSONL, upload, and submit a batch job."""
    is_reasoning = model.startswith("o") or model.startswith("gpt-5")
    max_tokens = MAX_COMPLETION_TOKENS.get(model, 16000)

    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "max_completion_tokens": max_tokens,
    }

    if not is_reasoning:
        body["temperature"] = 0.0
        body["top_p"] = 1.0
        body["response_format"] = {"type": "json_object"}

    req_line = (
        json.dumps(
            {
                "custom_id": custom_id,
                "method": "POST",
                "url": "/v1/chat/completions",
                "body": body,
            }
        )
        + "\n"
    )

    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "batch_input.jsonl").write_text(req_line)

    file = client.files.create(file=req_line.encode(), purpose="batch")
    batch = client.batches.create(
        input_file_id=file.id,
        endpoint="/v1/chat/completions",
        completion_window="24h",
    )

    info = {
        "batch_id": batch.id,
        "input_file_id": file.id,
        "model": model,
        "custom_id": custom_id,
    }
    (out_dir / "batch_info.json").write_text(json.dumps(info, indent=2))
    return info


def main():
    parser = argparse.ArgumentParser(
        description="Cross-validate model A's output with model B"
    )
    parser.add_argument("--server", required=True, help="Server URL for book data")
    parser.add_argument(
        "--results-a",
        required=True,
        help="Path to model A's batch_output.jsonl (or base dir for chunks)",
    )
    parser.add_argument("--model-a", required=True, help="Model A name (for labeling)")
    parser.add_argument(
        "--mode-a",
        required=True,
        choices=["groups", "full"],
        help="Was model A's run groups or full mode?",
    )
    parser.add_argument(
        "--input-data", help="Path to original input (groups.json or full_input.json)"
    )
    parser.add_argument(
        "--chunk-pattern",
        help="Glob pattern for chunk dirs (e.g., 'o4-mini_baseline*_chunk*')",
    )
    parser.add_argument("--model-b", required=True, help="Model B for cross-validation")
    parser.add_argument(
        "--variants",
        default="both",
        choices=["no-data", "with-data", "both"],
        help="Run without data, with data, or both",
    )
    parser.add_argument(
        "--output", default="testdata/dedup-bench", help="Output base directory"
    )
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    api_key = os.environ.get("OPENAI_API_KEY", "")
    if not api_key and not args.dry_run:
        print("ERROR: OPENAI_API_KEY not set")
        sys.exit(1)

    # Load model A's suggestions
    results_path = Path(args.results_a)
    if results_path.is_file():
        suggestions_a = load_suggestions_from_jsonl(str(results_path))
    elif args.chunk_pattern:
        suggestions_a = load_suggestions_from_chunks(
            str(results_path), args.chunk_pattern
        )
    else:
        # Try to find chunk dirs automatically
        if results_path.is_dir():
            suggestions_a = []
            for jsonl in sorted(results_path.rglob("batch_output.jsonl")):
                suggestions_a.extend(load_suggestions_from_jsonl(str(jsonl)))
        else:
            print(f"ERROR: Cannot find results at {results_path}")
            sys.exit(1)

    print(f"Loaded {len(suggestions_a)} suggestions from {args.model_a}")

    # Load original input data if provided
    input_data = None
    if args.input_data:
        input_data = json.loads(Path(args.input_data).read_text())
        print(f"Loaded original input: {len(input_data)} items")

    # Fetch book data for medium/low confidence items
    med_low = [s for s in suggestions_a if s.get("confidence") in ("medium", "low")]
    print(f"  {len(med_low)} medium/low confidence suggestions")

    # Collect author IDs for book enrichment
    book_author_ids = set()
    if input_data and med_low:
        for s in med_low:
            gi = s.get("group_index", -1)
            if args.mode_a == "groups" and 0 <= gi < len(input_data):
                group = input_data[gi]
                canonical = group.get("canonical", {})
                if canonical.get("id"):
                    book_author_ids.add(canonical["id"])
                for v in group.get("variants", []):
                    if v.get("id"):
                        book_author_ids.add(v["id"])
            elif args.mode_a == "full":
                for aid in s.get("author_ids", []):
                    book_author_ids.add(aid)

    books_by_author = {}
    if book_author_ids:
        print(f"Fetching books for {len(book_author_ids)} uncertain authors...")
        books_by_author = fetch_books_for_authors(args.server, book_author_ids)
        total_books = sum(len(b) for b in books_by_author.values())
        print(f"  Found {total_books} books")

    # Build the suggestions summary for model B
    suggestions_text = json.dumps(suggestions_a, indent=2)

    # Build book evidence for uncertain items
    book_evidence = {}
    if books_by_author and input_data:
        for s in med_low:
            gi = s.get("group_index", -1)
            if args.mode_a == "groups" and 0 <= gi < len(input_data):
                group = input_data[gi]
                ev = {}
                canonical = group.get("canonical", {})
                if canonical.get("id") and canonical["id"] in books_by_author:
                    ev[canonical.get("name", "")] = [
                        t["title"] + (f" ({t['series']})" if t.get("series") else "")
                        for t in books_by_author[canonical["id"]][:10]
                    ]
                for v in group.get("variants", []):
                    if v.get("id") and v["id"] in books_by_author:
                        ev[v.get("name", "")] = [
                            t["title"]
                            + (f" ({t['series']})" if t.get("series") else "")
                            for t in books_by_author[v["id"]][:10]
                        ]
                if ev:
                    book_evidence[gi] = ev

    # Create run directory
    ts = datetime.now().strftime("%Y-%m-%dT%H-%M-%S")
    run_dir = Path(args.output) / f"{ts}-crossval"
    run_dir.mkdir(parents=True, exist_ok=True)

    # Save metadata
    (run_dir / "config.json").write_text(
        json.dumps(
            {
                "model_a": args.model_a,
                "mode_a": args.mode_a,
                "model_b": args.model_b,
                "suggestions_count": len(suggestions_a),
                "uncertain_count": len(med_low),
                "book_evidence_groups": len(book_evidence),
            },
            indent=2,
        )
    )

    all_jobs = []
    client = None if args.dry_run else OpenAI(api_key=api_key)

    variants = []
    if args.variants in ("no-data", "both"):
        variants.append("no-data")
    if args.variants in ("with-data", "both"):
        variants.append("with-data")

    for variant in variants:
        label = f"{args.model_a}-to-{args.model_b}_{variant}"
        out = run_dir / "runs" / label

        if variant == "no-data":
            system = REVIEW_PROMPT_NO_DATA.format(model_a=args.model_a)
            user_parts = [
                f"## {args.model_a}'s suggestions ({args.mode_a} mode)\n\n{suggestions_text}",
            ]
            if book_evidence:
                user_parts.append(
                    f"\n\n## Book title evidence for uncertain suggestions (medium/low confidence)\n\n"
                    + json.dumps(book_evidence, indent=2)
                )
            user_prompt = "\n".join(user_parts)

        else:  # with-data
            system = REVIEW_PROMPT_WITH_DATA.format(model_a=args.model_a)
            input_text = (
                json.dumps(input_data, indent=2)
                if input_data
                else "(original data not available)"
            )
            user_parts = [
                f"## Original input data ({args.mode_a} mode)\n\n{input_text}",
                f"\n\n## {args.model_a}'s suggestions\n\n{suggestions_text}",
            ]
            if book_evidence:
                user_parts.append(
                    f"\n\n## Book title evidence for uncertain suggestions\n\n"
                    + json.dumps(book_evidence, indent=2)
                )
            user_prompt = "\n".join(user_parts)

        est_tokens = len(user_prompt) // 4
        print(
            f"\n[{variant}] {args.model_b} reviewing {args.model_a}: ~{est_tokens} input tokens"
        )

        if args.dry_run:
            out.mkdir(parents=True, exist_ok=True)
            (out / "user_prompt_preview.txt").write_text(
                user_prompt[:5000] + "\n...(truncated)"
            )
            print(f"  Dry run — saved preview to {out}")
            continue

        cid = f"crossval_{label}_{ts}"
        try:
            info = submit_batch_job(client, args.model_b, system, user_prompt, cid, out)
            all_jobs.append(
                {
                    "batch_id": info["batch_id"],
                    "config": {
                        "model": args.model_b,
                        "prompt_variant": f"crossval-{variant}",
                    },
                    "mode": f"crossval-{variant}",
                    "run_dir": str(out),
                    "label": label,
                }
            )
            print(f"  -> {info['batch_id']}")
        except Exception as e:
            print(f"  ERROR: {e}")

        time.sleep(0.5)

    if all_jobs:
        (run_dir / "batch_jobs.json").write_text(json.dumps(all_jobs, indent=2))
        print(f"\nSubmitted {len(all_jobs)} cross-validation jobs.")
        print(f"Check: python3 scripts/dedup_bench_check.py {run_dir}")
    elif args.dry_run:
        print(f"\nDry run complete. Output: {run_dir}")


if __name__ == "__main__":
    main()
