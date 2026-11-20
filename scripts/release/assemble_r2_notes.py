#!/usr/bin/env python3
# file: scripts/release/assemble_r2_notes.py
# version: 0.1.0
# guid: fda144e5-b386-4056-a53b-fbec1e78d384
# last-edited: 2026-11-20

"""Assemble the R2 release note from the five sources 09 section 9.2.9 names.

The assembler reads committed inputs only; it never runs the binary or opens a
store. Every source is required: a missing file, a missing section, an empty
table or an owner text that cannot be located exactly once makes the run fail
with a message naming it, and no output file is written (never a partial note).

Sources (SOURCES below):
  1. the rename table golden (05-config 1.12.3), config keys and env names;
  2. the kind-rename map (05-tasks 5T.8.2), read from the generated Go source
     because the test that owned a listing of it does not exist;
  3. the deleted routes (05-http-api 8.1, the R2 rows of the stages table);
  4. the metric renames (05-metrics 13, the F5 and F7 rows);
  5. 09 section 9.6 (the changed-in-R2 table and the kept contract).

Owner texts are quoted verbatim. Each is located by a start phrase that must
match exactly once in its spec file. Every spec sentence that mentions the R2
release note must be either quoted (OWNER_TEXTS) or listed as a pointer that
only refers to the note (POINTERS); an unlisted one fails the run, so a new
owner text can never be dropped silently. Row 21 of 09 section 9.6 (the shell's
paths) and the shell's stages row belong to R3's note and are left out.

Exit codes: 0 note written, 1 a source or verbatim phrase is missing, 2 bad
arguments.
"""

from __future__ import annotations

import argparse
import dataclasses
import re
import sys
from pathlib import Path
from typing import Sequence

SPEC_DIR = "docs/specs/platform-core"

# The five sources (09 section 9.2.9). Paths are relative to the repo root.
SOURCES: dict[str, str] = {
    "rename_table": "internal/books/cutover/testdata/rename_table.golden",
    "kind_map": "internal/books/cutover/kindrename/map_gen.go",
    "routes": SPEC_DIR + "/05-http-api.md",
    "metrics": SPEC_DIR + "/05-metrics.md",
    "changed": SPEC_DIR + "/09-abo-on-core.md",
}

# The shell's changed-in-R2 row (09 section 9.6 row 21) is R3's.
SHELL_ROW = "21"
# An "Outside the release" cell with nothing for the owner to do.
NO_ACTION = "—"

NOTE_MENTION = re.compile(r"R2\s+release\s+note|R2's\s+release\s+note|Release\s+note\s+\(R2")


class SourceError(Exception):
    """A source, section or verbatim phrase is missing or ambiguous."""


@dataclasses.dataclass(frozen=True)
class OwnerText:
    """One owner text quoted verbatim.

    mode "sentence": the sentence holding the phrase; "quoted": the double
    quoted string that opens inside the phrase; "block": from the phrase to
    the end of its paragraph.
    """

    file: str
    phrase: str
    mode: str
    label: str


@dataclasses.dataclass(frozen=True)
class Pointer:
    """A sentence that only refers to the note and states nothing itself."""

    file: str
    phrase: str


