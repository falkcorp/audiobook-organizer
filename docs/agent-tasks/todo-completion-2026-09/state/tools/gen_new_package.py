#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/gen_new_package.py
# version: 1.0.0
# guid: 7f0a3c6e-5d2b-4e81-a9c4-1b8d6f2e0a57
# last-edited: 2026-09-10
"""Generate the 2026-09 todo-completion package from state/merged.json.

Usage: gen_new_package.py <state-dir> <old-package-dir> <new-package-dir> <repo-root>

Produces, under <new-package-dir>:
  BREAKDOWN-2026-09-10.md, ORCHESTRATION.md, <ws>/README.md, <ws>/orchestration.md,
  <ws>/TASK-NNN-*.md  (carried-forward REAL briefs keep their ids; new briefs start at 300)
and under <repo-root>/todo.d/: one headerless fragment per NEW brief.
Re-runnable: regenerated files are overwritten; nothing outside those paths is touched.
"""
import collections
import json
import os
import re
import sys
import uuid

STATE, OLD, NEW, REPO = sys.argv[1:5]
DATE = "2026-09-10"
DATE_COMPACT = "20260910"
PKG = os.path.relpath(NEW, REPO)  # docs/agent-tasks/todo-completion-2026-09
d = json.load(open(os.path.join(STATE, "merged.json"), encoding="utf-8"))

RISK_RANK = {"data-loss": 0, "security": 1, "correctness": 2, "perf": 3, "ux": 4, "hygiene": 5}
SEV_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3}
GATED = {
    "docs/agent-tasks/torrent-relocation/": "parked (DECISIONS-PENDING row 2, PR #2715)",
    "docs/agent-tasks/ai-responses-migration/": "on hold (two-arm useResponsesAPI design)",
}
WS_BY_PATH = [
    ("internal/database", "database"), ("internal/operations", "operations"),
    ("internal/dedup", "dedup"), ("internal/merge", "dedup"), ("internal/metafetch", "metadata"),
    ("internal/metadata", "metadata"), ("internal/scanner", "scanner"), ("internal/organizer", "organize"),
    ("internal/server", "server-handlers"), ("internal/realtime", "server-handlers"),
    ("internal/activity", "activity"), ("internal/itunes", "itunes"), ("internal/search", "search"),
    ("internal/plugins/maintenance", "maintenance"), ("internal/maintenance", "maintenance"),
    ("internal/plugins/dedup", "dedup"), ("web/", "web"), (".github", "ci-tooling"),
    ("scripts/", "ci-tooling"), ("Makefile", "ci-tooling"), ("Dockerfile", "ci-tooling"),
    ("internal/config", "config"), ("cmd/", "misc-go"), ("internal/", "misc-go"),
]
GATE_BY_WS = {
    "web": "cd web && npm ci && npm run build && npm test -- --run",
    "ci-tooling": "actionlint .github/workflows/*.yml 2>/dev/null || true; python3 -m py_compile scripts/*.py; go build ./...",
}


def ws_for(path):
    for prefix, ws in WS_BY_PATH:
        if (path or "").startswith(prefix) or f"/{prefix}" in (path or ""):
            return ws
    return "misc-go"


def slug(s, n=48):
    s = re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")
    return s[:n].rstrip("-")


def header(path, version="1.0.0"):
    return f"<!-- file: {path} -->\n<!-- version: {version} -->\n<!-- guid: {uuid.uuid4()} -->\n<!-- last-edited: {DATE} -->\n\n"


def go_pkg_gate(path):
    m = re.match(r"(internal/[^/]+(?:/[^/]+)?)/", path or "")
    pkg = m.group(1) if m else "internal"
    return f"go build ./... && go vet ./... && go test ./{pkg}/... -count=1"


def priority(risk, sev):
    if sev == "critical" or (risk in ("data-loss", "security") and sev in (None, "high")):
        return "P0" if sev == "critical" else "P1"
    return {"high": "P1", "medium": "P2", "low": "P3"}.get(sev, "P2" if risk in ("correctness", "perf") else "P3")


