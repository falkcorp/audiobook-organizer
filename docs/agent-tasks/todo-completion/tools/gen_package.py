#!/usr/bin/env python3
"""Project the scout JSON into a plan-op Tier L package (skeleton-first).

Usage: gen_package.py <scout-dir> <plan-worktree-root>
Writes docs/agent-tasks/todo-completion/** and docs/plans/2026-08-21-todo-completion-taskboard.md
"""
import json, sys, os, re, uuid, glob, collections, shutil, subprocess

SCOUT, ROOT = sys.argv[1], sys.argv[2]
DATE = "2026-08-21"
PKG = "todo-completion"
REPO = "/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer"
TRAILER = "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
OUT = os.path.join(ROOT, "docs/agent-tasks", PKG)

PROTOCOL = """> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`{gate}`) in each
> finished worktree, opens the PR, merges (rebase/FF unless the repo profile says
> otherwise), and then **rebases every open sibling worktree** before dispatching
> anything else.
>
> **Per-merge sibling-rebase loop:** after EVERY merge to `origin/main`:
> for each open sibling worktree, `git fetch origin && git rebase
> origin/main`. A sibling that skips a rebase is a future conflict.
>
> **Conflict escalation ladder** (in order, never skip a rung): 1) clean rebase;
> 2) conflict-resolver subagent (Sonnet-class, only when the conflict spans 1–3 small
> files); 3) file-copy cherry-pick fallback — re-apply the task's file states onto a
> fresh branch from HEAD; 4) mark `rebase_blocked`, stop the lane, escalate to a human.
>
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR that is
> NOT a held review-critical PR; any sibling worktree is un-rebased; the gate is red on
> `origin/main`; or a `rebase_blocked` marker is unresolved.
>
> **Held PRs (review-critical / prod-data path):** the coordinator opens the PR and
> STOPS — never `gh pr merge`. A held PR does not block the wave; only tasks that share a
> file with it are deferred to a `held-dependent` queue and dispatched after the owner
> merges it. The owner sees the held list in the coordinator's status report."""

# ---------- load ----------
items = []
for f in sorted(glob.glob(os.path.join(SCOUT, "scope-*.json"))):
    try:
        arr = json.load(open(f))
    except Exception as e:
        print("BAD JSON", f, e); continue
    for o in arr:
        o["_scope"] = os.path.basename(f)
        items.append(o)
print("items loaded:", len(items))
import csv
_sec = {}
for r in csv.DictReader(open(os.path.join(ROOT, "docs/plans/2026-08-21-todo-completion-inventory.tsv")), delimiter="\t"):
    _sec[int(r["todo_line"])] = r["section"]
for o in items:
    try: o["_section"] = _sec.get(int(o.get("todo_line") or 0), "")
    except Exception: o["_section"] = ""

def ws_for(domain, notes, section=""):
    d = (domain or "").strip().lower()
    if d.startswith("web"): d = "web"
    if d.startswith("docs"): d = "docs"
    if d.startswith(".github") or d.startswith("scripts") or d.startswith("ci"): d = "ci/scripts"
    if "missing-file" in (section or "").lower() or "missing-file" in (notes or "").lower()[:80]:
        return "missing-file-lane"
    m = {
        "web": "web", "docs": "docs", "ci/scripts": "ci-tooling", ".github": "ci-tooling", "scripts": "ci-tooling",
        "internal/database": "database", "internal/dedup": "dedup", "internal/plugins/dedup": "dedup",
        "internal/merge": "dedup", "internal/server/handlers": "server-handlers", "internal/server": "server",
        "internal/search": "search", "internal/itunes": "itunes", "internal/plugins/itunes": "itunes",
        "internal/syncapi": "abs-sync", "internal/audiobooks": "audiobooks", "internal/organizer": "organize",
        "internal/metafetch": "metadata", "internal/metadata": "metadata", "internal/config": "config",
        "internal/operations": "operations", "internal/plugins/maintenance": "maintenance",
        "internal/maintenance": "maintenance", "internal/scanner": "scanner", "internal/transcribe": "transcribe",
        "internal/aiscan": "transcribe", "cmd": "cli",
    }
    for k in sorted(m, key=len, reverse=True):
        if d == k or d.startswith(k + "/"):
            return m[k]
    if d.startswith("internal/"):
        return "misc-go"
    return "misc"

