- [ ] **LOG-SANITISE** Clear CodeQL's 46 `go/log-injection` ("log entries created from
      user input") alerts surfaced on #5061 (F4-08 re-cut; CodeQL could not separate new
      from pre-existing alerts on that diff). Sanitise or structure the logged values at
      each site in its own small PR, not in the identity flip. Sites: core/kernel/reset.go:138;
      tools/cmd/reconcile-book-counts/main.go:115; internal/versions/ingest.go:519;
      internal/server/version_lifecycle.go:328; internal/server/library_list_warmer.go:731,975;
      internal/server/handlers/scheduler_admin.go:471; internal/server/handlers/diagnostics.go:782;
      internal/server/handlers/dedup/handler.go:1397; internal/server/embedding_backfill.go:63;
      internal/repairs/rcreporter.go:97; internal/reconcile/reconcile.go:1354;
      internal/plugins/metafetch/env.go:217; internal/plugins/maintenance/repoint_missing_to_folder_audio.go:419;
      internal/plugins/itunes/adapter.go:38,42,46,50; internal/plugins/deluge/link_backfill.go:1796,1799;
      internal/plugins/deluge/env.go:242; internal/plugins/deluge/centralization.go:299;
      internal/plugins/dedup/env.go:243; internal/plugins/acoustid/env.go:358;
      internal/organizer/service.go:2495; internal/metafetch/service_apply.go:695;
      internal/merge/sync_follow.go:546; internal/maintenance/jobs/backfill_itunes_positions.go:327;
      internal/maintenance/jobs/abs_backfill_reporter.go:69; internal/itunes/service/importer.go:794,1535,1537,1539,1541;
      internal/dedup/engine.go:1622,4429,4433; internal/books/store/pebble_store_scancache.go:478;
      internal/books/store/pebble_store_authors.go:1038; internal/books/store/memdb_sync.go:211,228,246;
      internal/audiobooks/service_query.go:582,1271; internal/audiobooks/service_filtering.go:1087.
      Line numbers are at #5061 head 796e90873; re-locate with CodeQL before editing.
