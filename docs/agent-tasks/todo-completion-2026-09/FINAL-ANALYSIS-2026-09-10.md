<!-- file: docs/agent-tasks/todo-completion-2026-09/FINAL-ANALYSIS-2026-09-10.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c9f2a8e-6b4d-4f17-9e2a-8d5c1b7f0e46 -->
<!-- last-edited: 2026-09-10 -->

# Final analysis — burndown 2026-09-10 (todo-completion-2026-09)

Read this before dispatching anything. It is the adversarial pass over the package that
[`BREAKDOWN-2026-09-10.md`](BREAKDOWN-2026-09-10.md) describes: four read-only auditors
attacked the package at HEAD `42d187168`, every defect they found was fixed **in the
generator** (never by hand-editing a brief), the package was regenerated, and the result
is the recommended cut line and wave plan in §5. Raw auditor outputs are in
[`state/final/`](state/final/); the per-agent reports are in
[`state/RAW-RESULTS.md`](state/RAW-RESULTS.md) under *Final analysis*.

## 1. What was audited, and by what

| Auditor | Scope | Output |
|---|---|---|
| `plan-op:plan-auditor` (sonnet, read-only, 28 tool calls) | header lint on 219 files; anchor check on all 65 new briefs; every re-verify grep of all 111 carried briefs; same-file collisions across matrix §A rows 1–53; section structure | [`state/final/plan_audit.json`](state/final/plan_audit.json) |
| `audiobook-organizer:go-specialist` (sonnet, read-only, 53 tool calls) | adversarial re-verification of the 11 top-risk audit findings: real at HEAD? severity right? fix in the brief correct? blast radius, tests, standing-ban contact | [`state/final/adversarial_top11.json`](state/final/adversarial_top11.json) |
| `plan-op:brief-verifier` (sonnet, read-only, 34 tool calls) | cold-executed 6 top briefs (TASK-300, 302, 303, 309, 310, and the fix-library-states TODO brief) against an 8-point checklist | [`state/final/brief_verifier_top6.json`](state/final/brief_verifier_top6.json) |
| `Explore` (sonnet, read-only, 31 tool calls) | every one of the 30 TODO-section briefs: are the cited items still open, is the risk class right, is it a CODE task or a decision / prod run, does it touch a standing ban | [`state/final/todo_sections_validation.json`](state/final/todo_sections_validation.json) |

All four ran under the no-fork rule with a hard tool-call budget; none spawned children.

## 2. Findings — and what changed because of them

### 2.1 The 35 audit findings hold up

| Finding | Brief | Verdict | Consequence |
|---|---|---|---|
| DA-01 unguarded `MergeSplitBookCluster` RMW | TASK-300 | **CONFIRMED** | route is live and auth-gated; the racer is `merge.Service.MergeBooks`/`CombineBooks` on the same book id; fix as written |
| DA-02 auto-merge bypasses `MergeJournaled` | TASK-301 | **CONFIRMED** | exactly the 5 sites; the 3 HTTP sites use the narrower `h.mergeService`, so the fix needs an interface-shape decision — effort M is right but it is not a drop-in |
| DB-01 `book:0`..`book:;` guard bound | TASK-302 | **CONFIRMED** | `CreateBook` accepts caller-supplied non-ULID ids unconditionally, so the residual is real |
| SF-01 organize no-op returns success without stat | TASK-303 | **PARTLY** | branch at `:141-142` is real; the second branch (`:187-188`) already sits inside `os.Stat(targetPath)` — **the brief's Goal now carries the correction: fix `:141-142` only** |
| SV-02 restore `verify` silently skipped | TASK-306 | **CONFIRMED** | unimplemented at BOTH layers (`handler.go` and `backup.go:499-504`) |
| DB-02 three unbatched migration writes | TASK-305 | **CONFIRMED** | latent today; option (b) means touching ~60 `Up` signatures — prefer option (a) idempotency + lint |
| SF-02 scanner discards `AIPhaseSummary` | TASK-309 | **CONFIRMED** | surface as a non-fatal warning on the op record; do NOT fail a good scan chunk because the LLM was down |
| SF-03 ISBN sweep drops provider errors | TASK-310 | **CONFIRMED** | `isbn.go` was touched by 3 of the last 5 commits on main — rebase carefully |
| CI-01 deploy guard passes with unpushed commits | TASK-311 | **CONFIRMED** | the committed `Makefile` has no deploy targets; every `Makefile.local` is copied from the example, so the bug is real in practice |
| DB-03 `migration014UpPebble` never wired | TASK-315 | **CONFIRMED** | writes `LibraryState` — the field behind two live landmines; prefer the audit-first path (confirm zero rows have `{`, delete the dead code) |
| SV-01 DELETE history hits the dead v1 keyspace | TASK-319 | **CONFIRMED** | zero UI callers today; a landmine, not an active bug |

