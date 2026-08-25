## Scanner / scan cache

- [ ] **Wire `BackfillBookFileScanCache` so it can actually be invoked before the
      per-file scan cache reader goes live.** The function exists, is idempotent and
      has a dry-run mode, but it currently has ZERO callers, so as shipped the
      deploy-herd protection it was written for does not yet exist: if the reader is
      deployed without it being run, every `book_file` row reads as "never scanned"
      and the first scan is a whole-library cold re-read on a library that already
      takes 4-6 hours.

      Three options, and this is a deploy-shaped decision rather than a code one:
      (a) register it as a `maintenance.*` op — note the maintenance plugin is gated
      on `RootDir`, so with no `--dir` it registers 0 of 105 ops and would silently
      not appear; (b) expose it as an explicit `POST /api/v1/operations/...` endpoint
      like `elect-missing-primaries`; (c) leave it library-only and call it once by
      hand as a documented pre-deploy step.

      Until one of those lands, the reader must not be deployed without a manual
      invocation first. See `docs/plans/2026-08-24-per-file-scan-cache-design.md`.
