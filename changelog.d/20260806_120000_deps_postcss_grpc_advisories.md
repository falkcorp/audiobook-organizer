<!-- file: changelog.d/20260806_120000_deps_postcss_grpc_advisories.md -->
<!-- version: 1.0.0 -->
<!-- guid: c5e1abb4-d9dc-4230-a186-e4e1e25e649e -->
<!-- last-edited: 2026-08-06 -->

### Security

- **Closed two high-severity Dependabot alerts: `postcss` and
  `google.golang.org/grpc`.** Both are dependencies we never import directly, so
  neither bump changes a single line of our own code — but both were flagged
  against ranges the repository was sitting inside.

  `postcss` reached us transitively through Vite's build pipeline, pinned at
  `8.5.15` against a vulnerable range of `<= 8.5.17`. `npm update postcss`
  resolves it to `8.5.26`, comfortably inside the `^8.5.6` range Vite already
  asks for, so no `overrides` entry was needed and Vite's own dependency
  contract is untouched. The same update carried `nanoid` from `3.3.12` to
  `3.3.17` as a side effect — postcss depends on it, and npm refreshed it while
  it was in the tree.

  `google.golang.org/grpc` moved `v1.81.1` → `v1.82.1` (first patched version).
  It is an indirect dependency, arriving via the OpenTelemetry OTLP trace
  exporter and grpc-gateway; nothing in this repository imports it. Worth noting
  because it was the one real risk in this change: grpc `v1.82.1` declares
  `go 1.25.0`, so on a module still pinned to an older Go it would have silently
  rewritten the `go`/`toolchain` directives and turned a dependency patch into a
  language-version bump. `go.mod` already declares `go 1.26.0`, so it did not —
  `go mod tidy` produced a clean one-line change with no cascade into the otel
  or grpc-gateway versions.

  These are the two advisories with no API surface to review, deliberately split
  from the React Router `v6 → v7` major bump that closes the remaining alerts.
