## Decide whether to cap openai-go's own retry loop

`internal/ai/retry.go`'s `DoWithRetry` is not the only retry layer. openai-go
runs a second one underneath it that our code never sees:

- `internal/requestconfig/requestconfig.go` defaults `MaxRetries: 2`, and
- `shouldRetry` returns `true` whenever `res == nil` — i.e. for exactly the
  connection failures `isUnreachableError` now refuses to retry.

So one `DoWithRetry` attempt is really **three dials plus ~1.5s of the SDK's own
backoff**. Against the unroutable host in production on 2026-09-09 the two loops
multiplied to 9 dials and ~14.5s of pure backoff, which is what consumed the
parser chain's entire 30s budget.

The SDK's per-error opt-out cannot be used from our code: it is
`errors.As(err, &deterministic)` against an **unexported** `interface{ noRetry() }`
declared inside the SDK's internal package, and Go only lets same-package types
satisfy an unexported interface method. The only lever from outside is
`option.WithMaxRetries`, which is all-or-nothing.

- [ ] Decide whether to pass `option.WithMaxRetries(0)` and let `DoWithRetry`
      own retry policy outright. **The cost is real and needs a decision:** it
      also discards the SDK's `Retry-After` header handling, which is the
      correct behaviour for genuine OpenAI 429s and which `DoWithRetry`'s
      quadratic backoff does not replicate.
- [ ] If the answer is "only for local backends", scope it to
      `NewOpenAIParserWithBaseURL`'s Ollama path, where there is no
      `Retry-After` story to lose — but note that constructor is the generic
      "any OpenAI-compatible base URL" path, not Ollama-only.
- [ ] Whichever way it goes, `DoWithRetry`'s doc comment names the current
      behaviour explicitly; update it rather than leaving two descriptions.

Not blocking: with `DoWithRetry` fixed, a dead host costs ~5s instead of 30s,
which is already well inside `minRungBudget` (5s), so fallback rungs are reached.
This is throughput, not correctness.
