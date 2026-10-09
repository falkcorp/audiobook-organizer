<!-- file: docs/proposals/2026-10-holistic/02-filter-identification-pipeline/C-goal-bucket-map.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f4c2b61-9a3d-4e07-b5d8-2c6e1a7f9d42 -->
<!-- last-edited: 2026-10-08 -->

# Appendix C: missing-metadata buckets mapped to pipeline stages

The goal is fewer than 1,000 primary books with no fetched candidate and no applied metadata.
Fetched-but-unapproved books do not count as missing, an ASIN alone does not count, and the
excluded franchise (about 281 books) is not counted.

Today's number, from the task brief, is about 11,233. The bucket sizes below come from the
dated census notes of 2026-10-05 and 2026-10-06. They were **not measured at HEAD**, and the
buckets overlap.

| Bucket (dated size) | What it is | Stage that can move it | Proposal | Expected direction |
|---|---|---|---|---|
| Chapter fragments, ~4,700 | One-file book rows that belong to a bigger book | **Grouping**, not identification. No query or scoring fix helps. | PR 8: window-print containment as fragment-to-parent evidence, feeding the existing fragment fixer (owner approval by id stays) | The largest lever, gated on owner-approved consolidation lists |
| No cache row, 3,386 (10-05) | Never fetched, or candidates deleted by a retitle or relink | **Orchestration** and **cache policy** | The scheduled `candidate_fetch` (already shipped) drains the never-fetched. PR 5 stops new losses. PR 14 replaces the 6 h poll with a dirty set. | Should fall close to the overlap with the other buckets once a fetch pass completes |
| Author equals title, ~1,272 | The stored title is the author's name | **Question** construction | The folder-parse variant when title equals author (spec §3.3 Q); PR 7 catalog block on the author credit | Recall gain, size unknown until measured |
| Literal, series-decorated titles the providers cannot answer (part of ~8.9k zero-candidate) | "Series, Book NN - Name"-shaped titles | **Blocking** | PR 7 local catalog (0 calls); PR 6 shared cache makes re-asks free | Bounded by catalog coverage of these authors (Q3: measure first) |
| Not on Audible, ~930 | Audible has no record | **Provider coverage** | Open Library and Google are review-only (owner 10-06); the quota planner spends Google's 1,000/day on the highest expected gain | About 400/day through Google at most (09-05 note) |
| Retitled by scan (1,180 on 10-06, mostly re-fetched) | Lost candidates | **Cache policy** | PR 5 (stale, not deleted) plus P1 rescoring with no calls | Prevents recurrence |

**What does not move the number:** a better auto-apply gate (PR 10), and better scoring for
books that already have candidates. Both matter for correctness and for how much review work
the owner faces, but fetched-unapproved books already count as not missing.

**How to measure progress:** PR 4 adds a count for each value of `ident:`. The number to
watch is `ident:unasked` + `ident:asked_empty` among primary books, minus the excluded
franchise and the books held for owner review, clickable from the Library.
