#!/usr/bin/env python3
# file: docs/agent-tasks/todo-completion-2026-09/state/tools/gen_new_package.py
# version: 1.1.0
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


def load_final(name, key):
    """state/final/<name>.json — outputs of the 2026-09-10 final-analysis auditors; optional."""
    p = os.path.join(STATE, "final", name)
    if not os.path.exists(p):
        return {}
    return {r[key]: r for r in json.load(open(p, encoding="utf-8"))}


ADVERSARIAL = load_final("adversarial_top11.json", "id")          # finding id -> verdict row
SECTION_CHECK = load_final("todo_sections_validation.json", "brief")  # validation rows (keyed by the 09-10 brief id)
SECTION_BY_LINE = {ln: r for r in SECTION_CHECK.values() for ln in r.get("todo_lines", [])}  # TODO line -> row
# Per-line overrides: the validator judged some MULTI-item rows as a whole but named individual
# items inside them that must not be dispatched (or are mis-classed). Once bold-named items are
# split into their own briefs those lines carry their own verdict.
for r in load_final("todo_line_overrides.json", "line").values():
    base = dict(SECTION_BY_LINE.get(r["line"], {}))
    base.update(r)
    SECTION_BY_LINE[r["line"]] = base
DRIFT = load_final("carried_anchor_drift.json", "task_id")          # carried brief id -> stale re-verify anchor
DISPATCH_RANK = {"HOLD-FOR-OWNER": 0, "DROP": 1, "RECLASSIFY": 2, "DISPATCH": 3}
TODO_LINES = open(os.path.join(REPO, "TODO.md"), encoding="utf-8").read().split("\n")


def real_heading(line):
    """(heading_line_no, heading_text) of the nearest Markdown heading ABOVE `line` in TODO.md.

    The wave-1 inventory tracked only `## ` headings, so ~50 items were filed under the wrong
    section and 11 of 30 TODO-section briefs grepped the wrong paragraph (plan-auditor,
    2026-09-10). The document itself is the authority: walk up to the enclosing heading."""
    found = []
    for i in range(int(line) - 2, -1, -1):
        m = re.match(r"^(#{1,6})\s+(.*\S)\s*$", TODO_LINES[i])
        if not m:
            continue
        lvl, txt = len(m.group(1)), m.group(2)
        if not found:
            found.append((i + 1, lvl, txt))
            if len(txt) >= 25:
                break
            continue
        # a terse sub-heading ("### Fix", "### Decision needed") needs its PARENT for context:
        # the next heading of a strictly higher level
        if lvl < found[0][1]:
            found.append((i + 1, lvl, txt))
            break
    if not found:
        return 0, "(no heading)"
    if len(found) == 2:
        return found[0][0], f"{found[1][2]} › {found[0][2]}"
    return found[0][0], found[0][2]


def todo_item_text(line, max_lines=4):
    """The full item text at `line`: the checkbox line plus its indented continuation lines."""
    parts = [todo_text(line)]
    for j in range(int(line), min(len(TODO_LINES), int(line) + max_lines - 1)):
        nxt = TODO_LINES[j]
        if not nxt.strip() or re.match(r"^\s*(- \[|#|\||```)", nxt) or not nxt.startswith(" "):
            break
        parts.append(nxt.strip())
    return " ".join(parts)


def item_is_named(line):
    return bool(re.match(r"^(?:[^\w`*]{0,4}\s*)?\*\*", todo_text(line)))


def title_for(items, sec):
    """Brief title: a named item's own bold name (plus its first clause when the name is terse);
    otherwise the heading. Never a sentence fragment."""
    if len(items) == 1:
        text = todo_item_text(items[0]["line"])
        m = re.match(r"^(?:[^\w`*]{0,4}\s*)?\*\*(.+?)\*\*\s*(.*)$", text)
        if m:
            name, rest = m.group(1).strip().rstrip(".:,;—- "), m.group(2).strip()
            rest = rest.lstrip("—-: ")
            if (len(name) < 30 or " " not in name) and rest:
                clause = re.split(r"(?<=[.;:])\s|\s—\s", rest, maxsplit=1)[0].strip().rstrip(".:,;")
                name = f"{name} — {clause[:70]}"
            return name[:100]
        parts = re.split(r"(?<=[.;:])\s|\s—\s", text, maxsplit=1)
        clause = parts[0].strip().rstrip(".:,;")
        if re.fullmatch(r"`[^`\s]+`", clause) and len(parts) > 1:  # bare code span: say what about it
            clause = f"{clause} — " + re.split(r"(?<=[.;:])\s", parts[1].strip(), maxsplit=1)[0].strip().rstrip(".:,;")[:70]
        if len(clause) >= 20:
            return clause[:100]
    return re.sub(r"\s*\(\d{4}-\d{2}-\d{2}\)\s*$", "", sec).strip().rstrip(".")[:100]


