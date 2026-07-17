#!/usr/bin/env python3
# file: scripts/dedup_bench_submit.py
# version: 1.1.0
# guid: 3b9e1c5a-7d2f-4e8b-a6c1-9f4d0e2b8a53
# last-edited: 2026-07-17
"""Submit dedup benchmark batch jobs to OpenAI.

Fetches author data from a remote server, builds test configurations,
and submits them as batch API jobs (50% cheaper than real-time).

Usage:
    python3 scripts/dedup_bench_submit.py --server https://<server>:8484

The server URL may also be supplied via the ABK_API_URL environment
variable; one of the two is required. Loads API keys from scripts/.env
automatically.
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


# ── Rate limits from the audiobook-organizer project ──
# Batch queue limits (TPD = tokens per day in batch queue)
BATCH_LIMITS = {
    "gpt-4o": 90_000,  # Very low!
    "gpt-4o-mini": 2_000_000,
    "gpt-4.1": 900_000,
    "gpt-4.1-mini": 2_000_000,
    "gpt-4-turbo": 90_000,
    "gpt-5.1": 900_000,
    "gpt-5-mini": 5_000_000,
    "gpt-5-nano": 2_000_000,
    "o3-mini": 2_000_000,
    "o4-mini": 2_000_000,
}

# Max completion tokens per model (some models have lower limits)
MAX_COMPLETION_TOKENS = {
    "gpt-4o": 16384,
    "gpt-4o-mini": 16384,
    "gpt-4.1": 32768,
    "gpt-4.1-mini": 32768,
    "o3-mini": 65536,
    "o4-mini": 65536,
}
DEFAULT_MAX_TOKENS = 16000

# Models to test — only ones we have confirmed access to
DEFAULT_MODELS = [
    "gpt-4o",
    "gpt-4o-mini",
    "gpt-4.1",
    "gpt-4.1-mini",
    "o3-mini",
    "o4-mini",
]

# Prompt variants
PROMPT_VARIANTS = ["baseline", "lookup", "chain-of-thought"]

# ── Prompts ──

GROUPS_PROMPT_BASE = """You are an expert audiobook metadata reviewer. You will receive groups of potentially duplicate author names. For each group, determine the correct action:

- "merge": The variants are the same author with different name formats. Provide the correct canonical name.
- "split": The names represent different people incorrectly grouped together.
- "rename": The canonical name needs correction (e.g., "TOLKIEN, J.R.R." → "J.R.R. Tolkien").
- "skip": The group is fine as-is or you're unsure.
- "reclassify": Entry is not an author at all (narrator/publisher misclassified as author).

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee", "J. R. R. Tolkien" not "J.R.R. Tolkien".

PEN NAMES & ALIASES: When names are clearly pen names, handles, or stage names for the same person, use action "alias" instead of "merge".

COMPOUND ENTRIES WITH PUBLISHERS:
- "Graphic Audio [John Smith]" → Author: John Smith, Publisher: Graphic Audio
- "Full Cast Audio" → Publisher, not author. Use action "reclassify".

ROLE DECOMPOSITION: For every suggestion, populate the "roles" object to classify each name:
- "author": the actual book author with name variants
- "narrator": a voice actor identified by reading many different authors' books
- "publisher": a production company or publisher

