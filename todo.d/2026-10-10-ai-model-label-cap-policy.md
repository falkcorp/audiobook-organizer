- [ ] **AI-MODEL-LABEL-CAP-POLICY** Land the `model` label policy for the AI
      metrics (SF3 of PR #3948 round 1). The owner decided on 2026-10-11:
      always name the configured models, so the label no longer depends on
      which task ran first after boot. Before that decision `modelLabelSet` in
      `internal/ai/ai_metrics.go` admitted the first five distinct
      (provider, model) pairs that reached `observe` or `recordReplyFailure`
      and folded every later pair to `other` until the process restarted, so a
      settings experiment could leave the serving model folded to `other`, the
      `unknown` label took a slot though only `other` is exempt in `admit`, and
      the named set was order-dependent. The change is implemented in the
      stacked branch `fix/ai-model-label-configured`, not in this PR; the
      1,000-series bound is unchanged. Check this entry off when that branch
      merges (and grep `TODO.md` for the title first, per `todo.d/README.md`:
      the daily assembly may have folded this fragment in already).