def slugify(s):
    s = re.sub(r"[^a-z0-9]+", "-", (s or "").lower()).strip("-")
    return s[:48] or "task"

tasks, b2, b3, stale, parked, nottask = [], [], [], [], [], []
for o in items:
    v = o.get("verdict")
    if v == "actionable":
        if not (o.get("exact_files") or []):
            o["verdict_evidence"] = "NEEDS SCOPING: scout returned no exact_files — cannot enter the collision matrix. " + (o.get("verdict_evidence") or "")
            b2.append(o)
        else:
            tasks.append(o)
    elif v == "needs_design": b2.append(o)
    elif v == "prod_run": b3.append(o)
    elif v == "stale_done": stale.append(o)
    elif v == "parked": parked.append(o)
    else: nottask.append(o)

# split tasks by workstream and assign ids
tasks.sort(key=lambda o: (ws_for(o.get("domain"), o.get("notes"), o.get("_section","")), int(o.get("todo_line") or 0), o.get("part") or 0))
def stable_key(o):
    return f"{o.get('_scope')}|{o.get('todo_line')}|{o.get('part')}|{(o.get('title') or '')[:60]}"
IDMAP_PATH = os.path.join(SCOUT, "..", "task-ids.json")
idmap = json.load(open(IDMAP_PATH)) if os.path.exists(IDMAP_PATH) else {}
next_n = 1 + max([int(v[5:]) for v in idmap.values()] or [0])
for t in tasks:
    k = stable_key(t)
    if k not in idmap:
        idmap[k] = f"TASK-{next_n:03d}"; next_n += 1
    t["id"] = idmap[k]
    t["stable_key"] = k
json.dump(idmap, open(IDMAP_PATH, "w"), indent=1, sort_keys=True)
for t in tasks:
    t["ws"] = ws_for(t.get("domain"), t.get("notes"), t.get("_section",""))
    t["slug"] = slugify(t.get("title"))
    t["scope_file"] = t["_scope"]
    ef = []
    t["dropped_files"] = []
    for f in (t.get("exact_files") or []):
        f = str(f).strip().strip("`")
        if " " in f or "(" in f or len(f) > 160:
            t["dropped_files"].append(f); continue
        if f == "TODO.md": continue   # close-outs are a coordinator commit per wave, never a worker edit
        ef.append(f)
    # hub-file inference (simplicity judge blockers 2 & 3)
    if any(f.startswith("internal/plugins/maintenance/") and not os.path.exists(os.path.join(REPO, f)) for f in ef):
        ef.append("internal/plugins/maintenance/plugin.go")
    if any(re.match(r"internal/database/iface_.*\.go$", f) or f == "internal/database/store.go" for f in ef):
        for m in ("internal/database/mock_store.go", "internal/database/mocks/mock_store.go"):
            if os.path.exists(os.path.join(REPO, m)): ef.append(m)
    t["exact_files"] = sorted(set(ef))
    t["tier_label"] = {"haiku": "Haiku-class", "sonnet": "Sonnet-class", "opus": "Opus-class"}.get((t.get("tier") or "sonnet").lower(), "Sonnet-class")
    if t.get("review_critical") and t["tier_label"] != "Opus-class":
        t["tier_label"] = "Sonnet-class" if (t.get("effort") or "M") == "S" else "Opus-class"

# depends_on by todo_line → ids
NONTASK_VERDICT = {o.get("todo_line"): o.get("verdict") for o in items if o.get("verdict") != "actionable"}
by_line = collections.defaultdict(list)
idx_by_id = {t["id"]: t for t in tasks}
for t in tasks: by_line[int(t.get("todo_line") or 0)].append(t["id"])
for t in tasks:
    deps = []
    for ln in t.get("depends_on_lines") or []:
        if str(ln).startswith("TASK-"):
            deps.append(ln); continue
        try: ln = int(ln)
        except: continue
        hit = [i for i in by_line.get(ln, []) if i != t["id"]]
        if ln == int(t.get("todo_line") or 0):   # same-line dependency = earlier parts only (never a sibling cycle)
            me = int(t.get("part") or 0)
            hit = [i for i in hit if int(idx_by_id[i].get("part") or 0) < me]
        if hit: deps += hit
        else:
            v = NONTASK_VERDICT.get(ln, "unknown")
            if v in ("needs_design", "prod_run", "parked"):
                t.setdefault("external_blockers", []).append(f"TODO.md L{ln} ({v}) — not a task in this package; coordinator confirms it is resolved or explicitly waives it before dispatch")
            # stale_done / not_a_task dependencies are satisfied or moot: drop silently
    t["depends_on"] = sorted(set(d for d in deps if d in {x["id"] for x in tasks}))

