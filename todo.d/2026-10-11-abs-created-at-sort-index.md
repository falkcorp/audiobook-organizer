- [ ] **ABS-CREATED-AT-SORT-INDEX** Owner decision: enable the memdb `created_at` sort
      index (`enabled_sort_indexes`; the default enabled set is `{year}`). Today the ABS
      Recently Added shelf and the Library tab's `sort=addedAt&desc=1` both materialise the
      whole visible set and sort it before cutting the page: measured on a 68k-row / ~17k
      visible library, one call is ~30-39 ms and allocates ~78 MB per home-page load. With
      the index, a reverse walk to the 40 newest visible rows measured ~7 us and ~10 KB.
      Cost: roughly 19 MB of memory per indexed key and slower inserts. The change is
      configuration only (no code), but it trades memory and write speed for read speed, so
      it is the owner's call. A store-side alternative that needs no index: collect `*Book`
      pointers (memdb objects are read-only) or keep a bounded top-(offset+limit) heap
      instead of copying every match (976 B each), which would cut the unindexed sort from
      ~78 MB to ~130 KB; the author and series sorts mutate rows in `hydrateSortNames`, so
      they would need to stay on the copying path.
