<!-- file: docs/audits/2026-08-17-missing-file-audit-full-population.md -->
<!-- version: 1.2.0 -->
<!-- guid: 75babbdd-5bf3-41e9-a8ba-5281df2898f9 -->
<!-- last-edited: 2026-08-17 -->

# Missing-file audit, full population — and why the approved repair must NOT run

**Status:** 🔴 **`maintenance.missing-file-repair {"apply": true}` is BLOCKED.**
Option A ("delete dead rows only where the book keeps a surviving file") was
approved against a premise that the full-population measurement disproves for a
verified sub-population: **the bytes are on disk under a different filename.**
Deleting those rows destroys the only pointer to files that exist.

Nothing was modified. Both ops run below are report-only in effect
(`missing-file-audit` never requests `CapLibraryWrite`; `missing-file-repair`
was run with `apply` unset, which is a dry run).

## 1. The numbers, measured over the whole library

`maintenance.missing-file-audit`, op `01M0884EN40QHADKPK2WSGD82G`, run with **no
`path_prefix`** (see §5 for why the prefix must be omitted), `sample_limit: 200`.
Completed 2026-08-17 16:18:50Z.

| metric | value |
|---|---|
| `book_file` rows swept | **532,296** |
| missing | **71,954** (13.52%) |
| present | 460,342 |
| **unreadable** | **0** |
| books | **61,528** |
| books fully broken (every row dead) | **16,265** (26.4%) |
| books partially broken | 1,386 |
| books intact | 43,877 |

`unreadable = 0` matters because it is what separates "the bytes are gone" from
"I could not tell" — the op counts those separately by design, and a book with
any un-stat-able row is skipped entirely by the repair.

⚠️ **Exactly zero across 532,296 network stats deserves the obvious question: is
the `fileUnreadable` branch reachable at all?** It is —
`missing_file_audit_test.go:149` `TestMissingFileAudit_UnreadableIsNotCountedAsMissing`
drives a non-`IsNotExist` stat error and asserts `Unreadable == 1`, so the branch
is live and covered. What is *not* established is that this NAS mount can
actually produce such an error under load; the 0 is therefore "no error was
observed", not "no error is possible". It is strong enough to rely on for the
repair's skip rule and not strong enough to assert the mount was healthy
throughout.

### This supersedes the 120-book extrapolation — at the BOOK level

The prior figures came from a 120-book sample. One of the two comparisons holds
and one does not:

| | sampled (120 books) | measured (61,528 books) |
|---|---|---|
| books with no surviving file | 5 (4.2%) | **16,265 (26.4%)** |
| rows missing | 41.8% (552/1,322) | 13.52% (71,954/532,296) |

**Only the book-level row is a like-for-like rate comparison.** The fully-broken
book rate was understated by ~6×.

⚠️ **Do not quote the row rates as a "3× overstatement".** They are not
apples-to-apples: the sample averaged ~11 rows/book against the population's
~8.6, and there is no record that the 120 books were selected the way the
population sweep enumerates. Different denominators can produce both numbers
with neither being wrong. The row figure is reported here as the measured
population value, not as a correction of the sample.

🛑 **The standing STOP-AND-ASK is about "5 books". It is 16,265 books.** That is
a different decision and it is still yours to make — nothing was deleted,
relocated, or marked.

### Missing rows by tree

| tree | missing rows |
|---|---|
| `/mnt/bigdata/books/audiobook-organizer` | 67,722 |
| `/mnt/bigdata/books/newbooks` | 3,165 |
| `/mnt/bigdata/books/itunes` | **1,006** |
| `/X:/books/itunes/Audiobooks` | **61** |

Two of these contradict the documented shape and are findings in their own right:

- **iTunes was supposed to be zero.** The op's own header comment records "EVERY
  missing path was under the organizer's own destination tree … while nothing
  under the iTunes tree was missing." 1,006 rows now say otherwise. It is 1.4% of
  the missing population, so it does not overturn the main story, but the claim
  as written is false and the iTunes tree is hands-off, so these want a separate
  look rather than a repair.
- **`/X:/books/itunes/Audiobooks` is a mangled Windows path** — a drive letter
  that got a POSIX root glued to its front. 61 rows. Unrelated to everything
  below; filed here so it is not lost.

## 2. Root cause of the largest identified sub-population: an unsanitized `/`

The audit's sample paths have a shape that names its own cause:

```
row says: …/William Gibson/Blue Ant 3 - Zero History/Zero History - 70/131.mp3
                                                     └──────── one filename ────────┘
```

`Zero History - 70/131` is not a directory plus a file. It is the single filename
**"track 70 of 131"**, whose `/` was never sanitized and so became a path
separator. The row records a phantom directory and a phantom `131.mp3` inside it.

