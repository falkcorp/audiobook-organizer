<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-219-add-a-per-book-tsv-report-artifact-to-the-existi.md -->
<!-- version: 1.1.0 -->
<!-- guid: 60637175-5412-4c12-8b07-1576ab8b4696 -->
<!-- last-edited: 2026-09-02 -->

# TASK-219 — Add a per-book TSV report artifact to the EXISTING dedupe-book-file-rows dry run (DUPROW-2)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — dedupe_book_file_rows.go: no ReportPath/writeDupeRow/tsv hits; runDedupeBookFileRows :157, counters :224. TSV pattern exists metadata_cache_reap.go:223,478 and missing_file_repoint.go:188. Recommendation: keep — pure dry-run observability; pairs naturally with TASK-220.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** Additive report emission on an existing op, but the row collection happens inside a parallel RunItems callback so the accumulator must go under the existing mutex. · **Depends on:** TASK-220 · **Wave:** 4

Source: `TODO.md` line 90031 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90031p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-21.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-219-add-a-per-book-tsv-report-artifact-to-the-existi" -b agent/maintenance-219-add-a-per-book-tsv-report-artifact-to-the-existi origin/main
cd "$REPO/.worktrees/maintenance-219-add-a-per-book-tsv-report-artifact-to-the-existi"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Give the EXISTING `maintenance.dedupe-book-file-rows` op a per-book TSV artifact so a dry run produces a reviewable file instead of ten example strings in a log line. Add a `ReportPath string \`json:"reportPath,omitempty\`` field to DedupeBookFileRowsParams, collect one row per affected book inside the existing RunItems callback (under the existing `mu`), and write the TSV with a new `writeDupeRowsReport` function modeled exactly on `writeReapReport` (internal/plugins/maintenance/metadata_cache_reap.go:478). Columns: book_id, title, rows, distinct, dup_rows, has_fingerprint_on_dupe. The report is written on EVERY run (dry run and apply alike), like missing-file-repoint does. DO NOT CREATE A NEW OPERATION — the TODO text says 'new op' but the op already exists and a second one would duplicate 459 lines of reviewed keeper logic.

## Background (verify before editing)

