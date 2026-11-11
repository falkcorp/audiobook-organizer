#!/usr/bin/env python3
# file: scripts/ci/gen_shared_family_lists.py
# version: 1.1.0
# guid: 3f0c8a52-71d4-4b8e-9a26-5c1e7d0b4f93
# last-edited: 2026-11-10
"""Generate the R2 shared-family lists and check them against the corpus (F2-15).

05-storage §5S.3.13 "The shared families" and 14 E3: ``idx:`` and ``setting:``
hold both books' rows and rows that a cutover step moves. This script writes
``internal/books/testdata/shared_families.golden``, the list
``TestCutoverLeavesOtherFamiliesByteIdentical`` reads to know which keys it
must not hash, and checks it against a key scan of the committed corpus.

Where each entry comes from:

* **move** -- the sub-prefixes and exact keys of §5S.9.1 rows 1-9 and 18.
  ``keyfamilies.go`` registers ``idx:`` and ``setting:`` as single families,
  so it does not carry these; rows 1-7 are read from the books claim list's
  nested claims (``serializedSubClaims`` in ``booksclaims.go``, derived beside
  ``keyfamilies.go``) and must equal this script's table for those rows, and
  rows 8, 9 and 18 come from the table below (the spec's text, verbatim).
* **rewrite** -- §5S.9.1's "Rewritten in place by" column: M3's expiry flag
  and merge journals, M4's activity families, M6's ``rootscan:``. Each family
  must be registered in ``keyfamilies.go`` (``rootscan:`` is registered by F7;
  until then it is listed with that note).
* **keep** -- what stays books': ``setting:``'s registered children (from
  ``keyfamilies.go``) and every ``idx:<name>:`` sub-prefix the books store code
  writes (string literals in ``internal/books/store/*.go``) that no move entry
  covers (row 18's remainder: playback, playlists, book versions, collections,
  ABS sessions).
* **bookkeeping** -- keys the build's own legacy migration track and storage
  projection write on every upgrade, which no cutover step owns: §5S.9.1 row
  10 (``preference:storage_format``, ``preference:storage_migration``) and row
  16's migration bookkeeping (``preference:db_version``,
  ``preference:migration_<n>``, and ``counter:preference``, the ID counter the
  preference writer bumps for each new ledger row).

The census (``--check`` and ``--write``): every key of every committed corpus
store must fall in a family ``keyfamilies.go`` registers, and every ``idx:``
key must be covered by a move or keep entry. A key outside both fails the
script naming its prefix (the brief's STOP: report it, never add it by hand).
The key scan is Go's (``TestSharedFamilyCensusDump`` in internal/books, which
opens each store read-only from a copy); ``--census <file>`` reads a dump
instead, for tests.

Usage:
    python3 scripts/ci/gen_shared_family_lists.py --check   # CI: exit 1 on a diff
    python3 scripts/ci/gen_shared_family_lists.py --write   # regenerate the golden
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
STORE_PKG = REPO_ROOT / "internal" / "books" / "store"
KEYFAMILIES = STORE_PKG / "keyfamilies.go"
BOOKSCLAIMS = STORE_PKG / "booksclaims.go"
GOLDEN = REPO_ROOT / "internal" / "books" / "testdata" / "shared_families.golden"

# The golden's own header (file-headers rule). Bump GOLDEN_VERSION and
# GOLDEN_EDITED whenever the generated content changes.
GOLDEN_GUID = "b7d2e4a1-5c39-4f6e-8a01-9e3d6c2f7b58"
GOLDEN_VERSION = "1.1.0"
GOLDEN_EDITED = "2026-11-10"

CENSUS_TEST = "^TestSharedFamilyCensusDump$"
CENSUS_ENV = "R2_CENSUS_OUT"


class ListError(Exception):
    """A list disagrees with its source, or the corpus holds an unclassified key."""


@dataclass(frozen=True, order=True)
class Entry:
    cls: str  # move | rewrite | keep | bookkeeping
    kind: str  # prefix | exact
    value: str
    row: str  # the §5S.9.1 row
    note: str

    def covers(self, key: str) -> bool:
        return key.startswith(self.value) if self.kind == "prefix" else key == self.value

    def line(self) -> str:
        return "\t".join((self.cls, self.kind, self.value, self.row, self.note))


# §5S.9.1 rows 1-9 and 18: the moving sub-prefixes and exact keys of the shared
# families (the whole retired families -- u:, role:, apikey:, ... -- are their
# steps' Source and are not listed here).
MOVE = (
    Entry("move", "prefix", "idx:user:", "1", "M3: user indexes"),
    Entry("move", "prefix", "idx:role:name:", "2", "M3: role name index"),
    Entry("move", "prefix", "idx:apikey:", "3", "M3: API key indexes"),
    Entry("move", "prefix", "idx:invite:username:", "4a", "M3: deleted with the invites"),
    Entry("move", "prefix", "idx:oauth:", "5", "M3: OAuth identity indexes"),
    Entry("move", "prefix", "idx:sess:user:", "6", "M3: deleted with the web sessions"),
    Entry("move", "exact", "setting:config_blob", "7", "M2 rewrites it (Residual), M6 deletes it"),
    Entry("move", "exact", "setting:openai_api_key", "7", "M2: legacy secret row"),
    Entry("move", "exact", "setting:google_books_api_key", "7", "M2: legacy secret row"),
    Entry("move", "exact", "setting:hardcover_api_token", "7", "M2: legacy secret row"),
    Entry("move", "exact", "setting:basic_auth_password", "7", "M2: legacy secret row"),
    Entry("move", "exact", "setting:bootstrap_token_hash", "8", "M3: deleted"),
    Entry("move", "exact", "setting:bootstrap_token_expires_at", "8", "M3: deleted"),
    Entry("move", "prefix", "setting:scheduled_task_last_tick.", "9", "M4: task clocks"),
    Entry("move", "exact", "setting:library_scan_full_last_run", "9", "M4: task clock"),
    Entry("move", "exact", "setting:maintenance_window_last_run", "9", "M4: window clock"),
    Entry("move", "exact", "setting:registry.operations_paused", "9", "M4: pause marker"),
    Entry("move", "exact", "setting:registry.scan_standdown", "9", "M4: stand-down marker"),
    Entry("move", "prefix", "idx:sidx:dirty:", "18", "M6: the search dirty set, deleted"),
)

# Rows 1-7 are also books' nested claims (booksclaims.go); they must agree.
CLAIM_ROWS = frozenset({"1", "2", "3", "4a", "5", "6", "7"})

# §5S.9.1 "Rewritten in place by" (DATA-5): books families a step writes
# outside its Source. Each has its own before/after oracle in its owner's tests.
REWRITE = (
    Entry(
        "rewrite",
        "exact",
        "setting:system:migration:apikey_expiry_stamp_v1_done",
        "16",
        "M3 reads and deletes HEAD's one-shot key-expiry flag",
    ),
    Entry(
        "rewrite",
        "prefix",
        "usermerge:journal:",
        "19",
        "M3 rewrites each kept journal once, invite tokens hashed",
    ),
    Entry("rewrite", "prefix", "act:audit:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:batch:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:change:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:debug:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:info:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:system:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:digest:", "15", "M4: kind-rename map over stored def IDs"),
    Entry("rewrite", "prefix", "act:typ:", "15", "M4: kind-rename map over stored def IDs"),
    Entry(
        "rewrite", "prefix", "rootscan:", "19", "M6 writes the new books family (registered by F7)"
    ),
)

# Families a rewrite entry may name before keyfamilies.go registers them.
UNREGISTERED_OK = {"rootscan:": "registered in keyfamilies.go by F7 (05-library §5L.10)"}

# §5S.9.1 rows 10 and 16: the legacy migration track's ledger and the storage
# projection, written by every upgrade boot whatever steps it registers.
BOOKKEEPING = (
    Entry(
        "bookkeeping",
        "exact",
        "preference:storage_format",
        "10",
        "legacy guard projection; kept byte for byte",
    ),
    Entry(
        "bookkeeping",
        "exact",
        "preference:storage_migration",
        "10",
        "legacy guard projection; kept byte for byte",
    ),
    Entry(
        "bookkeeping",
        "exact",
        "preference:db_version",
        "16",
        "legacy schema version; never decreases",
    ),
    Entry(
        "bookkeeping",
        "prefix",
        "preference:migration_",
        "16",
        "legacy migration ledger; rows added, never rewritten",
    ),
    Entry(
        "bookkeeping",
        "exact",
        "counter:preference",
        "16",
        "the ID counter each new ledger row bumps (pebble_store_preferences.go nextID); never decreases",
    ),
)

_FAMILY_RE = re.compile(r'^\s*\{"([^"]+)",\s*"', re.M)
_SUBCLAIM_RE = re.compile(r'\{(Prefix|Key):\s*"([^"]+)"')
_IDX_LITERAL_RE = re.compile(r'"(idx:[a-z_]+:)')


def registered_families(src: str) -> list[str]:
    """The prefixes of keyfamilies.go's keyFamilies registry, in file order."""
    start = src.find("var keyFamilies = []KeyFamily{")
    if start < 0:
        raise ListError("keyfamilies.go: no `var keyFamilies = []KeyFamily{`")
    end = src.find("\n}\n", start)
    return _FAMILY_RE.findall(src[start:end])


