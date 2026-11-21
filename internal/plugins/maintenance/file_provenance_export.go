// file: internal/plugins/maintenance/file_provenance_export.go
// version: 1.2.7
// guid: 08ef35af-5704-459a-aeec-e88245abde52
// last-edited: 2026-11-18

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/tasks"
)

// fileProvenanceExportKind is rule R3: the native kind of maintenance.file-provenance-export.
const fileProvenanceExportKind tasks.Kind = "books.maintenance.file-provenance-export"

// Getting the ledger OUT of the database it lives in.
//
// The provenance chain proves nothing was rewritten in place. It cannot help
// if the Pebble store is corrupted, or restored from a backup taken before an
// incident — the history goes with it, and that is the failure a ledger
// database would ALSO have had. A plain JSONL file on disk survives it, rsyncs
// anywhere, and is greppable at three in the morning when the service will not
// start.
//
// The file is strictly append-only: this op never rewrites a byte it has
// already written. That is what the store-wide sequence is for. Timestamps
// cannot drive the cursor, because AdoptOrphanEvents preserves an event's
// original At — an adopted event is written NOW with an OLD timestamp, and any
// time-watermark cursor would step over it and never come back.

// fileProvExportParams configures one export run.
type fileProvExportParams struct {
	// Path is the JSONL file to append to. Required. It is a file name directly in
	// {root_dir}/.ledger (a bare name, or an absolute path whose directory is that
	// one), the ledger directory that report writers and reports-prune never use:
	// every report op truncates the file it is given and the pruner deletes by name,
	// so a ledger kept among the reports is one wrong report name from being emptied.
	// A name with a directory in it, a path anywhere else (.reports included), and
	// any path when root_dir is not configured or not mounted are refused before the
	// ledger is read. The file must be one this service made: a regular file owned by
	// the account it runs as, with one link (a ledger copied in by another account is
	// refused until its owner is changed). There is deliberately no default: an
	// export that picks its own destination is an export nobody can find afterwards.
	Path string `json:"path"`
	// Apply writes. Default false: report what would be exported, touch nothing.
	Apply bool `json:"apply"`
	// Max caps events per run so a first export over a large ledger can be
	// done in bounded chunks. Whatever is left is reported, never silent.
	Max int `json:"max"`
	// StartNew accepts a ledger file that does not continue where the cursor says
	// it should: renamed aside and recreated empty, replaced, restored from an
	// older copy or truncated. Without it such a file is refused, because the
	// events between the file's last line and the cursor are in no file at the
	// ledger's path, and appending anyway would hide that. With it the run goes
	// on from the cursor, and the gap is recorded in the result (ledger_gap) and
	// as a marker line at the start of what this run appends. Ignored when the
	// file is continuous with the cursor.
	StartNew bool `json:"start_new"`
}

const fileProvExportDefaultMax = 5000

// exportLedger is the ledger file as the export uses it: written and made
// durable, and read back (its size and its last lines) to check that it continues
// where the export cursor says it should before anything is appended. *os.File
// satisfies it.
type exportLedger interface {
	io.WriteCloser
	io.ReaderAt
	Sync() error
	Stat() (fs.FileInfo, error)
}

// openExportFile opens the export file for appending. The default opens it the
// way every report is opened (library.OpenReportFile): relative to a handle on its
// directory, never following a symlink, and refusing a file that is not a regular
// file owned by this user with one link. The export appends rather than replaces,
// and it is the ledger's only copy outside the database, so a link planted at its
// name must not redirect it. The file is opened for reading as well
// (library.ReportAppendAndRead: still O_APPEND, so every write goes at the end),
// because the export reads the end of it back before writing.
//
// It is also a seam. The single most important property of this op is
// that the cursor advances only AFTER the bytes are durable, and the only way
// to test that is to open successfully and then fail the write or the sync —
// which no real filesystem will do on demand. Tests substitute this; nothing
// else does.
var openExportFile = func(path string) (exportLedger, error) {
	return library.OpenReportFile(path, 0o644, library.ReportAppendAndRead)
}