**This is a known, already-diagnosed incident, and the code hole is already
closed.** `internal/organizer/pathbuild.go:139-158` documents it:

> From 2026-03-03 (`f29c3ce6`) to 2026-08-15 (`c54721c7`) the SHIPPED DEFAULT of
> `segment_title_format` was `"{title} - {track}/{total_tracks}"`, and every
> multi-file book organized in that window exploded one directory per track:
> 2,535 bogus directories, 2,584 stranded files, 35.2 GB, 77 books with no other
> copy.

Both commits verified at HEAD (a deliberately bogus SHA in the same call failed
to resolve, so the lookup discriminates):

- `f29c3ce6` 2026-03-03 — `feat: add smart apply pipeline with config, segment titles, and file rename`
- `c54721c7` 2026-08-15 — `feat(config)!: delete path_format/segment_title_format, default file pattern to {title} - {track:02d}`

What remains is the **database residue** of that incident. The disk was
repaired — files now sit flat under the new `{track:02d}` default — but the rows
still point at the old exploded paths.

## 3. The discriminating test: the bytes are there

Run per `docs/audits/2026-08-17-orphan-destination-rows-root-cause.md` §"The
discriminating test", against the live NAS.

**Instrument control (same call, required to differ):** a directory known to hold
files listed 3 entries; a bogus path errored `No such file or directory` on
stderr. No `2>/dev/null` anywhere.

```
row:      …/Blue Ant 3 - Zero History/Zero History - 70/131.mp3      → MISSING
phantom:  …/Blue Ant 3 - Zero History/Zero History - 70/             → does not exist
reality:  …/Blue Ant 3 - Zero History/Zero History - 70.mp3          → 4,015,202 bytes, Apr 28 13:39
control:  …/Blue Ant 3 - Zero History/Zero History - 999.mp3         → No such file (as required)
```

Second book, same result: `…/Unbound Deathlord 3 - Corruption/Corruption - 20.mp3`
= 61,019,048 bytes. The parent directory holds 130 flat `Zero History - NN.mp3`
files.

### Measured across every track-slash row in the sample

Of the 200 sampled missing paths, **101 match the track-slash shape**. For each,
the expected flat name under the new default (`{stem} - {track:02d}.{ext}`) was
derived and tested on disk, with **two deliberately bogus paths appended to the
same batch**:

```
PRESENT = 101   ABSENT = 2   TOTAL = 103
ABSENT: /mnt/bigdata/books/audiobook-organizer/__CONTROL_MUST_BE_MISSING__.mp3
ABSENT: /mnt/bigdata/books/itunes/__CONTROL2_MUST_BE_MISSING__.m4b
```

**101 of 101 recoverable. Both planted controls correctly absent.** These rows'
bytes exist. They land squarely in the first row of the root-cause doc's decision
table: **repoint, do not delete.**

### The other 99 sampled rows are genuine loss

The remaining 99 are almost entirely one cluster
(`…/The Saga of Recluce/The Saga of Recluce - 01 - The Magic of Recluce/…`).
Verified directly rather than by shape: the **series** directory
`…/L. E. Modesitt Jr./The Saga of Recluce` exists but holds only two entries —
`- 17 - Cyador's Heirs` and `- 18 - Heritage of Cyador` — and **both are empty
directories**. The `- 01 - The Magic of Recluce` book directory is absent
outright. So there are no bytes anywhere under that book, and deletion is the
correct repair for this shape.

(An earlier draft of this doc said the "entire directory is absent". The series
directory is present; the book directory under it is not. The conclusion is
unchanged and slightly strengthened — the surviving siblings are empty shells.)

⚠️ **The 101/99 split is NOT a population estimate.** The audit collects its
sample as the first N missing rows in iteration order, so it is clustered by
book, not random. What the sample establishes is *existence and mechanism* — that
a recoverable population exists and is large enough to appear immediately — not
its size. Sizing it needs a shape-classifying pass over all 71,954 rows.

## 4. What this means for the repair

`maintenance.missing-file-repair` has no repoint mode; it only deletes
(`missing_file_repair.go:287` `applyMissingFileRepair` → `DeleteBookFilesByIDs`).
Its per-book safety rule prevents **emptying a book** — it skips books where every
row is dead. It does **not** prevent **orphaning bytes**: a book with one live row
plus one track-slash row is classified *repairable*, and the delete takes the
track-slash row with it, discarding the only pointer to a file that is present.

So the recoverable population is not merely unprotected by the safety rule — it
is precisely the population the safety rule waves through.

**Recommended sequencing:**

1. **Classify before deleting.** Add a shape pass over all 71,954 missing rows
   that separates track-slash rows (recoverable) from vanished-directory rows
   (genuinely dead). This is a read-only op and it is the missing input to every
   decision below.
