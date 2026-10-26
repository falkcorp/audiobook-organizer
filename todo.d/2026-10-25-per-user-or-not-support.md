- [ ] **TODO-PERUSER-BOOLEAN** Support `OR` and grouped `NOT` over per-user terms
      (`read_status`, `progress_pct`, `last_played`) instead of refusing them. Today
      `search.CheckPerUserBoolean` refuses a per-user term under an `OR` with a sibling or under
      a `NOT` over a group (HTTP 400 on dynamic collection and smart playlist create/update, and
      `members_unavailable` for a stored row), because the translator flattens every per-user term
      into one AND list applied after the index search. The real fix is for the per-user pass to
      evaluate the boolean tree itself: have the translator return the tree with the per-user
      leaves left as callbacks (or evaluate each leaf to a per-viewer id set and combine the sets
      with the index's id set per node), then delete `CheckPerUserBoolean`. The library list search
      (`internal/audiobooks/service_query.go`, `service_search_cache.go`) no longer answers these
      shapes as an AND: `search=` refuses them with a 400 (`audiobooks.SearchPerUserProblem` in the
      list handler, and `search.CheckPerUserTerms` in `searchWithBleve` and in both substring
      fallbacks for every other caller), and the web search box, whose `field:value` terms travel
      as `filters=` JSON that cannot hold an OR or a group, sends such a query whole as `search=`
      (`serverQuery` in `web/src/utils/searchParser.ts`) so the server can refuse it. So the list
      needs the same change: once the tree is evaluated, the box can send the filters again, and
      the shape refusals there go with `CheckPerUserBoolean`. The value checks
      (`search.CheckPerUserValues`, applied to `search=`, to `filters=` through
      `search.PerUserFilterForTerm`, and to collections and playlists: statuses, numbers, dates,
      no patterns, lowercase names, no empty list) stay. Left from the PR 4164 re-review (B-1, S-D).
