// file: internal/books/cutover/m3/config.go
// version: 1.0.0
// guid: 659fd544-6cd1-4274-a149-74e6cd8bc6fb
// last-edited: 2026-11-12

package m3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// AtomRow is one row of books' atom table (05-rbac §6.1): HEAD's atom and the
// native scopes the cutover writes for it. internal/books builds them from its
// AtomTable; this package cannot import it.
type AtomRow struct {
	Name   string
	Scopes []rbac.Permission
}

// SeedRole is one of HEAD's four seed roles (admin, editor, viewer,
// fp-worker; 05-rbac §6.2): the ID HEAD's rows and users carry, which core's
// seed keeps as the native name, and the native set the seed grants (books'
// DefaultGrants and core's seed, resolved by the caller). The HEAD row of a
// seed role is deleted, never imported (HEAD rewrote it from its own seed at
// every boot); the set is what a key's intersection with its owner's roles is
// taken against.
type SeedRole struct {
	ID          string
	Permissions []rbac.Permission
}

// NativeReader reads what core's stores already hold: a user an earlier
// chunk imported (and so a key's owner), and a role an earlier chunk
// imported. auth.UserStore and rbac.Store satisfy it between them; a lookup
// of an absent record wraps auth.ErrNotFound / rbac.ErrUnknownRole.
type NativeReader interface {
	GetUser(ctx context.Context, id auth.UserID) (*auth.User, error)
	GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error)
}

// Config is everything M3 takes from outside. F4-08 builds it from the booted
// core; the tests build it over test doubles of the importers.
type Config struct {
	// StepID is the data migration's ID, named in a blocker's Step.
	StepID string
	// Atoms is books' atom table, in 05-rbac §6.1's order.
	Atoms []AtomRow
	// SeedRoles are HEAD's four seed roles.
	SeedRoles []SeedRole
	// Catalog is core's permission catalog as the cutover build declares it:
	// every scope an atom row, a seed set or an ambient scope names.
	Catalog *rbac.Catalog
	// Ambient is the ambient scope list (05-rbac §6.3: core:self,
	// core:metrics.read, books:reading), written into every imported key and
	// custom role that is not Confined.
	Ambient []rbac.Permission
	// Auth, Roles are core's importers; the same store instances serve
	// Deps (a role store that seals imports at Freeze must be the one the
	// kernel hands to both).
	Auth  auth.Importer
	Roles rbac.Importer
	// Native reads back what an earlier chunk imported.
	Native NativeReader
	// Audit records core.role.create (detail imported: true) in the chunk's
	// batch for each imported custom role. Nil records nothing.
	Audit audit.Logger
	// Legacy is books' legacy range: where Step reads the rows it pulls ahead
	// of its cursor, and where FixBlocker and DeleteBlocker look for the rows
	// that go with a row.
	Legacy func() storage.Reader
	// Now is the cutover's clock; KeyTTL is HEAD's default API-key lifetime
	// (handlers.DefaultAPIKeyTTL), which the one-shot expiry stamp adds to it.
	Now    func() time.Time
	KeyTTL time.Duration
	Logger *slog.Logger
}

// Migration is M3. Build one with New and wire its methods into a
// datamigrate.DataMigration (internal/books/cutover assembles it).
type Migration struct {
	cfg    Config
	log    *slog.Logger
	atoms  map[string][]rbac.Permission
	names  []string          // atom names in table order
	every  []rbac.Permission // the union of every row's scopes, table order
	seeds  map[string]SeedRole
	legacy func() storage.Reader

	// The memo of the chunk in progress. A StepWriter belongs to one chunk, so
	// a writer that is not the memo's starts a new chunk: the first item of
	// each chunk runs the chunk's pulled-forward work (rewrite.go, ensure) and
	// every later item of the chunk finds it done. The importers' batch is
	// indexed (storagemigrate), so an owner staged here is seen by its keys.
	mu         sync.Mutex
	chunk      *datamigrate.StepWriter
	chunkUsers map[auth.UserID][]rbac.RoleName
	chunkRoles map[rbac.RoleName][]rbac.Permission
	chunkNames map[string]string // folded username -> user ID, this chunk
	// chunkPulled is the key of every role and user row begin handled for this
	// chunk (imported, deleted by rule or quarantined). The runner may hand one
	// of them to Step as the chunk's first item, after begin pulled it forward;
	// that item has nothing left to do.
	chunkPulled map[string]struct{}
}

