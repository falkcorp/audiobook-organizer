### ABS search: post-mint-lock CPU profile (2026-09-08 03:34 EDT)

35 s CPU profile of prod, captured while driving 12 distinct uncached broad
queries concurrently. Total samples **437.25 s = 1249 % CPU** (≈12.5 of 48 cores).
Taken on the deployed debug build immediately after #3128.

**The mint lock is fixed and the fix is confirmed by absence, not by argument.**
`MintOrGetSyncFileIDs` is 10.80 s cum (**2.47 %**) and no longer serializes
anything; before #3128 the singular form was a process-global mutex held across
an fsync on the innermost loop of every item response. The whole
`abs.LibrarySearch` handler is now **39.53 s cum = 9.04 %** of process CPU.

**Search latency is now dominated by things that are not search.**

| Consumer | cum | share |
|---|---|---|
| GC (`gcBgMarkWorker`) | 206.40 s | **47.20 %** |
| `metadata.batch-apply-cached` → `applyCachedCandidateForBook` | 80.80 s | 18.48 % |
| ├ `WriteTagsSafe` | 58.40 s | 13.36 % |
| ├ `ComputeFileHashAndSize` | 53.64 s | 12.27 % |
| └ `sha256.blockGeneric` | 53.42 s | **12.22 %** |
| `abs.LibrarySearch` (everything below is inside it) | 39.53 s | 9.04 % |

Load average 22.85 on 48 cores. Measured search latency in that window: **3.4 s
(q=M) – 15.7 s (q=L)**, payloads 2.2–3.6 MB. Before #3128 the same query shape
measured 13.8–28.0 s. **Both measurements were taken with a `library.scan` and
the apply jobs running, so neither is a clean isolation** — the improvement is
real but the range is not a controlled A/B.

The apply jobs are hashing whole audio files with SHA-256 and that plus the
allocation churn is driving 47 % GC. Search will stay slow while they run. That
is a deliberate trade the user made (they are applying metadata and were
explicitly told not to be cancelled); it is not a search defect.

#### New hotspots INSIDE search, ranked (these are the real follow-ups)

Costs nest, so these overlap.

- [ ] **`searchSeriesHits` — 29.26 s cum (6.69 %), now larger than the entire
      item-view path.** Calls `seriesPageBooks` → `seriesRows` (22.69 s, 5.19 %).
      This is the single biggest thing inside search and had never been looked at,
      because the mint lock was masking it.
- [ ] **`coverPath` / `coverFile` inside `minifiedMedia` — 27.84 s cum (6.37 %).**
      Per-item filesystem work for cover art on the search path. Note `os.Stat` on
      book files measured 0.01 ms, so this is not "stat is slow" — it is either
      doing many more stats than expected or resolving paths expensively.
- [ ] **`minifiedItem` — 23.17 s (5.30 %).**
- [ ] `loadItemViews` / `loadOneItemView` — 17.80 s (4.07 %); `GetBookFiles`
      within it only 4.98 s (1.14 %), which is why the batch swap in the N+1
      audit's finding 1 is **not** the priority it looked like.
- [ ] **`MintOrGetSyncFileIDs` at 10.80 s (2.47 %) is now worth the prefix-scan
      follow-up** (audit finding 7): the `sync_file:book:<bookID>:<syncFileID>`
      index stores the fileID as its value, so one scan per book replaces the N
      point-gets this method still does. `RepointSyncFile` keeps that index
      current (verified).
- [ ] **Investigate the 47 % GC directly.** A 3 MB response per search is a large
      allocation source; so is the apply job. Worth a heap profile before assuming
      which one dominates.

Profile artifact: captured via `http://localhost:6060/debug/pprof/profile?seconds=35`
on the deployed debug build. Re-capture rather than trusting these exact numbers —
they were taken under a specific concurrent workload.

Companion: [`docs/audits/2026-09-08-n-plus-one-batch-endpoint-audit.md`](docs/audits/2026-09-08-n-plus-one-batch-endpoint-audit.md).