# ---------- collision matrix + waves (global) ----------
SOFT_HUBS = {"internal/plugins/maintenance/plugin.go", "internal/database/mock_store.go", "internal/database/mocks/mock_store.go",
             "web/public/openapi.json", "docs/api/openapi.json", "internal/server/routes.go", "web/src/api/types.ts", "changelog.d"}
def is_soft(f):
    return f in SOFT_HUBS or os.path.basename(f) == "openapi.json"
file_tasks = collections.defaultdict(list)
soft_tasks = collections.defaultdict(list)
for t in tasks:
    for f in t["exact_files"]:
        (soft_tasks if is_soft(f) else file_tasks)[f].append(t["id"])
collisions = {f: ids for f, ids in file_tasks.items() if len(ids) > 1}
soft_collisions = {f: ids for f, ids in soft_tasks.items() if len(ids) > 1}
idx = {t["id"]: t for t in tasks}
wave_of = {}
# topological by depends_on, then earliest wave w/o collision
order = sorted(tasks, key=lambda t: (bool(t.get("review_critical")), t["id"]))  # held (review-critical) PRs placed after siblings on the same file so a held PR never blocks a lane
placed = set()
_placing = set()
def place(t):
    if t["id"] in wave_of: return wave_of[t["id"]]
    if t["id"] in _placing:
        raise SystemExit(f"DEPENDENCY CYCLE through {t['id']}: {sorted(_placing)}")
    _placing.add(t["id"])
    minw = 1
    for d in t["depends_on"]:
        if d in idx: minw = max(minw, place(idx[d]) + 1)
    _placing.discard(t["id"])
    w = minw
    while True:
        clash = False
        for f in t["exact_files"]:
            for other in file_tasks[f]:
                if other != t["id"] and wave_of.get(other) == w:
                    clash = True; break
            if clash: break
        if not clash: break
        w += 1
    wave_of[t["id"]] = w
    return w
for t in order: place(t)
for t in tasks: t["wave"] = wave_of[t["id"]]

# ---------- write ----------
shutil.rmtree(OUT, ignore_errors=True)
os.makedirs(OUT, exist_ok=True)
def hdr(path):
    return f"<!-- file: {path} -->\n<!-- version: 1.0.0 -->\n<!-- guid: {uuid.uuid4()} -->\n<!-- last-edited: {DATE} -->\n\n"
def gate_for(t):
    files = t["exact_files"]
    web = any(f.startswith("web/") for f in files)
    go = any(f.endswith(".go") or f in ("go.mod", "Makefile") for f in files)
    # `make ci` is RED on main (10 pre-existing staticcheck findings — see
    # docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md), so the Go gate is build+vet+targeted tests.
    pkgs = sorted({os.path.dirname(f) for f in files if f.endswith(".go") and os.path.dirname(f)})
    gotest = "go test " + " ".join("./" + d + "/..." for d in pkgs) + " -count=1" if pkgs else "go test ./... -count=1 -short"
    gogate = "go build ./... && go vet ./... && " + gotest
    if web and go: return gogate + " && npm --prefix web run lint && npm --prefix web test"
    if web: return "npm --prefix web run lint && npm --prefix web test"
    if go: return gogate
    return "git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'"
def lst(xs, prefix="- "):
    return "\n".join(f"{prefix}{x}" for x in (xs or [])) or f"{prefix}(none)"
def nlst(xs):
    xs = [re.sub(r"^\s*(?:\d+[.)]|step \d+[:.])\s*", "", str(x), flags=re.I) for x in (xs or [])]
    return "\n".join(f"{i}. {x}" for i, x in enumerate(xs, 1)) or "1. (none)"

