- [ ] **`internal/ai/retry.go`'s `isPermanentAIError` HTTP-429 branch checks
      the wrong JSON field for OpenAI's real quota-exhaustion error.** Found
      while wiring `internal/scanner/ai_failure.go`'s `isPermanentAIFailure`
      to reuse this classifier (TODO L4852/L4961). The branch is
      `case 429: return apiErr.Code == "insufficient_quota"`, and
      `openai-go`'s `apierror.Error.Code` decodes the response JSON's `"code"`
      field (`internal/apierror/apierror.go`:
      `Code string \`json:"code" api:"required"\``). The production error
      captured in `internal/scanner/ai_failure_test.go`'s `prodQuotaError` —
      copied from the scanner's own incident journal, not composed — is a 429
      with `"type": "insufficient_quota"` but `"code": "credit_balance_exhausted"`.
      Against the real payload, `apiErr.Code` is `"credit_balance_exhausted"`,
      not `"insufficient_quota"`, so this branch returns `false` for the exact
      error the scanner's test suite exists to catch: `DoWithRetry` retries it
      as transient, burns `maxRetries` attempts with backoff, and only
      `internal/scanner/ai_failure.go`'s substring-marker fallback (which
      still checks for `"credit_balance_exhausted"` and `"insufficient_quota"`
      as raw text) catches it after the fact. Fix is presumably to also accept
      `apiErr.Type == "insufficient_quota"`, or to match on either field
      depending on which one OpenAI's docs treat as stable API for this error
      family — needs the same kind of primary-source check TASK-124 did
      before changing retry.go's classification, since retry.go's own
      `DoWithRetry` is used by other callers too.
