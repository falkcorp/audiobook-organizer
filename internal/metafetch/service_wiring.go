// file: internal/metafetch/service_wiring.go
// version: 1.11.0
// guid: 571bfbf4-238b-49cb-a6d8-b302921dd1c4
// last-edited: 2026-10-16

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

func NewService(db Store) *Service {
	return &Service{db: db}
}

// SetOverrideSources overrides the metadata source chain for testing.
func (mfs *Service) SetOverrideSources(sources []metadata.MetadataSource) {
	mfs.overrideSources = sources
}

// SetActivityService sets the activity service for dual-writing to the unified activity log.
func (mfs *Service) SetActivityService(svc *activity.Service) {
	mfs.activityService = svc
}

// SetSafeWriteDeps installs the Deluge pre-flight guard for cover-art and tag
// writes. Must be called before any cover embedding occurs.
//
// The Importer is dropped: a cover embed or tag write on a protected path is
// REFUSED (tagger.ErrProtectedPathWrite, counted as skipped_protected), never
// imported. An import copied a Deluge-seeding file to RootDir/<basename>,
// outside any book folder, and repointed its row there. A protected book's
// files are written only on its library copy, which lockLibraryCopy resolves.
func (mfs *Service) SetSafeWriteDeps(deps tagger.SafeWriteDeps) {
	deps.Importer = nil
	mfs.safeWriteDeps = deps
}

// SetOLStoreSource sets where local-first Open Library lookups get the dump
// store from. src is called on every lookup and returns nil while there is no
// usable store; it must not block. A nil src means no local store.
func (mfs *Service) SetOLStoreSource(src func() *openlibrary.OLStore) {
	if src == nil {
		mfs.olStoreSource.Store(nil)
		return
	}
	mfs.olStoreSource.Store(&src)
}

// liveOLStore is the current Open Library dump store, or nil. The memoized
// source chain holds this method, not a store, so a store that is closed and
// reopened is picked up without rebuilding the chain.
func (mfs *Service) liveOLStore() *openlibrary.OLStore {
	if src := mfs.olStoreSource.Load(); src != nil {
		return (*src)()
	}
	return nil
}

// wireOpenLibrary points local-first lookups at the Open Library service's
// live store, whatever store that is when a lookup runs: none at start-up and
// one after the first import, or a reopened one after a delete.
func (mfs *Service) wireOpenLibrary(ol *OpenLibraryService) {
	mfs.SetOLStoreSource(ol.LiveStore)
}

// SetDedupEngine sets the dedup engine for post-apply dedup checks.
func (mfs *Service) SetDedupEngine(engine *dedup.Engine) {
	mfs.dedupEngine = engine
}

// SetOpEnqueuer wires the op registry so post-apply dedup checks can go
// through dedup.check-book when dedup.on_import_via_scheduler is on. Call it
// before the registry starts, like SetDedupEngine.
func (mfs *Service) SetOpEnqueuer(enq dedup.OpEnqueuer) {
	mfs.opEnqueuer = enq
}

// SetMetadataScorer injects the pluggable metadata candidate scorer. A nil
// scorer (or a scorer that returns errors at runtime) makes the search
// pipeline fall back to the pre-existing significantWords F1 path, so this
// method is safe to leave unset.
func (mfs *Service) SetMetadataScorer(scorer ai.MetadataCandidateScorer) {
	mfs.metadataScorer = scorer
}

// SetMetadataLLMScorer injects the LLM rerank scorer. A nil scorer or a
// scorer that returns errors at runtime makes the rerank pass a no-op, so
// this method is safe to leave unset.
func (mfs *Service) SetMetadataLLMScorer(scorer ai.MetadataCandidateScorer) {
	mfs.llmScorer = scorer
}

// SetASINBackfillQueue sets the queue a metadata apply hands a book missing
// an ASIN or ISBN to (metafetch.asin-backfill, Audible only). Wired by the
// metafetch plugin, which owns the op. Nil: an apply queues nothing.
// Safe to call concurrently with an apply.
func (mfs *Service) SetASINBackfillQueue(q *ASINBackfillQueue) {
	mfs.asinBackfillQueue.Store(q)
}

// ASINBackfillQueue returns the queue (may be nil).
func (mfs *Service) ASINBackfillQueue() *ASINBackfillQueue {
	return mfs.asinBackfillQueue.Load()
}

// FileWorkScheduler runs work for bookID off the calling goroutine, through
// the server's file-I/O pool. It takes no lock: the caller cannot know which
// path the work writes (for a protected book it is the library copy's, and a
// rename moves it part-way through), so the file work locks for itself
// through the locker set by SetPathLocker. The server supplies both; metafetch
// cannot import the pool or the lock table without an import cycle.
type FileWorkScheduler func(bookID string, work func())

// SetFileWorkScheduler routes auto-fetch's file work through the file-I/O
// pool, where the manual and batch applies' file work also runs.
func (mfs *Service) SetFileWorkScheduler(s FileWorkScheduler) {
	mfs.fileWorkScheduler = s
}

// SetPathLocker wires the server's per-path write lock into the file work
// (FinishApplyFileWork, FinishAutoFetchFileWork). Each write takes it on the
// path of the files it is about to touch, resolved at that moment: the
// library copy's path for a protected book, the files' current path for the
// cover embed and the rename, and the post-rename path for the tag write. So
// an auto-fetch of book A (library copy B), a manual or batch apply of B, and
// a bulk write-back of B all serialize on B's path.
//
// The lock is not reentrant: no caller may hold it around those calls.
func (mfs *Service) SetPathLocker(lock func(path string) func()) {
	mfs.pathLock = lock
}

// SetFileWriteGate wires a non-blocking take on the server's process-wide
// write-back gate (writeBackFileGate.tryAcquire). A book's tag write uses it to
// run extra per-file writers while slots are free, so one book with 58 files
// no longer writes them strictly one after another, and the process never has
// more concurrent writers than the gate allows. It must never block: the
// callers already hold a slot for the book (batch apply, bulk write-back), and
// a blocking take there would deadlock once every slot was held by a book
// waiting for a second.
func (mfs *Service) SetFileWriteGate(tryAcquire func() (release func(), ok bool)) {
	mfs.fileWriteSlot = tryAcquire
}
