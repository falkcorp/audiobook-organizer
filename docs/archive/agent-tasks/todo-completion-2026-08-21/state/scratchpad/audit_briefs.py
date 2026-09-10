#!/usr/bin/env python3
"""Mechanical plan audit: run every re-verify grep in every brief at HEAD; lint headers/sections.
Usage: audit_briefs.py <package-root docs/agent-tasks/todo-completion> <repo-root> [--json out]
"""
import sys, os, re, glob, subprocess, json, shlex
PKG, REPO = sys.argv[1], sys.argv[2]
out_json = sys.argv[4] if len(sys.argv) > 4 else None
REQ = ["## ⛔ START HERE", "## Goal", "## Background", "## Step-by-step", "## How to test", "## Acceptance criteria", "## Commit message", "## Idempotency / Rollback"]
results = []
for path in sorted(glob.glob(os.path.join(PKG, "*", "TASK-*.md"))):
    text = open(path).read()
    rel = os.path.relpath(path, os.path.join(PKG, "..", "..", ".."))
    fails = []
    # header lint
    m = re.match(r"<!-- file: (.+?) -->\n<!-- version: (\S+) -->\n<!-- guid: ([0-9a-f-]{36}) -->\n<!-- last-edited: (\d{4}-\d{2}-\d{2}) -->", text)
    if not m: fails.append("header: malformed")
    elif m.group(1) != rel: fails.append(f"header: file path {m.group(1)} != {rel}")
    for s in REQ:
        if s not in text: fails.append(f"section missing: {s}")
    if "Anti-over-suppression" not in text: fails.append("no anti-over-suppression line")
    if "Co-Authored-By: Claude" not in text: fails.append("commit trailer missing")
    if "GATE UNDETECTED" in text: fails.append("gate undetected")
    # greps: lines inside the re-verify block and reuse lines
    blk = re.search(r"Re-verify these anchors.*?```bash\n(.*?)```", text, re.S)
    cmds = []
    if blk:
        for line in blk.group(1).splitlines():
            line = line.strip()
            if not line or line.startswith("#"): continue
            parts = line.split("   #", 1)
            exp = parts[1] if len(parts) > 1 else ""
            cmds.append((parts[0].strip(), exp))
    for mm in re.finditer(r"\(verify: `([^`]+)`\)", text):
        cmds.append((mm.group(1), ""))
    zero = []
    for c, exp in cmds:
        exp = exp.split(" — ", 1)[0]
        _neg = bool(re.search(r"\b(0|zero|no)\s*(hits?|matches?|results?)|absent|does not exist|no such file|not (yet )?(present|exist|implemented)|should (not|be empty)|expected: 0|— 0\b|exit 1", exp, re.I))
        _pos = bool(re.search(r"(^|\s)(≥|>=|[1-9]\d*|one|two|three)\s*(hits?|matches?|results?)|\bhit at\b", exp, re.I)) or bool(re.match(r"\s*(≥|>=|[1-9]\d*|one|two|three|\d+\+)\b", exp, re.I))
        expect_zero = _neg and not _pos   # a mixed expect ("hit at X; ZERO in Y") is a positive anchor: the command as a whole hits
        cc = c
        # strip trailing comment
        cc = re.sub(r"\s{2,}#.*$", "", cc)   # only the brief's "   #" comment separator, never a '#123' inside a pattern
        if not re.match(r"^(grep|rg|ls|test|sed|git|find|cat|wc|go|python3|jq|gh|awk|head|tail|\!|\[)", cc):
            continue
        try:
            r = subprocess.run(cc, shell=True, cwd=REPO, capture_output=True, text=True, timeout=60)
            hit = bool(r.stdout.strip()) or (cc.startswith(("test", "!", "[")) and r.returncode == 0)
            if cc.startswith("grep") and "-c" in cc.split() and r.stdout.strip() in ("0", ""): hit = False
            if expect_zero and hit: zero.append(f"EXPECTED-ZERO-BUT-HIT: {cc}")
            elif not expect_zero and not hit: zero.append(cc)
        except Exception as e:
            zero.append(f"{cc}  [error {e}]")
    if not cmds: fails.append("no re-verify greps at all")
    for z in zero: fails.append(f"zero-hit grep: {z}")
    results.append({"brief": rel, "greps": len(cmds), "fails": fails})
bad = [r for r in results if r["fails"]]
print(f"briefs={len(results)} failing={len(bad)} total_greps={sum(r['greps'] for r in results)}")
for r in bad:
    print("FAIL", r["brief"])
    for f in r["fails"]: print("   -", f)
if out_json: json.dump(results, open(out_json, "w"), indent=1)
