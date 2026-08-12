- [ ] **SEC-CODEQL-BACKLOG** 326 open CodeQL alerts on `main`, including **2
      critical** and **17 high**. Counted 2026-08-12 via the code-scanning API
      across all four result pages — this is the full set, not a page-1 sample.

      **Why this is filed as one entry and not 326:** 302 of the 326 are a
      single rule, `go/log-injection` (medium), spread across ~30 files. It is
      one pattern — user-supplied strings reaching a log call without newline
      stripping — not 302 independent defects. Fixing it is a mechanical sweep
      plus one helper, and it should be done as a sweep or explicitly accepted
      as a whole, never one PR at a time.

      ```
      302  [medium]    go/log-injection
        5  [high]      go/path-injection
        3  [medium]    actions/missing-workflow-permissions
        3  [high]      go/disabled-certificate-check
        3  [high]      js/remote-property-injection
        2  [high]      go/clear-text-logging
        2  [critical]  go/request-forgery
        2  [none]      js/trivial-conditional
        1  [high]      go/weak-sensitive-data-hashing
        1  [high]      go/uncontrolled-allocation-size
        1  [high]      js/insecure-temporary-file
        1  [high]      go/zipslip
      ```

      **The two critical ones are server-side request forgery** and should be
      read first, because both sit on paths that fetch a URL chosen by remote
      metadata rather than by the owner:

      - `internal/metadata/cover.go:135` (alert #662)
      - `internal/covers/covers.go:82` (alert #645)

      Not assessed. Do not assume they are false positives — a cover URL comes
      from a third-party provider response, which is exactly the untrusted
      input SSRF rules are about.

      **Highs worth reading before the log-injection sweep,** because they are
      on file-mutating or archive-extracting paths where a wrong answer costs
      data rather than log noise:

      - `go/zipslip` — `internal/backup/backup.go:275` (alert #13). Archive
        entry paths used during extraction. This is the restore path.
      - `go/path-injection` ×5 — `internal/fileops/safe_operations.go:122` and
        `:157` (#1477, #1478), `internal/metadata/assemble.go:272` (#1429),
        `internal/server/handlers/filesystem.go:271` (#1105),
        `internal/audiobooks/service_mutation.go:63` (#1104).
      - `go/disabled-certificate-check` ×3 — two are in `tools/cmd/` one-offs
        (`merge-split-books`, `reconcile-paths`), one is in
        `internal/mtls/provisioning.go:142` and matters more.
      - `go/weak-sensitive-data-hashing` — `internal/database/apikey_token.go:33`
        (alert #1466). API-token hashing.

      **Two alerts already assessed as false positives, with the reason
      recorded so nobody re-derives it:**

      1. `go/clear-text-logging` at `internal/server/server.go:360` (raised on
         PR #2321). The flagged expression is `fmt.Sprintf("%T", s.Store())`.
         `%T` renders only the dynamic type name and cannot render a field
         value, so the `password` field CodeQL traced into the store struct
         cannot reach the log record. CodeQL does not model `%T` as
         value-suppressing. The reason is also in a comment at the line.
      2. `go/uncontrolled-allocation-size` at
         `internal/database/memdb_summaries.go:163`. The `make` cap is clamped
         on **both** sides before use: `memdb_summaries.go:80` turns
         `limit <= 0` into 1,000,000 and `:160` clamps anything above 4096 down
         to 4096. No panic path and no unbounded allocation.

      **Context that changes how to read this list:** the CodeQL PR check fails
      on *new* alerts in changed code, so with a 302-alert pre-existing pattern
      almost any PR that adds a log line turns the check red. That has already
      happened twice (#2320 added 7 `go/log-injection` instances in
      `internal/server/handlers/metadata_cache.go`; #2321 surfaced 3 more in
      `internal/httputil/respond.go`). Until the sweep lands, a red CodeQL
      check on a PR carries almost no signal, which is itself the problem — a
      gate that is always red is a gate nobody reads.

      **Suggested order:** (1) read the 2 criticals, (2) the zipslip and the 5
      path-injections, (3) decide sweep-vs-accept on `go/log-injection` as a
      single decision, (4) re-check that the PR gate means something again.
