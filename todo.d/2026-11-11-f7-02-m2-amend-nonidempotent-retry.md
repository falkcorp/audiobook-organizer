- [ ] **F7-02-M2-AMEND** Amend 05-metadata M2.4 for the retry rule F7-02 implemented after the
      security review of #4610: a request whose method is not idempotent (no GET, HEAD, OPTIONS,
      TRACE, PUT or DELETE, and no `Idempotency-Key` header) is resent only when nothing was
      written (`httptrace` saw neither `GotConn` nor `WroteRequest`) or when the server says it did
      not process it (429, or 503 with a Retry-After). The spec retries every Retryable class,
      which can run a non-idempotent write twice. Record the rule in the spec, or decide to revert
      it. Details: `docs/agent-tasks/platform-core/seams.d/F7-02.md`, item N9.