def nested_claims(src: str) -> set[tuple[str, str]]:
    """booksclaims.go's serializedSubClaims as (kind, value) pairs."""
    start = src.find("var serializedSubClaims = []storage.Claim{")
    if start < 0:
        raise ListError("booksclaims.go: no `var serializedSubClaims = []storage.Claim{`")
    end = src.find("\n}\n", start)
    return {
        ("prefix" if k == "Prefix" else "exact", v) for k, v in _SUBCLAIM_RE.findall(src[start:end])
    }


def idx_literals(store_dir: pathlib.Path) -> set[str]:
    """Every idx:<name>: sub-prefix a non-test books store file spells."""
    out: set[str] = set()
    for p in sorted(store_dir.glob("*.go")):
        if p.name.endswith("_test.go") or p.name == "keyfamilies.go":
            continue
        out.update(_IDX_LITERAL_RE.findall(p.read_text(encoding="utf-8")))
    return out


def build_entries(
    families: list[str], claims: set[tuple[str, str]], idx_subs: set[str]
) -> list[Entry]:
    """The golden's entries, validated against their sources."""
    fams = set(families)
    for parent in ("idx:", "setting:", "preference:", "counter:"):
        if parent not in fams:
            raise ListError(f"keyfamilies.go does not register the shared family {parent}")
    table = {(e.kind, e.value) for e in MOVE if e.row in CLAIM_ROWS}
    if table != claims:
        raise ListError(
            "booksclaims.go's serializedSubClaims and §5S.9.1 rows 1-7 disagree: "
            f"only in the claims {sorted(claims - table)}, only in the table {sorted(table - claims)}"
        )
    for e in REWRITE:
        if e.kind == "prefix" and e.value not in fams and e.value not in UNREGISTERED_OK:
            raise ListError(f"rewrite entry {e.value} is not a family keyfamilies.go registers")
    keep: list[Entry] = []
    for f in families:
        if f.startswith("setting:") and f != "setting:":
            keep.append(
                Entry("keep", "prefix", f, "16", "registered setting: child (keyfamilies.go)")
            )
    moved = [e for e in MOVE if e.value.startswith("idx:")]
    for sub in sorted(idx_subs):
        if any(e.covers(sub) or sub.startswith(e.value) for e in moved):
            continue
        keep.append(
            Entry("keep", "prefix", sub, "18", "idx: remainder written by internal/books/store")
        )
    return sorted([*MOVE, *REWRITE, *keep, *BOOKKEEPING])


