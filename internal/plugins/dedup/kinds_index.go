// file: internal/plugins/dedup/kinds_index.go
// version: 1.1.3
// guid: 0c0d3b8a-6e5d-4e73-9a38-2b4f3f4d9a11
// last-edited: 2026-11-19

package dedup

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The index group (F5-04): the definitions that backfill or prune secondary
// indexes and flags: the ISBN/ASIN index, the candidate status index, the
// book_file segment sweep, the legacy fingerprint candidate purge and the
// window-print band index. (dedup.lsh-index-build is the coordinator's.)

// indexKindStore is the reads the index group's cores take, composed by
// embedding each definition's own narrow interface (the F5-11 precedent).
type indexKindStore interface {
	buildISBNIndexStore
	buildCandidateStatusIndexStore
	bookfileSegDropStore
	purgeLegacyFPStore
	windowIndexBuildStore
}

// indexKindWriter is the writes the index group's cores make, composed the same way.
type indexKindWriter interface {
	buildISBNIndexWriter
	buildCandidateStatusIndexWriter
	bookfileSegDropWriter
	purgeLegacyFPWriter
	windowIndexBuildWriter
}

// IndexKindDeps is what the index group's native kinds need from their owner.
// F5-18 wires them: the stores behind Store, the gated writer behind NewWriter;
// tests pass fakes.
type IndexKindDeps struct {
	Store     indexKindStore
	NewWriter func(tasks.Writer) indexKindWriter
}

// indexWriterFor builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func indexWriterFor(kind tasks.Kind, d IndexKindDeps, rc *tasks.RC) (indexKindWriter, error) {
	if err := errNeed(kind, "Index.NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// indexKinds lists the index group's native kinds in the coordinator's order.
func indexKinds(p *Plugin, d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.buildISBNIndexNative(d, sched),
		p.buildCandidateStatusIndexNative(d, sched),
		p.bookfileSegDropNative(d, sched),
		p.purgeLegacyFPNative(d, sched),
		p.windowIndexBuildNative(d, sched),
	}
}

// buildISBNIndexNative is the native dedup.build-isbn-index kind. Preview is
// HEAD's dry run (apply=false); Live writes the index rows and the flag.
func (p *Plugin) buildISBNIndexNative(d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(buildISBNIndexKind, sched, false)
	c.Title = "Build ISBN/ASIN secondary index"
	c.Help = buildISBNIndexHelp
	// Writes found: ISBNIndexStore.WriteISBNIndexForBook writes the
	// book:isbn10:/isbn13:/asin: rows, a row-family secondary index that belongs
	// to its row family (books, per ops.ResIndexes' doc), and SetISBNIndexBuilt
	// is SetSetting (settings). The scan reads books (GetAllBooksCore).
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResSettings)...)
	// HEAD Drop: the body keeps no checkpoint; a re-run rescans (idempotent).
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key("books.dedup.build-isbn-index")
	c.Timeout = 60 * time.Minute
	return tasks.Task(buildISBNIndexKind, tasks.TaskSpec[json.RawMessage, buildISBNIndexResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (buildISBNIndexResult, error) {
			if err := refuseKindModeKeys(buildISBNIndexKind, raw); err != nil {
				return buildISBNIndexResult{}, err
			}
			if err := errNeed(buildISBNIndexKind, "Index.Store", d.Store != nil); err != nil {
				return buildISBNIndexResult{}, err
			}
			env := buildISBNIndexEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := indexWriterFor(buildISBNIndexKind, d, rc)
				if err != nil {
					return buildISBNIndexResult{}, err
				}
				env.Writer = w
			}
			return buildISBNIndexCore(rc.Context(), env)
		},
	})
}

// buildCandidateStatusIndexNative is the native dedup.build-candidate-status-index
// kind. HEAD has no preview (no params, one write pass), so a native Preview is
// refused.
func (p *Plugin) buildCandidateStatusIndexNative(d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(buildCandidateStatusIndexKind, sched, false)
	c.Title = "Build candidate status secondary index"
	c.Help = buildCandidateStatusIndexHelp
	// Writes found: EmbeddingStore.WriteCandidateStatusIndexRow sets dedup:s:
	// rows and SetCandidateStatusIndexBuilt sets setting:<flag>, both in the
	// embedding store's keyspace (embeddings). The scan reads candidates there too.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...)
	// HEAD Requeue: FromZero. The body reads no checkpoint; the rows are idempotent.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.build-candidate-status-index")
	c.Timeout = 60 * time.Minute
	return tasks.Task(buildCandidateStatusIndexKind, tasks.TaskSpec[json.RawMessage, buildCandidateStatusIndexResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (buildCandidateStatusIndexResult, error) {
			if err := refuseKindModeKeys(buildCandidateStatusIndexKind, raw); err != nil {
				return buildCandidateStatusIndexResult{}, err
			}
			if err := kindLiveOnly(rc, buildCandidateStatusIndexKind); err != nil {
				return buildCandidateStatusIndexResult{}, err
			}
			if err := errNeed(buildCandidateStatusIndexKind, "Index.Store", d.Store != nil); err != nil {
				return buildCandidateStatusIndexResult{}, err
			}
			w, err := indexWriterFor(buildCandidateStatusIndexKind, d, rc)
			if err != nil {
				return buildCandidateStatusIndexResult{}, err
			}
			return buildCandidateStatusIndexCore(rc.Context(), buildCandidateStatusIndexEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Writer: w,
			})
		},
	})
}

