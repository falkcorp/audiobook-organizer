// file: core/kernel/cutover_importers.go
// version: 1.1.0
// guid: 6f2b8d41-a903-4c7e-b5d8-1e9a07c3f562
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// lateImporters are the importers Manifest.Cutover receives (06 §6.4.2).
// The migration list is built at P2, before the stores the importers write
// through exist (config's native store after P0/P4a, the secrets store at
// the start of P4b), so each importer a Cutover func holds is a forwarding
// value: it refuses with module.ErrImporterNotBound until the kernel binds it,
// and a step that runs before then fails instead of writing anywhere. The
// offline opens share storageOptions and so build the same values; they never
// bind them, which is the point: an offline open can plan the list but no
// step it could run writes through an importer.
type lateImporters struct {
	mu  sync.RWMutex
	cfg config.Importer
	sec secrets.Importer
	// legacy and allowRegen back Importers.Legacy and Importers.AllowKeyRegen
	// (F3-07 d): bound after Prepare, at the start of P4b, with the rest.
	legacy     func(storage.Owner) storage.Reader
	allowRegen bool
	// lookup backs Importers.OperatorSet: the config store's Lookup, bound
	// with the importers (bindLookup); nil = unbound.
	lookup func(config.Path) (config.KeyState, bool)
	// usr, roles and aud back Importers.Auth, Roles and Audit (F4-08 B3): the
	// identity boot's own store instances, bound by bindIdentity at the start of
	// P4b; nil = unbound (an app that did not declare the identity boot never
	// binds them, and a step that reaches one fails).
	usr   auth.Importer
	roles rbac.Importer
	aud   audit.Logger
	// taskImp, taskRuns and logImp back Importers.Tasks and Log (F5-18): the
	// native task store's importer and run reader over c/tasks/, and the journal's
	// importer over c/log/, bound by bindNative at the start of P4b; nil = unbound.
	taskImp  tasks.Importer
	taskRuns runGetter
	logImp   log.Importer
}

// values are the importers a Cutover func is handed.
func (l *lateImporters) values() module.Importers {
	return module.Importers{
		Config: lateConfig{l}, Secrets: lateSecrets{l},
		Auth: lateAuth{l}, Roles: lateRoles{l}, Audit: lateAudit{l},
		Tasks: lateTasks{l}, Log: lateLog{l},
		Legacy: l.legacyReader, AllowKeyRegen: l.allowKeyRegen,
		OperatorSet: l.operatorSet,
	}
}

// bindLookup points Importers.OperatorSet at the config store's Lookup.
func (l *lateImporters) bindLookup(f func(config.Path) (config.KeyState, bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lookup = f
}

// operatorSet is Importers.OperatorSet: whether an operator layer locks p,
// and which one. False while unbound or for an unregistered path.
func (l *lateImporters) operatorSet(p config.Path) (string, bool) {
	l.mu.RLock()
	f := l.lookup
	l.mu.RUnlock()
	if f == nil {
		return "", false
	}
	ks, ok := f(p)
	if !ok || ks.LockedBy == "" {
		return "", false
	}
	return ks.LockedBy, true
}

// bindLegacy points Importers.Legacy at db's legacy ranges and fixes
// Importers.AllowKeyRegen at allowRegen. The reader is nil for an owner with no
// claims (storage.DB.Legacy's ok=false), never a typed zero value a step could
// mistake for an open range.
func (l *lateImporters) bindLegacy(db *storage.DB, allowRegen bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.legacy = func(o storage.Owner) storage.Reader {
		lr, ok := db.Legacy(o)
		if !ok {
			return nil
		}
		return lr
	}
	l.allowRegen = allowRegen
}

func (l *lateImporters) legacyReader(o storage.Owner) storage.Reader {
	l.mu.RLock()
	f := l.legacy
	l.mu.RUnlock()
	if f == nil {
		return nil
	}
	return f(o)
}

func (l *lateImporters) allowKeyRegen() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.allowRegen
}

// bind points the importers at their stores; a nil argument leaves that
// importer unbound. Binding twice replaces the first binding.
func (l *lateImporters) bind(c config.Importer, s secrets.Importer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg, l.sec = c, s
}

func (l *lateImporters) config() (config.Importer, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.cfg == nil {
		return nil, module.ErrImporterNotBound
	}
	return l.cfg, nil
}

func (l *lateImporters) secrets() (secrets.Importer, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.sec == nil {
		return nil, module.ErrImporterNotBound
	}
	return l.sec, nil
}

type lateConfig struct{ l *lateImporters }

