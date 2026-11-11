// file: core/secrets/store.go
// version: 1.0.3
// guid: be9e243c-f9df-4ff7-930d-10a5b90f2f49
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/keyedmutex"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// writeAttempts bounds Put's and Delete's storage.RetryOnConflict.
const writeAttempts = 5

// Options configures Open (05-config §2.3).
type Options struct {
	StateDir *os.Root // opened by the kernel on core.paths.state_dir
	// KeyFile is the keyring file, relative to StateDir; "" is the default
	// "secrets.keyring" (core.secrets.key_file). A dry run passes a path
	// inside its scratch directory (§2.4 step 1).
	KeyFile  string
	Keyspace storage.Keyspace // c/secrets/
	// AppID is app.Options.AppID: a compiled constant of the app's main,
	// bound into every record's AAD and compared with c/secrets/meta.
	AppID string
	// AllowKeyRegen is core.secrets.allow_key_regen: with no keyring and
	// sealed records, generate new keys and keep the records (their reads
	// fail with ErrKeyUnavailable until the old keyring is put back).
	AllowKeyRegen bool
	// ResetBoot is set by the kernel only on the reset boot of a factory
	// reset (05-storage §5S.3.15): Open rewrites the keyring header's
	// install_id to InstallID. Every other Open leaves that field alone.
	ResetBoot bool
	// InstallID is the open store's c/storage/install marker (a name coined
	// by F3-03: the c/secrets/ keyspace cannot read c/storage/, so the
	// kernel passes storage.Admin.InstallID). Open writes it into the
	// keyring header when the header has none; ResetBoot requires it.
	InstallID string
	Logger    *slog.Logger
	Metrics   *metrics.Scope // the core "secrets" scope; nil = Instrument later
	Now       func() time.Time
}

// ephemeralValue is an operator-supplied value (env or file). The Store holds
// it by pointer (Store.ephemeral): see Store.String for why.
type ephemeralValue struct {
	v   Value
	src Source
}

// Store is the secret store over c/secrets/ and the keyring file. It is safe
// for concurrent use: reads take a read lock on the in-memory keyring only,
// writes serialize per name through a keyed mutex, and the keyring changes
// only at Open (05-config §2.6). It starts no goroutine.
type Store struct {
	ks      storage.Keyspace
	appID   string
	keyFile string
	root    *os.Root
	log     *slog.Logger
	now     func() time.Time

	writes keyedmutex.Mutex // per name: Put, Delete, Stage, StageUnreadable, the importer

	mu        sync.RWMutex
	ring      *keyring
	retired   map[string]bool          // verify derive keys RetireUnused retired, this process only
	blocked   []string                 // verify derive keys past retire_after kept by the in-use check
	ephemeral map[Name]*ephemeralValue // pointers: see String
	closed    bool

	ctr counters
}

// validAppID reports whether id is [a-z][a-z0-9]{1,15} (03 §3.6).
func validAppID(id string) bool {
	if len(id) < 2 || len(id) > 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// Open opens the store: it checks c/secrets/meta against AppID, removes the
// keyring's stale temporary files, resolves the keyring (05-config §2.4 step
// 1), then writes c/secrets/meta when absent or when the keyring holds a key
// number above the one it records. It deletes no record and no key, ever.
// The kernel calls it in 06 P4b, after the P4a checkpoint and before the
// migration list, with the store lock held.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.StateDir == nil {
		return nil, errors.New("secrets: Open needs the state directory")
	}
	if !validAppID(o.AppID) {
		return nil, fmt.Errorf("secrets: app ID %q is not [a-z][a-z0-9]{1,15}", o.AppID)
	}
	if o.Keyspace.Prefix() == "" {
		return nil, fmt.Errorf("secrets: Open needs the c/secrets/ keyspace: %w", storage.ErrInvalidKeyspace)
	}
	if o.InstallID != "" && !validInstallID(o.InstallID) {
		return nil, errors.New("secrets: the install ID is not 1 to 128 printable characters without spaces")
	}
	if o.ResetBoot && o.InstallID == "" {
		return nil, errors.New("secrets: a reset boot needs the fresh store's install ID")
	}
	keyFile, err := cleanKeyFile(o.KeyFile)
	if err != nil {
		return nil, err
	}
	s := &Store{
		ks: o.Keyspace, appID: o.AppID, keyFile: keyFile, root: o.StateDir,
		log: o.Logger, now: o.Now,
		retired: map[string]bool{}, ephemeral: map[Name]*ephemeralValue{},
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	meta, metaRaw, err := s.checkMeta(ctx)
	if err != nil {
		return nil, err
	}
	s.removeStaleTemps()
	f, err := s.resolveKeyring(ctx, o, meta)
	if err != nil {
		return nil, err
	}
	if s.ring, err = buildRing(f); err != nil {
		return nil, err
	}
	if err := s.recordKeyNumbers(ctx, meta, metaRaw, f.maxIDNumber()); err != nil {
		s.ring.wipe()
		return nil, err
	}
	if o.Metrics != nil {
		if err := s.Instrument(o.Metrics); err != nil {
			s.ring.wipe()
			return nil, err
		}
	}
	return s, nil
}

