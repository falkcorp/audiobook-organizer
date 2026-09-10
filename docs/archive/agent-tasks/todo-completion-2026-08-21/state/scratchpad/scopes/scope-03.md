# Scope 03 — 26 items

## ITEM L1063 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Audit every config option name — the set has grown by accretion and the
      naming is inconsistent.** Prod's `/api/v1/config` currently returns **113**
      keys. They were added over a long period by different code paths, and nothing
      has ever reviewed them as a set, so the vocabulary drifted.

      The trigger: `write_back_metadata` is named for a whole subsystem but gates a
      single call site on the auto-fetch path, while the flag that actually controls
      tag writing on apply is `auto_write_tags_on_apply`. Reading the two together
      leads a reasonable person to the wrong conclusion about whether the app writes
      to their files. That one has its own entry above; this task is the sweep.

      **What to look for:**
      - *Scope lies* — a name broader than what it gates (the
        `write_back_metadata` class). Name the flag after its call site, not its
        subsystem.
      - *Asymmetric pairs* — two flags controlling the same behaviour on two paths
        that do not share a prefix (`..._on_fetch` / `..._on_apply`).
      - *Names that read as booleans but are not* — e.g. `organization_strategy`
        has the value `auto`, which sits next to `auto_organize` (a real bool)
        and invites exactly the confusion it caused today.
      - *Dead options* — a key that nothing reads. Verify by grepping the
        `AppConfig.<Field>` READ sites, not the field declaration: several keys are
        declared, bound to viper and persisted, yet never consulted. A key that
        cannot change behaviour should be deleted, not documented.
      - *Unclear units* — `auto_scan_debounce_seconds` is good; anything with a
        bare number and no unit suffix is not.

      **Method:** enumerate from the live `/api/v1/config` response, not from the
      struct — the struct carries fields the API does not expose and vice versa. For
      each key, find its READ sites and write down the one sentence that describes
      what it actually changes. Any key where that sentence does not match the name
      is a rename candidate.

      **Every rename needs the deprecated-alias migration** described in the
      `write_back_metadata` entry: live config is a persisted snapshot, so a bare
      rename silently reverts the setting to its default on next load.

## ITEM L1247 [tier C] section: Config
primary_domain_guess: internal/config | all_domains_guess: internal/config;internal/metafetch

- [ ] **Rename `write_back_metadata` → `auto_write_tags_on_fetch`.** The current name
      reads like a global "do we ever write tags to files" switch. It is not. It
      gates exactly one call site — `mfs.writeBackMetadata(updatedBook, meta)` at
      `internal/metafetch/service_fetch.go:309`, on the **auto-fetch** path only.

      Tag writing on **apply** is a completely separate flag,
      `auto_write_tags_on_apply` (`internal/metafetch/service_writeback.go:604`),
      which is **on** in prod. So the two live side by side in the config with one
      named for what it does and the other named for the whole subsystem, and
      reading `write_back_metadata: false` naturally leads to "we're not writing
      tags to files at all" — which is wrong. That misreading already happened.

      Renaming makes the pair symmetric and self-documenting:
      `auto_write_tags_on_fetch` / `auto_write_tags_on_apply`.

      **Touch points:** `internal/config/config.go:531` (struct field + json tag),
      `:1445` (viper binding), `internal/config/persistence.go:1075` (snapshot
      load), `internal/metafetch/service_fetch.go:309` (the only read site), and
      `internal/config/config_unit_test.go:654`.

      **Migration matters — do not do a bare rename.** Live config is a persisted
      snapshot, not defaults (the stored value overrides `config.go`'s default).
      Prod's snapshot has the OLD key, so the loader must keep honouring
      `write_back_metadata` as a deprecated alias, or the setting silently reverts
      to its default on the next load and the fetch path changes behaviour without
      anyone asking for it. Read the old key when the new one is absent, and log
      once at WARN when the alias is used.

## ITEM L1275 [tier B] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Related asymmetry found while tracing the above: auto-fetch embeds cover
      art into audio files even when tag write-back is off.**
      `mfs.embedCoverInBookFiles(updatedBook, coverPath)` sits *outside* the
      `if config.AppConfig.WriteBackMetadata` block in `service_fetch.go` (~:301 vs
      :309). So with `write_back_metadata: false`, auto-fetch still modifies files
      on disk — artwork only, no text tags. That may well be intended, but it is
      not what either flag's name suggests, and it means "off" does not mean "does
      not touch my files." Decide whether cover embedding belongs under the same
      gate, and say so in a comment either way.

## ITEM L1317 [tier C] section: Config
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **CFG-AUDIT** Triage the findings in
      `docs/audits/2026-08-20-config-option-audit.md` (full config-option
      inventory + grep-verified usage/naming/default audit, 565 options). At
      minimum decide on: (1) `EnableRateLimit=false` not actually disabling
      rate limiting — only `APIRateLimitPerMinute > 0` gates it; (2)
      `AuthRateLimitPerMinute` is fully wired but never enforced anywhere; (3)
      `APIRateLimitPerMinute` default drift between the fresh-install viper
      default (0/unlimited) and `ResetToDefaults()`/`.env.example` (100); (4)
      `ai_backend.local_base_url` defaulting to a hardcoded developer LAN IP,
      which silently routes fresh installs into local-LLM mode; (5)
      `Config.ChapterConsolidationThresholdMin` being omitted from
      `ResetToDefaults()`, so a factory reset silently disables chapter
      consolidation instead of restoring the intended default of 10; (6)
      whether to delete the fully inert `--enable-sqlite3-i-know-the-risks`
      flag now that the SQLite backend is gone; (7) whether to wire up or
      remove the two entirely-unenforced Settings-UI subsystems (Storage
      Quotas, Memory Limits) and the ~10 other dead Settings-page toggles
      (`create_backups`, `verify_after_write`, `AutoFetchMetadata`,
      `EmbedCoverArt`, etc.) listed in the report so users stop being able to
      flip a switch that does nothing.

## ITEM L1338 [tier C] section: Config
primary_domain_guess: internal/metafetch | all_domains_guess: internal/metafetch

- [ ] **SCORE-REC** Route `ScoreOneResultWithBreakdown` through `scoreRecorder`
      like `ApplyNonBaseAdjustmentsWithBreakdown` now is. It still hand-builds
      its own `ScoreOpBase` step at `internal/metafetch/service_scoring.go:724`,
      duplicating what `newScoreRecorder` does. This is the last hand-rolled
      `ScoreStep` site left after #2639, which converted the sibling function
      because `scoreRecorder.add` had been flagged unused — the linter can only
      see the unused helper, never the copied logic that should be calling it,
      so nothing will flag this one. Done means: no `ScoreStep` composite
      literals outside `score_breakdown.go`, and the golden fixtures in
      `service_scoring_test.go` still pin the same totals (verify by mutation,
      not by a green run — halving a factor must fail them).

## ITEM L1350 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🏷️ **"Browse by Tag" surfaces internal bookkeeping tags and formats them
      badly.** Owner report 2026-08-10 with a screenshot of the Library page on
      mobile (`books.jdfalk.com`, "Browse by Tag (149)"). The widget is *almost*
      right; every problem below is presentation, not tagging.

      Observed, top five chips in order:

      | Chip as rendered | Count | Verdict |
      |---|---:|---|
      | `dedup:duration-match` | 24,883 | **Strip** — internal dedup bookkeeping |
      | `metadata:language:en` | 15,036 | Keep, but **reformat** (see below) |
      | `metadata:source:audible` | 14,895 | **Strip** — provenance, not a browse axis |
      | `dedup:duration-abridged` | 3,573 | **Strip**, and the count is **suspect** |
      | `science fiction & fantasy` | 1,109 | ✅ this is what the widget is for |

      **What the owner asked for:**

      1. **Strip `dedup:duration-match` entirely.** Nobody browses their library
         by "the deduper thought these two durations matched."
      2. **Strip `metadata:source:audible`** and its siblings (`google-books`,
         etc.). *"For those weird ones like audible metadata source or google
         books or whatever don't put those at the top ever if we can. If we just
         have to hide them that's fine too."* — so a hide/allow-list is an
         acceptable implementation; they do not have to be deleted from the data.
      3. **Strip the `metadata:` prefix and put a space between key and value.**
         `metadata:language:en` should read `language: en`, not
         `metadata:language:en`. Owner: *"for gosh sakes."*
      4. **`dedup:duration-abridged` (3,573) — verify the number before trusting
         it.** Owner: *"not sure on the abridged. That's a weird one. I don't
         think it's as high as you think it is."* Treat this as a **separate
         data question** from the display cleanup: 3,573 abridged editions out of
         the library is a claim the tagger is making, and the owner's intuition
         is that it is over-firing. Do not "fix" it by hiding the tag — measure
         whether the abridged detection is correct first, then decide. Hiding a
         wrong number makes it unfalsifiable.
      5. **Confirm tags are per-BOOK, not per-file.** Owner: *"Also tags are per
         book right?"* This needs a definitive answer from the schema, not an
         assumption — if any tag is stored per `book_file`, a multi-file
         audiobook would inflate every count in this widget by its file count,
         which would independently explain why several of these numbers look too
         high. **Check this before touching the display**, because it changes
         whether the counts above are even meaningful.

      **Suggested shape** (not locked): an ordering/visibility policy for tag
      namespaces rather than per-tag special cases — genuine subject tags
      (`science fiction & fantasy`) rank first, `metadata:*` renders
      prefix-stripped and `key: value` formatted below them, and `dedup:*` plus
      other machine-internal namespaces are hidden from Browse by Tag entirely
      while remaining searchable/filterable for anyone who wants them.

      Screenshot in the 2026-08-10 session; widget renders on the Library page
      under the search box, above `Select All`.

## ITEM L1403 [tier B] section: Config
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);ci/scripts

