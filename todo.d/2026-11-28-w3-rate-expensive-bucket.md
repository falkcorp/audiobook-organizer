- [ ] **W3-RATE-EXPENSIVE** Owner decision, no code change: RateExpensive is a
      separate per-client bucket of the same size as RateDefault
      (`core/internal/httpmw/ratelimit.go:59` keys the bucket class|client), so a
      client that mixes expensive and ordinary routes gets up to twice HEAD's
      request budget. Decide whether that doubling is accepted, or whether the
      two classes should share one budget per client; record the answer in the
      decisions log.
