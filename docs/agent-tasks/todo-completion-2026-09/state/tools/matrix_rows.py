#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/matrix_rows.py
# version: 1.0.0
# guid: 3a9e6c1d-7b42-4f08-9d5e-2c1b8a7f6e40
# last-edited: 2026-09-11
"""Shared row builder for PRIORITY-MATRIX.md and QUADRANT.md.

One row per unit of work — a surviving REAL brief, a Wave 3 audit finding, or a
TODO.md section holding REAL items no brief covers — built from `merged.json`
(frozen 2026-09-10) and then reduced by what has landed since:

* `final/done_since_0910.json` names the finding ids and brief ids whose PR merged
  after the freeze; those rows are dropped.
* TODO.md items are re-located by their `text_head` (assembly shifts every line
  daily) and an item found on a `- [x]` line is done. A section whose items are
  all done is dropped; a partially-done section keeps only its open items.

`build_rows(state_dir)` returns `(rows, done)`; every row carries the fields the
two renderers need plus `label` (a short mermaid-safe id) and `url` (a GitHub
link to the brief, the finding's source line, or the section's first open line).
"""
import collections
import json
import math
import os
import re

REPO_SLUG = "falkcorp/audiobook-organizer"
PKG = "docs/agent-tasks/todo-completion-2026-09"

RISK_RANK = {"data-loss": 0, "security": 1, "correctness": 2, "perf": 3, "ux": 4, "hygiene": 5, "null": 6, None: 6}
SEV_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3, None: 4}
EFF_RANK = {"S": 0, "M": 1, "L": 2, None: 3, "null": 3}
GATED = {
    "docs/agent-tasks/torrent-relocation/": "parked (DECISIONS-PENDING row 2)",
    "docs/agent-tasks/ai-responses-migration/": "on hold (useResponsesAPI design)",
}
# Short prefixes for briefs that live in sibling initiatives (their TASK ids collide
# with the package's own TASK-0xx numbering, so the label carries the initiative).
INITIATIVE_PREFIX = {
    "torrent-relocation": "TR",
    "ai-responses-migration": "AI",
    "ux-small-items": "UX",
    "dedup-pipeline-hardening": "DD",
    "bug-techdebt": "BT",
}


def _load_json(path, default):
    if os.path.exists(path):
        return json.load(open(path, encoding="utf-8"))
    return default


def _strip_box(text):
    text = (text or "").strip()
    for pre in ("- [ ]", "- [x]", "- [X]"):
        if text.startswith(pre):
            return text[len(pre):].strip()
    return text


class TodoLocator:
    """Find a merged.json TODO item's CURRENT line in TODO.md and whether it is checked.

    The inventory recorded 2026-09-10 line numbers; `assemble_todo.py` prepends fragments
    daily so every line has moved since. Match on the first 50 characters of the item's
    text (checkbox stripped), preferring the hit nearest the recorded line.
    """

    def __init__(self, todo_lines):
        self.lines = todo_lines
        self.by_key = collections.defaultdict(list)
        for i, line in enumerate(todo_lines):
            body = _strip_box(line)
            if body:
                self.by_key[body[:50]].append(i + 1)

    def locate(self, item):
        """Return (current_line, is_done). current_line is None when the text is gone."""
        key = _strip_box(item.get("text_head"))[:50]
        hits = self.by_key.get(key) or []
        if not hits:
            return None, False
        recorded = int(item["line"])
        line = min(hits, key=lambda n: abs(n - recorded))
        stripped = self.lines[line - 1].strip()
        return line, stripped.startswith("- [x]") or stripped.startswith("- [X]")


def _label_for(kind, row_id):
    """Mermaid-safe short label: letters, digits and hyphens only."""
    if kind == "finding":
        return row_id
    if kind == "todo-section":
        return "L" + row_id.split(":", 1)[1]
    if "/" in row_id:
        initiative, task = row_id.split("/", 1)
        return f"{INITIATIVE_PREFIX.get(initiative, initiative[:2].upper())}-{task.replace('TASK-', '')}"
    return "T" + row_id.replace("TASK-", "")