// checkMeta reads c/secrets/meta and its bytes (nil, nil when absent);
// another app ID is ErrAppIDMismatch naming both.
func (s *Store) checkMeta(ctx context.Context) (*storeMeta, []byte, error) {
	raw, err := s.ks.Get(ctx, metaKey())
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: read c/secrets/meta: %w", err)
	}
	m, err := decodeStoreMeta(raw)
	if err != nil {
		return nil, nil, err
	}
	if m.AppID != s.appID {
		return nil, nil, fmt.Errorf("%w: c/secrets/meta names app %q, this binary is app %q", ErrAppIDMismatch, m.AppID, s.appID)
	}
	return m, raw, nil
}

// recordKeyNumbers keeps c/secrets/meta's max_key_id at or above the
// keyring's highest key number n: it writes meta when absent, and rewrites
// it (expecting the bytes read) when n is higher than it records. Every key
// a running service derived or sealed under was in a keyring some Open
// loaded, so a regenerated keyring that numbers past max_key_id never gives
// an old key's ID to new material, exported or not.
func (s *Store) recordKeyNumbers(ctx context.Context, m *storeMeta, old []byte, n int) (err error) {
	if m != nil && m.MaxKeyID >= n {
		return nil
	}
	next := storeMeta{AppID: s.appID, KeyringFormat: keyringFormat, Created: s.now().UTC(), MaxKeyID: n}
	if m != nil {
		next.KeyringFormat, next.Created = m.KeyringFormat, m.Created
	}
	raw, err := encodeJSON(next)
	if err != nil {
		return err
	}
	b := s.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := b.Expect(s.ks, metaKey(), old); err != nil {
		return fmt.Errorf("secrets: write c/secrets/meta: %w", err)
	}
	if err := b.Set(metaKey(), raw); err != nil {
		return fmt.Errorf("secrets: write c/secrets/meta: %w", err)
	}
	err = b.Commit(ctx, storage.Sync)
	if errors.Is(err, storage.ErrConflict) {
		// Another Open wrote meta first: accepted when it names this app
		// and records at least n.
		cur, _, cerr := s.checkMeta(ctx)
		if cerr != nil {
			return cerr
		}
		if cur == nil || cur.MaxKeyID < n {
			return fmt.Errorf("secrets: c/secrets/meta changed under Open and records key number %d, below the keyring's %d", maxKeyID(cur), n)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets: write c/secrets/meta: %w", err)
	}
	return nil
}

// maxKeyID is m's max_key_id, 0 for no meta.
func maxKeyID(m *storeMeta) int {
	if m == nil {
		return 0
	}
	return m.MaxKeyID
}

// removeStaleTemps removes the keyring's leftover temporary files
// (<key file>.tmp-<12 hex>, 0600, holding key material) that a crash
// between create and rename left. Only the keyring's own writers make them
// (Open, rotate-derive and set-install-id), and all three run under the
// store lock Open's caller holds, so any found now is stale. Only regular
// files of that exact name shape are removed; the export record's
// temporaries are left, because export-keyring runs without the lock. A
// failure is logged, never fatal.
func (s *Store) removeStaleTemps() {
	dir, base := filepath.Split(s.keyFile)
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(s.root.FS(), filepath.ToSlash(filepath.Clean(dir)))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("secrets: could not list the keyring directory for stale temporary files", "key_file", s.keyFile, "error", err)
		}
		return
	}
	for _, e := range entries {
		suffix, ok := strings.CutPrefix(e.Name(), base+".tmp-")
		if !ok || !isTempSuffix(suffix) || !e.Type().IsRegular() {
			continue
		}
		name := filepath.Join(dir, e.Name())
		if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("secrets: could not remove a stale keyring temporary file", "file", name, "error", err)
			continue
		}
		s.log.Info("secrets: removed a stale keyring temporary file", "file", name)
	}
}

