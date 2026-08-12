<!-- file: docs/audits/2026-08-12-docs-consolidation-wave-2.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e2b8f47-1a93-4c60-b7d8-9f3e0a25c841 -->
<!-- last-edited: 2026-08-12 -->

# Docs consolidation, wave 2 — 2026-08-12

Closes the four items [the 2026-08-11 inventory](2026-08-11-docs-inventory.md) §4 explicitly
deferred: the 11 UNCERTAIN docs, `docs/system/**` + `docs/architecture/**` classification, the
openapi union merge (shipped separately), and `run-sweep.sh` (shipped separately).

**8 files archived. 2 factual errors found in live docs and fixed. 1 file left UNCERTAIN on
purpose.**

---

## 1. The finding that mattered most: a document that indexes a codebase that no longer exists

`docs/itunes-flow-diagrams.md` — 1,446 lines, zero real inbound references — was archived not
because it was stale but because **its content is wrong in a way that would actively mislead**.
Every file it indexes is gone:

| Cited by the doc | At HEAD |
|---|---|
| `internal/server/organize_service.go` | **absent** |
| `internal/server/merge_service.go` | **absent** |
| `internal/database/sqlite_store.go` | **absent** (no sqlite dep in `go.mod`) |
| `internal/server/metadata_fetch_service.go` | **absent** |
| `internal/server/itunes.go` | **absent** → `internal/server/handlers/itunes.go` |
| `internal/server/server.go` "~9000+ lines" | **1,091 lines** |

Every `file:line` anchor it offers (`server.go:3215`, `:6062`, `itunes.go:280`…) is dead.

**It also contains a leaked agent transcript.** Lines ~1408–1416 are the authoring agent's own
reasoning, committed verbatim as document body:

> *"Actually, I realize I cannot directly write files with the available tools in this agent
> environment…"*
> *"Since I cannot write files directly, I will present the complete file content in the
> response for the user to…"*

The document is, literally, an agent explaining that it could not write the document. That is
worth recording because it is a *detectable* class of defect: a doc whose body contains
first-person tool-availability commentary was never reviewed by a human, and its factual claims
should be treated as unverified regardless of how confident they read.

---

## 2. Archived (8 files, 7,455 lines)

All via `git mv` to `docs/archive/2026-08-consolidation-wave2/`. **Nothing deleted.**

| File | Lines | Why |
|---|---|---|
| `itunes-flow-diagrams.md` | 1,446 | §1 above |
| `architecture/metadata-cached-matcher-plan.md` | 2,167 | Feature shipped — `listCachedCandidates` live in `web/src/services/api.ts`, `web/src/pages/Library.tsx`, `internal/server/wire_library_routes.go`. Zero inbound. |
| `architecture/2026-06-01-handler-extraction-phase1.md` | 1,600 | Every file-map row satisfied at HEAD, **including both `Delete` rows** — `internal/server/auth_handlers.go` and `apikey_handlers.go` are absent, `handlers/auth.go`, `handlers/apikeys.go`, `wire_handlers.go` exist. Zero inbound. |
| `architecture/server-plugin-registry-plan.md` | 1,530 | Delivered: `internal/serviceregistry/` is live with 25 `register.go` files. Both live citers describe it in the past tense as "original 7-wave plan"; residual scope migrated into `server-plugin-registry-deferred-work.md`, which stays. |
| `architecture/server-plugin-registry-handoff.md` | 277 | Navigation stub whose only content is a 3-line companion list already reproduced in `deferred-work.md`. Zero inbound. |
| `architecture/metadata-cached-matcher-design.md` | 250 | Same feature as its plan. Its own header points at `docs/superpowers/specs/2026-05-13-metadata-cached-matcher-design.md`, which **does not exist** — a stale header, not a duplicate. |
| `plans/2026-08-06-series-embedded-positions-plan.md` | 103 | Steps 1–4 shipped and step 6 **ran on prod** (`TODO.md:3371` — 25 series merged, 52 books positioned, 0 failures). Its sibling *design* doc stays: it is cited from live Go at `series_denumber.go:45`. |
| `research/2026-06-15-sonarr-radarr-advanced-settings.md` | 82 | Subject consumed — `useAdvancedSettings.ts`, `ToolsPanel.tsx`, `ToolsSettingsTab.tsx` all shipped. Matches this consolidation's own precedent for same-dated consumed research. |

### One live link repointed

`docs/architecture/server-plugin-registry-deferred-work.md:20` referenced the now-archived plan.
Repointed. `CHANGELOG.md` references were left alone as historical record, per the wave-1 rule.

---

## 3. Two factual errors found in docs that STAY

Archiving was the smaller half of this pass. Two live documents assert things that are false at
HEAD.

### 3.1 🔴 `docs/system/storage.md` documented the wrong database schema — fixed

It asserted *"IDs are ULIDs (26-char Crockford base32, time-sortable)"* and listed abbreviated
key prefixes (`a:`, `b:`, `s:`). **Production uses integer IDs and spelled-out prefixes.**
Verified at HEAD: `internal/database/memdb_warmup.go:69` warms `"book:"`, `:86` warms
`"author:"`, and `grep '"a:"\|"b:"' internal/database/*.go` returns nothing.

The mechanism is worth noting because it is a general hazard: the table was **copied from
[`docs/database-pebble-schema.md`](../database-pebble-schema.md) without that document's
correction note.** The source says the ULID design *"was never implemented for core entities…
production code uses integer IDs"*; the copy took the design and dropped the caveat. A partial
copy of a corrected document can be more dangerous than no copy, because it launders a known-bad
design back into circulation as fact.