// fileProvExportLine is one JSONL record.
//
// A missing event is written as a MARKER rather than skipped. A sequence slot
// whose event has vanished is the evidence that something deleted a row, and
// an export that quietly omitted it would launder exactly the fact the ledger
// exists to preserve.
type fileProvExportLine struct {
	Seq        uint64              `json:"seq"`
	ExportedAt time.Time           `json:"exported_at"`
	Missing    bool                `json:"missing,omitempty"`
	Event      *database.FileEvent `json:"event,omitempty"`
	// Gap marks a line that is not an event: written at the start of a run that
	// was told (start_new) to carry on past a ledger file that did not continue
	// where the cursor said it should. Seq is 0 on it. A reader collapsing the file
	// on seq must skip lines whose Gap is set.
	Gap string `json:"gap,omitempty"`
}

// fileProvExportResult is what the op reports back.
type fileProvExportResult struct {
	Apply bool   `json:"apply"`
	Path  string `json:"path"`
	// CursorBefore / CursorAfter bracket what this run covered. Equal on a dry
	// run, and on a run that wrote nothing.
	CursorBefore uint64 `json:"cursor_before"`
	CursorAfter  uint64 `json:"cursor_after"`
	// MaxSeq is the highest sequence in the store. CursorAfter < MaxSeq means
	// there is more to export.
	MaxSeq uint64 `json:"max_seq"`
	// Scanned is how many sequence slots this run examined.
	Scanned int `json:"scanned"`
	// Written is how many lines were appended. Zero on a dry run.
	Written int `json:"written"`
	// Missing counts slots whose event row was gone — deletion evidence.
	Missing int `json:"missing"`
	// Gaps names sequence ranges with no index entry at all. A gap means the
	// index entry itself was deleted, which the per-chain hash link cannot
	// see because the evidence goes with the row.
	Gaps []string `json:"gaps,omitempty"`
	// BytesWritten is how much was appended.
	BytesWritten int64 `json:"bytes_written"`
	// ChainsVerified is how many distinct file chains this run re-verified.
	// The export is the sweep that already walks the ledger, so it is also
	// where verification runs — a verifier with no caller is decoration.
	ChainsVerified int `json:"chains_verified"`
	// ChainsBroken counts chains whose hash links did not hold. Chains that
	// predate chaining are NOT counted here; they are legitimate.
	ChainsBroken int `json:"chains_broken"`
	// BrokenChains names the first few offenders, for going and looking.
	BrokenChains []string `json:"broken_chains,omitempty"`
	// LedgerGap says how the ledger file failed to continue from the cursor and
	// was accepted anyway (start_new). Empty when the file was continuous.
	LedgerGap string `json:"ledger_gap,omitempty"`
}

// brokenChainSampleCap bounds the named offenders. The count is exact; the
// list is a starting point, not an inventory.
const brokenChainSampleCap = 12

func (p *Plugin) fileProvenanceExportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.file-provenance-export",
		Preview:     sdk.PreviewSpec{Params: fileProvExportParams{}},
		DisplayName: "Export the file provenance ledger to append-only JSONL",
		Description: "Appends every provenance event newer than the durable export " +
			"cursor to a JSONL file, so the ledger survives corruption of the database " +
			"it lives in. Never rewrites what it has written. Reports sequence gaps and " +
			"missing rows as deletion evidence. Path is required: a file name in the library's " +
			"ledger directory (root_dir/.ledger), such as ledger.jsonl; a path anywhere " +
			"else, or one with a directory in it, is refused, and the library volume must be " +
			"mounted. The directory is created if it is missing, on a dry run too. Default " +
			"dry-run (it reads the ledger and checks the path, and appends nothing), pass " +
			"{\"apply\": true} to write.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.file-provenance-export",
		// ResumeDrop: the cursor is durable, so a dropped run loses no ground —
		// the next run picks up exactly where the last committed one stopped.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		// With apply=true this appends JSONL to the caller's path (CapFilesWrite)
		// and advances the durable export cursor in the database
		// (CapLibraryWrite). It appends no provenance event and changes no book
		// or file row. Without apply it only reads the ledger.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runFileProvenanceExport(ctx, raw, reporter)
		},
	}
}