def tier(effort, risk):
    if risk in ("data-loss", "security") or effort == "L":
        return "Opus-class"
    return "Sonnet-class" if effort == "M" else "Haiku-class"


written = []
tasks = []  # rows for BREAKDOWN tables


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    written.append(os.path.relpath(path, REPO))


# ---------------------------------------------------------------------------
# 1. Carry forward surviving briefs from the todo-completion package
# ---------------------------------------------------------------------------
carried = [b for b in d["briefs"] if b["verdict"] == "REAL" and b["initiative"] == "todo-completion"]
for b in carried:
    old_rel = b["path"]  # docs/agent-tasks/todo-completion/<ws>/TASK-...
    parts = old_rel.split("/")
    ws, fname = parts[3], parts[4]
    src = os.path.join(OLD, ws, fname)
    if not os.path.exists(src):
        print(f"WARN missing carried brief {src}", file=sys.stderr)
        continue
    text = open(src, encoding="utf-8").read()
    new_rel = f"{PKG}/{ws}/{fname}"
    text = re.sub(r"<!-- file: .*? -->", f"<!-- file: {new_rel} -->", text, count=1)
    m = re.search(r"<!-- version: (\d+)\.(\d+)\.(\d+) -->", text)
    if m:
        text = text.replace(m.group(0), f"<!-- version: {m.group(1)}.{int(m.group(2)) + 1}.0 -->", 1)
    text = re.sub(r"<!-- last-edited: .*? -->", f"<!-- last-edited: {DATE} -->", text, count=1)
    status = (f"> **Status {DATE}:** 🟡 REAL — re-verified at HEAD 42d187168: {b.get('evidence', '').strip()} "
              f"· risk **{b.get('risk', 'hygiene')}** · effort **{b.get('effort', 'M')}**"
              + (" · **(verdict changed since 09-02)**" if b.get("changed_since_0902") else ""))
    text = re.sub(r"^(# TASK-\d+ — .*?\n)", lambda mm: mm.group(1) + "\n" + status + "\n", text, count=1, flags=re.M)
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": b["task_id"], "title": b["title"], "kind": "carried", "risk": b.get("risk", "hygiene"),
                  "effort": b.get("effort", "M"), "sev": None, "evidence": b.get("evidence", ""), "path": new_rel,
                  "gate": None, "prio": priority(b.get("risk"), "high" if b.get("risk") in ("data-loss", "security") else "medium")})

# ---------------------------------------------------------------------------
# 2. New briefs from Wave 3 findings (TASK-300+)
# ---------------------------------------------------------------------------
NEXT = 300
findings = sorted(d["findings"], key=lambda f: (RISK_RANK.get(f.get("risk"), 6), SEV_RANK.get(f.get("severity"), 4), f["id"]))
fragments = []


def brief_body(tid, title, ws, prio, effort, tr, src_line, goal, background, anchors, steps, tests, gate, review_critical, tag):
    branch = f"agent/{ws}-{tid[5:]}-{slug(title, 40)}"
    frag = f"changelog.d/{DATE_COMPACT}_{ws.replace('-', '_')}_{tid[5:]}.md"
    rc = ("· **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**" if review_critical else "")
    anchors_md = "\n".join(f"  {a}" for a in anchors)
    steps_md = "\n".join(f"{i}. {s}" for i, s in enumerate(steps, 1))
    tests_md = "\n".join(f"- {t}" for t in tests)
    return f"""# {tid} — {title} ({tag})

> **Status {DATE}:** 🆕 NEW — {src_line}

**Priority:** {prio} · **Effort:** {effort} · **Recommended subagent:** {tr} · {ws} subagent · **Depends on:** none · **Wave:** 1 {rc}

Source: {src_line}. Verified at HEAD `42d187168` on {DATE}; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/{ws}-{tid[5:]}" -b {branch} origin/main
cd "$REPO/.worktrees/{ws}-{tid[5:]}"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

{goal}

## Background (verify before editing)

{background}

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
{anchors_md}
  ```

## Step-by-step

{steps_md}

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: {DATE}`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `{frag}` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

{tests_md}

## How to test

```bash
{gate}
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: {DATE}" <file>` hits for each).
- [ ] Changelog fragment present: `test -f {frag}`.

## Commit message

```
fix({ws}): {title[:70]} ({tag})

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

{"**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing." if review_critical else "Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing."}

## Coordinator notes

{"review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner." if review_critical else "Standard lane: coordinator may admin-merge on a green gate."}
"""