- [ ] **E2EGATE-NOTREQUIRED** The E2E suite runs on every qualifying PR and its
      result is enforced by nothing. A red run merges exactly as easily as a
      green one. **Owner decision required — do not enable unattended.**

      This is the April-2026 spec-rot incident one layer up. That incident was
      "the suite existed and nothing ran it." The state now is "the suite runs
      and nothing acts on the answer," which is quieter and looks healthier.

      **Measured 2026-08-10 ~09:5x EDT**, on `main` at `dc724b80`:

          gh api repos/falkcorp/audiobook-organizer/rules/branches/main

      That endpoint is the authoritative one — it returns every rule applying to
      the branch **including org-inherited rulesets**, which a repo-scoped
      `/rulesets` query misses. It returned exactly four rules, all from the
      `falkcorp` **Organization** ruleset:

          required_linear_history
          deletion
          repository_delete
          repository_transfer

      There is **no `required_status_checks` rule and no `pull_request` rule**,
      and classic protection (`/branches/main/protection`) returns 404 "Branch
      not protected". Consequences, all of which follow directly:

      - A PR whose E2E run fails can be merged with the normal green button.
      - `gh pr merge --admin` bypasses nothing on this repo, because nothing is
        required. Tonight's four merges (#2277–#2280) were gated only by the
        agent session's own bash poll loop, not by GitHub.
      - Nothing requires a pull request at all; a direct push to `main` is
        blocked only by `required_linear_history`.

      **The fix is not simply "turn on required status checks."** `e2e.yml`
      carries `paths: ['web/**', '**.go', 'go.mod', 'go.sum',
      '.github/workflows/e2e.yml']`. A required check that is filtered out of a
      given PR can leave that PR pinned at *"Expected — Waiting for status"*
      forever, which would strand every docs-only PR — the exact shape of
      #2279 and #2280. GitHub's behaviour here depends on how the check is
      registered, and **this has not been measured on this repo.** Resolve that
      before enabling, e.g. with an always-runs `E2E Summary` job that reports
      success when the real job is skipped by path.

      **What was checked and found sound**, so it is not the problem: the
      `paths` filter does cover the whole suite. Every spec, plus
      `playwright.config.ts`, `global-setup.ts` and `utils/test-helpers.ts`,
      lives under `web/tests/e2e/`, which `web/**` matches. A fixture or config
      change like the one that caused the original four-month rot *would*
      trigger the job today.

      **NOT claimed:** that enabling enforcement is safe as-is; that the
      `paths`/required-check interaction behaves any particular way here; or
      that any PR has actually merged red. No merged-red PR was searched for.

      Fixed in the same PR as this fragment: `e2e.yml`'s header comment
      asserted the job "BLOCKS on every trigger", which was never true. A
      comment claiming a gate exists is worse than no comment, because it stops
      anyone from checking.

