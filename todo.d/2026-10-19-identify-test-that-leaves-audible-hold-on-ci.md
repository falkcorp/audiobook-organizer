- [ ] **TEST-LEAVES-AUDIBLE-HOLD** Find which `internal/server` test left a hold on
      audible in the process-wide throttle registry on Woodpecker pipeline 1222
      (mac agent). The harvest tests are now immune (#4083 injects the registry), but
      the leaking test is still unknown. A full local `go test ./internal/server/ -v`
      with a stack print in `RecordFailure` showed no audible hold outside the
      deliberate installs (`metadata_ops_throttle_test.go` lines 41/144/196, which
      reset and clean up), so it likely needs real network or a different order.
      Candidates that build a real `metadata.NewAudibleClient()`:
      `internal/server/metadata_ops_test.go` (~L317, bulk fetch with sources
      disabled still constructs the real client) and
      `internal/server/metadata_ops_throttle_test.go`. Pin it by running the package
      on the mac agent with that stack print, or by blocking DNS for api.audible.com.
