// file: internal/plugins/acoustid/books_tasklists.go
// version: 0.1.0
// guid: 6d14b8e3-27a9-4c50-9f81-e3a0c5b72d16
// last-edited: 2026-11-22

package acoustid

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own KindDeps for the AcoustID kinds (F5-18 A6), dark:
// nothing outside tests calls BooksTaskLists until the flip's registration
// commit. The writer is the gated one: each method is ONE writer.Gate call
// stamped with the method's whole row.
//
// The receiver of Kinds is the REGISTERED plugin, not a zero one. window-backfill
// leaves its lease hub, scan probe and tool resolver to the plugin
// (WindowKindDeps: a nil field is filled from it), and the server's worker-lease
// routes serve the registered plugin's hub (server.go: fpWorkerHub =
// acoustidPlug.WorkerHub()). A zero plugin would give the kind a hub nothing
// serves, so a live run would wait for leases no worker can take.

// KindSource is the store the builder reads and writes through.
type KindSource interface {
	kindStore
	kindSourceWrites
}

// kindSourceWrites is the five store writes the cores reach.
type kindSourceWrites interface {
	UpdateBookFile(id string, file *database.BookFile) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	ClearBookSignature(id string) error
	ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error
	RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error
}

// CandidateStore is the dedup candidate store: the reads the scan summary and
// the reset pass take and the delete the reset pass makes.
type CandidateStore interface {
	candidateReads
	DeleteCandidates(ids []int64) (int, error)
}

// ScanEngine is the dedup engine's AcoustID scan, as acoustid.scan runs it.
type ScanEngine = scanEngine

// BooksTaskInputs is everything BooksTaskLists builds the AcoustID lists from.
type BooksTaskInputs struct {
	// Plugin is the registered AcoustID plugin: the receiver of Kinds, so the
	// window kind shares its worker hub, scan probe and tool resolver.
	Plugin *Plugin
	// Source is the library store.
	Source KindSource
	// Candidates is the dedup candidate store; nil when embedding is disabled, in
	// which case the scan summary and the reset pass skip their candidate step as
	// HEAD did.
	Candidates CandidateStore
	// NewEngine builds the dedup engine over the run's gated writer. nil when
	// embedding is disabled: acoustid.scan then refuses, as HEAD's did with no
	// engine. The flip owns the gated engine (the engine writes candidates
	// through its own stores, seams.d/F5-05.md).
	NewEngine func(tasks.Writer) ScanEngine
	// Lookup, Fingerprint and Window are the hooks of the lookup-online,
	// fpcalc-running and window kinds; a nil field is HEAD's own function.
	Lookup      LookupDeps
	Fingerprint FingerprintDeps
	Window      WindowKindDeps
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every AcoustID kind with its schedule, over the books
// module's own dependency values.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for name, set := range map[string]bool{
		"Plugin": in.Plugin != nil, "Source": in.Source != nil, "Schedules": in.Schedules != nil,
	} {
		if !set {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing) // map order is random; name them the same way every time
	if len(missing) > 0 {
		return nil, fmt.Errorf("acoustid: BooksTaskLists: missing input(s): %v", missing)
	}
	deps, err := newBooksKindDeps(in)
	if err != nil {
		return nil, err
	}
	return booksReceiver(in).Kinds(deps, in.Schedules), nil
}

// booksReceiver is the plugin the kinds are declared on.
func booksReceiver(in BooksTaskInputs) *Plugin { return in.Plugin }

// booksKindRows is the rows of the writer's methods. The shared table has the
// two book writes; the rest follow what each kind's own Effects already decided
// (kinds_*.go): the fpwin: sidecar and the batched wipe's index entries are
// fingerprint_index, a book-signature clear is the book-write row, the dedup candidates are
// embeddings, and the batched wipe is the file row plus fingerprint_index.
func booksKindRows() (writer.Rows, error) {
	shared := writer.SharedRows()
	rows := writer.Rows{}
	for _, m := range []string{"UpdateBookFile", "ModifyBook"} {
		row, ok := shared[m]
		if !ok {
			return nil, fmt.Errorf("acoustid: no resource row for %s", m)
		}
		rows[m] = row
	}
	// ClearBookSignature is a ModifyBook (backfill.go:789), so it takes the whole
	// book-write row (books, metadata_cache, indexes), not the books resource alone.
	rows["ClearBookSignature"] = rows["ModifyBook"]
	rows["ReplaceFingerprintWindows"] = []ops.Resource{ops.ResFingerprintIndex}
	rows["RecordFingerprintWindowFailure"] = []ops.Resource{ops.ResFingerprintIndex}
	rows["DeleteCandidates"] = []ops.Resource{ops.ResEmbeddings}
	rows["ClearAllAcoustIDFingerprints"] = resources.Union(rows["UpdateBookFile"], []ops.Resource{ops.ResFingerprintIndex})
	return rows, nil
}

