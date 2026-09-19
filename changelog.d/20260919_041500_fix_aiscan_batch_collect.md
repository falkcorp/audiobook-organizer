### Fixed

- Batch-mode AI author scans now collect their results. The batch poller only handed a scan's batches to the pipeline under a batch type nothing ever created, so a batch scan waited on a collector that never ran. The scan's own batch types are now routed, and a running scan also checks its own batches every minute, which also catches failed or expired batches.
- A completed batch is downloaded and applied once, even when several pollers see it at the same time or it is seen again after a restart.
- AI author scans no longer store every result twice. Both enrichment steps started cross-validation when they finished, so a normal scan wrote two copies of each suggestion; cross-validation now replaces a scan's results instead of adding to them, and runs once.
- Cross-validation waits for both enrichment steps to exist and finish. It used to treat an enrichment step that had not started yet as finished, so it could compare suggestions before they were enriched.
- An interrupted realtime AI author scan now resumes instead of failing. Each finished 500-author chunk is saved as soon as it returns, and the resumed scan asks only for the chunks that never finished.
- A batch scan that stopped while creating its batch is marked as submitting first, so a restart finds the batch it already paid for (by the scan and step recorded on the batch) and picks it up instead of paying for a second one. If that lookup itself fails, the scan fails with an error saying so instead of guessing.
- Groups-mode batch results are matched to authors using the grouping the batch was submitted with, not one rebuilt from the author list hours later.
