<!-- file: docs/audits/2026-08-11-docs-inventory.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4d1c8a72-3e6b-4f5a-9c28-7b0d1e4f6a93 -->
<!-- last-edited: 2026-08-12 -->

# Docs inventory and consolidation — 2026-08-11

**Question asked:** the `docs/` tree looks bloated; completed task packages were probably
never archived. **Answer: the second half is wrong, and that matters** — the archiving
discipline *was* followed. The bloat is somewhere else, and the indexes lie about what exists.

---

## 1. Headline findings

### 1.1 Nothing in `docs/agent-tasks/` is archive-ready

All **10** live packages verified ACTIVE or PARTIAL against code artifacts at HEAD.
`docs/archive/agent-tasks/` already contains exactly the 4 workstreams that finished.

Method: a ✅ or "shipped" line inside a brief was **not** accepted as evidence. For each
brief, the concrete artifact it promised was identified and grepped for at HEAD. `git log`
was deliberately **not** used to prove a PR landed — this repo rebase-merges and rewrites
SHAs, so commit archaeology is unreliable here; the artifact is the evidence.

| Package | Verdict | Briefs | Blocked on |
|---|---|---|---|
| `abs-sync` | ACTIVE | 10/10 written (index claims 12; TASK-11/12 unwritten) | 9 live TODO items; TASK-12's identity gaps absent — `grep -c RepointSync internal/dedup/book_dedup.go internal/scanner/scanner.go` → **0 and 0**, while `book_dedup.go:395 MergeBooks` hard-deletes |
| `bug-techdebt` | ACTIVE | 5/7 | TASK-01's required warn-log absent; TASK-02's acceptance is `staticcheck` exit 0 but HEAD exits **1** (5 findings, all in `_test.go` touched *after* the brief — drift, not backlog) |
| `dedup-pipeline-hardening` | **PARTIAL** | 5/5 code + 1 operational | ~~one contradictory line~~ resolved 2026-08-12 (§1.3): the prod run **did** happen. Now blocked only on T03 (sandbox purge wave) + T13 |
| `error-correction-2026-07` | ACTIVE | 10/13 inline in `TASKS.md` | T03, T04, T13 — the only genuine unchecked boxes in the directory |
| `ux-small-items` | ACTIVE | 4 + 1 partial + 1 N/A / 8 | TASK-05, TASK-08 have zero implementation |
| `torrent-relocation` | ACTIVE | 1/7 | TASK-02's STOP-FOR-HUMAN Deluge spike never opened |
| `ai-responses-migration` | ACTIVE | 0/5 | explicit do-not-start hold |
| `responses-api-migration` | ACTIVE | hold doc only | hold never lifted |
| `community-fingerprint-index` | ACTIVE | gate file only | spec still `Draft — STOP-FOR-HUMAN` |
| `workflow-system` | ACTIVE | gate file only | superseded by a newer owner-approved plan the gate file never points at |

### 1.2 The real bloat: `docs/superpowers/fleet-*`

**56 of the 65 superseded files were one thing** — 28 DONE brief/status pairs. Link-isolated
(nothing outside those two directories referenced them) and header-less (76 of 77 carried no
`<!-- file: -->` header), which made this both the highest-yield and lowest-risk move
available. **Archived** to `docs/archive/superpowers/fleet-done/`.

### 1.3 Two bookkeeping contradictions — ✅ RESOLVED 2026-08-12

> **Update (2026-08-12).** Settled against production. **The status docs were right and
> `TODO.md` was wrong.** Details and correction in §1.3.1 below. One of the two "contradictions"
> turned out not to be one at all — see the 7,878/7,891 correction.

The same event was recorded both ways:

| Said NOT done — **wrong** | Said done — **correct** |
|---|---|
| `TODO.md:4988` — prod drain "code merged, run NOT executed; operator-gated" | `docs/operations/pending-prod-actions.md:26` — "**EXECUTED ON PROD 2026-07-18**", exact-pending 9,074→1,311 |
| `TODO.md:5311` — T04 prod deploy `- [ ]`, "nothing deployed since 2026-07-17" | `docs/dedup/STATUS.md:78-86` — "EXECUTED ON PRODUCTION 2026-07-18" |

#### 1.3.1 What production actually says

Neither doc was treated as evidence. The production host's journal still carries the run
(journald holds 2.2 GB there; 2026-07-18 is within retention):

```
Jul 18 12:48:54  registry: starting run  op_id=01KXV22ZJ6QWWZ1SF1FZGXBC82
                 def_id=maintenance.dedup-exact-triage
Jul 18 12:48:58  dedup triage: complete  scanned=10319 purgeable=7891 keep=278 review=2150
                 lookup_errors=0 apply=true dismissed=7891 dismiss_errors=0
Jul 18 12:48:58  operation finished  outcome=completed duration_ms=3860
```

