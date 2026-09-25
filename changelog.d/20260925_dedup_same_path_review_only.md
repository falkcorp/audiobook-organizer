### Fixed

- Dedup: two book rows that resolve to the same cleaned file path (a real duplicate shape, previously suppressed as chapters of one book) now go to the review queue only. Auto-resolve, the exact-file-hash auto-merge, the LLM high-confidence auto-merge, and the "Merge Filtered" bulk-merge endpoint all refuse to merge this pair; `purge-stale`'s same-directory rule no longer deletes it either. The candidate is tagged with a `same_path` signal so the review UI can explain why it needs a human decision.
