### Fixed

- The scheduled `dedup_llm_review` task no longer tries to queue `dedup.llm-review` when the dedup engine is running without the embedding store. That operation only exists when the dedup plugin is built, which needs both, so the task now checks for the plugin, the same way `label_refinement` does.