// fileProvExportEnv is what the core needs from its engine (rules R2 and R10).
type fileProvExportEnv struct {
	t6g4Base
}

// fileProvExportNativeParams is fileProvExportParams for the native run: the
// mode keys are only refusal markers (the mode comes from the start request).
type fileProvExportNativeParams struct {
	Path     string `json:"path"`
	Max      int    `json:"max"`
	StartNew bool   `json:"start_new"`
	t6g4ModeKeys
}

// runFileProvenanceExport is the v2 shell around runFileProvenanceExportCore (rule R2 item 2).
func (p *Plugin) runFileProvenanceExport(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeFileProvExportParams(rawParams)
	if err != nil {
		return err
	}
	_, err = p.runFileProvenanceExportCore(ctx, params, p.fileProvenanceExportHeadEnv(reporter, params.Apply))
	return err
}

// fileProvenanceExportHeadEnv builds the env HEAD's shell gives the core: the
// provenance store resolved off the deps (nil when no layer implements it).
func (p *Plugin) fileProvenanceExportHeadEnv(reporter sdk.Reporter, apply bool) fileProvExportEnv {
	prov := p.deps.FileProvenanceStore()
	return fileProvExportEnv{t6g4Base: t6g4HeadBase(reporter, apply,
		headT6G4Store{deps: p.deps, prov: prov}, headT6G4Writer{prov: prov})}
}

// fileProvenanceExportKind is the native shell (rule R2 item 3). HEAD has a
// preview (it reads the ledger and verifies chains, writing nothing).
func (p *Plugin) fileProvenanceExportKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(fileProvenanceExportKind, tasks.TaskSpec[fileProvExportNativeParams, fileProvExportResult]{
		Title: "Export the file provenance ledger to append-only JSONL",
		Help: "Appends every provenance event newer than the durable export " +
			"cursor to a JSONL file, so the ledger survives corruption of the database " +
			"it lives in. Never rewrites what it has written. Reports sequence gaps and " +
			"missing rows as deletion evidence. Path is required: a file name in the library's " +
			"ledger directory (root_dir/.ledger), such as ledger.jsonl; a path anywhere " +
			"else, or one with a directory in it, is refused, and the library volume must be " +
			"mounted. The directory is created if it is missing, on a preview too. A live run " +
			"refuses a ledger file that does not continue where the cursor says it should " +
			"(renamed aside and recreated, replaced, truncated); start_new=true carries on " +
			"anyway and records the gap in the result and as a marker line. A preview " +
			"(the default) appends nothing, start in live mode to write.",
		// Appends the ledger to a JSONL file on disk (files) and moves the export
		// cursor in the provenance keyspace, declared as t6g4ResProvenance.
		Effects:        tasks.Writes(t6g4Res(t6g4ResProvenance, ops.ResFiles)...),
		Perms:          tasks.Permissions{Start: t6g4StartRequirement()},
		Resume:         tasks.ResumeDrop,
		Priority:       tasks.PriorityLow,
		ConcurrencyKey: tasks.Key("books.maintenance.file-provenance-export"),
		// HEAD declares no Timeout (the registry's own default applies); the
		// native kind states 120 minutes (F5-09 review, MINOR 13). Listed in
		// the PR's Deviations.
		Timeout:  120 * time.Minute,
		Schedule: sched,
		Ready:    []tasks.ReadySignal{t6g4SignalMemDB},
		Run: func(rc *tasks.RC, in fileProvExportNativeParams) (fileProvExportResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return fileProvExportResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return fileProvExportResult{}, err
			}
			return p.runFileProvenanceExportCore(rc.Context(), fileProvExportParams{
				Path: in.Path, Apply: base.Live, Max: in.Max, StartNew: in.StartNew,
			}, fileProvExportEnv{t6g4Base: base})
		},
	})
}

// decodeFileProvExportParams is the params decode both HEAD entry points share.
func decodeFileProvExportParams(rawParams json.RawMessage) (fileProvExportParams, error) {
	var params fileProvExportParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fileProvExportParams{}, fmt.Errorf("file-provenance-export: decode params: %w", err)
		}
	}
	return params, nil
}

