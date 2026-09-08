### CodeQL: the repo's path-sanitizer models have never been exercised, and at least one is malformed

Found while fixing #3130, where `pathvalidation.SecureJoin` failed to clear a
`go/path-injection` alert it should have cleared. Two separate defects, and the reason
neither was noticed is the same: **`main` carries 0 `go/path-injection` alerts** (100 open
alerts overall, none of that rule), so no flow has ever depended on these rows working.
A sanitizer model that is never exercised is indistinguishable from one that does not work.

- [ ] **`.github/codeql/models/path-sanitizers.model.yml` uses extensible predicates that do
      not exist for Go.** Its rows declare `extensible: barrierModel` and
      `extensible: barrierGuardModel` with 9-column tuples. The Go pack's extensible
      predicates for this purpose are `pathInjectionSanitizer` and
      `pathInjectionSanitizerGuard`, which take **3-column** rows. Confirm whether CodeQL
      silently ignores the file or errors, then either port the rows to the correct
      predicate or delete the file — as written it contributes nothing while looking like
      coverage.
- [ ] **`.github/codeql/models/go-sanitizers.model.yml` may be mis-addressing the output of
      multi-return functions.** It uses the correct predicate and does list
      `["…/internal/security/pathvalidation", "SecureJoin", "ReturnValue"]`, but `SecureJoin`
      is `func(root string, parts ...string) (string, error)`. Determine whether a
      multi-return function needs `ReturnValue[0]` rather than a bare `ReturnValue`; the same
      question applies to every other `(T, error)` entry in that file (`CleanAbsolutePath`
      and friends). **Unconfirmed** — this is the leading hypothesis for #3130's behaviour,
      not a diagnosis.
- [ ] **Add a canary.** Whatever the fix, the failure mode here is silence: the model can rot
      indefinitely because nothing fails when it stops working. A minimal source→`SecureJoin`
      →sink fixture that CodeQL must report as clean would turn the next regression into a
      red check instead of a surprise three years later.

#3130 sidestepped the question rather than answering it — the cover lookup was rewritten to
probe through an `fs.FS` rooted at the covers directory, which `io/fs` confines by
construction and which needs no custom model. That is a better shape for that call site
regardless, but it means the model defect above is **still live** for every other call site
that relies on `SecureJoin` to satisfy the analyzer.

⚠️ These are `.github/` files: push them with git, never the MCP contents API.
