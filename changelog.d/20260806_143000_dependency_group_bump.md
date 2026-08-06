<!-- file: changelog.d/20260806_143000_dependency_group_bump.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a2d90f4-1c7b-4e83-b5d6-08f39ce27a15 -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **Routine dependency group bump** across both ecosystems:
  `github.com/openai/openai-go/v3` 3.46.0 → 3.49.0, `axios` 1.18.1 → 1.19.0,
  `@playwright/test` 1.62.0 → 1.62.1, and `form-data` 4.0.5 → 4.0.6. The
  `github/codeql-action` steps move to v4.37.4, still SHA-pinned.

  None of these close an open security advisory — the five outstanding
  Dependabot alerts (`postcss`, `google.golang.org/grpc`, and three against
  `react-router` / `react-router-dom`) are untouched by this bump and are being
  handled separately. Recording that here so a green dependency PR is not
  mistaken for the vulnerabilities being cleared.