TODO_BASELINE = "46628240"  # the TODO.md revision the scouts read; line numbers are relative to it
_TODO = subprocess.run(["git", "-C", REPO, "show", f"{TODO_BASELINE}:TODO.md"], capture_output=True, text=True).stdout.splitlines()
def todo_refind(t):
    ln = int(t.get("todo_line") or 0)
    if ln <= 0: return "this item lives in a todo.d/ fragment (see src_id), not in TODO.md"
    raw = _TODO[ln-1] if ln-1 < len(_TODO) else ""
    m = re.search(r"\*\*([A-Za-z0-9][A-Za-z0-9._/ -]{2,40})\*\*", raw)
    if m:
        return f'grep -n -F "**{m.group(1)}**" TODO.md'
    frag = re.sub(r"^\s*(- \[[ x]\]|\d+\.)\s*", "", raw).strip()[:50]
    frag = frag.replace('"', '\\"')
    return f'grep -n -F "{frag}" TODO.md' if len(frag) > 12 else f"sed -n '{ln}p' TODO.md"

def brief(t):
    ws, nn, slug = t["ws"], t["id"][5:], t["slug"]
    rel = f"docs/agent-tasks/{PKG}/{ws}/{t['id']}-{slug}.md"
    gate = gate_for(t)
    anchors = "\n".join(f"  {a.get('grep_cmd')}   # {a.get('expect','')} — {a.get('claim','')}" for a in (t.get("verified_anchors") or []))
    if not anchors:
        ex = [f for f in t["exact_files"] if os.path.exists(os.path.join(REPO, f))]
        anchors = "\n".join(f"  test -f {f} && echo OK   # 1 hit — file exists at HEAD (docs/edit target)" for f in ex) or "  # (new-file task: no pre-existing anchors; the exact_files above are CREATED by this task)"
    reuse = "\n".join(f"- Use `{r.get('name')}` in `{r.get('file')}` (verify: `{r.get('verify_grep')}`) — do NOT write a parallel helper." for r in (t.get("reuse") or [])) or "- No existing helper identified; do not invent new constants for concepts that already have a name — grep first."
    pol = t.get("polarity") or "additive"
    new_sym = (t.get("acceptance") or ["<new symbol>"])[0]
    DATA_ROLLBACK = ("**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn \"func.*CreateOperationChange\" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.\n\n")
    if pol == "additive":
        idem = f"If the first acceptance check below already passes at HEAD (`{new_sym}`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change)."
    elif pol == "removal":
        idem = f"If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched."
    else:
        idem = f"If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored."
    if t.get("review_critical"):
        idem = DATA_ROLLBACK + idem
    aos = t.get("anti_over_suppression") or "N/A"
    aos_line = "Anti-over-suppression: N/A" if aos.strip().upper() == "N/A" else f"Anti-over-suppression test: `{aos}` — a known-good input still passes with the new guard active."
    rc = " · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**" if t.get("review_critical") else ""
    dep = ", ".join(t["depends_on"]) if t["depends_on"] else "none"
    if t.get("external_blockers"): dep += " · **External blockers:** " + "; ".join(t["external_blockers"])
    body = f"""# {t['id']} — {t.get('title')} ({t.get('src_id') or 'TODO.md L' + str(t.get('todo_line'))})

**Priority:** P{1 if t.get('review_critical') else 2} · **Effort:** {t.get('effort','M')} · **Recommended subagent:** {t['tier_label']} · {t['ws']} subagent · **Why:** {t.get('why_tier','')} · **Depends on:** {dep} · **Wave:** {t['wave']}{rc}

Source: `TODO.md` line {t.get('todo_line')} as of commit {TODO_BASELINE} (later edits shift lines) — re-find it with `{todo_refind(t)}` (line numbers drift; the grep is built from the line's own text). Scope file: `{t['_scope']}`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO={REPO}   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/{ws}-{nn}-{slug}" -b agent/{ws}-{nn}-{slug} origin/main
cd "$REPO/.worktrees/{ws}-{nn}-{slug}"
git rebase origin/main
{"npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree" if ws == "web" else "# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)"}
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

{t.get('goal','')}

## Background (verify before editing)

{lst(t.get('background'))}

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
{anchors}
  ```

### Reuse — don't invent

{reuse}

## Step-by-step

{nlst(t.get('steps'))}

Then, always:
- Keep the change purely {pol} — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: {DATE}`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/{DATE.replace('-','')}_{ws}_{nn}.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

{lst(t.get('edge_cases'))}

## Tests

{lst(t.get('tests'))}

{aos_line}

## How to test

```bash
{gate}
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

{lst(t.get('acceptance'), '- [ ] ')}
- [ ] {aos_line}
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `{gate}` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: {DATE}" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/{DATE.replace('-','')}_{ws}_{nn}.md`.

## Commit message

```
{'fix' if 'fix' in (t.get('title') or '').lower() else 'feat' if pol=='additive' else 'refactor'}({ws}): {(t.get('title') or '')[:60]} ({t.get('src_id') or 'TODO L' + str(t.get('todo_line'))})

<why the change was needed; what it protects; what it deliberately does NOT change>

{TRAILER}
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

{idem}

## Coordinator notes

{t.get('notes') or '(none)'}
"""
    return rel, hdr(rel) + body