10 of 11 confirmed exactly, 1 narrowed, 0 refuted. The severity ranking in the matrix stands.

### 2.2 The briefs had three systemic defects — all fixed in the generator

Brief-verifier ran six briefs cold and failed every one on the same checks:

| Defect | Effect on a cold executor | Fix (in `state/tools/gen_new_package.py` 1.1.0) |
|---|---|---|
| `REPO=/path/to/audiobook-organizer` placeholder in every ⛔ START HERE block | cannot start | real primary-checkout path, same convention as the 111 carried briefs |
| Review-critical boilerplate demanded a dry-run/`apply=false` surface and a "dry-run writes nothing" test even for a lock, a bound, or a propagated error | invents an `apply` parameter nobody asked for (TASK-300), or reports the brief unsatisfiable | a decidable rule: *does the fix add or change a write path?* NO → pure code change, `git revert`, presence grep; YES → the full protocol |
| Re-verify `sed` window covered only the audit's first anchor line | fixes 1 of 2 call sites (SF-02: `:1705` but not `:1714`), 2 of 4 (SF-03), 1 of 2 branches (SF-01) | `anchor_windows()` parses every `file:N[-M]` and "lines a, b, c" reference in the evidence and emits one window per cluster — TASK-309 now shows 6 windows, TASK-310 both functions |

Plus the TASK-337 REWRITE: "Fix **or** unregister `fix-library-states`" is two fixes with different blast radii and an existing test asserting the job IS registered. The brief (now TASK-338) carries the decision: **unregister**, update `TestFixLibraryStatesJob_Registered`, never run the job.

### 2.3 The TODO-section briefs were built on a wrong section field — regrouped from the document itself

The wave-1 inventory tracked only `## ` headings, so ~50 items were filed under the wrong section. Consequences the auditors found: 11 of 30 TODO briefs grepped a *different* paragraph than their title named; 7 briefs took an unrelated `##` heading (`recoverPebbleClosed …`) as their title; 8 briefs bundled items that are owner decisions or prod runs.

Fix: the generator now walks `TODO.md` itself for the real enclosing heading (climbing to the parent when the nearest heading is terse, e.g. `### Fix`), treats every bold-named item (`**NAME** …`) as its own unit of work, never merges items with different dispatch verdicts, and anchors each item by its **actual text at HEAD** instead of the inventory's `text_head`. Result: 30 → **41** TODO-section briefs, every title a real name.

### 2.4 Dispatch verdicts — 13 briefs are not worker tasks

Explore judged every TODO-section item's *shape*. The generator stamps the verdict on the brief, the BREAKDOWN, and the matrix (`GATED`):

| Verdict | Count | Briefs | Why |
|---|---|---|---|
| **HOLD-FOR-OWNER** | 13 | TASK-341, 349, 350, 353, 355, 356, 357, 369, 370, 371, 372, 374, 375 | the item's own text says *decide* / *design pending* / *run in prod* / *redeploy*; TASK-350 also collides with the scan and book_file bans; TASK-369–372 are Cloudflare/systemd/secret-rotation runbook items, not repo code |
| **RECLASSIFY** → correctness, standard lane | 3 | TASK-339, 343, 351 | "counted, never touched" reporting gaps and a pipeline-completeness feature — not data-loss |
| **DISPATCH** | 25 | the rest | with per-item caveats carried in the brief (e.g. TASK-354's L4244 stops at measurement; TASK-362's headline edit belongs to the coordinator) |

Held briefs stay in the package and the matrix so the cut line is complete, but each says **Do NOT dispatch this brief to a worker** under its title.

### 2.5 Mechanical audit of the package

