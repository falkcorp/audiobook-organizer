- [ ] **Set `ai_backend.parse_batch_size` / `parse_batch_timeout_seconds` on prod
      and confirm a real `library.ai-parse` run parses > 0 books at the
      production batch size.** The knobs exist as of 2026-09-09 but prod is still
      on the defaults (20 / 30s), which are measured to fail on its CPU-only
      backend: one 20-filename batch is 105s on qwen2.5:3b and 201s on
      qwen2.5:7b. Suggested starting pair for that hardware is size 4 with a 90s
      timeout (~68 completion tokens per filename, ~10s per book on the CPU 7B).

      Verify the way the 2026-09-09 session did, not with a status endpoint: a
      single-book probe passes even on a broken configuration, because the cost
      is linear in batch size. Trigger `POST /operations/v2` with
      `{"def_id":"library.ai-parse","params":{"books":[... 20 entries ...]}}` and
      read `progress_message` for `N/N book(s) parsed`.

- [ ] **Decide 7B vs 3B for `ai_backend.local_llm_model` on the CPU backend.**
      qwen2.5:7b-instruct is 6.9 tok/s and 3b-instruct is 15.2 tok/s on the
      Ryzen 7 3800X — but on the one 20-filename sample compared so far, the 3B
      returned `series_number: 0` for "Mistborn 01" where the 7B returned `1`.
      This is a metadata WRITE path, so the 2.2x speedup is not obviously worth
      it. Needs a real accuracy comparison over a sample of actual library
      filenames before switching, not a single spot check.

- [ ] **Two `library.ai-parse` failure rows on prod dated 2026-09-09 are probe
      artifacts, not symptoms.** One with a single book ID ending `dead`, one
      with 20 sequential fake UUIDs. Both were deliberate verification runs. Do
      not diagnose them as production failures.