func newBooksKindDeps(in BooksTaskInputs) (KindDeps, error) {
	rows, err := booksKindRows()
	if err != nil {
		return KindDeps{}, err
	}
	src, cands := in.Source, in.Candidates
	d := KindDeps{
		Store: src,
		NewWriter: func(w tasks.Writer) kindWriter {
			return &booksKindWriter{src: src, cands: cands, gate: writer.ForRun(w, rows)}
		},
		NewEngine:   in.NewEngine,
		Lookup:      in.Lookup,
		Fingerprint: in.Fingerprint,
		Window:      in.Window,
	}
	// A nil CandidateStore stays an untyped-nil interface: the cores read "no
	// embedding store" as Candidates == nil.
	if cands != nil {
		d.Candidates = cands
	}
	return d, nil
}

// booksKindWriter is one run's gated writes. A ReadOnly run has no writer (w is
// nil): every method then refuses instead of dereferencing nil.
type booksKindWriter struct {
	src   kindSourceWrites
	cands CandidateStore
	gate  *writer.Gate
}

var errNoCandidateStore = errors.New("acoustid: no candidate store (embedding is disabled)")

func (w *booksKindWriter) UpdateBookFile(id string, file *database.BookFile) error {
	return w.gate.Err(context.Background(), "UpdateBookFile", writer.Ref(string(ops.ResBookFiles), id), func() error {
		return w.src.UpdateBookFile(id, file)
	})
}

func (w *booksKindWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return writer.Value(context.Background(), w.gate, "ModifyBook", writer.Ref(string(ops.ResBooks), id), func() (*database.Book, error) {
		return w.src.ModifyBook(id, fn)
	})
}

func (w *booksKindWriter) ClearBookSignature(id string) error {
	return w.gate.Err(context.Background(), "ClearBookSignature", writer.Ref(string(ops.ResBooks), id), func() error {
		return w.src.ClearBookSignature(id)
	})
}

func (w *booksKindWriter) ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error {
	return w.gate.Err(context.Background(), "ReplaceFingerprintWindows", writer.Ref(string(ops.ResFingerprintIndex), string(ref)), func() error {
		return w.src.ReplaceFingerprintWindows(ref, ws)
	})
}

func (w *booksKindWriter) RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error {
	id := ""
	if f != nil {
		id = string(f.Ref)
	}
	return w.gate.Err(context.Background(), "RecordFingerprintWindowFailure", writer.Ref(string(ops.ResFingerprintIndex), id), func() error {
		return w.src.RecordFingerprintWindowFailure(f)
	})
}

func (w *booksKindWriter) DeleteCandidates(ids []int64) (int, error) {
	if w.cands == nil {
		return 0, errNoCandidateStore
	}
	return writer.Value(context.Background(), w.gate, "DeleteCandidates", writer.Ref(string(ops.ResEmbeddings), ""), func() (int, error) {
		return w.cands.DeleteCandidates(ids)
	})
}

// FingerprintResetter is the batched wipe, found on the store through its
// decorator chain (a write found by capability, so it comes from the writer and
// never the read store); nil on a backend without it. The wipe is ONE gated
// call around the whole batched pass.
func (w *booksKindWriter) FingerprintResetter() fingerprintResetter {
	r := resolveFingerprintResetter(w.src)
	if r == nil {
		return nil
	}
	return gatedResetter{r: r, gate: w.gate}
}

type gatedResetter struct {
	r    fingerprintResetter
	gate *writer.Gate
}

func (g gatedResetter) ClearAllAcoustIDFingerprints(ctx context.Context, batchSize int, progress func(processed, cleared, total int)) (int, int, error) {
	var cleared, total int
	err := g.gate.Err(ctx, "ClearAllAcoustIDFingerprints", writer.Ref(string(ops.ResFingerprintIndex), ""), func() error {
		var e error
		cleared, total, e = g.r.ClearAllAcoustIDFingerprints(ctx, batchSize, progress)
		return e
	})
	return cleared, total, err
}
