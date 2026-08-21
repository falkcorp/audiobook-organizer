#!/usr/bin/env python3
"""Apply verifier/judge `fix` patches to the scout JSON (source of truth), keyed by task_id via the current skeleton.
Usage: apply_patches.py <scratchpad>   (reads patches/verify-*.json, judges/*.json, dryrun skeleton; edits scout-all/*.json)
Idempotent: records applied (file, task_id, sha) in patches/applied.json.
"""
import json, glob, os, sys, hashlib
S = sys.argv[1]
SK = json.load(open(os.path.join(S, "dryrun/docs/agent-tasks/todo-completion/skeleton.json")))
byid = {t["id"]: t for t in SK["tasks"]}
applied_path = os.path.join(S, "patches/applied.json")
applied = set(json.load(open(applied_path))) if os.path.exists(applied_path) else set()
ALLOWED = {"goal", "background", "steps", "tests", "acceptance", "edge_cases", "anti_over_suppression", "exact_files",
           "tier", "effort", "review_critical", "polarity", "verified_anchors", "depends_on_lines", "notes", "reuse", "title"}

def find_obj(t):
    f = os.path.join(S, "scout-all", t["scope_file"])
    arr = json.load(open(f))
    for o in arr:
        if o.get("todo_line") == t.get("todo_line") and o.get("part") == t.get("part") and (o.get("title") or "")[:40] == (t.get("title") or "")[:40]:
            return f, arr, o
    return f, arr, None

stats = {"applied": 0, "skipped": 0, "unmatched": 0, "overrides": 0}
entries = []
for f in sorted(glob.glob(os.path.join(S, "patches/verify-*.json"))):
    try:
        for e in json.load(open(f)):
            for fi in e.get("findings", []):
                if fi.get("fix") or fi.get("verdict_override"):
                    entries.append((f, e["task_id"], fi))
            if e.get("verdict_override"):
                entries.append((f, e["task_id"], e))
    except Exception as ex:
        print("bad", f, ex)
for f in sorted([os.path.join(S, "judges", n + ".json") for n in ("correctness", "ops-rollback", "simplicity-scope") if os.path.exists(os.path.join(S, "judges", n + ".json"))]):
    try:
        for fi in json.load(open(f)).get("findings", []):
            if fi.get("fix") or fi.get("verdict_override"):
                for tid in fi.get("task_ids", []):
                    entries.append((f, tid, fi))
    except Exception as ex:
        print("bad", f, ex)

for src, tid, fi in entries:
    key = hashlib.sha1((src + tid + json.dumps(fi, sort_keys=True)).encode()).hexdigest()
    if key in applied:
        stats["skipped"] += 1; continue
    t = byid.get(tid)
    if not t:
        stats["unmatched"] += 1; print("no task", tid, src); continue
    path, arr, o = find_obj(t)
    if o is None:
        stats["unmatched"] += 1; print("no scout obj", tid); continue
    fix = fi.get("fix") or {}
    ov = fi.get("verdict_override") or fix.get("verdict_override")
    if ov:
        o["verdict"] = ov
        o["verdict_evidence"] = f"[{os.path.basename(src)}] " + (fi.get("reason") or fi.get("problem") or "") + " | " + (o.get("verdict_evidence") or "")
        stats["overrides"] += 1
    for k, v in fix.items():
        if k == "verdict_override": continue
        if isinstance(v, dict) and any(str(kk).startswith("TASK-") for kk in v):
            if tid not in v: continue
            v = v[tid]
        if k not in ALLOWED: continue
        if k == "exact_files":
            o[k] = sorted(set((o.get(k) or []) + list(v)))
        elif k == "verified_anchors":
            o[k] = (o.get(k) or []) + [a for a in v if a not in (o.get(k) or [])]
        else:
            o[k] = v
    o.setdefault("review_notes", []).append(f"[{os.path.basename(src)}] {fi.get('problem','')[:300]}")
    json.dump(arr, open(path, "w"), indent=1)
    applied.add(key); stats["applied"] += 1
json.dump(sorted(applied), open(applied_path, "w"))
print(stats)
