// file: core/internal/configstore/committer.go
// version: 1.0.0
// guid: ae4a8ee2-7f9c-4f18-869f-dc29b76840eb
// last-edited: 2026-11-10

package configstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// settingsUpdate is the audit action of every committed Apply (05-rbac §7.1).
const settingsUpdate = "core.settings.update"

// defaultAttempts is how often Commit tries before a conflict is returned.
const defaultAttempts = 5

// SecretStager stages one secret write into a batch: secrets.(*Store).Stage
// once F3-03 builds the store (05-config §2.3).
type SecretStager interface {
	Stage(ctx context.Context, b *storage.Batch, w secrets.Write) error
}

// KeyLookup names the section of a key for the audit record: the
// config.Store.
type KeyLookup interface {
	Lookup(p config.Path) (config.KeyState, bool)
}

// CommitterOptions configures a Committer.
type CommitterOptions struct {
	Store   *Store       // the native store; its keyspace is each batch's origin
	Audit   *audit.Store // records core.settings.update in the same batch
	Keys    KeyLookup    // the config store
	Secrets SecretStager // nil until F3-03: a Commit with secret writes is refused
	// Attempts bounds storage.RetryOnConflict; 0 means 5.
	Attempts int
}

// Committer is the kernel's config.Committer.
type Committer struct {
	o CommitterOptions
}

// NewCommitter checks o and returns the committer.
func NewCommitter(o CommitterOptions) (*Committer, error) {
	switch {
	case o.Store == nil:
		return nil, errors.New("configstore: the committer needs the native store")
	case o.Audit == nil:
		return nil, errors.New("configstore: the committer needs the audit store (core.settings.update is recorded in every commit)")
	case o.Keys == nil:
		return nil, errors.New("configstore: the committer needs the config store to name sections")
	case o.Attempts < 0:
		return nil, fmt.Errorf("configstore: Attempts %d", o.Attempts)
	}
	if o.Attempts == 0 {
		o.Attempts = defaultAttempts
	}
	return &Committer{o: o}, nil
}

// Commit stages c into ONE batch — the rows of c.Set and c.Unset, each
// secret write of c.Secrets and the core.settings.update record — and
// commits it with Sync durability, retrying a conflict on a fresh batch.
// The record names the changed sections and keys ("changed": audit redacts a detail whose name contains "key"), never a value.
func (c *Committer) Commit(ctx context.Context, cm config.Commit) error {
	if len(cm.Secrets) > 0 && c.o.Secrets == nil {
		return errors.New("configstore: a secret write needs the secrets store, which is not open")
	}
	ev := c.event(cm)
	return storage.RetryOnConflict(ctx, c.o.Attempts, func(ctx context.Context) (err error) {
		b := c.o.Store.Keyspace().NewBatch()
		defer func() {
			if cerr := b.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("configstore: discard the batch: %w", cerr)
			}
		}()
		if err := c.o.Store.Stage(ctx, b, cm.Set, cm.Unset); err != nil {
			return err
		}
		for _, w := range cm.Secrets {
			if err := c.o.Secrets.Stage(ctx, b, w); err != nil {
				return fmt.Errorf("configstore: stage secret %s: %w", w.Name, err)
			}
		}
		if err := c.o.Audit.RecordIn(ctx, b, ev); err != nil {
			return fmt.Errorf("configstore: stage the audit record: %w", err)
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// event is the audit record of cm: the sections and the keys it changes
// (05-rbac §7.1). The actor is the writer of the commit's rows or secrets
// (Override.By, secrets.Write.By); with none, audit fills it from ctx.
func (c *Committer) event(cm config.Commit) audit.Event {
	keySet := map[config.Path]bool{}
	by := ""
	for _, ov := range cm.Set {
		keySet[ov.Path] = true
		if by == "" {
			by = ov.By
		}
	}
	for _, p := range cm.Unset {
		keySet[p] = true
	}
	for _, w := range cm.Secrets {
		keySet[config.Path(w.Name)] = true
		if by == "" {
			by = w.By
		}
	}
	for _, p := range cm.Boot {
		keySet[p] = true
	}
	keys := slices.Sorted(maps.Keys(keySet))
	secSet := map[config.Path]bool{}
	for _, p := range keys {
		if ks, ok := c.o.Keys.Lookup(p); ok {
			secSet[ks.Section] = true
		}
	}
	names := make([]string, 0, len(keys))
	for _, p := range keys {
		names = append(names, string(p))
	}
	sections := make([]string, 0, len(secSet))
	for _, p := range slices.Sorted(maps.Keys(secSet)) {
		sections = append(sections, string(p))
	}
	return audit.Event{
		Action:  settingsUpdate,
		Outcome: audit.Success,
		Actor:   audit.Actor{UserID: by},
		Details: map[string]any{"sections": sections, "changed": names, "generation": cm.Gen},
	}
}