## ITEM L1462 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🔴 **The binary ITL parser extracts ZERO smart playlists from real iTunes
      libraries, while the XML export of the same library has 292.** Measured
      2026-08-10 against the owner's live library. This is the blocker standing
      between `maintenance.itunes-playlist-import` and the owner's request
      *"I want all my dynamic playlists from iTunes imported."*

      | Source | Playlists | Smart |
      |---|---:|---:|
      | `/mnt/bigdata/books/itunes/iTunes Library.itl` (live, Jul 19, 32 MB) | 357 | **0** |
      | `.../bkup/itunesgood/iTunes Library.itl` (Apr 2, 28 MB) | 335 | **0** |
      | `/mnt/bigdata/books/itunes/iTunes Library.xml` (same library) | 351 | **292** |

      **Not writeback data loss.** Both the live ITL and an April backup return
      zero, so this is not our ITL writer stripping records — it is extraction
      that never fires on real files. Both parse cleanly otherwise (97,999 and
      90,900 tracks, correct titles, `ver=12.13.10.3`).

      **Not an unimplemented stub either** — that was the first hypothesis and it
      was wrong. `itl_be.go:341-354` and `itl_le.go:429-441` do populate
      `IsSmart`, `SmartCriteria` (hohm `0x65`) and `SmartInfo` (hohm `0x66`).
      The code exists and presumably works on the synthetic fixtures the unit
      tests build by hand — `playlist_sync_test.go` constructs `ITLPlaylist`
      values with `IsSmart: true` directly, so **no test has ever exercised the
      parser that fills those fields.** A third instance of the session's theme:
      the tests pass because they bypass the thing that is broken.

      The XML proves the data exists and is recoverable: 292 `Smart Criteria`
      blobs, with names that are clearly the owner's real playlists (series and
      author names — "A Mage's Cultivation", "Aether's Revival", "Aaron Oster",
      "All the Skills").

      **Two candidate directions — needs a decision:**
      1. **Fix ITL extraction.** Find why the `0x65`/`0x66` branch does not fire
         on 12.13.10.3 files (offset/section assumption, playlist record layout,
         BE-vs-LE path). Highest value: the ITL is the write/authority surface,
         so push-back will need this too. Start by dumping the hohm types
         actually encountered while parsing one real playlist record — the
         parser reaches these playlists (titles are correct), so the records are
         either absent from the stream or skipped.
      2. **Import from the XML export instead.** The criteria are present and
         base64-encoded; `ParseSmartCriteria`/`TranslateSmartCriteria` already
         consume that blob shape. Much cheaper, unblocks the owner immediately,
         and read-only against a file we already parse elsewhere. Downside: the
         XML is an export, not the authority, so it can lag the ITL.

      Recommend **2 to unblock, then 1** — but confirm the XML's base64 blob is
      byte-identical in shape to what `ParseSmartCriteria` expects before
      assuming it drops in.

      `maintenance.itunes-playlist-import` already guards this case explicitly:
      when it parses playlists but extracts zero smart ones it logs a warning
      naming the XML discrepancy and the `grep -c 'Smart Criteria'` check,
      rather than reporting a clean "0 imported" that reads like an empty
      library.

## ITEM L1517 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **ITUNES-SMARTCRIT-PARSE** `ParseSmartCriteria` does not understand the
      real Smart Criteria format. It reports success on every blob and returns
      **garbage**. Measured 2026-08-10 against all 292 smart playlists in the
      owner's live `iTunes Library.xml`.

      🚨 **This is a "reporting success while meaning nothing" defect**, and it
      is worse than a parse failure would be: `ParseSmartCriteria` is documented
      as "tolerant of unknown fields/operators — those are recorded as raw hex
      rather than causing parse failure," so a totally wrong layout yields
      `err == nil` and a full-looking rule list. **292/292 blobs parsed with
      zero errors and 1,751 rules — and essentially every rule is empty:**

          PLAYLIST "Audiobooks" conj=OR rules=2
             field=unknown_0x2000000 op=op_0x00 operands=[]
             field=unknown_0x00      op=op_0x00 operands=[]

      One operand decoded as `72057594037927936` — that is `0x0100000000000000`,
      a byte-order artifact, confirming the endianness is also wrong.

      **Is the criteria plain text in the XML?** No. `Smart Criteria` is a
      base64-wrapped **binary** blob — byte-for-byte the same `SLst` structure as
      in the `.itl`. The XML's advantage is only that the blob is *present*
      (292/292) where ITL extraction yields 0. **But the string operands inside
      the blob are plain UTF-16BE and are recoverable without a full parser.**

      **What the format actually is** (derived from the 292 real blobs, and the
      two claims marked ✅ are the only ones validated against ground truth):

      - ✅ Magic `SLst` (`0x534c7374`) at offset 0 — the parser never checks it.
      - ✅ **Big-endian**, not little-endian.
      - ⚠️ It is **NOT** a flat `header + rules[]` array. The blob is a **nested
        tree of `SLst` containers** — an 850-byte single-rule blob contains
        *three* `SLst` magics (offsets 0x000, 0x0C0, 0x1FC). An earlier revision
        of this entry claimed a flat 136-byte header with variable-length rules;
        parsing all 292 blobs that way **overruns on 281/292** and that claim was
        wrong. The container nesting is still unmapped.

      **A parser is not required to extract the rules.** Operands can be located
      structurally and the surrounding rule header read at fixed negative
      offsets. Over all 292 blobs this yields **358 operands whose declared
      length matches**, which is what proves the alignment:

          find a UTF-16BE run at offset `off`
          require  u32be(off-4)  == len(run)*2     # length prefix agrees
          field  =  u32be(off-56)
          operator= u32be(off-52)

      **Field codes** (over alignment-proven operands): `3`=Album ×204,
      `4`=Artist ×126, `8`=Genre ×23, plus `71` ×2, `2` ×2, `14` ×1.

      **Operator words — validated against actual playlist membership**, by
      testing whether the materialized members really satisfy the rule:

      | word | meaning | evidence |
      |---|---|---|
      | `0x1000002` | contains | satisfied **4017/4017 = 100%** |
      | `0x3000002` | does **NOT** contain | satisfied **0/23700 = 0.0%** |
      | `0x1000001` | **UNRESOLVED** (22 uses) | 18.2% — fits neither |

      🚨 `0x3000002` is a **perfect inversion**. Treating every `…0002` word as
      "contains" would ship 79 playlists matching exactly the books they were
      written to exclude — silently, and looking like a successful import. Do not
      map an operator word without running this membership check on it.

      **Conjunction:** with negation applied, **38/46** multi-rule playlists have
      every rule holding for >95% of members, so rules are predominantly ANDed;
      the other 8 (e.g. `Recent Litrpg`, 10 rules) are ORs. The AND/OR flag has
      **not** been located — candidate is the u32 at offset 8 of each `SLst`
      container (`0x02` outer vs `0x01` inner in the dump), untested.

      **Why no test caught the original defect:** `playlist_sync_test.go` builds
      `ITLPlaylist` values by hand with `IsSmart: true` and never exercises a
      real blob, and the tolerant-by-design error handling means a round-trip
      over real data still returns `nil`. Any fix must assert on **rule
      content** — a non-empty `Rules` slice is not evidence, since the broken
      parser produces 1,751 of them. The membership check above is the ground
      truth to assert against, and it needs no prod access.

      **Two independent recovery sources, covering 290/292 (99.3%):**

