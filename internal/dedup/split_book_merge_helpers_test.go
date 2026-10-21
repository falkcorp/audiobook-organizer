// file: internal/dedup/split_book_merge_helpers_test.go
// version: 1.0.0
// guid: 2c6e9a05-f1b8-4d37-a7e3-5b0d8c4f1a96
// last-edited: 2026-10-21

package dedup

// mergeSplitBookCluster is the plain merge, with the zero SplitMergeOptions and
// no recheck. It was the exported MergeSplitBookCluster until every production
// caller moved to MergeSplitBookClusterRechecked (stored candidates) or
// MergeSplitBookClusterWithOptions (the Repairs fixer and the chapter-group
// job, which gate their own members); only these tests still wanted the
// shorthand.
func mergeSplitBookCluster(store Store, keepID string, srcIDs []string, suggestedTitle string) (*SplitBookMergeResult, error) {
	return MergeSplitBookClusterWithOptions(store, keepID, srcIDs, suggestedTitle, SplitMergeOptions{})
}