// runFileProvenanceExportCore is HEAD's run body (rule R2 item 1): the export and
// the report lines.
func (p *Plugin) runFileProvenanceExportCore(ctx context.Context, params fileProvExportParams, env fileProvExportEnv) (fileProvExportResult, error) {
	log := env.Logger()
	result, err := p.exportFileProvenanceCore(ctx, params, env)
	if err != nil {
		return result, err
	}

	if b, mErr := json.Marshal(result); mErr == nil {
		log.Info("file-provenance-export report (JSON)", "report", string(b))
	}
	if len(result.Gaps) > 0 {
		// Loud. A gap is not a hiccup — it means sequence entries were
		// deleted, and nothing else in the system would have told anyone.
		log.Warn("file-provenance-export: SEQUENCE GAPS — ledger rows were deleted",
			"gaps", result.Gaps)
	}
	if result.ChainsBroken > 0 {
		// The chain link only ever breaks when something rewrote or removed a
		// row. There is no benign cause, so this is the loudest line the op has.
		log.Error("file-provenance-export: BROKEN CHAINS — provenance rows were rewritten or removed",
			"broken", result.ChainsBroken, "verified", result.ChainsVerified,
			"examples", result.BrokenChains)
	}
	if result.Missing > 0 {
		log.Warn("file-provenance-export: sequence slots whose event row is gone",
			"missing", result.Missing)
	}
	if result.LedgerGap != "" {
		log.Warn("file-provenance-export: the ledger file did not continue from the cursor; carrying on (start_new)",
			"gap", result.LedgerGap, "path", result.Path)
	}
	if result.CursorAfter < result.MaxSeq {
		log.Warn("file-provenance-export: more events remain — run again to continue",
			"cursor", result.CursorAfter, "max_seq", result.MaxSeq)
	}
	log.Info("file-provenance-export complete",
		"apply", result.Apply, "path", result.Path, "scanned", result.Scanned,
		"written", result.Written, "cursor_before", result.CursorBefore,
		"cursor_after", result.CursorAfter, "max_seq", result.MaxSeq,
		"bytes", result.BytesWritten, "chains_verified", result.ChainsVerified,
		"chains_broken", result.ChainsBroken)
	return result, nil
}

