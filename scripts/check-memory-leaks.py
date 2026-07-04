#!/usr/bin/env python3
# file: scripts/check-memory-leaks.py
# version: 1.2.0
"""
Detects common memory leak patterns in React/TypeScript code.
Scans for:
- Untracked setTimeout/setInterval
- Untracked addEventListener/removeEventListener pairs
- Untracked fetch/subscriptions in handlers

Flags:
  (default)        Human-readable report; exits 1 when leaks found
  --json           JSON array of findings; exits 1 when leaks found
  --todo-entry     Single TODO.md list item for the findings; exits 0 always
                   (meant for CI reporting steps)
"""

import argparse
import json
import re
import sys
from pathlib import Path
from typing import List, Tuple, Optional


class MemoryLeakDetector:
    # Directories that contain non-React code (services, stores, utilities).
    # setTimeout/setInterval in these files does NOT cause React unmount leaks;
    # they run in module or class scope where lifecycle is managed differently.
    COMPONENT_ONLY_DIRS = {"components", "pages", "hooks"}

    def __init__(self, web_src_dir: Optional[str] = None):
        if web_src_dir is None:
            # Find web/src by looking up from current directory
            current = Path.cwd()
            for parent in [current] + list(current.parents):
                candidate = parent / "web" / "src"
                if candidate.exists():
                    web_src_dir = str(candidate)
                    break
            if web_src_dir is None:
                web_src_dir = "web/src"

        self.web_src = Path(web_src_dir)
        self.issues: List[Tuple[str, int, str]] = []
        # Lines that indicate proper cleanup
        self.ignore_patterns = [
            r"clearTimeout|clearInterval",
            r"removeEventListener",
            r"useRef.*current",
            r"return\s*\(\)",
            r"cancelled\s*=\s*true",
            r"AbortController",
        ]

    def _is_component_file(self, filepath: str) -> bool:
        """Return True only for files inside components/, pages/, or hooks/."""
        parts = Path(filepath).parts
        return any(d in parts for d in self.COMPONENT_ONLY_DIRS)

    def should_ignore(self, line: str) -> bool:
        """Check if line should be ignored"""
        # Ignore empty lines and comments
        stripped = line.strip()
        if not stripped or stripped.startswith("//"):
            return True

        for pattern in self.ignore_patterns:
            if re.search(pattern, line):
                return True
        return False

    def check_untracked_timers(self, filepath: str, content: str) -> None:
        """Find setTimeout/setInterval without proper tracking.

        Only runs on component files (components/, pages/, hooks/) because
        timers in services, stores, and utilities are not tied to React
        component lifecycle and cannot cause unmount-after-fire leaks.
        """
        if not self._is_component_file(filepath):
            return

        lines = content.split("\n")

        for i, line in enumerate(lines, 1):
            if self.should_ignore(line):
                continue

            # Look for setTimeout/setInterval
            match = re.search(r"(setTimeout|setInterval)\s*\(", line)
            if not match:
                continue

            timer_type = match.group(1)

            # Check if it's being assigned to a ref or variable
            if re.search(
                r"(Ref\.current\s*=|\.current\s*=|const\s+\w*[Rr]ef\s*=|\w*[Ii]d\s*=|const\s+\w*interval\s*=|const\s+\w*timeout\s*=|\w+[Tt]imer\s*=|\w+[Tt]imeout\s*=)",
                line,
            ):
                continue  # Already tracked in variable

            # Check if we're in a useEffect with cleanup
            in_use_effect = False
            has_cleanup = False

            # Look backwards for useEffect
            for j in range(i - 1, max(0, i - 50), -1):
                if "useEffect" in lines[j]:
                    in_use_effect = True
                    # Check if there's a return statement (cleanup) within this useEffect
                    # Look forward from useEffect for return statement
                    for k in range(j, min(i + 20, len(lines))):
                        if re.search(
                            r"return\s*\(\s*\)\s*=>|return\s*function|clearTimeout|clearInterval",
                            lines[k],
                        ):
                            has_cleanup = True
                            break
                    break

            if in_use_effect and has_cleanup:
                continue  # Cleanup exists

            # This is an untracked timer
            self.issues.append(
                (filepath, i, f"Untracked {timer_type} (may fire after unmount)")
            )

    def check_untracked_listeners(self, filepath: str, content: str) -> None:
        """Find addEventListener without removeEventListener.

        Searches up to 100 lines ahead for a matching removeEventListener.
        Stops only when the enclosing function scope fully closes (scope_depth
        drops below -1), not on the first closing brace — that would produce
        false positives when add and remove are in sibling blocks (e.g. an
        XHR onload handler following the addEventListener call).
        """
        lines = content.split("\n")

        for i, line in enumerate(lines, 1):
            if "addEventListener" not in line or self.should_ignore(line):
                continue

            # Get the listener name for matching
            listener_match = re.search(r"addEventListener\s*\(\s*['\"](\w+)['\"]", line)
            if not listener_match:
                continue

            event_name = listener_match.group(1)

            # Look ahead for removeEventListener with same event
            found_remove = False
            scope_depth = 0

            for j in range(i, min(i + 100, len(lines))):
                line_j = lines[j]

                # Track scope to avoid false positives
                scope_depth += line_j.count("{") - line_j.count("}")

                if (
                    f"removeEventListener('{event_name}'" in line_j
                    or f'removeEventListener("{event_name}"' in line_j
                ):
                    found_remove = True
                    break

                # Stop only when we leave the enclosing function entirely
                # (scope_depth < -1 means we've closed more than one extra level).
                # Using -1 instead of 0 avoids false positives when add and remove
                # live in sibling blocks at the same depth.
                if scope_depth < -1:
                    break

            if not found_remove:
                self.issues.append(
                    (
                        filepath,
                        i,
                        f"addEventListener('{event_name}') without removeEventListener",
                    )
                )

    def check_poll_without_cleanup(self, filepath: str, content: str) -> None:
        """Find polling functions created in handlers without cleanup tracking."""
        lines = content.split("\n")

        # Patterns that indicate the polling is properly guarded against leaks.
        # Includes both explicit cancellation flags and ref-based unmount guards.
        _CANCELLATION_PATTERNS = re.compile(
            r"cancelled|AbortController|Ref\.current|isMounted|isUnmounted|isCleanedUp"
        )

        for i, line in enumerate(lines, 1):
            if self.should_ignore(line):
                continue

            # Look for patterns like: const poll = async () => { ... setTimeout(poll, ...)
            if re.search(r"const\s+\w*[Pp]oll\s*=\s*async", line):
                # Collect a 50-line window around the polling function
                window = "".join(lines[max(0, i - 1) : min(i + 50, len(lines))])
                # If any cancellation/cleanup pattern is present, skip
                if _CANCELLATION_PATTERNS.search(window):
                    continue
                for j in range(i, min(i + 50, len(lines))):
                    if re.search(r"setTimeout\(\w*[Pp]oll", lines[j]):
                        self.issues.append(
                            (
                                filepath,
                                j + 1,
                                "Recursive polling without cancellation flag",
                            )
                        )
                        break

    def scan(self) -> bool:
        """Scan all TSX/TS files"""
        if not self.web_src.exists():
            print(f"Warning: {self.web_src} not found, skipping memory leak scan")
            return True

        tsx_files = sorted(
            list(self.web_src.rglob("*.tsx")) + list(self.web_src.rglob("*.ts"))
        )
        # Exclude test files
        tsx_files = [
            f for f in tsx_files if ".test." not in f.name and "setup.ts" not in f.name
        ]

        print(
            f"🔍 Scanning {len(tsx_files)} files for memory leaks...", file=sys.stderr
        )

        for filepath in tsx_files:
            try:
                content = filepath.read_text(encoding="utf-8")
                rel_path = filepath.relative_to(self.web_src.parent)

                self.check_untracked_timers(str(rel_path), content)
                self.check_untracked_listeners(str(rel_path), content)
                self.check_poll_without_cleanup(str(rel_path), content)
            except Exception as e:
                print(f"Error scanning {filepath}: {e}", file=sys.stderr)

        return len(self.issues) == 0

    def report(self) -> None:
        """Print findings"""
        if not self.issues:
            print("✅ No memory leak patterns detected\n")
            return

        print(f"\n⚠️  Found {len(self.issues)} potential memory leaks:\n")

        by_file = {}
        for filepath, line_no, issue in self.issues:
            if filepath not in by_file:
                by_file[filepath] = []
            by_file[filepath].append((line_no, issue))

        for filepath in sorted(by_file.keys()):
            print(f"  {filepath}")
            for line_no, issue in sorted(by_file[filepath]):
                print(f"    Line {line_no}: {issue}")

        print()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Detect memory leak patterns in React/TS code"
    )
    group = parser.add_mutually_exclusive_group()
    group.add_argument(
        "--json", action="store_true", help="Output findings as JSON array"
    )
    group.add_argument(
        "--todo-entry",
        metavar="DATE",
        help="Output a TODO.md list item for the findings (DATE = YYYY-MM-DD); exits 0 always",
    )
    group.add_argument(
        "--todo-insert",
        metavar="DATE",
        help="Like --todo-entry but also inserts the entry into TODO.md in-place",
    )
    parser.add_argument(
        "--run-url",
        metavar="URL",
        default="",
        help="GitHub Actions run URL to include in TODO entry",
    )
    args = parser.parse_args()

    detector = MemoryLeakDetector()
    clean = detector.scan()

    if args.json:
        data = [
            {"file": f, "line": ln, "message": msg} for f, ln, msg in detector.issues
        ]
        print(json.dumps(data, indent=2))
        return 0 if clean else 1

    date = args.todo_entry or args.todo_insert
    if date:
        count = len(detector.issues)
        if count == 0:
            return 0
        # Build a compact TODO.md bullet that the burndown bot won't auto-process
        # (tag is [memory-leak], NOT [failed-batch-hard]).
        lines = [
            f"- [ ] **MEMLEAK-{date}** [memory-leak] {count} potential memory leak(s) "
            f"detected by scheduled scan{' — ' + args.run_url if args.run_url else ''}."
        ]
        # Up to 20 specific findings as sub-bullets
        for filepath, line_no, msg in detector.issues[:20]:
            lines.append(f"  - `{filepath}:{line_no}` — {msg}")
        if count > 20:
            lines.append(f"  - _(+{count - 20} more — see run output)_")
        entry = "\n".join(lines) + "\n"

        if args.todo_insert:
            _insert_into_todo(entry)

        print(entry)
        return 0

    # Default: human-readable report
    detector.report()
    return 0 if clean else 1


def _insert_into_todo(entry: str) -> None:
    """Insert entry under '## ⚠️ Automated Findings' section in TODO.md (creates section if absent)."""
    todo_path = Path("TODO.md")
    if not todo_path.exists():
        return

    content = todo_path.read_text(encoding="utf-8")
    header = "## ⚠️ Automated Findings"

    if header in content:
        # Insert right after the header line
        idx = content.index(header) + len(header)
        # Skip past any immediately following newline
        while idx < len(content) and content[idx] == "\n":
            idx += 1
        content = content[:idx] + "\n" + entry + content[idx:]
    else:
        # Prepend a new section before the last "---" divider, or append at end
        last_divider = content.rfind("\n---\n")
        if last_divider != -1:
            content = (
                content[:last_divider]
                + f"\n\n{header}\n\n{entry}"
                + content[last_divider:]
            )
        else:
            content += f"\n\n{header}\n\n{entry}"

    todo_path.write_text(content, encoding="utf-8")


if __name__ == "__main__":
    sys.exit(main())