`apply=true` and `dismissed=7891` — a real apply, not a dry-run, matching `STATUS.md` exactly.
⇒ **`TODO.md` entries 1 and T04 were stale. Corrected.**

#### 1.3.2 The "purgeable drift" was not a drift — this audit got it wrong

The first version of this document reported **7,878** (`TODO.md:5304`) vs **7,891**
(`STATUS.md:68`) as an unexplained inconsistency. It is not one:

| Figure | Population | Scanned | Source |
|---|---|---|---|
| 7,878 | **dedup sandbox** | 10,304 | `TODO.md` T02 |
| 7,891 | **production** | 10,319 | prod journal + `STATUS.md` |

Two different datasets — the sandbox replica held 15 fewer candidates. **Both numbers are
correct.** They were only ever comparable-looking because neither line said which population it
described; both now do. The lesson is the audit's own: two numbers that differ are not
automatically in conflict — establish they measure the same thing before calling it a
contradiction.

#### 1.3.3 New finding: the backlog has re-accumulated

Measured on prod 2026-08-12 via `GET /api/v1/dedup/stats`:

| | post-run 2026-07-18 | 2026-08-12 |
|---|---|---|
| exact **pending** | 1,311 | **5,947** |
| exact dismissed | 9,242 | 8,258 |

The drain did what it claimed. But pending has regrown **~4.5×** in 3.5 weeks, so whatever
emits the junk candidates was never addressed — this needs a *source* fix, not a second drain.
Filed as a `todo.d` fragment.

⇒ `dedup-pipeline-hardening` is no longer blocked by a bookkeeping contradiction. Its remaining
gap is T03 (the **sandbox** purge wave, still unrun) and T13.

### 1.4 Two files were corrupt at HEAD — fixed

`docs/agent-tasks/ai-responses-migration/README.md` and `orchestration.md` were committed
**JSON-string-encoded**: each began with a literal `"` and contained literal two-character
`\n` sequences with no line terminators (`wc -l` = 0). Decoded to real Markdown (50 and 70
lines) in this pass.

### 1.5 `run-sweep.sh` silently cannot drive 4 of the 10 packages

It discovers work via `find -maxdepth 1 -name 'TASK-*.md'`. Four packages contain no such
files — they use `AWAIT-APPROVAL.md`, `HOLD-STATUS.md`, or `TASKS.md` with 13 inline tasks.
For those it creates no worktrees and emits no prompts. **A silent no-op, not an error** —
the failure mode is indistinguishable from "nothing to do." Not fixed here; needs a decision
about whether the script should hard-fail on a package it cannot parse.

### 1.6 `docs/openapi.yaml` vs `docs/api/openapi.json` — do NOT pick a winner

These are **not** duplicates. They are two independently hand-maintained specs, both
incomplete, and **neither is generated** — no reference to either file exists in `Makefile`,
`scripts/`, `.github/workflows/`, or any `.go` file.

After normalizing the `/api/v1` prefix: the JSON has **117 paths the YAML lacks**, but the
YAML has **25 paths the JSON lacks** — including `/auth/login|logout|me|sessions*` and
`/ai/scans*`. Versions disagree too (JSON `0.215.0`, YAML `2.1.0`).

⇒ Consolidation must be a **union onto `docs/api/openapi.json`**, carrying the 25 YAML-only
paths across. **Deferred** — it is a content merge, not a file move, and doing it blind would
lose real API surface.

---

## 2. Classification totals

177 files classified across 8 subtrees.

| Bucket | Count |
|---|---|
| CURRENT (keep in place) | 100 |
| SUPERSEDED (archive) | 65 |
| DUPLICATE (merge) | 1 |
| UNCERTAIN (needs a decision) | 11 |

**There is no DELETE bucket.** Everything moved is under `docs/archive/`, recoverable.

### Two rules that overrode the age heuristic

1. **A doc cited by an open `docs/plans/DECISIONS-PENDING.md` row is CURRENT.** Six plans
   dated 2026-07-10/12/13 and marked "Draft" or "BLOCKED" look archivable by every surface
   signal, but they are the source documents of the owner's own STOP-FOR-HUMAN queue.
   Archiving them would have broken that queue.
2. **An audit or spec cited from live `internal/*.go` is CURRENT** — it is the rationale for
   code at HEAD. This kept 7 docs out of SUPERSEDED, notably
   `audits/2026-07-05-updatebookfile-…` (9 call sites) and
   `specs/2026-07-29-abs-sync-api-design.md` (11).

---

## 3. What was actually changed in this pass

### Archived (67 files, all via `git mv`)