## ITEM L1627 [tier C] section: Config
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/dedup;internal/merge;internal/search;internal/server/handlers

- [ ] **MERGE-CACHE-EVICT** A merge must evict (or dirty-flag) every merged-away
      book/file ID from **every** read cache, so the losers stop appearing after
      the merge succeeds. **Owner-reported 2026-08-10:** "you think you merged
      something then you see 2 copies still." Applies to both merge shapes —
      several files into one book, and two books into a version group.

      This is a trust bug, not a cosmetic one: a merge that visibly does nothing
      teaches the owner not to believe the merge button. Mechanism does not
      matter (evict, dirty-flag, write-through) — the invariant does: **after
      `MergeBooks` returns success, no read path may still serve a loser ID.**

      **Established by grep at `76269d57` (measured, not inferred):**

      - Merge entry points: `internal/merge/service.go:125`
        (`(*Service).MergeBooks`) and `internal/dedup/book_dedup.go:395`
        (`MergeBooks`). HTTP entry: `internal/server/handlers/duplicates/handler.go:292`.
      - Losers are **soft-deleted**, not removed: `merge.SoftDeleteBook`
        (`internal/merge/service.go:544`) sets `MarkedForDeletion` /
        `MarkedForDeletionAt` and calls `store.UpdateBook`, falling back to
        `DeleteBook` only if that write fails.
      - `UpdateBook` does write through to the in-memory copy — the
        `UpsertBookToMemDB` / `DeleteBookFromMemDB` API exists at
        `internal/database/memdb_sync.go:123` and `:182` — and calls
        `InvalidateLibraryStats`. So **memdb is the layer least likely to be at
        fault**; do not start there.
      - 🚨 **`internal/merge/` and `internal/dedup/` contain ZERO references to
        the search index** — no `IndexBook`, no delete-from-index, no dirty-set
        enqueue. The Bleve index lives in `internal/search/` (`bleve_index.go`,
        `index_builder.go`). A merge therefore never tells the index its losers
        are gone.

      **NOT established — verify before fixing, do not assume:**

      1. Whether `UpdateBook` itself enqueues into the search dirty set added by
         **#2268**. If it does, the index may self-heal on the next reconcile
         pass and the visible-duplicate window is a *latency* problem, not a
         *correctness* one — a different fix (force a reconcile on merge) than
         an explicit evict.
      2. Whether a soft-deleted book is **deleted from** the Bleve index or
         merely re-indexed carrying `MarkedForDeletion`, and whether the query
         path filters on that flag. If it is filtered only *after* pagination,
         this is the same post-filter-after-pagination defect already recorded
         for search — losers would consume page slots even once filtered.
      3. The **file-level** merge path (several files into one book) was not
         located in this pass. Find it and check it independently; do not assume
         it shares `merge.SoftDeleteBook`.
      4. Whether the version-group read path de-duplicates. Related:
         `GetBooksByVersionGroup`'s pointer index (fixed in **#2288**) — a merge
         writes `VersionGroupID`, so a stale index row is another way a loser
         could keep surfacing.

      **Acceptance criteria (the regression test to write):** merge N books into
      a version group, then **immediately** — no sleep, no refresh — re-query
      (a) the library list, (b) search, (c) the version-group endpoint, and
      assert every loser ID appears in **none** of them. A test that passes only
      after a sleep is measuring the reconciler, not the fix.

      Related: the cached-aggregates dirty-flag pattern already used elsewhere in
      this codebase, and `InvalidateLibraryStats` as the existing precedent for
      "a write invalidates a derived read."

## ITEM L1727 [tier C] section: Config
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] 🟡 **Why does React silently drop a `setState` issued from a `setTimeout`
      on a mounted class component inside a portal?** This is the residual
      unknown under the Drawer fix above and under the 2026-08-10 "invisible
      sheet" incident, and it is now the *only* part still unexplained. Ruled
      out: duplicate React copies (single `react@18.3.1`, deduped), StrictMode
      (dev-only in `web/src/main.tsx`), `flushSync`/`startTransition`/
      `unstable_batchedUpdates` (absent from `web/src`), uncaught exceptions
      (none observed), and unmounting (`componentWillUnmount` never runs,
      `isMounted` stays true). Leading untested hypothesis: this is a
      production-build React 18 concurrent-root update issued while the root is
      mid-render, where the dev-only warning that would have named it does not
      exist. `exit: false` sidesteps the question rather than answering it, so
      any other component that keeps a timer-driven exit transition is still
      exposed — `MuiMenu` currently is (its `exit: 0` measured 20/20 on
      2026-08-10, but that is the same kind of evidence that `exit: 0` on the
      Drawer produced before it failed 10/10 on v6).

## ITEM L1744 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] 🟡 **`batch-operations.spec.ts:100` [webkit] is an intermittent flake —
      find its mechanism.** Observed 2026-08-10 failing once on `main`
      (`76269d57`) and once on the MUI v6 branch, and **passing** on a re-run of
      each. `main` is green: 556 passed / 0 failed / 8 skipped, exit 0.

          Error: expect(locator).toBeChecked() failed
          Locator: getByLabel('Select Test Book 1', { exact: true })
          Timeout: 5000ms — element(s) not found

      "Batch Operations › selection persists across page navigation". When it
      does fail, the checkbox is not merely unchecked — its **label is absent**,
      so the row is not rendering as the test expects at all. Webkit only. That
      shape (row not rendered yet, rather than state lost) points at the
      navigation completing before the list re-renders, which is a timing
      mechanism worth finding rather than re-running past.

      🚨 **This entry previously claimed `main` was red.** That claim came from a
      single run and was wrong; it was published in a PR body and a memory file
      before being re-run. A failure seen once on a suite with known webkit
      flake is not a measurement — re-run before recording it. Per
      `feedback_fix_flaky_tests`, this still gets its mechanism found rather
      than being ignored as noise.

## ITEM L1767 [tier C] section: Config
primary_domain_guess: internal/itunes | all_domains_guess: internal/itunes

