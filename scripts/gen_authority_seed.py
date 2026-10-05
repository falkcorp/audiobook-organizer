#!/usr/bin/env python3
# file: scripts/gen_authority_seed.py
# version: 1.1.0
# guid: dff4675b-1040-403a-a41d-de92bbf943e7
# last-edited: 2026-10-05

"""Generate the authority-list seed from an Audible library export.

Usage:
    scripts/gen_authority_seed.py EXPORT.json [--out PATH] [--check]

EXPORT.json is an ``audible-cli library export --format json`` file: a list of
product objects whose ``authors`` and ``narrators`` are lists of
``{"name": ..., "asin": ...}`` and whose ``publisher_name`` is a string. A
wrapper object with an ``items`` list is accepted too.

The seed holds PUBLIC contributor facts only (owner decision 2026-10-04):
author, narrator and publisher NAMES and contributor ASINs. It never holds a
book title, a product ASIN, a series, or any per-title data or count. Two
guards enforce that here, before anything is written, and the Go test
internal/authority/authoritybuild/seed_test.go re-checks the file's shape:

* a name whose letters key equals the letters key of any title, subtitle or
  series title in the export is dropped (reported on stderr); and
* a contributor ASIN equal to any product ``asin``, ``sku``, ``sku_lite`` or
  ``amazon_asin`` in the export is dropped (reported on stderr).

Cast credits. Author-array members of a cast-context product are cast
members, not authors (owner decision: Big Finish / franchise casts are
cast_author only, never author evidence). A product is cast context when it
has more than 3 authors, carries a dramatized / full-cast marker in its title
or subtitle, or matches the owner's manual-only patterns (Doctor Who / Big
Finish / Torchwood ...) in its title, subtitle, series or publisher. The test
fails toward cast. These rules are a port of internal/catalog/entry.go
(EditionKind) and internal/applygate/manual_only.go; keep them in step. The
opt-in Go test TestSeed_AuthorsAreNotCastOnlyUnderGoRules (set
AUTHORITY_EXPORT_PATH) checks this port against the Go rules on a real export.
A cast-only person is left out of the seed entirely unless they also narrate;
then they appear as a narrator.

Role-marked credits ("Jane Doe - translator", "Read by Jane Doe") are split
with a port of internal/metadata/contributor_roles.go ClassifyContributor:
the bare name is kept, and a translator / illustrator / editor / introduction
credit is never author evidence. A person known only from such credits is
left out. Absence means nothing,
so leaving a name out is always safe.

Collective credits ("Full Cast", "Various Authors", ...) are never emitted.

--check exits 1 when the file at --out differs from what would be written.
"""

from __future__ import annotations

import argparse
import collections
import json
import re
import sys
import unicodedata
import uuid
from datetime import date
from pathlib import Path

DEFAULT_OUT = Path("internal/authority/authoritybuild/seed/authority_seed.json")
SEED_SOURCE = "owner_library_seed"
SEED_TIER = "O"

# internal/authorcredit/authorcredit.go collectiveCredits (letters keys).
COLLECTIVE = {
    "fullcast", "cast", "fullcastproduction", "fullcastdramatization",
    "various", "variousauthors", "variousnarrators", "variousartists",
    "dramatized", "dramatization", "others", "etal", "andothers",
    "multipleauthors", "multiplenarrators", "uncredited",
}

# internal/catalog/entry.go dramatizedRe / fullCastRe.
DRAMATIZED_RE = re.compile(
    r"\bdramati[sz](?:ed|ation)\b|\bradio\s+(?:drama|play)\b|\baudio\s+drama\b", re.I)
FULL_CAST_RE = re.compile(r"\bfull[\s-]+cast\b", re.I)

# internal/applygate/manual_only.go manualOnlyRe / manualOnlyUnitRe /
# counterMeasuresRe.
MANUAL_ONLY_RE = re.compile(
    r"\b(doctor[\s._-]*who|dr\.?[\s._-]*who|big[\s._-]*finish|torchwood|"
    r"(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|"
    r"thirteenth|fourteenth|fifteenth|[1-9](?:st|nd|rd|th)|1[0-5]th|war|fugitive)[\s._-]*doctor|"
    r"gallifrey(?:an)?|daleks?|jago[\s._-]*(?:&|and)[\s._-]*litefoot|"
    r"diary[\s._-]*of[\s._-]*river[\s._-]*song|bernice[\s._-]*summerfield|"
    r"paternoster[\s._-]*gang|missy|blake[’']?s[\s._-]*7)\b",
    re.I)