# Owner texts, grouped by spec file. Output order is file name, then position.
OWNER_TEXTS: tuple[OwnerText, ...] = (
    OwnerText(
        "05-authn.md",
        "R2's release note says: \"With authentication off, Settings can no longer turn it on",
        "quoted",
        "authentication off, Settings",
    ),
    OwnerText(
        "05-authn.md",
        'the R2 release note says "Account merges finished before this release can',
        "quoted",
        "account merges",
    ),
    OwnerText(
        "05-authn.md",
        'its identity text is: "Everyone signs in to the web app again once.',
        "quoted",
        "identity text",
    ),
    OwnerText(
        "05-backup.md",
        "the R2 release note and the operator runbook say so (14 E9)",
        "sentence",
        "what a restore loses",
    ),
    OwnerText(
        "05-backup.md",
        "the R2 release note says it in one sentence (SEC-10)",
        "sentence",
        "restore cost, SEC-10",
    ),
    OwnerText(
        "05-config.md",
        "over `cmd/root.go:625`); R2's release note says so",
        "sentence",
        "environment names",
    ),
    OwnerText(
        "05-config.md",
        "space-separated list) are not read (R2's release note says so)",
        "sentence",
        "names that are not read",
    ),
    OwnerText(
        "05-config.md",
        "which stage 0 always admits (05-http-api §4.2). R2's release note says so.",
        "sentence",
        "peer allow-list",
    ),
    OwnerText(
        "05-config.md",
        "R2's release note says \"With authentication off, Settings can no longer turn it on: set",
        "quoted",
        "authentication off, Settings",
    ),
    OwnerText(
        "05-config.md",
        "R2's release note: \"API keys can no longer change integration URLs or credentials;",
        "quoted",
        "integration URLs and credentials",
    ),
    OwnerText(
        "05-config.md",
        "R2's release note already says so",
        "sentence",
        "the protected-field exemption",
    ),
    OwnerText(
        "05-config.md",
        "**Release note (R2, config and secrets).**",
        "block",
        "config and secrets",
    ),
    OwnerText(
        "05-config.md",
        "R2's release note says to delete them once no longer needed",
        "sentence",
        "old copies of secrets",
    ),
    OwnerText(
        "05-health.md",
        "stated in R2's release note",
        "sentence",
        "the version field",
    ),
    OwnerText(
        "05-metadata.md",
        "the R2 release note says how to clear it",
        "sentence",
        "local_migrated",
    ),
    OwnerText(
        "05-metadata.md",
        "**Not carried over** (each is a line of the R2 release note):",
        "block",
        "not carried over",
    ),
    OwnerText(
        "05-notify.md",
        "the R2 release note lists the header and payload change",
        "sentence",
        "webhook format",
    ),
    OwnerText(
        "05-rbac.md",
        "These change HEAD's auth-off reach on purpose; R2's release note states each (09)",
        "sentence",
        "auth-off tightenings",
    ),
    OwnerText(
        "05-rbac.md",
        "R2's release note states the tightening, and the identity cutover oracle lists it",
        "sentence",
        "credential-route detail behind a credential",
    ),
    OwnerText(
        "05-storage.md",
        "which the R2 release note states",
        "sentence",
        "freed blocks of a copy",
    ),
    OwnerText(
        "05-storage.md",
        "09's runbook and the R2 release note say so",
        "sentence",
        "rollback and revocations",
    ),
    OwnerText(
        "05-storage.md",
        '"Rewritten in place by"). The R2 release note says so',
        "sentence",
        "journals rewritten in place",
    ),
    OwnerText(
        "05-storage.md",
        "still hold the old representation; the R2 release note says",
        "sentence",
        "older archives and set-asides",
    ),
    OwnerText(
        "05-tasks.md",
        "deletes all 15 declared strings and lists them in the R2 release note",
        "sentence",
        "the deleted v2 schedule strings",
    ),
    OwnerText(
        "07-ui-shell.md",
        "(the R2 release note says so)",
        "sentence",
        "invite and temp-login links",
    ),
    OwnerText(
        "09-abo-on-core.md",
        "R2 release notes say so",
        "sentence",
        "the checkpoint",
    ),
    OwnerText(
        "09-abo-on-core.md",
        'their auth-off reach (05-rbac §4.11): "With authentication off, credential,'
        " API-key, session, audit and backup",
        "quoted",
        "authentication off",
    ),
    OwnerText(
        "09-abo-on-core.md",
        "The R2 release note gives the measured duration",
        "sentence",
        "first-boot time",
    ),
)