2. **Repoint the recoverable rows** to the `{track:02d}` name, verified by
   `os.Stat` before the write — never by string transform alone. Note the naive
   transform does **not** work: `SanitizePathComponent` maps `/` → space, giving
   `Zero History - 70 131.mp3`, but the file on disk is `Zero History - 70.mp3`.
   The correct target comes from the *new* naming default, not from collapsing
   the old string.

   ⚠️ **A repoint implementation may already exist — evaluate before building.**
   Reported by the maintenance lane (session `ao-fixes-2`):
   `internal/maintenance/jobs/repair_missing_files.go`, job `repair-missing-files`,
   sets `FilePath` / `OriginalFilename` / `Missing=false` / `FileSize` / `Format`
   via `UpdateBookFile` at `:566`, is dry-run by default, and returns a per-row
   `Method` + `NewPath` — i.e. a dry run yields a repoint *plan*, which is exactly
   the artifact step 1 needs.

   🔴 **NOW MEASURED — it does not resolve this shape, and one tier can actively
   corrupt.** The maintenance lane flagged this as unmeasured; reading the four
   candidate tiers against our row
   `…/Blue Ant 3 - Zero History/Zero History - 70/131.mp3` (true file:
   `…/Blue Ant 3 - Zero History/Zero History - 70.mp3`) answers it without a prod
   run:

   | tier | mechanism | on a track-slash row |
   |---|---|---|
   | 1 | iTunes PID → XML Location (`:281`) | **miss** — organizer-tree rows have no `ITunesPersistentID` |
   | 2 | exact basename in filename index (`:292`) | looks up `131.mp3`; true file is `Zero History - 70.mp3` → **never matches the right file** ⚠️ see below |
   | 3 | stem-prefix within the same directory (`:341`) | `os.ReadDir` on the phantom parent — **all 25 measured parents are ABSENT**, so this always fails |
   | 4 | author + title-prefixed album dir (`:366`) | stats `<album>/131.mp3` (absent); its fallback accepts only an album holding **exactly one** audio file, and these books hold 130+ → **miss** |

   ⚠️ **Tier 2 is not merely a miss — it is a silent cross-book corruption risk.**
   When the index holds exactly one path for the stored basename it accepts it as
   `method="filename"` with no check that the file belongs to the same book, and
   only the *multi*-match branch narrows by parent directory and author. Measured
   on the live tree: **4,082 files have bare-digit names**, across **517 distinct
   basenames, of which 170 appear exactly once** — so the unverified auto-accept
   branch is reachable, and a hit would repoint the row at an unrelated book's
   audio. (Controls in the same call: normal-named mp3s under one author = 35;
   a planted nonexistent name = 0.)

   **Conclusion: repoint capability for this population must be built, not merely
   wired up.** The correct target is derived from the *new* naming default and
   must be `os.Stat`-verified before the write. `repair-missing-files` remains
   useful as a model for the write itself (`UpdateBookFile` field set at `:566`)
   and for its dry-run-returns-a-plan shape — not for its candidate search.

   ⚠️ **Naming trap, and it has already caused one published error.** These two are
   near-mirrors with opposite mutations — distinguish them by path, never by name:

   | file | id | does |
   |---|---|---|
   | `internal/maintenance/jobs/repair_missing_files.go` | `repair-missing-files` | **repoints** (`UpdateBookFile`), zero deletes |
   | `internal/plugins/maintenance/missing_file_repair.go` | `maintenance.missing-file-repair` | **deletes** (`DeleteBookFilesByIDs`) |

   The other lane published "repair_missing_files deletes book_file rows" on that
   confusion and corrected it in `ea16241f`. Everything else in this document
   refers to the **deleting** op, by path.
3. **Only then** run the delete, scoped to the classified-dead set.
4. The 16,265 fully-broken books remain a human decision (§1).

## 5. Method notes

- **Both ops were run with no `path_prefix`, deliberately.** The prefix filter is
  applied when building `items`, and the per-book roll-up is computed from
  `items` only — so scoping to the organizer tree would classify any book whose
  surviving file lives under iTunes as fully-broken. That fails safe (skipped,
  not deleted) but it inflates `BooksFullyBroken`, which is exactly the number
  under STOP-AND-ASK. Unrestricted also buys the iTunes-tree negative control
  used in §1.
