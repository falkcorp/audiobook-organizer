- [ ] **FLIP-HOLD-CHECK** Make the `hold-rehearsal` label a real gate. Today it
      is only a label on unrehearsed flip PRs, and the F4-08 flip (#5061) was
      merged on 2026-11-27 with 19 failing checks and no passing prod-copy
      rehearsal. Add a required check that fails while the label is present, so
      neither a manual merge nor the merge queue can take a held PR. Done means
      a labelled PR cannot be merged without removing the label.
- [ ] **CODEQL-HIGH-MAIN** Triage the four high CodeQL alerts that are on main
      and predate the F4-08 flip: #2244 (clear-text logging,
      `core/internal/authhttp/errors.go`), and #2283, #2242, #2241 (reflected
      XSS in `core/ui/handler.go`, `core/internal/httpmw/writer.go` and
      `compress.go`). #2244 is in sign-in code and goes first. Each one is
      either fixed in code or dismissed with a written reason.