| Check | Result | Action |
|---|---|---|
| Header lint, 219 files | 0 failures, 0 path mismatches | — |
| Guid uniqueness | 111 carried briefs shared their archive twin's guid | generator now mints `uuid5(path)` for every copy — stable across regeneration, distinct from the archive; 0 collisions between `.md` files now (one pre-existing duplicate inside the archive itself is untouched) |
| 65 new-brief anchors | 1 missing file (TASK-339's `go-sanitizers.model.yml` — that brief is now held), 1 `test -f` on a directory, 1 zero-hit grep, 11 wrong-paragraph greps | `test -e`; real-text anchors (§2.3) |
| 111 carried re-verify greps | 106 still open, **0 look done**, 5 stale anchor strings (TASK-071, 129, 189, 192, 193 — symbol renamed or moved) | each carries a `⚠️ Anchor drift` line under its Status; bodies unchanged |
| Same-file collisions in §A rows 1–53 | 3 pairs in the same wave (`author_strip_merge.go` ×3, `pebble_store.go` ×2, `internal/backup/` ×2) | per-workstream waves are now collision-aware (earliest wave with no shared file); two of the three dissolve because their members are held |
| Matrix anchors | rows for TASK-140 and TASK-040 pointed at the undated old path | matrix now reads `state/final/brief_index.json`; every row has a `Brief` column |
| Section structure | 187/187 briefs have all 11 sections | — |

## 3. The package after regeneration

**187 briefs** = 111 carried (ids kept) + 35 audit findings (TASK-300–334) + 41 TODO-section (TASK-335–375). 13 held, 3 reclassified, **171 dispatchable** (111 + 35 + 25). Matrix: 303 rows — 124 brief, 35 finding, 144 todo-section; by risk data-loss 43 · security 18 · correctness 114 · perf 28 · ux 10 · hygiene 90; by effort S 150 · M 117 · L 36.

Gates: TODO lint exit 0; `git diff --stat main -- internal cmd web` empty; 0 header path mismatches; 0 public-repo patterns in added lines. The banned word appears in 18 added lines, all verbatim copies of 2026-08-21 brief bodies (61 files on `main` contain it) and pre-existing `TODO.md` lines — nothing written in this pass uses it.

## 4. Residual risk the auditors could not close

- **35 UNCLEAR TODO items and 1 UNCLEAR brief** need a prod observation or a live run to classify; none were probed (standing bans).
- **TASK-192/193** carry a Status line claiming the confidence overrides are "already wired"; the plan-auditor found no function definition and no call site — re-derive before executing.
- **DA-02's fix shape** (widen `handler.go`'s dependency vs. expose journaling on `merge.Service`) is a design call the worker will otherwise make alone; decide it when approving.
- **`dedup-pipeline-hardening/TASK-06`** (prod drain, ~387k candidates) ranks at §A row 15 and is dispatchable by the matrix's rules but is an owner-run op, not a worker task.
- The audit findings' severities are the audit agents' own; the adversarial pass confirmed the top 11, not all 35.

## 5. Recommendation — cut line and waves

**Cut line: PRIORITY-MATRIX §A rows 1–64** — every data-loss and security row (1–61) plus the three critical/high correctness findings SF-02, SF-03, CI-01 (62–64). That is the ORCHESTRATION default and it is where the adversarial coverage ends.

Inside the cut: 64 rows, **15 gated** (13 held TODO briefs + 2 torrent-relocation siblings), **49 dispatchable**: 12 findings, 12 carried briefs, 25 TODO-section briefs; by effort S 17 · M 22 · L 10. One of the S rows (`dedup-pipeline-hardening/TASK-06`) is an owner run.

Proposed dispatch, 4 workers max, review-critical PRs held for the owner:

| Wave | Briefs (all effort S unless marked) | Why this grouping |
|---|---|---|
| 1 | TASK-300 (DA-01), TASK-302 (DB-01), TASK-303 (SF-01, narrowed), TASK-306 (SV-02) | four confirmed data-loss findings, four different packages, no shared files, each a one-day pure code change |
| 2 | TASK-309 (SF-02), TASK-310 (SF-03), TASK-311 (CI-01), TASK-304 (WEB-04) | the three critical/high correctness findings + the web data-loss finding; scanner / metafetch / Makefile example / web — disjoint |
| 3 | TASK-360 (orphan-files hard delete fail-open), TASK-363 (purge-empty-authors safety counter, M), TASK-354 (duplicate FilePath in batch), TASK-338 (unregister fix-library-states) | the four highest-severity TODO data-loss briefs; TASK-363 must not share a wave with TASK-302 (same `author_bookref.go` family) — it does not |
| 4 | TASK-307 (CI-02), TASK-308 (SV-03), TASK-352, TASK-362 | remaining S-effort security + data-loss briefs |
| 5+ | the M/L rows in matrix order: TASK-301 (DA-02, after the interface decision), TASK-305 (DB-02, option a), TASK-140, TASK-072, TASK-220, TASK-337, TASK-340, TASK-344, TASK-346, TASK-347, TASK-358, TASK-359, TASK-315 (DB-03, audit-first), TASK-319, then security M rows | per-workstream `orchestration.md` waves already avoid same-file collisions inside a workstream; the coordinator checks across workstreams with `state/final/brief_index.json` |

**Decisions the owner should make when approving** (each is one line in a reply):

1. Accept the cut (rows 1–64) or name another row number.
2. SF-01: accept the narrowing to `:141-142` (recommended).
3. DA-02: widen `handler.go`'s store dependency to the Engine, or add journaling to `merge.Service`.
4. DB-03: audit-first (recommended) or wire the migration.
5. DB-02: option (a) idempotency rule + lint (recommended) or the shared-batch refactor.
6. The 13 held briefs: answer the decision each one asks, or leave them held.

Nothing executes until that reply. The package regenerates from `state/` in one command chain (`gen_new_package.py` → `build_matrix.py` → `build_reconciliation.py`); never hand-edit a brief or a table.