def _blob(path, line=None, plain=False):
    url = f"https://github.com/{REPO_SLUG}/blob/main/{path}"
    if plain:
        url += "?plain=1"
    if line:
        url += f"#L{line}"
    return url


def build_rows(state_dir):
    """Build the open rows and the list of rows done since the 2026-09-10 freeze."""
    state_dir = os.path.abspath(state_dir)
    d = json.load(open(os.path.join(state_dir, "merged.json"), encoding="utf-8"))
    repo = os.path.abspath(os.path.join(state_dir, "..", "..", "..", ".."))
    todo_lines = open(os.path.join(repo, "TODO.md"), encoding="utf-8").read().split("\n")
    locator = TodoLocator(todo_lines)

    briefs = _load_json(os.path.join(state_dir, "final", "brief_index.json"), [])
    brief_by_id = {b["id"]: b for b in briefs}
    brief_by_source = {b["source_key"]: b for b in briefs if b.get("source_key")}
    brief_by_todo_line = {ln: b for b in briefs for ln in (b.get("todo_lines") or [])}
    design = {}
    for name in ("design_fit_rows_1_29.json", "design_fit_rows_31_64.json"):
        for r in _load_json(os.path.join(state_dir, "final", name), []):
            design[r["brief"]] = r
    ledger = _load_json(os.path.join(state_dir, "final", "done_since_0910.json"), {})
    done_findings = ledger.get("findings") or {}
    done_briefs = ledger.get("briefs") or {}

    rows, done = [], []

    def real_heading(line):
        """Nearest heading above `line` (same rule as gen_new_package.py)."""
        found = []
        for i in range(int(line) - 2, -1, -1):
            m = re.match(r"^(#{1,6})\s+(.*\S)\s*$", todo_lines[i])
            if m:
                found.append((i + 1, len(m.group(1)), m.group(2)))
                if len(m.group(2)) >= 25 or len(found) == 2:
                    break
        if not found:
            return 0, "(no heading)"
        if len(found) == 2 and found[1][1] < found[0][1]:
            return found[0][0], f"{found[1][2]} › {found[0][2]}"
        return found[0][0], found[0][2]

    # 1. surviving briefs
    for b in d["briefs"]:
        if b["verdict"] != "REAL":
            continue
        own = b["initiative"] == "todo-completion"
        row_id = b["task_id"] if own else f"{b['initiative']}/{b['task_id']}"
        carried = brief_by_id.get(b["task_id"]) if own else None
        path = carried["path"] if carried else b["path"]
        row = {
            "kind": "brief",
            "id": row_id,
            "label": _label_for("brief", row_id),
            "title": b["title"][:110],
            "risk": b.get("risk") or "hygiene",
            "severity": None,
            "effort": b.get("effort") or "M",
            "count": 1,
            "anchor": path,
            "url": _blob(path),
            "brief": carried["id"] if carried else "",
        }
        gate = next((v for k, v in GATED.items() if b["path"].startswith(k)), None)
        if not gate and carried and carried["dispatch"] != "DISPATCH":
            gate = f"{carried['dispatch']} — {carried['dispatch_why'][:120]}"
        if not gate:
            dfit = design.get(row_id)
            if dfit and dfit["verdict"] in ("DEFER", "SUPERSEDED"):
                gate = f"{dfit['verdict']} — {dfit['why'][:120]}"
        row["gate"] = gate
        if own and b["task_id"] in done_briefs:
            done.append({**row, "pr": done_briefs[b["task_id"]]})
        else:
            rows.append(row)

    # 2. audit findings
    for f in d["findings"]:
        fb = brief_by_source.get(f["id"]) or {}
        row = {
            "kind": "finding",
            "id": f["id"],
            "label": _label_for("finding", f["id"]),
            "title": f["title"][:110],
            "risk": f.get("risk") or "correctness",
            "severity": f.get("severity"),
            "effort": f.get("effort") or "M",
            "count": 1,
            "anchor": f"{f.get('file')}:{f.get('line')}",
            "url": _blob(f.get("file"), f.get("line")),
            "gate": f"{fb['dispatch']} — {fb['dispatch_why'][:120]}" if fb and fb["dispatch"] != "DISPATCH" else None,
            "brief": fb.get("id", ""),
        }
        if f["id"] in done_findings:
            done.append({**row, "pr": done_findings[f["id"]]})
        else:
            rows.append(row)

    # 3. uncovered REAL TODO items, rolled up per section (open items only)
    sections = collections.defaultdict(list)
    for t in d["todo"]:
        if t["verdict"] != "REAL" or t.get("dup_of"):
            continue
        b = brief_by_todo_line.get(t["line"])
        current, checked = locator.locate(t)
        # Headings are read from the CURRENT file, so use the re-located line, not the frozen one.
        key = ("brief", b["id"], b["title"]) if b else ("heading",) + real_heading(current or t["line"])
        sections[key].append({**t, "current_line": current, "checked": checked})
    for key, items in sections.items():
        b = brief_by_id.get(key[1]) if key[0] == "brief" else None
        open_items = [t for t in items if not t["checked"]]
        worst = min(items, key=lambda t: RISK_RANK.get(t.get("risk"), 6))
        risk = b["risk"] if b else (worst.get("risk") or "hygiene")  # RECLASSIFY verdicts lower the class
        first_line = min(t["line"] for t in items)
        row_id = f"TODO.md:{first_line}"
        gate = f"{b['dispatch']} — {b['dispatch_why'][:120]}" if b and b["dispatch"] != "DISPATCH" else None
        row = {
            "kind": "todo-section",
            "id": row_id,
            "label": _label_for("todo-section", row_id),
            "title": (b["title"] if b else key[2])[:110],
            "risk": risk,
            "severity": None,
            "effort": None,
            "count": len(open_items),
            "anchor": "",
            "url": "",
            "gate": gate,
            "brief": b["id"] if b else "",
        }
        if b and b["id"] in done_briefs:
            done.append({**row, "pr": done_briefs[b["id"]], "count": len(items)})
            continue
        if not open_items:
            done.append({**row, "pr": "TODO.md checked off", "count": len(items)})
            continue
        cheapest = min(open_items, key=lambda t: EFF_RANK.get(t.get("effort"), 3))
        ordered = sorted(open_items, key=lambda t: t["line"])
        shown = [str(t["current_line"] or t["line"]) for t in ordered[:6]]
        first_open = next((t["current_line"] for t in ordered if t["current_line"]), None)
        row["effort"] = cheapest.get("effort") or "M"
        row["anchor"] = "lines " + ",".join(shown) + (" …" if len(ordered) > 6 else "")
        row["url"] = _blob("TODO.md", first_open, plain=True) if first_open else _blob("TODO.md")
        rows.append(row)

    return rows, done


def risk_key(r):
    return (RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["severity"], 4), EFF_RANK.get(r["effort"], 3), r["id"])


def effort_key(r):
    return (EFF_RANK.get(r["effort"], 3), RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["severity"], 4), r["id"])


def large_impact(r):
    """Impact split used by the quadrant: data-loss, security and correctness are large."""
    return RISK_RANK.get(r["risk"], 6) <= 2


def easy(r):
    """Effort split used by the quadrant: only S is easy; M and L sit on the hard side."""
    return r["effort"] == "S"


def spread(points, x0, x1, y0, y1):
    """Lay `points` out on a grid inside the rectangle so labels do not stack on one dot.

    Points are placed row-major in the order given (callers sort worst-first so the most
    severe rows sit at the top of their band). Returns a list of (x, y) in the same order.
    """
    n = len(points)
    if n == 0:
        return []
    width, height = x1 - x0, y1 - y0
    cols = max(1, math.ceil(math.sqrt(n * (width / height))))
    rows_n = math.ceil(n / cols)
    out = []
    for i in range(n):
        c, r = i % cols, i // cols
        x = x0 + (c + 0.5) * width / cols
        y = y1 - (r + 0.5) * height / rows_n
        out.append((round(x, 3), round(y, 3)))
    return out
