#!/usr/bin/env python3
# file: scripts/dedup_bench_check.py
# version: 1.0.2
# guid: 72693f85-ecfb-4c5e-ab92-b5b4879721b2
# last-edited: 2026-10-20

"""Check status of dedup benchmark batch jobs and download results.

Usage:
    python3 scripts/dedup_bench_check.py [run_dir]

Loads API keys from scripts/.env automatically.
"""

import json
import os
import sys
from pathlib import Path

from openai import OpenAI

# Load .env from scripts/ directory
env_path = Path(__file__).parent / ".env"
if env_path.exists():
    for line in env_path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())


def check_batches(run_dir: str, api_key: str, label: str):
    """Check all batches for a given run directory and API key."""
    jobs_file = Path(run_dir) / "batch_jobs.json"
    if not jobs_file.exists():
        print(f"No batch_jobs.json in {run_dir}")
        return

    jobs = json.loads(jobs_file.read_text())
    client = OpenAI(api_key=api_key)

    completed = 0
    failed = 0
    pending = 0

    print(f"\n{'=' * 80}")
    print(f"Checking {len(jobs)} batches with key: {label}")
    print(f"Run dir: {run_dir}")
    print(f"{'=' * 80}\n")

    header = f"{'Model':<15} {'Prompt':<18} {'Mode':<6} {'Status':<12} {'OK':>4} {'Fail':>4} {'Total':>5} {'Error'}"
    print(header)
    print("-" * len(header) + "-" * 40)

    for job in jobs:
        bid = job["batch_id"]
        model = job["config"]["model"]
        mode = job["mode"]
        prompt = job["config"]["prompt_variant"]

        try:
            batch = client.batches.retrieve(bid)
        except Exception as e:
            print(
                f"{model:<15} {prompt:<18} {mode:<6} {'ERROR':<12} {'':>4} {'':>4} {'':>5} {str(e)[:60]}"
            )
            failed += 1
            continue

        rc = batch.request_counts
        status = str(batch.status)
        ok = rc.completed if rc else 0
        fail = rc.failed if rc else 0
        total = rc.total if rc else 0

        err_msg = ""
        if batch.errors and batch.errors.data:
            err_msg = batch.errors.data[0].message[:80]

        print(
            f"{model:<15} {prompt:<18} {mode:<6} {status:<12} {ok:>4} {fail:>4} {total:>5} {err_msg}"
        )

        # Download error details for failed requests
        if batch.error_file_id and fail > 0:
            try:
                content = client.files.content(batch.error_file_id)
                for line in content.text.strip().split("\n")[:3]:
                    e = json.loads(line)
                    resp_err = e.get("response", {}).get("body", {}).get("error", {})
                    code = resp_err.get("code", "?")
                    msg = resp_err.get("message", "?")[:120]
                    print(f"  -> {code}: {msg}")
            except Exception as ex:
                print(f"  -> could not fetch error file: {ex}")

        # Download successful results
        if status == "completed" and batch.output_file_id and ok > 0:
            out_dir = Path(job["run_dir"])
            out_dir.mkdir(parents=True, exist_ok=True)
            try:
                content = client.files.content(batch.output_file_id)
                out_file = out_dir / "batch_output.jsonl"
                out_file.write_text(content.text)

                # Parse and count suggestions
                suggestions = 0
                actions = {}
                for line in content.text.strip().split("\n"):
                    resp = json.loads(line)
                    if resp.get("response", {}).get("status_code") != 200:
                        continue
                    body = resp["response"]["body"]
                    choices = body.get("choices", [])
                    if not choices:
                        continue
                    msg_content = choices[0].get("message", {}).get("content", "")
                    try:
                        parsed = json.loads(msg_content)
                        for s in parsed.get("suggestions", []):
                            suggestions += 1
                            act = s.get("action", "unknown")
                            actions[act] = actions.get(act, 0) + 1
                    except json.JSONDecodeError:
                        pass

                # Save stats
                stats = {
                    "model": model,
                    "prompt": prompt,
                    "mode": mode,
                    "suggestions": suggestions,
                    "actions": actions,
                }
                (out_dir / "parsed_stats.json").write_text(json.dumps(stats, indent=2))
                print(f"  -> Downloaded: {suggestions} suggestions, actions: {actions}")
                completed += 1
            except Exception as ex:
                print(f"  -> download error: {ex}")
        elif status == "completed":
            completed += 1
        elif status in ("failed", "expired", "cancelled"):
            failed += 1
        else:
            pending += 1

    print(f"\nSummary: {completed} completed, {failed} failed, {pending} pending")
    if pending > 0:
        print("Run again later to check remaining jobs.")


def main():
    # Find run directory
    bench_dir = Path("testdata/dedup-bench")

    if len(sys.argv) > 1:
        run_dir = sys.argv[1]
    else:
        # Find most recent run
        if not bench_dir.exists():
            print("No testdata/dedup-bench directory found")
            sys.exit(1)
        runs = sorted([d for d in bench_dir.iterdir() if d.is_dir()], reverse=True)
        if not runs:
            print("No runs found in testdata/dedup-bench/")
            sys.exit(1)
        run_dir = str(runs[0])
        print(f"Using most recent run: {run_dir}")

    # Try both keys
    primary_key = os.environ.get("OPENAI_API_KEY", "")
    old_key = os.environ.get("OPENAI_API_KEY_OLD", "")

    if old_key:
        check_batches(run_dir, old_key, "OLD key (personal org)")

    if primary_key and primary_key != old_key:
        check_batches(run_dir, primary_key, "PRIMARY key (audiobook-organizer project)")


if __name__ == "__main__":
    main()
