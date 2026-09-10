#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/apply_todo_checkoffs.py
# version: 1.2.0
# guid: e4b8c1a3-6f2d-4d97-8a05-3c7e9b1f5d62
# last-edited: 2026-09-10
"""Check off TODO.md items the reconciliation proved DONE or STALE.

Usage: apply_todo_checkoffs.py <state-dir> <TODO.md> [--dry-run]
Only the checkbox line changes: `- [ ]` → `- [x]` plus a trailing evidence note.
Refuses to touch a line whose text no longer matches the verdict's `text_head`
(the file drifted since the inventory) and reports it instead. Idempotent.
"""
import json
import os
import sys

STATE, TODO = sys.argv[1], sys.argv[2]
DRY = "--dry-run" in sys.argv
DATE = "2026-09-10"
d = json.load(open(os.path.join(STATE, "merged.json"), encoding="utf-8"))
lines = open(TODO, encoding="utf-8").read().split("\n")

changed, skipped, already = [], [], []
for t in d["todo"]:
    if t["verdict"] not in ("DONE", "STALE"):
        continue
    i = int(t["line"]) - 1
    cur = lines[i]
    head = (t.get("text_head") or "").strip()
    if "[x]" in cur and DATE in cur:
        already.append(t["line"])
        continue
    def strip_box(x):
        x = x.strip()
        for pre in ("- [ ]", "- [x]"):
            if x.startswith(pre):
                x = x[len(pre):].strip()
        return x
    body, head = strip_box(cur), strip_box(head)
    if "- [ ]" not in cur or not body or not body.startswith(head[:40]):
        skipped.append((t["line"], cur[:80], head[:80]))
        continue
    ev = (t.get("evidence") or "").replace("\n", " ").strip()
    ev = ev if len(ev) <= 220 else ev[:219] + "…"
    tag = "✅ DONE" if t["verdict"] == "DONE" else "⏩ STALE"
    lines[i] = cur.replace("- [ ]", "- [x]", 1) + f" — {tag} {DATE}: {ev}"
    changed.append(t["line"])

if not DRY:
    with open(TODO, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines))
print(json.dumps({"changed": len(changed), "already": len(already), "skipped": len(skipped), "dry_run": DRY,
                  "skipped_detail": skipped[:20]}, indent=1, ensure_ascii=False))
