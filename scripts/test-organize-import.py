#!/usr/bin/env python3
# file: scripts/test-organize-import.py
# version: 2.0.1
# guid: 9a8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d
# last-edited: 2026-10-20

"""
Test script to validate audiobook organizer scanning and renaming logic.

This script processes a large file list (file-list-books) and simulates
how the audiobook-organizer would identify, categorize, and rename files.
It generates a detailed report showing:
- Books with all their constituent files grouped together
- Duplicate/version detection linking different editions
- Extracted metadata (title, author, series, position)
- Proposed organized path structure
- Statistics and summary
"""

import argparse
import contextlib
import json
import os
import re
import sys
from collections import defaultdict
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class FileInfo:
    """Represents a single file that is part of an audiobook."""

    original_path: str
    filename: str
    extension: str
    size_mb: float = 0.0
    is_chapter: bool = False
    chapter_number: int | None = None


@dataclass
class BookVersion:
    """Represents a specific version of an audiobook (different format/quality/edition)."""

    version_id: str
    files: list[FileInfo] = field(default_factory=list)
    primary_file: str | None = None  # The main file (if multi-file book)
    format: str = ""  # m4b, mp3, etc.
    total_files: int = 0
    total_size_mb: float = 0.0
    is_multi_file: bool = False
    quality_notes: str = ""  # e.g., "Unabridged", "Remastered"


@dataclass
class BookMetadata:
    """Represents an audiobook with all its versions and files."""

    book_id: str  # Unique identifier for this book
    title: str = ""
    author: str = ""
    series: str = ""
    position: int = 0
    narrator: str = ""
    confidence: str = "low"  # low, medium, high
    extraction_method: str = ""
    proposed_path: str = ""
    issues: list[str] = field(default_factory=list)
    versions: list[BookVersion] = field(default_factory=list)
    duplicate_group_id: str | None = None  # Links duplicates together


class SeriesPatternMatcher:
    """Pattern matching for series identification (mirrors Go matcher.go logic)."""

    # Patterns from internal/matcher/matcher.go
    PATTERNS = [
        # "Series - Title"
        (re.compile(r"(?i)(.*?)\s+-\s+(.+)"), "dash_separator"),
        # "Series 1: Title" or "Series 1 - Title"
        (re.compile(r"(?i)(.+?)\s+(\d+)(?:\s*:|\s+-)\s+(.+)"), "numbered_series"),
        # "Series Book 1: Title"
        (
            re.compile(r"(?i)(.+?)\s+Book\s+(\d+)(?:\s*:|\s+-)\s+(.+)"),
            "book_numbered",
        ),
        # "Series #1: Title"
        (re.compile(r"(?i)(.+?)\s+#(\d+)(?:\s*:|\s+-)\s+(.+)"), "hash_numbered"),
        # "Series Vol. 1: Title" or "Series Volume 1: Title"
        (
            re.compile(r"(?i)(.+?)\s+Vol(?:\.|ume)?\s+(\d+)(?:\s*:|\s+-)\s+(.+)"),
            "volume_numbered",
        ),
    ]

    SERIES_WORDS = ["trilogy", "series", "saga", "chronicles", "sequence", "collection"]

    @staticmethod
    def identify_series(title: str, file_path: str) -> tuple[str, int, str]:
        """
        Identify series name and position from title and file path.

        Returns:
            Tuple of (series_name, position, method_used)
        """
        if not title:
            title = Path(file_path).stem

        # Try pattern matching first
        for pattern, method in SeriesPatternMatcher.PATTERNS:
            match = pattern.match(title)
            if match and len(match.groups()) >= 2:
                series = match.group(1).strip()
                position = 0

                # Extract position based on pattern
                if method in ["numbered_series", "book_numbered", "hash_numbered"]:
                    with contextlib.suppress(ValueError, IndexError):
                        position = int(match.group(2))
                elif method == "volume_numbered" and len(match.groups()) >= 3:
                    with contextlib.suppress(ValueError, IndexError):
                        position = int(match.group(3))

                return series, position, f"pattern:{method}"

        # Check directory structure
        path_parts = Path(file_path).parts
        if len(path_parts) >= 2:
            parent_dir = path_parts[-2]
            author_dir = path_parts[-3] if len(path_parts) >= 3 else ""

            # If parent directory is not the author name, it might be a series
            if parent_dir != author_dir and len(parent_dir.split()) > 1:
                # Check for series keywords
                for word in SeriesPatternMatcher.SERIES_WORDS:
                    if word.lower() in parent_dir.lower():
                        return parent_dir, 0, "directory:keyword"

                # Check if parent dir name is in title (fuzzy match)
                if parent_dir.lower() in title.lower() or title.lower() in parent_dir.lower():
                    return parent_dir, 0, "directory:fuzzy"

        # Look for series patterns in title itself
        if ": " in title:
            parts = title.split(": ", 1)
            if len(parts) == 2:
                return parts[0], 0, "title:colon"

        if " - " in title:
            parts = title.split(" - ", 1)
            if len(parts) == 2:
                return parts[0], 0, "title:dash"

        return "", 0, "none"