// isTempSuffix reports whether s is writeFileAtomic's temporary suffix: 12
// lower-case hex characters.
func isTempSuffix(s string) bool {
	if len(s) != 12 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// resolveKeyring is §2.4 step 1's keyring half: load a present keyring
// (fixing its mode or failing closed), generate a first one, or refuse with
// ErrKeyMissing when sealed records exist (AllowKeyRegen generates new keys
// and keeps them). It writes install_id into the header when absent, and
// rewrites it on a reset boot only. A new keyring is k<n> and d<n> with n
// past every key number known: the sealed records' key IDs, c/secrets/meta's
// max_key_id (m) and the export record's IDs. It is created exclusively,
// never over a keyring that appeared meanwhile. The keyring path must hold a
// regular file or nothing: a symbolic link (dangling or not), a directory or
// any other entry is refused, never followed and never replaced, as
// export-keyring refuses one (05-config §2.4 names a file, 0600, written
// atomically; nothing there allows a link).
func (s *Store) resolveKeyring(ctx context.Context, o Options, m *storeMeta) (*keyringFile, error) {
	fi, statErr := s.root.Lstat(s.keyFile)
	switch {
	case statErr == nil && fi.Mode().IsRegular():
		return s.loadKeyring(o)
	case statErr == nil:
		return nil, notRegularKeyring(s.keyFile, fi.Mode())
	case !errors.Is(statErr, fs.ErrNotExist):
		return nil, fmt.Errorf("secrets: stat the keyring %s: %w", s.keyFile, statErr)
	}
	sealed, maxKID, err := s.countSealed(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	known := max(maxKID, maxKeyID(m), s.exportedMaxID())
	if sealed == 0 {
		f, err := newKeyringFile(known+1, o.InstallID, now)
		if err != nil {
			return nil, err
		}
		if err := writeKeyringNew(s.root, s.keyFile, f); err != nil {
			return nil, err
		}
		if known == 0 {
			s.log.Info("secrets: created a new keyring", "key_file", s.keyFile, "key_ids", []string{f.Keys[0].ID, f.Keys[1].ID})
		} else {
			s.log.Warn("secrets: the keyring was missing; no stored secret needs it, so a new one was created. "+
				"What was signed or encrypted under the old derive keys (backup MACs, archives, sign-in state) no longer verifies "+
				"until the old keyring is put back",
				"key_file", s.keyFile, "key_ids", []string{f.Keys[0].ID, f.Keys[1].ID}, "highest_known_key_number", known)
		}
		return f, nil
	}
	if !o.AllowKeyRegen {
		return nil, fmt.Errorf("%w: the keyring %s is missing and %d stored secret(s) need it; "+
			"put the keyring back (from the copy `secrets export-keyring` wrote, or from a host-loss restore's --keyring), "+
			"or set core.secrets.allow_key_regen=true to start a new keyring and re-enter those secrets",
			ErrKeyMissing, s.keyFile, sealed)
	}
	f, err := newKeyringFile(known+1, o.InstallID, now)
	if err != nil {
		return nil, err
	}
	if err := writeKeyringNew(s.root, s.keyFile, f); err != nil {
		return nil, err
	}
	s.log.Warn("secrets: the keyring was missing; core.secrets.allow_key_regen generated a new one. "+
		"The stored secrets under the old keys are kept and read as \"must be re-entered\" until the old keyring is put back",
		"key_file", s.keyFile, "kept_records", sealed, "key_ids", []string{f.Keys[0].ID, f.Keys[1].ID})
	return f, nil
}

// notRegularKeyring is the refusal of an entry at the keyring path that is
// not a regular file.
func notRegularKeyring(keyFile string, mode fs.FileMode) error {
	what := fmt.Sprintf("an entry of mode %v", mode)
	switch {
	case mode&fs.ModeSymlink != 0:
		what = "a symbolic link"
	case mode.IsDir():
		what = "a directory"
	}
	return fmt.Errorf("secrets: the keyring path %s holds %s, not a regular file; it is never followed or replaced: "+
		"put the keyring file itself there, or remove the entry so a new keyring is created", keyFile, what)
}

// exportedMaxID is the highest key number the export record names, 0 when
// there is none. A record that cannot be read is logged and counts as none:
// c/secrets/meta's max_key_id still bounds the numbering.
func (s *Store) exportedMaxID() int {
	r, err := readExportRecord(s.root, exportedRecordName(s.keyFile))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("secrets: the export record cannot be read; numbering a new keyring without it", "key_file", s.keyFile, "error", err)
		}
		return 0
	}
	return r.maxIDNumber()
}