ws_tasks = collections.defaultdict(list)
for t in tasks: ws_tasks[t["ws"]].append(t)

for ws, ts in ws_tasks.items():
    d = os.path.join(OUT, ws); os.makedirs(d, exist_ok=True)
    for t in ts:
        rel, text = brief(t)
        open(os.path.join(ROOT, rel), "w").write(text)
    # README
    rel = f"docs/agent-tasks/{PKG}/{ws}/README.md"
    rows = "\n".join(f"| {t['id']} | {t.get('src_id') or 'L'+str(t.get('todo_line'))} | {(t.get('title') or '')[:70]} | P{1 if t.get('review_critical') else 2} | {t.get('effort','M')} | {t['tier_label']} | {t['wave']} |" for t in ts)
    shared = [(f, ids) for f, ids in collisions.items() if any(i in {x['id'] for x in ts} for i in ids)]
    coll = "\n".join(f"- `{f}`: {', '.join(ids)} → serialize by wave ({', '.join(f'{i}=w{wave_of[i]}' for i in ids)})" for f, ids in sorted(shared)) or "- No same-file collisions inside this workstream."
    waves = collections.defaultdict(list)
    for t in ts: waves[t["wave"]].append(t["id"])
    wt = "\n".join(f"| {w} | {', '.join(waves[w])} | {'none' if w==1 else f'wave {w-1} merged + siblings rebased'} | disjoint files within the wave (computed collision matrix) |" for w in sorted(waves))
    gates = sorted({gate_for(t) for t in ts})
    open(os.path.join(ROOT, rel), "w").write(hdr(rel) + f"""# Workstream — {ws} (todo-completion)

{len(ts)} tasks projected from the 2026-08-21 TODO-completion skeleton (`../skeleton.json`). Every fact here is a projection of that skeleton — edit the skeleton and regenerate, never this file.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
{rows}

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- Gate for every task in this workstream:
  ```bash
  {' ; '.join(gates)}
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Review-critical tasks (prod-data paths) are Opus-class and their PRs stay open for the owner.

## Collision / wave note

{coll}

| Wave | Tasks | Prereq | Parallel-safe because |
|------|-------|--------|-----------------------|
{wt}

Waves are GLOBAL across the package: a wave-2 task here may be waiting on a wave-1 task in another workstream that shares a file (see `../BREAKDOWN-{DATE}.md` collision table).

See [ORCHESTRATION.md](../../ORCHESTRATION.md) for the coordinator + worker protocol.
""")
    # orchestration
    rel = f"docs/agent-tasks/{PKG}/{ws}/orchestration.md"
    mer = []
    for w in sorted(waves):
        mer.append(f"    subgraph Wave{w}\n" + "\n".join(f"      {i.replace('-','')}[{i} {idx[i]['slug'][:28]}]" for i in waves[w]) + "\n    end")
    edges = []
    for t in ts:
        for d_ in t["depends_on"]:
            edges.append(f"    {d_.replace('-','')} --> {t['id'].replace('-','')}")
        for f in t["exact_files"]:
            for o in file_tasks[f]:
                if o != t["id"] and wave_of[o] < t["wave"] and o in {x['id'] for x in ts}:
                    edges.append(f"    {o.replace('-','')} --> {t['id'].replace('-','')}")
    edges = sorted(set(edges))
    open(os.path.join(ROOT, rel), "w").write(hdr(rel) + f"""# Orchestration — {ws} workstream (todo-completion)

Read the package-level [`../../ORCHESTRATION.md`](../../ORCHESTRATION.md) first. This file only adds the workstream-specific wave order.

## Waves (respect `Depends on:` and the collision matrix)

```mermaid
flowchart LR
{chr(10).join(mer)}
{chr(10).join(edges)}
```

An edge `A --> B` means B waits for A's merge (shared file or explicit dependency). No edge = parallel-safe.

## Coordinator protocol (verbatim)

{PROTOCOL.format(gate=' ; '.join(gates))}

## Run it

Dispatch each brief with the paste preamble:

> You are an autonomous coding agent. Execute this task exactly. Do not skip the
> START HERE setup. Stop and report if any acceptance criterion fails.

Run at most 4 workers concurrently on this machine (16 concurrent agents crashed the session on {DATE}).
""")

