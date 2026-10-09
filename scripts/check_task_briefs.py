#!/usr/bin/env python3
# file: scripts/check_task_briefs.py
# version: 1.0.0
# guid: 93f31e1f-3605-4f23-902b-2b8cf08b8c2d
# last-edited: 2026-10-09

"""Check and regenerate the dependency graph of the holistic-roadmap task briefs.

Every brief under docs/proposals/2026-10-holistic/tasks/<ws>/<ws>-<id>.md has a
header table. Its `Depends on` cell opens with the only authoritative ordering,
`**Merge first:** <comma-separated brief ids>.` (or `none`), followed by prose.
Its `Blocks` cell is generated from every brief's `Merge first` list.

    check_task_briefs.py                 verify: ids exist, waves respect deps, no cycles,
                                         Blocks mirrors Merge first (exit 1 on a problem)
    check_task_briefs.py --regen-blocks  rewrite every Blocks cell from the Merge first lists
                                         (bumps the version patch and last-edited of changed briefs)
    check_task_briefs.py --index         rewrite the counts box and the brief table in tasks/README.md

Run --regen-blocks and --index after any edit to a Merge first list or a Wave cell.
"""

from __future__ import annotations

import argparse
import datetime
import glob
import os
import re
import sys

TASKS = os.path.join(os.path.dirname(__file__), "..", "docs", "proposals", "2026-10-holistic", "tasks")
WAVE_ORDER = {"0": 0, "1": 1, "2": 2, "F": 3, "3": 4, "4": 5}
ID_RE = re.compile(r"\b(0[0-9]|1[01])[- ](PR|P|C|G|X|M|S|T|D|V|U|F|R) ?(\d+[a-z]?)\b")
MERGE_FIRST_RE = re.compile(r"^\*\*Merge first:\*\* (.*?)\. ")
BLOCKS_RE = re.compile(r"^(.*?)(?:; not briefed \(roadmap waves 3 to 4\): (.*?))? \(generated from every brief's `Merge first` list on \d{4}-\d{2}-\d{2}; do not hand-edit\)$")


def field(text: str, name: str) -> str:
    m = re.search(r"^\| %s \| *(.*?) *\|$" % re.escape(name), text, re.M)
    return m.group(1) if m else ""


def load(root: str) -> dict[str, dict]:
    briefs: dict[str, dict] = {}
    for path in sorted(glob.glob(os.path.join(root, "*", "*-*.md"))):
        if path.endswith("README.md"):
            continue
        bid = os.path.basename(path)[:-3]
        text = open(path, encoding="utf-8").read()
        title = re.search(r"^# [0-9A-Za-z-]+: (.*)$", text, re.M)
        mf = MERGE_FIRST_RE.match(field(text, "Depends on"))
        bl = BLOCKS_RE.match(field(text, "Blocks"))
        briefs[bid] = {
            "path": path,
            "text": text,
            "title": title.group(1).strip() if title else "",
            "wave": field(text, "Wave").split()[0].strip("*`"),
            "model": field(text, "Model").split()[0].strip("*`").lower(),
            "size": field(text, "Size").split()[0].strip("*`"),
            "merge_first": [] if not mf or mf.group(1) == "none" else mf.group(1).split(", "),
            "merge_first_ok": mf is not None,
            "blocks": [] if not bl or bl.group(1) == "none briefed" else bl.group(1).split(", "),
            "blocks_dangling": bl.group(2).split(", ") if bl and bl.group(2) else [],
            "blocks_ok": bl is not None,
        }
    return briefs


def verify(briefs: dict[str, dict]) -> int:
    problems = 0

    def report(msg: str) -> None:
        nonlocal problems
        problems += 1
        print(msg)

    for bid, b in briefs.items():
        if not b["merge_first_ok"]:
            report(f"{bid}: Depends on does not open with '**Merge first:** ...'")
        if not b["blocks_ok"]:
            report(f"{bid}: Blocks is not in the generated form")
        if b["wave"] not in WAVE_ORDER:
            report(f"{bid}: Wave '{b['wave']}' is not one of 0/1/2/F/3/4")
        for x in b["merge_first"]:
            if x not in briefs:
                report(f"{bid}: Merge first names {x}, which has no brief")
                continue
            if WAVE_ORDER.get(briefs[x]["wave"], 9) > WAVE_ORDER.get(b["wave"], 9):
                report(f"{bid} (wave {b['wave']}) must merge after {x} (wave {briefs[x]['wave']})")
            if bid not in briefs[x]["blocks"]:
                report(f"{x}: Blocks is stale (missing {bid}); run --regen-blocks")
        for x in b["blocks"]:
            if x not in briefs or bid not in briefs[x]["merge_first"]:
                report(f"{bid}: Blocks names {x} but {x} does not list {bid} in Merge first; run --regen-blocks")
    color = {bid: 0 for bid in briefs}

    def dfs(u: str, stack: list[str]) -> None:
        color[u] = 1
        for v in briefs[u]["merge_first"]:
            if v not in briefs:
                continue
            if color[v] == 1:
                report("cycle: " + " -> ".join(stack + [v]))
            elif color[v] == 0:
                dfs(v, stack + [v])
        color[u] = 2

    for bid in briefs:
        if color[bid] == 0:
            dfs(bid, [bid])
    print(f"briefs: {len(briefs)}  problems: {problems}")
    return problems


