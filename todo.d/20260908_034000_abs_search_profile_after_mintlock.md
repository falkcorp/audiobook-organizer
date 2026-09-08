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
- [x] **DONE (#3130).** **`coverPath` / `coverFile` → `metadata.CoverPathForBook`
      — 27.96 s cum (6.40 %). Root cause identified: `filepath.Glob` reads and
      sorts the entire covers directory once per book.** `internal/metadata/cover.go:180` globs
      `<rootDir>/covers/<bookID>.*`. Because the pattern contains a meta character,
      Go's `filepath.Glob` cannot do a point lookup — it falls into `glob()`, which
      calls `Readdirnames(-1)` on the whole directory, `slices.Sort`s every name, and
      runs `filepath.Match` against each. The profile splits exactly that way:

      | callee of `filepath.glob` | cum | share of glob |
      |---|---|---|
      | `os.(*File).Readdirnames` | 18.00 s | 64.38 % |
      | `slices.Sort[[]string,string]` | 5.57 s | 19.92 % |
      | `filepath.Match` | 4.17 s | 14.91 % |
      | `os.Stat` | 0.08 s | 0.29 % |

      **`/mnt/bigdata/books/audiobook-organizer/covers` holds 9,288 entries**
      (counted on prod 2026-09-08), so every cover resolution reads and sorts 9,288
      filenames to find one whose name is already known. Cost is
      O(items × covers-dir-size) and grows as the library gains covers, independent
      of the query.

      **Fix:** the bookID is known and only the extension is unknown — `os.Stat` the
      five candidate extensions (`.jpg .jpeg .png .webp .gif`) in the order the
      existing loop prefers, and return the first hit. `os.Stat` measured 0.01 ms on
      this box, so this replaces a 9,288-entry readdir + sort with at most five
      point lookups. Behaviour differs in one edge case worth a test: `Glob` returns
      *any* extension matching `.*` and the loop then filters to the five known
      image types, so a cover stored under some other extension is found by neither
      the old code (filtered out) nor the new (not probed) — but a **case-variant**
      extension (`.JPG`) is matched by `Glob` + `strings.ToLower` today and would be
      missed by naive stats. Probe case variants or keep the comparison
      case-insensitive.

      Note `CoverPathForBook` has two other callers — `metafetch.writeBackForBook`
      and `ApplyMetadataFileIO` (0.07 s / 0.06 s here) — so they benefit too, and
      both must stay correct.

      ⚠️ Verify the root dir from `GET /api/v1/config` (`root_dir`), **not** from the
      systemd unit: the unit sets `AUDIOBOOK_ROOT_DIR=/var/lib/audiobooks`, which a
      config file overrides to `/mnt/bigdata/books/audiobook-organizer`. The unit's
      value does not exist on disk. (`AUDIOBOOK_ROOT_DIR` is in fact vestigial —
      `viper.AutomaticEnv()` runs with no `SetEnvPrefix`, so the key it reads is
      `ROOT_DIR`.)

      **Fixed in #3130**, which also had to fix a path-traversal exposure the change
      surfaced: CodeQL models `os.Stat` as a path sink and does not model `Glob`, so
      swapping them turned a silent pre-existing issue into a new high-severity alert
      on the read — and the `os.Create` one line below had the same exposure. Both
      now go through `safeCoverID`; the read is then confined by probing through an
      `fs.FS` rooted at the covers directory and the write by
      `pathvalidation.SecureJoin`. The directory is
      also growing: 7,885 files on 2026-08-02, 9,288 on 2026-09-08, so the cost of
      the old glob rose over time.
- [ ] **`minifiedItem` — 23.17 s (5.30 %).**
- [ ] `loadItemViews` / `loadOneItemView` — 17.80 s (4.07 %); `GetBookFiles`
      within it only 4.98 s (1.14 %), which is why the batch swap in the N+1
      audit's finding 1 is **not** the priority it looked like. Two reasons, and
      they must be read together: (a) `GetBookFiles` is **prefix-bounded**, so the
      per-book cost is already proportional to that book's files, not to the table
      — the N+1 shape is real but each call is cheap; and (b) the proposed
      replacement `GetBookFilesForIDsCore` **falls back to a full scan of all
      ~726 K `book_file:` rows** when memdb is unavailable, which is exactly the
      state during the ~130 s async warmup after every restart. So the swap trades
      a measured-minor warm cost for an unmeasured-severe cold one. Do not treat
      "only 1.14 %" as a green light to apply finding 1 unchanged — if it is done
      at all, the fallback must be made prefix-bounded first.
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
