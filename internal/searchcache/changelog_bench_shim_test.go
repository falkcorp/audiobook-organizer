// file: internal/searchcache/changelog_bench_shim_test.go
// version: 1.1.0
// guid: 8e2f4c71-3a9b-4d05-b6e1-7c0d9a2f5e38
// last-edited: 2026-09-26

package searchcache

// changedSinceCapped is ChangedSince as cache.go calls it, with the default
// patch cap.
func changedSinceCapped(cl *ChangeLog, since uint64) ([]string, uint64, bool) {
	return cl.ChangedSince(since, DefaultMaxPatchChanged+1)
}