def family_of(key: str, families: list[str]) -> str:
    """The longest registered prefix of key, or "" (unregistered)."""
    best = ""
    for f in families:
        if key.startswith(f) and len(f) > len(best):
            best = f
    return best


def census_problems(
    keys: dict[str, list[str]], families: list[str], entries: list[Entry]
) -> list[str]:
    """Every corpus key the lists cannot classify, one line per prefix."""
    idx_entries = [e for e in entries if e.value.startswith("idx:") and e.cls in ("move", "keep")]
    bad: dict[str, set[str]] = {}
    for store, ks in sorted(keys.items()):
        for k in ks:
            fam = family_of(k, families)
            if fam == "":
                bad.setdefault(k.split(":", 1)[0] + ":", set()).add(f"{store}: unregistered family")
            elif fam == "idx:" and not any(e.covers(k) for e in idx_entries):
                parts = k.split(":")
                sub = ":".join(parts[:2]) + ":" if len(parts) > 2 else k
                bad.setdefault(sub, set()).add(f"{store}: no move or keep entry")
    return [f"{p} ({'; '.join(sorted(why))})" for p, why in sorted(bad.items())]


def render(entries: list[Entry]) -> str:
    head = [
        "# file: internal/books/testdata/shared_families.golden",
        f"# version: {GOLDEN_VERSION}",
        f"# guid: {GOLDEN_GUID}",
        f"# last-edited: {GOLDEN_EDITED}",
        "#",
        "# GENERATED by scripts/ci/gen_shared_family_lists.py; never edit by hand",
        '# (CI runs it with --check). 05-storage §5S.3.13 "The shared families",',
        "# §5S.9.1 rows 1-10, 15, 16, 18, 19. Columns (TAB): class kind value row note.",
        "# TestCutoverLeavesOtherFamiliesByteIdentical skips every move and rewrite",
        "# entry, checks the bookkeeping entries grow-only (bookkeepingProblems: every",
        "# row there before keeps its bytes, the schema version and the counter never",
        "# decrease), and hashes keep entries like every other books key.",
    ]
    return "\n".join(head + [e.line() for e in entries]) + "\n"


