<!-- file: docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md -->
<!-- version: 2.0.0 -->
<!-- guid: 3f6c1d84-7a92-4b5e-8c03-19d4e7b26af5 -->
<!-- last-edited: 2026-08-13 -->

# A re-organize run on 2026-08-11 duplicated the library under `Unknown Author/` — but the blocks are shared, not duplicated on disk

Found 2026-08-13 while repairing an unrelated version-group defect. **Nothing has
been deleted or modified as a result of this audit** — it is a findings record.

## The shape of the damage

A full per-group census of all 63,870 books (every page of both the primary and
non-primary sets, not sampled) found:

| version-group shape | groups |
|---|---|
| exactly one primary | ~13,530 |
| **more than one primary** | **10,780** |
| zero primaries | 479 (separately repaired — see `ElectMissingPrimaries`) |

**10,798 surplus primary rows** exist beyond the one-per-group the invariant
allows. Their creation dates are not spread out:

| day | surplus primaries created |
|---|---|
| **2026-08-11** | **9,942** |
| 2026-04-04 | 839 |
| 2026-04-30 | 12 |
| 2026-04-28 | 4 |
| 2026-06-01 | 1 |

The 2026-08-11 rows arrive in a sustained burst — ~550/minute from 07:03, still
running at 22:34. That is one long automated run, not user activity.

## What the run actually did

Sampled 3-member/2-primary groups all show the identical shape (6/6 sampled):

```
primary=False  organized_source  2026-04-04  .../itunes/.../Gertrude Chandler Warner/...
primary=True   organized         2026-04-04  .../audiobook-organizer/Gertrude Chandler Warner/...
primary=True   organized         2026-08-11  .../audiobook-organizer/Unknown Author/...
```

So the run re-organized books that were **already organized**. For each one it:

1. lost the author metadata, filing the result under `Unknown Author/`;
2. wrote a **second physical copy** of the audio;
3. marked the new row `is_primary_version = true`;
4. demoted only the original *source* row — never the already-organized primary.

Step 4 is the flag bug — real, and fixed. Steps 1–2 looked like an expensive
disk bug but measured out as nearly free: the copy is a block clone sharing
extents with its source (see the correction below).

> **CORRECTION (v2.0.0, measured 2026-08-13 20:15).** The headline in this
> document's title is **wrong**: the 14 TB is not 14 TB of consumed space.
> ZFS block cloning is active and working, the duplicate files already share
> blocks with their sources, and **there is essentially nothing to reclaim.**
> The disk-cost section below is retained for the record but is superseded by
> "Measured: the space is already shared" at the end. The *version-group*
> findings — surplus primaries, the organizer defect, the trigger — are
> unaffected and stand.

## Disk cost

| measure | value |
|---|---|
| `audiobook-organizer/Unknown Author/` | **14 TB**, 23,622 entries |
| whole organized tree | 22 TB |
| share of the organized tree in that one directory | **64%** |
| pool `bigdata` | 151T / 166T allocated — **14.4T free, 91% capacity** |

These figures come from `du`, and **`du` is the wrong instrument here** — it
reports each file's own block count and cannot see that ZFS clones share those
blocks, so every clone is counted again. `links=1` only rules out *hardlinks*;
it says nothing about block cloning, which is what is actually happening.

The pool is genuinely at 91% capacity, and ZFS does degrade above ~80% — but
reclaiming this tree would not move that number. See the measured correction
below.

## The code path

`internal/organizer/service.go` (~1170–1320) is correct for a **first** organize:
it creates the organized record as primary and demotes the source. It is wrong on
a **second** organize of a book already in a group — it inherits the existing
`versionGroupID`, creates another record with `IsPrimaryVersion = true`, and
demotes only `book` (the source). The group's pre-existing primary is never
demoted, so primaries accumulate.

Group-id shape corroborates the attribution — the defect is confined to the
bare-ULID minter, which is the organizer's (`ulid.Make().String()` with no prefix
at service.go:1180):

| id shape | groups | 1 primary | >1 primary | 0 primary |
|---|---|---|---|---|
| bare-ULID (organizer) | 17,635 | 6,886 | **10,742** | 7 |
| `vg-<16hex>` (scanner) | 6,230 | 5,945 | 38 | 247 |
| `vg-<ULID>` (iTunes importer) | 924 | 699 | **0** | 225 |

The scanner's own linker (`scanner.go` ~2100) is well behaved: it elects exactly
one primary explicitly by RootDir membership.

## What must NOT be assumed

**Do not delete `Unknown Author/` wholesale.** The tree is not purely
duplicates — it also holds books whose author genuinely is unknown, and a
measured 314 rows where the `Unknown Author` copy is now the **only** surviving
copy of that audio. Deleting blind is data loss.

### The version-group predicate was the wrong test

The first attempt compared each `Unknown Author` file against its **version-group
twin** and concluded only 1,077 of 4,158 were safe to reclaim. That test was too
narrow, and its verdicts are superseded:

| verdict (group-scoped test) | count |
|---|---|
| content identical | 864 |
| dup's content contained in twin | 213 |
| **differs** | **2,767** (1,160 of them single-file vs multi-file set) |
| twin missing from disk | 314 |

