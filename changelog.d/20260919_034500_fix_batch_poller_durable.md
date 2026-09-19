### Fixed

- Restarting the server no longer re-applies finished OpenAI batch results.
  The batch poller used to remember which batches it had handled only in
  memory, so every restart replayed every completed batch in OpenAI's recent
  list — re-running LLM dedup verdicts and re-writing embeddings. Each handled
  batch is now recorded in the database, and an AI job that already finished
  is never applied again even if its batch is delivered twice.
- A paid OpenAI batch is no longer lost when the server is stopped just after
  submitting it. Each batch now carries its job id, and the poller re-attaches
  any job that never recorded its batch so the results are still collected.
- When applying an AI job's results fails (the database is busy, the server
  is shutting down), the job is retried later with growing waits instead of
  being retried blindly every five minutes forever. After five failed tries
  it is marked failed with the reason, visible in the AI jobs list. A batch
  that OpenAI itself failed or let expire now marks its job failed instead of
  leaving it waiting forever.