def todo_text(line):
    """The TODO.md item text at `line` (1-based) at HEAD, checkbox stripped."""
    t = TODO_LINES[int(line) - 1].strip()
    for pre in ("- [ ]", "- [x]"):
        if t.startswith(pre):
            t = t[len(pre):].strip()
    return t


def files_in(text, limit=8):
    """Source files a brief names (for the same-file collision rule)."""
    seen = []
    for m in re.finditer(r"\b((?:internal|web/src|cmd|scripts|\.github|tools)/[\w./-]+\.(?:go|ts|tsx|py|yml|yaml|sh))\b", text):
        f = m.group(1)
        if f not in seen and not f.endswith("_test.go"):
            seen.append(f)
    return seen[:limit]

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
    """File header. Re-uses the guid (and bumps the minor version) of a file that already
    exists at `path`, so regenerating the package never churns identity across a re-run."""
    guid, existing = str(uuid.uuid5(uuid.NAMESPACE_URL, path)), os.path.join(REPO, path)  # stable per path, distinct per file
    if os.path.exists(existing):
        head = open(existing, encoding="utf-8").read(400)
        mg = re.search(r"<!-- guid: ([0-9a-f-]{36}) -->", head)
        mv = re.search(r"<!-- version: (\d+)\.(\d+)\.(\d+) -->", head)
        if mg:
            guid = mg.group(1)
        if mv:
            version = f"{mv.group(1)}.{int(mv.group(2)) + 1}.0"
    return f"<!-- file: {path} -->\n<!-- version: {version} -->\n<!-- guid: {guid} -->\n<!-- last-edited: {DATE} -->\n\n"


def anchor_windows(file_, line, evidence):
    """Every `sed -n` window a cold executor must read before editing.

    The audit's `line` is only the FIRST anchor; the evidence prose names the others
    ("lines 399, 402, 423, 426", "isbn.go:418-435", "engine.go:4047"). A brief whose
    re-verify window shows one of four call sites gets one of four fixed (brief-verifier,
    2026-09-10), so collect every line reference for the anchor file plus any other
    `path:N[-M]` reference, merge ranges within 30 lines, and emit one window each."""
    refs = collections.defaultdict(set)  # file -> set(int)
    if line:
        refs[file_].add(int(line))
    base = os.path.basename(file_ or "")
    # path/or/basename.ext:N[-M]
    for m in re.finditer(r"([\w./-]+\.(?:go|ts|tsx|py|yml|yaml|sh|example|md)):(\d+)(?:-(\d+))?", evidence or ""):
        f, lo, hi = m.group(1), int(m.group(2)), int(m.group(3) or m.group(2))
        if f.endswith(base) and base:
            f = file_
        elif "/" not in f:
            continue  # bare basename of some other file — not resolvable, skip
        refs[f].update(range(lo, hi + 1) if hi - lo <= 60 else {lo, hi})
    # "lines 399, 402, 423, 426" / "lines 72-74" — bare line lists refer to the anchor file
    sep = r"(?:\s*,\s*(?:and\s+)?|\s+and\s+)"
    for m in re.finditer(r"\blines? (\d+(?:\s*[-–]\s*\d+)?(?:" + sep + r"\d+(?:\s*[-–]\s*\d+)?)*)", evidence or "", flags=re.I):
        for part in re.split(sep, m.group(1)):
            lo_hi = [int(x) for x in re.split(r"\s*[-–]\s*", part)]
            lo, hi = lo_hi[0], lo_hi[-1]
            if hi - lo <= 60:
                refs[file_].update(range(lo, hi + 1))
    out = []
    for f in sorted(refs, key=lambda x: (x != file_, x)):
        nums = sorted(refs[f])
        groups, cur = [], [nums[0]]
        for n in nums[1:]:
            if n - cur[-1] <= 30:
                cur.append(n)
            else:
                groups.append(cur)
                cur = [n]
        groups.append(cur)
        for g in groups:
            lo, hi = max(1, g[0] - 6), g[-1] + 6
            out.append(f"sed -n '{lo},{hi}p' {f}   # expect the code described under Background (drifted lines: re-find by the quoted text)")
    return out


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
    # The archive keeps the original guid; this copy is a distinct file and needs its own
    # (plan-auditor 2026-09-10: 111 archive/package guid collisions). uuid5 keeps it stable.
    text = re.sub(r"<!-- guid: .*? -->", f"<!-- guid: {uuid.uuid5(uuid.NAMESPACE_URL, new_rel)} -->", text, count=1)
    status = (f"> **Status {DATE}:** 🟡 REAL — re-verified at HEAD 42d187168: {b.get('evidence', '').strip()} "
              f"· risk **{b.get('risk', 'hygiene')}** · effort **{b.get('effort', 'M')}**"
              + (" · **(verdict changed since 09-02)**" if b.get("changed_since_0902") else ""))
    if b["task_id"] in DRIFT:
        dr = DRIFT[b["task_id"]]
        status += (f"\n> ⚠️ **Anchor drift ({DATE}, plan-auditor):** `{dr['anchor']}` → {dr['finding']}. "
                   f"Re-derive the anchor before editing; the brief body below is unchanged from 08-21.")
    text = re.sub(r"^(# TASK-\d+ — .*?\n)", lambda mm: mm.group(1) + "\n" + status + "\n", text, count=1, flags=re.M)
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": b["task_id"], "title": b["title"], "kind": "carried", "risk": b.get("risk", "hygiene"),
                  "effort": b.get("effort", "M"), "sev": None, "evidence": b.get("evidence", ""), "path": new_rel,
                  "gate": None, "prio": priority(b.get("risk"), "high" if b.get("risk") in ("data-loss", "security") else "medium"),
                  "files": files_in(text), "source_key": b["path"], "dispatch": "DISPATCH", "dispatch_why": ""})

