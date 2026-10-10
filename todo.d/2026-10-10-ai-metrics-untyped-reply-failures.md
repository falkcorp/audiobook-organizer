- [ ] **AI-METRICS-UNTYPED-REPLY-FAILURES** Untyped bad replies are not counted
      in `ai_failures_total` (N2 of PR #3938 round 1). Only `*ReplyParseError`
      and `*ResultCountError` reach `recordReplyFailure`. Four of the nine
      tasks turn a bad reply into a plain `fmt.Errorf` inside the
      `DoWithRetry` closure: `reviewAuthorBatch` and `discoverAuthorBatch`
      ("failed to parse response" in `internal/ai/openai_parser.go`) and
      `scoreMetadataBatch` (`internal/ai/metadata_llm_review.go`), so an
      unparseable reply is two `ok` requests and no failure. The empty-choices
      error ("no response from OpenAI", six sites in `openai_parser.go` and one
      in `metadata_llm_review.go`, plus `errNoCoverTextChoices` in
      `cover_text.go`) is uncounted for the same reason. Net effect: the
      "Reply-quality failures by task" panel is structurally zero for those
      tasks. Fix: return `newReplyParseError` from those sites (empty choices
      as `reply_parse`), call `recordReplyFailure` where each is returned, and
      add one test per site. It changes the error type those callers see, so
      check the retry predicates (`isPermanentAIError`, `DoWithRetry`) still
      treat them as retryable.
