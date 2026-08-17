- [ ] **The AI-scan cancel wiring is unverified, and it fails silently.**
      `CancelOperationV2` now cancels an AI scan through the pipeline manager
      (ported from the retired `DELETE /operations/:id`), but the collaborators
      arrive via `handlers.WithAIScanCancellation(...)` in `wire_handlers.go` and
      nothing asserts that call is still there. Drop it and cancelling an AI scan
      returns `204 No Content` while the scan keeps running — the exact defect the
      port exists to prevent.
      No test can cover it today because `Server.pipelineManager`
      (`*aiscan.PipelineManager`) and `Server.aiScanStore` (`*database.AIScanStore`)
      are concrete types, so a test cannot substitute them and drive the real
      construction path. Narrow them to the `ScanCanceler` / `AIScanLister`
      interfaces the handler already declares, then assert the wiring.
      Good candidate for the interface-splitting review.