Fixed with an inline correction rather than a rewrite, since the table is still useful as a
topical summary.

### 3.2 🟠 SEC-9 is still live — browser-side OpenAI key exposure

Surfaced while assessing `docs/audits/2026-06-22-repo-optimization-security-sweep.md` (which is
**kept** — see §4). `web/src/components/wizard/WelcomeWizard.tsx:147-160` still performs:

```js
fetch('https://api.openai.com/v1/models', { Authorization: `Bearer ${openaiKey}` })
```

…from the browser. That is the exact exposure the 2026-06-22 audit flagged as SEC-9, unfixed
seven weeks later. **Not fixed here** — it is frontend work outside a docs pass, and filed
instead.

---

## 4. Kept, with the evidence (so this doesn't get re-litigated)

| File | Why it stays |
|---|---|
| `plans/2026-07-10-ux-small-items.md` + `specs/…-ux-small-items-design.md` | Shared fate. Package README declares the plan **authoritative**; TASK-05 has zero implementation, TASK-08 is open at `pending-prod-actions.md:29`. |
| `plans/2026-07-12-dedup-clean-remeasurement-runbook.md` | **Step 5 never ran.** Its findings note recommends band minimums 96 / 85.5, but `internal/dedup/unified/config.go:68-69` still reads `BandCertainMin: 97.0, BandHighMin: 90.0`. |
| `specs/2026-07-25-itunes-2way-sync-phase2-metadata-design.md` | Phase 2 unshipped: `relocate_sync_cycle.go` touches only `ops.LocationUpdates`, and `ops.MetadataUpdates` is never populated from the sync cycle. |
| `consultancy/10-V2-REEVALUATION.md` | Sole record of which consultancy IDs survived the 31-task roadmap — **none of them appear in `TODO.md`**. Its UI findings are measurably *worse* now: `Library.tsx` 2,075 → 2,322 lines, `api.ts` 5,766 → 6,035, still zero list virtualization. |
| `audits/2026-06-22-repo-optimization-security-sweep.md` | A **live** backlog: drawn down by `changelog.d/20260810_213500_make_test_everything.md:22` one day before it was classified. 40 of its 41 finding IDs are tracked nowhere else. |
| all 9 `docs/system/*.md` | Every one has ≥3 live inbound refs and all nine are named as the target of **open issue #1276** via `agent-tasks/ux-small-items/TASK-06:25`. `docs/system/runbooks.md` is cited from *shipped deploy config* (`deploy/prometheus/*`). |
| `architecture/server-plugin-registry-design.md` | Authoritative spec for `internal/serviceregistry/`, live and load-bearing (26 files call `serviceregistry.Register`). |
| `architecture/server-plugin-registry-deferred-work.md` | Ticket **W7.1 NEWSERVER-TRIM** measurably open: target was "`NewServer` becomes ~25 lines", actual is **576**. |
| `architecture/embedding-store-db-selection.md` | Zero inbound, but the ADR's decision is in force — `internal/database/embedding_store.go:19` imports Pebble directly. It is the only written rationale for that choice. |

---

## 5. The top-level ↔ `docs/system/` "duplicate cluster" was mostly not duplicates

The inventory suspected a duplicate cluster. Content was diffed, not just names. **Only one of
four topical pairs is a real subset relationship:**

| Pair | Verdict |
|---|---|
| `docs/architecture.md` (70 L) ↔ `system/architecture.md` (124 L) | **Different documents.** `comm -12` on sorted unique lines returns **zero** shared non-blank lines. The two-tier split is *intentional* and declared at `docs/architecture.md:8-10`. |
| `docs/AI-REFERENCE.md` ↔ `system/api.md` + `system/components.md` | **Different.** Route sets share **11 of 137** tuples (8%): 96 AI-REFERENCE-only, 30 api.md-only. Archiving either loses 96 or 30 documented routes. |
| `docs/database-architecture.md` ↔ `system/storage.md` | **Different.** They document two *non-overlapping* key schemas — and `database-architecture.md`'s is the one matching shipped code. |
| `docs/developer-guide.md` ↔ `system/components.md` | **Stale subset** (18 packages vs ~70) but not archivable — its Getting Started / Testing Gotchas / Common Tasks sections have no counterpart anywhere. |

This is the `slog-prod-verify.md` lesson holding a second time: **same name or same topic is not
evidence of duplication.** Three of four pairs would have lost real content had they been
consolidated on the strength of their titles.

---

## 6. Corrections to the wave-1 inventory's own arithmetic

- §2 says **11** UNCERTAIN; §4 enumerates **10**. The file has a single commit in its history, so
  no name was edited out — it shipped inconsistent. **There is no 11th file.**
- §3 archives 56 + 11 = **67**, but §2's buckets give SUPERSEDED 65 + DUPLICATE 1 = **66**.

Neither changes any decision, but a document whose totals don't reconcile invites exactly the
kind of "which number is real?" archaeology this consolidation exists to end.

---

## 7. Still open

| Item | Who |
|---|---|
| `architecture/2026-06-01-server-handler-extraction-design.md` — **left UNCERTAIN deliberately.** Its pattern is live (22 files in `handlers/`), but its stated scope of "24 handler files in 4 phases" is unfinished: 5 legacy `*_handlers.go` remain. No tracker cites it. | owner — are phases 2–4 still intended? |
| **SEC-9** browser-side OpenAI key exposure (§3.2) | frontend fix |
| The 2026-06-22 security sweep needs a **status column**, not a decision — 40 of 41 findings are in an unknown state | follow-up pass |
| `docs/openapi.yaml` merge direction — resolved separately; the `api-doc` skill was repointed in the same change so the merge cannot silently undo itself | done |