// loadKeyring reads a present keyring: its mode fixed to 0600 or
// ErrKeyfileInsecure, a read failure never answered by a new keyring, the
// header's install_id written when absent or rewritten on a reset boot.
func (s *Store) loadKeyring(o Options) (*keyringFile, error) {
	fixed, err := checkMode(s.root, s.keyFile)
	if err != nil {
		return nil, err
	}
	if fixed {
		s.log.Warn("secrets: the keyring was readable by group or others; set it to 0600", "key_file", s.keyFile)
	}
	f, err := readKeyring(s.root, s.keyFile)
	if err != nil {
		return nil, fmt.Errorf("secrets: the keyring %s exists but cannot be read (it is never replaced): %w", s.keyFile, err)
	}
	switch {
	case o.ResetBoot && f.InstallID != o.InstallID:
		s.log.Info("secrets: reset boot: the keyring header takes the fresh store's install ID",
			"key_file", s.keyFile, "old_install_id", f.InstallID, "install_id", o.InstallID)
		f.InstallID = o.InstallID
	case !o.ResetBoot && f.InstallID == "" && o.InstallID != "":
		f.InstallID = o.InstallID
	default:
		if o.InstallID != "" && f.InstallID != o.InstallID {
			s.log.Warn("secrets: the keyring header names another install than the store; left unchanged "+
				"(secrets set-install-id sets it back after a factory-reset undo)",
				"key_file", s.keyFile, "header_install_id", f.InstallID, "store_install_id", o.InstallID)
		}
		return f, nil
	}
	if err := writeKeyring(s.root, s.keyFile, f); err != nil {
		return nil, err
	}
	return f, nil
}

// countSealed counts the records that need a keyring: sealed records and
// records that do not decode (fail closed: they may be sealed). Cleared and
// unreadable markers need none. maxKID is the largest key-ID number the
// sealed records name, so a regenerated keyring never reuses an ID.
func (s *Store) countSealed(ctx context.Context) (n, maxKID int, err error) {
	for kv, err := range s.ks.ScanPrefix(ctx, recordPrefix()) {
		if err != nil {
			return 0, 0, fmt.Errorf("secrets: scan c/secrets/: %w", err)
		}
		rec, derr := decodeRecord(kv.Value)
		if derr != nil {
			n++
			continue
		}
		if rec.sealed() {
			n++
			maxKID = max(maxKID, idNumber(rec.KID))
		}
	}
	return n, maxKID, nil
}

// fingerprint is the first 8 hex of HMAC-SHA256(fingerprint subkey, value).
func (r *keyring) fingerprint(value []byte) string {
	m := hmac.New(sha256.New, r.fp)
	m.Write(value)
	return hex.EncodeToString(m.Sum(nil))[:8]
}

// reasonErr is the Meta.Err of an unreadable marker's reason.
func reasonErr(reason string) error {
	if reason == reasonKey {
		return ErrKeyUnavailable
	}
	return ErrCorrupt
}

// lookup is Get's and Meta's read (05-config §2.3 "Read order"): the
// operator value, else the record, else ErrNotSet. withValue keeps the
// plaintext for Get; Meta and All zero it after the fingerprint.
func (s *Store) lookup(ctx context.Context, n Name, withValue bool) (Value, Meta, error) {
	if err := n.Validate(); err != nil {
		return Value{}, Meta{Name: n}, err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return Value{}, Meta{Name: n}, errClosed
	}
	if eph, ok := s.ephemeral[n]; ok {
		v, m := s.ephemeralMetaLocked(n, eph, withValue)
		s.mu.RUnlock()
		return v, m, nil
	}
	s.mu.RUnlock()
	raw, err := s.ks.Get(ctx, recordKey(n))
	if errors.Is(err, storage.ErrNotFound) {
		return Value{}, Meta{Name: n}, ErrNotSet
	}
	if err != nil {
		return Value{}, Meta{Name: n}, fmt.Errorf("secrets: read %s: %w", n, err)
	}
	return s.fromRecord(n, raw, withValue)
}

// ephemeralMetaLocked is the Meta (and, for Get, a copy of the value) of an
// operator value. The caller holds s.mu and has checked the store is open:
// the fingerprint and the copy are taken under the same lock as the map
// read, because Ephemeral wipes a replaced value under the write lock, so no
// reader ever touches the store's own bytes outside it (copy on read).
func (s *Store) ephemeralMetaLocked(n Name, eph *ephemeralValue, withValue bool) (Value, Meta) {
	m := Meta{Name: n, Set: true, Source: eph.src, Fingerprint: s.ring.fingerprint(eph.v.raw())}
	if !withValue {
		return Value{}, m
	}
	return valueOf(eph.v.Bytes()), m
}

