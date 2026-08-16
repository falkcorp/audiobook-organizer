- [ ] **Give the AI parser typed provider errors.** `internal/scanner/ai_failure.go`
      decides whether an AI failure is permanent by substring-matching the error text
      (`insufficient_quota`, `invalid_api_key`, …) because `aiParser.ParseBatch` flattens
      the HTTP status and the provider's error code into a `fmt.Errorf` string several
      layers down. Return a typed error carrying status + provider code so the check can
      be `errors.As` instead of `strings.Contains`. The current matcher is safe to miss —
      the phase still stops after 3 consecutive failures — but a miss costs ~60s of
      guaranteed-failing calls per scan.
