// file: internal/server/handlers/abs/export_cache_test.go
// version: 1.0.0
// guid: 3a9d6e12-8f4b-4c71-b0e5-6f2a1d9c8b47
// last-edited: 2026-09-13

package abs

// WaitCacheRefreshes blocks until every background cache refresh on h has
// finished, so the abs_test package can observe a refresh's result and never
// leaves one running past its fixture.
func WaitCacheRefreshes(h *Handler) {
	h.contributorsRefresh.wait()
	h.filterDataRefresh.wait()
	h.seriesBooksRefresh.wait()
}