# skeleton
skel = {"profile": {"docs_root": "docs", "gate_cmd": "go build ./... && go vet ./... && go test ./<changed-pkgs>/... -count=1 (make ci is RED on main — never the gate)", "default_branch": "main", "repo_abs_path": REPO, "commit_trailer": TRAILER},
        "tasks": [{**{k: v for k, v in t.items() if not k.startswith("_")}, "gate": gate_for(t)} for t in tasks],
        "collisions": collisions, "soft_collisions": soft_collisions,
        "workstreams": [{"ws": ws, "tasks": [t["id"] for t in ts]} for ws, ts in ws_tasks.items()],
        "buckets": {"b2_needs_design": b2, "b3_prod_run": b3, "stale_done": stale, "parked": parked, "not_a_task": nottask}}
json.dump(skel, open(os.path.join(OUT, "skeleton.json"), "w"), indent=1)

# BREAKDOWN
rel = f"docs/agent-tasks/{PKG}/BREAKDOWN-{DATE}.md"
coll_rows = "\n".join(f"| `{f}` | {', '.join(ids)} | serialize: {', '.join(f'wave{wave_of[i]}={i}' for i in sorted(ids, key=lambda i: wave_of[i]))} |" for f, ids in sorted(collisions.items())) or "| (none) | | |"
ws_sections = []
for ws, ts in ws_tasks.items():
    rows = "\n".join(f"| {t['id']} | {t.get('src_id') or 'L'+str(t.get('todo_line'))} | {(t.get('title') or '')[:70]} | **{t['tier_label']}** | {t.get('why_tier','')[:90]} | {t['wave']} |" for t in ts)
    n_similar = len(ts)
    mode = ("`/parallel-sweep` — trigger: %d tasks (≥3 threshold) with disjoint files per wave (collision matrix above), Bucket-1 briefs, gate = per-brief How-to-test block (make ci is red on main)." % n_similar) if n_similar >= 3 else "SERIAL (coordinator-driven) — fewer than 3 tasks."
    if any(t.get("review_critical") for t in ts):
        mode += " Review-critical tasks inside this workstream are **SINGLE-AGENT (strong model)** — never parallelized with each other, PR left open for the owner."
    ws_sections.append(f"### WS — {ws} · {len(ts)} tasks\n\n| Task | TODO id | Title | Tier | Why tier | Wave |\n|------|---------|-------|------|----------|------|\n{rows}\n\nExecution mode: {mode}\n")
def tbl(rows, cols):
    return "\n".join(cols) + "\n" + "\n".join(rows)