# Sentences that mention the note only to say it exists. Each phrase must match
# exactly once and its sentence must contain a mention, or the run fails: a
# stale entry cannot linger.
POINTERS: tuple[Pointer, ...] = (
    Pointer("02-decision-record.md", "and the R2 release note lists them"),
    Pointer("03-architecture.md", "it drives the R2 config step"),
    Pointer("03-architecture.md", "the runbook and the R2 release note (last PR of R2)"),
    Pointer("03-architecture.md", "the R2 release note says so, and the owner reconfigures"),
    Pointer("03-architecture.md", "are listed in the R2 release note, generated from"),
    Pointer("05-authn.md", "stated in R2's release note (§4.11, SEC5-8)"),
    Pointer("05-authn.md", "R2's release note states the"),
    Pointer("05-config.md", "the R2 release note's rename list (09)"),
    Pointer("05-logging.md", "the R2 release note lists (05-rbac"),
    Pointer("09-abo-on-core.md", "and the R2 release note lists it. Ecosystem names"),
    Pointer("09-abo-on-core.md", "**The R2 release note** is assembled from"),
    Pointer("09-abo-on-core.md", "sets the R2 release note's expected first-boot time"),
    Pointer("09-abo-on-core.md", "This list is the source of the R2 release note"),
    Pointer("09-abo-on-core.md", "a caller outside the repo: the R2 release note and the runbook"),
    Pointer("12-repo-split.md", "and R2's release note lists them"),
    Pointer("14-risks-open-questions.md", "the runbook and the R2 release note say both plainly"),
    Pointer("14-risks-open-questions.md", "changes in R2 and the R2 release note lists it"),
)


# --------------------------------------------------------------------------
# Reading sources


def read_text(repo: Path, rel: str, what: str) -> str:
    """Return a source file's text, failing loudly when it is missing or empty."""
    path = repo / rel
    if not path.is_file():
        raise SourceError(f"missing source {what}: {rel}")
    text = path.read_text(encoding="utf-8")
    if not text.strip():
        raise SourceError(f"empty source {what}: {rel}")
    return text


def section(text: str, heading: str, what: str) -> str:
    """Return the body of the one heading that starts with ``heading``.

    The body runs to the next heading of the same or a higher level.
    """
    lines = text.split("\n")
    level = len(heading) - len(heading.lstrip("#"))
    starts = [i for i, ln in enumerate(lines) if ln.startswith(heading)]
    if len(starts) != 1:
        raise SourceError(f"{what}: heading {heading!r} found {len(starts)} times, expected once")
    stop = re.compile(rf"^#{{1,{level}}} ")
    end = len(lines)
    for i in range(starts[0] + 1, len(lines)):
        if stop.match(lines[i]):
            end = i
            break
    return "\n".join(lines[starts[0] + 1 : end])


def split_row(line: str) -> list[str]:
    """Split a Markdown table row on unescaped pipes; cells are stripped."""
    body = line.strip()
    if not (body.startswith("|") and body.endswith("|")):
        raise SourceError(f"not a table row: {line[:80]!r}")
    return [c.strip() for c in re.split(r"(?<!\\)\|", body)[1:-1]]


def table_rows(body: str, header_start: str, what: str, width: int) -> list[list[str]]:
    """Rows of the one table whose header line starts with ``header_start``."""
    lines = body.split("\n")
    heads = [i for i, ln in enumerate(lines) if ln.startswith(header_start)]
    if len(heads) != 1:
        raise SourceError(
            f"{what}: table header {header_start!r} found {len(heads)} times, expected once"
        )
    rows: list[list[str]] = []
    for ln in lines[heads[0] + 2 :]:
        if not ln.startswith("|"):
            break
        cells = split_row(ln)
        if len(cells) != width:
            raise SourceError(f"{what}: row has {len(cells)} cells, expected {width}: {ln[:90]}")
        rows.append(cells)
    if not rows:
        raise SourceError(f"{what}: table {header_start!r} has no rows")
    return rows


def esc(cell: str) -> str:
    """Escape unescaped pipes so a derived cell stays in its column."""
    return re.sub(r"(?<!\\)\|", r"\\|", cell)