- maintenance.dedupe-book-file-rows already exists (dedupe_book_file_rows.go:35, registered plugin.go:44), already defaults to apply=false, already uses a bounded worker pool sized to runtime.NumCPU via registry.RunItems (dedupe_book_file_rows.go:252 with RunItemsOptions{Concurrency: workers}), and already counts rows vs distinct paths per book. It is the report op this item describes, minus the artifact.
- Its dry run currently reports only aggregate counters plus up to 10 example strings (dedupe_book_file_rows.go:322-327 builds `examples`), which is not a per-book census.
- The item says to reuse missing-file-audit's artifact writer, but missing-file-audit writes no artifact (grep for tsv/os.WriteFile in missing_file_audit.go returns 0 hits). The real precedents are metadata_cache_reap.go:478 (writeReapReport) and missing_file_repoint.go:490 (writeRepointReport).
- PASS 1 uses the memdb Core projection (GetAllBookFilesCore, L177) which does NOT carry AcoustIDFingerprint — so has_fingerprint_on_dupe must be computed in PASS 2 from store.GetBookFiles (L256), which reads Pebble directly.
- The op is gated on nothing today; unlike some siblings it does not require RootDir, because it reads only DB rows. Do not add a RootDir gate — the TODO's 'gated on RootDir like siblings' does not apply to a pure-DB op and would make it inert (see the standing maintenance-plugin-gated-on-RootDir hazard).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (p \*Plugin) runDedupeBookFileRows" internal/plugins/maintenance/dedupe_book_file_rows.go   # 1 hit at L157 — the op already exists, is bounded-worker-pool parallel via registry.RunItems, and dry-runs by default
  grep -n "p.dedupeBookFileRowsDef()," internal/plugins/maintenance/plugin.go   # 1 hit at L44 — it is already registered in the plugin's def list like its siblings
  grep -n "func writeReapReport" internal/plugins/maintenance/metadata_cache_reap.go   # 1 hit at L478 — the TSV-artifact pattern to copy is writeReapReport in metadata_cache_reap.go
  grep -n "reportPath = filepath.Join(\"reports\"" internal/plugins/maintenance/metadata_cache_reap.go internal/plugins/maintenance/missing_file_repoint.go   # 2 hits: metadata_cache_reap.go:223 and missing_file_repoint.go:188 — both existing report ops derive the default path as filepath.Join("reports", "<op>-<name>.tsv")
  ls internal/plugins/maintenance/reports   # 1 entry: metadata-cache-reap-unknown-op.tsv — the reports/ directory already exists in the package with a committed artifact
  grep -n "tsv\|os.WriteFile\|artifact" internal/plugins/maintenance/missing_file_audit.go   # 0 hits — missing-file-audit (the op the TODO says to compare with) writes NO artifact — it only logs, so there is nothing to reuse from it
  grep -n "var deleted, wouldDelete, failed, recomputed, salvaged int" internal/plugins/maintenance/dedupe_book_file_rows.go   # 1 hit at L224 — the counters the report needs are already accumulated under a mutex inside the RunItems callback
  ```

### Reuse — don't invent

- Use `writeReapReport — the TSV writer to model (header line, tab-joined rows, os.WriteFile 0o664, MkdirAll on the parent)` in `internal/plugins/maintenance/metadata_cache_reap.go` (verify: `grep -n "func writeReapReport" internal/plugins/maintenance/metadata_cache_reap.go`) — do NOT write a parallel helper.
- Use `ReportPath params field + default-path derivation` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n "ReportPath string" internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.
- Use `seedDupBooks / concurrentReporter — existing PebbleStore fixture for this exact op` in `internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go` (verify: `grep -n "func seedDupBooks" internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go`) — do NOT write a parallel helper.
- Use `shortPath — path shortener already in the file` in `internal/plugins/maintenance/dedupe_book_file_rows.go` (verify: `grep -n "func shortPath" internal/plugins/maintenance/dedupe_book_file_rows.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/plugins/maintenance/dedupe_book_file_rows.go, add a field to DedupeBookFileRowsParams (currently L26-33):
	// ReportPath overrides where the per-book TSV lands. Empty means a derived
	// path under reports/. The report is written on EVERY run, dry or apply,
	// so an operator always has the census the summary line only samples.
	ReportPath string `json:"reportPath,omitempty"`
2. Add a row type near dupGroup (L69-74):

// dupeReportRow is one affected book's line in the TSV.
type dupeReportRow struct {
	BookID     string
	Title      string
	Rows       int
	Distinct   int
	DupRows    int
	DupHasFP   bool // at least one of the REDUNDANT rows carries an AcoustID fingerprint
}
3. In runDedupeBookFileRows, next to the existing `var mu sync.Mutex` / counters block (L223-225), add `var reportRows []dupeReportRow`.
4. Inside the RunItems callback, after `byPath` is built (L265-272) and before the group loop, compute the per-book totals: `totalRows := 0; for _, rs := range byPath { totalRows += len(rs) }` and `distinct := len(byPath)`. Track `dupHasFP` as the group loop runs: after `ranked := rankKeeper(rows)` and `keeper, redundant := ranked[0], ranked[1:]` (L299-300), set `if !dupHasFP { for ri := range redundant { if len(redundant[ri].AcoustIDFingerprint) > 0 { dupHasFP = true; break } } }`.
5. Still inside the callback, after the group loop and before the batched delete, append the row when the book actually had duplicates:
	if totalRows > distinct {
		title := ""
		if b, berr := store.GetBookByID(bookID); berr == nil && b != nil { title = b.Title }
		mu.Lock()
		reportRows = append(reportRows, dupeReportRow{BookID: bookID, Title: title, Rows: totalRows, Distinct: distinct, DupRows: totalRows - distinct, DupHasFP: dupHasFP})
		mu.Unlock()
	}
