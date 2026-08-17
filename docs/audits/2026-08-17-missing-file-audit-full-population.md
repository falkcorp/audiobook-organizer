<!-- file: docs/audits/2026-08-17-missing-file-audit-full-population.md -->
<!-- version: 1.5.0 -->
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

   ⚠️ **Tier 2 also carries a latent cross-book corruption risk — but it is RARE
   for this population, and an earlier version of this section overstated it.**
   When the index holds exactly one path for the stored basename, `case 1:`
   accepts it as `method="filename"` with **no check that the file belongs to the
   same book**; only the *multi*-match `default:` branch narrows by parent
   directory and author. The asymmetry is the tell: the code already knows a bare
   basename is insufficient proof of identity — that is why the ambiguous path
   narrows — and then applies that knowledge only when the match is ambiguous.
   One match is evidence of *uniqueness*, not of *correctness*; the count says
   nothing about ownership. (Framing due to the maintenance lane.)

   **How often it would actually fire here — measured, and lower than I first
   implied.** Building the same index shape tier 2 uses (379,527 distinct
   basenames over both search roots) and looking up every distinct basename from
   the 260 sampled missing rows:

   ```
   sample basenames: 102 real + 1 planted control
     SINGLETON (tier-2 auto-accept) :   1   — "Dungeon of Pride.m4b"
     multi (goes to narrowing)      : 101
     absent from index              :   1   — the planted control ✓
   ```

   The dominant basenames are **not** singletons — verified directly, with
   controls, rather than via the index parse: `131.mp3` = **9** occurrences
   (69 of the 200 sampled rows carry it), against a known-good control
   `166.mp3` = 172 and a planted bad control = 0. So for the track-slash
   population tier 2 lands in the narrowing branch, narrows to zero (the stored
   parent `Zero History - 2` matches none of the nine real parents), and falls
   through — a miss, not a mis-repoint.

   🔻 **Correction to an earlier draft.** It cited "4,082 bare-digit files, 517
   distinct names, 170 singletons" as if that measured the risk to this
   population. It does not: those count files **on disk**, not the basenames of
   **missing rows**. The on-disk figure establishes the branch is reachable in
   principle; the row-side figure above (1 of 102) is the one that bounds how
   often it fires here, and it is small. The defect in tier 2 is real and worth
   fixing on its own merits — it is not what blocks this repair.

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

⚠️ **`rows_to_delete=20000` was the `max_deletes` cap, not the real total.** That
run logged `plan truncated by max_deletes {cap: 20000, run_again_to_continue:
true}`; `missingFileRepairDefaultMax` is 20,000 (`missing_file_repair.go:52`).
A capped apply would report "deleted 20,000" and look finished while leaving the
rest, so any apply must read `CappedAt` explicitly.

### ✅ The true total, now measured

Re-run uncapped — op `01M08ECGMCCP5TTDH6ZGANVA7H`, `{"max_deletes": 1000000}`,
167s, completed 2026-08-17 18:08:08Z, still a dry run (`apply=false` in the
start log):

```
books=61528 repairable=1386 fully-broken(skipped)=16265
unreadable(skipped)=0 intact=43877 rows_to_delete=25677
```

**25,677 rows**, and **no `plan truncated` warning was emitted**, which is what
establishes this as the whole plan rather than another ceiling. The default cap
was hiding 5,677 rows — 22% of the plan.

That also splits the missing population for the first time:

| | rows |
|---|---|
| missing rows in **repairable** books (the delete plan) | **25,677** |
| missing rows in **fully-broken** books (skipped entirely) | **46,277** |
| total missing | 71,954 |

So **64% of the missing rows belong to books the repair will not touch at all** —
the 16,265 books that will not load. The delete plan addresses the smaller share.

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

## 8. The books that will not load are mostly RECOVERABLE