def md_table(head: Sequence[str], rows: Sequence[Sequence[str]]) -> str:
    out = ["| " + " | ".join(head) + " |", "|" + "---|" * len(head)]
    for row in rows:
        out.append("| " + " | ".join(row) + " |")
    return "\n".join(out)


# --------------------------------------------------------------------------
# Source 1: the rename table


def rename_rows(text: str) -> list[list[str]]:
    """One row per ``## <old key>`` section of the golden, sorted by old key."""
    sections: dict[str, list[str]] = {}
    current: str | None = None
    for ln in text.split("\n"):
        if ln.startswith("## "):
            current = ln[3:].strip()
            if current in sections:
                raise SourceError(f"rename table: duplicate section {current!r}")
            sections[current] = []
        elif current is not None and ln.strip():
            sections[current].append(ln.strip())
    if not sections:
        raise SourceError("rename table: no '## <key>' sections found")
    rows: list[list[str]] = []
    for old in sorted(sections):
        targets: list[str] = []
        envs: list[str] = []
        retired: list[str] = []
        for ln in sections[old]:
            if ln.startswith("target "):
                targets.append(ln[len("target ") :].split(" convert=")[0].split(" list=")[0])
                m = re.search(r"\benv=(\S+)", ln)
                if m:
                    envs.append(m.group(1))
            elif ln.startswith("retired "):
                m2 = re.match(r"retired (file|env) (\S+) -> (\S+) (\S+)", ln)
                if not m2:
                    raise SourceError(f"rename table: unreadable line under {old!r}: {ln}")
                retired.append(f"{m2.group(1)} `{m2.group(2)}` ({m2.group(4)})")
        rows.append(
            [
                f"`{esc(old)}`",
                esc("; ".join(targets) or "(no target)"),
                esc(", ".join(f"`{e}`" for e in envs) or "-"),
                esc("; ".join(retired) or "-"),
            ]
        )
    return rows


# --------------------------------------------------------------------------
# Source 2: the kind-rename map (generated Go source)

_KIND_ENTRY = re.compile(r'^\t"([^"]+)":\s+\{(.*)\},?$')
_KIND_FIELD = re.compile(r'(\w+): ("[^"]*"|\w+)')


def go_map_entries(text: str, name: str) -> list[tuple[str, dict[str, str]]]:
    """Entries ``(old, fields)`` of the generated Go map ``name``, sorted by old."""
    lines = text.split("\n")
    start = [i for i, ln in enumerate(lines) if ln.startswith(f"var {name} = map[")]
    if len(start) != 1:
        raise SourceError(f"kind map: var {name} found {len(start)} times, expected once")
    entries: list[tuple[str, dict[str, str]]] = []
    for ln in lines[start[0] + 1 :]:
        if ln.startswith("}"):
            break
        m = _KIND_ENTRY.match(ln)
        if not m:
            raise SourceError(f"kind map: unreadable entry in {name}: {ln!r}")
        fields = {k: v.strip('"') for k, v in _KIND_FIELD.findall(m.group(2))}
        if not fields:
            raise SourceError(f"kind map: entry without fields in {name}: {ln!r}")
        entries.append((m.group(1), fields))
    if not entries:
        raise SourceError(f"kind map: {name} has no entries")
    return sorted(entries, key=lambda e: e[0])


def kind_map(text: str) -> tuple[list[list[str]], list[list[str]]]:
    if "Code generated" not in text or "DO NOT EDIT" not in text:
        raise SourceError("kind map: the generated-file header is missing")

    def cell(fields: dict[str, str], key: str) -> str:
        return f"`{fields[key]}`" if key in fields else "-"

    defs = [
        [f"`{o}`", cell(f, "Kind"), cell(f, "Rule"), cell(f, "Marker")]
        for o, f in go_map_entries(text, "defTargets")
    ]
    sched = [[f"`{o}`", cell(f, "Kind")] for o, f in go_map_entries(text, "schedulerTargets")]
    return defs, sched