GetBookByID is already in OpsStore via opsBookReader (deps.go:41) — verify with `grep -n "GetBookByID(id string)" internal/plugins/maintenance/deps.go`, which returns 2 hits (L41 in opsBookReader, L146 in ReconcileStore); the L41 one is the one OpsStore embeds.
6. After the RunItems call returns and before the summary is built (around L433), derive the path and write the report:
	reportPath := params.ReportPath
	if reportPath == "" {
		mode := "dryrun"
		if params.Apply { mode = "apply" }
		reportPath = filepath.Join("reports", "dedupe-book-file-rows-"+mode+".tsv")
	}
	sort.Slice(reportRows, func(i, j int) bool { return reportRows[i].BookID < reportRows[j].BookID }) // workers finish out of order; the file must be diffable between runs
	if werr := writeDupeRowsReport(reportPath, reportRows); werr != nil {
		log.Warn("dedupe-book-file-rows: could not write report", "path", reportPath, "err", werr, "rows", len(reportRows))
	} else {
		log.Info("dedupe-book-file-rows: wrote report", "path", reportPath, "rows", len(reportRows))
	}
Add `"path/filepath"` to the import block (sort is already imported).
7. Append the writer at the bottom of the file, copying the shape of writeReapReport (metadata_cache_reap.go:478-492):

// writeDupeRowsReport dumps EVERY affected book and its duplication shape, TSV.
// Written on dry runs as well as applies: the dry run is the artifact an owner
// approves an apply from, and a log line capped at ten examples is not that.
func writeDupeRowsReport(path string, rows []dupeReportRow) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil { return err }
	var b strings.Builder
	b.WriteString("book_id\ttitle\trows\tdistinct\tdup_rows\thas_fingerprint_on_dupe\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%d\t%d\t%d\t%t\n", r.BookID, strings.ReplaceAll(r.Title, "\t", " "), r.Rows, r.Distinct, r.DupRows, r.DupHasFP)
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
Add "os" to the import block. Read metadata_cache_reap.go:478-492 first and match its exact conventions (it may already MkdirAll or not — follow it).
8. Add the report path to the op's summary string (L443-446) so the operator is told where the file went, e.g. append `| report: %s`.
9. Bump `// version:` on internal/plugins/maintenance/dedupe_book_file_rows.go (1.4.0 -> 1.5.0) and set `// last-edited:` to today.
10. Add a changelog fragment under changelog.d/ (no file header in the fragment).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_219.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Zero affected books → still write the file, containing only the header line. An empty file and a missing file are indistinguishable from 'the op did not run'; a header-only file is not.
- A book that becomes unreadable mid-run (GetBookFiles fails, L257-264) already increments `failed` and returns nil — it contributes NO report row. Say so in the summary so 'books_affected' and 'report rows' can legitimately differ.
- GetBookByID failing or returning nil → title is the empty string, not an error. The report is diagnostic; a missing title must never abort it.
- Titles containing tabs or newlines must be sanitized (replace tab with space; also replace \n and \r with space) or the TSV column count breaks.
- params.Limit truncates bookIDs (L208-210) — the report then covers only the processed subset while the summary's `books_affected` covers all of them. State that in the report-written log line so a capped run is never mistaken for a full census.
- reportRows is appended from parallel workers: EVERY append must be inside mu.Lock()/Unlock(). `go test -race ./internal/plugins/maintenance/...` must be run at least once during development.

## Tests