// New validates cfg and builds M3. It never writes.
func New(cfg Config) (*Migration, error) {
	switch {
	case cfg.StepID == "":
		return nil, errors.New("m3: Config.StepID is the data migration's ID")
	case len(cfg.Atoms) == 0:
		return nil, errors.New("m3: Config.Atoms is books' atom table")
	case cfg.Catalog == nil:
		return nil, errors.New("m3: Config.Catalog is core's permission catalog")
	case cfg.Auth == nil || cfg.Roles == nil || cfg.Native == nil:
		return nil, errors.New("m3: Config.Auth, Roles and Native are core's importers and readers")
	case cfg.Legacy == nil:
		return nil, errors.New("m3: Config.Legacy is books' legacy range")
	case cfg.Now == nil:
		return nil, errors.New("m3: Config.Now is the cutover's clock")
	case cfg.KeyTTL <= 0:
		return nil, errors.New("m3: Config.KeyTTL is HEAD's default API-key lifetime")
	case len(cfg.Ambient) == 0:
		return nil, errors.New("m3: Config.Ambient is the ambient scope list")
	}
	m := &Migration{cfg: cfg, log: cfg.Logger, atoms: map[string][]rbac.Permission{}, seeds: map[string]SeedRole{}, legacy: cfg.Legacy}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	for _, a := range cfg.Atoms {
		if a.Name == "" || len(a.Scopes) == 0 {
			return nil, fmt.Errorf("m3: atom %q has no scopes", a.Name)
		}
		if _, dup := m.atoms[a.Name]; dup {
			return nil, fmt.Errorf("m3: atom %q is listed twice", a.Name)
		}
		m.atoms[a.Name] = slices.Clone(a.Scopes)
		m.names = append(m.names, a.Name)
		for _, s := range a.Scopes {
			if !slices.Contains(m.every, s) {
				m.every = append(m.every, s)
			}
		}
	}
	for _, s := range cfg.SeedRoles {
		if s.ID == "" {
			return nil, errors.New("m3: a seed role has no ID")
		}
		m.seeds[s.ID] = s
	}
	admin, ok := m.seeds["admin"]
	if !ok {
		return nil, errors.New("m3: Config.SeedRoles has no admin role: an admin's keys cannot be told apart")
	}
	if !rbac.GrantsAdmin(admin.Permissions) {
		// keyScopes decides who is an admin by this set: a seed that does not
		// grant core:admin would make no owner an admin, and every admin's
		// every-atom key would silently lose core:admin.
		return nil, errors.New("m3: Config.SeedRoles admin does not grant core:admin")
	}
	return m, nil
}

// SourceRanges is the step's Source: the identity families and their idx:
// children, each one prefix range, and the two bootstrap rows, each an exact
// key, sorted by Start as the runner requires.
func SourceRanges() []storage.Range {
	out := make([]storage.Range, 0, len(identityPrefixes)+len(identityKeys))
	for _, p := range identityPrefixes {
		out = append(out, storage.Range{Start: []byte(p), End: prefixEnd(p)})
	}
	for _, k := range identityKeys {
		out = append(out, storage.Range{Start: []byte(k), End: append([]byte(k), 0)})
	}
	slices.SortFunc(out, func(a, b storage.Range) int { return bytes.Compare(a.Start, b.Start) })
	return out
}

// prefixEnd is the exclusive end of the prefix's range.
func prefixEnd(p string) []byte {
	end := bytes.Clone([]byte(p))
	end[len(end)-1]++
	return end
}

// blocked is the step's own refusal of an identity row it cannot import, with
// the report the preflight would have given (the defensive check of
// 05-storage §5S.3.13: a row the preflight passed that the step then
// refuses fails the chunk, never a quarantine).
func (m *Migration) blocked(key, family, reason, fix string) error {
	return &storage.CutoverBlockedError{Blockers: []storage.CutoverBlocker{{
		Step: m.cfg.StepID, Key: []byte(key), Family: family, Reason: reason, Fix: fix,
	}}}
}

// fixHint is the operator's way out for a row of an identity family.
const fixHint = "storage cutover-blockers fix|delete"