The flaw: organize **copies** rather than moves (`reflink → hardlink → copy`, and
it never deletes the source), so every organized file has an ancestor still on
disk — but that ancestor is usually the source in `newbooks/`, which frequently
is **not a member of the same version group**. Comparing against the group twin
therefore compares against the wrong file.

### The correct test is content-addressed, whole-disk

A full walk of `/mnt/bigdata/books` found:

| measure | value |
|---|---|
| audio files on disk | 549,033 |
| **audio files under `Unknown Author/`** | **222,965** (41% of all audio files) |
| **UA files with a same-size file elsewhere on disk** | **215,160 (13.90 TB)** |
| UA files with no size match anywhere outside UA | 7,805 |

So 96.5% of the tree has a candidate ancestor. Size alone is not proof — these
are same-source rips at fixed bitrates and size collisions are common — so the
reclamation predicate is:

1. an identical-content file exists **anywhere** outside `Unknown Author/`, where
   identity is measured on **interior content** (25/50/75% probes), not head/tail
   — ID3/MP4 tag blocks differ between an `Unknown Author` copy and its
   correctly-tagged twin even when the audio is byte-identical; **and**
2. that twin is currently present on disk.

Content verification of the 215,160 candidates is the gate. Nothing may be
deleted on the strength of the size match alone.

### Measured: the space is already shared — there is nothing to reclaim

An earlier revision of this document claimed the duplication was physically
stored, reasoning that `bigdata/BD/bigdata/books` reports 38.6T used against
39.7T logical and that the ~1.1T gap was explained by compression. **That
inference was wrong.** `logicalused` does not inflate to expose cloned blocks,
so the gap says nothing about sharing, and `du` cannot see it either — ZFS
reports each cloned file's full block count, so `du` double-counts every clone.

The correct instruments say the opposite:

| measure | value |
|---|---|
| `bcloneused` | 12.5T |
| **`bclonesaved`** | **21.8T** |
| `bcloneratio` | **2.75x** |
| allocation delta writing 2 GB | +3,409 MB |
| allocation delta **cloning** that 2 GB | **+0 MB** |

`FICLONE` works on this dataset — verified directly with `cp --reflink=always`
and `copy_file_range`, both rc=0. The reflink failures in the application log
are `no such file or directory`: the database pointed at a source path that no
longer exists. Those are failed organizes, not a cloning problem.

**A 50-file pilot settled it.** 50 pairs verified byte-identical by full
SHA-256, snapshot taken for rollback, each `Unknown Author` copy rewritten as a
`--reflink=always` clone of its twin, content re-verified afterwards (50/50
unchanged), then the snapshot destroyed so freed blocks could be released:

```
logical processed : 5.412 GB
TRUE RECLAIM      : +1.531 GB   (allocation went UP, nothing was freed)
control: background allocation drift with no activity = 36 MB/min
```

Had those files been independent copies, releasing them would have freed ~5.4 GB
— far above the 36 MB/min noise floor. It freed nothing, so **the files were
already sharing blocks with their sources.**

Two methodology notes for whoever revisits this:

- A safety snapshot **retains the freed blocks**, so reclaim cannot appear while
  it exists. Measure after destroying it, or the result reads as zero.
- `filefrag` does not work on ZFS (no FIEMAP), so per-file extent sharing is not
  directly observable. Pool-level `bclonesaved` plus a measured pilot is the
  available substitute.

**Consequence: do not pursue reclamation.** Neither deleting the `Unknown
Author` tree nor offline reflink-dedup would return meaningful space, and ZFS
`dedup=on` is a non-starter — it only affects new writes and its DDT would cost
far more RAM than this pool can spare. The remaining duplication is a *library
hygiene* problem (duplicate rows and confusing `Unknown Author` entries in the
UI), not a capacity problem.

## What invoked it

**A bulk organize of `/mnt/bigdata/books/newbooks/audiobooks/`** — the routine
sweep that pulls in recently downloaded books. Server logs across the burst
window show a continuous stream of organize attempts against that tree.

The mechanism, end to end:

1. Newly downloaded files land in `newbooks/`. Many are books **already owned**
   via the iTunes library.
2. The scanner hash-matches the new file to the existing book and places it in
   that book's existing version group.
3. Organize runs against the new row. Its author metadata has not been fetched
   yet, so the target path resolves under `Unknown Author/`.
4. `CreateOrganizedVersion` writes a **second physical copy** there, marks the
   new record primary, and demotes only its own source row — leaving the
   group's earlier organized primary untouched.

So the trigger is not exotic and **it will recur on the next sweep** unless the
double-organize is guarded. Fixing the flag alone would leave the disk cost.

Notably, the operations API records **no organize operation** in the burst
window (27 ops logged that day, none of them an organize), so this ran without
an operation record — the duplication is invisible to any audit that reads the
operation log rather than the books themselves.

The same logs show a high volume of organize *failures* in three distinct
classes — `duplicate file already organized`, `target path already occupied by
a different file`, and `is a directory but single-file organize was requested`.
Failure accounting for exactly these is being addressed separately on
`obs/organize-collision-accounting`.

## Open questions

- Should organize refuse to run against a book whose group already contains an
  `organized` member, or should it demote the existing primary? The first is
  safer; the second is what the current code half-does.
- Why was author metadata unresolved at organize time rather than organize
  waiting for enrichment? That ordering is what put 14 TB under one directory
  name.
- Are the 839 surplus rows dated 2026-04-04 the same defect or the original
  import?
