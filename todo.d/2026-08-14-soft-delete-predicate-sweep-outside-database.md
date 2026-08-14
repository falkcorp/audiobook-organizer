- [ ] 🧹 **Sweep the 26 open-coded soft-delete checks outside `internal/database`
      onto `Book.IsSoftDeleted()`.** PR #2392 collapsed the 37 copies of
      `b.MarkedForDeletion != nil && *b.MarkedForDeletion` *inside* the database
      package onto one predicate, but the predicate was unexported, so every other
      package had no choice but to keep restating the rule. `Book.IsSoftDeleted()`
      now exists (added 2026-08-14 alongside the iTunes writeback fix) and the
      existing sites were deliberately left alone — converting 26 call sites would
      have buried the fix that commit was about.

      Standing census, 26 sites across 18 files:

      | count | file |
      |---|---|
      | 3 | `internal/itunes/rebuild.go` |
      | 2 | `internal/undo/engine.go` |
      | 2 | `internal/plugins/maintenance/title_repair.go` |
      | 2 | `internal/plugins/maintenance/regroup_apply.go` |
      | 2 | `internal/plugins/dedup/quarantine_chapter_artifacts.go` |
      | 2 | `internal/itunes/pid_integrity.go` |
      | 2 | `internal/itunes/cross_type.go` |
      | 1 each | `internal/server/handlers/metadata/handler.go`, `internal/scanner/scanner.go`, `internal/plugins/maintenance/repair_junk_titles.go`, `internal/organizer/service.go`, `internal/itunes/relocate.go`, `internal/itunes/pid_repair.go`, `internal/dedup/split_book_detector.go`, `internal/dedup/engine.go`, `internal/dedup/collectors_exact.go`, `internal/dedup/auto_resolve.go`, `internal/audiobooks/service_mutation.go` |

      ⚠️ **One of the 26 is not the predicate and must not be converted.**
      `internal/scanner/scanner.go:2619` reads
      `scanned.MarkedForDeletion == nil && existing.MarkedForDeletion != nil` — a
      nil-vs-set comparison between two rows for merge logic, not a "is this book
      in the trash" question. A regex sweep will match it. Read every site before
      rewriting it; the grep is a work-list, not a patch.

      Also worth a second look while in there: `internal/dedup/auto_resolve.go:358`
      is already a named helper wrapping the same rule
      (`return b != nil && b.MarkedForDeletion != nil && *b.MarkedForDeletion`) —
      that one is a whole duplicate predicate, not just a call site, and deleting
      it in favour of the exported one is the highest-value single edit here.

      Suggested shape: `/parallel-sweep`, one PR per package family (dedup, itunes,
      maintenance, the rest), since ≥3 mechanically-similar refactors is exactly
      what that command is for. Standing check afterwards:

      ```
      grep -rn "MarkedForDeletion != nil" --include='*.go' internal/ cmd/ \
        | grep -v '^internal/database/' | grep -v _test | grep -v mocks
      ```

      should return only `scanner.go`'s merge comparison.

      Why it matters rather than being tidiness: a rule spelled out in 26 places is
      the exact mechanism by which the two `GetAllBooksCore` implementations drifted
      into disagreeing for the entire life of the memdb query layer, and that one
      cost ~35 full-library ops four weeks of running against 3,953 trashed books.
      Related: [[project_memdb_softdeleted_leak]].