- [ ] 🎧 **iTunes dynamic-playlist import and playlist push-back are fully
      implemented and NEVER CALLED.** Owner request 2026-08-10: *"I want all my
      dynamic playlists from iTunes imported"* and *"I'd like it if we could sync
      our dynamic playlists."* Measured the same day — this is a **wiring gap,
      not a build gap.**

      `internal/itunes/service/playlist_sync.go` (v2.1.0) implements both halves
      of spec 3.4:

      - `MigrateSmartPlaylists(lib *itunes.ITLLibrary) (imported, skipped int)`
        — reads smart playlists from the ITL, parses the Smart Criteria blob,
        translates it to our DSL, creates `UserPlaylist` rows with `type=smart`,
        and stores the raw blob in `ITunesRawCriteriaB64` for audit. Idempotent,
        skips playlists already imported by iTunes PID.
      - `PushDirty() int` — creates an ITL playlist for dirty playlists with no
        PID, updates the track list for those that have one.

      **Both have ZERO non-test callers.** Verified by enumerating every method
      on `*PlaylistSync` and grepping for call sites across `internal/` and
      `cmd/` excluding `_test.go`:

          MigrateSmartPlaylists -> 0 non-test callers
          PushDirty             -> 0 non-test callers

      The service *constructs* `PlaylistSync` (`itunes/service/service.go:124`,
      "M1 step 4") and the store side is complete — `ListDirtyUserPlaylists()`
      exists, `idx:upl:dirty:` is maintained, `idx:upl:itunes:<pid>` maps PIDs
      back to playlists. Everything is in place except an invocation. So the
      owner's iTunes smart playlists have never been imported, and no playlist
      has ever been pushed back, while the code to do both sits tested and idle.

      This is the same failure shape as the rest of the 2026-08-10 backlog: a
      mechanism that reports nothing because it never runs. It will not show up
      as an error, a warning, or a failed op — there is simply no op.

      **Work:**
      1. Decide the trigger for `MigrateSmartPlaylists` — a one-shot maintenance
         op (consistent with the rest of the codebase), a step in the existing
         iTunes import flow, or an explicit endpoint. It needs an `*ITLLibrary`,
         so it has to hang off whatever already parses the ITL.
      2. Decide the trigger for `PushDirty` — this one WRITES to the iTunes
         library, so it must respect the standing rule that the active iTunes
         tree is hands-off, and it must be dry-run-gated on first run like every
         other apply path here. Import (read-only) and push (write) should ship
         as **separate** units; the owner asked for import first.
      3. Report exact counts on the first import run (imported / skipped), and
         verify by re-reading the DB rather than trusting the return values.

## ITEM L1852 [tier C] section: Config
primary_domain_guess: ci/scripts | all_domains_guess: ci/scripts

- [ ] **A `todo.d` fragment assembled between the PR that files it and the PR
      that finishes it leaves an open task in `TODO.md` for completed work.**
      Hit for real on 2026-08-10; found only because `TODO.md` happened to be
      re-read after the merge. Nothing reported it.

      **Exact timeline** (`git log` on `main`):

          04:25 EDT  a75b9ad2  PR #2272 adds
                               todo.d/20260810-library-exhaustive-deps-…md
          04:51 EDT  6658d1a8  assemble_todo.py folds it into TODO.md
                               and `git rm`s the fragment  [skip ci]
          05:12 EDT  a655753e  PR #2273 does the work and deletes the
                               same fragment

      Result: the fragment is gone, the work is done, and `TODO.md` carries an
      unchecked `- [ ]` entry describing it — including instructions that PR
      #2273 had just proven wrong. Cleaned up by hand in #2274.

      **Why it is easy to miss.** `scripts/assemble_todo.py` *consumes*
      fragments: `git_rm(fragments)` at `main()`'s end deletes each one as it
      folds it in. So by the time the finishing PR merges, the fragment is
      already gone from `main`, and that PR's own deletion of it is a silent
      no-op. The absence of the fragment therefore proves nothing either way —
      it looks identical whether the task was assembled or never existed.

      The window is not narrow: 26 minutes here.

      **A mechanical check is harder than it first looks.** The obvious one —
      "if a PR deletes a `todo.d` fragment, require the matching `TODO.md`
      entry to be checked off" — will not fire reliably, because after a rebase
      the deletion may not be in the PR's diff at all (assemble already removed
      the file upstream). And "flag any `- [ ]` entry whose
      `<!-- file: todo.d/… -->` marker points at a missing fragment" matches
      *every* assembled entry, since assemble always deletes. Both directions
      are dead ends without knowing whether the work happened, which is not
      derivable from the files.

      **So the practical fix is a rule, not a script:** when a PR completes work
      that had a `todo.d` fragment, `grep TODO.md` for it before merging and
      check the entry off there if assemble got to it first. Worth adding to
      `todo.d/README.md` and to the post-task hygiene list in `CLAUDE.md`,
      beside the existing CHANGELOG/TODO/executive-summary triple.

      **If a mechanical guard is wanted anyway,** the least-bad version is
      probably a check on the *PR body*: PRs that say "closes the todo.d
      fragment …" or delete a fragment must also touch `TODO.md`. That is a
      heuristic, and it should be written as one rather than as a guarantee.

## ITEM L1945 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **VGBACKFILL-BOUNDS-FRAGILE** Separately, and still worth doing: the
      version-group backfill's iterator bounds `book:0` .. `book:;` admit only
      IDs whose first byte is `0x30`–`0x3A`. That is correct today only because
      every production book ID happens to be a ULID starting with `0`. It is
      not enforced anywhere — `CreateBook` mints a ULID only `if book.ID == ""`,
      so a caller supplying a letter-leading ID would become permanently
      invisible to this scan with no error. Replace the bounds with a prefix
      scan over `book:` → `book;` and let the existing one-colon structural
      filter reject the secondary indexes, which is what it was written for.
      This is a latent-correctness fix, **not** the cause of any observed
      under-scan.

## ITEM L1957 [tier B] section: Config
primary_domain_guess: internal/server/maintenance_fixups.go | all_domains_guess: internal/server/maintenance_fixups.go

- [ ] **`wipeActivity` dry-run count saturates at 2.** `wipeActivity` in
  `internal/server/maintenance_fixups.go` reports its dry-run row count from
  `svc.Query(ctx, ActivityFilter{Limit: 1})`'s `total`. Since the bounded-scan
  change in `0adf6e97`, `total` is a LOWER BOUND: the walk stops once it has
  collected `Offset+Limit+1 == 2` matches, so the dry-run preview now reports
  "2" no matter how many activity rows actually exist. The wipe itself is
  unaffected (it calls `WipeAllActivity`), so this is a misleading preview
  rather than data loss — but the preview is exactly what an operator uses to
  decide whether to run the wipe. Fix needs either a dedicated count path or a
  `CountByPrefix`-style call rather than reusing the paged query. Noted inline
  at the call site during the activity-cancellation work (branch
  `fix/activityquery`).

