#!/usr/bin/env python3
# file: scripts/test-organize-import-v2.py
# version: 2.0.1
# guid: c1d2e3f4-a5b6-7c8d-9e0f-1a2b3c4d5e6f

"""
Test script to validate audiobook organizer scanning and renaming logic (Version 2).

This script processes large file lists and groups files by book, handling:
- Multiple files per book (chapters, multi-part audiobooks)
- Duplicate detection (same book in different formats/quality)
- Version grouping (links all versions of the same book)
- Comprehensive statistics and reporting

Improvements over V1:
- Groups all files belonging to the same book
- Detects and links duplicate versions intelligently
- Shows book-centric view instead of file-centric
- Better handling of multi-file audiobooks
"""

import argparse
import hashlib
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
    is_chapter: bool = False
    chapter_number: int | None = None


@dataclass
class BookVersion:
    """Represents a specific version of an audiobook (different format/quality/edition)."""

    version_id: str
    files: list[FileInfo] = field(default_factory=list)
    primary_file: str | None = None  # The main/first file
    format: str = ""  # Primary format (m4b, mp3, etc.)
    total_files: int = 0
    is_multi_file: bool = False
    base_directory: str = ""  # Common directory for all files in this version


@dataclass
class BookMetadata:
    """Represents an audiobook with all its versions and files."""

    book_id: str  # Unique identifier for this book
    title: str = ""
    author: str = ""
    series: str = ""
    position: int = 0
    confidence: str = "low"  # low, medium, high
    extraction_method: str = ""
    proposed_path: str = ""
    issues: list[str] = field(default_factory=list)
    versions: list[BookVersion] = field(default_factory=list)
    duplicate_group_id: str | None = None  # Links duplicates together
    total_versions: int = 0


