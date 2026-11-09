- [ ] **Make `library.CleanAbsolutePath` and `library.Join` sanitizers CodeQL credits, then
      re-check the dismissed path alerts** (from #4496's round-7 SF2; filed there, not built).
      - **Why.** CodeQL's `go/path-injection` and `go/zipslip` do not treat either result as
        sanitized. `CleanAbsolutePath` (`core/library/pathvalidation.go`) rejects a path when
        `cleaned != path`, a comparison the queries do not read, and its doc comment says taint
        tools treat its return as sanitized, which is false. `Join` and `Validate`
        (`core/library/safepath.go`) check `cleaned != cleanRoot && !strings.HasPrefix(...)`:
        the equal-root branch skips the prefix check, so the check does not guard the path they
        return. #4496 worked around both in its own two callers with restated checks:
        `strings.HasPrefix(target, within)` in `internal/backup/backup.go`'s `extractArchive`,
        and `strings.HasPrefix(cleanTarget, VolumeName+Separator)` in
        `internal/server/handlers/system/handler.go`'s `RestoreBackup`.
      - **Fix.** Put a check the queries credit on the path each helper returns. For example,
        `Join` and `Validate` could make one prefix check of `cleaned+sep` against
        `cleanRoot+sep`, and `CleanAbsolutePath` could add a prefix check against
        `VolumeName(cleaned)+sep`. Run CodeQL locally (2.27.4, go-queries 1.6.14, `TaintedPath`
        and `ZipSlip`) on the whole module before and after. Then remove #4496's two
        restatements and the comments that explain them.
      - **Callers.** `CleanAbsolutePath`, 12 production calls:
        - `internal/mediainfo/mediainfo.go`
        - `internal/plugins/maintenance/duration_backfill_unknown_rows.go` (2)
        - `internal/plugins/maintenance/file_provenance_export.go`
        - `internal/plugins/maintenance/folder_books_fixer.go`
        - `internal/server/handlers/audiobooks/handler_files.go` (2)
        - `internal/server/handlers/filesystem.go`
        - `internal/server/handlers/itunes.go` (2)
        - `internal/server/handlers/system/handler.go`
        - `internal/server/itl_paths.go`

        `Join`, 16 production calls:
        - `internal/backup/backup.go`
        - `internal/covers/covers.go`
        - `internal/deluge/import.go` (2)
        - `internal/fileops/safe_operations.go` (3)
        - `internal/fileops/service.go` (4)
        - `internal/reconcile/reconcile.go`
        - `internal/server/covers.go` (2)
        - `internal/server/openlibrary_service.go` (2)

        `Validate`, 1 production call: `internal/backup/backup.go`.
      - **Dismissed alerts to re-check.** 37 of the 96 dismissed `go/path-injection` and
        `go/zipslip` alerts are in files that call these helpers. Only the before-and-after run
        can say which of them flow through a helper. Reopen what the change fixes, and dismiss
        again, with the new reason, only what still fires.
        - The dismissal names `CleanAbsolutePath`: #2151 (`mediainfo.go:111`) and #1105
          (`filesystem.go:295`).
        - SEC-AUDIT-12 triage:
          - `backup.go`: #14, #17, #18, #19, #346, #347.
          - `fileops/safe_operations.go`: #198, #199, #200, #202, #203, #205, #207, #208,
            #209, #210, #349, #350, #542, #543.
          - `server/openlibrary_service.go`: #669.
        - The fileops import-path allow-list guards them:
          - `fileops/service.go`: #620, #622, #623, #663, #664, #665.
          - `audiobooks/handler_files.go`: #1064, #1065.
          - `filesystem.go`: #1094.
        - Accepted risk, the iTunes library path: `itunes.go` #1055, #1057, #1058, #1059, #1060.
      - **Cost.** A change in the core module that changes the flow at every caller above.
        Alerts may close as fixed or stay open and need dismissing again. Run each caller's
        package tests, because `Join`'s equal-root case is behaviour some callers may rely on.