# --------------------------------------------------------------------------
# Source 3: the deleted routes


def route_rows(text: str) -> list[list[str]]:
    body = section(text, "### 8.1 Stages", "deleted routes")
    rows = table_rows(body, "| Release (flip)", "deleted routes", 4)
    picked = [[c[0], c[1]] for c in rows if c[0].startswith("R2")]
    if not picked:
        raise SourceError("deleted routes: no R2 row in the 8.1 stages table")
    return picked


# --------------------------------------------------------------------------
# Source 4: the metric renames


def metric_rows(text: str) -> list[list[str]]:
    body = section(text, "## 13. Migration from ABO", "metric renames")
    picked: list[list[str]] = []
    for header in ("| HEAD family |", "| HEAD family (labels) |"):
        for c in table_rows(body, header, "metric renames", 4):
            if re.match(r"(F5|F7)\b", c[2]):
                picked.append([c[0], c[1], c[2], c[3]])
    if not picked:
        raise SourceError("metric renames: no F5 or F7 row in 05-metrics section 13")
    return sorted(picked)


# --------------------------------------------------------------------------
# Source 5: 09 section 9.6


def changed_rows(text: str) -> tuple[list[list[str]], str]:
    """The 9.6 rows without the shell's, and the kept-contract paragraph."""
    body = section(text, "## 9.6 Changed in R2", "09 9.6")
    rows = table_rows(body, "| # |", "09 9.6", 6)
    if not any(r[0] == SHELL_ROW for r in rows):
        raise SourceError(f"09 9.6: row {SHELL_ROW} (the shell's, left out) is not where expected")
    kept = [r for r in rows if r[0] != SHELL_ROW]
    marker = "**The kept contract (D12 (c)).**"
    pos = body.find(marker)
    if pos < 0 or body.count(marker) != 1:
        raise SourceError(f"09 9.6: the kept-contract paragraph {marker!r} not found once")
    end = body.find("\n\n", pos)
    paragraph = body[pos : end if end >= 0 else len(body)]
    return kept, paragraph


# --------------------------------------------------------------------------
# Owner texts


def phrase_regex(phrase: str) -> re.Pattern[str]:
    """The phrase with any whitespace run standing for a line wrap."""
    return re.compile(r"\s+".join(re.escape(tok) for tok in phrase.split()))


def locate(text: str, phrase: str, where: str) -> re.Match[str]:
    hits = list(phrase_regex(phrase).finditer(text))
    if len(hits) != 1:
        raise SourceError(
            f"{where}: start phrase {phrase!r} found {len(hits)} times, expected once"
        )
    return hits[0]


_SENT_END = re.compile(r"[.!?][)*`\"]*(?=\s|$)")
_SENT_START = re.compile(r"[.!?][)*`\"]*\s+(?=\S)")
_LIST_MARK = re.compile(r"(?m)^[ \t]*(?:[-*]|\d+\.)[ \t]+")


def sentence_span(text: str, start: int, end: int) -> tuple[int, int]:
    """Span of the sentence holding ``text[start:end]``.

    Bounded by the paragraph (blank lines), by the cell of a table row, by a
    list-item marker on the left and by sentence punctuation on both sides.
    """
    b0 = text.rfind("\n\n", 0, start)
    b0 = 0 if b0 < 0 else b0 + 2
    b1 = text.find("\n\n", end)
    b1 = len(text) if b1 < 0 else b1
    line0 = text.rfind("\n", 0, start) + 1
    if text.startswith("|", line0):
        b0 = line0
        nl = text.find("\n", end)
        b1 = len(text) if nl < 0 else nl
        cell0 = text.rfind("|", b0, start)
        if cell0 >= 0:
            b0 = cell0 + 1
        cell1 = text.find("|", end, b1)
        if cell1 >= 0:
            b1 = cell1
    left = b0
    for m in _SENT_START.finditer(text, b0, start):
        left = max(left, m.end())
    for m in _LIST_MARK.finditer(text, b0, start):
        left = max(left, m.end())
    m2 = _SENT_END.search(text, end, b1)
    right = m2.end() if m2 else b1
    return left, right