// fromRecord reads one record's bytes: a cleared marker is ErrNotSet, an
// unreadable marker or a failed decryption is Meta.Err (and the error).
func (s *Store) fromRecord(n Name, raw []byte, withValue bool) (Value, Meta, error) {
	rec, err := decodeRecord(raw)
	if err != nil {
		return Value{}, Meta{Name: n, Set: true, Source: SourceStored, Err: ErrCorrupt}, ErrCorrupt
	}
	m := Meta{Name: n, Source: SourceStored, UpdatedAt: rec.At, UpdatedBy: rec.By}
	switch {
	case rec.Cleared:
		m.Source = 0
		return Value{}, m, ErrNotSet
	case rec.Unreadable != "":
		m.Set, m.Err = true, reasonErr(rec.Unreadable)
		return Value{}, m, m.Err
	}
	m.Set, m.KeyID = true, rec.KID
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Value{}, Meta{Name: n}, errClosed
	}
	pt, err := s.ring.open(s.appID, n, rec)
	if err != nil {
		m.Err = err
		return Value{}, m, err
	}
	m.Fingerprint = s.ring.fingerprint(pt)
	if !withValue {
		clear(pt)
		return Value{}, m, nil
	}
	return valueOf(pt), m, nil
}

// Get reads n (05-config §2.3 "Read order"): ErrNotSet when nothing is set
// or the record is a cleared marker; ErrKeyUnavailable or ErrCorrupt (also
// in Meta.Err) when the record cannot be decrypted or is an unreadable
// marker.
func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error) {
	v, m, err := s.lookup(ctx, n, true)
	s.ctr.op(opGet, err)
	return v, m, err
}

// Meta describes n without its value. Unset (or cleared) is Meta.Set false
// and a nil error; a record that cannot be decrypted is Meta.Set true with
// Meta.Err and a nil error. The error is for an invalid name, a storage
// failure or a closed store.
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error) {
	_, m, err := s.lookup(ctx, n, false)
	if errors.Is(err, ErrNotSet) || errors.Is(err, ErrKeyUnavailable) || errors.Is(err, ErrCorrupt) {
		return m, nil
	}
	return m, err
}

// errLocked: a write to a name an operator value locks.
func errLocked(n Name) error {
	return fmt.Errorf("secrets: %s is set by the operator (environment or config file) and cannot be changed here", n)
}

// locked reports whether an operator value locks n.
func (s *Store) locked(n Name) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, errClosed
	}
	_, ok := s.ephemeral[n]
	return ok, nil
}

// checkWrite validates w: a valid name, a non-empty By, and either a value
// or Delete with a zero value.
func checkWrite(w Write) error {
	if err := w.Name.Validate(); err != nil {
		return err
	}
	if w.By == "" {
		return fmt.Errorf("secrets: a write of %s names no actor (Write.By)", w.Name)
	}
	switch {
	case w.Delete && !w.Value.IsZero():
		return fmt.Errorf("secrets: a delete of %s carries a value", w.Name)
	case !w.Delete && w.Value.IsZero():
		return fmt.Errorf("secrets: a write of %s carries no value (set Delete to clear it)", w.Name)
	}
	return nil
}

// Put encrypts v under n with the active data key and commits it. A storage
// error changes nothing. A name an operator value locks is refused.
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error) {
	m, err := s.write(ctx, Write{Name: n, Value: v, By: by})
	s.ctr.op(opPut, err)
	return m, err
}

// Delete writes n's cleared marker (05-config §2.3); the record's history
// (at, by) stays. A name an operator value locks is refused.
func (s *Store) Delete(ctx context.Context, n Name, by string) error {
	_, err := s.write(ctx, Write{Name: n, Delete: true, By: by})
	s.ctr.op(opDelete, err)
	return err
}

