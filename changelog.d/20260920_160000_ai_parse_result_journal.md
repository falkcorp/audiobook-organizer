- Finished LLM filename-parse results now survive a restart or deploy. `library.ai-parse`
  drops its operation on restart, so every result the model had already produced but that
  had not yet been applied was thrown away and paid for again on a later scan. Results are
  now recorded in the durable AI result journal the moment they arrive, keyed by the exact
  filename the model was shown plus a parse-prompt version, so a re-run serves them instead
  of re-asking. Nothing was ever applied twice, before or after — what was being lost was
  compute, not data.
- The AI journal pruner now walks every journal kind instead of only whisper's, and one
  unprunable kind no longer leaves the others unpruned.
