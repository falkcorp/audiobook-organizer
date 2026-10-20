#!/usr/bin/env python3
# file: scripts/openai_parsing_test.py
# version: 1.1.4
# guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
# last-edited: 2026-10-20

"""
OpenAI Audiobook Filename Parsing Test Script

This script tests the OpenAI integration for parsing audiobook filenames by:
1. Selecting 5,000 random files from multiple source directories
2. Ensuring balanced representation across all source paths
3. Testing OpenAI parsing with concurrent batch requests
4. Providing comprehensive metrics and validation
5. Generating detailed reports on parsing accuracy and performance

Usage:
    python scripts/test_openai_parsing.py [--file-list PATH] [--num-samples N] [--batch-size N] [--workers N]

Environment Variables:
    OPENAI_API_KEY: Required - OpenAI API key for authentication
"""

import argparse
import json
import os
import random
import sys
import time
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import asdict, dataclass
from datetime import datetime

if os.environ.get("PYTEST_CURRENT_TEST"):
    try:
        import pytest

        pytest.skip(
            "CLI utility for manual OpenAI parsing; skipped in automated test runs",
            allow_module_level=True,
        )
    except Exception:
        # Continue without pytest available; script remains importable
        pass

try:
    from openai import OpenAI
except ImportError:
    OpenAI = None

try:
    from dotenv import load_dotenv
except ImportError:
    print("WARNING: python-dotenv not found. Install with: pip install python-dotenv")
    print("Continuing without .env file support...")

    def load_dotenv() -> None:
        """Placeholder loader when python-dotenv is unavailable."""
        return None


@dataclass
class ParsedMetadata:
    """Structured metadata extracted from a filename."""

    title: str | None = None
    author: str | None = None
    series: str | None = None
    series_number: int | None = None
    narrator: str | None = None
    publisher: str | None = None
    year: int | None = None
    confidence: str = "low"


@dataclass
class TestResult:
    """Result of testing a single filename."""

    filename: str
    source_path: str
    parsed_metadata: ParsedMetadata | None
    success: bool
    error: str | None
    response_time: float
    tokens_used: int | None


@dataclass
class TestMetrics:
    """Overall test metrics and statistics."""

    total_files: int
    successful_parses: int
    failed_parses: int
    total_time: float
    total_tokens: int
    avg_response_time: float
    confidence_breakdown: dict[str, int]
    source_path_breakdown: dict[str, int]
    error_types: dict[str, int]