class SeriesPatternMatcher:
    """Pattern matching for series identification (mirrors Go matcher.go logic)."""

    PATTERNS = [
        (re.compile(r"(?i)(.*?)\s+-\s+(.+)"), "dash_separator"),
        (re.compile(r"(?i)(.+?)\s+(\d+)(?:\s*:|\s+-)\s+(.+)"), "numbered_series"),
        (re.compile(r"(?i)(.+?)\s+Book\s+(\d+)(?:\s*:|\s+-)\s+(.+)"), "book_numbered"),
        (re.compile(r"(?i)(.+?)\s+#(\d+)(?:\s*:|\s+-)\s+(.+)"), "hash_numbered"),
        (
            re.compile(r"(?i)(.+?)\s+Vol(?:\.|ume)?\s+(\d+)(?:\s*:|\s+-)\s+(.+)"),
            "volume_numbered",
        ),
    ]

    SERIES_WORDS = ["trilogy", "series", "saga", "chronicles", "sequence", "collection"]

    @staticmethod
    def identify_series(title: str, file_path: str) -> tuple[str, int, str]:
        """Identify series name and position from title and file path."""
        if not title:
            title = Path(file_path).stem

        for pattern, method in SeriesPatternMatcher.PATTERNS:
            match = pattern.match(title)
            if match and len(match.groups()) >= 2:
                series = match.group(1).strip()
                position = 0

                if method in ["numbered_series", "book_numbered", "hash_numbered"]:
                    try:
                        position = int(match.group(2))
                    except (ValueError, IndexError):
                        pass
                elif method == "volume_numbered" and len(match.groups()) >= 3:
                    try:
                        position = int(match.group(3))
                    except (ValueError, IndexError):
                        pass

                return series, position, f"pattern:{method}"

        # Check directory structure
        path_parts = Path(file_path).parts
        if len(path_parts) >= 2:
            parent_dir = path_parts[-2]
            author_dir = path_parts[-3] if len(path_parts) >= 3 else ""

            if parent_dir != author_dir and len(parent_dir.split()) > 1:
                for word in SeriesPatternMatcher.SERIES_WORDS:
                    if word.lower() in parent_dir.lower():
                        return parent_dir, 0, "directory:keyword"

                if parent_dir.lower() in title.lower() or title.lower() in parent_dir.lower():
                    return parent_dir, 0, "directory:fuzzy"

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

    SUPPORTED_EXTENSIONS = [".m4b", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus"]

    @staticmethod
    def extract_author_from_path(file_path: str) -> str:
        """Extract author name from directory structure."""
        path_parts = Path(file_path).parts
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

        meaningful_dirs = []
        for i, part in enumerate(path_parts):
            if part.lower() not in skip_dirs and i < len(path_parts) - 1:
                meaningful_dirs.append(part)

        if not meaningful_dirs:
            return ""

        first_dir = meaningful_dirs[0]

        if " - " in first_dir and any(char.isdigit() for char in first_dir):
            author_match = re.match(r"^([^-]+)\s*-", first_dir)
            if author_match:
                author = author_match.group(1).strip()
                if not author.lower().startswith(("book", "chapter", "part")):
                    return author

        filename = Path(file_path).stem
        if " - " in filename:
            parts = filename.split(" - ")
            if len(parts) >= 3:
                author = parts[0].strip()
                if not author.lower().startswith(("book", "chapter", "part")):
                    return author

        if not first_dir.lower().startswith(("book", "chapter", "part", "volume", "vol")):
            return first_dir

        return ""

    @staticmethod
    def extract_title_from_filename(filename: str) -> str:
        """Extract title from filename."""
        title = Path(filename).stem

        if re.match(r"(?i)^Chapter\s+\d+", title):
            return ""

        title = re.sub(r"^\d+[-_.\s]+", "", title)
        title = re.sub(r"\s*\((?:Unabridged|Audiobook|Retail)\)$", "", title, flags=re.IGNORECASE)

        return title.strip()

    @staticmethod
    def is_chapter_file(filename: str) -> tuple[bool, int | None]:
        """Check if file is a chapter file and extract chapter number."""
        stem = Path(filename).stem
        match = re.match(r"(?i)^Chapter\s+(\d+)", stem)
        if match:
            return True, int(match.group(1))

        # Check for numbered files like "01.mp3", "Track 01.mp3"
        match = re.match(r"(?i)^(?:Track\s+)?(\d+)(?:[-_.\s]|$)", stem)
        if match and len(match.group(1)) <= 3:  # Reasonable chapter numbers
            return True, int(match.group(1))

        return False, None


class BookGrouper:
    """Groups files into books and identifies duplicates."""

    def __init__(self):
        """Initialize the book grouper."""
        self.books_by_directory: dict[str, list[FileInfo]] = defaultdict(list)
        self.pattern_matcher = SeriesPatternMatcher()
        self.path_extractor = PathExtractor()

    def group_files(self, file_paths: list[str]) -> list[BookMetadata]:
        """
        Group files by book and identify versions/duplicates.

        Args:
            file_paths: List of file paths to process

        Returns:
            List of BookMetadata objects with grouped files
        """
        # Phase 1: Group files by directory
        for file_path in file_paths:
            if not self._is_supported_file(file_path):
                continue

            directory = str(Path(file_path).parent)
            filename = Path(file_path).name
            ext = Path(file_path).suffix.lower()

            is_chapter, chapter_num = self.path_extractor.is_chapter_file(filename)

            file_info = FileInfo(
                original_path=file_path,
                filename=filename,
                extension=ext,
                is_chapter=is_chapter,
                chapter_number=chapter_num,
            )

            self.books_by_directory[directory].append(file_info)

        # Phase 2: Create book entries from grouped files
        books = []
        processed_dirs: set[str] = set()

        for directory, files in self.books_by_directory.items():
            if directory in processed_dirs:
                continue

            # Create book metadata from this directory
            book = self._create_book_from_directory(directory, files)
            if book:
                books.append(book)
                processed_dirs.add(directory)

        # Phase 3: Identify duplicates across books
        self._identify_duplicates(books)

        return books

    def _is_supported_file(self, file_path: str) -> bool:
        """Check if file has supported extension."""
        ext = Path(file_path).suffix.lower()
        return ext in self.path_extractor.SUPPORTED_EXTENSIONS

    def _create_book_from_directory(
        self, directory: str, files: list[FileInfo]
    ) -> BookMetadata | None:
        """Create a BookMetadata object from files in a directory."""
        if not files:
            return None

        # Use first non-chapter file for metadata extraction, or first file if all chapters
        primary_file = None
        for f in files:
            if not f.is_chapter:
                primary_file = f
                break
        if not primary_file:
            primary_file = files[0]

        # Extract metadata
        title = self.path_extractor.extract_title_from_filename(primary_file.filename)
        author = self.path_extractor.extract_author_from_path(primary_file.original_path)
        series, position, method = self.pattern_matcher.identify_series(
            title, primary_file.original_path
        )

        # Generate book ID (for grouping duplicates)
        book_id = self._generate_book_id(title, author, series)

        # Create version
        version_id = hashlib.md5(directory.encode()).hexdigest()[:12]
        version = BookVersion(
            version_id=version_id,
            files=files,
            primary_file=primary_file.original_path,
            format=self._determine_primary_format(files),
            total_files=len(files),
            is_multi_file=len(files) > 1,
            base_directory=directory,
        )

        # Calculate confidence
        confidence = self._calculate_confidence(title, author, series, position)

        # Create book metadata
        book = BookMetadata(
            book_id=book_id,
            title=title or "Unknown_Title",
            author=author or "Unknown_Author",
            series=series,
            position=position,
            confidence=confidence,
            extraction_method=method,
            proposed_path=self._generate_proposed_path(
                title, author, series, position, version.format
            ),
            versions=[version],
            total_versions=1,
        )

        # Identify issues
        self._identify_issues(book)

        return book

    def _generate_book_id(self, title: str, author: str, series: str) -> str:
        """Generate a unique but matchable ID for a book."""
        # Normalize strings for comparison
        norm_title = self._normalize_for_comparison(title)
        norm_author = self._normalize_for_comparison(author)
        norm_series = self._normalize_for_comparison(series)

        # Create ID from normalized metadata
        id_string = f"{norm_author}_{norm_series}_{norm_title}"
        return hashlib.md5(id_string.encode()).hexdigest()[:16]

    def _normalize_for_comparison(self, text: str) -> str:
        """Normalize text for duplicate comparison."""
        if not text:
            return ""
        # Remove special chars, lowercase, remove extra spaces
        text = re.sub(r"[^\w\s]", "", text.lower())
        text = re.sub(r"\s+", "_", text.strip())
        return text

    def _determine_primary_format(self, files: list[FileInfo]) -> str:
        """Determine the primary format from a list of files."""
        # Count extensions
        ext_counts = defaultdict(int)
        for f in files:
            ext_counts[f.extension] += 1

        # Return most common extension
        if ext_counts:
            return max(ext_counts, key=ext_counts.get)
        return ""

    def _calculate_confidence(self, title: str, author: str, series: str, position: int) -> str:
        """Calculate confidence level for extracted metadata."""
        score = 0
        if title and title != "Unknown_Title":
            score += 1
        if author and author != "Unknown_Author":
            score += 1
        if series:
            score += 1
        if position > 0:
            score += 1

        if score >= 4:
            return "high"
        elif score >= 2:
            return "medium"
        else:
            return "low"

    def _generate_proposed_path(
        self, title: str, author: str, series: str, position: int, format: str
    ) -> str:
        """Generate proposed organized path structure."""
        parts = []

        if author and author != "Unknown_Author":
            parts.append(self._sanitize_filename(author))
        else:
            parts.append("Unknown_Author")

        if series:
            parts.append(self._sanitize_filename(series))

        filename_parts = []
        if series and position > 0:
            filename_parts.append(f"{position:02d}")
        if title and title != "Unknown_Title":
            filename_parts.append(self._sanitize_filename(title))
        else:
            filename_parts.append("Unknown_Title")

        filename = " - ".join(filename_parts) + format
        parts.append(filename)

        return str(Path(*parts))

    @staticmethod
    def _sanitize_filename(name: str) -> str:
        """Sanitize a string for use in a filename."""
        name = re.sub(r'[<>:"/\\|?*]', "", name)
        name = re.sub(r"\s+", " ", name)
        name = name.strip(". ")
        return name

    def _identify_issues(self, book: BookMetadata) -> None:
        """Identify potential issues with the extracted metadata."""
        if book.title == "Unknown_Title":
            book.issues.append("Missing or unclear title")

        if book.author == "Unknown_Author":
            book.issues.append("Missing or unclear author")

        if book.confidence == "low":
            book.issues.append("Low confidence in metadata extraction")

        if book.extraction_method == "none":
            book.issues.append("No series pattern detected")

    def _identify_duplicates(self, books: list[BookMetadata]) -> None:
        """Identify and link duplicate books (same content, different versions)."""
        # Group books by book_id
        books_by_id: dict[str, list[BookMetadata]] = defaultdict(list)

        for book in books:
            books_by_id[book.book_id].append(book)

        # Process groups with multiple versions
        duplicate_group_counter = 0
        for book_id, book_group in books_by_id.items():
            if len(book_group) > 1:
                # Assign duplicate group ID
                duplicate_group_counter += 1
                group_id = f"DUP{duplicate_group_counter:04d}"

                for book in book_group:
                    book.duplicate_group_id = group_id
                    book.total_versions = len(book_group)


class TestReportGenerator:
    """Generate reports from grouped and analyzed books."""

    def __init__(self):
        """Initialize the report generator."""
        self.stats = defaultdict(int)

    def generate_report(self, books: list[BookMetadata], output_file: str | None = None) -> dict:
        """Generate comprehensive report from scanned books."""
        report = {
            "summary": self._generate_summary(books),
            "statistics": self._generate_statistics(books),
            "books": [self._book_to_dict(book) for book in books],
            "duplicates": self._generate_duplicates_summary(books),
            "issues_summary": self._generate_issues_summary(books),
        }

        if output_file:
            with open(output_file, "w", encoding="utf-8") as f:
                json.dump(report, f, indent=2)

        return report

    def _generate_summary(self, books: list[BookMetadata]) -> dict:
        """Generate high-level summary statistics."""
        total_files = sum(v.total_files for book in books for v in book.versions)
        multi_file_books = sum(1 for book in books for v in book.versions if v.is_multi_file)
        books_with_duplicates = sum(1 for book in books if book.duplicate_group_id)

        return {
            "total_books": len(books),
            "total_files_processed": total_files,
            "multi_file_books": multi_file_books,
            "books_with_duplicates": books_with_duplicates,
            "unique_duplicate_groups": len(
                {b.duplicate_group_id for b in books if b.duplicate_group_id}
            ),
            "books_with_series": sum(1 for b in books if b.series),
            "books_with_position": sum(1 for b in books if b.position > 0),
            "high_confidence": sum(1 for b in books if b.confidence == "high"),
            "medium_confidence": sum(1 for b in books if b.confidence == "medium"),
            "low_confidence": sum(1 for b in books if b.confidence == "low"),
            "books_with_issues": sum(1 for b in books if b.issues),
        }

    def _generate_statistics(self, books: list[BookMetadata]) -> dict:
        """Generate detailed statistics."""
        formats = defaultdict(int)
        extraction_methods = defaultdict(int)
        authors = defaultdict(int)
        series = defaultdict(int)

        for book in books:
            extraction_methods[book.extraction_method] += 1
            if book.author and book.author != "Unknown_Author":
                authors[book.author] += 1
            if book.series:
                series[book.series] += 1
            for version in book.versions:
                formats[version.format] += 1

        return {
            "by_format": dict(formats),
            "by_extraction_method": dict(extraction_methods),
            "top_authors": dict(sorted(authors.items(), key=lambda x: x[1], reverse=True)[:20]),
            "top_series": dict(sorted(series.items(), key=lambda x: x[1], reverse=True)[:20]),
        }

    def _generate_duplicates_summary(self, books: list[BookMetadata]) -> list[dict]:
        """Generate summary of duplicate groups."""
        # Group by duplicate_group_id
        dup_groups: dict[str, list[BookMetadata]] = defaultdict(list)

        for book in books:
            if book.duplicate_group_id:
                dup_groups[book.duplicate_group_id].append(book)

        # Create summary
        duplicates = []
        for group_id, group_books in sorted(dup_groups.items()):
            duplicates.append(
                {
                    "group_id": group_id,
                    "title": group_books[0].title,
                    "author": group_books[0].author,
                    "version_count": len(group_books),
                    "versions": [
                        {
                            "version_id": v.version_id,
                            "format": v.format,
                            "file_count": v.total_files,
                            "directory": v.base_directory,
                        }
                        for book in group_books
                        for v in book.versions
                    ],
                }
            )

        return duplicates

    def _generate_issues_summary(self, books: list[BookMetadata]) -> dict:
        """Generate summary of common issues."""
        issue_counts = defaultdict(int)

        for book in books:
            for issue in book.issues:
                issue_counts[issue] += 1

        return dict(sorted(issue_counts.items(), key=lambda x: x[1], reverse=True))

    def _book_to_dict(self, book: BookMetadata) -> dict:
        """Convert BookMetadata to dictionary for JSON serialization."""
        return {
            "book_id": book.book_id,
            "title": book.title,
            "author": book.author,
            "series": book.series,
            "position": book.position,
            "confidence": book.confidence,
            "extraction_method": book.extraction_method,
            "proposed_path": book.proposed_path,
            "issues": book.issues,
            "duplicate_group_id": book.duplicate_group_id,
            "total_versions": book.total_versions,
            "versions": [
                {
                    "version_id": v.version_id,
                    "format": v.format,
                    "total_files": v.total_files,
                    "is_multi_file": v.is_multi_file,
                    "base_directory": v.base_directory,
                    "files": [
                        {
                            "path": f.original_path,
                            "filename": f.filename,
                            "extension": f.extension,
                            "is_chapter": f.is_chapter,
                            "chapter_number": f.chapter_number,
                        }
                        for f in v.files
                    ],
                }
                for v in book.versions
            ],
        }

    def print_summary(self, report: dict) -> None:
        """Print a human-readable summary of the report."""
        summary = report["summary"]
        stats = report["statistics"]

        print("\n" + "=" * 80)
        print("AUDIOBOOK ORGANIZER TEST REPORT V2")
        print("=" * 80)

        print("\nSUMMARY:")
        print(f"  Total books identified: {summary['total_books']:,}")
        print(f"  Total files processed: {summary['total_files_processed']:,}")
        print(f"  Multi-file books: {summary['multi_file_books']:,}")
        print(f"  Books with duplicate versions: {summary['books_with_duplicates']:,}")
        print(f"  Unique duplicate groups: {summary['unique_duplicate_groups']:,}")
        print(f"  Books with series identified: {summary['books_with_series']:,}")
        print(f"  Books with series position: {summary['books_with_position']:,}")

        print("\nCONFIDENCE LEVELS:")
        print(f"  High confidence: {summary['high_confidence']:,}")
        print(f"  Medium confidence: {summary['medium_confidence']:,}")
        print(f"  Low confidence: {summary['low_confidence']:,}")
        print(f"\n  Books with issues: {summary['books_with_issues']:,}")

        print("\nFILE FORMATS:")
        for format, count in sorted(stats["by_format"].items()):
            print(f"  {format}: {count:,}")

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

        if "duplicates" in report and report["duplicates"]:
            print(f"\nDUPLICATE GROUPS (showing first 10 of {len(report['duplicates'])}):")
            for dup in report["duplicates"][:10]:
                print(f"  {dup['group_id']}: {dup['author']} - {dup['title']}")
                print(f"    Versions: {dup['version_count']}")
                for v in dup["versions"]:
                    print(
                        f"      - {v['format']} ({v['file_count']} files) in {Path(v['directory']).name}"
                    )

        if "issues_summary" in report and report["issues_summary"]:
            print("\nCOMMON ISSUES:")
            for issue, count in list(report["issues_summary"].items())[:5]:
                print(f"  {issue}: {count:,}")

        print("\n" + "=" * 80)


def main():
    """Main entry point for the test script."""
    parser = argparse.ArgumentParser(
        description="Test audiobook organizer V2 - with book grouping and duplicate detection"
    )
    parser.add_argument("file_list", help="Path to file list (e.g., file-list-books)")
    parser.add_argument(
        "--output",
        "-o",
        help="Output JSON file for detailed report",
        default="organize-test-report-v2.json",
    )
    parser.add_argument("--limit", "-l", type=int, help="Limit number of files to process")
    parser.add_argument(
        "--sample", "-s", type=int, help="Output sample of books to stdout", default=5
    )

    args = parser.parse_args()

    if not os.path.exists(args.file_list):
        print(f"Error: File not found: {args.file_list}", file=sys.stderr)
        sys.exit(1)

    print(f"Reading file list from: {args.file_list}")

    with open(args.file_list, encoding="utf-8") as f:
        file_paths = [line.strip() for line in f if line.strip()]

    if args.limit:
        file_paths = file_paths[: args.limit]

    print(f"Processing {len(file_paths):,} files...")

    # Group files into books
    grouper = BookGrouper()
    books = grouper.group_files(file_paths)

    print(f"Identified {len(books):,} unique books")

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
            print(f"\n[{book.book_id}] {book.author} - {book.title}")
            if book.series:
                print(f"  Series: {book.series} #{book.position if book.position else 'N/A'}")
            print(f"  Method: {book.extraction_method}, Confidence: {book.confidence}")
            print(f"  Proposed: {book.proposed_path}")
            if book.duplicate_group_id:
                print(
                    f"  ⚠️  DUPLICATE: Group {book.duplicate_group_id} ({book.total_versions} versions)"
                )
            print(f"  Versions: {len(book.versions)}")
            for i, version in enumerate(book.versions, 1):
                print(f"    Version {i}: {version.format} - {version.total_files} file(s)")
                print(f"      Directory: {version.base_directory}")
                if version.is_multi_file:
                    print("      Files:")
                    for file in version.files[:3]:  # Show first 3 files
                        chapter_info = (
                            f" (Chapter {file.chapter_number})" if file.is_chapter else ""
                        )
                        print(f"        - {file.filename}{chapter_info}")
                    if len(version.files) > 3:
                        print(f"        ... and {len(version.files) - 3} more files")
            if book.issues:
                print(f"  Issues: {', '.join(book.issues)}")

    print(f"\nDetailed report written to: {args.output}")


if __name__ == "__main__":
    main()