for f in findings:
    tid = f"TASK-{NEXT}"
    NEXT += 1
    ws = ws_for(f.get("file"))
    risk, sev, effort = f.get("risk", "correctness"), f.get("severity"), f.get("effort", "M")
    prio = priority(risk, sev)
    title = f["title"].strip().rstrip(".")
    fname = f"{tid}-{slug(title)}.md"
    path = f"{PKG}/{ws}/{fname}"
    file_, line = f.get("file", ""), f.get("line")
    review_critical = risk in ("data-loss", "security")
    anchors = [f"test -f {file_}   # the file the finding is anchored to still exists"]
    if line:
        lo, hi = max(1, int(line) - 6), int(line) + 6
        anchors.append(f"sed -n '{lo},{hi}p' {file_}   # expect the code described under Background (drifted lines: re-find by the quoted text)")
    gate = GATE_BY_WS.get(ws) or go_pkg_gate(file_)
    goal = f"{f.get('suggested_fix', '').strip()}\n\nWhy it matters: {f.get('why_it_matters', '').strip()}"
    background = f"- {f.get('evidence', '').strip()}\n- Anchor: `{file_}:{line}` (audit `{f['id']}`, confidence {f.get('confidence', 'medium')}, severity {sev})."
    if f.get("already_tracked_hint"):
        background += f"\n- Related tracking: {f['already_tracked_hint']}"
    steps = [
        "Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).",
        f"Implement the fix described under Goal in `{file_}` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).",
        "Write the regression test first (it must fail against the pre-fix code), then make it pass.",
        "Run the gate; add the changelog fragment; bump headers; report with exact counts.",
    ]
    tests = [
        "A regression test that reproduces the defect described in Background and fails on the pre-fix code.",
        "Existing package tests stay green (`-count=1`).",
    ]
    if review_critical:
        tests.append("A test proving the dry-run / guard path writes nothing (fail-closed on error).")
    text = header(path) + brief_body(tid, title, ws, prio, effort, tier(effort, risk), f"Wave 3 audit finding `{f['id']}` ({f['source_file']})",
                                     goal, background, anchors, steps, tests, gate, review_critical, f["id"])
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": tid, "title": title, "kind": "new-finding", "risk": risk, "effort": effort, "sev": sev,
                  "evidence": f"{file_}:{line}", "path": path, "gate": None, "prio": prio})
    fragments.append((f"todo.d/{DATE}-{slug(title, 40)}-{f['id'].lower()}.md",
                      f"- [ ] **{f['id']}** {title} — `{file_}:{line}`. {f.get('why_it_matters', '').strip()[:300]} Brief: `{path}`.\n"))

# ---------------------------------------------------------------------------
# 3. New briefs for uncovered data-loss / security TODO.md sections
# ---------------------------------------------------------------------------
sections = collections.OrderedDict()
for t in d["todo"]:
    if t["verdict"] == "REAL" and not t.get("dup_of") and t.get("risk") in ("data-loss", "security"):
        sections.setdefault(t["section"], []).append(t)
