- [ ] **TODO-PERUSER-BOOLEAN** Support `OR` and grouped `NOT` over per-user terms
      (`read_status`, `progress_pct`, `last_played`) instead of refusing them. Today
      `search.CheckPerUserBoolean` refuses a per-user term under an `OR` with a sibling or under
      a `NOT` over a group (HTTP 400 on dynamic collection and smart playlist create/update, and
      `members_unavailable` for a stored row), because the translator flattens every per-user term
      into one AND list applied after the index search. The real fix is for the per-user pass to
      evaluate the boolean tree itself: have the translator return the tree with the per-user
      leaves left as callbacks (or evaluate each leaf to a per-viewer id set and combine the sets
      with the index's id set per node), then delete `CheckPerUserBoolean`. The library list search
      (`internal/audiobooks/service_query.go`, `service_search_cache.go`) still takes the flat AND
      reading for these shapes and needs the same change. Left from the PR 4164 re-review (B-1).
