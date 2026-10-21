### Fixed

#### Split-books repair no longer proposes merging distinct books as chapters of one work

The split-book detector took a shared author and series plus a run of numbers as
proof that several books were chapters of one, which is also exactly what the
volumes of a series look like. The Repairs lane offered to merge them. The
detector now vetoes a cluster that shows evidence of being several works: two
or more distinct Book/Vol numbers in titles, subfolder names or file names (V2;
the number may be digits, an upper-case Roman numeral I-XL or a number word
One-Ninety-Nine, compounds included, after Book/Bk/Vol); "Stem N: Subtitle" titles with different subtitles (V2b, which now
also sees "Episode N" and "Section N" titles: only Chapter, Track, Part and Disc
markers are stripped first); a folder named for
several works, such as "Books 1-4", a series, collection, omnibus or box set
(V4); a run of years (V5, reported by `sequentialRun` and vetoed by the
detector, not rejected by it); copy/dup file
suffixes (V8); grandparent-shape subfolders that name different things (V9); two
or more distinct ASINs among the members (V7); and
any member at or over `repair_chapter_max_min` (V3). `series_sequence` is shown
as evidence and is not a veto.

`DetectSplitBookCandidates` fails closed, so the scan op, the bulk-merge
candidates and the `merge-split-books` CLI all lose vetoed clusters.
`DetectSplitBookCandidatesWithVetoes` returns them with their reasons. A
vetoed cluster still claims its books, so no later pass re-emits them. The
cluster's number source is now reported truthfully (`filename`, `subdir` or
`filename+subdir`), a cluster whose titles are all distinct is named for its
folder instead of the lexically smallest title, and a number after Book/Vol is
no longer stripped from the suggested title.

The Repairs fixer lists vetoed clusters as held rows with the veto lines in the
evidence, and adds the fragment fixer's checks: `skipped_no_files` (a member has
no file rows), `held_duration_gate` and `held_duration_unknown` (same
`repair_chapter_max_min` limit, unknown before long), and
`held_distinct_works`.

Candidates stored by an earlier scan are snapshots, and the bulk-merge op
(`dedup.split-book-bulk-merge`) and `POST /dedup/split-book-candidates/:id/merge`
used to merge them as written. Both now go through
`dedup.MergeSplitBookClusterRechecked`, which re-reads every member and refuses
(the route answers 409, the op skips the candidate and leaves it stored) unless
the current rows still pass (the check runs under the merge lock, as the
merge's `Precheck`, so a row edited just before the merge is judged as it is
then): no gone or path-less member, a file row on every
member, no file with an unknown (0) duration or one at or over
`repair_chapter_max_min`, and no distinct-works veto. The shape the vetoes need
is inferred from the members' current paths, not from the stored row. A dry run
of the op reports which candidates the real run would refuse. The unused
`MergeSplitBookCluster` wrapper was removed (its callers use
`MergeSplitBookClusterWithOptions` or the rechecked entry point).

### Changed

#### The Repairs apply confirm and result banner name the rows

The confirm said only "Apply N repair row(s)". It now lists the rows being
applied (title and book count, up to ten, then "and N more"), and the "Apply
finished" banner names the rows that were applied. Apply-all reads its first rows
in one small request before the confirm. The chapter limit is shared through
`config.ResolveRepairChapterMaxMin`.
