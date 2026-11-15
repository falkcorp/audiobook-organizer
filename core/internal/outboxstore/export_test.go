// file: core/internal/outboxstore/export_test.go
// version: 1.0.1
// guid: 8c1e7d40-2a95-4b3f-a6d1-5e0f9b7c3a28
// last-edited: 2026-11-15

package outboxstore

// EncodeDeadForTest is the stored form of a dead letter, for a test that plants
// a letter without its counter (a store from before the counters existed).
func EncodeDeadForTest(d Dead) ([]byte, error) { return deadCodec().Encode(d) }

// EncodeDeadCountForTest is the stored form of a counter of n.
func EncodeDeadCountForTest(n int) ([]byte, error) { return deadCountCodec().Encode(DeadCount{N: n}) }

// SetRepairHook installs fn as the seam RepairDeadCounts runs between computing
// its fixes and applying them; nil removes it.
func SetRepairHook(s *Store, fn func()) { s.beforeRepairCommit = fn }

// KeysScanned is how many keys the dead-letter counting reads have visited.
func KeysScanned(s *Store) int64 { return s.scanned.Load() }