// write is Put and Delete: one batch of its own, retried on conflict.
func (s *Store) write(ctx context.Context, w Write) (Meta, error) {
	if err := checkWrite(w); err != nil {
		return Meta{Name: w.Name}, err
	}
	if lk, err := s.locked(w.Name); err != nil || lk {
		return Meta{Name: w.Name}, errors.Join(err, lockedErr(lk, w.Name))
	}
	unlock := s.writes.Lock(string(w.Name))
	defer unlock()
	var m Meta
	err := storage.RetryOnConflict(ctx, writeAttempts, func(ctx context.Context) (err error) {
		b := s.ks.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		if m, err = s.stage(ctx, b, w); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return Meta{Name: w.Name}, fmt.Errorf("secrets: write %s: %w", w.Name, err)
	}
	return m, nil
}

// lockedErr is errLocked when lk, else nil.
func lockedErr(lk bool, n Name) error {
	if !lk {
		return nil
	}
	return errLocked(n)
}

// Stage stages w into the caller's batch: it binds c/secrets/ to b, records
// an Expect on n's key (c/secrets/ is Serialized) and writes the sealed
// record or the cleared marker. It never commits. ctx is the read of the
// Expect (a deviation from §2.3's Stage(b, w), matching
// configstore.SecretStager). A name an operator value locks is refused.
func (s *Store) Stage(ctx context.Context, b *storage.Batch, w Write) error {
	if b == nil {
		return errors.New("secrets: Stage needs the caller's batch")
	}
	if err := checkWrite(w); err != nil {
		return err
	}
	if lk, err := s.locked(w.Name); err != nil || lk {
		return errors.Join(err, lockedErr(lk, w.Name))
	}
	unlock := s.writes.Lock(string(w.Name))
	defer unlock()
	_, err := s.stage(ctx, b, w)
	return err
}

// StageUnreadable stages n's unreadable marker (reason "key" or "corrupt")
// into the caller's batch: a pre-core secret the cutover could not decrypt
// (05-config §1.12.2). Reads then fail with ErrKeyUnavailable or ErrCorrupt
// until n is set again. The cutover's marker is not refused by an operator
// value: the record is what must be re-entered once that value goes away.
func (s *Store) StageUnreadable(ctx context.Context, b *storage.Batch, n Name, reason string, by string) error {
	if err := checkUnreadable(b, n, reason, by); err != nil {
		return err
	}
	unlock := s.writes.Lock(string(n))
	defer unlock()
	_, err := s.stageMarker(ctx, b, n, reason, by)
	return err
}

// checkUnreadable validates StageUnreadable's and ImportUnreadable's input.
func checkUnreadable(b *storage.Batch, n Name, reason, by string) error {
	if b == nil {
		return errors.New("secrets: StageUnreadable needs the caller's batch")
	}
	if err := n.Validate(); err != nil {
		return err
	}
	if reason != reasonKey && reason != reasonCorrupt {
		return fmt.Errorf("secrets: unreadable reason %q for %s, want key or corrupt", reason, n)
	}
	if by == "" {
		return fmt.Errorf("secrets: the unreadable marker of %s names no actor", n)
	}
	return nil
}

// bindForUpdate binds c/secrets/ to b and reads n's record with an Expect on
// its committed value; a nil record is absent.
func (s *Store) bindForUpdate(ctx context.Context, b *storage.Batch, n Name) ([]byte, error) {
	b.Bind(s.ks)
	raw, err := b.GetForUpdate(ctx, s.ks, recordKey(n))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: stage %s: %w", n, err)
	}
	return raw, nil
}

// stage stages w (already checked) into b; the caller holds n's lock.
func (s *Store) stage(ctx context.Context, b *storage.Batch, w Write) (Meta, error) {
	if _, err := s.bindForUpdate(ctx, b, w.Name); err != nil {
		return Meta{Name: w.Name}, err
	}
	return s.put(b, w)
}

// put writes w's record into b (n bound, Expect recorded).
func (s *Store) put(b *storage.Batch, w Write) (Meta, error) {
	at := s.now().UTC()
	m := Meta{Name: w.Name, UpdatedAt: at, UpdatedBy: w.By}
	var rec *record
	if w.Delete {
		rec = &record{V: recordVersion, Cleared: true, At: at, By: w.By}
	} else {
		s.mu.RLock()
		if s.closed {
			s.mu.RUnlock()
			return Meta{Name: w.Name}, errClosed
		}
		sealed, err := s.ring.seal(s.appID, w.Name, w.Value.raw(), at, w.By)
		fp := s.ring.fingerprint(w.Value.raw())
		s.mu.RUnlock()
		if err != nil {
			return Meta{Name: w.Name}, err
		}
		rec = sealed
		m.Set, m.Source, m.KeyID, m.Fingerprint = true, SourceStored, sealed.KID, fp
	}
	raw, err := encodeRecord(rec)
	if err != nil {
		return Meta{Name: w.Name}, err
	}
	if err := b.In(s.ks).Set(recordKey(w.Name), raw); err != nil {
		return Meta{Name: w.Name}, fmt.Errorf("secrets: stage %s: %w", w.Name, err)
	}
	return m, nil
}