# ---------------------------------------------------------------------------
# 2. New briefs from Wave 3 findings (TASK-300+)
# ---------------------------------------------------------------------------
NEXT = 300
findings = sorted(d["findings"], key=lambda f: (RISK_RANK.get(f.get("risk"), 6), SEV_RANK.get(f.get("severity"), 4), f["id"]))
fragments = []


def brief_body(tid, title, ws, prio, effort, tr, src_line, goal, background, anchors, steps, tests, gate, review_critical, tag,
               dispatch="", wave="per ../orchestration.md (collision-aware)"):
    branch = f"agent/{ws}-{tid[5:]}-{slug(title, 40)}"
    frag = f"changelog.d/{DATE_COMPACT}_{ws.replace('-', '_')}_{tid[5:]}.md"
    rc = ("· **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**" if review_critical else "")
    anchors_md = "\n".join(f"  {a}" for a in anchors)
    steps_md = "\n".join(f"{i}. {s}" for i, s in enumerate(steps, 1))
    tests_md = "\n".join(f"- {t}" for t in tests)
    return f"""# {tid} — {title} ({tag})

> **Status {DATE}:** 🆕 NEW — {src_line}
{dispatch}
**Priority:** {prio} · **Effort:** {effort} · **Recommended subagent:** {tr} · {ws} subagent · **Depends on:** none · **Wave:** {wave} {rc}

Source: {src_line}. Verified at HEAD `42d187168` on {DATE}; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
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

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
{"- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner." if review_critical else "- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold)."}

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
    anchors = [f"test -e {file_}   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)"]
    anchors += anchor_windows(file_, line, f.get("evidence", ""))
    gate = GATE_BY_WS.get(ws) or go_pkg_gate(file_)
    goal = f"{f.get('suggested_fix', '').strip()}\n\nWhy it matters: {f.get('why_it_matters', '').strip()}"
    background = f"- {f.get('evidence', '').strip()}\n- Anchor: `{file_}:{line}` (audit `{f['id']}`, confidence {f.get('confidence', 'medium')}, severity {sev})."
    if f.get("already_tracked_hint"):
        background += f"\n- Related tracking: {f['already_tracked_hint']}"
    adv = ADVERSARIAL.get(f["id"])
    src_line = f"Wave 3 audit finding `{f['id']}` ({f['source_file']})"
    if adv:
        src_line += f" · adversarial re-check {DATE}: **{adv['verdict']}**"
        background += (f"\n- **Adversarial re-check ({DATE}, `state/final/adversarial_top11.json`): {adv['verdict']}** — {adv['evidence']}"
                       f"\n  - Blast radius: {adv.get('blast_radius', '')}"
                       f"\n  - Existing tests to extend: {adv.get('existing_test_file', 'none')}"
                       f"\n  - Standing-ban contact: {adv.get('prod_bans_touched', 'none')}")
        if adv.get("notes"):
            background += f"\n  - Note: {adv['notes']}"
        if adv.get("fix_in_brief_correct") is False:
            goal = (f"**Correction from the adversarial re-check ({DATE}) — this overrides the audit's suggested fix where they differ:** "
                    f"{adv.get('notes') or adv.get('evidence')}\n\n") + goal
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
        tests.append("ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.")
    text = header(path) + brief_body(tid, title, ws, prio, effort, tier(effort, risk), src_line,
                                     goal, background, anchors, steps, tests, gate, review_critical, f["id"])
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": tid, "title": title, "kind": "new-finding", "risk": risk, "effort": effort, "sev": sev,
                  "evidence": f"{file_}:{line}", "path": path, "gate": None, "prio": prio,
                  "files": [file_] + files_in(f.get("evidence", "")), "source_key": f["id"], "dispatch": "DISPATCH", "dispatch_why": ""})
    fragments.append((f"todo.d/{DATE}-{slug(title, 40)}-{f['id'].lower()}.md",
                      f"- [ ] **{f['id']}** {title} — `{file_}:{line}`. {f.get('why_it_matters', '').strip()[:300]} Brief: `{path}`.\n"))

# ---------------------------------------------------------------------------
# 3. New briefs for uncovered data-loss / security TODO.md sections
# ---------------------------------------------------------------------------
sections = collections.OrderedDict()  # grouping key -> items
for t in d["todo"]:
    if t["verdict"] == "REAL" and not t.get("dup_of") and t.get("risk") in ("data-loss", "security"):
        hline, sec = real_heading(t["line"])
        v = SECTION_BY_LINE.get(t["line"], {}).get("dispatch_verdict", "UNVALIDATED")
        # One brief per REAL enclosing heading — except that a bold-named item (`**NAME** …`) is
        # its own unit of work, and items with different dispatch verdicts never share a brief
        # (a held item must not drag a dispatchable sibling with it).
        key = (hline, sec, v, t["line"] if item_is_named(t["line"]) else 0)
        sections.setdefault(key, []).append(t)
for (hline, sec, _v, _k), items in sections.items():
    tid = f"TASK-{NEXT}"
    NEXT += 1
    paths = [m.group(0) for t in items for m in [re.search(r"(internal|web|scripts|cmd|\.github)/[\w./-]+", t.get("evidence") or "")] if m]
    ws = ws_for(paths[0]) if paths else "misc-go"
    risk = min((t.get("risk") for t in items), key=lambda r: RISK_RANK.get(r, 6))
    effort = min((t.get("effort") or "M" for t in items), key=lambda e: {"S": 0, "M": 1, "L": 2}.get(e, 1))
    title = title_for(items, sec)
    fname = f"{tid}-{slug(title)}.md"
    path = f"{PKG}/{ws}/{fname}"
    lines = ", ".join(str(t["line"]) for t in items)
    # dispatch verdict = the most restrictive verdict of any validated item in this brief
    checks = [SECTION_BY_LINE[t["line"]] for t in items if t["line"] in SECTION_BY_LINE]
    verdict = min((c["dispatch_verdict"] for c in checks), key=lambda v: DISPATCH_RANK.get(v, 9)) if checks else "UNVALIDATED"
    why = "; ".join(sorted({c["why"] for c in checks})) if checks else "not covered by the 2026-09-10 validation pass — treat as HOLD until validated"
    shape = "; ".join(sorted({c["shape"] for c in checks})) if checks else "?"
    assessed = "; ".join(sorted({c["class_assessed"] for c in checks})) if checks else "?"
    bans = "; ".join(sorted({c["bans_conflict"] for c in checks if c["bans_conflict"] != "none"})) if checks else ""
    if verdict == "RECLASSIFY":
        risk = "correctness"
    review_critical = risk in ("data-loss", "security")
    hold = verdict in ("HOLD-FOR-OWNER", "DROP", "UNVALIDATED")
    dispatch_line = (f"> **Dispatch {DATE} (`state/final/todo_sections_validation.json`): {verdict}** — shape: {shape} · class: {assessed}"
                     + (f" · ⛔ standing-ban contact: {bans}" if bans else "") + f" · {why}")
    if hold:
        dispatch_line += "\n> **Do NOT dispatch this brief to a worker.** It needs an owner decision or a prod run; it is listed in BREAKDOWN under *Held for the owner* and gated in PRIORITY-MATRIX."
    anchors = []
    for t in items[:4]:
        q = todo_text(t["line"])[:60].replace('"', "").replace("\\", "")
        anchors.append(f"grep -n -F \"{q}\" TODO.md   # item L{t['line']} still exists (line numbers drift; text is the anchor)")
    anchors.append(f"sed -n '{hline}p' TODO.md   # the enclosing heading: {sec[:70]}")
    for p in sorted(set(paths))[:4]:
        anchors.append(f"test -e {p.split(':')[0]}   # anchor file from the reconciliation evidence")
    item_list = "\n".join(f"  - L{t['line']}: {todo_item_text(t['line'])[:220]}" for t in items)
    goal = (f"Close the {len(items)} still-open `TODO.md` item(s) under the heading “{sec}” (TODO.md line {hline}; items at lines {lines} as of HEAD 42d187168):\n{item_list}\n\n"
            f"Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. "
            + ("Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write." if shape != "CODE" else ""))
    background = "\n".join(f"- L{t['line']} — {todo_item_text(t['line'])[:300]} — evidence: {t.get('evidence', '')}" for t in items)
    steps = [
        "Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.",
        "Re-run the anchors; for each item confirm the gap at HEAD or report it closed.",
        "Implement item by item, smallest first; one commit per item where they are separable.",
        "Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.",
    ]
    tests = ["One regression test per item that fails on the pre-fix code.", "Existing package tests stay green (`-count=1`).",
             "ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default."]
    gate = GATE_BY_WS.get(ws) or go_pkg_gate(paths[0] if paths else "internal/")
    text = header(path) + brief_body(tid, title, ws, priority(risk, "high"), effort, tier(effort, risk), f"`TODO.md` heading “{sec}” (L{hline}), items at lines {lines}",
                                     goal, background, anchors, steps, tests, gate, review_critical, f"TODO.md:{items[0]['line']}",
                                     dispatch=dispatch_line, wave=("owner-gated — not a worker task" if hold else "per ../orchestration.md (collision-aware)"))
    write(os.path.join(NEW, ws, fname), text)
    tasks.append({"ws": ws, "id": tid, "title": title, "kind": "new-todo", "risk": risk, "effort": effort, "sev": None,
                  "evidence": f"TODO.md lines {lines}", "path": path, "gate": (f"{verdict}: {shape}" if hold or verdict == "RECLASSIFY" else None),
                  "prio": priority(risk, "high"), "files": sorted(set(p.split(":")[0] for p in paths)),
                  "source_key": f"TODO.md:{items[0]['line']}", "todo_lines": [t["line"] for t in items],
                  "dispatch": verdict, "dispatch_why": why})

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
    # Waves: effort order (S -> M -> L), then the same-file rule — a brief joins the earliest wave
    # in which no already-placed brief names one of its files (plan-auditor 2026-09-10 found
    # 3 same-wave collisions in the effort-only grouping). Held briefs are not placed.
    held = [r for r in rows if r["dispatch"] in ("HOLD-FOR-OWNER", "DROP", "UNVALIDATED")]
    placed = []  # list of (wave_no, row)
    for r in sorted((r for r in rows if r not in held), key=lambda r: ({"S": 0, "M": 1, "L": 2}.get(r["effort"], 1), RISK_RANK.get(r["risk"], 6), r["id"])):
        w = 1
        while any(pw == w and set(pr["files"]) & set(r["files"]) for pw, pr in placed):
            w += 1
        placed.append((w, r))
        r["wave"] = w
    mer = ["```mermaid", "flowchart LR"]
    for w in sorted({pw for pw, _ in placed}):
        mer.append(f"    subgraph Wave{w}")
        for pw, r in placed:
            if pw == w:
                mer.append(f"      {r['id'].replace('-', '')}[{r['id']} {r['effort']} {slug(r['title'], 28)}]")
        mer.append("    end")
    mer.append("```")
    if held:
        mer.append("")
        mer.append("**Held for the owner (not dispatchable as code):** " + ", ".join(f"{r['id']} ({r['dispatch']})" for r in held))
    write(os.path.join(NEW, ws, "orchestration.md"), header(f"{PKG}/{ws}/orchestration.md") + f"""# Orchestration — {ws} workstream (todo-completion-2026-09)

