// file: internal/plugins/acoustid/plugin.go
// version: 1.9.0
// guid: d4e5f6a7-b8c9-0123-def0-123456789abc
// last-edited: 2026-09-19

// Package acoustid is the UOS plugin for AcoustID fingerprinting operations.
// It wraps the internal dedup.Engine and registers OperationDefs through
// the public pkg/plugin/sdk interface.
package acoustid

import (
	"context"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin is the AcoustID plugin. It wraps the shared dedup.Engine and embedding store so that
// the Run functions can call engine methods without importing internal packages.
type Plugin struct {
	engine         *dedupengine.Engine
	store          pluginStore
	embeddingStore *database.EmbeddingStore

	// toolsMu guards the window op's tool wiring: SetToolRegistry runs
	// during server startup, the op reads it when dispatched.
	toolsMu      sync.Mutex
	toolResolver fingerprint.ToolResolver
	// scanProbe lets the window op yield to a running library.scan.
	scanProbe libraryScanProbe
	// windowToolsFn replaces tool resolution in tests (fake binaries).
	windowToolsFn func(context.Context) (fingerprint.WindowTools, error)
}

// New constructs an acoustid Plugin. engine and embeddingStore may be nil if embedding is disabled;
// the plugin will no-op gracefully in that case.
func New(engine *dedupengine.Engine, store pluginStore, embeddingStore *database.EmbeddingStore) *Plugin {
	return &Plugin{engine: engine, store: store, embeddingStore: embeddingStore}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "acoustid" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "AcoustID" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all AcoustID OperationDefs with the UOS registry.
func (p *Plugin) Register(r sdk.Registry) error {
	if p.engine == nil {
		return nil
	}

	ops := p.opDefs()

	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}

// opDefs is every op the plugin registers, split out of Register so a test can
// validate each def without a dedup engine.
func (p *Plugin) opDefs() []sdk.OperationDef {
	return []sdk.OperationDef{
		p.scanDef(),
		p.backfillDef(),
		p.fingerprintRescanDef(),
		p.resetAllDef(),
		p.lshBackfillDef(),
		p.onlineLookupDef(),
		p.durationBackfillDef(),
		p.windowBackfillDef(),
	}
}

// pluginStore is what this plugin reads and writes, measured with an
// empty-interface compiler probe under -gcflags=-e: nine methods (split
// below to stay under the interfacebloat limit), no
// forwarding constraints. It was pluginStore -- 398 methods -- until
// 2026-08-19. The window backfill added the four fpwin: sidecar methods
// (2026-09-19); the set is split into three embedded interfaces to stay under
// the interfacebloat width gate.
//
// The batched fast paths (ClearAllAcoustIDFingerprints and friends) live on
// *PebbleStore, not on this interface, and are resolved with
// database.AsPebbleStore rather than a bare assertion -- see reset_all.go.
type pluginStore interface {
	pluginBookReader
	pluginFileStore
	pluginBookSigWriter
	windowSidecarStore
}

// pluginBookReader pages through books for the backfill.
type pluginBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	// CountAllBooks sizes the backfill progress bar now that the op pages
	// through books instead of loading them all up front.
	CountAllBooks() (int, error)
}

// pluginFileStore reads and writes the book_file rows that carry prints.
type pluginFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetFilesWithZeroDurationFingerprint(limit, offset int) ([]database.BookFile, int64, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// pluginBookSigWriter writes the book-level signature.
type pluginBookSigWriter interface {
	// ModifyBook is the book-signature write: a locked read-modify-write of
	// the five BookSig* columns, never GetBookByID -> UpdateBook.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	// ClearBookSignature deletes a book's signature when re-synthesis finds
	// no usable current-era data.
	ClearBookSignature(id string) error
}

// windowSidecarStore is the part of database.FingerprintWindowStore the
// window backfill uses. It is on database.Store (via BookFileFingerprintStore),
// so the production indexedStore satisfies it through its embedded Store at
// compile time; nothing is type-asserted at run time.
type windowSidecarStore interface {
	GetFingerprintWindows(ref database.FingerprintWindowRef) ([]database.FingerprintWindow, error)
	GetFingerprintWindowFailure(ref database.FingerprintWindowRef) (*database.FingerprintWindowFailure, error)
	ReplaceFingerprintWindows(ref database.FingerprintWindowRef, ws []database.FingerprintWindow) error
	RecordFingerprintWindowFailure(f *database.FingerprintWindowFailure) error
}
