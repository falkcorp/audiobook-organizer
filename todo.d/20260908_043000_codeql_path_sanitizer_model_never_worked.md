### CodeQL: the repo's path-sanitizer barrier model does not credit SecureJoin, and we now know it is not the predicate names

Found while fixing #3130, where `pathvalidation.SecureJoin` failed to clear a
`go/path-injection` alert it should have cleared. The reason nobody noticed is that
**`main` carries 0 `go/path-injection` alerts** (~100 open alerts overall, none of that
rule), so no flow has ever depended on these rows working. A sanitizer model that is
never exercised is indistinguishable from one that does not work.

> ⚠️ **This entry was filed backwards on 2026-09-08 and corrected the same day (#3132).**
> The original text claimed `path-sanitizers.model.yml` used predicates that "do not exist
> for Go" and that `pathInjectionSanitizer` was the correct one. That is the reverse of the
> truth. Verified against `github/codeql`:
> `go/ql/lib/semmle/go/dataflow/internal/ExternalFlowExtensions.qll` declares
> `barrierModel` (**9** data columns) and `barrierGuardModel` (**10**);
> `pathInjectionSanitizer`/`pathInjectionSanitizerGuard` appear **only** under `java/` and
> `ruby/`. Do not re-derive this from the old text.

- [x] **`.github/codeql/models/go-sanitizers.model.yml` declared a predicate Go does not
      have** (`pathInjectionSanitizer`, 3-column rows). An unknown `extensible:` fails pack
      loading, so it could never credit anything and risked taking the pack down with it. It
      was also a duplicate of `path-sanitizers.model.yml`. **Deleted in #3132.**
- [x] **`.github/codeql/models/path-sanitizers.model.yml` is structurally correct.** Its
      rows measure exactly 9 columns for `barrierModel` and 10 for `barrierGuardModel`,
      matching the declarations above minus the auto-supplied `madId`. No change needed.
- [ ] **🔴 The barrier still does not fire, and deleting the invalid file did not fix it.**
      #3132 ran a known-positive control: a probe commit restoring the exact
      `SecureJoin`+`os.Stat` form that produced 8/8 alerts on 2026-09-07, with the invalid
      file already removed. **The alert fired anyway** — alert 1866, `open`,
      `internal/metadata/cover.go:250`, 2026-09-08T11:44:21Z. So the broken pack was not the
      cause and the remaining question is why *valid* `barrierModel` rows take no effect.
      Candidates, none yet tested:
      - the `kind` column value `"path-injection"` may not be what Go's tainted-path query
        looks for in a **barrier** (sink kinds and barrier kinds need not share a vocabulary);
      - the access path `ReturnValue[0]` may be wrong for a `(T, error)` function, or may need
        to be a bare `ReturnValue`;
      - the pack may not be loading at all — only `.github/workflows/codeql.yml` passes
        `config-file:`; `security.yml`'s advanced reusable workflow does not, so at most one
        of the two Go analyses can see the pack;
      - `subtypes` (`false`) or the empty `type`/`signature` columns may not match.
      **Next step is a canary, not another guess:** the control above is cheap to re-run, so
      change one variable per push and read the alert rather than reasoning about it.
- [ ] **Every language is analyzed by CodeQL twice per run, and Go costs ~8.5 min each time.**
      Noticed while chasing the above, because alerts arrive under two different
      `analysis_key`s and it is not obvious which one a given alert came from.
      `.github/workflows/codeql.yml` analyzes `go`, `javascript-typescript` and `actions` in
      three jobs; `.github/workflows/security.yml` separately passes
      `languages: '["go", "javascript", "actions"]'` to the advanced reusable workflow.
      Measured on completed runs on main: `Analyze (go)` 8m17s (run 34202309268) and
      `Advanced CodeQL Security / CodeQL Analysis (go)` 8m54s (run 34202310167).
      Do **not** simply delete one without first checking which analysis the branch's
      required status contexts reference — and note the config-file asymmetry above, which
      means the two are not interchangeable.

⚠️ These are `.github/` files: push them with git, never the MCP contents API.
