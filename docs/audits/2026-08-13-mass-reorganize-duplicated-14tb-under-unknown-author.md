<!-- file: docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f6c1d84-7a92-4b5e-8c03-19d4e7b26af5 -->
<!-- last-edited: 2026-08-13 -->

# A re-organize run on 2026-08-11 wrote 14 TB of duplicate files under `Unknown Author/`

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

Step 4 is the flag bug; steps 1–2 are the disk bug, and the expensive one.

## Disk cost

| measure | value |
|---|---|
| `audiobook-organizer/Unknown Author/` | **14 TB**, 23,622 entries |
| whole organized tree | 22 TB |
| share of the organized tree in that one directory | **64%** |
| pool `bigdata` | 151T / 166T allocated — **14.4T free, 91% capacity** |

Verified duplicates are **`links=1`** — independent copies, not hardlinks, so the
space is genuinely consumed. File sizes match the earlier organized copy exactly.

ZFS performance degrades sharply above ~80% capacity; the pool is at 91%.
Reclaiming these would return it to roughly 82%.

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

**Do not delete `Unknown Author/` wholesale.** 23,622 directory entries versus
9,942 surplus primaries means the tree is not purely duplicates — it will also
contain books whose author genuinely is unknown and which have no second copy.
Deleting blind is data loss.

The safe predicate for reclaiming a file is per-book, not per-directory:

1. the row is a surplus primary in a multi-primary group, **and**
2. another member of that same group is `organized` with a different path, **and**
3. that other file exists on disk and matches on size — ideally on hash, since
   size alone has collisions at this scale.

Only then is the `Unknown Author/` copy provably redundant.

## Open questions

- Why did the run lose author metadata? `Unknown Author` implies `AuthorID` was
  nil or unresolvable at organize time. If the trigger was a transient author
  lookup failure, the same run could recur.
- What invoked it? A scheduled maintenance task, a manual organize-all, or a
  reconcile apply. The operation log for 2026-08-11 07:00–07:05 should name it.
- Are the 839 rows dated 2026-04-04 the same defect or the original import?