def bump_header(text: str, today: str) -> str:
    text = re.sub(
        r"<!-- version: (\d+)\.(\d+)\.(\d+) -->",
        lambda m: f"<!-- version: {m.group(1)}.{m.group(2)}.{int(m.group(3)) + 1} -->",
        text,
        count=1,
    )
    return re.sub(r"<!-- last-edited: \d{4}-\d{2}-\d{2} -->", f"<!-- last-edited: {today} -->", text, count=1)


def regen_blocks(briefs: dict[str, dict]) -> int:
    today = datetime.date.today().isoformat()
    blocked_by: dict[str, set[str]] = {bid: set() for bid in briefs}
    for bid, b in briefs.items():
        for x in b["merge_first"]:
            if x in blocked_by:
                blocked_by[x].add(bid)
    changed = 0
    for bid, b in sorted(briefs.items()):
        want = sorted(blocked_by[bid])
        cell = ", ".join(want) if want else "none briefed"
        if b["blocks_dangling"]:
            cell += "; not briefed (roadmap waves 3 to 4): " + ", ".join(b["blocks_dangling"])
        cell += f" (generated from every brief's `Merge first` list on {today}; do not hand-edit)"
        old_cell = field(b["text"], "Blocks")
        if re.sub(r" on \d{4}-\d{2}-\d{2};", " on DATE;", old_cell) == re.sub(r" on \d{4}-\d{2}-\d{2};", " on DATE;", cell):
            continue
        text, n = re.subn(r"^\| Blocks \| .*? \|$", lambda m: f"| Blocks | {cell} |", b["text"], count=1, flags=re.M)
        if n != 1:
            print(f"{bid}: no Blocks row")
            continue
        open(b["path"], "w", encoding="utf-8").write(bump_header(text, today))
        changed += 1
    print(f"Blocks rewritten: {changed}")
    return 0


def regen_index(briefs: dict[str, dict], root: str) -> int:
    readme = os.path.join(root, "README.md")
    src = open(readme, encoding="utf-8").read()
    rows = sorted(briefs.items(), key=lambda kv: (WAVE_ORDER.get(kv[1]["wave"], 9), kv[0]))
    sonnet = sum(1 for _, b in rows if b["model"] == "sonnet")
    opus = sum(1 for _, b in rows if b["model"] == "opus")
    if sonnet + opus != len(rows):
        print("model cells that are neither sonnet nor opus:", [bid for bid, b in rows if b["model"] not in ("sonnet", "opus")])
        return 1
    waves = {w: sum(1 for _, b in rows if b["wave"] == w) for w in WAVE_ORDER}
    counts = "| Count | Value |\n|---|---|\n| Briefs | %d |\n| Sonnet / Opus | %d / %d |\n" % (len(rows), sonnet, opus)
    counts += "".join("| Wave %s | %d |\n" % (w, waves[w]) for w in WAVE_ORDER if waves[w])
    src, c1 = re.subn(r"\| Count \| Value \|\n\|---\|---\|\n(?:\|.*\|\n)+", counts, src, count=1)
    table = "| Brief | Title | Wave | Model | Size | Merge first |\n|---|---|---|---|---|---|\n"
    for bid, b in rows:
        table += "| [%s](%s/%s.md) | %s | %s | %s | %s | %s |\n" % (
            bid, bid.split("-")[0], bid, b["title"], b["wave"], b["model"], b["size"],
            ", ".join(b["merge_first"]) if b["merge_first"] else "none",
        )
    src, c2 = re.subn(
        r"\| Brief \| Title \| Wave \| Model \| Size \| (?:Depends on|Merge first) \|\n\|---\|---\|---\|---\|---\|---\|\n(?:\|.*\|\n)+",
        table, src, count=1,
    )
    if c1 != 1 or c2 != 1:
        print("README.md: counts box or brief table not found")
        return 1
    old = open(readme, encoding="utf-8").read()
    if src != old:
        src = re.sub(
            r"<!-- version: (\d+)\.(\d+)\.(\d+) -->",
            lambda m: f"<!-- version: {m.group(1)}.{int(m.group(2)) + 1}.0 -->", src, count=1,
        )
        src = re.sub(r"<!-- last-edited: \d{4}-\d{2}-\d{2} -->", f"<!-- last-edited: {datetime.date.today().isoformat()} -->", src, count=1)
        open(readme, "w", encoding="utf-8").write(src)
    print("index: briefs=%d sonnet=%d opus=%d waves=%s%s" % (len(rows), sonnet, opus, {w: n for w, n in waves.items() if n}, "" if src != old else " (unchanged)"))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--tasks", default=os.path.normpath(TASKS), help="tasks directory (default: the repo's)")
    ap.add_argument("--regen-blocks", action="store_true")
    ap.add_argument("--index", action="store_true")
    args = ap.parse_args()
    briefs = load(args.tasks)
    if not briefs:
        print("no briefs under", args.tasks)
        return 1
    if args.regen_blocks:
        rc = regen_blocks(briefs)
        briefs = load(args.tasks)
    else:
        rc = 0
    if args.index:
        rc = regen_index(briefs, args.tasks) or rc
        briefs = load(args.tasks)
    return 1 if verify(briefs) else rc


if __name__ == "__main__":
    sys.exit(main())