// bookfileSegDropNative is the native dedup.bookfile-seg-drop kind. Preview is
// HEAD's dry run: the sweep is scan and write in one store call, so a Preview
// run reaches it through the read store's dry method and builds no writer.
func (p *Plugin) bookfileSegDropNative(d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(bookfileSegDropKind, sched, false)
	c.Title = "Drop AcoustID segment fields from BookFile values"
	c.Help = bookfileSegDropHelp
	// Writes found: PebbleStore.SweepBookFileSegDrop rewrites book_file: rows and
	// deletes their book_file_acoustid: index entries (book_files); SetSetting
	// writes the done flag (settings).
	c.Effects = tasks.Writes(res(ops.ResBookFiles, ops.ResSettings)...)
	// HEAD Drop: the sweep keeps no checkpoint; rewritten rows are fast-skipped on a re-run.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.bookfile-seg-drop")
	c.Timeout = 60 * time.Minute
	return tasks.Task(bookfileSegDropKind, tasks.TaskSpec[json.RawMessage, bookfileSegDropResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (bookfileSegDropResult, error) {
			if err := refuseKindModeKeys(bookfileSegDropKind, raw); err != nil {
				return bookfileSegDropResult{}, err
			}
			if err := errNeed(bookfileSegDropKind, "Index.Store", d.Store != nil); err != nil {
				return bookfileSegDropResult{}, err
			}
			env := bookfileSegDropEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := indexWriterFor(bookfileSegDropKind, d, rc)
				if err != nil {
					return bookfileSegDropResult{}, err
				}
				env.Writer = w
			}
			return bookfileSegDropCore(rc.Context(), env)
		},
	})
}

// purgeLegacyFPNativeParams has one native field: the cutover date (HEAD's apply is
// the run's mode).
type purgeLegacyFPNativeParams struct {
	CutoverDate string `json:"cutover_date"`
}

// purgeLegacyFPNative is the native dedup.purge-legacy-fp-candidates kind.
// Preview is HEAD's dry run; Live marks the stale rows and sets the flag.
func (p *Plugin) purgeLegacyFPNative(d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(purgeLegacyFPKind, sched, true) // HEAD: NoWarmupWait
	c.Title = "Purge legacy fingerprint candidates"
	c.Help = purgeLegacyFPHelp
	// Writes found: EmbeddingStore.ReclassifyCandidate flips a candidate's status
	// (embeddings); SetSetting writes the done flag (settings). Reads: the book
	// and book-file scans (GetAllBooksCore, GetBookByID, GetAllBookFilesCore).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings, ops.ResSettings)...).AlsoReads(res(ops.ResBooks, ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key("books.dedup.purge-legacy-fp-candidates")
	c.Timeout = 30 * time.Minute
	return tasks.Task(purgeLegacyFPKind, tasks.TaskSpec[json.RawMessage, purgeLegacyFPResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (purgeLegacyFPResult, error) {
			params, err := decodeNativeParams[purgeLegacyFPNativeParams](purgeLegacyFPKind, raw)
			if err != nil {
				return purgeLegacyFPResult{}, err
			}
			cutover, err := purgeLegacyFPCutover(params.CutoverDate)
			if err != nil {
				return purgeLegacyFPResult{}, err
			}
			if err := errNeed(purgeLegacyFPKind, "Index.Store", d.Store != nil); err != nil {
				return purgeLegacyFPResult{}, err
			}
			env := purgeLegacyFPEnv{kindEnv: nativeEnv(rc), Store: d.Store, Cutover: cutover}
			if env.Live {
				w, err := indexWriterFor(purgeLegacyFPKind, d, rc)
				if err != nil {
					return purgeLegacyFPResult{}, err
				}
				env.Writer = w
			}
			return purgeLegacyFPCore(rc.Context(), env)
		},
	})
}

// windowIndexBuildNative is the native dedup.window-index-build kind. Preview is
// HEAD's default preview (dry_run omitted); Live builds the rows and the flag.
func (p *Plugin) windowIndexBuildNative(d IndexKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(windowIndexBuildKind, sched, false)
	c.Title = "Build window-print index"
	c.Help = windowIndexBuildHelp
	// Writes found: PebbleStore.PutWindowIndex writes the fpwinidx: band rows and
	// member list (fingerprint_index, as HEAD declares); SetWindowIndexBuilt is
	// SetSetting (settings), which HEAD's declaration omits.
	c.Effects = tasks.Writes(res(ops.ResFingerprintIndex, ops.ResSettings)...)
	// HEAD Restart, but its Run reads no checkpoint on restart: FromZero. The
	// coordinator's cross-row resume ruling (RULINGS-common.md:6) maps a v2 Restart
	// to Continue only when HEAD reads its checkpoint; run-all does, this does not.
	// A restart behaves as before either way: a file with a member list is skipped
	// on the rerun.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.window-index")
	c.Timeout = 180 * time.Minute
	return tasks.Task(windowIndexBuildKind, tasks.TaskSpec[json.RawMessage, windowIndexBuildResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (windowIndexBuildResult, error) {
			if err := refuseKindModeKeys(windowIndexBuildKind, raw); err != nil {
				return windowIndexBuildResult{}, err
			}
			if err := errNeed(windowIndexBuildKind, "Index.Store", d.Store != nil); err != nil {
				return windowIndexBuildResult{}, err
			}
			env := windowIndexBuildEnv{kindEnv: nativeEnv(rc), Store: d.Store, Fence: nativeFence(rc)}
			if env.Live {
				w, err := indexWriterFor(windowIndexBuildKind, d, rc)
				if err != nil {
					return windowIndexBuildResult{}, err
				}
				env.Writer = w
			}
			return windowIndexBuildCore(rc.Context(), env)
		},
	})
}