b2_rows = "\n".join(f"| L{o.get('todo_line')} {(o.get('title') or '')[:60]} | {(o.get('verdict_evidence') or '')[:160]} |" for o in b2) or "| (none) | |"
b3_rows = " · ".join(f"L{o.get('todo_line')} {(o.get('title') or '')[:50]}" for o in b3) or "(none)"
stale_rows = "\n".join(f"| L{o.get('todo_line')} | {(o.get('title') or '')[:60]} | {(o.get('verdict_evidence') or '')[:140]} |" for o in stale) or "| (none) | | |"
parked_rows = "\n".join(f"| L{o.get('todo_line')} | {(o.get('title') or '')[:60]} | {(o.get('verdict_evidence') or '')[:120]} |" for o in parked) or "| (none) | | |"
tiers = collections.Counter(t["tier_label"] for t in tasks)
open(os.path.join(ROOT, rel), "w").write(hdr(rel) + f"""# Agent-Task Breakdown & Fan-Out Plan — {DATE} (todo-completion)

This document turns the TODO-completion master plan ([`../../plans/2026-08-21-todo-completion-master-plan.md`](../../plans/2026-08-21-todo-completion-master-plan.md)) into **weak-model-proof agent briefs** plus a fan-out strategy. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md). Every table here is projected from [`skeleton.json`](skeleton.json); regenerate, never hand-edit.

## Method

{len(items)} scoped items (16 read-only scouts, {DATE}) were verified against HEAD and sorted into buckets. **Only Bucket 1 becomes agent briefs.** Owner decisions of {DATE} (see `docs/plans/DECISIONS-PENDING.md`) are applied: parked tracks are excluded, prod runs go to `docs/operations/pending-prod-actions.md`.

Counts: Bucket 1 = **{len(tasks)}** briefs ({', '.join(f'{k} {v}' for k, v in tiers.most_common())}) · Bucket 2 = {len(b2)} · Bucket 3 = {len(b3)} · stale/done = {len(stale)} · parked = {len(parked)} · not-a-task = {len(nottask)}.

---

## Bucket 1 — Authored as agent briefs

### ⚠️ Same-file collision rule (drives wave ordering — GLOBAL across workstreams)

| Shared file | Tasks that touch it | Resolution |
|-------------|---------------------|------------|
{coll_rows}

Waves: {', '.join(f'wave {w}: {n}' for w, n in sorted(collections.Counter(t['wave'] for t in tasks).items()))}.

#### Soft collisions (append-only hub files — NOT wave-serialized; resolved by the per-merge sibling rebase + conflict ladder)

| Hub file | Tasks that append to it |
|----------|-------------------------|
{chr(10).join(f"| `{f}` | {', '.join(ids)} |" for f, ids in sorted(soft_collisions.items())) or "| (none) | |"}

Rule for the coordinator: after every merge touching a hub file, rebase each sibling that lists the same hub; a registrar/mock conflict is a 1–3 line append and goes to rung 2 (conflict-resolver) at most — never rung 4.

{chr(10).join(ws_sections)}

### Coordinator protocol (verbatim)

{PROTOCOL.format(gate='the per-brief How-to-test block (go build/vet/targeted tests or npm lint+test; make ci is RED on main and is never the gate)')}

---

## Bucket 2 — NOT briefs: needs brainstorm/design first

| Item | Why it needs design first |
|------|---------------------------|
{b2_rows}

---

## Bucket 3 — NOT tasks: operational / prod-verification (no code deliverable)

{b3_rows}

Route these to `docs/operations/pending-prod-actions.md`. ⚠️ A running scan clobbers applied metadata — never apply during a scan.

---

## Stale — already done at HEAD (close the box; one close-out commit)

| Line | Item | Evidence |
|------|------|----------|
{stale_rows}

## Parked by owner decision ({DATE})

| Line | Item | Decision |
|------|------|----------|
{parked_rows}

---

## Cost / efficiency strategy (fan-out)

- **Tier split:** Haiku-class for mechanical edits; Sonnet-class default; Opus-class only for review-critical / cross-cutting. Actual: {', '.join(f'{k} {v}' for k, v in tiers.most_common())}.
- **Coordinator owns git/gh:** workers stay in their worktree and report done; only the coordinator merges + rebases siblings. PRs merge on green CI; review-critical PRs stay open for the owner.
- **Concurrency cap: 4 workers at a time on this machine.** 16 concurrent agents crashed the session on {DATE}.
- **Waves respect the collision table** — never co-schedule two tasks touching the same file.
- **Known CI noise:** `plugins/maintenance` tests are flaky (mutation-matrix record); `internal/server` test package stalls (TODO-SRVTIMEOUT — fixed by its own task in this package).
""")
print(f"tasks={len(tasks)} b2={len(b2)} b3={len(b3)} stale={len(stale)} parked={len(parked)} not_task={len(nottask)} collisions={len(collisions)} waves={max(wave_of.values()) if wave_of else 0}")
print("per ws:", {ws: len(ts) for ws, ts in ws_tasks.items()})