Return ONLY valid JSON: {"suggestions": [{"group_index": N, "action": "merge|split|rename|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "roles": {"author": {"name": "Name", "variants": ["V1"], "reason": "why"}, "narrator": {"name": "Name", "ids": [indices], "reason": "why"}, "publisher": {"name": "Name", "ids": [indices], "reason": "why"}}}]}"""

FULL_PROMPT_BASE = """You are an expert audiobook metadata reviewer. You will receive a list of authors with their IDs, book counts, and sample book titles. Find groups of authors that are likely the same person (different name formats, typos, abbreviations, last-name-first, etc).

CRITICAL RULES:
- COMPOUND NAMES: Many author entries contain multiple people separated by commas, ampersands, "and", or semicolons. When you find a compound entry that matches an individual author entry, suggest a merge with the individual as canonical.
- Use sample_titles to distinguish authors from narrators. A narrator reads many different authors' books.
- NEVER merge two genuinely different people.
- Only merge when names clearly refer to the same person.
- If unsure, use action "skip" — false negatives are far better than false positives.
- Identify narrators or publishers incorrectly listed as authors.
- INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".
- PEN NAMES & ALIASES: When names are clearly pen names or handles, use action "alias" instead of "merge".

COMPOUND ENTRIES WITH PUBLISHERS:
- "Graphic Audio [John Smith]" → Author: John Smith, Publisher: Graphic Audio
- "Full Cast Audio" → Publisher, not author. Use action "reclassify".

ROLE DECOMPOSITION: For every suggestion, populate the "roles" object.

Return ONLY valid JSON: {"suggestions": [{"author_ids": [1, 42], "action": "merge|rename|split|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "roles": {"author": {"name": "Name", "variants": ["V1"], "reason": "why"}, "narrator": {"name": "Name", "ids": [ids], "reason": "why"}, "publisher": {"name": "Name", "ids": [ids], "reason": "why"}}}]}

Only include groups where you find actual duplicates or issues."""

PROMPT_SUFFIXES = {
    "baseline": "",
    "lookup": """

VALIDATION STEP: Before making your final decision on each group, mentally verify:
1. Is the canonical name a real, known author? If you recognize them, use their most commonly published name.
2. For merges: are you confident both names refer to the same real person? Check if the sample book titles are consistent with a single author.
3. For renames: use the author's most widely recognized professional name format.
4. If a name could be either an author or narrator, check the sample titles — narrators tend to read books by many different authors across different genres.
Do NOT fabricate authors. If you don't recognize a name, base your decision purely on name similarity and the provided context.""",
    "chain-of-thought": """

REASONING PROCESS: For each group, think through these steps before deciding:
1. List all names in the group and their structural differences (initials vs full, order, punctuation)
2. Check if sample titles suggest same author or different people
3. Consider if any name is a narrator or publisher rather than author
4. Decide the action and confidence level
5. Then output your JSON suggestion

Include your brief reasoning in the "reason" field.""",
}


def get_system_prompt(mode: str, variant: str) -> str:
    base = GROUPS_PROMPT_BASE if mode == "groups" else FULL_PROMPT_BASE
    return base + PROMPT_SUFFIXES.get(variant, "")


def fetch_authors(server_url: str) -> dict:
    """Fetch authors and books from the remote server."""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    # Fetch authors
    print(f"Fetching authors from {server_url}...")
    req = urllib.request.Request(f"{server_url}/api/v1/authors")
    with urllib.request.urlopen(req, context=ctx) as resp:
        author_data = json.loads(resp.read())

    authors = author_data.get("items", [])
    print(f"  {len(authors)} authors")

    # Fetch books with pagination
    book_counts = {}
    sample_books = {}
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
            title = book.get("title", "")
            book_counts[aid] = book_counts.get(aid, 0) + 1
            if aid not in sample_books:
                sample_books[aid] = []
            if len(sample_books[aid]) < 3:
                sample_books[aid].append(title)

        print(f"  Fetched {len(items)} books (offset {offset})")
        if len(items) < page_size:
            break
        offset += page_size

    print(f"  {sum(book_counts.values())} total book-author mappings")

    return {
        "authors": authors,
        "book_counts": book_counts,
        "sample_books": sample_books,
    }


def build_groups_input(authors: list, book_counts: dict, sample_books: dict) -> list:
    """Build heuristic-grouped input. Simple grouping by similar names."""
    # We'll just send all authors as full-mode input for now since the Go
    # heuristic grouper isn't available in Python. The Go binary already
    # saved groups.json from the dry run — use that if available.
    return None  # Signal to use saved groups.json


def build_full_input(authors: list, book_counts: dict, sample_books: dict) -> list:
    """Build full-mode discovery input."""
    inputs = []
    for a in authors:
        aid = a["id"]
        inputs.append(
            {
                "id": aid,
                "name": a["name"],
                "book_count": book_counts.get(aid, 0),
                "sample_titles": sample_books.get(aid, []),
            }
        )
    return inputs


def estimate_tokens(text: str) -> int:
    """Rough token estimate: ~4 chars per token for JSON."""
    return len(text) // 4


def chunk_list(items: list, chunk_size: int) -> list:
    """Split a list into chunks."""
    return [items[i : i + chunk_size] for i in range(0, len(items), chunk_size)]


def submit_batch(
    client: OpenAI,
    model: str,
    prompt_variant: str,
    mode: str,
    temperature: float,
    system_prompt: str,
    user_prompt: str,
    max_tokens: int,
    custom_id: str,
    out_dir: Path,
) -> dict | None:
    """Build JSONL, upload, and submit a batch job. Returns job info or None on error."""
    is_reasoning = (
        model.startswith("o3")
        or model.startswith("o4")
        or model.startswith("o1")
        or model.startswith("gpt-5")
    )
    # Cap max_tokens to model limit
    model_max = MAX_COMPLETION_TOKENS.get(model, DEFAULT_MAX_TOKENS)
    max_tokens = min(max_tokens, model_max)

    body = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "max_completion_tokens": max_tokens,
    }

    if not is_reasoning:
        body["temperature"] = temperature
        body["top_p"] = 1.0
        body["response_format"] = {"type": "json_object"}

    req_line = {
        "custom_id": custom_id,
        "method": "POST",
        "url": "/v1/chat/completions",
        "body": body,
    }

    jsonl = json.dumps(req_line) + "\n"

    # Estimate tokens
    input_tokens = estimate_tokens(system_prompt + user_prompt)
    total_tokens = input_tokens + max_tokens  # worst case

    # Check batch queue limit
    limit = BATCH_LIMITS.get(model, 100_000)
    if total_tokens > limit:
        print(
            f"  SKIP: estimated {total_tokens} tokens > batch limit {limit} for {model}"
        )
        return None

    # Save JSONL locally
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "batch_input.jsonl").write_text(jsonl)
    (out_dir / "config.json").write_text(
        json.dumps(
            {
                "model": model,
                "prompt_variant": prompt_variant,
                "mode": mode,
                "temperature": temperature,
                "estimated_tokens": total_tokens,
            },
            indent=2,
        )
    )

    # Upload
    try:
        file = client.files.create(file=jsonl.encode(), purpose="batch")
        batch = client.batches.create(
            input_file_id=file.id,
            endpoint="/v1/chat/completions",
            completion_window="24h",
        )
        (out_dir / "batch_info.json").write_text(
            json.dumps(
                {
                    "batch_id": batch.id,
                    "input_file_id": file.id,
                    "model": model,
                    "prompt_variant": prompt_variant,
                    "mode": mode,
                    "temperature": temperature,
                    "estimated_tokens": total_tokens,
                },
                indent=2,
            )
        )
        return {
            "batch_id": batch.id,
            "config": {
                "model": model,
                "prompt_variant": prompt_variant,
                "temperature": temperature,
                "top_p": 1.0,
            },
            "mode": mode,
            "num_chunks": 1,
            "num_requests": 1,
            "input_file_id": file.id,
            "run_dir": str(out_dir),
            "estimated_tokens": total_tokens,
        }
    except Exception as e:
        print(f"  ERROR: {e}")
        (out_dir / "error.json").write_text(json.dumps({"error": str(e)}))
        return None


def main():
    parser = argparse.ArgumentParser(description="Submit dedup benchmark batch jobs")
    parser.add_argument(
        "--server",
        default=os.environ.get("ABK_API_URL"),
        help="Server URL (e.g., https://<server>:8484); defaults to $ABK_API_URL",
    )
    parser.add_argument(
        "--output", default="testdata/dedup-bench", help="Output directory"
    )
    parser.add_argument(
        "--models", nargs="+", default=DEFAULT_MODELS, help="Models to test"
    )
    parser.add_argument("--mode", default="both", choices=["groups", "full", "both"])
    parser.add_argument(
        "--chunk-size", type=int, default=500, help="Max authors per chunk"
    )
    parser.add_argument("--dry-run", action="store_true", help="Extract data only")
    parser.add_argument(
        "--groups-json", help="Path to pre-computed groups.json (from Go binary)"
    )
    args = parser.parse_args()

    if not args.server:
        parser.error(
            "--server is required (or set the ABK_API_URL environment variable "
            "to your server URL, e.g. https://<server>:8484)"
        )

    api_key = os.environ.get("OPENAI_API_KEY", "")
    if not api_key and not args.dry_run:
        print("ERROR: OPENAI_API_KEY not set (check scripts/.env)")
        sys.exit(1)

    # Create run directory
    ts = datetime.now().strftime("%Y-%m-%dT%H-%M-%S")
    run_dir = Path(args.output) / ts
    (run_dir / "runs").mkdir(parents=True, exist_ok=True)
    print(f"Output: {run_dir}")

    # Fetch data
    data = fetch_authors(args.server)
    authors = data["authors"]
    book_counts = data["book_counts"]
    sample_books = data["sample_books"]

    # Save frozen data
    (run_dir / "authors.json").write_text(json.dumps(authors, indent=2))

    # Load or skip groups
    groups_input = None
    if args.groups_json:
        groups_input = json.loads(Path(args.groups_json).read_text())
        (run_dir / "groups.json").write_text(json.dumps(groups_input, indent=2))
        print(f"Loaded {len(groups_input)} pre-computed groups")
    else:
        # Check if a recent dry run has groups
        bench_dir = Path(args.output)
        if bench_dir.exists():
            for d in sorted(bench_dir.iterdir(), reverse=True):
                gf = d / "groups.json"
                if gf.exists():
                    groups_input = json.loads(gf.read_text())
                    (run_dir / "groups.json").write_text(
                        json.dumps(groups_input, indent=2)
                    )
                    print(f"Loaded {len(groups_input)} groups from {gf}")
                    break

    full_input = build_full_input(authors, book_counts, sample_books)
    (run_dir / "full_input.json").write_text(json.dumps(full_input, indent=2))

    print(f"\nData: {len(authors)} authors, {len(groups_input or [])} groups")

    if args.dry_run:
        print("Dry run — data saved, no API calls.")
        return

    client = OpenAI(api_key=api_key)

    # Build test matrix
    modes = {"groups": True, "full": True}
    if args.mode == "groups":
        modes["full"] = False
    elif args.mode == "full":
        modes["groups"] = False

    all_jobs = []
    job_num = 0

    # Dimension 1: All models x baseline prompt x temp=0
    for model in args.models:
        for mode_name, enabled in modes.items():
            if not enabled:
                continue

            if mode_name == "groups" and groups_input is None:
                print(f"Skipping groups mode — no groups.json available")
                continue

            job_num += 1
            dir_name = f"{model}_baseline_t0.0_{mode_name}"
            out_dir = run_dir / "runs" / dir_name

            system_prompt = get_system_prompt(mode_name, "baseline")

            if mode_name == "groups":
                user_data = json.dumps(groups_input)
                user_prompt = f"Review these duplicate author groups:\n\n{user_data}"
                max_tokens = 32000
            else:
                # Chunk full input to stay under token limits
                chunks = chunk_list(full_input, args.chunk_size)
                if len(chunks) > 1:
                    # For chunked full mode, submit each chunk separately
                    for ci, chunk in enumerate(chunks):
                        chunk_dir = run_dir / "runs" / f"{dir_name}_chunk{ci}"
                        user_data = json.dumps(chunk)
                        user_prompt = (
                            f"Find duplicate authors in this list:\n\n{user_data}"
                        )
                        cid = f"{model}_baseline_t0.0_{mode_name}_chunk{ci}"

                        print(
                            f"[{job_num}] {model} baseline {mode_name} chunk {ci}/{len(chunks)}",
                            end=" ",
                        )
                        job = submit_batch(
                            client,
                            model,
                            "baseline",
                            mode_name,
                            0.0,
                            system_prompt,
                            user_prompt,
                            16000,
                            cid,
                            chunk_dir,
                        )
                        if job:
                            all_jobs.append(job)
                            print(f"-> {job['batch_id']}")
                        time.sleep(0.5)
                    continue
                else:
                    user_data = json.dumps(full_input)
                    user_prompt = f"Find duplicate authors in this list:\n\n{user_data}"
                    max_tokens = 16000

            cid = f"{model}_baseline_t0.0_{mode_name}"
            print(f"[{job_num}] {model} baseline {mode_name}", end=" ")
            job = submit_batch(
                client,
                model,
                "baseline",
                mode_name,
                0.0,
                system_prompt,
                user_prompt,
                max_tokens,
                cid,
                out_dir,
            )
            if job:
                all_jobs.append(job)
                print(f"-> {job['batch_id']}")
            time.sleep(0.5)

    # Dimension 2: Prompt variations on best-candidate models
    prompt_models = [m for m in ["gpt-4.1-mini", "gpt-4.1"] if m in args.models]
    for model in prompt_models:
        for variant in ["lookup", "chain-of-thought"]:
            for mode_name, enabled in modes.items():
                if not enabled:
                    continue
                if mode_name == "groups" and groups_input is None:
                    continue

                job_num += 1
                dir_name = f"{model}_{variant}_t0.0_{mode_name}"
                out_dir = run_dir / "runs" / dir_name

                system_prompt = get_system_prompt(mode_name, variant)

                if mode_name == "groups":
                    user_data = json.dumps(groups_input)
                    user_prompt = (
                        f"Review these duplicate author groups:\n\n{user_data}"
                    )
                    max_tokens = 32000
                else:
                    chunks = chunk_list(full_input, args.chunk_size)
                    if len(chunks) > 1:
                        for ci, chunk in enumerate(chunks):
                            chunk_dir = run_dir / "runs" / f"{dir_name}_chunk{ci}"
                            user_data = json.dumps(chunk)
                            user_prompt = (
                                f"Find duplicate authors in this list:\n\n{user_data}"
                            )
                            cid = f"{model}_{variant}_t0.0_{mode_name}_chunk{ci}"
                            print(
                                f"[{job_num}] {model} {variant} {mode_name} chunk {ci}/{len(chunks)}",
                                end=" ",
                            )
                            job = submit_batch(
                                client,
                                model,
                                variant,
                                mode_name,
                                0.0,
                                system_prompt,
                                user_prompt,
                                16000,
                                cid,
                                chunk_dir,
                            )
                            if job:
                                all_jobs.append(job)
                                print(f"-> {job['batch_id']}")
                            time.sleep(0.5)
                        continue
                    else:
                        user_data = json.dumps(full_input)
                        user_prompt = (
                            f"Find duplicate authors in this list:\n\n{user_data}"
                        )
                        max_tokens = 16000

                cid = f"{model}_{variant}_t0.0_{mode_name}"
                print(f"[{job_num}] {model} {variant} {mode_name}", end=" ")
                job = submit_batch(
                    client,
                    model,
                    variant,
                    mode_name,
                    0.0,
                    system_prompt,
                    user_prompt,
                    max_tokens,
                    cid,
                    out_dir,
                )
                if job:
                    all_jobs.append(job)
                    print(f"-> {job['batch_id']}")
                time.sleep(0.5)

    # Dimension 3: Temperature variations on gpt-4.1-mini baseline
    if "gpt-4.1-mini" in args.models:
        for temp in [0.3, 0.7]:
            for mode_name, enabled in modes.items():
                if not enabled:
                    continue
                if mode_name == "groups" and groups_input is None:
                    continue

                job_num += 1
                dir_name = f"gpt-4.1-mini_baseline_t{temp}_{mode_name}"
                out_dir = run_dir / "runs" / dir_name

                system_prompt = get_system_prompt(mode_name, "baseline")

                if mode_name == "groups":
                    user_data = json.dumps(groups_input)
                    user_prompt = (
                        f"Review these duplicate author groups:\n\n{user_data}"
                    )
                    max_tokens = 32000
                else:
                    chunks = chunk_list(full_input, args.chunk_size)
                    if len(chunks) > 1:
                        for ci, chunk in enumerate(chunks):
                            chunk_dir = run_dir / "runs" / f"{dir_name}_chunk{ci}"
                            user_data = json.dumps(chunk)
                            user_prompt = (
                                f"Find duplicate authors in this list:\n\n{user_data}"
                            )
                            cid = f"gpt-4.1-mini_baseline_t{temp}_{mode_name}_chunk{ci}"
                            print(
                                f"[{job_num}] gpt-4.1-mini baseline t={temp} {mode_name} chunk {ci}/{len(chunks)}",
                                end=" ",
                            )
                            job = submit_batch(
                                client,
                                "gpt-4.1-mini",
                                "baseline",
                                mode_name,
                                temp,
                                system_prompt,
                                user_prompt,
                                16000,
                                cid,
                                chunk_dir,
                            )
                            if job:
                                all_jobs.append(job)
                                print(f"-> {job['batch_id']}")
                            time.sleep(0.5)
                        continue
                    else:
                        user_data = json.dumps(full_input)
                        user_prompt = (
                            f"Find duplicate authors in this list:\n\n{user_data}"
                        )
                        max_tokens = 16000

                cid = f"gpt-4.1-mini_baseline_t{temp}_{mode_name}"
                print(
                    f"[{job_num}] gpt-4.1-mini baseline t={temp} {mode_name}", end=" "
                )
                job = submit_batch(
                    client,
                    "gpt-4.1-mini",
                    "baseline",
                    mode_name,
                    temp,
                    system_prompt,
                    user_prompt,
                    max_tokens,
                    cid,
                    out_dir,
                )
                if job:
                    all_jobs.append(job)
                    print(f"-> {job['batch_id']}")
                time.sleep(0.5)

    # Save all job info
    (run_dir / "batch_jobs.json").write_text(json.dumps(all_jobs, indent=2))

    print(f"\nSubmitted {len(all_jobs)} batch jobs.")
    print(f"Check results: python3 scripts/dedup_bench_check.py {run_dir}")


if __name__ == "__main__":
    main()
