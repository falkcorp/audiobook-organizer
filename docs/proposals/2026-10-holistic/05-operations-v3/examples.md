<!-- file: docs/proposals/2026-10-holistic/05-operations-v3/examples.md -->
<!-- version: 1.0.1 -->
<!-- guid: ead1652c-f4a7-4477-b22f-271c27847356 -->
<!-- last-edited: 2026-10-08 -->

# Operations v3 — three worked examples, compared with v2

Each example is a real v2 op at HEAD `f7211eb39`, rewritten against the
[`sdk-api.md`](sdk-api.md) sketch. Line counts for v2 are `wc -l` of the real files; v3
counts are of the sketches below, comments excluded. Parent: [`../05-operations-v3.md`](../05-operations-v3.md).

---

## Example 1 — trivial op: `library.size-refresh`

### v2 today

- `internal/server/library_size_refresh_op.go` — 83 lines: a 16-field def literal, a `Run`
  closure, `init()` → `addOpRegistrar`.
- `internal/scheduler/tasks.go:451-470` — 20-line `TaskDefinition` named `library_size_refresh`
  that enqueues it, plus `librarySizeRefreshParams{}`.
- `internal/scheduler/maintenance.go:163` — `"library_size_refresh": "library.size-refresh"`.
- `internal/server/testdata/op_ids.golden` — one appended line.

Defects visible in the v2 code: `Cancellable: true` while `calculateLibrarySizes` takes no
context (`internal/server/server_helpers.go:86`), so Cancel does nothing; three spellings of
the name; progress is `0/1` then `1/1`.

### v3

```go
// internal/library/ops_size_refresh.go
var SizeRefresh = ops.Task("library.size-refresh", ops.TaskSpec[ops.NoParams, SizeResult]{
	Common: ops.Common{
		Title:    "Library size refresh",
		Help:     "Walks the library and import trees to refresh the on-disk size cache.",
		Effects:  ops.ReadOnly(ops.ResFiles), // refreshes an in-memory cache only
		Schedule: ops.InMaintenanceWindow(30).When(func() bool { return config.AppConfig.Maintenance.LibrarySizeRefresh }),
	},
	Run: func(rc *ops.RC, _ ops.NoParams) (SizeResult, error) {
		folders, err := deps.Store.GetAllImportPaths()
		if err != nil {
			return SizeResult{}, err
		}
		lib, imp, err := calculateLibrarySizes(rc.Context(), config.AppConfig.RootDir, folders) // ctx threaded
		return SizeResult{LibraryBytes: lib, ImportBytes: imp}, err
	},
})

type SizeResult struct {
	LibraryBytes int64 `json:"library_bytes"`
	ImportBytes  int64 `json:"import_bytes"`
}
```

Plus `SizeRefresh` in the package's `Ops()` bundle (1 line). The result struct replaces the
two hand-formatted log lines; the generated UI shows it.

| | v2 | v3 |
|---|---|---|
| files touched | 4 | 1 (+1 bundle line) |
| lines | ~110 | ~25 |
| names for the op | 3 | 1 |
| cancel | declared, not real | `Cooperative` default; `opstest.Conformance` fails until ctx is threaded |
| schedule | TaskDefinition + map entry | one `Schedule` field |

---

## Example 2 — batch op: `acoustid.lsh-backfill`

### v2 today

`internal/plugins/acoustid/lsh_backfill.go`, 167 lines. What the code does and what goes
wrong:
- `registry.RunItems` with **no `Concurrency`** (`:109`) — sequential over every book file;
- tallies are plain `int`s read by the `Label` closure (`:154-157`) — correct only because the
  loop is sequential; adding `Concurrency` without also adding atomics makes a race;
- two `sdk.NewProgress` frames by hand (`:84`, `:93`);
- writes with `p.store.UpdateBookFile` directly — no fence, no journal (the index rebuild is a
  side effect of the write hook);
- `ResumeDrop` on a whole-library walk: a deploy discards the run.

### v3