MANUAL_ONLY_UNIT_RE = re.compile(r"(?:^|[/\\])\s*UNIT(?:\s*[:–—-]|\s)")
COUNTER_MEASURES_RE = re.compile(r"(?:^|[\W_])counter[._-]+measures(?:$|[\W_])", re.I)

MAX_PLAIN_AUTHORS = 3

# internal/metadata/contributor_roles.go ClassifyContributor: a role written
# after or before the name. Translator / illustrator / editor / introduction
# credits are "other" and never author evidence.
ROLE_SUFFIX_RE = re.compile(
    r"^(.*?)\s*(?:[-\u2013\u2014,]\s*|\(\s*|\[\s*)(editor|compiler|translator|trans\.|illustrator|"
    r"narrator|reader|foreword|introduction|afterword|contributor|adapter)\s*[)\]]?\s*$", re.I)
ROLE_ED_ABBREV_RE = re.compile(r"^(.*?)\s*(?:\(\s*eds?\.?\s*\)|\[\s*eds?\.?\s*\]|,\s*eds?\.)\s*$", re.I)
ROLE_PREFIX_RE = re.compile(
    r"^\s*(edited|compiled|translated|illustrated|narrated|read|adapted|introduced)\s+by\s+(.+?)\s*$", re.I)


def role_from_word(w: str) -> str:
    w = w.strip().lower()
    return "narrator" if w.startswith("narrat") or w.startswith("read") else "other"


def classify_contributor(credit: str) -> tuple[str, str]:
    """Port of metadata.ClassifyContributor: (bare name, author|narrator|other)."""
    credit = credit.strip()
    m = ROLE_PREFIX_RE.match(credit)
    if m:
        return m.group(2).strip(), role_from_word(m.group(1))
    m = ROLE_SUFFIX_RE.match(credit)
    if m and m.group(1).strip():
        return m.group(1).strip(), role_from_word(m.group(2))
    m = ROLE_ED_ABBREV_RE.match(credit)
    if m and m.group(1).strip():
        return m.group(1).strip(), "other"
    return credit, "author"


def letters_key(s: str) -> str:
    """internal/authorcredit.LettersKey: NFC, lower-cased, letters and digits."""
    out = []
    for ch in unicodedata.normalize("NFC", (s or "").lower()):
        cat = unicodedata.category(ch)
        if cat.startswith("L") or cat == "Nd":
            out.append(ch)
    return "".join(out)


def matches_manual_only(s: str) -> bool:
    if not s:
        return False
    folded = s.replace("_", " ")
    return bool(MANUAL_ONLY_RE.search(folded) or MANUAL_ONLY_UNIT_RE.search(folded)
                or COUNTER_MEASURES_RE.search(s))


def series_titles(item: dict) -> list[str]:
    return [str(s.get("title") or "") for s in (item.get("series") or []) if isinstance(s, dict)]


def is_cast_context(item: dict) -> bool:
    authors = [a for a in (item.get("authors") or []) if isinstance(a, dict)]
    if len(authors) > MAX_PLAIN_AUTHORS:
        return True
    text = f"{item.get('title') or ''} {item.get('subtitle') or ''}"
    if DRAMATIZED_RE.search(text) or FULL_CAST_RE.search(text):
        return True
    if matches_manual_only(text) or matches_manual_only(str(item.get("publisher_name") or "")):
        return True
    return any(matches_manual_only(t) for t in series_titles(item))


def clean(s) -> str:
    return " ".join(str(s or "").split())


class Person:
    def __init__(self) -> None:
        self.displays: collections.Counter[str] = collections.Counter()
        self.asins: set[str] = set()
        self.author = 0
        self.cast = 0
        self.narrator = 0
        self.other = 0


def pick_display(c: collections.Counter) -> str:
    # Most frequent spelling, then lexical, so a regeneration is stable.
    return sorted(c.items(), key=lambda kv: (-kv[1], kv[0]))[0][0]


def load_items(path: Path) -> list[dict]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(data, dict):
        data = data.get("items")
    if not isinstance(data, list):
        raise SystemExit(f"{path}: expected a JSON list of products (or an object with 'items')")
    return [x for x in data if isinstance(x, dict)]


