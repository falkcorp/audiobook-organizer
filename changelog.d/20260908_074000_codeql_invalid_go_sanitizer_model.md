### Fixed

- **Removed a CodeQL data-extension file that declared a predicate Go does not have.**
  `.github/codeql/models/go-sanitizers.model.yml` added rows to `pathInjectionSanitizer`
  and `pathInjectionSanitizerGuard`. Those extensible predicates exist only in CodeQL's
  Java and Ruby packs — the Go pack declares `barrierModel` (9 data columns) and
  `barrierGuardModel` (10) in `go/ql/lib/semmle/go/dataflow/internal/ExternalFlowExtensions.qll`.
  An unknown `extensible:` fails pack loading, so the file could not do anything except
  jeopardise the pack it shared a directory with.

  It was also a duplicate: `path-sanitizers.model.yml` already declares the same seven
  sanitizers and one guard, against the correct predicates and with the correct column
  counts. That file is unchanged and is now the only one.

  Found while clearing the pre-existing `go/path-injection` alerts that #3130 surfaced —
  the alerts persisted straight through `pathvalidation.SecureJoin`, which this pack was
  supposed to credit as a barrier.
