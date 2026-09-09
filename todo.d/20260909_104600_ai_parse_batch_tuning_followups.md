- [x] **Set `ai_backend.parse_batch_size` / `parse_batch_timeout_seconds` on prod
      and confirm a real `library.ai-parse` run parses > 0 books at the
      production batch size.** Done 2026-09-09: prod runs size 4 / timeout 90s /
      workers 1, and op `01M22Z996DV1G1MJ25XST6S9GN` returned
      `20/20 book(s) parsed in 5/5 batches; 0 batch failure(s), 0 save
      failure(s)` in 252s (~12.6s per book on the CPU 7B). The defaults (20/30s)
      are measured to fail on that hardware: one 20-filename batch is 105s on
      qwen2.5:3b and 201s on qwen2.5:7b.

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

- [ ] **Run one embedding through the APP to close out the local-embedding
      claim.** The backend itself is proven: `bge-m3` on the CPU-only Ollama
      answers a 64-input request — `embedChunkSize` in
      `internal/dedup/engine.go` — in **5.6s** against the 30s
      `defaultRequestTimeout` in `internal/ai/embedding_client.go`, so that
      deadline's "enough for any batch ≤ 64 inputs on a healthy network" comment
      still holds now that the network is a local CPU. Measured 2026-09-09:
      n=1 2.0s, n=16 1.6s, n=64 5.6s, 1024 dims. Sub-linear, unlike the parse
      path, which is why the same "fixed deadline over a caller-chosen batch"
      shape is a bug there and not here.

      What is NOT verified is the app's own path: no `dedup.embed-*` op has run
      since the 2026-09-09 fixes (`/operations/timeline?since=720m` returns zero
      embed rows with `truncated=false`, `scan_capped=false`, `matched=26`, so
      that window is complete). Trigger `dedup.embed-scan` and confirm books
      actually embed before treating local embeddings as working end-to-end.

- [ ] **The AI parser CHAIN is unexercised on this deployment.** Prod is
      `llm_mode=local`, so `internal/scanner/ai_parser_chain.go` has a single
      rung and its `minRungBudget` interaction with the now-configurable
      `parse_batch_timeout_seconds` has never run. The comment there describing
      how raising the timeout widens the window for every rung is reasoning, not
      tested behaviour — exercise it before relying on it.

- [ ] **Four `library.ai-parse` failure rows on prod dated 2026-09-09 are probe
      artifacts, not symptoms.** Completed at 09:18Z (`0/4`), 09:46Z (`0/1`),
      10:12Z (`0/20`) and 11:01Z (`12/20`) — the diagnostic ladder that found the
      batch-size and worker faults, run against fake book IDs the saver skips
      cleanly. Do not diagnose them as production failures. (The two earlier
      rows at 03:03Z and 03:26Z are from before that session and are NOT covered
      by this note.)