for sec, items in sections.items():
    tid = f"TASK-{NEXT}"
    NEXT += 1
    paths = [m.group(0) for t in items for m in [re.search(r"(internal|web|scripts|cmd|\.github)/[\w./-]+", t.get("evidence") or "")] if m]
    ws = ws_for(paths[0]) if paths else "misc-go"
    risk = min((t.get("risk") for t in items), key=lambda r: RISK_RANK.get(r, 6))
    effort = min((t.get("effort") or "M" for t in items), key=lambda e: {"S": 0, "M": 1, "L": 2}.get(e, 1))
    title = re.sub(r"\s*\(\d{4}-\d{2}-\d{2}\)\s*$", "", sec).strip().rstrip(".")[:100]
    fname = f"{tid}-{slug(title)}.md"
    path = f"{PKG}/{ws}/{fname}"
    lines = ", ".join(str(t["line"]) for t in items)
    review_critical = True
    anchors = [f"grep -n -F \"{items[0]['text_head'][6:60].replace(chr(34), '')}\" TODO.md   # the source item still exists (line numbers drift)"]
    for p in sorted(set(paths))[:4]:
        anchors.append(f"test -e {p.split(':')[0]}   # anchor file from the reconciliation evidence")
    goal = (f"Close the {len(items)} still-open `TODO.md` item(s) under section “{sec}” (lines {lines} as of HEAD 42d187168). "
            f"Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.")
    background = "\n".join(f"- L{t['line']} — {t['text_head']} — evidence: {t.get('evidence', '')}" for t in items)
    steps = [
        "Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.",
        "Re-run the anchors; for each item confirm the gap at HEAD or report it closed.",
        "Implement item by item, smallest first; one commit per item where they are separable.",
        "Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.",
    ]
    tests = ["One regression test per item that fails on the pre-fix code.", "Existing package tests stay green (`-count=1`).",
             "A test proving any new guard/repair path is fail-closed and dry-run by default."]
    gate = GATE_BY_WS.get(ws) or go_pkg_gate(paths[0] if paths else "internal/")
    text = header(path) + brief_body(tid, title, ws, priority(risk, "high"), effort, tier(effort, risk), f"`TODO.md` section “{sec}”, lines {lines}",
                                     goal, background, anchors, steps, tests, gate, review_critical, f"TODO.md:{items[0]['line']}")
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": tid, "title": title, "kind": "new-todo", "risk": risk, "effort": effort, "sev": None,
                  "evidence": f"TODO.md lines {lines}", "path": path, "gate": None, "prio": priority(risk, "high")})

# ---------------------------------------------------------------------------
# 4. Per-workstream README + orchestration
# ---------------------------------------------------------------------------
by_ws = collections.OrderedDict()
for t in sorted(tasks, key=lambda t: (t["ws"], t["id"])):
    by_ws.setdefault(t["ws"], []).append(t)

for ws, rows in by_ws.items():
    rel = f"{PKG}/{ws}/README.md"
    tbl = ["| Task | Kind | Risk | Priority | Effort | Title | Evidence |", "|---|---|---|---|---|---|---|"]
    for r in rows:
        tbl.append(f"| [{r['id']}]({os.path.basename(r['path'])}) | {r['kind']} | {r['risk']} | {r['prio']} | {r['effort']} | {r['title'][:80]} | {r['evidence'][:90].replace('|', '/')} |")
    write(os.path.join(NEW, ws, "README.md"), header(rel) + f"""# Workstream — {ws} (todo-completion-2026-09)

{len(rows)} tasks: {sum(1 for r in rows if r['kind'] == 'carried')} carried forward from the 2026-08-21 package (ids kept), {sum(1 for r in rows if r['kind'] != 'carried')} new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

{chr(10).join(tbl)}

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
""")
    waves = {"S": [], "M": [], "L": []}
    for r in rows:
        waves.setdefault(r["effort"], waves["M"]).append(r)
    mer = ["```mermaid", "flowchart LR"]
    for i, (eff, rs) in enumerate(((k, v) for k, v in waves.items() if v), 1):
        mer.append(f"    subgraph Wave{i}_{eff}")
        for r in rs:
            mer.append(f"      {r['id'].replace('-', '')}[{r['id']} {slug(r['title'], 28)}]")
        mer.append("    end")
    mer.append("```")
    write(os.path.join(NEW, ws, "orchestration.md"), header(f"{PKG}/{ws}/orchestration.md") + f"""# Orchestration — {ws} workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are by effort (S → M → L) because the new briefs carry no cross-task `Depends on:`; carried-forward briefs keep their original `Depends on:` line — honor it over this grouping. **Same-file rule:** two briefs that name the same file in their anchors never run in the same wave.

{chr(10).join(mer)}
""")

