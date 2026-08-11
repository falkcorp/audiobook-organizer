- [ ] **ORGANIZE-4TH-COPY** `internal/server/handlers/filesystem.go:286` is a
      FOURTH copy of the single-file/multi-file organize routing bug, and it is
      the worst-behaved of the four.

      **Not fixed here on purpose.** That file belongs to **Wave 12** of the
      silent-failure plan, and the plan's rule is that every wave's file set is
      disjoint from every other's. Wave 3 leaving it alone is the rule working,
      not an oversight — but the audit characterises that line as bucket (d)
      "per-path DB resolution during a filesystem browse", which undersells it.
      Re-rank it 🔴 and fix it in Wave 12, or pull it forward on its own.

      **The defect.** The auto-organize block after a filesystem browse calls
      `org.OrganizeBook(dbBook)` — the SINGLE-FILE path — for every book. Any
      book whose `file_path` is a directory fails with
      `file_path %s is a directory but single-file organize was requested`.
      Multi-file books are most of the library.

      **Why it is worse than the other three:** the error is discarded by a
      bare `continue` with **no log at all**, so unlike `server.go` (which at
      least logged a warning) this copy fails completely silently. It also
      collapses `if err != nil || dbBook == nil` into one branch, hiding a DB
      lookup error and a missing row as the same non-event.

      **Fix:** `organizeService.OrganizeOneBook(org, dbBook, log)` plus
      organized/failed/notInDB/lookupErrors counters, exactly as
      `server.go`'s `AutoOrganizeFn` (#2303) and
      `folder_autoscan_op.go` (this wave) now do.

      ---

      **The other five call sites of `Organizer.OrganizeBook` are CORRECT.**
      Recorded so nobody re-checks them:

      | site | why it is fine |
      |---|---|
      | `organizer/service.go:1000` | this IS `OrganizeOneBook`'s single-file branch |
      | `itunes/service/importer.go:1549` | guarded by `if len(files) > 1 → organizeMultiFileBook` |
      | `server/batch_save_op.go:125` | guarded by an `isDir` stat → `OrganizeDirectoryBook` |
      | `server/handlers/organize.go:253` | full three-way branch (alreadyInRoot / isDir / single) |
      | `metafetch/service_apply.go:345` | the `else` of an explicit multi-file branch |

      **The process lesson, which is the reason this entry is this long.**
      PR #2303 fixed this defect in `server.go`, hoisted the three-way decision
      into `Service.OrganizeOneBook`, and asserted in its own PR body that "a
      third caller cannot reintroduce the same omission by copying the wrong
      half." That claim was **wrong when it was written**: a third copy
      (`folder_autoscan_op.go`) and a fourth (`filesystem.go`) already existed.

      The fix was right; the claim of completeness was not, because the search
      that produced it grepped for the *symptom string* from the production log
      rather than for every caller of the dangerous function. A one-line
      `grep -rn '\.OrganizeBook('` would have found all six immediately.

      When a fix is justified by "now it cannot happen elsewhere", the grep that
      proves it must be over the **callee**, not the symptom.
