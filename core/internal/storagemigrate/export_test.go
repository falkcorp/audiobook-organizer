// file: core/internal/storagemigrate/export_test.go
// version: 1.0.0
// guid: 30ae4396-483e-4f17-8a56-f86c654a2cd3
// last-edited: 2026-11-10

package storagemigrate

import (
	"context"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// HeldPrefix is the prefix, relative to c/storage/, of migration id's held
// item records: one per StepWriter.Hold, listed, never dropped (tests only).
func HeldPrefix(id string) []byte { return sub(id, keys.Str("held")) }

// PlanOf reads migration id's persisted shard plan: its shard count and
// boundaries (tests only).
func PlanOf(ctx context.Context, sks storage.Keyspace, id string) (shards int, bounds [][]byte, found bool, err error) {
	p, found, err := read(ctx, sks, planKey(id), planCodec())
	return p.Shards, p.Boundaries, found, err
}

// CursorOf reads shard n's committed cursor (tests only).
func CursorOf(ctx context.Context, sks storage.Keyspace, id string, n int) ([]byte, bool, error) {
	c, found, err := read(ctx, sks, shardKey(id, n), cursorCodec())
	return c.Last, found, err
}

// ReportOf reads Verify's last report record: its state and error text
// (tests only).
func ReportOf(ctx context.Context, sks storage.Keyspace, id string) (state, errText string, failures []string, found bool, err error) {
	r, found, err := read(ctx, sks, reportKey(id), reportCodec())
	return r.State, r.Error, r.Report.Failures, found, err
}
