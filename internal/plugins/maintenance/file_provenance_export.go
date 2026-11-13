// file: internal/plugins/maintenance/file_provenance_export.go
// version: 1.2.0
// guid: 08ef35af-5704-459a-aeec-e88245abde52
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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
	// Path is the JSONL file to append to. Required, absolute, no traversal.
	// There is deliberately no default: an export that picks its own
	// destination is an export nobody can find afterwards.
	Path string `json:"path"`
	// Apply writes. Default false: report what would be exported, touch nothing.
	Apply bool `json:"apply"`
	// Max caps events per run so a first export over a large ledger can be
	// done in bounded chunks. Whatever is left is reported, never silent.
	Max int `json:"max"`
}

const fileProvExportDefaultMax = 5000

// syncWriteCloser is an append target that can be made durable. *os.File
// satisfies it.
type syncWriteCloser interface {
	io.WriteCloser
	Sync() error
}

// openExportFile is a seam. The single most important property of this op is
// that the cursor advances only AFTER the bytes are durable, and the only way
// to test that is to open successfully and then fail the write or the sync —
// which no real filesystem will do on demand. Tests substitute this; nothing
// else does.
var openExportFile = func(path string) (syncWriteCloser, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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
			"missing rows as deletion evidence. Path is required; default dry-run, pass " +
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

// fileProvExportNativeParams is fileProvExportParams for the native run: Apply
// is only a refusal marker (the mode comes from the start request).
type fileProvExportNativeParams struct {
	Path  string `json:"path"`
	Max   int    `json:"max"`
	Apply *bool  `json:"apply,omitempty"`
}

// runFileProvenanceExport is the v2 shell around runFileProvenanceExportCore (rule R2 item 2).
func (p *Plugin) runFileProvenanceExport(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeFileProvExportParams(rawParams)
	if err != nil {
		return err
	}
	prov := p.deps.FileProvenanceStore()
	_, err = p.runFileProvenanceExportCore(ctx, params, fileProvExportEnv{t6g4Base: t6g4HeadBase(reporter, params.Apply,
		headT6G4Store{deps: p.deps, prov: prov}, headT6G4Writer{prov: prov})})
	return err
}

// fileProvenanceExportKind is the native shell (rule R2 item 3). HEAD has a
// preview (it reads the ledger and verifies chains, writing nothing).
func (p *Plugin) fileProvenanceExportKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(fileProvenanceExportKind, tasks.TaskSpec[fileProvExportNativeParams, fileProvExportResult]{
		Common: tasks.Common{
			Title: "Export the file provenance ledger to append-only JSONL",
			Help: "Appends every provenance event newer than the durable export " +
				"cursor to a JSONL file, so the ledger survives corruption of the database " +
				"it lives in. Never rewrites what it has written. Reports sequence gaps and " +
				"missing rows as deletion evidence. Path is required; default dry-run, pass " +
				"{\"apply\": true} to write.",
			Effects:        tasks.Writes(t6g4Res("file_provenance")...),
			Perms:          tasks.Permissions{Start: t6g4StartRequirement()},
			Resume:         tasks.ResumeDrop,
			Priority:       tasks.PriorityLow,
			ConcurrencyKey: tasks.Key("books.maintenance.file-provenance-export"),
			Schedule:       sched,
			Ready:          []tasks.ReadySignal{t6g4SignalMemDB},
		},
		Run: func(rc *tasks.RC, in fileProvExportNativeParams) (fileProvExportResult, error) {
			if in.Apply != nil {
				return fileProvExportResult{}, fmt.Errorf("%s: the apply param is not accepted; the run's mode comes from the start request (live applies, preview reports)", rc.Kind())
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return fileProvExportResult{}, err
			}
			return p.runFileProvenanceExportCore(rc.Context(), fileProvExportParams{
				Path: in.Path, Apply: base.Live, Max: in.Max,
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

// exportFileProvenance does the work and returns the tally.
//
// Split from the reporter plumbing so the behaviour can be asserted directly
// rather than by parsing a log line back out of a fake reporter.
func (p *Plugin) exportFileProvenance(ctx context.Context, rawParams json.RawMessage) (fileProvExportResult, error) {
	params, err := decodeFileProvExportParams(rawParams)
	if err != nil {
		return fileProvExportResult{}, err
	}
	prov := p.deps.FileProvenanceStore()
	// No reporter: the export itself logs nothing; pinned callers assert the tally.
	return p.exportFileProvenanceCore(ctx, params, fileProvExportEnv{t6g4Base: t6g4Base{
		Live: params.Apply, Store: headT6G4Store{deps: p.deps, prov: prov}, Writer: headT6G4Writer{prov: prov},
	}})
}

// exportFileProvenanceCore is the export itself over decoded params. The ledger is
// read through env.Store and the cursor advanced only through env.Writer.
func (p *Plugin) exportFileProvenanceCore(ctx context.Context, params fileProvExportParams, env fileProvExportEnv) (fileProvExportResult, error) {
	if params.Path == "" {
		return fileProvExportResult{}, fmt.Errorf("file-provenance-export: path is required")
	}
	// Absolute, cleaned, no traversal. Declared as a path-injection barrier in
	// .github/codeql/models/path-sanitizers.model.yml, which is this repo's
	// convention for a validated path rather than an inline suppression.
	outPath, err := library.CleanAbsolutePath(params.Path)
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
			written++
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