func (c lateConfig) Stage(b *storage.Batch, p config.Path, v json.RawMessage, by string) error {
	imp, err := c.l.config()
	if err != nil {
		return err
	}
	return imp.Stage(b, p, v, by)
}

func (c lateConfig) Default(p config.Path) (json.RawMessage, error) {
	imp, err := c.l.config()
	if err != nil {
		return nil, err
	}
	return imp.Default(p)
}

type lateSecrets struct{ l *lateImporters }

func (s lateSecrets) ImportSecret(ctx context.Context, b *storage.Batch, w secrets.Write) error {
	imp, err := s.l.secrets()
	if err != nil {
		return err
	}
	return imp.ImportSecret(ctx, b, w)
}

func (s lateSecrets) ImportUnreadable(ctx context.Context, b *storage.Batch, n secrets.Name, reason, by string) error {
	imp, err := s.l.secrets()
	if err != nil {
		return err
	}
	return imp.ImportUnreadable(ctx, b, n, reason, by)
}

// bindIdentity points Importers.Auth, Roles and Audit at the identity boot's
// stores (identity_boot.go buildIdentityStores): the same instances the auth
// and rbac services are built over, so a role imported at P4b is in the role
// cache Freeze loads. A nil argument leaves that importer unbound.
func (l *lateImporters) bindIdentity(a auth.Importer, r rbac.Importer, au audit.Logger) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usr, l.roles, l.aud = a, r, au
}

func (l *lateImporters) identity() (auth.Importer, rbac.Importer, audit.Logger) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.usr, l.roles, l.aud
}

// lateAuth, lateRoles and lateAudit forward to the identity boot's stores and
// refuse with module.ErrImporterNotBound until bindIdentity ran.
type lateAuth struct{ l *lateImporters }

func (a lateAuth) get() (auth.Importer, error) {
	if u, _, _ := a.l.identity(); u != nil {
		return u, nil
	}
	return nil, module.ErrImporterNotBound
}

func (a lateAuth) ImportUser(b *storage.Batch, u *auth.User, passwordHash string) error {
	imp, err := a.get()
	if err != nil {
		return err
	}
	return imp.ImportUser(b, u, passwordHash)
}

func (a lateAuth) ImportAPIKey(b *storage.Batch, k *auth.APIKey, tokenSHA256 [32]byte) error {
	imp, err := a.get()
	if err != nil {
		return err
	}
	return imp.ImportAPIKey(b, k, tokenSHA256)
}

func (a lateAuth) ImportIdentity(b *storage.Batch, id *auth.ExternalIdentity) error {
	imp, err := a.get()
	if err != nil {
		return err
	}
	return imp.ImportIdentity(b, id)
}

func (a lateAuth) ImportInviteAllow(b *storage.Batch, email string) error {
	imp, err := a.get()
	if err != nil {
		return err
	}
	return imp.ImportInviteAllow(b, email)
}

// GetUser reads a user the identity store holds, committed by an earlier chunk
// of the identity step (books' M3 reads a key's owner back through it:
// m3.NativeReader). It is the one read on Importers.Auth: the bound store is
// the auth service's own *authstore.Store, which has it, and the importer
// interface does not, so a caller asks for it with a type assertion on
// Importers.Auth. It refuses with module.ErrImporterNotBound until
// bindIdentity ran, and with errNoNativeRead when the bound importer cannot read.
func (a lateAuth) GetUser(ctx context.Context, id auth.UserID) (*auth.User, error) {
	imp, err := a.get()
	if err != nil {
		return nil, err
	}
	r, ok := imp.(interface {
		GetUser(context.Context, auth.UserID) (*auth.User, error)
	})
	if !ok {
		return nil, errNoNativeRead
	}
	return r.GetUser(ctx, id)
}

// errNoNativeRead is the answer of GetUser and GetRole when the importer bound
// to them cannot read back: no caller could tell an absent record from a store
// that cannot be asked, so it is its own error and never auth.ErrNotFound.
var errNoNativeRead = errors.New("kernel: the identity importer bound at this boot cannot read back what it stored")

type lateRoles struct{ l *lateImporters }

// GetRole reads a role the role store holds, committed by an earlier chunk
// (m3.NativeReader, as lateAuth.GetUser). It refuses like GetUser.
func (r lateRoles) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	_, imp, _ := r.l.identity()
	if imp == nil {
		return nil, module.ErrImporterNotBound
	}
	rd, ok := imp.(interface {
		GetRole(context.Context, rbac.RoleName) (*rbac.Role, error)
	})
	if !ok {
		return nil, errNoNativeRead
	}
	return rd.GetRole(ctx, name)
}