The 16,265 fully-broken books are the user-visible symptom ("a lot of books that
won't load"). The repair skips them, so the question is not whether to delete
them but whether their audio still exists. Measured on the 60 fully-broken book
IDs the op reports, by testing each book's own `file_path` on disk — controls in
the same batch: a planted path ABSENT ✓, a known-good directory present ✓:

| what is at the book's `file_path` | count of 60 |
|---|---|
| **a real audio file**, present right now | **12** |
| a **directory** containing audio (iTunes author folder) | **43** |
| **genuinely absent** | **5** |

**Only ~1 in 12 of these books has actually lost its bytes.** For the rest the
pointer is broken, not the file. Deleting their rows would convert a fixable
index problem into permanent loss.

⚠️ **Sample of 60 in iteration order, clustered by book — proportion-in-sample,
not a population estimate for all 16,265.**

**These are a DIFFERENT shape from the track-slash population in §2**, which
matters because it means one repoint implementation will not cover both:

- **The 12 file-hits are the easy case.** The book already records a correct path
  to existing audio, e.g. `…/Drake O'Keef/The Seven Deadly Demons/Dungeon of
  Pride/Dungeon of Pride - Drake O'Keef - read by Steve Campbell.m4b`. Only the
  `book_file` row disagrees.
- **The 43 directory-hits are iTunes-tree books** whose `file_path` resolves to an
  *author* folder holding hundreds of files (Jim Butcher 1,033; Stephen King
  1,555). Recoverable in principle, but selecting the right file needs a
  title-matching step that can pick wrong — the same hazard as
  `repair-missing-files` tier 2 (§4). The iTunes tree is hands-off for writes;
  everything here was read-only.

## 9. Relink vs remove — and how to verify a relink

`repair-missing-files` dry run, op `01M08FE40VYSMXC7HG2THFHJ6H`, triggered with an
**explicit** `{"dry_run": true}` rather than relying on the advertised default
(prod advertises `dry_run:true`; the control `backfill-file-hashes` advertises
`false`, so the field discriminates).

| outcome | rows |
|---|---|
| **could relink** | **22,922** |
| unresolved — no candidate | **47,920** |
| ambiguous — refused to guess | **1,034** |
| *classified* | *71,876 of 71,954* |

By method — and the second row is the problem:

| method | rows |
|---|---|
| `pid` (iTunes PID → XML) | 9,104 |
| **`filename` (unverified tier-2 auto-accept)** | **8,128** |
| `author_title` | 4,486 |
| `flat_stem` | 1,193 |
| `truncation` | 11 |

🔴 **35% of proposed relinks use the one method with no same-book check.** §4's
row-side measurement (1 singleton in 102 sampled basenames) said this fires
*rarely in the track-slash population* — that still holds, and it is exactly why
it under-called the whole-library figure. The sample was not drawn from where
this method fires. 8,128 is the number that matters.

⚠️ **Correction:** §4 says "four tiers". There are **five** — a tier 4b
`flat_stem` (`:435`) handles flat iTunes author dirs. It does not rescue the
track-slash shape (stored stem `131` matches nothing), so §4's conclusion stands,
but the count was wrong.

### What can actually verify a relink — measured

Signal coverage across 2,077 `book_file` rows of the 60 fully-broken books:

| signal | coverage |
|---|---|
| `duration` | **100%** |
| `file_size` | **100%** |
| `track_number` | 91.8% |
| `original_filename` | 12.5% |
| `file_hash` | 2.2% |
| `intro_transcription` | 0.4% |
| `transcribed_title` | 0.3% |
| **AcoustID fingerprint** (`acoustid_seg0`) | **0%** |

**The content signals are absent exactly where they would be needed.** Fingerprint
coverage on these rows is zero and transcription is under 1%, so neither can
adjudicate this repair today. What survives is `file_size` + `duration`, both at
100%.

**`file_size` works, but only with a tolerance.** Compared stored size against the
on-disk file for the 7 single-file fully-broken books whose path resolves:

```
SIZE-MATCH   5 / 7
SIZE-DIFFER  2 / 7   stored 412,981,032 vs disk 412,982,098  (Δ 1,066 B)
                     stored 110,775,438 vs disk 110,775,503  (Δ    65 B)
```

Both deltas are sub-2KB on files of 110–413 MB — the signature of a **tag rewrite
after the size was recorded**, not a different file. So an equality test would
reject two correct matches. Use a tolerance (a few tens of KB), and corroborate
with `duration`, which a tag write does not change.

**Recommended verification before any repoint write**, in cost order — all CPU,
none needing the GPU:

1. `os.Stat` the derived candidate — it must exist.
2. `file_size` within tolerance of the stored value.
3. `duration` match via ffprobe (corroborates, and is immune to tag writes).
4. `file_hash` / fingerprint / transcript **when present** — decisive, but only
   2.2% / 0% / 0.3% of the time, so they are a bonus, not the gate.

That is a real identity check in place of "this basename was unique."

### 🐛 Separate defect found here: the `missing` flag is stale

Every one of the 2,077 rows above reports `missing: false` and
`file_exists: true` via `/audiobooks/:id/files`, while the audit proves each
path fails `os.Stat`. These are stored columns that nothing maintains. **Do not
filter on them** — and anything that already does is silently wrong.

## 10. Open items this leaves behind

1. 🛑 **16,265 fully-broken books await a human decision.** Untouched.
2. ✅ **The repairable-row total is now measured: 25,677** (§7), with 46,277
   missing rows sitting in the skipped fully-broken books. No longer a floor.
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