// stageMarker stages n's unreadable marker into b; the caller holds n's
// lock.
func (s *Store) stageMarker(ctx context.Context, b *storage.Batch, n Name, reason, by string) (Meta, error) {
	if _, err := s.bindForUpdate(ctx, b, n); err != nil {
		return Meta{Name: n}, err
	}
	return s.putMarker(b, n, reason, by)
}

// putMarker writes n's unreadable marker into b (n bound, Expect recorded).
func (s *Store) putMarker(b *storage.Batch, n Name, reason, by string) (Meta, error) {
	at := s.now().UTC()
	raw, err := encodeRecord(&record{V: recordVersion, Unreadable: reason, At: at, By: by})
	if err != nil {
		return Meta{Name: n}, err
	}
	if err := b.In(s.ks).Set(recordKey(n), raw); err != nil {
		return Meta{Name: n}, fmt.Errorf("secrets: stage %s: %w", n, err)
	}
	return Meta{Name: n, Set: true, Source: SourceStored, UpdatedAt: at, UpdatedBy: by, Err: reasonErr(reason)}, nil
}

// Ephemeral registers an operator-supplied value for n (env, a <NAME>_FILE
// or an ecosystem binding, or the config file): it is read first and locks
// n against Put, Delete and Stage. It lives in memory only and is never
// persisted. A name that fails the grammar or a source other than SourceEnv
// or SourceFile is refused with an error log line (config.Load checks both
// before it calls).
func (s *Store) Ephemeral(n Name, v Value, src Source) {
	if err := n.Validate(); err != nil {
		s.log.Error("secrets: an operator value with an invalid name was ignored", "error", err)
		return
	}
	if src != SourceEnv && src != SourceFile {
		s.log.Error("secrets: an operator value with a source other than env or file was ignored", "name", string(n), "source", int(src))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	// Readers copy an operator value under s.mu (ephemeralMetaLocked), so
	// with the write lock held none can still hold the old bytes: they are
	// wiped once the new value is in place.
	old := s.ephemeral[n]
	s.ephemeral[n] = &ephemeralValue{v: valueOf(v.Bytes()), src: src}
	if old != nil {
		clear(old.v.raw())
	}
}

// All yields the Meta of every secret the store knows, sorted by name: each
// record (cleared markers as Set false), with operator values overlaid.
// It never yields a value. A storage error is yielded once, last.
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error] {
	return func(yield func(Meta, error) bool) {
		metas, err := s.allMetas(ctx)
		if err != nil {
			yield(Meta{}, err)
			return
		}
		for _, m := range metas {
			if !yield(m, nil) {
				return
			}
		}
	}
}

// allMetas is All's list.
func (s *Store) allMetas(ctx context.Context) ([]Meta, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, errClosed
	}
	eph := make(map[Name]Meta, len(s.ephemeral))
	for n, e := range s.ephemeral {
		_, eph[n] = s.ephemeralMetaLocked(n, e, false)
	}
	s.mu.RUnlock()
	byName := map[Name]Meta{}
	for kv, err := range s.ks.ScanPrefix(ctx, recordPrefix()) {
		if err != nil {
			return nil, fmt.Errorf("secrets: scan c/secrets/: %w", err)
		}
		n, ok := nameOfRecordKey(kv.Key)
		if !ok {
			s.log.Warn("secrets: a c/secrets/ record key that names no valid secret was skipped")
			continue
		}
		if _, isEph := eph[n]; isEph {
			continue
		}
		_, m, err := s.fromRecord(n, kv.Value, false)
		if errors.Is(err, errClosed) {
			return nil, err
		}
		byName[n] = m
	}
	maps.Copy(byName, eph)
	names := slices.Sorted(maps.Keys(byName))
	out := make([]Meta, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// Undecryptable is the names of the stored secrets that cannot be read: a
// key the keyring lacks, a failed authentication, an unreadable marker or a
// record that does not decode (05-health secrets_undecryptable; a name
// coined by F3-03). An operator value hides its record, as it does in Get.
func (s *Store) Undecryptable(ctx context.Context) ([]Name, error) {
	metas, err := s.allMetas(ctx)
	if err != nil {
		return nil, err
	}
	var out []Name
	for _, m := range metas {
		if m.Err != nil {
			out = append(out, m.Name)
		}
	}
	return out, nil
}

// KeyIDsReferenced scans the records readable through r (the c/secrets/
// keyspace of the live store, or of a staged restore; keys relative to it)
// and reports every key ID the sealed ones need, sorted (DATA3-12). Records
// that do not decode are skipped.
func (s *Store) KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error) {
	if r == nil {
		return nil, errors.New("secrets: KeyIDsReferenced needs a reader")
	}
	seen := map[string]bool{}
	p := recordPrefix()
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: p, End: keys.PrefixEnd(p)}) {
		if err != nil {
			return nil, fmt.Errorf("secrets: scan the records: %w", err)
		}
		rec, derr := decodeRecord(kv.Value)
		if derr != nil || !rec.sealed() {
			continue
		}
		seen[rec.KID] = true
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// KeyFile is the keyring file's path relative to the state directory.
func (s *Store) KeyFile() string { return s.keyFile }

// KeyIDs is every key ID of the loaded keyring, in file order.
func (s *Store) KeyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil
	}
	return s.ring.ids()
}

