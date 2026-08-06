<!-- file: docs/status/2026-07-02-local-cutover-and-matching.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f3a1c92-0b4e-4d21-9a6c-2e8f4b1d7c30 -->
<!-- last-edited: 2026-07-02 -->

# Status — Local Backend Cutover + Matching/Dedup Fixes (2026-07-02)

## TL;DR

OpenAI hit `insufficient_quota` (429). Switched embeddings + LLM to a **local
Ollama backend on the Windows GPU box** (<gpu-host>), fixed the matching and
dedup bugs that were reported, and shipped 9 PRs. A full re-embed to `bge-m3` is
running on the GPU. Two tracks remain: the **LLM backend-mode toggle** and
**fingerprint coverage** for dedup.

## Shipped this session (all merged + deployed)

| PR | Area | What |
|----|------|------|
| #1733 | reliability | PEBBLE-CLOSED-SHUTDOWN-RACE — enrol registry notify goroutines in `goroutineWG` + `notifyStopped` gate; real-Pebble `-race` repro |
| #1734 | matching | Transcription boost requires title agreement (author/narrator no longer over-boost wrong book by right author) |
| #1735 | frontend | Pagination bounce-to-page-1 fixed — `shallowEqualFilters` guard on the URL→filters sync effect |
| #1736 | dedup | `ReevaluateAcoustIDConflicts` + `/dedup/purge-acoustid-conflicts` diagnostic endpoint |
| #1737 | dedup | AcoustID emission veto — **pulled** (coverage-bound; not viable as a gate) |
| #1738 | embeddings | Re-embed skip is model-aware (`embeddingModelMatches`) so a backend switch actually re-embeds |
| #1739 | embeddings | Ollama availability trusts explicit `embedding.base_url` (`server.go:616`) |
| #1740 | embeddings | HNSW `Import` discards stale-dim snapshot (3072 vs 1024) |
| #1741 | embeddings | HNSW `Upsert` `safeAdd` recovers coder/hnsw library panics → error |

## Local backend setup (Windows GPU box, <gpu-host>)

- Reached via `ssh windows-gpu` (key preinstalled). PowerShell over SSH mis-parses
  scp'd `.ps1` — use `-EncodedCommand` (base64 UTF-16LE).
- Ollama kept alive by scheduled task **"OllamaServe"** (interactive session for
  GPU access; plain `ollama serve` over SSH dies on disconnect / headless auto-start).
- Models pulled: **bge-m3** (1024-dim embeddings), **qwen2.5:7b-instruct** (LLM).
- Setup scripts currently in scratchpad only: `setup-ollama.ps1`, `start-ollama.ps1`
  → TODO commit as `scripts/manage-ollama-windows.py`.

## Prod config

```
embedding.base_url        = http://<gpu-host>:11434/v1
embedding.model           = bge-m3
embedding.dimensions      = 1024
metadata_scoring.llm_enabled = false
```

Vector indexes (chromem in-memory + coder/hnsw snapshot at
`/var/lib/audiobook-organizer/hnsw`) are **derived** from PebbleDB's
`EmbeddingStore`. `embeddings.db` SQLite is stale/legacy.

## Re-embed status

Running via `dedup.embed-scan` (sync, `POST /dedup/embed` — the model-cutover path;
NOT `dedup.embed-async`, which uses the OpenAI Batch API). ~12K non-primary
versions skipped, then ~29K primaries embedding via bge-m3 on the GPU. 0 errors,
0 panics. Self-completing.

## Matching / dedup findings

- **Transcription:** fixed as above (#1734).
- **AcoustID dedup veto:** NOT viable at current coverage. ~65% of candidates have
  an unfingerprinted book; `BookSignatureSimilarityMasked ≈ 0.50` is the noise
  floor for uncorrelated audio; only 5/4510 comparable pairs fell below it. The
  real lever is **fingerprint coverage** (~8,387 books unfingerprinted).

## Pending

1. **LLM backend-mode toggle** — config enum + FE selector (disable-all /
   OpenAI-only / local-only / OpenAI+local-fallback) + model-download prompt when
   local. Local target = qwen2.5:7b-instruct on <gpu-host>.
2. **Fingerprint coverage** track — fingerprint the ~8,387 unfingerprinted books,
   then the AcoustID veto + stronger dedup auto-resolution become viable.
3. **Commit Windows Ollama scripts** as `scripts/manage-ollama-windows.py`.
4. Deferred (gated — do NOT run without greenlight): ai-responses-migration ×5,
   dedup C8, perf-cleanup CONS-13.
