<!-- file: docs/decisions/2026-10-30-narrator-fold-match.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92bfe06a-0fa3-461c-be3d-cd38cd6e1432 -->
<!-- last-edited: 2026-10-30 -->

# Narrator sync fold-matches existing records (NARRATOR-SYNC-FOLD-MATCH)

## Decision log

### Chosen

- **A `narrator_fold:<fold>:<id>` index next to `narrator_name:`**, the TODO's recommended option, rather than a `ListNarrators` scan per sync. One empty-valued key per (fold, narrator id): no read-modify-write of an id list, a delete that can only remove its own key (no ownership check, unlike `narrator_name:`), and a reconcile that is just "set the keys that are absent". `fold` is `util.FoldKey`, letters and digits only, so the `:` ends it and `narrator_fold:abc:` cannot reach `abcd`. A name that folds to nothing is not indexed and never fold-matches. The family sorts after `narrator:` (`_` > `;`), so `ListNarrators` and memdb warmup do not walk it. Registered in `keyfamilies.go`.
- **`FoldKey` moved to `internal/util`; `authorjunk.FoldKey` now delegates to it.** `internal/database` is layer 2 and `internal/authorjunk` computes to layer 3, so importing it from the store is a wrong-way edge that `TestLayering` rejects. `util` is the layer-0 leaf that already holds `NormalizeAuthor`, the key of the exact index. One implementation, all 86 existing `authorjunk.FoldKey` callers unchanged.
- **The fold check lives where the create decision is made.** `resolveNarratorCredit` calls a new `PebbleStore.resolveNarratorFolded`: lock-free exact lookup, then lock-free fold lookup; on a miss it takes `nameIdx.narrator`, re-checks both, and only then mints the record (`mintNarratorLocked`, the body `CreateNarrator` used to hold). Without the locked re-check two goroutines resolving "Renée Montoya" and "Renee Montoya" on an empty store would still mint two records, which is the case the TODO says needs its own test (`TestNarratorFold_ConcurrentSpellingsMintOneRecord`, run under `-race`). If the re-check finds a match the lock is dropped and the match is resolved outside it, so tie-breaking (a junction scan) never runs under the lock.
- **`CreateNarrator` keeps its exact-name semantics** for its other callers (the narrators API, `credits_backfill`, `narrator_split_joined`, `narrator_purge_empty`), which mean "this spelling". It only gained the fold key in its existing batch.
- **Resolution order:** exact normalized name; else the single fold match (the record keeps its spelling); else, among several fold matches, the record with the most credited books (lowest id on a tie), logged at Warn with every candidate id and its book count; else create. Credits are counted in one junction pass for all candidates (`BookNarratorsLinking`), not one pass per candidate.
- **Trust model (fail safe, not fail open).** Readers use the index only once THIS process has finished `ReconcileNarratorFoldIndex` (`nameIdx.narratorFoldTrusted`, cleared by `Reset`). Until then, the lookup scans `ListNarrators` (thousands of small rows): slower, never wrong. A persisted sentinel alone is not believed, because a binary that predates the index writes narrators with no fold key while the old sentinel still vouches for the index (rollback then roll-forward). The sentinel `system:backfill:narrator_fold_index_v1_done` is still written when a pass completes, as the record that a full build has run (it also drives the "first build" log line).
- **Every candidate is point-verified on read** (the row exists and its name still folds to the key), so an extra key is harmless; only a missing key can be wrong, and the reconcile writes missing keys. The reconcile never deletes.
- **Reconcile runs on every boot** from `startBackfills` (`narrator-fold-index`), not behind memdb warmup (narrators number in the thousands). It lists the records once, splits them into 256-record chunks and fans out over a bounded pool (NumCPU, cap 8, per CLAUDE.md). A worker folds and probes for absent keys without a lock, then takes `nameIdx.narrator` only for its commit, re-verifying each row there so a concurrent `DeleteNarrator` cannot be undone by a stale Set. Chunks are disjoint, so no two workers write one key.
- **Writers.** The only writers of `narrator:` rows are `CreateNarrator` (via `mintNarratorLocked`) and `DeleteNarrator`; both maintain the fold key in the same batch as the row. The store has no narrator rename. The merge paths (`credits_backfill`, `narrator_split_joined`, `narrator_purge_empty`) repoint `book_narrators` rows and finish in `DeleteNarrator`, so they need no index work. Raw-key writers (a restore) are covered by the boot reconcile.

### Rejected

- **Scan `ListNarrators` on every sync.** O(records) per book write on every CreateBook/UpdateBook/ModifyBook that carries a narrator column, and it still needs the locked re-check to be race-free.
- **Make `CreateNarrator` itself fold-match.** It would silently change four other callers whose intent is an exact spelling.
- **One key per fold holding a JSON id list.** Needs a read-modify-write under the lock on every create and delete, and an ownership-style check on delete.
- **Copying `FoldKey` into `internal/database`.** Two folds that can drift is the bug class this change exists to remove.
- **Trusting the persisted sentinel alone.** See the trust model above.

### Difference from the TODO text

The TODO said an ambiguous fold (several records) should "create nothing and log, as the backfill's `narrator_name_ambiguous` does". The task brief asked for a link instead: exact match, else the record with the most credits, logged at Warn. A sync that creates nothing and links nothing leaves the book with no narrator credit until someone merges, which is worse than linking to the busiest record of a set the owner already considers one person. The brief's rule is implemented; the Warn line names every candidate so the duplicates can be merged.

### Author side

There is no `resolveAuthorCredit` in `narrator_credit_sync.go`; that file is narrator-only. Authors resolve through `scanner.resolveAuthorIDs` -> `authorcredit.Resolve` -> `GetAuthorByName`/`CreateAuthor` (exact normalized name, with author aliases as the spelling-variant mechanism) in other packages. It has the same exact-name shape, so an unaccented author spelling can mint a second author unless an alias exists. That is a separate author refactor (a different package, the `author:name:` index, and PR #4287 is editing the neighbouring author code), so it is not touched here; it is noted for a follow-up.

### Known limits

- `ReconcileNarratorFoldIndex` skips a narrator row that does not decode (`ListNarrators` already skips it); such a row is also unreachable by id, so nothing is hidden that was reachable before.
- Until a boot's reconcile finishes (seconds for a few thousand narrators), each narrator resolve that misses the exact index scans the records.
- The Warn for an ambiguous fold repeats on every sync of a book that carries the ambiguous name, until the duplicates are merged. That is deliberate: it is the signal.