## ITEM L1970 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`WipeAllActivity` still does an uncancellable full scan on a request
  path.** It calls `scanTierKVs(context.Background(), ...)` per tier, and is
  reachable from `handleWipe`. The activity-cancellation work deliberately left
  the maintenance methods (`Prune`, `WipeAllActivity`, `Summarize`,
  `CompactByDay`) context-free per scope, so `Query`/`GetDistinctSources` are
  cancellable but this path is not: an abandoned wipe request still scans every
  tier to completion. Lower severity than the query path (a wipe is rare and
  operator-initiated, not fired on every page load) but it is the same shape of
  defect and the same fix.

## ITEM L1980 [tier C] section: Config
primary_domain_guess: internal/audiobooks | all_domains_guess: internal/audiobooks;internal/server/search_reconciler.go;web (frontend)

- [ ] **SEARCH-CACHE** Search results are not cached anywhere on the server.
      Every keystroke-debounced query re-runs the full Bleve search plus the
      book hydration behind it.

      **Measured by reading the code 2026-08-11:** `AudiobookService` already
      owns a `listCache`, but it is consulted only on the **non-search** branch
      of `GetAudiobooks` (`internal/audiobooks/service_query.go`, cache key
      `all:<limit>:<offset>:p=…:sb=…:asc=…:noq=…`). The `if search != ""` branch
      goes straight to `searchWithBleve` / `store.SearchBooks` and never touches
      a cache on the way in or out.

      The frontend has `web/src/stores/useLibraryCache.ts` (50 entries, LRU-ish
      eviction), so a repeated query from the *same* browser tab may be served
      client-side — but nothing is shared between users, tabs, or the mobile
      app, and a cold tab always pays full cost.

      **Why it is worth doing:** the per-user post-filter path fetches a
      `searchPostFilterWindow`-sized candidate set from Bleve and narrows it in
      Go, so a search is markedly more expensive than a plain list page. That is
      also the reason the cache key cannot be the query string alone.

      **Cache key must include, or it will serve wrong results:**

      - the query string,
      - `limit`/`offset`,
      - `UserID` whenever per-user filters are active (`PerUserFilters` +
        `UserID`) — per-user narrowing happens AFTER Bleve returns, so two users
        running the same query legitimately get different sets,
      - sort field and direction,
      - every `ListFilters` value that participates in post-filtering
        (`LibraryState`, `Tag`/`Tags`, `FieldFilters`, `IsPrimaryVersion`,
        fingerprint status/coverage bounds).

      ⚠️ **Invalidation is the hard part, and it is the same gap as
      [MERGE-CACHE-EVICT].** A cached search that outlives an edit, merge or
      delete shows books that no longer exist, which is exactly the "I merged
      these and still see two copies" confusion. Prefer wiring it to the
      existing search-index dirty-set/reconciler
      (`internal/server/search_reconciler.go`) rather than a bare TTL — or use a
      short TTL (30–60s) as an explicit, documented first cut and say so in the
      log.

      Do NOT cache before deciding invalidation. A stale search result is worse
      than a slow one.

## ITEM L2025 [tier C] section: Config
primary_domain_guess: internal/merge | all_domains_guess: internal/merge;internal/reconcile;internal/server/wire_dedup_routes.go;web (frontend);docs

- [ ] **REVIEW-COMBINE-FIRST** Let the owner combine two books into one, or
      merge them as duplicates, **before** applying metadata — from the same
      surface where they are choosing the metadata. Requested 2026-08-11:
      *"a way to combine two books into one before I apply metadata, or merge
      them as duplicates before I apply my metadata."*

      **The ordering is the whole point.** Today the sequence is forced:
      metadata is applied to whatever book row happens to exist, and only later
      can rows be combined. When one logical book is split across several rows
      (extremely common in this library — see the 199 books exploded into 6,060
      single-file folders), the owner ends up applying the same metadata several
      times to fragments of one book, and the combine afterwards has to
      reconcile competing metadata that need never have diverged.

      Both actions already exist as separate UI flows:

      - **Combine into One Book** — `web/src/components/BatchToolbar.tsx:101`
        → `web/src/pages/Library.tsx:1256` → `POST /api/v1/audiobooks/combine`
        (`internal/server/wire_dedup_routes.go:75`). Hard-deletes the absorbed
        shells.
      - **Merge as Versions** — `web/src/pages/Library.tsx:1232`. Soft-deletes
        the losers and demotes them to non-primary.

      So the backend capability is there; what is missing is reaching it from
      the metadata chooser without losing the chooser's state.

      ⚠️ **Blocked-ish — read first.** Two live defects sit directly under this:

      1. **Version groups with two elected primaries.** Sampled on prod
         2026-08-11: 10 of 15 groups had two members both marked
         `is_primary_version=true`, so the "merged" book lists twice
         permanently. Suspected writers: `internal/merge/service.go:196-206`
         (reuses an existing group ID but only writes the flag on the books
         passed in, never demoting pre-existing members) and
         `internal/reconcile/reconcile.go:770-795`. Building combine-before-apply
         on top of a merge that can leave two primaries will produce more of
         them, faster. **Not verified** as the causal path for those rows.
      2. Applying metadata from the review screen did not reach the files at
         all until the fix on branch `fix/review-apply-writes-tags` — see the
         separate entry.

      Documentation check done 2026-08-11: the universal review queue
      (`docs/plans/2026-07-13-review-queue-and-regroup.md`, shipped July,
      `review_apply_enabled` defaults OFF) is **regroup-only**. It does not
      cover user-initiated combine, dedup review, or metadata review. The bulk
      metadata review plan
      (`docs/archive/superpowers/plans/2026-04-06-bulk-metadata-review.md`) and
      the dedup label review panel
      (`docs/archive/agent-tasks/dedup-ui/TASK-04-label-review-panel.md`) were
      both archived without shipping. The owner's instinct that these want one
      home is right; that home does not exist yet.