class OpenAIFilenameParser:
    """OpenAI-based audiobook filename parser with batch processing support."""

    def __init__(self, api_key: str, model: str = "gpt-4o-mini", max_workers: int = 10):
        """
        Initialize the OpenAI parser.

        Args:
            api_key: OpenAI API key
            model: Model to use (default: gpt-4o-mini for cost-effectiveness)
            max_workers: Maximum concurrent workers for parallel requests
        """
        self.client = OpenAI(api_key=api_key)
        self.model = model
        self.max_workers = max_workers
        self.system_prompt = """You are an expert at parsing audiobook filenames. Extract structured metadata from the filename.

Common patterns:
- "Title - Author" or "Author - Title"
- "Author - Series Name Book N - Title"
- "Title (Series Name #N)" or "Title (Series Name, Book N)"
- May include narrator: "Title - Author - Narrator" or "Title - Author - read by Narrator"
- May include year: "Title (2020)" or "Title - Author (2020)"
- May include publisher or version info in brackets or parentheses
- File extensions and folder paths should be ignored

Return ONLY valid JSON with these fields (omit if not found):
{
  "title": "book title",
  "author": "author name",
  "series": "series name",
  "series_number": 1,
  "narrator": "narrator name",
  "publisher": "publisher name",
  "year": 2020,
  "confidence": "high|medium|low"
}

Set confidence based on:
- high: Clear, unambiguous structure with title and author
- medium: Some ambiguity but reasonable interpretation
- low: Very unclear or minimal information"""

    def parse_filename(self, filename: str) -> tuple[ParsedMetadata | None, float, int | None]:
        """
        Parse a filename using OpenAI.

        Args:
            filename: The filename to parse

        Returns:
            Tuple of (parsed_metadata, response_time, tokens_used)
        """
        start_time = time.time()

        try:
            # Extract just the filename from full path
            basename = os.path.basename(filename)

            user_prompt = f"Parse this audiobook filename:\n\n{basename}"

            response = self.client.chat.completions.create(
                model=self.model,
                messages=[
                    {"role": "system", "content": self.system_prompt},
                    {"role": "user", "content": user_prompt},
                ],
                response_format={"type": "json_object"},
                temperature=0.3,
                max_tokens=500,
            )

            response_time = time.time() - start_time

            # Parse the JSON response
            content = response.choices[0].message.content
            data = json.loads(content)

            # Create ParsedMetadata object
            metadata = ParsedMetadata(
                title=data.get("title"),
                author=data.get("author"),
                series=data.get("series"),
                series_number=data.get("series_number"),
                narrator=data.get("narrator"),
                publisher=data.get("publisher"),
                year=data.get("year"),
                confidence=data.get("confidence", "low"),
            )

            tokens_used = response.usage.total_tokens if response.usage else None

            return metadata, response_time, tokens_used

        except Exception as e:
            response_time = time.time() - start_time
            basename = os.path.basename(filename)
            print(f"Error parsing '{basename}': {e}")
            return None, response_time, None

    def parse_batch(
        self, filenames: list[str]
    ) -> list[tuple[str, ParsedMetadata | None, float, int | None]]:
        """
        Parse multiple filenames concurrently using thread pool.

        Args:
            filenames: List of filenames to parse

        Returns:
            List of tuples: (filename, parsed_metadata, response_time, tokens_used)
        """
        results = []

        with ThreadPoolExecutor(max_workers=self.max_workers) as executor:
            # Submit all tasks
            future_to_filename = {
                executor.submit(self.parse_filename, filename): filename for filename in filenames
            }

            # Collect results as they complete
            for future in as_completed(future_to_filename):
                filename = future_to_filename[future]
                try:
                    metadata, response_time, tokens = future.result()
                    results.append((filename, metadata, response_time, tokens))
                except Exception as e:
                    print(f"Exception processing {filename}: {e}")
                    results.append((filename, None, 0.0, None))

        return results


def load_file_list(file_path: str) -> list[str]:
    """
    Load the file list from disk.

    Args:
        file_path: Path to the file containing the list

    Returns:
        List of file paths
    """
    print(f"Loading file list from {file_path}...")
    with open(file_path) as f:
        files = [line.strip() for line in f if line.strip()]
    print(f"Loaded {len(files):,} files")
    return files


def categorize_by_source(files: list[str]) -> dict[str, list[str]]:
    """
    Categorize files by their source directory.

    Args:
        files: List of file paths

    Returns:
        Dictionary mapping source path to list of files
    """
    categorized = defaultdict(list)

    # Define the source paths we're interested in
    source_paths = [
        "/mnt/bigdata/books/abooks",
        "/mnt/bigdata/books/newbooks",
        "/mnt/bigdata/books/itunes",
    ]

    for file_path in files:
        for source in source_paths:
            if file_path.startswith(source):
                categorized[source].append(file_path)
                break
        else:
            # File doesn't match any of our target sources
            categorized["other"].append(file_path)

    return dict(categorized)


def select_balanced_sample(
    categorized_files: dict[str, list[str]], total_samples: int
) -> list[str]:
    """
    Select a balanced sample of files from all source directories.

    Args:
        categorized_files: Files categorized by source directory
        total_samples: Total number of samples to select

    Returns:
        List of selected file paths
    """
    print(f"\nSelecting {total_samples:,} balanced samples...")

    # Calculate how many samples per source
    sources = [s for s in categorized_files if s != "other"]
    samples_per_source = total_samples // len(sources)
    remainder = total_samples % len(sources)

    selected = []

    for idx, source in enumerate(sources):
        available = categorized_files[source]
        # Give the remainder to the first sources
        to_select = samples_per_source + (1 if idx < remainder else 0)
        # Don't select more than available
        to_select = min(to_select, len(available))

        selected_from_source = random.sample(available, to_select)
        selected.extend(selected_from_source)

        print(f"  {source}: selected {to_select:,} / {len(available):,} files")

    # Shuffle the final selection
    random.shuffle(selected)

    print(f"\nTotal selected: {len(selected):,} files")
    return selected