Read the package-level [`../ORCHESTRATION.md`](../ORCHESTRATION.md) first. Waves here are effort order (S → M → L) with the **same-file rule applied**: a brief joins the earliest wave in which no earlier-placed brief names one of its source files, so two briefs that share a file never sit in the same wave. Carried-forward briefs keep their original `Depends on:` line — honor it over this grouping.

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
    ws_tables.append("| Task | Kind | Risk | Sev | Priority | Effort | Dispatch | Title | Evidence |\n|---|---|---|---|---|---|---|---|---|")
    for r in sorted(rows, key=lambda r: (RISK_RANK.get(r["risk"], 6), SEV_RANK.get(r["sev"], 4), r["id"])):
        disp = r["dispatch"] if r["dispatch"] == "DISPATCH" else f"**{r['dispatch']}**"
        ws_tables.append(f"| [{r['id']}]({ws}/{os.path.basename(r['path'])}) | {r['kind']} | {r['risk']} | {r['sev'] or '—'} | {r['prio']} | {r['effort']} | {disp} | {r['title'][:90]} | {r['evidence'][:100].replace('|', '/')} |")
held_all = [t for t in tasks if t["dispatch"] in ("HOLD-FOR-OWNER", "DROP", "UNVALIDATED")]
reclass_all = [t for t in tasks if t["dispatch"] == "RECLASSIFY"]
held_md = "\n".join(f"- [{t['id']}]({t['ws']}/{os.path.basename(t['path'])}) — {t['title'][:80]} — **{t['dispatch']}**: {t['dispatch_why'][:200]}" for t in held_all) or "- none"
reclass_md = "\n".join(f"- [{t['id']}]({t['ws']}/{os.path.basename(t['path'])}) — {t['title'][:80]} — now `{t['risk']}` (standard lane): {t['dispatch_why'][:200]}" for t in reclass_all) or "- none"

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
- **{n_td} new from `TODO.md`**: one brief per *enclosing heading* (re-derived from `TODO.md` itself, not the inventory's `## `-only section field) whose still-open items were classed data-loss or security and had no brief. Each carries a `Dispatch` verdict from the 2026-09-10 validation pass (`state/final/todo_sections_validation.json`): **{len(held_all)} held for the owner** (decision / prod run — not worker tasks), **{len(reclass_all)} reclassified** to correctness (standard lane), the rest dispatchable. The remaining {S['todo_by_verdict'].get('REAL', 0) - S['todo_dup_of_brief'] - sum(len(v) for v in sections.values())} uncovered REAL items (correctness/perf/ux/hygiene) stay tracked in `TODO.md` and appear as `todo-section` rows in the matrix — brief them on demand when the cut line reaches them.

Not in this package, deliberately: {len(gated)} REAL sibling briefs that are **owner-gated** ({", ".join(sorted(set(b['path'].split('/')[2] for b in gated)))}) and {len(sib_real)} REAL briefs that live in still-active sibling initiatives ({", ".join(sorted(set(b['path'].split('/')[2] for b in sib_real)))}) — those packages keep their own READMEs; their status rows are in RECONCILIATION §1.

## Per-workstream briefs
{chr(10).join(ws_tables)}

## Held for the owner — {len(held_all)} briefs (decision or prod run; never dispatch as code)

{held_md}

## Reclassified — {len(reclass_all)} briefs (validation found the risk class wrong)

{reclass_md}

## Same-file collision rule (drives wave ordering — GLOBAL across workstreams)

Two briefs whose anchors name the same file never run in the same wave. The per-workstream `orchestration.md` waves already apply this rule within a workstream (effort order first, then the earliest collision-free wave); the coordinator still applies it ACROSS workstreams before dispatch — `state/final/brief_index.json` lists every brief's files.

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

with open(os.path.join(STATE, "final", "brief_index.json"), "w", encoding="utf-8") as fh:
    json.dump([{k: t.get(k) for k in ("id", "kind", "ws", "path", "risk", "sev", "effort", "prio", "dispatch", "dispatch_why", "files", "source_key", "todo_lines", "wave", "title")} for t in tasks],
              fh, indent=1, ensure_ascii=False)
print(json.dumps({"carried": n_car, "new_findings": n_fnd, "new_todo_sections": n_td, "total_briefs": len(tasks),
                  "held": len(held_all), "reclassified": len(reclass_all),
                  "workstreams": {k: len(v) for k, v in by_ws.items()}, "fragments": len(fragments),
                  "files_written": len(written)}, indent=1))