// exportFileProvenanceCore is the export itself over decoded params. The ledger is
// read through env.Store and the cursor advanced only through env.Writer.
func (p *Plugin) exportFileProvenanceCore(ctx context.Context, params fileProvExportParams, env fileProvExportEnv) (fileProvExportResult, error) {
	if params.Path == "" {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: path is required")
	}
	// The ledger lands on the library volume, so the volume must be there: with an
	// unmounted share the mountpoint is a writable directory on the underlying
	// filesystem, the ledger would be appended there and the cursor advanced, and
	// the lines would be hidden once the share is mounted over them, a gap nobody
	// would see. Checked before the path is resolved (resolving creates the ledger
	// directory) and on a preview too. library.Mounted is a coarse check, not proof
	// of a healthy mount.
	root := env.Store.RootDir()
	if merr := library.Mounted(root); merr != nil {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: refusing to run: %w", merr)
	}
	// Confined to {root_dir}/.ledger: a name that would land anywhere else is
	// refused here, before the ledger is read. LedgerPathNotify is declared as a
	// path-injection barrier in .github/codeql/models/path-sanitizers.model.yml,
	// which is this repo's convention for a validated path rather than an inline
	// suppression. Resolving prepares the directory (creates it, removes world
	// write, writes and removes a probe file), so it does that on a preview too.
	outPath, err := t6g4LedgerPath(env.Store, params.Path, "file-provenance-export.jsonl")
	if err != nil {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: path: %w", err)
	}
	if params.Max <= 0 {
		params.Max = fileProvExportDefaultMax
	}

	store := env.Store
	if !store.FileProvenanceAvailable() {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: provenance store not initialized")
	}

	cursor, err := store.GetFileProvExportCursor()
	if err != nil {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: read cursor: %w", err)
	}
	maxSeq, err := store.MaxFileEventSeq()
	if err != nil {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: read max sequence: %w", err)
	}

	result := fileProvExportResult{
		Apply: params.Apply, Path: outPath,
		CursorBefore: cursor, CursorAfter: cursor, MaxSeq: maxSeq,
	}

	rows, err := store.ScanFileEventsBySeq(cursor, params.Max)
	if err != nil {
		return result, fmt.Errorf("file-provenance-export: scan: %w", err)
	}
	result.Scanned = len(rows)
	if len(rows) == 0 {
		return result, nil
	}

	// Gap detection. Every sequence number between the cursor and the last row
	// should be present; a missing one means the index entry was deleted.
	expected := cursor + 1
	for _, r := range rows {
		if r.Seq > expected {
			result.Gaps = append(result.Gaps, fmt.Sprintf("%d-%d", expected, r.Seq-1))
		}
		expected = r.Seq + 1
	}

	lines := make([]fileProvExportLine, 0, len(rows))
	now := time.Now()
	for _, r := range rows {
		line := fileProvExportLine{Seq: r.Seq, ExportedAt: now}
		if r.Event.Kind == "" {
			result.Missing++
			line.Missing = true
		} else {
			ev := r.Event
			line.Event = &ev
		}
		lines = append(lines, line)
	}

	// Verify the chains this batch touched. Read-only, so it runs on a dry run
	// too — which makes the default invocation a ledger health check that
	// writes nothing. Deduped by book_file, because a batch of events usually
	// spans far fewer files than it has events.
	seenChains := make(map[string]struct{})
	for _, r := range rows {
		if r.Event.BookFileID == "" {
			continue
		}
		if _, dup := seenChains[r.Event.BookFileID]; dup {
			continue
		}
		seenChains[r.Event.BookFileID] = struct{}{}
		hist, hErr := store.GetFileHistory(r.Event.BookFileID)
		if hErr != nil {
			continue
		}
		result.ChainsVerified++
		if rep := database.VerifyFileChain(hist); rep.Verdict == database.ChainBroken {
			result.ChainsBroken++
			if len(result.BrokenChains) < brokenChainSampleCap {
				result.BrokenChains = append(result.BrokenChains, r.Event.BookFileID)
			}
		}
	}

	if !params.Apply {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	f, err := openExportFile(outPath)
	if err != nil {
		return result, fmt.Errorf("file-provenance-export: open %s: %w", outPath, err)
	}
	// The ledger directory is closed to other users, but whoever can write the
	// library root can rename it, and the next run would make a fresh one and
	// append from the cursor: the events before it would be in no file at this
	// path, and nothing would say so. The file must continue where the cursor says.
	gap, gapErr := checkLedgerContinuity(f, cursor, params.StartNew)
	if gapErr != nil {
		return result, fmt.Errorf("file-provenance-export: %s: %w", outPath, errors.Join(gapErr, f.Close()))
	}
	if gap != "" {
		result.LedgerGap = gap
		lines = append([]fileProvExportLine{{ExportedAt: now, Gap: gap}}, lines...)
	}

	var written int
	var bytesWritten int64
	writeErr := func() error {
		for _, line := range lines {
			b, mErr := json.Marshal(line)
			if mErr != nil {
				return fmt.Errorf("marshal seq %d: %w", line.Seq, mErr)
			}
			b = append(b, '\n')
			n, wErr := f.Write(b)
			bytesWritten += int64(n)
			if wErr != nil {
				return fmt.Errorf("write seq %d: %w", line.Seq, wErr)
			}
			if line.Gap == "" {
				written++
			}
		}
		// fsync before the cursor moves. The ordering is the whole safety
		// argument: if this fails, the cursor stays put and the next run
		// re-appends: duplicate lines, which a reader can collapse on seq. If
		// the cursor moved first and the write failed, those events would be
		// missing from the export forever, and nothing would ever say so.
		return f.Sync()
	}()
	closeErr := f.Close()

	result.Written = written
	result.BytesWritten = bytesWritten
	if writeErr != nil {
		return result, fmt.Errorf("file-provenance-export: %w", writeErr)
	}
	if closeErr != nil {
		return result, fmt.Errorf("file-provenance-export: close %s: %w", outPath, closeErr)
	}

	last := rows[len(rows)-1].Seq
	if err := env.Writer.SetFileProvExportCursor(last); err != nil {
		return result, fmt.Errorf("file-provenance-export: advance cursor: %w", err)
	}
	result.CursorAfter = last
	return result, nil
}

// checkLedgerContinuity says whether the ledger file f continues where the export
// cursor says it should: when events up to cursor were already exported, the last
// exported line in the file must be at or after the cursor (after it is possible
// and harmless: a run that wrote and synced and died before it advanced the
// cursor, which the next run re-appends). A file that is empty (made by this very
// open, or truncated), that holds no exported line, or that ends before the cursor
// is not that ledger: it was renamed aside and recreated, replaced, restored from an
// older copy or cut short, and the events between its end and the cursor are in no
// file at this path.
//
// It returns "" when the file is continuous. When it is not, it returns an error
// that says so and names start_new, unless startNew is set, in which case it
// returns the description for the result and the marker line. The ledger is read
// from the file rather than from a copy kept beside the cursor: what is checked is
// the bytes about to be appended to, and a restored or copied ledger that really
// holds the lines is not a gap. cursor 0 (nothing exported yet) always passes.
func checkLedgerContinuity(f exportLedger, cursor uint64, startNew bool) (string, error) {
	if cursor == 0 {
		return "", nil
	}
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect the ledger file: %w", err)
	}
	var problem string
	if info.Size() == 0 {
		problem = fmt.Sprintf("the ledger file is empty, but the export cursor says events up to seq %d were already exported", cursor)
	} else {
		last, found, lerr := lastLedgerSeq(f, info.Size())
		switch {
		case lerr != nil:
			return "", fmt.Errorf("read the end of the ledger file: %w", lerr)
		case !found:
			problem = fmt.Sprintf("the ledger file holds no exported event, but the export cursor says events up to seq %d were already exported", cursor)
		case last < cursor:
			problem = fmt.Sprintf("the ledger file ends at seq %d, but the export cursor says events up to seq %d were already exported", last, cursor)
		}
	}
	if problem == "" {
		return "", nil
	}
	if !startNew {
		return "", fmt.Errorf("%s, so the earlier lines are not in this file (it was renamed, replaced, restored "+
			"from an older copy or truncated); nothing was appended. Pass start_new=true to carry on in this file anyway; "+
			"the gap is then recorded in the result and as a marker line", problem)
	}
	return problem, nil
}