# ---------------------------------------------------------------------------
# 5. BREAKDOWN + ORCHESTRATION
# ---------------------------------------------------------------------------
S = d["summary"]
n_car = sum(1 for t in tasks if t["kind"] == "carried")
n_fnd = sum(1 for t in tasks if t["kind"] == "new-finding")
n_td = sum(1 for t in tasks if t["kind"] == "new-todo")
gated = [b for b in d["briefs"] if b["verdict"] == "REAL" and any(b["path"].startswith(k) for k in GATED)]
sib_real = [b for b in d["briefs"] if b["verdict"] == "REAL" and b["initiative"] != "todo-completion" and b not in gated]

ws_tables = []
for ws, rows in by_ws.items():
    c = collections.Counter(r["kind"] for r in rows)
    ws_tables.append(f"\n### WS — {ws} · {len(rows)} tasks — carried {c.get('carried', 0)}, new {c.get('new-finding', 0) + c.get('new-todo', 0)}\n")
    ws_tables.append("| Task | Kind | Risk | Sev | Priority | Effort | Title | Evidence |\n|---|---|---|---|---|---|---|---|")
    for r in sorted(rows, key=lambda r: (RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["sev"], 4), r["id"])):
        ws_tables.append(f"| [{r['id']}]({ws}/{os.path.basename(r['path'])}) | {r['kind']} | {r['risk']} | {r['sev'] or '—'} | {r['prio']} | {r['effort']} | {r['title'][:90]} | {r['evidence'][:100].replace('|', '/')} |")

breakdown = header(f"{PKG}/BREAKDOWN-{DATE}.md") + f"""# Agent-Task Breakdown — {DATE} (todo-completion-2026-09)

This package replaces [`../../archive/agent-tasks/todo-completion-2026-08-21/BREAKDOWN-2026-08-21.md`](../../archive/agent-tasks/todo-completion-2026-08-21/BREAKDOWN-2026-08-21.md) (dormant since 2026-08-23, last reconciled 2026-09-02). Every table here is projected from [`state/merged.json`](state/merged.json) by [`state/tools/gen_new_package.py`](state/tools/gen_new_package.py); regenerate, never hand-edit. Companion docs: [`RECONCILIATION-{DATE}.md`](RECONCILIATION-{DATE}.md) (every DONE/STALE/REAL verdict with evidence) and [`PRIORITY-MATRIX.md`](PRIORITY-MATRIX.md) (risk-ordered × effort-ordered, pick a cut line).

## What was reconciled

Baseline HEAD `42d187168` (== `origin/main`), 399 commits / 138 merged PRs after the 2026-09-02 reconciliation.

| Input | Total | ✅ DONE | ⏩ STALE | 🟡 REAL | ❓ UNCLEAR |
|---|---|---|---|---|---|
| Briefs (`docs/agent-tasks/**/TASK-*.md`) | {S['briefs_total']} | {S['briefs_by_verdict'].get('DONE', 0)} | {S['briefs_by_verdict'].get('STALE', 0)} | {S['briefs_by_verdict'].get('REAL', 0)} | {S['briefs_by_verdict'].get('UNCLEAR', 0)} |
| `TODO.md` unchecked items | {S['todo_total']} | {S['todo_by_verdict'].get('DONE', 0)} | {S['todo_by_verdict'].get('STALE', 0)} | {S['todo_by_verdict'].get('REAL', 0)} | {S['todo_by_verdict'].get('UNCLEAR', 0)} |
| Wave 3 read-only audit findings (untracked work) | {S['findings_total']} | — | — | {S['findings_total']} | — |

Evidence rule (unchanged from 09-02): `DONE` names the merged PR/commit **and** a code check; `REAL` names the file:line at HEAD that still shows the gap; `STALE` names what replaced it. A doc claim is a hypothesis; the code is the fact.

## What this package contains — {len(tasks)} briefs

- **{n_car} carried forward** from the 2026-08-21 package: every `todo-completion` brief still REAL at HEAD, same id, same body, with a new `> **Status {DATE}:**` line under the title. 9 verdicts changed since 09-02 (see RECONCILIATION).
- **{n_fnd} new from the Wave 3 audit** (TASK-300+): database/operations, silent failures in metafetch/scanner/organize, schema/queries, dedup/activity, server/handlers, web, CI. Risk-ordered ids.
- **{n_td} new from `TODO.md`**: one brief per section whose still-open items are data-loss or security class and had no brief. The remaining {S['todo_by_verdict'].get('REAL', 0) - S['todo_dup_of_brief'] - sum(len(v) for v in sections.values())} uncovered REAL items (correctness/perf/ux/hygiene) stay tracked in `TODO.md` and appear as `todo-section` rows in the matrix — brief them on demand when the cut line reaches them.

Not in this package, deliberately: {len(gated)} REAL sibling briefs that are **owner-gated** ({", ".join(sorted(set(b['path'].split('/')[2] for b in gated)))}) and {len(sib_real)} REAL briefs that live in still-active sibling initiatives ({", ".join(sorted(set(b['path'].split('/')[2] for b in sib_real)))}) — those packages keep their own READMEs; their status rows are in RECONCILIATION §1.

## Per-workstream briefs
{chr(10).join(ws_tables)}

## Same-file collision rule (drives wave ordering — GLOBAL across workstreams)

Two briefs whose anchors name the same file never run in the same wave. New briefs list their anchor file under **Background**; carried briefs under **Reuse / exact_files**. The per-workstream `orchestration.md` groups by effort only — the coordinator applies this rule on top before dispatch.

## Coordinator protocol

See [`ORCHESTRATION.md`](ORCHESTRATION.md) (verbatim from the 2026-08-21 package: coordinator owns git, per-merge sibling rebase, conflict ladder, held review-critical PRs).

## TODO.md check-offs made in this PR

{S['todo_by_verdict'].get('DONE', 0)} items checked `[x]` with a `✅ DONE {DATE}` note and {S['todo_by_verdict'].get('STALE', 0)} with a `⏩ STALE {DATE}` note, each carrying the evidence from RECONCILIATION §2. Applied by `state/tools/apply_todo_checkoffs.py`.
"""
write(os.path.join(NEW, f"BREAKDOWN-{DATE}.md"), breakdown)

