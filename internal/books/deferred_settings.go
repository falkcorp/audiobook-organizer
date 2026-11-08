// file: internal/books/deferred_settings.go
// version: 1.0.0
// guid: 6c2e9f47-1b8d-4a35-9e60-3d7f2a5c8b14
// last-edited: 2026-11-08

package books

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// deferredSettings is the settings store HEAD's settings load runs over in
// books' Register: reads go to the store, writes are held in order and read
// back by the load itself, and apply commits them in the repairs start hook.
// Books' P6 constructor commits no write (05-storage §5S.4 step 12,
// TestBooksConstructorWritesNothing): the kernel marks the open checkpoint
// records served at the start of Run, before the first module code that can
// write, so a write in Register would precede that mark. HEAD's load writes
// on some boots (the blob-shape migrations, the one-shot settings
// migrations, the corrupt-secret repair; 05-storage §5S.9.3 keeps them in
// books' HEAD config load up to R1). They are idempotent: a boot that dies
// before the start hooks loses them, and the next boot's load stages them
// again from the same stored rows.
type deferredSettings struct {
	store database.SettingsStore

	mu sync.Mutex
	// ops is every write the load made, in order; apply replays them.
	ops []settingWrite
	// overlay is each written key's row as the store would return it after
	// the writes so far; nil for a deleted key.
	overlay map[string]*database.Setting
}

// settingWrite is one held SetSetting or DeleteSetting.
type settingWrite struct {
	key, value, typ string
	secret, delete  bool
}

func newDeferredSettings(store database.SettingsStore) *deferredSettings {
	return &deferredSettings{store: store, overlay: map[string]*database.Setting{}}
}

// GetSetting returns the held row for a written key, else the store's.
func (d *deferredSettings) GetSetting(key string) (*database.Setting, error) {
	d.mu.Lock()
	row, held := d.overlay[key]
	d.mu.Unlock()
	if !held {
		return d.store.GetSetting(key)
	}
	if row == nil {
		return nil, fmt.Errorf("setting not found: %s: %w", key, database.ErrSettingNotFound)
	}
	cp := *row
	return &cp, nil
}

// SetSetting holds the write and stores the row SetSetting would store: a
// secret's value encrypted with the key InitEncryption loaded.
func (d *deferredSettings) SetSetting(key, value, typ string, isSecret bool) error {
	stored := value
	if isSecret && value != "" {
		enc, err := database.EncryptValue(value)
		if err != nil {
			return fmt.Errorf("encryption failed: %w", err)
		}
		stored = enc
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, settingWrite{key: key, value: value, typ: typ, secret: isSecret})
	d.overlay[key] = &database.Setting{Key: key, Value: stored, Type: typ, IsSecret: isSecret}
	return nil
}

// DeleteSetting holds the delete.
func (d *deferredSettings) DeleteSetting(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, settingWrite{key: key, delete: true})
	d.overlay[key] = nil
	return nil
}

// GetAllSettings is the store's rows with the held writes applied, in key
// order as the store returns them. A store error (an unreadable row) is
// returned as the store returned it.
func (d *deferredSettings) GetAllSettings() ([]database.Setting, error) {
	rows, err := d.store.GetAllSettings()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.overlay) == 0 {
		return rows, nil
	}
	out := make([]database.Setting, 0, len(rows)+len(d.overlay))
	for _, r := range rows {
		if _, held := d.overlay[r.Key]; !held {
			out = append(out, r)
		}
	}
	for _, r := range d.overlay {
		if r != nil {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b database.Setting) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// pending reports how many writes are held.
func (d *deferredSettings) pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ops)
}

// apply commits the held writes to the store in order, once; a later call
// commits nothing. The first failure stops it and is returned: the load
// stages the rest again on the next boot.
func (d *deferredSettings) apply() error {
	d.mu.Lock()
	ops := d.ops
	d.ops = nil
	d.mu.Unlock()
	for _, op := range ops {
		var err error
		if op.delete {
			err = d.store.DeleteSetting(op.key)
		} else {
			err = d.store.SetSetting(op.key, op.value, op.typ, op.secret)
		}
		if err != nil {
			return fmt.Errorf("books: commit the settings load's write of %q: %w", op.key, err)
		}
	}
	return nil
}