def run_parsing_tests(
    parser: OpenAIFilenameParser,
    files: list[str],
    output_dir: str = "test_results",
    batch_size: int = 50,
) -> tuple[list[TestResult], TestMetrics]:
    """
    Run parsing tests on all selected files using batch processing.

    Args:
        parser: OpenAI parser instance
        files: List of files to test
        output_dir: Directory to save results
        batch_size: Number of files to process in each batch

    Returns:
        Tuple of (test_results, metrics)
    """
    print(f"\nStarting parsing tests on {len(files):,} files...")
    print(f"Using batch size: {batch_size} with {parser.max_workers} concurrent workers")
    print(f"Results will be saved to: {output_dir}/")

    os.makedirs(output_dir, exist_ok=True)

    results = []
    start_time = time.time()

    # Track metrics
    successful = 0
    failed = 0
    total_tokens = 0
    confidence_counts = Counter()
    source_counts = Counter()
    error_types = Counter()
    total_response_time = 0.0

    # Process files in batches
    total_batches = (len(files) + batch_size - 1) // batch_size

    for batch_idx in range(0, len(files), batch_size):
        batch_files = files[batch_idx : batch_idx + batch_size]
        current_batch_num = (batch_idx // batch_size) + 1

        print(
            f"\nProcessing batch {current_batch_num}/{total_batches} ({len(batch_files)} files)..."
        )

        # Parse batch concurrently
        batch_results = parser.parse_batch(batch_files)

        # Process batch results
        for file_path, metadata, response_time, tokens in batch_results:
            # Extract source path
            source_path = "other"
            for source in [
                "/mnt/bigdata/books/abooks",
                "/mnt/bigdata/books/newbooks",
                "/mnt/bigdata/books/itunes",
            ]:
                if file_path.startswith(source):
                    source_path = source
                    break

            # Create result
            if metadata:
                result = TestResult(
                    filename=os.path.basename(file_path),
                    source_path=source_path,
                    parsed_metadata=metadata,
                    success=True,
                    error=None,
                    response_time=response_time,
                    tokens_used=tokens,
                )
                successful += 1
                confidence_counts[metadata.confidence] += 1
                if tokens:
                    total_tokens += tokens
            else:
                result = TestResult(
                    filename=os.path.basename(file_path),
                    source_path=source_path,
                    parsed_metadata=None,
                    success=False,
                    error="Parsing failed",
                    response_time=response_time,
                    tokens_used=None,
                )
                failed += 1
                error_types["parsing_failed"] += 1

            results.append(result)
            source_counts[source_path] += 1
            total_response_time += response_time

        # Progress update after each batch
        processed = batch_idx + len(batch_files)
        elapsed = time.time() - start_time
        rate = processed / elapsed if elapsed > 0 else 0
        remaining = (len(files) - processed) / rate if rate > 0 else 0

        print(
            f"Progress: {processed}/{len(files):,} ({processed / len(files) * 100:.1f}%) | "
            f"Success: {successful:,} | Failed: {failed:,} | "
            f"Rate: {rate:.1f} files/sec | "
            f"ETA: {remaining:.0f}s"
        )

    total_time = time.time() - start_time
    avg_response_time = total_response_time / len(files) if files else 0

    # Create metrics
    metrics = TestMetrics(
        total_files=len(files),
        successful_parses=successful,
        failed_parses=failed,
        total_time=total_time,
        total_tokens=total_tokens,
        avg_response_time=avg_response_time,
        confidence_breakdown=dict(confidence_counts),
        source_path_breakdown=dict(source_counts),
        error_types=dict(error_types),
    )

    return results, metrics


def save_results(results: list[TestResult], metrics: TestMetrics, output_dir: str):
    """
    Save test results to disk.

    Args:
        results: List of test results
        metrics: Test metrics
        output_dir: Directory to save results
    """
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    # Save detailed results as JSON
    results_file = os.path.join(output_dir, f"parsing_results_{timestamp}.json")
    with open(results_file, "w") as f:
        json.dump([asdict(r) for r in results], f, indent=2, default=str)
    print(f"\nSaved detailed results to: {results_file}")

    # Save metrics as JSON
    metrics_file = os.path.join(output_dir, f"metrics_{timestamp}.json")
    with open(metrics_file, "w") as f:
        json.dump(asdict(metrics), f, indent=2)
    print(f"Saved metrics to: {metrics_file}")

    # Generate human-readable report
    report_file = os.path.join(output_dir, f"report_{timestamp}.txt")
    with open(report_file, "w") as f:
        f.write("=" * 80 + "\n")
        f.write("OPENAI AUDIOBOOK FILENAME PARSING TEST REPORT\n")
        f.write("=" * 80 + "\n\n")

        f.write(f"Test Date: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n")
        f.write(f"Total Files Tested: {metrics.total_files:,}\n")
        f.write(f"Total Test Duration: {metrics.total_time:.2f} seconds\n\n")

        f.write("PARSING RESULTS\n")
        f.write("-" * 80 + "\n")
        f.write(
            f"Successful: {metrics.successful_parses:,} ({metrics.successful_parses / metrics.total_files * 100:.1f}%)\n"
        )
        f.write(
            f"Failed: {metrics.failed_parses:,} ({metrics.failed_parses / metrics.total_files * 100:.1f}%)\n\n"
        )

        f.write("CONFIDENCE BREAKDOWN\n")
        f.write("-" * 80 + "\n")
        for confidence, count in sorted(metrics.confidence_breakdown.items()):
            pct = count / metrics.successful_parses * 100 if metrics.successful_parses > 0 else 0
            f.write(f"  {confidence.capitalize()}: {count:,} ({pct:.1f}%)\n")
        f.write("\n")

        f.write("SOURCE PATH DISTRIBUTION\n")
        f.write("-" * 80 + "\n")
        for source, count in sorted(metrics.source_path_breakdown.items()):
            pct = count / metrics.total_files * 100
            f.write(f"  {source}: {count:,} ({pct:.1f}%)\n")
        f.write("\n")

        f.write("PERFORMANCE METRICS\n")
        f.write("-" * 80 + "\n")
        f.write(f"Average Response Time: {metrics.avg_response_time:.3f} seconds\n")
        f.write(f"Total Tokens Used: {metrics.total_tokens:,}\n")
        if metrics.successful_parses > 0:
            f.write(
                f"Avg Tokens per Parse: {metrics.total_tokens / metrics.successful_parses:.1f}\n"
            )
        f.write(f"Processing Rate: {metrics.total_files / metrics.total_time:.2f} files/sec\n\n")

        if metrics.error_types:
            f.write("ERROR BREAKDOWN\n")
            f.write("-" * 80 + "\n")
            for error_type, count in sorted(metrics.error_types.items()):
                pct = count / metrics.total_files * 100
                f.write(f"  {error_type}: {count:,} ({pct:.1f}%)\n")
            f.write("\n")

        # Sample successful parses
        f.write("SAMPLE SUCCESSFUL PARSES\n")
        f.write("-" * 80 + "\n")
        successful_results = [r for r in results if r.success and r.parsed_metadata]
        for result in random.sample(successful_results, min(20, len(successful_results))):
            f.write(f"\nFilename: {result.filename}\n")
            f.write(f"Source: {result.source_path}\n")
            if result.parsed_metadata:
                f.write(f"  Title: {result.parsed_metadata.title}\n")
                f.write(f"  Author: {result.parsed_metadata.author}\n")
                if result.parsed_metadata.series:
                    f.write(f"  Series: {result.parsed_metadata.series}")
                    if result.parsed_metadata.series_number:
                        f.write(f" #{result.parsed_metadata.series_number}")
                    f.write("\n")
                if result.parsed_metadata.narrator:
                    f.write(f"  Narrator: {result.parsed_metadata.narrator}\n")
                if result.parsed_metadata.year:
                    f.write(f"  Year: {result.parsed_metadata.year}\n")
                f.write(f"  Confidence: {result.parsed_metadata.confidence}\n")
            f.write(f"  Response Time: {result.response_time:.3f}s\n")
            if result.tokens_used:
                f.write(f"  Tokens: {result.tokens_used}\n")

    print(f"Saved human-readable report to: {report_file}")

    # Print summary to console
    print("\n" + "=" * 80)
    print("TEST SUMMARY")
    print("=" * 80)
    print(f"Total Files: {metrics.total_files:,}")
    print(
        f"Successful: {metrics.successful_parses:,} ({metrics.successful_parses / metrics.total_files * 100:.1f}%)"
    )
    print(
        f"Failed: {metrics.failed_parses:,} ({metrics.failed_parses / metrics.total_files * 100:.1f}%)"
    )
    print(f"Total Time: {metrics.total_time:.2f}s")
    print(f"Avg Response: {metrics.avg_response_time:.3f}s")
    print(f"Total Tokens: {metrics.total_tokens:,}")
    print("\nConfidence Breakdown:")
    for confidence, count in sorted(metrics.confidence_breakdown.items()):
        pct = count / metrics.successful_parses * 100 if metrics.successful_parses > 0 else 0
        print(f"  {confidence.capitalize()}: {count:,} ({pct:.1f}%)")
    print("=" * 80)


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(description="Test OpenAI parsing on audiobook filenames")
    parser.add_argument(
        "--file-list",
        default="/Users/jdfalk/repos/scratch/file-list-books",
        help="Path to file containing list of audiobook files",
    )
    parser.add_argument(
        "--num-samples",
        type=int,
        default=5000,
        help="Number of files to sample and test (default: 5000)",
    )
    parser.add_argument(
        "--output-dir",
        default="test_results",
        help="Directory to save test results (default: test_results)",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=50,
        help="Number of files to process in each batch (default: 50)",
    )
    parser.add_argument(
        "--workers",
        type=int,
        default=10,
        help="Number of concurrent workers for parallel requests (default: 10)",
    )
    parser.add_argument("--seed", type=int, help="Random seed for reproducible sampling")

    args = parser.parse_args()

    # Set random seed if provided
    if args.seed:
        random.seed(args.seed)
        print(f"Using random seed: {args.seed}")

    # Load environment variables
    load_dotenv()

    if OpenAI is None:
        print("ERROR: openai package not found. Install with: pip install openai")
        return 1

    # Get API key
    api_key = os.getenv("OPENAI_API_KEY")
    if not api_key:
        print("ERROR: OPENAI_API_KEY not found in environment")
        print("Please set it in .env file or as environment variable")
        return 1

    print("=" * 80)
    print("OPENAI AUDIOBOOK FILENAME PARSING TEST")
    print("=" * 80)
    print("Configuration:")
    print(f"  File List: {args.file_list}")
    print(f"  Sample Size: {args.num_samples:,}")
    print(f"  Batch Size: {args.batch_size}")
    print(f"  Concurrent Workers: {args.workers}")
    print(f"  Output Directory: {args.output_dir}")
    print("=" * 80)

    # Load files
    all_files = load_file_list(args.file_list)

    # Categorize by source
    categorized = categorize_by_source(all_files)

    print("\nSource Directory Breakdown:")
    for source, files in sorted(categorized.items()):
        print(f"  {source}: {len(files):,} files")

    # Select balanced sample
    selected_files = select_balanced_sample(categorized, args.num_samples)

    # Initialize parser
    print("\nInitializing OpenAI parser...")
    openai_parser = OpenAIFilenameParser(api_key=api_key, max_workers=args.workers)
    print(f"Using model: {openai_parser.model}")
    print(f"Max concurrent workers: {openai_parser.max_workers}")

    # Run tests
    results, metrics = run_parsing_tests(
        openai_parser, selected_files, args.output_dir, batch_size=args.batch_size
    )

    # Save results
    save_results(results, metrics, args.output_dir)

    print("\nTest complete!")
    return 0


if __name__ == "__main__":
    sys.exit(main())