// ledgerTailChunk is how much of the end of the ledger is read at a time.
const ledgerTailChunk = 64 << 10

// lastLedgerSeq finds the sequence of the last event line in the file of the given
// size, reading backwards from the end in chunks. A line without a trailing newline
// at the very end is a write that was cut off, and is skipped, as are lines that
// do not parse, marker lines (Gap set) and anything with seq 0.
func lastLedgerSeq(r io.ReaderAt, size int64) (seq uint64, found bool, err error) {
	var carry []byte // the start of a line whose beginning is in an earlier chunk
	for end, first := size, true; end > 0; first = false {
		start := max(0, end-ledgerTailChunk)
		buf := make([]byte, int(end-start)+len(carry))
		n, rerr := r.ReadAt(buf[:end-start], start)
		if rerr != nil && !(errors.Is(rerr, io.EOF) && int64(n) == end-start) {
			return 0, false, rerr
		}
		copy(buf[end-start:], carry)
		lines := bytes.Split(buf, []byte{'\n'})
		carry = nil
		if start > 0 {
			// The first element may be the tail of a line that began before start.
			carry = append([]byte(nil), lines[0]...)
			lines = lines[1:]
		}
		if first && len(lines) > 0 {
			// The element after the last newline is empty when the file ends in
			// one, and a cut-off write when it does not; neither is a line.
			lines = lines[:len(lines)-1]
		}
		for _, line := range slices.Backward(lines) {
			var l struct {
				Seq uint64 `json:"seq"`
				Gap string `json:"gap"`
			}
			if json.Unmarshal(line, &l) != nil || l.Seq == 0 || l.Gap != "" {
				continue
			}
			return l.Seq, true, nil
		}
		end = start
	}
	return 0, false, nil
}
