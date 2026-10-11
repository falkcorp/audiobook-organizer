- [ ] **AI-MODEL-LABEL-CAP-POLICY** Decide how the `model` label cap admits
      models (SF3 of PR #3948 round 1; OWNER DECISION, behavior unchanged for
      now). `modelLabelSet` in `internal/ai/ai_metrics.go` admits the first
      five distinct (provider, model) pairs that reach `observe` or
      `recordReplyFailure` and folds every later pair to `other` until the
      process restarts. Three consequences: a settings experiment (trying
      three names in `filename_parse_model` or `metadata_review_model`, or
      editing a pool row's `chat_model` twice) uses slots for good, so the
      model that ends up serving traffic can fold to `other`; the `unknown`
      label takes a slot though only `other` is exempt in `admit`; and which
      models are named depends on which task runs first after boot, so a
      "p95 by model" panel can show a model named before a deploy and folded
      after it with no config change. The 1,000-series bound itself holds.
      Option A, pre-admit the configured models at startup
      (`filename_parse_model`, `metadata_review_model`, the embedding model
      and every pool row's `chat_model` at load) so the named set is
      deterministic; also exempt `unknown` from admission. Option B, keep the
      first-seen behavior and document in `docs/AI-REFERENCE.md` that the
      named set is per boot and order-dependent. Option A costs a startup hook
      that must also run when a pool row is added or edited; option B costs
      nothing but leaves the dashboard's model breakdown unstable once the
      pool grows past five models.
