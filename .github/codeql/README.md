<!-- file: .github/codeql/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: 303b26f9-5786-4834-906e-ad57c12ff434 -->
<!-- last-edited: 2026-09-01 -->

# CodeQL Custom Models Pack

This directory contains a CodeQL Models-as-Data (MaD) pack that extends CodeQL's
static analysis capabilities for the audiobook organizer project by declaring
custom sanitizers and validators.

## Purpose

CodeQL's default analysis flags path-traversal vulnerabilities in Go code when
untrusted input flows to filesystem operations. However, the audiobook organizer
uses dedicated sanitizer functions (`SafeJoin` and `WithinRoot` in `internal/util/path.go`)
that prevent path-traversal attacks. This pack teaches CodeQL to recognize these
sanitizers, eliminating false-positive `go/path-injection` alerts.

## Pack Structure

- `codeql-pack.yml` — Pack metadata and dependencies
- `models/path-sanitizers.model.yml` — Sanitizer/validator declarations

## Models Defined

### `SafeJoin` (Barrier)

```yaml
["github.com/jdfalk/audiobook-organizer/internal/util", "", False, "SafeJoin", "", "", "ReturnValue[0]", "path-injection", "manual"]
```

- **Type**: Barrier (stops taint flow)
- **Function**: `func SafeJoin(root string, parts ...string) (string, error)`
- **Semantics**: When `SafeJoin` returns successfully (error is nil), the returned
  path is guaranteed to be within `root`. Any path-traversal attempts cause an error return.
- **Effect**: The return value (index 0 of the tuple) is marked as sanitized for `path-injection`.

### `WithinRoot` (Barrier Guard)

```yaml
["github.com/jdfalk/audiobook-organizer/internal/util", "", False, "WithinRoot", "", "", "Argument[0]", "true", "path-injection", "manual"]
```

- **Type**: Barrier Guard (conditional sanitizer)
- **Function**: `func WithinRoot(path, root string) bool`
- **Semantics**: Returns `true` if `path` is equal to or contained within `root`.
- **Effect**: When `WithinRoot` returns `true`, the `path` argument (Argument[0]) is
  sanitized for `path-injection` in the guarded control-flow path.

## Adding New Sanitizers

To add new path-sanitization helpers:

1. **Implement** the sanitizer function in `internal/util/path.go` (or appropriate package).
2. **Add tests** demonstrating the sanitizer prevents path-traversal.
3. **Declare** the MaD entry in `models/path-sanitizers.model.yml`:
   - **Barrier** (function that sanitizes output): Use `barrierModel` with `ReturnValue[N]`
   - **Barrier Guard** (validation predicate): Use `barrierGuardModel` with `Argument[N]`
4. **Bump** the version in `codeql-pack.yml` and `path-sanitizers.model.yml` headers.
5. **Re-run** CodeQL scan (PR or workflow dispatch on `.github/workflows/codeql.yml`).
6. **Verify** alert count drops in Security > Code Scanning.

## Model Syntax Reference

MaD entries for Go follow this structure:

```yaml
extensions:
  - addsTo:
      pack: codeql/go-all
      extensible: <predicate-name>  # barrierModel or barrierGuardModel
    data:
      - [<package>, <type>, <subtypes>, <name>, <signature>, <ext>, <access-path>, <kind>, <provenance>]
```

### Fields

1. **package**: Full Go import path (e.g., `github.com/jdfalk/audiobook-organizer/internal/util`)
2. **type**: Receiver type for methods (empty string `""` for package-level functions)
3. **subtypes**: `True` if model applies to subtypes/embedders, `False` otherwise
4. **name**: Function/method name
5. **signature**: Always `""` for Go (unused)
6. **ext**: Always `""` (reserved)
7. **access-path**: Where sanitization applies
   - `ReturnValue[N]` — Nth return value
   - `Argument[N]` — Nth argument
8. **kind**: Query kind (e.g., `path-injection`, `sql-injection`)
9. **provenance**: Origin (`manual` for hand-written)

### Barrier Guard Syntax

Barrier guards have an additional field for the accepting value:

```yaml
- [<package>, <type>, <subtypes>, <name>, <signature>, <ext>, <input>, <acceptingValue>, <kind>, <provenance>]
```

- **input**: Argument sanitized (e.g., `Argument[0]`)
- **acceptingValue**: Boolean value that indicates safety (`"true"` or `"false"`)

## Expected Impact

Based on the SAST/SCA audit (see `docs/security/audit-2026-05-03/sast-sca-auditor.md`),
approximately **35-45% of the 80+ `go/path-injection` alerts are false positives**
caused by CodeQL not recognizing `SafeJoin` and `WithinRoot`. This pack should
reduce the alert count to ~40-50 actionable findings.

## References

- [CodeQL MaD Announcement (2026-04-21)](https://github.blog/changelog/2026-04-21-codeql-now-supports-sanitizers-and-validators-in-models-as-data/)
- [Customizing Library Models for Go](https://codeql.github.com/docs/codeql-language-guides/customizing-library-models-for-go/)
- [CodeQL Go Extensions (Standard Library)](https://github.com/github/codeql/tree/main/go/ql/lib/ext)
- [CodeQL Pack Properties](https://docs.github.com/en/code-security/tutorials/customize-code-scanning/customizing-analysis-with-codeql-packs#codeqlpack-yml-properties)

## Local Verification (Optional)

If the CodeQL CLI is installed locally:

```bash
# Install pack dependencies
codeql pack install .github/codeql/

# Create CodeQL database
codeql database create \
  --language=go \
  --command="cd /path/to/worktree && GOTOOLCHAIN=go1.27.1 go build ./..." \
  /path/to/abk-codeql-db

# Run analysis with custom pack
codeql database analyze /path/to/abk-codeql-db \
  --format=sarif-latest \
  --output=/path/to/abk-codeql.sarif \
  --search-path=.github/codeql \
  security-extended

# Check alert count
grep -c '"ruleId": "go/path-injection"' /path/to/abk-codeql.sarif
```

**Note:** Replace `/path/to/worktree` and `/path/to/abk-codeql-db` with actual paths.
`GOTOOLCHAIN=go1.27.1` matches the Makefile's pin so the extractor builds with the
same toolchain as CI; the former `GOEXPERIMENT=jsonv2` requirement ended with Go 1.27,
where `encoding/json/v2` is GA and the default.

## Maintenance

- **When adding new path sanitizers**: Follow "Adding New Sanitizers" above.
- **When refactoring util package**: Update package path in all MaD entries.
- **When CodeQL updates break the pack**: Check [CodeQL changelog](https://codeql.github.com/docs/codeql-overview/codeql-changelog/)
  and update syntax if necessary.
