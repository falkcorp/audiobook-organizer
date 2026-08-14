- [ ] **CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/`logger.sanitizeLogLine`
      as CodeQL log-injection sanitizers via the model pack.** #2445 removed
      the fast-path bypass, but the conduit's own alerts
      (`internal/logging/structured.go:51/58/65`) are STILL open at 316 total:
      taint through the variadic `args ...any` slice survives because CodeQL
      treats per-element slice writes as weak updates — no code shape fixes
      that. The repo already has the mechanism
      (`.github/codeql/models/*.model.yml`, `pathInjectionSanitizer` rows) —
      BEFORE adding rows, verify the extensible predicate name for log
      injection actually exists in the pinned `codeql/go-all` (an unknown
      `extensible:` fails the pack). If none exists, the alternatives are a
      custom .ql suite or bulk dismissal-with-justification of
      conduit-routed alerts. Baseline to beat: 316 open go/log-injection.
