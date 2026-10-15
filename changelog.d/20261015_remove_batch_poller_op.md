### Changed

- Removed the `maintenance.batch-poller` operation. Nothing ever enqueued it, so it only left an empty operation row every five minutes. The scheduler's built-in `batch_poller` task is, and was, the only thing that polls OpenAI for completed batches; its behaviour is unchanged.
