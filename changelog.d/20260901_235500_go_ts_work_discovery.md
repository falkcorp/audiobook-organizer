### Added

#### Go and TypeScript work-discovery audit

`docs/audits/2026-09-01-go-ts-work-discovery.md` records what four read-only
surveys found and did not fix: duplicated behaviour (three maintenance-op
catalogues, three backup-cleanup predicates, fourteen pagination parsers,
four retry implementations, ten N+1 `GetBookFiles` loops), serial
whole-library loops, the ten worst silent-failure sites, coverage floors,
dead code, a `go fix`-adjacent modernization census with an explicit
do-not-convert list, two confirmed frontend bugs (`getBooksByAuthor` always
returns `[]`; three `Book` field names disagree with the Go wire format), and
a package-by-package census of what the TypeScript toolchain upgrades unlock.
Each item carries a `file:line` anchor and the source's own sizing so the
follow-on PRs can cite it by ID.
