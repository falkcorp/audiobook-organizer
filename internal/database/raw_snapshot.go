// file: internal/database/raw_snapshot.go
// version: 1.0.0
// guid: 5e7c1a94-3b28-4d60-8f17-a2d94c60b3e1
// last-edited: 2026-10-21

package database

import (
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// RawSnapshot is a point-in-time, read-only view of the raw keyspace: every
// GetRaw and every page of ScanPrefixPage answers as of the instant
// NewRawSnapshot returned, however many writes commit meanwhile. A reader that
// needs several scans to agree with each other (the decisions veto snapshot
// reads verdicts, pair edges and set memberships) must read them through one.
//
// It holds an engine lease until Close, so a caller closes it promptly.
type RawSnapshot interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) (pairs []KVPair, next string, err error)
	Close() error
}

// RawSnapshotter is implemented by stores that can open a RawSnapshot.
// *PebbleStore does; it is deliberately not part of RawKVStore, so adding it
// did not widen the mocked interface.
type RawSnapshotter interface {
	NewRawSnapshot() (RawSnapshot, error)
}

var _ RawSnapshotter = (*PebbleStore)(nil)

// NewRawSnapshot opens a RawSnapshot over the main store.
func (p *PebbleStore) NewRawSnapshot() (RawSnapshot, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("NewRawSnapshot: store is not open")
	}
	return &rawSnapshot{snap: p.db.NewSnapshot()}, nil
}

type rawSnapshot struct{ snap *GatedSnapshot }

func (r *rawSnapshot) GetRaw(key string) ([]byte, error) {
	val, closer, err := r.snap.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	out := make([]byte, len(val))
	copy(out, val)
	return out, nil
}

func (r *rawSnapshot) ScanPrefixPage(prefix, after string, limit int) ([]KVPair, string, error) {
	return scanPrefixPageWith(r.snap.NewIter, prefix, after, limit)
}

func (r *rawSnapshot) Close() error { return r.snap.Close() }
