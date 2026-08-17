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

**Reachability, measured on the live organizer tree by the prod-ops lane (their measurement, not
mine):** 4,082 files carry bare-digit basenames across 517 distinct names, and **170 of those
appear exactly once**, so `case 1:` is reachable rather than theoretical. Controls in the same
call: normal-named mp3s under one author = 35; a planted nonexistent name = 0.

⚠️ **Still unmeasured, and it is the number that decides severity:** how many *actual* missing
`book_file` rows currently carry a basename that is a singleton in the index. That is what turns
this from reachable into actively-firing, and it needs the row set. Measure it before rating this.

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
