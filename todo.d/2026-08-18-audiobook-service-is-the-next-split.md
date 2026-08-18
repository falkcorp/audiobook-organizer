- [ ] **Split `AudiobookService`, not just its store interface.** `audiobookStore`
      (`internal/audiobooks/service.go:36`) is one of the four remaining
      `interfacebloat` findings. A compiler probe measured its true requirement at
      ~50 methods (44 direct calls, plus `RecordMetadataChange` and 5 author/series
      alias-and-count methods pulled in by assignability constraints). At <=7
      methods per group that needs 8 groups, which lands exactly on the linter's
      limit of 8 -- so a flat regrouping buys width but no headroom, and a nested
      tier of mid-level composites would score 3 while still carrying all 50
      methods, which is the wide-embed style with better names.
      The honest unit of work is the service itself: the probe bucketed its calls
      as `service_single.go` 23, `service_mutation.go` 20, `service_query.go` 15,
      `service_tags.go` 10, `service_filtering.go` 8, `helpers.go` 5 -- six real
      consumers sharing one `store` field. Split those into services with their own
      narrow stores and `audiobookStore` dissolves rather than being regrouped.