class PathExtractor:
    """Extract metadata from file paths and filenames."""

    # Supported extensions from config
    SUPPORTED_EXTENSIONS = [".m4b", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus"]

    @staticmethod
    def extract_author_from_path(file_path: str) -> str:
        """Extract author name from directory structure."""
        path_parts = Path(file_path).parts

        # Common patterns:
        # /author/series/book.m4b
        # /author/book.m4b
        # /books/author/series/book.m4b

        # Skip root directories and common folder names
        skip_dirs = [
            "books",
            "audiobooks",
            "bt",
            "incomplete",
            "mnt",
            "bigdata",
            "data",
            "/",
        ]

        # Find first meaningful directory that's not a skip dir
        meaningful_dirs = []
        for i, part in enumerate(path_parts):
            if part.lower() not in skip_dirs and i < len(path_parts) - 1:  # Not the filename
                meaningful_dirs.append(part)

        if not meaningful_dirs:
            return ""

        # Try to extract author from first meaningful directory
        first_dir = meaningful_dirs[0]

        # Check if it looks like a book title like "Author - Year - Title"
        if " - " in first_dir and any(char.isdigit() for char in first_dir):
            author_match = re.match(r"^([^-]+)\s*-", first_dir)
            if author_match:
                author = author_match.group(1).strip()
                # Validate it's not just a title
                if not author.lower().startswith(("book", "chapter", "part")):
                    return author

        # Check if filename has author info we can use
        filename = Path(file_path).stem
        if " - " in filename:
            parts = filename.split(" - ")
            if len(parts) >= 3:
                # Might be "Author - Year - Title" or "Author - Series - Title"
                author = parts[0].strip()
                if not author.lower().startswith(("book", "chapter", "part")):
                    return author

        # Default to first meaningful directory if it doesn't look like a series name
        if not first_dir.lower().startswith(("book", "chapter", "part", "volume", "vol")):
            return first_dir

        return ""

    @staticmethod
    def extract_title_from_filename(filename: str) -> str:
        """Extract title from filename, removing extension and common patterns."""
        title = Path(filename).stem

        # Remove common patterns
        # "Chapter 01.mp3" -> not a title
        if re.match(r"(?i)^Chapter\s+\d+", title):
            return ""

        # Remove track numbers
        title = re.sub(r"^\d+[-_.\s]+", "", title)

        # Remove common suffixes
        title = re.sub(r"\s*\((?:Unabridged|Audiobook|Retail)\)$", "", title, flags=re.IGNORECASE)

        return title.strip()


class AudiobookScanner:
    """Main scanner that processes files and extracts metadata."""

    def __init__(self, supported_extensions: list[str] | None = None):
        """Initialize the scanner with supported file extensions."""
        self.supported_extensions = supported_extensions or PathExtractor.SUPPORTED_EXTENSIONS
        self.pattern_matcher = SeriesPatternMatcher()
        self.path_extractor = PathExtractor()

    def is_supported_file(self, file_path: str) -> bool:
        """Check if file has a supported audio extension."""
        ext = Path(file_path).suffix.lower()
        return ext in self.supported_extensions

    def scan_file(self, file_path: str) -> BookMetadata | None:
        """
        Scan a single file and extract metadata.

        Args:
            file_path: Absolute path to the audiobook file

        Returns:
            BookMetadata object or None if file should be skipped
        """
        if not self.is_supported_file(file_path):
            return None

        # Skip chapter files (multiple chapters in same directory = single book)
        filename = Path(file_path).name
        if re.match(r"(?i)^Chapter\s+\d+", filename):
            return None

        metadata = BookMetadata(
            original_path=file_path,
            filename=filename,
            extension=Path(file_path).suffix.lower(),
        )

        # Extract title from filename
        metadata.title = self.path_extractor.extract_title_from_filename(filename)

        # Extract author from path
        metadata.author = self.path_extractor.extract_author_from_path(file_path)

        # Identify series
        series, position, method = self.pattern_matcher.identify_series(metadata.title, file_path)
        metadata.series = series
        metadata.position = position
        metadata.extraction_method = method

        # Determine confidence
        metadata.confidence = self._calculate_confidence(metadata)

        # Generate proposed organized path
        metadata.proposed_path = self._generate_proposed_path(metadata)

        # Identify potential issues
        self._identify_issues(metadata)

        return metadata

    def _calculate_confidence(self, metadata: BookMetadata) -> str:
        """Calculate confidence level for extracted metadata."""
        score = 0

        if metadata.title:
            score += 1
        if metadata.author:
            score += 1
        if metadata.series:
            score += 1
        if metadata.position > 0:
            score += 1

        if score >= 4:
            return "high"
        elif score >= 2:
            return "medium"
        else:
            return "low"

    def _generate_proposed_path(self, metadata: BookMetadata) -> str:
        """Generate proposed organized path structure."""
        # Pattern: Author/Series/Book_Title.ext
        # Or: Author/Book_Title.ext (if no series)

        parts = []

        if metadata.author:
            parts.append(self._sanitize_filename(metadata.author))
        else:
            parts.append("Unknown_Author")

        if metadata.series:
            series_part = self._sanitize_filename(metadata.series)
            parts.append(series_part)

        # Build filename
        filename_parts = []
        if metadata.series and metadata.position > 0:
            filename_parts.append(f"{metadata.position:02d}")
        if metadata.title:
            filename_parts.append(self._sanitize_filename(metadata.title))
        else:
            filename_parts.append("Unknown_Title")

        filename = " - ".join(filename_parts) + metadata.extension
        parts.append(filename)

        return str(Path(*parts))

    @staticmethod
    def _sanitize_filename(name: str) -> str:
        """Sanitize a string for use in a filename."""
        # Remove or replace invalid characters
        name = re.sub(r'[<>:"/\\|?*]', "", name)
        # Replace multiple spaces with single space
        name = re.sub(r"\s+", " ", name)
        # Remove leading/trailing spaces and dots
        name = name.strip(". ")
        return name

    def _identify_issues(self, metadata: BookMetadata) -> None:
        """Identify potential issues with the extracted metadata."""
        if not metadata.title or metadata.title == "Unknown_Title":
            metadata.issues.append("Missing or unclear title")

        if not metadata.author or metadata.author == "Unknown_Author":
            metadata.issues.append("Missing or unclear author")

        if metadata.confidence == "low":
            metadata.issues.append("Low confidence in metadata extraction")

        if metadata.extraction_method == "none":
            metadata.issues.append("No series pattern detected")


class TestReportGenerator:
    """Generate reports from scanned audiobook metadata."""

    def __init__(self):
        """Initialize the report generator."""
        self.stats = defaultdict(int)

    def generate_report(self, books: list[BookMetadata], output_file: str | None = None) -> dict:
        """
        Generate comprehensive report from scanned books.

        Args:
            books: List of BookMetadata objects
            output_file: Optional path to write JSON report

        Returns:
            Dictionary containing report data
        """
        report = {
            "summary": self._generate_summary(books),
            "statistics": self._generate_statistics(books),
            "books": [self._book_to_dict(book) for book in books],
            "issues_summary": self._generate_issues_summary(books),
        }

        if output_file:
            with open(output_file, "w", encoding="utf-8") as f:
                json.dump(report, f, indent=2)

        return report

    def _generate_summary(self, books: list[BookMetadata]) -> dict:
        """Generate high-level summary statistics."""
        return {
            "total_files_processed": len(books),
            "books_with_series": sum(1 for b in books if b.series),
            "books_with_position": sum(1 for b in books if b.position > 0),
            "high_confidence": sum(1 for b in books if b.confidence == "high"),
            "medium_confidence": sum(1 for b in books if b.confidence == "medium"),
            "low_confidence": sum(1 for b in books if b.confidence == "low"),
            "books_with_issues": sum(1 for b in books if b.issues),
        }

    def _generate_statistics(self, books: list[BookMetadata]) -> dict:
        """Generate detailed statistics."""
        extensions = defaultdict(int)
        extraction_methods = defaultdict(int)
        authors = defaultdict(int)
        series = defaultdict(int)

        for book in books:
            extensions[book.extension] += 1
            extraction_methods[book.extraction_method] += 1
            if book.author:
                authors[book.author] += 1
            if book.series:
                series[book.series] += 1

        return {
            "by_extension": dict(extensions),
            "by_extraction_method": dict(extraction_methods),
            "top_authors": dict(sorted(authors.items(), key=lambda x: x[1], reverse=True)[:20]),
            "top_series": dict(sorted(series.items(), key=lambda x: x[1], reverse=True)[:20]),
        }

    def _generate_issues_summary(self, books: list[BookMetadata]) -> dict:
        """Generate summary of common issues."""
        issue_counts = defaultdict(int)

        for book in books:
            for issue in book.issues:
                issue_counts[issue] += 1

        return dict(sorted(issue_counts.items(), key=lambda x: x[1], reverse=True))

    @staticmethod
    def _book_to_dict(book: BookMetadata) -> dict:
        """Convert BookMetadata to dictionary for JSON serialization."""
        return {
            "original_path": book.original_path,
            "filename": book.filename,
            "extension": book.extension,
            "title": book.title,
            "author": book.author,
            "series": book.series,
            "position": book.position,
            "confidence": book.confidence,
            "extraction_method": book.extraction_method,
            "proposed_path": book.proposed_path,
            "issues": book.issues,
        }

    def print_summary(self, report: dict) -> None:
        """Print a human-readable summary of the report."""
        summary = report["summary"]
        stats = report["statistics"]

        print("\n" + "=" * 80)
        print("AUDIOBOOK ORGANIZER TEST REPORT")
        print("=" * 80)

        print("\nSUMMARY:")
        print(f"  Total files processed: {summary['total_files_processed']:,}")
        print(f"  Books with series identified: {summary['books_with_series']:,}")
        print(f"  Books with series position: {summary['books_with_position']:,}")
        print("\nCONFIDENCE LEVELS:")
        print(f"  High confidence: {summary['high_confidence']:,}")
        print(f"  Medium confidence: {summary['medium_confidence']:,}")
        print(f"  Low confidence: {summary['low_confidence']:,}")
        print(f"\n  Books with issues: {summary['books_with_issues']:,}")

        print("\nFILE FORMATS:")
        for ext, count in sorted(stats["by_extension"].items()):
            print(f"  {ext}: {count:,}")

        print("\nEXTRACTION METHODS:")
        for method, count in sorted(
            stats["by_extraction_method"].items(), key=lambda x: x[1], reverse=True
        ):
            print(f"  {method}: {count:,}")

        print("\nTOP 10 AUTHORS:")
        for author, count in list(stats["top_authors"].items())[:10]:
            print(f"  {author}: {count:,} books")

        print("\nTOP 10 SERIES:")
        for series, count in list(stats["top_series"].items())[:10]:
            print(f"  {series}: {count:,} books")

        if "issues_summary" in report and report["issues_summary"]:
            print("\nCOMMON ISSUES:")
            for issue, count in list(report["issues_summary"].items())[:5]:
                print(f"  {issue}: {count:,}")

        print("\n" + "=" * 80)


def main():
    """Main entry point for the test script."""
    parser = argparse.ArgumentParser(
        description="Test audiobook organizer scanning and import logic"
    )
    parser.add_argument(
        "file_list",
        help="Path to file list (e.g., file-list-books)",
    )
    parser.add_argument(
        "--output",
        "-o",
        help="Output JSON file for detailed report",
        default="organize-test-report.json",
    )
    parser.add_argument(
        "--limit",
        "-l",
        type=int,
        help="Limit number of files to process (for testing)",
    )
    parser.add_argument(
        "--sample",
        "-s",
        type=int,
        help="Output sample of books to stdout",
        default=10,
    )

    args = parser.parse_args()

    # Check if file exists
    if not os.path.exists(args.file_list):
        print(f"Error: File not found: {args.file_list}", file=sys.stderr)
        sys.exit(1)

    print(f"Reading file list from: {args.file_list}")

    # Read file list
    with open(args.file_list, encoding="utf-8") as f:
        file_paths = [line.strip() for line in f if line.strip()]

    if args.limit:
        file_paths = file_paths[: args.limit]

    print(f"Processing {len(file_paths):,} files...")

    # Initialize scanner
    scanner = AudiobookScanner()

    # Scan all files
    books = []
    for i, file_path in enumerate(file_paths):
        if i % 1000 == 0 and i > 0:
            print(f"  Processed {i:,} files...")

        metadata = scanner.scan_file(file_path)
        if metadata:
            books.append(metadata)

    print(f"Scanned {len(books):,} valid audiobook files")

    # Generate report
    report_gen = TestReportGenerator()
    report = report_gen.generate_report(books, args.output)

    # Print summary
    report_gen.print_summary(report)

    # Print sample books
    if args.sample and books:
        print(f"\nSAMPLE OF {min(args.sample, len(books))} BOOKS:")
        print("-" * 80)
        for book in books[: args.sample]:
            print(f"\nOriginal: {book.original_path}")
            print(f"  Title: {book.title}")
            print(f"  Author: {book.author}")
            print(f"  Series: {book.series} (#{book.position if book.position else 'N/A'})")
            print(f"  Method: {book.extraction_method}")
            print(f"  Confidence: {book.confidence}")
            print(f"  Proposed: {book.proposed_path}")
            if book.issues:
                print(f"  Issues: {', '.join(book.issues)}")

    print(f"\nDetailed report written to: {args.output}")


if __name__ == "__main__":
    main()
