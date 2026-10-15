### Changed

- Removed the `maintenance.batch-poller` operation. It never ran: nothing enqueued it, and its declared five-minute schedule was never driven (an operation's `Schedule` field is read by nothing). Nothing visible changes. The scheduler's built-in `batch_poller` task is, and was, the only thing that polls OpenAI for completed batches; its behaviour is unchanged.