// String names the store's app and keyring file, never a value or a key:
// the operator values and the keyring sit in unexported fields, which fmt
// would print raw.
//
// String, GoString and Format cover the Store printed itself or through an
// exported field. Through an unexported field (a wrapper such as the kernel's
// kernelSecrets, or a bound Ref's resolver) fmt cannot call them: a pointer
// printed with a verb it does not take (%s, %q) is reprinted with %v at
// depth 0, which walks the Store's fields one level and prints every pointer
// below as an address. So every field that holds key material sits behind a
// pointer: ring is a *keyring. A field holding a keyMaterial, a keyring type
// or a []byte of key material by value would print it raw
// (TestStore_SecretFieldsSitBehindPointers). Operator values are Values,
// whose bytes fmt never reaches whatever holds them (see Value); ephemeral
// maps to *ephemeralValue as well.
func (s *Store) String() string {
	if s == nil {
		return "secrets.Store(nil)"
	}
	return "secrets.Store(app " + s.appID + ", keyring " + s.keyFile + ", " + redacted + ")"
}

// GoString is String.
func (s *Store) GoString() string { return s.String() }

// Format prints String for every verb.
func (s *Store) Format(f fmt.State, _ rune) { writeRedacted(f, s.String()) }

// LogValue renders the store's app and keyring file only.
func (s *Store) LogValue() slog.Value {
	if s == nil {
		return slog.StringValue("secrets.Store(nil)")
	}
	return slog.GroupValue(slog.String("app_id", s.appID), slog.String("key_file", s.keyFile))
}

// UnexportedKeys is the key IDs of the loaded keyring that the export record
// (<key file>.exported, written by secrets export-keyring) does not hold with
// the same material digest (05-health secrets_keyring_not_exported; a name
// coined by F3-03). Comparing the digest, not only the ID, keeps a record
// written for a keyring since lost from vouching for a new k1. It reads
// the record on every call, since export-keyring runs while the server
// serves. An absent record leaves every key unexported; a record that cannot
// be read does too, and its error is returned beside them.
//
// The digests are taken under the read lock: the key material is the
// keyring's own memory, which Close wipes under the write lock, so hashing
// it after the unlock would race with Close and could digest zeroed bytes.
func (s *Store) UnexportedKeys() ([]string, error) {
	keys, err := s.loadedKeyDigests()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.ID
	}
	exported, err := readExportRecord(s.root, exportedRecordName(s.keyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return ids, err
	}
	var out []string
	for _, k := range keys {
		if !exported.covers(k) {
			out = append(out, k.ID)
		}
	}
	return out, nil
}

// loadedKeyDigests is every key of the loaded keyring as the export record
// names it (ID and material digest), in file order, read under s.mu.
func (s *Store) loadedKeyDigests() ([]exportedKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errClosed
	}
	keys := make([]exportedKey, len(s.ring.file.Keys))
	for i := range s.ring.file.Keys {
		keys[i] = exportedKeyOf(&s.ring.file.Keys[i])
	}
	return keys, nil
}

// Close wipes the loaded key material and the operator values. It does not
// close the state directory (the kernel owns it) or the keyspace.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.ring.wipe()
	for _, e := range s.ephemeral {
		clear(e.v.raw())
	}
	clear(s.ephemeral)
	s.mu.Unlock()
	// Unregister with s.mu released: a collection in flight holds the
	// metrics pipeline while its callback waits for s.mu.
	return s.ctr.unregister()
}

// equalValue compares two secret byte strings in constant time.
func equalValue(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