old_orch = open(os.path.join(REPO, "docs/agent-tasks/ORCHESTRATION.md"), encoding="utf-8").read()
write(os.path.join(NEW, "ORCHESTRATION.md"), header(f"{PKG}/ORCHESTRATION.md") + f"""# Orchestration — todo-completion-2026-09

The package-level protocol is the parent [`../ORCHESTRATION.md`](../ORCHESTRATION.md) (coordinator owns git; workers never push; per-merge sibling rebase; conflict ladder; held review-critical PRs). This file adds only what is specific to this package.

## Dispatch order

1. Pick the cut line in [`PRIORITY-MATRIX.md`](PRIORITY-MATRIX.md) §A (risk) — the owner decides; default is every row with risk `data-loss` or `security` plus every `critical`/`high` finding.
2. Within the cut, dispatch by workstream `orchestration.md` waves (S → M → L), applying the same-file collision rule from the BREAKDOWN.
3. Hard cap: **4 concurrent worker agents**. Workers must not spawn sub-agents (three agents did so during the 2026-09-10 reconciliation and raced on shared output files).
4. Review-critical briefs (data-loss / security, marked in the brief) are held for the owner — never admin-merged.
5. After each merge: rebase every open sibling worktree; re-run the matrix generator if a brief's status changed (`state/tools/merge_verdicts.py` → `build_matrix.py`).

## State

`state/` holds the raw agent outputs (`wave1/`, `wave3/`), the merged ledger (`merged.json`), each agent's final report (`RAW-RESULTS.md`) and the generators (`tools/`). Nothing there is hand-edited.
""")

# ---------------------------------------------------------------------------
# 6. todo.d fragments (headerless) for NEW finding briefs
# ---------------------------------------------------------------------------
for rel, body in fragments:
    write(os.path.join(REPO, rel), body)

print(json.dumps({"carried": n_car, "new_findings": n_fnd, "new_todo_sections": n_td, "total_briefs": len(tasks),
                  "workstreams": {k: len(v) for k, v in by_ws.items()}, "fragments": len(fragments),
                  "files_written": len(written)}, indent=1))