## ITEM L2077 [tier C] section: Config
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] 🚨 **E2E runs in one worktree can silently be served by a DIFFERENT
      worktree's server, and `global-setup.ts` does not catch it.** Hit for real
      on 2026-08-11 while gating `fix/library-load-freeze`. This is a false-green
      generator and it affects every agent running e2e concurrently.

      **What happened.** `playwright.config.ts` hardcodes `127.0.0.1:8484` and
      sets `reuseExistingServer: !process.env.CI`. There were 11 worktrees
      checked out. The sequence:

      1. Killed `:8484`, confirmed free.
      2. Ran `npm run build && go build` in my worktree (~3 minutes).
      3. During that window, a sibling worktree's agent started ITS server on
         `:8484`.
      4. My server launched, failed with
         `listen tcp 127.0.0.1:8484: bind: address already in use`, and did not
         exit — it just never served.
      5. `curl :8484` returned `200`, so everything looked healthy.
      6. My gate ran green-path assertions against the **sibling's build**, which
         contained none of my changes, and reported 4 failed / 1 passed.

      I only noticed because the numbers were *identical* to the pre-fix run —
      42,017 DOM nodes, requested limits `["1000","20"]`. Had my change been a
      no-op in the other direction, this would have been a **false green** and I
      would have shipped it.

      **Why the existing guard misses it.** `global-setup.ts` asserts the served
      bundle is not older than local build artifacts. A sibling that just rebuilt
      passes that check comfortably. The guard answers *"is it stale?"* — the
      question that matters here is *"is it mine?"*

      **Fix shape**, either or both:

      1. **Derive the port from the worktree** so collisions are impossible —
         e.g. hash the worktree path into a port, or read `E2E_PORT` with a
         per-worktree default. Then two agents cannot contend at all. (Workaround
         used on the day: a throwaway config with `--port 8585` and
         `reuseExistingServer: false`. It worked — full chromium suite 283
         passed / 0 failed / 21 skipped — but it was scratch, not committed.)
      2. **Assert identity, not freshness.** Have `global-setup.ts` fetch the
         served `index.html`, resolve the hashed asset filenames, and require
         they match the filenames present in the local `web/dist`. A sibling's
         bundle has different content hashes, so this fails immediately and
         loudly.

      Also worth fixing: a server that fails to bind should **exit non-zero**
      rather than staying alive doing nothing. That hung process is what made
      `lsof`/`ps` look reassuring while the port belonged to someone else.

## ITEM L2181 [tier C] section: Config
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/itunes;internal/readstatus

- [ ] **PLAYBACK-IMPORT** Listened / in-progress status is not coming across from
      iTunes (or from the files), so a book the owner has already finished still
      shows as unplayed here, and a book they are part-way through shows no
      progress. Reported 2026-08-11: *"I thought we were tracking listened status
      and copying that over from iTunes... it feels like none of the stuff to
      actually make the other features that need those were done."*

      Investigate and report before changing anything — this is suspected to be
      an **unwired pipeline**, the same shape as two other defects found the same
      night (the iTunes playlist importer was never called; nothing ever
      scheduled a folder scan). Confirm which of these is true rather than
      assuming:

      - Does the iTunes importer **read** the ITL/XML play-count, `Played`
        flag, and bookmark/position fields at all? If it parses them, where do
        they land?
      - Is there a **write path** from those parsed values onto the book /
        book_file rows (read status, progress position)? `internal/readstatus/`
        and `internal/itunes/service/position_sync.go` both exist — are either
        actually invoked on import, or only on the 2-way-sync path?
      - Does the **file itself** carry progress (embedded chapter/position
        metadata, `.m4b` bookmarks)? If so, is it read on scan?
      - Does the **API expose** listened/progress to the UI, and does the UI
        render it? A value that is stored but never surfaced looks identical to
        one that was never stored.

      ⚠️ Related known defect — do not repair progress data until it is fixed:
      silent-failure **Wave 5** covers `internal/itunes/service/position_sync.go`
      (lines 85, 118), where a *failed read* is indistinguishable from "no prior
      state", so the iTunes bookmark **overwrites the user's real playback
      position**. Backfilling progress through that path could destroy the very
      data this task is meant to restore. Wave 5 lands first.

      Also relevant: `internal/readstatus/readstatus.go:144` discards the
      existing state and rebuilds a fresh one on a read error, and
      `internal/database/pebble_store_playback.go:107` leaks a stale status index
      entry so a book can appear under two statuses at once.

## ITEM L2219 [tier C] section: Config
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);ci/scripts

- [ ] **LEAKSCAN-SCOPE** `scripts/check-memory-leaks.py` reports a false
      `addEventListener without removeEventListener` when the add is nested more
      than one brace level deeper than the cleanup. Its look-ahead abandons the
      search once `scope_depth < -1`, so an add inside `if (x) { if (y) {...}
      else { add } }` with the matching remove in a `finally` at function level
      is never paired — the two closing braces end the scan first.

      Hit for real on 2026-08-11 in `web/src/utils/apiFetch.ts`: the listener
      *was* removed in `finally`, and CI failed anyway. Worked around there by
      flattening the nesting, which is not a fix — the next correctly-cleaned-up
      listener at that depth will fail the same way, and the obvious "fix" a
      future contributor reaches for is deleting the check.

      Proper fix: pair by handler identity within the enclosing function rather
      than by brace-depth proximity, or track the function body extent instead
      of a running depth counter. Whatever is chosen, add a regression fixture
      with the add nested two levels below the remove so the heuristic cannot
      silently regress.

## ITEM L2238 [tier C] section: Config
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **REVIEW-PREVIEW** Play the first ~2 minutes of audio directly from the
      metadata chooser. Requested 2026-08-11: *"I need a way to play the first 2
      minutes of audio right from the metadata chooser."*

      **Why it matters more than it sounds.** The chooser asks the owner to
      confirm a candidate match, but everything on screen is second-hand — a
      title string, a cover, a narrator name. The only ground truth for "is this
      actually the right book and the right narration" is the audio itself, and
      today confirming means leaving the UI entirely. For a library with known
      title contamination and mis-grouped multi-part sets, a 2-minute listen is
      the cheapest possible verification.

      Notes before designing:

      - A range-request audio endpoint may already exist for the player; check
        before adding a second one. If one exists, the work is UI-only.
      - The chooser row shows a **single file** even for a 40-part book (see
        REVIEW-MULTIFILE-CLARITY below), so "the first 2 minutes" must mean the
        first 2 minutes of **part 1 of the book**, not of whichever file happens
        to be attached to the row. Getting this wrong makes the preview
        actively misleading.
      - Do not stream the whole file. Bound the response; an unbounded
        request-scoped read on this server is exactly the shape that OOM-killed
        production on 2026-08-11.

      Documentation check done 2026-08-11: nothing designed. One backlog entry
      in `docs/archive/backlog-2026-04-10.md` calls it "nice but" and it was
      never specced. Not covered by the review-queue plan.

## ITEM L2329 [tier C] section: Config
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers

- [ ] 🐛 **`GET /audiobooks/soft-deleted` computes its `total` by fetching up to
      10,000 rows and taking `len()`, so the count is silently WRONG above
      10,000 and the server pays a 10,000-row read on every call.** Found
      2026-08-11 while fixing the library load freeze (branch
      `fix/library-load-freeze`); deliberately NOT fixed there, because that
      change is client-side and this one is not.

      `internal/server/handlers/audiobooks/handler.go`, in
      `ListSoftDeletedAudiobooks`:

      ```go
      books, err := h.audiobookService.GetSoftDeletedBooks(ctx, params.Limit, params.Offset, olderThanDays)
      ...
      // Get total count (unpaginated) for proper pagination support
      allBooks, _ := h.audiobookService.GetSoftDeletedBooks(ctx, 10000, 0, olderThanDays)
      total := len(allBooks)
      ```

      Two separate problems:

      1. **The count saturates.** A library with 12,000 soft-deleted books
         reports `total: 10000`. Nothing anywhere says the number is a floor, so
         the UI presents a wrong count as an exact one. Note the error from the
         second call is discarded into `_`, so a failed count is reported as
         `total: 0` — indistinguishable from "nothing is soft-deleted", which is
         the more alarming direction to be wrong in.

      2. **The read happens regardless of `limit`.** The client-side fix now
         asks for `limit=1` on mount specifically to avoid pulling 10,000 rows,
         and the handler pulls them anyway to compute `total`. The wire payload
         and the DOM cost are gone (that was the freeze); the server-side read
         is not.

      Fix shape: add a real count to the store layer — `CountSoftDeletedBooks`
      alongside `GetSoftDeletedBooks`, iterating keys without materializing
      book structs — and have the handler call it instead of the
      fetch-and-`len()` trick. Propagate the error rather than dropping it. If
      an exact count is genuinely too expensive, return an explicit
      `total_is_lower_bound: true` so the UI can render "10,000+" honestly,
      but prefer the real count.

      Worth checking for the same fetch-and-`len()` pattern elsewhere in the
      handlers package while in there.

## ITEM L2373 [tier C] section: Config
primary_domain_guess: internal/database | all_domains_guess: internal/database;internal/merge;internal/reconcile

- [ ] **VG-DOUBLE-PRIMARY** A version group can end up with **two** members both
      flagged `is_primary_version=true`, so a merged book shows twice in the
      library forever. Found 2026-08-11 while investigating the owner's report
      that combined books still list individually.

      **Measured on prod, cache-busted so this is not the stale-list defect:**
      sampled 15 non-primary books at `offset=500`; all 15 sat in genuine
      multi-member groups **with** an elected primary — so there is no
      orphan-group defect. But **10 of those 15 groups had two primaries out of
      three members.** Reproduced on the plain (non-search) list path the UI
      uses:

      ```
      ?limit=20&is_primary_version=true&filters=[{"field":"title","value":"Dungeon Tour Guide"}]&_cb=201
      → count=4
        01KNDBMH1SJ29JF4ZTS5NTETPF  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-04-04
        01KZQQVA66GVMFNVWPA9T0V2EE  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-08-11
        01KNDC7G0GCX0ZPTJDXQG8NQ3N  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
        01KZR8862HZJE7AWMTGQFADEHG  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
      ```

      Two groups, four rows. The list filter is an exact index lookup on `true`
      (`internal/database/memdb_summaries.go:133`,
      `internal/database/memdb_reads.go:623-628`), so both members list. This is
      **independent of the response-cache staleness bug** — it persists after a
      cache bust and after a restart.

      **Candidate writers — none verified as the causal path for these rows:**

      - `internal/merge/service.go:196-206` reuses an existing group's ID but
        writes `IsPrimaryVersion` only on the books passed **into** the call.
        Pre-existing members of that group are never demoted. This is the
        strongest candidate.
      - `internal/reconcile/reconcile.go:770-795` promotes `kept` and demotes
        only `originals`, not sibling library copies.
      - `internal/reconcile/reconcile.go:1358-1367` mints a new group + primary.

      The newer half of each observed pair is a `01KZ…` ULID minted 2026-08-11
      at an `organize`d path, which *looks like* the organize/library-copy path
      minting a second primary into an existing group. **Not verified** — do not
      start from that assumption without confirming it.

      **Needs both halves:**
      1. Forward fix — enforce one primary per group. When reusing an existing
         group ID, load every current member and demote them.
      2. Backfill — the existing double-primary rows do not self-heal. Needs a
         maintenance repair op. Scope unknown: 10 of 15 sampled is not a
         library-wide rate, it is a sample of one offset window. **Measure the
         real count before sizing the repair.**

      Add an invariant test that a group can never have more than one primary,
      and run it against the existing data as a diagnostic before writing the
      repair.

      Related but distinct: the 24h list-cache staleness
      (branch `fix/list-cache-generation`) masks merges too, but that one is a
      read-path bug that a restart clears. This one is real, persistent data.

## ITEM L2431 [tier C] section: Config
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **UI-LOCKUP-2** The web interface still locks up despite the virtualization
      work and the earlier backend fixes. Reported 2026-08-11.

      **Do not assume this is still a frontend/DOM-volume problem.** Measured on
      prod the same night, the backend alone can account for a UI that appears
      frozen:

      - `GET /api/v1/audiobooks?library_state=imported&limit=1` took **36
        seconds** — for one row.
      - `GET /api/libraries/{id}/personalized` took **2m10s**.
      - The server was OOM-killed **four times** in ninety minutes, and memdb
        warmup takes **568 s (9.5 min)** during which the library is unusable —
        `library list warm-up: memdb not ready after 5 min, skipping` fires
        because warmup outlives its own waiter, so the list cache never warms.
      - The activity-log query ignores client disconnect, so abandoned requests
        keep scanning; 30 such goroutines were pinning 30 GB with zero clients
        connected.

      A frontend cannot render what it cannot fetch, and a browser tab whose
      requests never return looks exactly like a frozen UI. So the first job is
      to **separate the two**, not to add more virtualization:

      1. Reproduce with DevTools Network open. Are requests **pending** (backend)
        or returning fast while the page janks (frontend)?
      2. If backend: which endpoint, and is it one of the known-unbounded ones?
      3. If frontend: profile it. Is virtualization actually active on the list
        that janks, or only on the one that was fixed before?
      4. Check whether the lock-up correlates with server restarts / warmup
        windows — if it only happens in the ~10 min after a restart, it is
        startup sequencing, not the UI.

      State which of the two it is, with evidence, before writing any fix. The
      previous round of this task was closed against a DOM-volume hypothesis; if
      that hypothesis was wrong, or was right then and is not the binding
      constraint now, say so explicitly.