def parse_golden(text: str) -> list[Entry]:
    out = []
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        out.append(Entry(*line.split("\t")))
    return out


def run_census(repo: pathlib.Path) -> dict[str, list[str]]:
    """Run the Go key scan of every committed corpus store; {store: [key, ...]}."""
    with tempfile.TemporaryDirectory(prefix="census-") as tmp:
        out = pathlib.Path(tmp) / "census.json"
        env = {**os.environ, CENSUS_ENV: str(out)}
        cmd = ["go", "test", "./internal/books/", "-run", CENSUS_TEST, "-count=1"]
        proc = subprocess.run(cmd, cwd=repo, env=env, capture_output=True, text=True)
        if proc.returncode != 0 or not out.exists():
            raise ListError(
                f"the corpus key scan failed ({' '.join(cmd)}):\n{proc.stdout}{proc.stderr}"
            )
        return decode_census(json.loads(out.read_text(encoding="utf-8")))


def decode_census(doc: dict[str, list[str]]) -> dict[str, list[str]]:
    """The dump holds keys hex-encoded (a key is bytes); decode as latin-1."""
    return {
        store: [bytes.fromhex(h).decode("latin-1") for h in hexes] for store, hexes in doc.items()
    }


def generate(repo: pathlib.Path, census: dict[str, list[str]] | None) -> str:
    store = repo / "internal" / "books" / "store"
    families = registered_families((store / "keyfamilies.go").read_text(encoding="utf-8"))
    claims = nested_claims((store / "booksclaims.go").read_text(encoding="utf-8"))
    entries = build_entries(families, claims, idx_literals(store))
    keys = census if census is not None else run_census(repo)
    if not keys:
        raise ListError("the census found no corpus store")
    problems = census_problems(keys, families, entries)
    if problems:
        raise ListError(
            "the corpus holds keys no list classifies (STOP: report the prefix, never add it "
            "to the golden by hand):\n  " + "\n  ".join(problems)
        )
    return render(entries)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true", help="fail when the golden differs")
    mode.add_argument("--write", action="store_true", help="rewrite the golden")
    ap.add_argument("--census", help="a key dump to use instead of running the Go scan")
    ap.add_argument("--golden", default=str(GOLDEN))
    ap.add_argument("--repo", default=str(REPO_ROOT))
    args = ap.parse_args(argv)
    census = None
    if args.census:
        census = decode_census(json.loads(pathlib.Path(args.census).read_text(encoding="utf-8")))
    try:
        want = generate(pathlib.Path(args.repo), census)
    except ListError as e:
        print(f"gen_shared_family_lists: {e}", file=sys.stderr)
        return 1
    golden = pathlib.Path(args.golden)
    if args.write:
        golden.write_text(want, encoding="utf-8")
        print(f"wrote {golden}")
        return 0
    have = golden.read_text(encoding="utf-8") if golden.exists() else ""
    if have != want:
        import difflib

        sys.stdout.writelines(
            difflib.unified_diff(
                have.splitlines(True), want.splitlines(True), str(golden), "generated"
            )
        )
        print(f"gen_shared_family_lists: {golden} is stale: run with --write", file=sys.stderr)
        return 1
    print(f"{golden}: up to date")
    return 0


if __name__ == "__main__":
    sys.exit(main())