def build(items: list[dict]) -> tuple[list[dict], dict]:
    people: dict[str, Person] = collections.defaultdict(Person)
    pubs: dict[str, collections.Counter] = collections.defaultdict(collections.Counter)
    title_keys: set[str] = set()
    product_ids: set[str] = set()

    for it in items:
        for t in (it.get("title"), it.get("subtitle"), *series_titles(it)):
            k = letters_key(str(t or ""))
            if k:
                title_keys.add(k)
        for f in ("asin", "sku", "sku_lite", "amazon_asin"):
            v = clean(it.get(f)).upper()
            if v:
                product_ids.add(v)

        cast = is_cast_context(it)
        for role, field in (("author", "authors"), ("narrator", "narrators")):
            for c in it.get(field) or []:
                if not isinstance(c, dict):
                    continue
                name, marked = classify_contributor(clean(c.get("name")))
                key = letters_key(name)
                if not key or key in COLLECTIVE:
                    continue
                p = people[key]
                p.displays[name] += 1
                asin = clean(c.get("asin")).upper()
                if asin:
                    p.asins.add(asin)
                if marked == "other":
                    p.other += 1  # translator, illustrator, ...: never an author
                elif role == "narrator" or marked == "narrator":
                    p.narrator += 1
                elif cast:
                    p.cast += 1
                else:
                    p.author += 1
        pub = clean(it.get("publisher_name"))
        if letters_key(pub):
            pubs[letters_key(pub)][pub] += 1

    dropped = {"name_equals_title": [], "asin_equals_product": []}
    entries: list[dict] = []

    def keep_asins(asins: set[str], owner: str) -> list[str]:
        out = []
        for a in sorted(asins):
            if a in product_ids:
                dropped["asin_equals_product"].append(owner)
            else:
                out.append(a)
        return out

    for key, p in people.items():
        display = pick_display(p.displays)
        if key in title_keys:
            dropped["name_equals_title"].append(display)
            continue
        if p.author > 0:
            kind, also_narrator = "author", p.narrator > 0
        elif p.narrator > 0:
            kind, also_narrator = "narrator", False
        else:
            continue  # cast-only or role-only (translator ...): left out
        entries.append({
            "kind": kind,
            "name": display,
            "asins": keep_asins(p.asins, display),
            "also_narrator": also_narrator,
        })
    for key, c in pubs.items():
        display = pick_display(c)
        if key in title_keys:
            dropped["name_equals_title"].append(display)
            continue
        entries.append({"kind": "publisher", "name": display, "asins": [], "also_narrator": False})

    entries.sort(key=lambda e: (e["kind"], letters_key(e["name"]), e["name"]))
    return entries, dropped


def render(entries: list[dict], previous: dict | None, out: Path) -> dict:
    prev_entries = (previous or {}).get("entries")
    guid = (previous or {}).get("guid") or str(uuid.uuid4())
    version = (previous or {}).get("version") or "1.0.0"
    last_edited = (previous or {}).get("last_edited") or date.today().isoformat()
    if previous is not None and prev_entries != entries:
        major, minor, _patch = (int(x) for x in version.split("."))
        version = f"{major}.{minor + 1}.0"
        last_edited = date.today().isoformat()
    return {
        "file": out.as_posix(),
        "version": version,
        "guid": guid,
        "last_edited": last_edited,
        "source": SEED_SOURCE,
        "tier": SEED_TIER,
        "entries": entries,
    }


def dump(doc: dict) -> str:
    """JSON with one entry per line, so a regeneration diffs line by line."""
    head = {k: v for k, v in doc.items() if k != "entries"}
    lines = ["{"]
    for k, v in head.items():
        lines.append(f" {json.dumps(k)}: {json.dumps(v, ensure_ascii=False)},")
    lines.append(' "entries": [')
    rows = [json.dumps(e, ensure_ascii=False, separators=(", ", ": ")) for e in doc["entries"]]
    lines.append(",\n".join(f"  {r}" for r in rows))
    lines.append(" ]")
    lines.append("}")
    return "\n".join(lines) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("export", type=Path)
    ap.add_argument("--out", type=Path, default=DEFAULT_OUT)
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()

    entries, dropped = build(load_items(args.export))
    for reason, names in dropped.items():
        if names:
            print(f"dropped {len(names)} ({reason}): {', '.join(sorted(set(names)))}", file=sys.stderr)

    previous = None
    if args.out.exists():
        previous = json.loads(args.out.read_text(encoding="utf-8"))
    doc = render(entries, previous, args.out)
    text = dump(doc)

    counts = collections.Counter(e["kind"] for e in entries)
    print(f"seed: authors={counts['author']} narrators={counts['narrator']} "
          f"publishers={counts['publisher']}", file=sys.stderr)

    if args.check:
        current = args.out.read_text(encoding="utf-8") if args.out.exists() else ""
        if current != text:
            print(f"{args.out} is stale; rerun without --check", file=sys.stderr)
            return 1
        return 0
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
