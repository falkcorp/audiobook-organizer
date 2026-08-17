### Fixed

- **Applying metadata no longer holds the review dialog hostage.** The apply already
  ran as a background operation server-side, but the dialog then sat on
  `pollOperationV2` until every book finished, which cancelled out the benefit. It
  now dispatches, toasts "Metadata apply queued for N book(s) — watch the bell for
  progress", and returns. The bell already tracks every background op, so progress is
  not lost. The Apply buttons re-enable immediately instead of after the batch, and
  further applies queue server-side rather than being locked out.
- **A blank `llm_mode` no longer silently means OpenAI.** `EffectiveLLMMode()` fell
  through to OpenAI whenever an API key happened to be set, so an empty config field
  chose a paid external service with nothing logged — on 2026-08-16 that ran a whole
  library scan against OpenAI until the account hit `credit_balance_exhausted`, at
  which point 77 consecutive batches failed and the watchdog killed the scan. A blank
  mode now prefers the local backend whenever one is configured.
