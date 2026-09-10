#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/build_matrix.py
# version: 1.1.0
# guid: 5d1f8b2c-3e7a-4a95-b6c0-7f2e9d4a1c83
# last-edited: 2026-09-10
"""Build PRIORITY-MATRIX.md (risk-ordered + effort-ordered) from merged.json.

Usage: build_matrix.py <state-dir> <out-md>
Rows: surviving REAL briefs, Wave 3 audit findings, and uncovered REAL TODO items
rolled up per TODO.md section. Re-runnable.
"""
import collections
import json
import os
import re
import sys

STATE, OUT = sys.argv[1], sys.argv[2]
d = json.load(open(os.path.join(STATE, "merged.json"), encoding="utf-8"))
REPO = os.path.abspath(os.path.join(STATE, "..", "..", "..", ".."))
PKG = "docs/agent-tasks/todo-completion-2026-09"
TODO_LINES = open(os.path.join(REPO, "TODO.md"), encoding="utf-8").read().split("\n")

# state/final/brief_index.json is written by gen_new_package.py: every brief in the package with
# its dispatch verdict (2026-09-10 validation) and source key. Optional — the matrix still builds
# without it, just without the Brief column and the dispatch gates.
_idx_path = os.path.join(STATE, "final", "brief_index.json")
BRIEFS = json.load(open(_idx_path, encoding="utf-8")) if os.path.exists(_idx_path) else []
BRIEF_BY_ID = {b["id"]: b for b in BRIEFS}
BRIEF_BY_SOURCE = {b["source_key"]: b for b in BRIEFS if b.get("source_key")}
BRIEF_BY_TODO_LINE = {ln: b for b in BRIEFS for ln in (b.get("todo_lines") or [])}


def real_heading(line):
    """Nearest heading above `line` in TODO.md (same rule as gen_new_package.py) — the inventory's
    `section` field tracked only `## ` headings and mis-filed ~50 items."""
    found = []
    for i in range(int(line) - 2, -1, -1):
        m = re.match(r"^(#{1,6})\s+(.*\S)\s*$", TODO_LINES[i])
        if m:
            found.append((i + 1, len(m.group(1)), m.group(2)))
            if len(m.group(2)) >= 25 or len(found) == 2:
                break
    if not found:
        return 0, "(no heading)"
    if len(found) == 2 and found[1][1] < found[0][1]:
        return found[0][0], f"{found[1][2]} › {found[0][2]}"
    return found[0][0], found[0][2]

RISK_RANK = {"data-loss": 0, "security": 1, "correctness": 2, "perf": 3, "ux": 4, "hygiene": 5, "null": 6, None: 6}
SEV_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3, None: 4}
EFF_RANK = {"S": 0, "M": 1, "L": 2, None: 3, "null": 3}
GATED = {
    "docs/agent-tasks/torrent-relocation/": "parked (DECISIONS-PENDING row 2)",
    "docs/agent-tasks/ai-responses-migration/": "on hold (useResponsesAPI design)",
}

rows = []

# 1. surviving briefs
for b in d["briefs"]:
    if b["verdict"] != "REAL":
        continue
    gate = next((v for k, v in GATED.items() if b["path"].startswith(k)), None)
    carried = BRIEF_BY_ID.get(b["task_id"]) if b["initiative"] == "todo-completion" else None
    rows.append({
        "kind": "brief",
        "id": b["task_id"] if b["initiative"] == "todo-completion" else f"{b['initiative']}/{b['task_id']}",
        "title": b["title"][:110],
        "risk": b.get("risk") or "hygiene",
        "severity": None,
        "effort": b.get("effort") or "M",
        "count": 1,
        "anchor": carried["path"] if carried else b["path"],  # carried briefs live in the 2026-09 package now
        "gate": gate,
        "brief": carried["id"] if carried else "",
    })

# 2. audit findings
for f in d["findings"]:
    rows.append({
        "kind": "finding",
        "id": f["id"],
        "title": f["title"][:110],
        "risk": f.get("risk") or "correctness",
        "severity": f.get("severity"),
        "effort": f.get("effort") or "M",
        "count": 1,
        "anchor": f"{f.get('file')}:{f.get('line')}",
        "gate": None,
        "brief": (BRIEF_BY_SOURCE.get(f["id"]) or {}).get("id", ""),
    })

# 3. uncovered REAL TODO items, rolled up per section
sections = collections.defaultdict(list)
for t in d["todo"]:
    if t["verdict"] == "REAL" and not t.get("dup_of"):
        # group by the brief that covers the item (one row per brief), else by the REAL heading
        b = BRIEF_BY_TODO_LINE.get(t["line"])
        sections[("brief", b["id"], b["title"]) if b else ("heading",) + real_heading(t["line"])].append(t)
