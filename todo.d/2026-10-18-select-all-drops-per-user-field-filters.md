- [ ] **SELECT-ALL-PER-USER-FILTERS** The background select-all resolver in
      `internal/server/metadata_ops.go` (the `for _, ff := range f.FieldFilters`
      loop near line 572, doc comment at line 563) silently drops every per-user
      `field_filters` entry (`read_status`, `progress_pct`, `last_played`)
      because a background op has no user context. A select-all on a view that is
      filtered by `read_status:unread` therefore resolves to the whole filtered
      view minus that filter, and the op runs against more books than the
      operator saw on screen. Found while reviewing 07-F5a (#4071); that PR left
      the code alone because it is a separate code path. Fix properly: carry the
      requesting user's id in the op params and evaluate the per-user filters
      against that user's rows, or refuse the select-all with a clear error when
      a per-user filter is present, instead of widening it silently.
