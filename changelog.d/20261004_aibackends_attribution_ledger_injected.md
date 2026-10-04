### Fixed

#### `TestEndpointsStatus_RoutingSwitchAndAttribution` no longer fails under `-count>1`

The test recorded two attempts into the process-wide `aidispatch.DefaultAttribution` ledger, then asserted that `GET /ai/endpoints/status` reported exactly 2 requests. The ledger is process-wide on purpose: dispatchers are built per call, so the counts must outlive them. Each later `-count` iteration therefore read 4, then 6. It failed 19 of 20 iterations at `-count=20`.

The aibackends `Handler` now takes its ledger by injection. `aibackendshandler.New` accepts options, and `WithAttribution` sets the ledger. The default is still the process-wide ledger, so production wiring is unchanged. The test records into a ledger it owns. A new test pins that the default handler reports the process-wide ledger.