- internal/plugins/maintenance/dedupe_book_file_rows_test.go (EXISTING file — append): `TestWriteDupeRowsReport_HeaderAndRows` — call writeDupeRowsReport(filepath.Join(t.TempDir(), "r.tsv"), []dupeReportRow{...2 rows...}), read the file back, assert line 1 is exactly "book_id\ttitle\trows\tdistinct\tdup_rows\thas_fingerprint_on_dupe" and that each data line splits into exactly 6 tab-separated fields.
- internal/plugins/maintenance/dedupe_book_file_rows_test.go: `TestWriteDupeRowsReport_TitleWithTabDoesNotBreakColumns` — a row whose Title contains a literal tab; assert the data line still splits into exactly 6 fields.
- internal/plugins/maintenance/dedupe_book_file_rows_test.go: `TestDedupeBookFileRows_DryRunWritesReport` (guard with `if testing.Short() { t.Skip(...) }`, same as the parallel tests) — build a PebbleStore with `database.NewPebbleStore(t.TempDir())` + `s.WaitForWarmup()`, seed with the existing `seedDupBooks(t, s, 3, 4)`, run `p.runDedupeBookFileRows(ctx, json.Marshal(DedupeBookFileRowsParams{Apply:false, ReportPath: filepath.Join(t.TempDir(),"r.tsv")}), &concurrentReporter{})`, then assert the file exists, has 1 header + 3 data lines, every row reports rows=4 distinct=1 dup_rows=3, and that `s.GetBookFiles(id)` still returns 4 rows for each book (the dry run mutated nothing).
- internal/plugins/maintenance/dedupe_book_file_rows_test.go: `TestDedupeBookFileRows_ReportRowsAreSortedByBookID` — same fixture with 6 books; assert the data lines are in ascending book_id order, proving the sort survives the out-of-order parallel completion.
- internal/plugins/maintenance/dedupe_book_file_rows_test.go: ANTI-OVER-SUPPRESSION — `TestDedupeBookFileRows_ReportOmitsBooksWithNoDuplicates`: seed 3 duplicated books via seedDupBooks AND create 2 books with a single unique file each; assert the report contains exactly 3 data lines and none of them names a clean book. Without this a writer that emitted a row for every book would pass every other assertion.

Anti-over-suppression test: `TestDedupeBookFileRows_ReportOmitsBooksWithNoDuplicates` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0.
- [ ] `grep -n "func writeDupeRowsReport" internal/plugins/maintenance/dedupe_book_file_rows.go` returns exactly 1 hit.
- [ ] `grep -c "dedupeBookFileRowsDef" internal/plugins/maintenance/*.go` still reports ONE definition — i.e. no second op was created.
- [ ] `grep -n "book_id\\\\ttitle\\\\trows\\\\tdistinct\\\\tdup_rows\\\\thas_fingerprint_on_dupe" internal/plugins/maintenance/dedupe_book_file_rows.go` returns 1 hit (the header is spelled exactly as specified).
- [ ] `go test ./internal/plugins/maintenance/... -run 'DupeRowsReport|DedupeBookFileRows_' -count=1 -v` shows all listed tests PASS.
- [ ] Anti-over-suppression test: `TestDedupeBookFileRows_ReportOmitsBooksWithNoDuplicates` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_219.md`.

## Commit message

```
feat(maintenance): Add a per-book TSV report artifact to the EXISTING dedupe-bo (DUPROW-2)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -n "func writeDupeRowsReport" internal/plugins/maintenance/dedupe_book_file_rows.go` returns exactly 1 hit.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

MOST IMPORTANT: the TODO text says 'New op in internal/plugins/maintenance' — that is WRONG at HEAD. maintenance.dedupe-book-file-rows already exists (dedupe_book_file_rows.go, 459 lines, v1.4.0, registered plugin.go:44) and already has the parallel RunItems worker pool and the apply=false dry run. The brief must be executed as a delta on that file; creating a second op would fork 459 lines of data-loss-reviewed keeper logic. It also says to gate on RootDir 'like siblings' — do NOT: this op reads only DB rows and a RootDir gate would make it inert. depends_on_lines lists 90032 only because both edit the SAME file (internal/plugins/maintenance/dedupe_book_file_rows.go) — they must not run in parallel worktrees; land 90032 first (it is the riskier, review_critical one) then rebase this on top. The package's flakiness is known: internal/plugins/maintenance is on the flaky list, so a single red run is not proof of a regression — re-run before concluding.