- **56** — the 28 DONE fleet pairs → `docs/archive/superpowers/fleet-done/{tasks,status}/`
- **11** — superseded singles:
  `technical_design.md` (2026-01-19, describes a "CLI + lightweight HTTP API" that no longer
  matches HEAD) · `implementation-guide.md` (a stale mirror of TODO.md, frozen 2026-03-19) ·
  `itunes-sync-diagnostic-suite.md` · `slog-prod-verify.md` (see below) ·
  `plans/2026-07-29-abs-sync-phase0-oracle.md` (self-stamped DONE) ·
  `status/2026-07-11-remaining-work-execution.md` · `research/2026-05-11-embedded-db-migration.md` ·
  `research/sql-database-analysis.md` · `research/2026-06-15-config-architecture-evaluation.md` ·
  `audits/2026-05-01-structure-audit.md` ·
  `audits/2026-07-07-dedup-fullscan-composing-scores-writestall.md`

### Merged

`docs/slog-prod-verify.md` and `docs/operations/slog-prod-verify.md` shared a name and a topic
but were **entirely different text** — two independently written runbooks, not near-copies.
Canonical is now `docs/operations/slog-prod-verify.md` (the one cited by TODO #28 and
`pending-prod-actions.md`); the top-level copy's unique **verification checklist** and
redeploy note were folded in, and it gained the version header it never had.

### Links repointed (9 live files)

Moving files broke real references. All fixed; references *inside* `docs/archive/` were
deliberately left alone as historical record.

Two were genuine breakage, not cosmetics:

- `docs/agent-tasks/ux-small-items/TASK-08-slog-prod-verify.md:46` contains
  `test -f docs/slog-prod-verify.md && echo PROCEDURE-OK  # must print PROCEDURE-OK` — an
  **acceptance check** that the move would have silently failed.
- `README.md:264` and `.github/README.md:256` linked `docs/technical_design.md` from the
  repo's front door.

### Headers

- 8 moved files had their `<!-- file: -->` path rewritten to the new location, with a patch
  version bump and `last-edited` stamp. Each rewrite was anchored **per-file** — an
  unanchored `gsed` across the tree is what leaked 74 headers into `TODO.md` previously.
- 4 CURRENT files had stale header paths fixed (`docs/BUILD.md`, `BUILD_TAGS_GUIDE.md`,
  `CODING_STANDARDS.md`, `MOCKERY_GUIDE.md` all said `<basename>.md` instead of
  `docs/<basename>.md`).

### Indexes rewritten

`docs/agent-tasks/README.md` listed **9 archived folders and only 1 of the 10 packages that
actually live in that directory**. Replaced with a verified live table plus the archived set,
and the `run-sweep.sh` limitation documented inline.

---

## 4. Not done — explicitly

| Item | Why | Who decides |
|---|---|---|
| **11 UNCERTAIN files** left in place | archiving on a guess is worse than leaving them | owner |
| **`openapi.yaml` / `openapi.json` union merge** | §1.6 — a content merge that would lose 25 paths if done as a pick-a-winner | needs a session of its own |
| ~~**The two bookkeeping contradictions**~~ | ✅ **RESOLVED 2026-08-12** against the prod journal (§1.3): the run happened, `TODO.md` was stale, and the 7,878/7,891 "drift" was sandbox-vs-prod, not a conflict | — |
| **`docs/system/**` (9) and `docs/architecture/**` (9)** | out of the classification scope, but **required** to settle the top-level-vs-`docs/system/` duplicate cluster | follow-up pass |
| **78 remaining missing headers** | 76 of them were the fleet files (now archived, still header-less); the rest are CURRENT files that need headers written, not fixed | follow-up |
| **`run-sweep.sh` silent no-op** | §1.5 — needs a behaviour decision, not just a doc note | owner |

### The 11 UNCERTAIN files

`docs/itunes-flow-diagrams.md` (1446 lines, zero inbound) · `docs/openapi.yaml` ·
`plans/2026-07-10-ux-small-items.md` · `plans/2026-07-12-dedup-clean-remeasurement-runbook.md` ·
`plans/2026-08-06-series-embedded-positions-plan.md` · `specs/2026-07-10-ux-small-items-design.md` ·
`specs/2026-07-25-itunes-2way-sync-phase2-metadata-design.md` ·
`research/2026-06-15-sonarr-radarr-advanced-settings.md` · `consultancy/10-V2-REEVALUATION.md` ·
`audits/2026-06-22-repo-optimization-security-sweep.md`

---

## Related

- [`docs/reference/abs-target-client-contract.md`](../reference/abs-target-client-contract.md)
- [`docs/reference/abs-upstream-api-reference.md`](../reference/abs-upstream-api-reference.md)
- [`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](2026-08-11-abs-coverage-gap-audit.md)