def extract(text: str, entry: OwnerText) -> tuple[str, int, int]:
    """Return ``(quoted text, cover start, cover end)`` for one owner text."""
    where = f"{entry.file} ({entry.label})"
    m = locate(text, entry.phrase, where)
    if entry.mode == "block":
        end = text.find("\n\n", m.end())
        end = len(text) if end < 0 else end
        return text[m.start() : end].rstrip(), m.start(), end
    s0, s1 = sentence_span(text, m.start(), m.end())
    if entry.mode == "sentence":
        return text[s0:s1].strip(), s0, s1
    if entry.mode == "quoted":
        q0 = text.find('"', m.start(), m.end())
        q1 = text.find('"', q0 + 1) if q0 >= 0 else -1
        if q0 < 0 or q1 < 0:
            raise SourceError(f"{where}: no complete double-quoted text in the phrase")
        return text[q0 : q1 + 1], s0, max(s1, q1 + 1)
    raise SourceError(f"{where}: unknown mode {entry.mode!r}")


def spec_text(repo: Path, name: str, cache: dict[str, str]) -> str:
    if name not in cache:
        cache[name] = read_text(repo, f"{SPEC_DIR}/{name}", "owner text file")
    return cache[name]


def owner_texts(repo: Path) -> list[tuple[OwnerText, str, str]]:
    """Every owner text, verbatim, and the coverage check over the spec files."""
    cache: dict[str, str] = {}
    spans: dict[str, list[tuple[int, int]]] = {}
    found: list[tuple[str, int, OwnerText, str, str]] = []
    for entry in OWNER_TEXTS:
        text = spec_text(repo, entry.file, cache)
        quote, c0, c1 = extract(text, entry)
        if not quote:
            raise SourceError(f"{entry.file} ({entry.label}): extracted text is empty")
        norm_quote = " ".join(quote.split())
        if norm_quote not in " ".join(text.split()):
            raise SourceError(f"{entry.file} ({entry.label}): quote is not verbatim")
        spans.setdefault(entry.file, []).append((c0, c1))
        start = text.find(quote)
        found.append((entry.file, start, entry, quote, nearest_heading(text, start)))
    for ptr in POINTERS:
        text = spec_text(repo, ptr.file, cache)
        m = locate(text, ptr.phrase, f"{ptr.file} (pointer)")
        p0, p1 = sentence_span(text, m.start(), m.end())
        if not NOTE_MENTION.search(text, p0, p1):
            raise SourceError(
                f"{ptr.file}: pointer {ptr.phrase!r} no longer mentions the R2 release note"
            )
        spans.setdefault(ptr.file, []).append((p0, p1))
    spec_root = repo / SPEC_DIR
    if not spec_root.is_dir():
        raise SourceError(f"missing spec directory: {SPEC_DIR}")
    uncovered: list[str] = []
    for path in sorted(spec_root.glob("*.md")):
        text = spec_text(repo, path.name, cache)
        for hit in NOTE_MENTION.finditer(text):
            covered = any(a <= hit.start() < b for a, b in spans.get(path.name, []))
            if not covered:
                line = text.count("\n", 0, hit.start()) + 1
                uncovered.append(f"{path.name}:{line}")
    if uncovered:
        raise SourceError(
            "spec sentences mention the R2 release note but are neither quoted nor listed as"
            " pointers (add each to OWNER_TEXTS or POINTERS): " + ", ".join(uncovered)
        )
    found.sort(key=lambda f: (f[0], f[1]))
    return [(e, q, h) for _, _, e, q, h in found]


def nearest_heading(text: str, pos: int) -> str:
    """The closest Markdown heading above ``pos``, verbatim, so a quote names its section."""
    found = ""
    for m in re.finditer(r"(?m)^#{1,6} +(.+?) *$", text[:pos]):
        found = m.group(1)
    return found


