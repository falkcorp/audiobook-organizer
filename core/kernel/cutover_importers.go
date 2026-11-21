// file: core/kernel/cutover_importers.go
// version: 1.1.0
// guid: 6f2b8d41-a903-4c7e-b5d8-1e9a07c3f562
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
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
}

// values are the importers a Cutover func is handed.
func (l *lateImporters) values() module.Importers {
	return module.Importers{
		Config: lateConfig{l}, Secrets: lateSecrets{l},
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
