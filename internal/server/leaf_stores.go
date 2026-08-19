// file: internal/server/leaf_stores.go
// version: 1.0.0
// guid: 4d92a6f8-1b07-4e53-9c81-3a05d7e264b9
// last-edited: 2026-08-19

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// Store slices for this package's leaf helpers. Each was database.Store -- 398
// methods -- until 2026-08-19; each figure below is what the function actually
// calls, read off its body rather than guessed.

// seriesPruneReader: computeSeriesPrunePreview.
type seriesPruneReader interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
}

// externalIDBackfillStore: the backfiller's own field.
type externalIDBackfillStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// importPathLister: cachedImportPaths.
type importPathLister interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// The dismissed-dedup-group helpers read and write one preference key each, so
// they take one method each rather than a shared two-method pair.
type userPreferenceReader interface {
	GetUserPreference(key string) (*database.UserPreference, error)
}

type userPreferenceWriter interface {
	SetUserPreference(key, value string) error
}

// entityAssignStore: assignPublisherPreservingRecord.
type entityAssignStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// authorAssignStore: assignResolvedAuthorPreservingRecord.
type authorAssignStore interface {
	entityAssignStore

	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// adminBootstrapStore: findOrCreateAdminUser.
type adminBootstrapStore interface {
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*database.User, error)
	GetRoleByID(id string) (*database.Role, error)
	GetRoleByName(name string) (*database.Role, error)
	ListUsers() ([]database.User, error)
}

// startupKeyStore: InitStartupReadOnlyKey, which also creates the admin user.
type startupKeyStore interface {
	adminBootstrapStore

	CreateAPIKey(key *database.APIKey) (*database.APIKey, error)
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	DeleteSetting(key string) error
}

// These two wrappers forward into packages that already declare what they need,
// so both are embedded by name and re-narrow on their own.
type trashedVersionCleaner = versions.TrashedVersionCleaner

type undoConflictChecker = undo.ConflictChecker
