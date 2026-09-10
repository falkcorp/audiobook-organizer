#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/merge_verdicts.py
# version: 1.0.0
# guid: c2a7e5d1-9b3f-4e60-8d14-2f6a7c0b9e35
# last-edited: 2026-09-10
"""Merge the Wave 1/2/3 agent outputs into one reconciliation ledger.

Usage: merge_verdicts.py <state-dir>   (the directory holding wave1/ and wave3/)
Writes <state-dir>/merged.json and prints a summary. Re-runnable; idempotent.
"""
import collections
import glob
import json
import os
import re
import sys

STATE = sys.argv[1]
W1 = os.path.join(STATE, "wave1")
W3 = os.path.join(STATE, "wave3")


def load(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def norm_verdict(v):
    v = (v or "").upper()
    if v in ("UNKNOWN", "UNCLEAR"):
        return "UNCLEAR"
    return v


# ---- briefs -----------------------------------------------------------------
briefs = []
for part in "ABC":
    for row in load(os.path.join(W1, f"briefs_verdicts_{part}.json")):
        row["verdict"] = norm_verdict(row.get("verdict"))
        row["path"] = re.sub(r"^.*?/docs/agent-tasks/", "docs/agent-tasks/", row["path"])
        row.setdefault("initiative", row["path"].split("/")[2])
        row["source_part"] = part
        briefs.append(row)

# ---- TODO items -------------------------------------------------------------
todo = []
present_chunks = []
for n in (1, 2, 3, 4):
    p = os.path.join(W1, f"todo_verdicts_{n}.json")
    if not os.path.exists(p):
        continue
    present_chunks.append(n)
    for row in load(p):
        row["verdict"] = norm_verdict(row.get("verdict"))
        row["chunk"] = n
        todo.append(row)

# dedupe by line (a chunk file rewritten by forks may repeat rows)
by_line = {}
for row in todo:
    by_line.setdefault(row["line"], row)
todo = [by_line[k] for k in sorted(by_line)]

expected = {int(r["line"]) for r in load(os.path.join(W1, "todo_open_items.json"))}
covered = set(by_line)
missing_lines = sorted(expected - covered)

# ---- brief <-> TODO cross-links --------------------------------------------
brief_by_id = {b["task_id"]: b for b in briefs if b["initiative"] == "todo-completion"}
for row in todo:
    d = row.get("dup_of")
    if d and d in brief_by_id:
        row["dup_brief_verdict"] = brief_by_id[d]["verdict"]

# ---- Wave 3 audit findings -------------------------------------------------
findings = []
for p in sorted(glob.glob(os.path.join(W3, "*.json"))):
    data = load(p)
    rows = data.get("findings", data) if isinstance(data, dict) else data
    for f in rows:
        f["source_file"] = os.path.basename(p)
        findings.append(f)

# ---- summary ----------------------------------------------------------------
def counter(rows, key):
    return dict(collections.Counter((r.get(key) or "null") for r in rows))


summary = {
    "briefs_total": len(briefs),
    "briefs_by_verdict": counter(briefs, "verdict"),
    "briefs_real_by_risk": counter([b for b in briefs if b["verdict"] == "REAL"], "risk"),
    "briefs_real_by_effort": counter([b for b in briefs if b["verdict"] == "REAL"], "effort"),
    "todo_chunks_present": present_chunks,
    "todo_total": len(todo),
    "todo_expected": len(expected),
    "todo_missing_lines": len(missing_lines),
    "todo_by_verdict": counter(todo, "verdict"),
    "todo_real_by_risk": counter([t for t in todo if t["verdict"] == "REAL"], "risk"),
    "todo_real_by_effort": counter([t for t in todo if t["verdict"] == "REAL"], "effort"),
    "todo_dup_of_brief": sum(1 for t in todo if t.get("dup_of")),
    "findings_total": len(findings),
    "findings_by_severity": counter(findings, "severity"),
    "findings_by_risk": counter(findings, "risk"),
}

out = {
    "summary": summary,
    "briefs": briefs,
    "todo": todo,
    "todo_missing_lines": missing_lines,
    "findings": findings,
}
with open(os.path.join(STATE, "merged.json"), "w", encoding="utf-8") as fh:
    json.dump(out, fh, indent=1, ensure_ascii=False)

print(json.dumps(summary, indent=1))