for key, items in sections.items():
    worst = min(items, key=lambda t: RISK_RANK.get(t.get("risk"), 6))
    cheapest = min(items, key=lambda t: EFF_RANK.get(t.get("effort"), 3))
    b = BRIEF_BY_ID.get(key[1]) if key[0] == "brief" else None
    risk = worst.get("risk") or "hygiene"
    gate = None
    if b:
        risk = b["risk"]  # RECLASSIFY verdicts lower the class
        if b["dispatch"] != "DISPATCH":
            gate = f"{b['dispatch']} — {b['dispatch_why'][:120]}"
    rows.append({
        "kind": "todo-section",
        "id": f"TODO.md:{min(t['line'] for t in items)}",
        "title": (b["title"] if b else key[2])[:110],
        "risk": risk,
        "severity": None,
        "effort": cheapest.get("effort") or "M",
        "count": len(items),
        "anchor": "lines " + ",".join(str(t["line"]) for t in sorted(items, key=lambda t: t["line"])[:6]) + (" …" if len(items) > 6 else ""),
        "gate": gate,
        "brief": b["id"] if b else "",
    })


def risk_key(r):
    return (RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["severity"], 4), EFF_RANK.get(r["effort"], 3), r["id"])


def effort_key(r):
    return (EFF_RANK.get(r["effort"], 3), RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["severity"], 4), r["id"])


def table(sorted_rows, limit=None):
    out = ["| # | Kind | Id | Brief | Risk | Sev | Effort | n | Title | Anchor / gate |", "|---|---|---|---|---|---|---|---|---|---|"]
    for i, r in enumerate(sorted_rows[:limit] if limit else sorted_rows, 1):
        gate = f" · **GATED: {r['gate']}**" if r["gate"] else ""
        brief = f"`{r['brief']}`" if r.get("brief") else "—"
        out.append(f"| {i} | {r['kind']} | `{r['id']}` | {brief} | {r['risk']} | {r['severity'] or '—'} | {r['effort']} | {r['count']} | {r['title']} | `{r['anchor']}`{gate} |")
    return "\n".join(out)


by_kind = collections.Counter(r["kind"] for r in rows)
by_risk = collections.Counter(r["risk"] for r in rows)
by_effort = collections.Counter(r["effort"] for r in rows)

md = f"""<!-- file: docs/agent-tasks/todo-completion-2026-09/PRIORITY-MATRIX.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8e2c4f7a-1d5b-4b39-9a6e-3c8f0d2b7e41 -->
<!-- last-edited: 2026-09-10 -->

# Priority matrix — burndown 2026-09-10

Generated by `state/tools/build_matrix.py` from `state/merged.json`; regenerate, never
hand-edit. One row per unit of work: a surviving brief (`brief`), a Wave 3 audit
finding (`finding`), or a TODO.md section holding REAL items that no brief covers
(`todo-section`, `n` = item count). `Brief` is the TASK id in this package that covers
the row (findings → TASK-300+, data-loss/security TODO sections → TASK-335+; carried briefs
are their own id); `—` means no brief yet — brief it on demand when the cut line reaches it.
Pick a cut line in either table; the two orderings are the same rows.

`GATED` rows: owner-gated sibling initiatives, plus `HOLD-FOR-OWNER` TODO sections whose
items are decisions or prod runs (2026-09-10 validation, `state/final/todo_sections_validation.json`)
— they stay in the ranking so the cut line is complete, but they are not worker tasks.

**Rows: {len(rows)}** — {dict(by_kind)}.
By risk: {dict(sorted(by_risk.items(), key=lambda kv: RISK_RANK.get(kv[0], 6)))}.
By effort: {dict(sorted(by_effort.items(), key=lambda kv: EFF_RANK.get(kv[0], 3)))}.

Risk order: data-loss → security → correctness → perf → ux → hygiene; within a risk,
severity (critical→low) then effort (S→L). Effort order: S → M → L; within an effort,
risk then severity. `GATED` rows are REAL but need an owner decision before dispatch.

## A. Risk-ordered (most dangerous first)

{table(sorted(rows, key=risk_key))}

## B. Effort-ordered (quickest wins first)

{table(sorted(rows, key=effort_key))}
"""
with open(OUT, "w", encoding="utf-8") as fh:
    fh.write(md)
print(f"rows={len(rows)} kinds={dict(by_kind)} risk={dict(by_risk)} effort={dict(by_effort)}")