func (r lateRoles) ImportRole(b *storage.Batch, role *rbac.Role) error {
	_, imp, _ := r.l.identity()
	if imp == nil {
		return module.ErrImporterNotBound
	}
	return imp.ImportRole(b, role)
}

type lateAudit struct{ l *lateImporters }

func (a lateAudit) get() (audit.Logger, error) {
	if _, _, au := a.l.identity(); au != nil {
		return au, nil
	}
	return nil, module.ErrImporterNotBound
}

func (a lateAudit) Record(ctx context.Context, e audit.Event) error {
	lg, err := a.get()
	if err != nil {
		return err
	}
	return lg.Record(ctx, e)
}

func (a lateAudit) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	lg, err := a.get()
	if err != nil {
		return err
	}
	return lg.RecordIn(ctx, b, e)
}

// Emit drops the event while unbound: it is the asynchronous, best-effort
// path (audit.Logger.Emit never fails), and nothing before P4b has a store to
// queue it for.
func (a lateAudit) Emit(ctx context.Context, e audit.Event) {
	if lg, err := a.get(); err == nil {
		lg.Emit(ctx, e)
	}
}

// runGetter is the read of one stored run: tasks.RunStore.Get.
type runGetter interface {
	Get(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error)
}

// bindNative points Importers.Tasks at the native task store's importer, with
// the run reader the importer's Get answers from, and Importers.Log at the
// journal's importer (F5-18; 05-tasks 5T.8.3, 05-logging §5). A nil argument
// leaves that importer unbound. Binding twice replaces the first binding.
func (l *lateImporters) bindNative(ti tasks.Importer, runs runGetter, li log.Importer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.taskImp, l.taskRuns, l.logImp = ti, runs, li
}

func (l *lateImporters) native() (tasks.Importer, runGetter, log.Importer) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.taskImp, l.taskRuns, l.logImp
}

// lateTasks forwards to the native task importer and refuses with
// module.ErrImporterNotBound until bindNative ran.
type lateTasks struct{ l *lateImporters }

func (t lateTasks) get() (tasks.Importer, error) {
	if imp, _, _ := t.l.native(); imp != nil {
		return imp, nil
	}
	return nil, module.ErrImporterNotBound
}

func (t lateTasks) Run(ctx context.Context, b *storage.Batch, r tasks.RunRecord) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.Run(ctx, b, r)
}

func (t lateTasks) RunData(ctx context.Context, b *storage.Batch, id tasks.RunID, d tasks.RunData) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.RunData(ctx, b, id, d)
}

func (t lateTasks) Clock(ctx context.Context, b *storage.Batch, k tasks.Kind, c tasks.Clock) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.Clock(ctx, b, k, c)
}

func (t lateTasks) Pause(ctx context.Context, b *storage.Batch, p tasks.Pause) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.Pause(ctx, b, p)
}

func (t lateTasks) StandDown(ctx context.Context, b *storage.Batch, s tasks.StandDown) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.StandDown(ctx, b, s)
}

func (t lateTasks) Debounce(ctx context.Context, b *storage.Batch, d tasks.DebounceBucket) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.Debounce(ctx, b, d)
}

func (t lateTasks) Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k tasks.Kind, at time.Time) error {
	imp, err := t.get()
	if err != nil {
		return err
	}
	return imp.Stamp(ctx, b, subject, k, at)
}

func (t lateTasks) RenameKind(ctx context.Context, b *storage.Batch, from, to tasks.Kind, cursor string, limit int) (string, error) {
	imp, err := t.get()
	if err != nil {
		return "", err
	}
	return imp.RenameKind(ctx, b, from, to, cursor, limit)
}

// Get reads a run the native store holds, committed by an earlier chunk of the
// tasks step (books' activity rewrite asks for a repairs row's run through a type
// assertion on Importers.Tasks: m4.RunReader). It refuses with
// module.ErrImporterNotBound until bindNative ran.
func (t lateTasks) Get(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error) {
	_, runs, _ := t.l.native()
	if runs == nil {
		return tasks.RunRecord{}, module.ErrImporterNotBound
	}
	return runs.Get(ctx, id)
}

// lateLog forwards to the journal importer and refuses with
// module.ErrImporterNotBound until bindNative ran.
type lateLog struct{ l *lateImporters }

func (g lateLog) Import(ctx context.Context, recs []log.ImportedRecord) error {
	_, _, imp := g.l.native()
	if imp == nil {
		return module.ErrImporterNotBound
	}
	return imp.Import(ctx, recs)
}