```go
// internal/plugins/acoustid/lsh_backfill.go
var LSHBackfill = ops.Batch("acoustid.lsh-backfill", ops.BatchSpec[ops.NoParams, database.BookFileCore, LSHResult]{
	Common: ops.Common{
		Title:     "Backfill LSH fingerprint index",
		Help:      "Re-saves book files that have a whole-file fingerprint but no LSH index entry. Idempotent.",
		Effects:   ops.Writes(ops.ResBookFiles, ops.ResIndexes),
		Exclusive: "acoustid.fingerprint",
	},
	Source: func(rc *ops.RC, _ ops.NoParams) (ops.Source[database.BookFileCore], error) {
		files, err := deps.Store.GetAllBookFilesCore()
		return ops.Source[database.BookFileCore]{
			Items: files, Key: func(f database.BookFileCore) string { return f.ID },
			Order: ops.Snapshot,
		}, err
	},
	// Concurrency omitted: CPU() is the default.
	Label: func(f database.BookFileCore) string { return f.ID },
	Item: func(rc *ops.ItemRC, _ ops.NoParams, f database.BookFileCore) error {
		switch {
		case f.AcoustIDFingerprintDurationSec == 0:
			rc.Skip("no_fingerprint")
			return nil
		case lshChecker != nil && lshChecker.HasLSHIndex(f.ID):
			rc.Skip("already_indexed")
			return nil
		}
		// Hydrate-then-write happens inside ModifyBookFile; the fn is a no-op
		// because the write hook rebuilds fpidx from the stored bytes.
		_, err := rc.Writer().ModifyBookFile(rc.Context(), f.ID, func(*database.BookFile) error { return nil })
		if err == nil {
			rc.Count("indexed", 1)
		}
		return err // counted as failed; OnItemError default is Continue
	},
	Finish: func(rc *ops.RC, _ ops.NoParams, s ops.Summary) (LSHResult, error) {
		return LSHResult{Indexed: s.Counter("indexed"), Skipped: s.Skipped(), Failed: s.Failed}, nil
	},
})
```

What the runner now does that the v2 author wrote by hand or forgot: worker pool at
`NumCPU`, cancel check per item, atomic counters (no Label race), progress
`{done, total, unit:"files", counters}`, a frozen snapshot plus watermark so a deploy resumes
at the watermark (default `ResumeContinue` for a `Snapshot` source), the fence on every
write, preview by default (a manual run with no mode reports how many files *would* be
re-saved and writes nothing).

`ModifyBookFile` with a no-op fn still writes the row: the v3 Writer treats "fn returned nil"
as "write" for `ModifyBookFile`. A def that wants "write only if changed" uses `Changed`.
Whether a no-op re-save should produce a history row is an open question (§7 of the parent,
Q7); the recommended answer is no history row when no tracked field changed, but an intent
and its clear are still recorded.

| | v2 | v3 |
|---|---|---|
| lines | 167 (≈90 code) | ~40 |
| concurrency | 1 (omitted) | NumCPU (default) |
| race exposure if someone raises concurrency | yes (plain ints in Label) | none (atomic counters) |
| resume on deploy | dropped | watermark |
| writes fenced / journaled | no / no | yes / yes |
| preview | none | default |

---

## Example 3 — trial → approve → apply fixer

Modeled on `maintenance.normalize-letter-l-ordinals`
(`internal/plugins/maintenance/letter_l_ordinal_fixer.go`, 168 lines) running through
`internal/repairs` (`engine.go` 1,039 lines + `writer.go` 356 + `guards.go` 654, shared).

### v2 today

The fixer implements `ID/Title/Description/Plan/Replan/Apply` and an `evaluate` helper. `Plan`
lists candidates and runs its own `RunItems` with a `Label` over an atomic; `Replan`
re-reads one book; `evaluate` sets `r.Fingerprint = junkFingerprint(r, …)` on each of five
return paths. Plans and applies run as two shared ops (`repairs.plan`, `repairs.apply`,
`internal/plugins/maintenance/repairs_ops.go:34,53`) and the lane talks to dedicated
`/api/v1/repairs` routes (`internal/server/wire_repairs_routes.go`). Apply partitions rows and
checks cancel only between partitions (`internal/repairs/engine.go:667-672`, F5).

### v3