def blockquote(quote: str) -> str:
    return "\n".join("> " + ln.strip() if ln.strip() else ">" for ln in quote.split("\n"))


# --------------------------------------------------------------------------
# The note


def assemble(repo: Path) -> str:
    """Build the whole note in memory; raises SourceError before any output."""
    rename = rename_rows(read_text(repo, SOURCES["rename_table"], "rename table"))
    defs, sched = kind_map(read_text(repo, SOURCES["kind_map"], "kind-rename map"))
    routes = route_rows(read_text(repo, SOURCES["routes"], "deleted routes"))
    metrics = metric_rows(read_text(repo, SOURCES["metrics"], "metric renames"))
    changed, kept = changed_rows(read_text(repo, SOURCES["changed"], "changed in R2"))
    owners = owner_texts(repo)

    out: list[str] = ["# R2 release note", ""]
    out += [
        "## Summary",
        "",
        "R2 moves settings, identity, tasks, events, integrations, backup and health onto the new"
        " core, and it cannot be undone in place: fixing forward is the default, and a rollback"
        " restores a checkpoint or the off-host archive and loses every write since the upgrade."
        " This note is generated from the repository's own tables and quotes each owner's text"
        " verbatim.",
        "",
        f"- {len(rename)} config keys and settings renamed or retired (table below).",
        f"- {len(defs)} task kinds and {len(sched)} scheduler names renamed.",
        f"- {len(routes)} R2 stages delete routes; {len(metrics)} metric families renamed.",
        f"- {len(owners)} quoted owner texts.",
        "",
    ]
    out += ["## What you must do", ""]
    for row in changed:
        action = row[5]
        if action != "—":
            out.append(f"- Row {row[0]}, {row[1]}: {action}")
    out.append("")
    out += [
        "## Changed in R2",
        "",
        md_table(
            ["#", "Surface", "Before R2", "From R2", "Owner file", "Outside the release"], changed
        ),
        "",
    ]
    out += ["## The kept contract", "", blockquote(kept), ""]
    out += [
        "## Renamed config keys and env names",
        "",
        md_table(["Old key", "Becomes", "New env name", "Old names (file or env, action)"], rename),
        "",
    ]
    out += [
        "## Renamed task kinds",
        "",
        md_table(["Old definition ID", "Kind", "Rule", "Marker"], defs),
        "",
    ]
    out += ["### Renamed scheduler names", "", md_table(["Old name", "Kind"], sched), ""]
    out += ["## Deleted routes and their successors", ""]
    for flip, change in routes:
        out += [f"### {flip}", "", change, ""]
    out += [
        "## Renamed metrics",
        "",
        md_table(["HEAD family", "Successor", "Flip", "Consumers changed"], metrics),
        "",
    ]
    out += ["## Owner texts", ""]
    for entry, quote, heading in owners:
        out += [
            f"### {entry.file[:-3]}: {entry.label}",
            "",
            f'Source: `{SPEC_DIR}/{entry.file}`, under "{heading}"',
            "",
            blockquote(quote),
            "",
        ]
    return "\n".join(out).rstrip("\n") + "\n"


def default_repo() -> Path:
    return Path(__file__).resolve().parents[2]


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Assemble the R2 release note from its five committed sources.",
        epilog="exit 0: note written; exit 1: a source or verbatim phrase is missing;"
        " exit 2: bad arguments",
    )
    parser.add_argument("--out", required=True, type=Path, help="file to write the note to")
    parser.add_argument(
        "--repo", type=Path, default=default_repo(), help="repository root (default: this one)"
    )
    args = parser.parse_args(argv)
    try:
        note = assemble(args.repo)
    except SourceError as exc:
        print(f"assemble_r2_notes: error: {exc}", file=sys.stderr)
        return 1
    args.out.write_text(note, encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
