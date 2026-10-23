// file: internal/usermerge/service.go
// version: 1.1.0
// guid: 7b3e91d4-2c58-4a07-b6f1-0e94d5a8c3b2
// last-edited: 2026-10-23

// Package usermerge is the admin "merge user B into user A" tool: it plans a
// merge of one account's per-user data into another (a read-only preview with a
// digest), applies it with a before-image journal, and undoes it from that
// journal. Nothing here knows which two accounts are involved; the caller names
// them.
//
// Terms. The survivor (A) is the account that keeps existing and receives
// everything. The absorbed account (B) is retired: it is locked, loses its
// email, is stamped with the survivor's id (database.User.MergedIntoUserID) and
// keeps its row, so history written under its id (the activity log, operation
// actors, the book change log) still resolves to a name. B's own rows are moved
// or merged into A's, never copied, so the census guard in service_test.go can
// assert that no live row of B's remains.
//
// The storage half (the raw key layouts, the compare-and-set batch, the two
// user-row writes) is database.UserMerger, resolved with database.AsCapability.
// It is deliberately not on database.Store.
//
// Mutating entry points (Apply, Resume, Undo) are serialized by one process
// mutex, take merge.LockMergeRMW and the per-(user, book) stripes of every book
// they touch, and are reached only from the maintenance.user-merge operation,
// which redeems a single-use Grant minted by the HTTP handler; see grant.go.
package usermerge

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Store is the slice of database.Store the merge reads. The write side is the
// database.UserMerger and database.UserCredentialWriter capabilities.
type Store interface {
	GetUserByID(id string) (*database.User, error)
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
	ScanPrefix(prefix string) ([]database.KVPair, error)
	ListUserSessions(userID string) ([]database.Session, error)
	RevokeSession(id string) error
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

// ABSSessionRevoker is the optional ABS-session capability (a store without an
// ABS keyspace has no ABS session to revoke).
type ABSSessionRevoker interface {
	RevokeAllABSSessionsForUser(userID string) (int, error)
}

// Journal-and-conflict errors the handler maps to HTTP statuses.
var (
	// ErrRefused wraps a plan that has refusals; the plan itself is in the
	// *RefusedError.
	ErrRefused = errors.New("usermerge: the merge is refused")
	// ErrDigestMismatch: what the admin previewed is not what would be applied
	// now. Nothing was merged.
	ErrDigestMismatch = errors.New("usermerge: the accounts changed since the preview; preview again")
	// ErrOwnerIdentityNotAcknowledged: the absorbed account holds the owner's
	// sign-in and the request did not acknowledge that.
	ErrOwnerIdentityNotAcknowledged = errors.New("usermerge: the account being merged away holds the owner's sign-in; acknowledge it to proceed")
	// ErrJournalNotFound: no such merge journal.
	ErrJournalNotFound = errors.New("usermerge: merge journal not found")
	// ErrJournalState: the journal is not in a state the action works on.
	ErrJournalState = errors.New("usermerge: the merge is not in a state that allows this")
	// ErrUnsupported: the store lacks a capability the merge needs.
	ErrUnsupported = errors.New("usermerge: this store cannot merge users")
)

// RefusedError carries the plan whose refusals stopped an apply.
type RefusedError struct{ Plan *Plan }

func (e *RefusedError) Error() string {
	// Row-quoting refusals are left out: this text reaches operation rows that
	// non-admins can read. The admin's preview has the full list.
	return e.Plan.PublicError()
}

// Unwrap lets errors.Is(err, ErrRefused) match.
func (e *RefusedError) Unwrap() error { return ErrRefused }

// runMu serializes Apply, Resume and Undo process-wide: two merges (or a resume
// racing the run it resumes) never interleave.
var runMu sync.Mutex

// chunkSize is how many ops one ApplyUserMergeOps batch carries. Each batch
// also rewrites the journal with the advanced cursor, so a crash resumes at a
// chunk boundary. A variable so a test can force several chunks.
var chunkSize = 500

// maxSweeps bounds the straggler sweep after the retire step (rows the absorbed
// account's in-flight requests wrote while the merge ran).
const maxSweeps = 3

// Service plans, applies and undoes user merges.
type Service struct {
	store  Store
	merger database.UserMerger
	creds  database.UserCredentialWriter
	abs    ABSSessionRevoker // optional

	// ownerEmail and backfillUserID read config at call time; tests replace them.
	ownerEmail     func() string
	backfillUserID func() string
	now            func() time.Time
	newID          func() string
	// audit, when set, is called once per completed apply or undo.
	audit func(summary string, details map[string]any)
	log   logger.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithOwnerEmail overrides where the configured owner email comes from.
func WithOwnerEmail(f func() string) Option { return func(s *Service) { s.ownerEmail = f } }

// WithBackfillUserID overrides where the pinned iTunes-position backfill user
// id (config abs_itunes_position_backfill_user_id) comes from.
func WithBackfillUserID(f func() string) Option { return func(s *Service) { s.backfillUserID = f } }

// WithClock overrides the clock.
func WithClock(f func() time.Time) Option { return func(s *Service) { s.now = f } }

// WithAudit sets the audit sink called after an apply or undo completes.
func WithAudit(f func(summary string, details map[string]any)) Option {
	return func(s *Service) { s.audit = f }
}

// New resolves the capabilities the merge needs from store. A store without
// database.UserMerger or database.UserCredentialWriter returns ErrUnsupported.
func New(store Store, opts ...Option) (*Service, error) {
	merger, ok := database.AsCapability[database.UserMerger](store)
	if !ok {
		return nil, fmt.Errorf("%w: no user-merge storage capability", ErrUnsupported)
	}
	creds, ok := database.AsCapability[database.UserCredentialWriter](store)
	if !ok {
		return nil, fmt.Errorf("%w: no credential writer", ErrUnsupported)
	}
	s := &Service{
		store: store, merger: merger, creds: creds,
		ownerEmail:     func() string { return config.Snapshot().OwnerEmail },
		backfillUserID: func() string { return config.Snapshot().ABSItunesPositionBackfillUserID },
		now:            time.Now,
		newID:          func() string { return ulid.Make().String() },
		log:            logger.New("usermerge"),
	}
	if r, ok := database.AsCapability[ABSSessionRevoker](store); ok {
		s.abs = r
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}
