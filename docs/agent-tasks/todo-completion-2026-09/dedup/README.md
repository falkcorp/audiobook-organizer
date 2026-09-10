<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f09e3ef-0356-4f5a-8bcc-6791e95b8e7a -->
<!-- last-edited: 2026-09-10 -->

# Workstream — dedup (todo-completion-2026-09)

14 tasks: 9 carried forward from the 2026-08-21 package (ids kept), 5 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-040](TASK-040-make-unmergeauto-reverse-external-id-reassignmen.md) | carried | data-loss | P1 | L | Make UnmergeAuto reverse external-ID reassignment and iTunes write-back removals | internal/database/dedup_automerge_journal.go:36-51 AutoMergeJournalEntry still has only Ke |
| [TASK-045](TASK-045-build-a-dry-run-report-only-classifier-for-serie.md) | carried | hygiene | P2 | M | Build a dry-run report-only classifier for series that look like they were minte | find . -iname 'series_title_leak_audit*' = 0 results at HEAD. |
| [TASK-046](TASK-046-route-merge-asexternalidreassigner-through-datab.md) | carried | hygiene | P2 | S | Route merge.AsExternalIDReassigner through database.AsCapability instead of a ba | internal/merge/service.go:34-42 AsExternalIDReassigner(s any) ExternalIDReassigner still d |
| [TASK-048](TASK-048-physically-co-locate-a-combine-survivor-s-files-.md) | carried | hygiene | P2 | M | Physically co-locate a Combine survivor's files under RootDir after CombineBooks | internal/merge/service.go:771 (line drifted from 09-02's L467 due to file growth) still re |
| [TASK-049](TASK-049-acoustic-confirm-signal-promote-near-dupe-title-.md) | carried | correctness | P2 | M | Acoustic-confirm signal: promote near-dupe title-leak pairs using WholeFileSimil | grep WholeFileSimilarity internal/dedup/auto_resolve.go = 0 hits at HEAD. |
| [TASK-050](TASK-050-shattered-book-reassembly-match-fragment-file-se.md) | carried | correctness | P2 | L | Shattered-book reassembly: match fragment file-sets against the reference corpus | grep 'AcoustID/Fingerprint/fpidx' internal/dedup/split_book_detector.go = 0 hits. `git log |
| [TASK-180](TASK-180-measure-whether-dedup-duration-abridged-3-573-is.md) | carried | hygiene | P2 | S | Measure whether dedup:duration-abridged (3,573) is over-firing before touching i | find . -iname 'dedup_abridged_measure*' = 0 results at HEAD. |
| [TASK-192](TASK-192-clamp-composescore-against-per-kind-confidence-b.md) | carried | correctness | P2 | M | Clamp ComposeScore against per-kind confidence bounds; route calibrate-composite | grep -rn 'apply_confidence/ApplyConfidence' internal/ = 0 hits at HEAD. scoreWithClamp exi |
| [TASK-193](TASK-193-wire-round-2-confidence-bound-clamping-into-a-di.md) | carried | correctness | P2 | M | Wire Round-2 confidence-bound clamping into a distinct apply_confidence path; ke | Same absence as TASK-192: apply_confidence 0 hits repo-wide. |
| [TASK-300](TASK-300-mergesplitbookcluster-performs-an-unguarded-read.md) | new-finding | data-loss | P0 | S | MergeSplitBookCluster performs an unguarded read-modify-write on book/file rows  | internal/dedup/split_book_merge.go:67 |
| [TASK-301](TASK-301-unattended-auto-merge-paths-exact-file-hash-matc.md) | new-finding | data-loss | P1 | M | Unattended auto-merge paths (exact file-hash match, LLM high-confidence verdict) | internal/dedup/engine.go:1373 |
| [TASK-325](TASK-325-two-ops-scan-the-whole-embedding-book-keyspace-w.md) | new-finding | perf | P2 | S | Two ops scan the whole embedding/book keyspace with a plain sequential loop doin | internal/plugins/dedup/cleanup_orphan_embeddings.go:184 |
| [TASK-354](TASK-354-dedup-series-dedup-s-apply-path-writes-no-undo-l.md) | new-todo | data-loss | P1 | M | dedup.series-dedup's apply path writes no undo-ledger rows and does not check fo | TODO.md lines 4967 |
| [TASK-364](TASK-364-sec-origin-is-reachable-from-the-lan-bind-loopba.md) | new-todo | data-loss | P1 | L | SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as spe | TODO.md lines 17185, 17307, 17329 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
