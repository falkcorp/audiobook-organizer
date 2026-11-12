// file: core/backup/inspector.go
// version: 1.0.1
// guid: 94d2b6e1-0a7c-4f35-8e19-6b3a1c5d7f08
// last-edited: 2026-11-12

package backup

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/storage"
)

// SecretIDs is the part of the keyring a StorageInspector asks for the key IDs
// a store's secret records reference (secrets.Store satisfies it).
type SecretIDs interface {
	KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error)
}

// StorageInspector is the Inspector over storage: it opens a store directory
// read-only with the build's guard, upgrade floors and claims (storage.Open,
// which refuses what the build cannot serve, a *storage.ErrUpgradeFloor
// included, and writes nothing), and, when Admin is set, counts every owner's
// keys and bytes through Admin.OpenReadOnly and reads the key IDs of the
// store's secret records.
type StorageInspector struct {
	// Base is the open template: Owners, Build and the rest of what the kernel
	// passes storage.Open. Path, ReadOnly, MustExist and StateFiles are set per
	// call.
	Base storage.OpenOptions
	// Admin opens the same directory for the key counts; nil leaves Keys, Bytes
	// and SecretKeyIDs empty.
	Admin *storage.Admin
	// Secrets and SecretsOwner (default "core/secrets") give the secret
	// records' key IDs; nil leaves SecretKeyIDs empty.
	Secrets      SecretIDs
	SecretsOwner storage.Owner
}

var _ Inspector = (*StorageInspector)(nil)

// Inspect implements Inspector.
func (si *StorageInspector) Inspect(ctx context.Context, dir string) (StoreInfo, error) {
	opts := si.Base
	opts.Path, opts.ReadOnly, opts.MustExist, opts.StateFiles = dir, true, true, nil
	o, err := storage.Open(ctx, opts)
	if err != nil {
		return StoreInfo{}, err
	}
	info := StoreInfo{
		InstallID: o.InstallID(), PebbleFormat: o.PebbleFormat(),
		Levels: map[string]int{}, Versions: map[string]int{},
	}
	for ow, l := range o.Levels() {
		info.Levels[string(ow)] = l
	}
	for ow, v := range o.Versions() {
		info.Versions[string(ow)] = v
	}
	if err := o.Close(); err != nil {
		return StoreInfo{}, err
	}
	if si.Admin == nil {
		return info, nil
	}
	ro, err := si.Admin.OpenReadOnly(ctx, dir)
	if err != nil {
		return StoreInfo{}, err
	}
	defer ro.Close()
	if err := si.count(ctx, ro, &info); err != nil {
		return StoreInfo{}, err
	}
	if si.Secrets != nil {
		so := si.SecretsOwner
		if so == "" {
			so = "core/secrets"
		}
		// A build that does not declare the owner has no secret records to check;
		// one that does must be able to read them: an error here is a refusal, never
		// "no keys needed".
		declared := false
		for _, os := range si.Base.Owners {
			declared = declared || (os.Owner == so && os.Enabled)
		}
		if declared {
			ks, err := ro.Keyspace(so)
			if err != nil {
				return StoreInfo{}, fmt.Errorf("backup: the store's secret records cannot be read: %w", err)
			}
			if info.SecretKeyIDs, err = si.Secrets.KeyIDsReferenced(ctx, ks); err != nil {
				return StoreInfo{}, fmt.Errorf("backup: key IDs the store's secrets need: %w", err)
			}
			sort.Strings(info.SecretKeyIDs)
		}
	}
	return info, nil
}

// count scans every owner's keyspace, one owner per task over a bounded pool
// (the books keyspace dwarfs the rest, so the pool bounds work without
// splitting an owner).
func (si *StorageInspector) count(ctx context.Context, ro *storage.ReadOnlyDB, info *StoreInfo) error {
	info.Keys, info.Bytes = map[string]int64{}, map[string]uint64{}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, min(4, runtime.NumCPU())))
	for _, os := range si.Base.Owners {
		if !os.Spec.Keyspace {
			continue
		}
		g.Go(func() error {
			ks, err := ro.Keyspace(os.Owner)
			if err != nil {
				return nil // a disabled owner has no handle
			}
			var n int64
			var b uint64
			for kv, err := range ks.Scan(gctx, storage.ScanOptions{}) {
				if err != nil {
					return err
				}
				n++
				b += uint64(len(kv.Key) + len(kv.Value))
			}
			mu.Lock()
			info.Keys[string(os.Owner)], info.Bytes[string(os.Owner)] = n, b
			mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}