```go
// internal/plugins/maintenance/fix_letter_l_ordinals.go
var LetterLOrdinals = ops.Fixer("maintenance.normalize-letter-l-ordinals", ops.FixerSpec[ops.NoParams, database.BookCore]{
	Common: ops.Common{
		Title:   `Letter-l ordinals ("l Volume" -> "1 Volume")`,
		Help:    "Rewrites a leading letter-l ordinal to digits and locks the title it wrote.",
		Effects: ops.Writes(ops.ResBooks, ops.ResFieldStates),
	},
	Candidates: func(rc *ops.RC, _ ops.NoParams) (ops.Source[database.BookCore], error) {
		all, err := deps.Store.GetAllBooksCore(0, 0)
		var out []database.BookCore
		for _, b := range all {
			if !b.IsSoftDeleted() && util.HasLetterLOrdinal(b.Title) {
				out = append(out, b)
			}
		}
		return ops.Source[database.BookCore]{Items: out, Key: func(b database.BookCore) string { return b.ID }, Order: ops.Snapshot}, err
	},
	Load: func(rc *ops.RC, _ ops.NoParams, id string) (database.BookCore, bool, error) {
		b, err := deps.Store.GetBookByID(id)
		if err != nil || b == nil || b.IsSoftDeleted() {
			return database.BookCore{}, false, err
		}
		return b.Core(), true, nil
	},
	Evaluate: func(rc *ops.ItemRC, _ ops.NoParams, b database.BookCore) (ops.Row, error) {
		row := ops.Row{ID: b.ID, Subjects: ops.Books(b.ID), Title: b.Title, Risk: ops.RiskLow,
			Current: map[string]string{"title": b.Title}, Inputs: map[string]string{"title": b.Title}}
		fixed, ok := util.NormalizeLetterLOrdinal(b.Title)
		if !ok {
			row.Hold = "the title no longer uses a letter-l ordinal"
			return row, nil
		}
		// User locks, owner-manual franchises, iTunes paths: framework guards.
		row.Proposed = map[string]string{"title": fixed}
		row.Reason = "the ordinal is the letter l, not the digit 1 / roman I"
		return row, nil
	},
	Apply: func(rc *ops.ItemRC, w ops.Writer, _ ops.NoParams, row ops.Row) error {
		if _, err := w.ModifyBook(rc.Context(), row.ID, func(b *database.Book) error {
			if b.Title != row.Current["title"] {
				return ops.ErrChangedSincePlan // compare-and-set under the write lock
			}
			b.Title = row.Proposed["title"]
			return nil
		}); err != nil {
			return err
		}
		return w.LockFields(rc.Context(), row.ID, "title")
	},
})
```

Flow the framework provides, with no fixer code:
1. `POST /api/v3/ops/runs {def, mode:"preview"}` → plan run; rows stored in the run result;
   the Repairs lane pages them from `GET /api/v3/ops/runs/:id/rows`.
2. A person selects rows; `POST /api/v3/ops/runs {def, mode:"live", plan_ref, rows:[…]}`.
   The runtime checks the approver's permission and verified auth method, records the
   approval on the run, re-`Load`s and re-`Evaluate`s each row, refuses fingerprint changes
   (`changed_since_plan`), holds the scan stand-down with per-write renewal, and applies
   through the fenced Writer, partitioned by `Row.ID`, checking cancel before **every row**.
3. Undo is the run's revert (`POST /api/v3/ops/runs/:id/revert`), reading the history rows
   the Writer wrote after each commit.

| | v2 | v3 |
|---|---|---|
| fixer lines | 168 | ~50 |
| Plan vs Replan | two code paths | one `Evaluate` |
| fingerprint | hand-computed on 5 return paths | framework, from `Inputs` |
| cancel during apply | between partitions | before every row + every write |
| ops / routes per fixer | shared `repairs.*` ops + `/api/v1/repairs/*` | the def itself; generic `/api/v3/ops/*` |

---

## Failure modes compared

| failure mode (finding) | v2 | v3 |
|---|---|---|
| op added but not in the prod binary (F14) | guarded for container plugins only | one catalog; linter + `go list -deps` test + startup ledger check |
| declared schedule never fires (F1) | 14 defs | `Schedule` is the timer; validated at registration |
| sequential by omission (F7) | `Concurrency` defaults to 1 | defaults to `CPU()`; sequential needs a reason |
| Label/tally race (F7) | author's job | counters are atomic and owned by the runner |
| cancel ignored (F4-F6) | cooperative, unverified; slot freed after 5s | fenced writes; slot held until exit; conformance test |
| ledger before write (F10) | 64 hand-written sites | Writer orders intent → write → history |
| unsafe cursor resume (F11) | per-op knowledge | `SourceOrder` + `InsertBelowCursor` fault |
| status classifiers disagree (F2) | 4 functions | one table, generated TS |
| timeout looks like cancel (F3) | `canceled` | `timed_out` |
| timeline read as census (F12) | caller must read three flags | separate census endpoint with exact counts |
| dry-run default by param lint (F15) | 26 files call `ResolveDryRun` | `Mode` is a framework field |
| apply without recorded approval (F16) | Repairs only | any def with `Approval` |
