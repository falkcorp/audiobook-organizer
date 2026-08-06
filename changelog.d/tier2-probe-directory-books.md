<!-- file: changelog.d/tier2-probe-directory-books.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e3b0c95-8d24-4f61-a07c-5b19d2e6a834 -->
<!-- last-edited: 2026-08-06 -->

### Added

#### `maintenance.probe-directory-books`: tier-2 duration probe for the 1,019 directory-shaped books stuck in review

`maintenance.relink-unlinked-books` (PR #2147) relinked 16.0k of 17,149 unlinked
books, but 1,019 directory-shaped books went to review — not because they were
unknowable, but because they were never measured. `classifyUnlinked` calls
`linkintegrity.ClassifyDir(names, subdirs, nil)`: durations are always `nil`,
because a whole-library pass over 44,887 books can afford one DB read and one
`os.Stat` per book and nothing more. Without durations, `ClassifyDir`'s series
guard cannot fire, so it correctly refuses to auto-link and every multi-file
folder is parked with the reason "no durations are known — cannot rule out a
series".

That refusal is right at tier 1 and wrong as a final answer. The new op is the
First Aid tier-2 escalation: the flagged set is ~1,019 folders rather than 44,887
books, which is small enough to afford an `ffprobe` per audio file. It reuses the
tier-1 detection (`classifyUnlinked`) rather than reimplementing it, then re-runs
the *same* classifier through the new `linkintegrity.ClassifyDirProbed` with the
measured durations filled in. Writing a second classifier here would let tier 1
and tier 2 drift into disagreeing about the same folder. Dry run by default;
nothing is written unless the caller passes `{"apply": true}`.

🔴 **Absent evidence means "cannot verify", never "refuted."** A probe that fails
contributes nothing to the verdict — never a zero. A zero duration reads as
"short file, therefore a chapter, therefore safe to merge", and that exact
substitution (`DurationSec == 0` taken as evidence rather than as its absence)
disabled the regroup series guard across 97.5% of the review queue and came
within one apply of merging 41 of 43 distinct novels. The invariant is now
carried structurally by `linkintegrity.ProbedDuration`'s `OK` flag instead of by
convention, and it treats a *successful* `ffprobe` exit reporting zero seconds
(a truncated or header-only container) as unmeasured too — `ProbeDurationSeconds`
deliberately does not validate that itself. A coverage guard additionally keeps
any folder with an unprobeable file in review, because excluding failures alone
still lets a barely-inspected folder pass when its one measured file happens to
read chapter-length.

A missing `ffprobe` makes the op fail before it touches the store, rather than
classifying all 1,019 folders as unknown-duration — which would look exactly like
a successful run that found nothing to do. `internal/audioutil` gained
`LookupFFprobe` / `FFprobeAvailable` / `ErrFFprobeNotAvailable`, following the
detect-or-disable convention `internal/fingerprint` already uses.

Concurrency is a bounded `registry.RunItems` pool at the folder level sized to
`runtime.NumCPU()`, per the CLAUDE.md mandate; files *within* a folder are probed
sequentially, because a nested pool would put NumCPU² `ffprobe` processes on the
box at once. Progress is stamped mid-folder rather than only between folders: the
registry watchdog kills any op quiet for longer than `ProgressTimeout` (default 5
minutes), and a 23-file folder each hitting the 20s per-file `ffprobe` timeout
runs ~7.7 minutes — the same way `maintenance.dedupe-book-file-rows` previously
died at book 19/194. The apply path writes the *measured* duration onto each new
`book_file` row (tier 1 must seed 0, having never probed); a zero there would
leave the regroup series guard just as inert as having no rows at all. Like its
tier-1 sibling, the file contains no `UpdateBook` call at all, so the write-back
wipe class is structurally unreachable.

Verified with `go build ./...`, `go vet`, and
`go test ./internal/linkintegrity/... ./internal/plugins/maintenance/... -race
-count=1` (green). The exclusion guard is mutation-tested: making failed probes
contribute zeros fails three tests, including one whose numbers are chosen so
that exclusion *alone* decides the verdict (4 files sharing a stem, one measured
at 6,000s and three unprobeable — counted as zeros that is 1-of-4 long and
auto-links; excluded it is 1-of-1 long and the series guard fires).
