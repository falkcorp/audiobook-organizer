### `repair-missing-files` tier 2 can repoint a row at another book's audio

`rmfr_repairOne` in `internal/maintenance/jobs/repair_missing_files.go` resolves a missing
`book_file` row through four tiers. **Tier 2 (`:292-339`) accepts a unique basename match with no
ownership check at all.**

```go
paths := idx[base]            // filename index across ALL search roots
switch len(paths) {
case 1:
    candidate, method = paths[0], "filename"   // ← :299-301, accepted unconditionally
```

The `default:` branch immediately below (`:304-337`) narrows multi-match candidates by parent
directory and then by author last name. The `case 1:` branch does neither. The asymmetry is the
bug: the code already encodes that a bare basename is insufficient evidence of identity, but
applies that knowledge only when the match is ambiguous. **One match is evidence of uniqueness,
not of correctness** — a singleton basename elsewhere in the search roots is no more likely to
belong to this book than one of several.

A hit rewrites the row via `UpdateBookFile` at `:566`, setting `FilePath`, `OriginalFilename`,
`Missing=false`, `FileSize` and `Format` — so the row ends up pointing at an unrelated book's
audio while *looking* fully repaired. There is no post-write verification of book identity.

**Reachability — measure the ROW side, not the DISK side.** Both numbers exist and only one bounds
the risk. All measurements by the prod-ops lane; recorded here as theirs.

*On-disk* (does the corpus contain singleton basenames at all): 4,082 files carry bare-digit
basenames across 517 distinct names, **170 of them singletons**. Controls: normal-named mp3s under
one author = 35; a planted nonexistent name = 0.

*Row-side* (do actual missing rows resolve to a singleton — the only way to reach `case 1:`):
building the same index tier 2 builds, 379,527 distinct basenames over both search roots, and
looking up every distinct basename from 260 sampled missing rows gives **1 singleton
("Dungeon of Pride.m4b") / 101 multi / 1 absent** (planted control ✓).

**So the on-disk figure overstates the risk and the row-side figure is the one to quote: ~1 in
102.** This corrects the first version of this fragment, which cited the on-disk numbers as
reachability — the same count-the-wrong-population error the audit header warns about.

The track-slash population specifically does **not** mis-repoint: `131.mp3` occurs **9** times, not
once (settled by direct `find` after two parses disagreed; known-good control `166.mp3` = 172,
planted bad control = 0). Nine occurrences reach the multi-match branch, which narrows by parent
directory — stored parent `Zero History - 2` matches none of the nine real parents — and falls
through to zero. **A miss, not a mis-repoint.**

⚠️ Consequence for prioritisation: this defect is real and worth fixing on its own merits, but it
is **not** what blocks the track-slash repair. Do not sequence the repair behind it.

Bare-digit basenames are common because of the `segment_title_format` slash bug (default
`{title} - {track}/{total_tracks}`, `f29c3ce6` → `c54721c7`, documented at
`internal/organizer/pathbuild.go:139-158`): a row reading `.../Zero History - 70/131.mp3` is one
filename, "track 70 of 131", whose `/` became a path separator. Its basename is `131.mp3`.

**These two findings compound, which is why neither should be fixed in isolation:**
`repair-missing-files` advertises `dry_run:true`, and per
`todo.d/20260817-resumerequeue-two-divergent-implementations.md` an interrupted dry run can come
back as a real run through the nil-params requeue path. A silent preview→apply transition on a job
whose tier 2 can repoint across books is a worse outcome than either defect alone. The prod-ops
lane declined to run this job as a prod dry run for exactly this reason and read the tiers
statically instead.

Fix direction: require tier 2's `case 1:` to pass the same parent-directory / author narrowing the
multi-match branch already applies, or reject the single match outright and let tiers 3-4 handle
it. Either way the accept path should carry a same-book assertion before `UpdateBookFile`.

Separately — and this does **not** fix the above — none of the four tiers resolves the track-slash
shape, so repointing that population needs new candidate logic rather than a wiring change:

- tier 1 (`:281`, iTunes PID → XML Location): organizer-tree rows have no `ITunesPersistentID`.
- tier 2 (`:292`): looks up `131.mp3`; the real file is `Zero History - 70.mp3`.
- tier 3 (`:341`, stem-prefix in same dir): `os.ReadDir` on the phantom parent — 25/25 distinct
  parents absent on the live tree, with three positive controls present in the same batch.
- tier 4 (`:366`, author + title-prefixed album dir): stats `<album>/131.mp3` (absent), and its
  single-audio-file fallback does not apply to books holding 130+ files.

`repair_missing_files.go` remains the right model for the *write* (`:566` field set) and for
dry-run-returns-a-plan (`res.Method` / `res.NewPath` per row); only the candidate search is unfit.