- **A `library.scan` was cancelled first.** The 6-hour unattended scheduler
  (`scheduled.library_scan`, enabled, 360 min) fired op
  `01M087XVMXTDDYD2BVEJ35GC89` at 16:12:29Z, 6h after service start at 10:12:18Z.
  Counts cannot hold still during a scan. Cancelled and verified by reading the
  status back to `interrupted_quiesced` — **not** by the `DELETE` response, which
  returns `204` for a bogus operation ID too and therefore proves nothing.
  It never left the tag-reading phase, so the LLM host GPU stayed at 40 °C.
  **Next unattended fire ≈ 22:12Z.**

## 6. Ops run

| op | id | mode | result |
|---|---|---|---|
| `maintenance.missing-file-audit` | `01M0884EN40QHADKPK2WSGD82G` | read-only | completed |
| `maintenance.missing-file-repair` | `01M088D97B261ZN5FBC816BHTA` | **dry run** | see §7 |

## 7. Dry-run plan — and the direct proof that the apply is unsafe

`maintenance.missing-file-repair` with `{}` (dry run), op
`01M088D97B261ZN5FBC816BHTA`, 163s, completed.

```
books=61528 repairable=1386 fully-broken(skipped)=16265
unreadable(skipped)=0 intact=43877 rows_to_delete=20000
```

⚠️ **`rows_to_delete=20000` is the `max_deletes` cap, not the real total.** The
run logged `plan truncated by max_deletes {cap: 20000, run_again_to_continue:
true}`. `missingFileRepairDefaultMax` is 20,000
(`missing_file_repair.go:52`). The true repairable-row count is **≥ 20,000 and
unmeasured** — a capped apply would report "deleted 20,000" and look finished
while leaving the rest. Any future apply must read `CappedAt` explicitly.

### The delete set was tested directly

The op reports 60 `sample_paths`, drawn **only from repairable books** — i.e. rows
it would actually delete. Classified by shape: **24 track-slash, 36 other.**

⚠️ **The shape regex alone cannot make this call, and its limit is known.** In
the negative-control run it printed `'/x/Title - 3/12.mp3' → True`, which is
correct for the pattern but means it cannot distinguish a phantom directory from
a *real* directory named `Title - 3` that genuinely contains `12.mp3`. The
discriminator is whether the named parent directory exists, and that was tested
for **all 25 distinct parents** of the 60 candidates:

```
ABSENT  25/25 phantom parents
EXISTS  n=34   …/Edward Castle/Unbound Deathlord 3 - Corruption   (good control)
EXISTS  n=2    …/L. E. Modesitt Jr./The Saga of Recluce           (good control)
EXISTS  n=4590 /mnt/bigdata/books/audiobook-organizer             (good control)
ABSENT         /mnt/bigdata/books/__MUST_BE_MISSING__             (bad control)
```

A first pass of this check listed *everything* as absent, controls included — an
instrument that cannot answer "exists" is not measuring anything. The run above
is the re-run with positive controls in the same batch, and it discriminates.
So the 24/36 split is parent-verified, not regex-asserted.

For all 24 track-slash candidates the expected flat name was tested on disk, with
a bogus path appended to the same batch:

```
PRESENT = 24   ABSENT = 1   (the 1 absent is the planted control)
```

**24 of 24 delete candidates have their bytes on disk**, totalling
**768,466,423 bytes (768.5 MB)** across just those 24 files — sizes from 1.7 MB
to 61.3 MB, e.g.
`…/Unbound Deathlord 3 - Corruption/Corruption - 20.mp3` at 61,019,048 bytes.

That is 40% of the visible delete-set sample pointing at extant files, in a plan
of at least 20,000 rows. **The apply was not run.** It stops here pending the
classify-then-repoint sequencing in §4.

### The remaining 36

The other 36 sampled candidates are the `The Saga of Recluce - 01 - The Magic of
Recluce` cluster, whose book directory is absent (§3). Those are genuine loss and
deletion is correct for them — which is exactly why the delete set needs
classifying rather than approving or rejecting wholesale.

## 8. Open items this leaves behind

1. 🛑 **16,265 fully-broken books await a human decision.** Untouched.
2. **The repairable-row total is unmeasured** — the dry run hit the 20,000
   `max_deletes` cap (§7). Every count in this document is a floor, not a total.
3. ⏰ **The unattended `library.scan` scheduler re-fires ≈ 22:12Z**, and a running
   scan is not a passive observer — it clobbers applied metadata (and it moved the
   counts under this audit, which is why it was cancelled at 16:12Z). If this
   audit is to be re-run, or any repair applied, do it away from that boundary or
   disable `scheduled.library_scan` first.
4. **The 1,006 iTunes-tree missing rows and the 61 `/X:/…` mangled Windows paths**
   (§1) are separate findings with no owner. The iTunes tree is hands-off.
5. **`missing_file_audit.go`'s header comment is factually false** — it claims
   nothing under the iTunes tree is missing. The maintenance lane has offered to
   correct it; it is their file.
