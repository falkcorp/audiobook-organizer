<!-- file: docs/consultancy/03-matching-and-backends.md -->
<!-- version: 1.0.0 -->
<!-- guid: 74c1dd3a-9f40-4b1d-8826-406f0841f7ba -->
<!-- last-edited: 2026-07-02 -->

# Consultancy Evaluation — Matching Methods & AI Backends (2026-07-02)

Evaluation produced by a read-only multi-agent consultancy workflow. Two specialist reports feed this dimension: a matching-subsystem code review (MATCH-*) and a backend-toggle readiness review with a full design (TOGGLE-*). All findings cite `file:line` in the repo as of 2026-07-02. An independent advisor pass verified the phase's citations; its corrections are reflected inline and in the "Advisor verification" section.

## Executive Summary

The matching subsystem's recent fixes are sound: the #1734 title-gated transcription boost (`service_scoring.go:290-327`) and the apply-side hard gate (`transcribedTitleAgrees`, `service_fetch.go:235`) are correctly implemented. The most serious defect — MATCH-1, **confirmed critical and live during the current bge-m3 re-embed** — is that `EmbeddingScorer`'s BookID fast-path returns the stored (possibly 3072-dim OpenAI) vector with no model/dimension check, while candidates are embedded with the new 1024-dim client. `CosineSimilarity` returns 0 on length mismatch, the scorer returns all-zero scores with a nil error, no F1 fallback triggers, and every candidate is filtered below `EmbeddingMinScore` — searches for any not-yet-re-embedded book return zero results whenever embedding scoring is enabled.

Second-tier issues: `RerankTopK` mixes clamped [0,1] LLM scores with the unclamped multiplied base-score tail (a scale-mismatch landmine for the pending local-LLM toggle — see advisor correction on MATCH-2); `IsGarbageValue`'s substring `"error"` check nukes legitimate titles/authors ("The Terror", "Comedy of Errors") from hints, boosts, and writes; the auto-match op's Search/Apply pair reads cache candidate[0] twice with no identity check (TOCTOU past the gates); per-source tier fallback can mix embedding-cosine and F1 scores in one ranked list; the cover filter can drop a top-scored exact ASIN/transcription match; `embedclient` registration requires an OpenAI key even for keyless Ollama; and retry paths have no permanent-error (429 quota) classification, which the fallback backend mode will need.

On the backend side, current plumbing is halfway to a toggle: embeddings can point at Ollama via `embedding.base_url` (`config.go:94`, scoped per-client at `embedding_client.go:116-123`), embeddings are model-tagged (cache keyed by hash+model; dedup re-embed skip is model-aware via `embeddingModelMatches`, `engine.go:2110`), and availability gating exists (`server.go:616-625` trusts a configured base_url). But **there is no mode enum anywhere** — key presence is the de-facto backend selector: local embedding still requires a non-empty `OpenAIAPIKey` (`register.go:35`); the LLM side has NO per-config base URL at all — only the process-wide `OPENAI_BASE_URL` env (`openai_parser.go:85`), which would redirect every client; `retry.go` retries 429/insufficient_quota blindly with no fallback trigger; the OpenAI Batch API path (`EmbedBooksAsync` → `CreateEmbeddingBatch`) is not gated on backend and its skip-check also lacks the model-match guard; and Ollama availability is probed once at startup into a plain (non-atomic) bool. The full backend-mode toggle design (independent embedding/LLM mode enums, config migration, error-classified fallback, FE selector, model-pull prompt) is reproduced in the Design section below.

## Advisor verification

The P3 boundary advisor spot-checked citations and reports them accurate. Specific corrections and confirmations, reflected in this report:

- **MATCH-1 confirmed end-to-end**: unchecked store fast-path (`embedding_scorer.go:92-95`) + `CosineSimilarity=0` on dim mismatch (`embedding_store.go:1214`) + nil-error acceptance (`service_scoring.go:492`) = no F1 fallback. **Genuinely critical during the live re-embed.**
- **MATCH-2 slightly overstated as originally reported**: the code (`service_scoring.go:647-661`) deliberately avoids re-multiplying; the residual defect is a **scale mismatch** between clamped [0,1] LLM scores and the unclamped tail — real but conditional. "Systematic demotion" is too strong; the finding text below has been adjusted accordingly.
- **MATCH-3 and TOGGLE-1 confirmed verbatim.**
- **Omission noted by the advisor** (not in the specialist findings): mangled slog calls throughout the matching code — duplicate attribute keys, printf verbs left in messages, generic `"value"` keys (`service_scoring.go:607, 612, 642, 677`) — suggest a botched mechanical fmt→slog sweep. Flagged as a bug-hunt target for a follow-up phase (repo-wide grep for duplicate attr keys and `"value",` / `"count",` patterns with %-verbs still in messages).

## Findings Table

| ID | Severity | Impact | Effort | Title |
|---|---|---|---|---|
| MATCH-1 | critical | high | low | EmbeddingScorer store fast-path ignores model/dimension — mixed-dim query vs candidate vectors score 0 and silently kill all search candidates |
| MATCH-2 | high | high | low | RerankTopK mixes clamped [0,1] LLM scores with unclamped multiplied base scores — scale mismatch can demote reranked winners below the tail |
| TOGGLE-1 | high | high | low | Local Ollama embedding still requires a non-empty OpenAIAPIKey |
| TOGGLE-2 | high | high | low | EmbedBooksAsync (OpenAI Batch API) is not gated on backend and its skip-check ignores the embedding model |
| TOGGLE-3 | high | high | medium | No per-config base URL for the LLM path — local LLM mode is currently impossible without redirecting every client |
| MATCH-3 | medium | medium | low | IsGarbageValue substring match on "error" rejects legitimate titles/authors (e.g. "The Terror", "Comedy of Errors") |
| MATCH-4 | medium | medium | medium | Per-source scorer fallback can mix embedding-cosine and F1 scores in one ranked candidate list |
| MATCH-5 | medium | medium | low | Cover filter runs after all scoring and can drop the highest-scored candidate |
| MATCH-6 | medium | medium | low | Auto-match apply path re-reads cache candidate[0] independently of the gated search snapshot (TOCTOU past all three gates) |
| MATCH-7 | medium | medium | medium | No permanent-error classification in retry paths — 429 insufficient_quota is retried like a transient failure |
| MATCH-8 | medium | medium | low | embedclient and llmparser registrations gate on OpenAIAPIKey even for keyless local Ollama backends |
| TOGGLE-4 | medium | medium | low | retry.go retries 429/insufficient_quota blindly; no error classification, no fallback hook |
| TOGGLE-5 | medium | medium | medium | Ollama availability is a one-shot startup trust with a non-atomic flag — unsafe for runtime mode switching |
| MATCH-9 | low | low | low | LevenshteinDistance operates on bytes, inflating distances for non-ASCII titles |
| TOGGLE-6 | low | low | low | metadatallmscorer is built regardless of MetadataScoring.LLMEnabled; gate lives only at one call site |
| TOGGLE-7 | low | medium | medium | Model names are OpenAI-specific defaults with no per-backend mapping |

## MATCH-1 — EmbeddingScorer store fast-path ignores model/dimension (critical)

**Advisor: confirmed end-to-end; genuinely critical during the live re-embed.**

`queryVector` (`embedding_scorer.go:92-96`) returns `store.Get("book", bookID).Vector` with no model or dimension check. Candidates are embedded live with the current client (bge-m3, 1024-dim). During the in-flight re-embed, most of the ~50K stored vectors are still text-embedding-3-large (3072-dim). `CosineSimilarity` returns 0 on `len(a)!=len(b)` (`embedding_store.go:1214`), so `Score` returns all-zero scores with a nil error. `ScoreBaseCandidates` treats that as success (`tier="embedding"`, `service_scoring.go:492-493`) — the F1 fallback never fires — and every candidate is then dropped below `EmbeddingMinScore` (default 0.82) at `service_search.go:334`, and `pickBestMatchFromScored` never reaches `EmbeddingBestMatch` (0.88). Dedup fixed exactly this class with `embeddingModelMatches` (#1738, `engine.go:2110`) but the metadata scorer was not given the same guard.

**Recommendation:** In `queryVector`, only use the stored vector when `existing.Model == client model` (Record already carries Model, `embedding_store.go:95`); otherwise fall through to `EmbedOne`. Defense-in-depth: in `Score`, if `len(qVec) != len(cv)`, return an error (not 0 scores) so `ScoreBaseCandidates` falls back to F1.

**Citations:**
- internal/ai/embedding_scorer.go:92-98
- internal/database/embedding_store.go:1213-1216
- internal/metafetch/service_scoring.go:491-500
- internal/metafetch/service_search.go:330-337
- internal/dedup/engine.go:2110-2115

## MATCH-2 — RerankTopK score-scale mismatch between clamped LLM scores and unclamped tail (high)

**Advisor correction applied:** the original report characterized this as "systematic demotion of reranked winners." The code (`service_scoring.go:647-661`) deliberately avoids re-multiplying; the residual defect is a scale mismatch between clamped [0,1] LLM scores and the unclamped tail — real but conditional, not systematic.

`Candidate.Score` at rerank time carries the full multiplier stack (×1.5 author, ×1.3 narrator, ×1.15 narrator-present, ×2.0 transcription, ×1.3 duration — routinely 1.5–4.0, intentionally unclamped). `RerankTopK` overwrites the top-K scores with LLM scores hard-clamped to [0,1] (`llm_scorer.go:87-92`) and re-sorts the whole list against the untouched tail (`service_scoring.go:658-660`). Any tail candidate scoring >1.0 — common — outranks even an LLM-certain (1.0) reranked winner, undermining the rerank's intent whenever that condition holds. Dormant today (`metadata_scoring.llm_enabled=false` in prod) but this is the first thing the pending local-LLM (qwen2.5:7b-instruct) backend toggle would activate.

**Recommendation:** Rescale LLM scores into the ambiguous window before re-sorting: map LLM [0,1] onto `[candidates[ambiguousEnd-1].Score, bestScore]` (rank-preserving within the window), or re-sort only the top-K slice and keep the tail fixed below it.

**Citations:**
- internal/metafetch/service_scoring.go:646-661
- internal/ai/llm_scorer.go:86-93

## MATCH-3 — IsGarbageValue substring match on "error" rejects legitimate titles/authors (medium)

**Advisor: confirmed verbatim.**

`IsGarbageValue` treats any string containing the substring "error" as garbage (`service_scoring.go:36`) — "terror", "errors", "terrorist" all contain it. Consequences: `hintsFromBook` drops a valid transcribed title like "The Terror" (`service_scoring.go:553`), disabling the transcription boost and the apply-side title gate for that book; `SearchMetadataForBookWithOptions` blanks `bookAuthor`/`bookNarrator` containing the substring (`service_search.go:188-197`), losing author boosts; `IsBetterValue`/`IsBetterStringPtr` (`service_scoring.go:44-65`) permanently refuse to write such titles/authors during metadata apply. Dan Simmons' "The Terror", Shakespeare's "The Comedy of Errors", any "...Terror..." thriller in a 50K library are affected.

**Recommendation:** Restrict the HTML/error heuristic to patterns that can't occur in real titles: match `"<html"`, `"<!doctype"`, and anchored phrases like `"403 forbidden"`, `"error:"` / `"internal server error"` via word-boundary regex, not a bare `Contains("error")`.

**Citations:**
- internal/metafetch/service_scoring.go:35-38
- internal/metafetch/service_scoring.go:547-562
- internal/metafetch/service_search.go:188-197

## MATCH-4 — Per-source scorer fallback can mix embedding-cosine and F1 scores in one ranked list (medium)

`ScoreBaseCandidates` is invoked once per metadata source inside the source loop (`service_search.go:305`). If the embedding scorer succeeds for source A's batch but errors for source B's (Ollama hiccup, timeout — `embedBatchRaw` gives up after 3 attempts), source A candidates carry cosine-based scores (~0.8–1.0 band, threshold 0.82) while source B candidates carry F1 scores (0–1.15 band, threshold 0) — then all are sorted together at `service_search.go:527` and compete for the same top-50. The two scales are not comparable; F1=1.0 exact-token matches beat cosine 0.9 near-certain matches or vice versa depending on the mix. There is no per-candidate tier tag in `MetadataCandidate` to detect this downstream.

**Recommendation:** Score once across all sources' merged results (single tier for the whole search), or record the tier per candidate and, on mixed tiers, re-score the whole set with F1 for comparability.

**Citations:**
- internal/metafetch/service_search.go:305
- internal/metafetch/service_scoring.go:491-507
- internal/metafetch/service_search.go:526-529

## MATCH-5 — Cover filter runs after all scoring and can drop the highest-scored candidate (medium)

After scoring, candidates without `CoverURL` are dropped whenever at least one candidate has a cover (`service_search.go:489-497`). The filter is unconditional on score: a transcription-boosted exact-title match (×2.0) or a direct ASIN lookup (`service_search.go:432-481` — Audnexus records frequently lack `CoverURL`) is silently removed while a low-scored wrong-book candidate with a cover survives and becomes the top result. For pipelines that auto-apply candidate[0] (auto-match-transcribed reads cached candidates[0]), this converts a cosmetic preference into a wrong-match driver.

**Recommendation:** Exempt strong-evidence candidates from the cover filter: keep coverless candidates that are TranscriptionBoosted, came from direct ASIN lookup, or score within epsilon of the best; or demote coverless (×0.9) instead of deleting.

**Citations:**
- internal/metafetch/service_search.go:487-497
- internal/metafetch/service_search.go:441-481

## MATCH-6 — Auto-match apply path re-reads cache candidate[0] independently of the gated search snapshot (medium)

`SearchTranscriptionCandidate` ignores its transcribed title/author arguments and returns cached candidates[0] (`server_maintenance_deps.go:354-370`); `runAutoMatchTranscribed` applies gates 1–3 to that snapshot, then calls `ApplyTranscriptionCandidate` — which also ignores its `candTitle`/`candAuthor` arguments and re-reads cache candidates[0] (`server_maintenance_deps.go:382-393`). If the candidate cache for that book is refreshed between the two reads (concurrent metadata fetch op, UI-triggered search), a different, never-gated candidate is applied. `ApplyMetadataCandidate`'s own audio-confirm check reduces but does not eliminate the exposure (it gates title, not the score/author gates). Also gate 3 uses raw `TranscribedAuthor` without `IsGarbageValue` (`auto_match_transcribed.go:158`), so garbage like "unknown" rejects every candidate (conservative, but silently zeroes eligibility).

**Recommendation:** Pass the gated candidate (or its title+author+score identity) into `ApplyTranscriptionCandidate` and verify it still equals cache candidates[0] before applying; run `transAuthor` through `IsGarbageValue` before gate 3.

**Citations:**
- internal/server/server_maintenance_deps.go:354-371
- internal/server/server_maintenance_deps.go:378-395
- internal/plugins/maintenance/auto_match_transcribed.go:130-176

## MATCH-7 — No permanent-error classification in retry paths (medium)

`DoWithRetry` (`retry.go:31-44`) and `embedBatchRaw` (`embedding_client.go:287-310`) retry every error uniformly: with OpenAI quota-exhausted, each LLM/batch/embedding call to OpenAI burns 3 attempts plus 1s+4s (embed) or quadratic (`DoWithRetry`) backoff before failing, and nothing distinguishes "quota dead, switch backends" from "transient network blip." The proposed fallback backend mode (openai↔local) has no signal to trigger on; today the only availability gate is the boolean `localOllamaOK` for the local path (`embedding_client.go:193`). This also inflates wall time for every scorer failure that cascades to F1 fallback per source (see MATCH-4).

**Recommendation:** Add `IsPermanentAPIError(err)` (429 insufficient_quota, 401/403, 404 model-not-found via openai-go's APIError status/code) — skip retries and return a typed error the backend selector can use to fail over and set a cooldown health flag.

**Citations:**
- internal/ai/retry.go:26-47
- internal/ai/embedding_client.go:287-330

## MATCH-8 — embedclient and llmparser registrations gate on OpenAIAPIKey even for keyless local Ollama (medium)

`embedclient` returns nil when `cfg.OpenAIAPIKey` is empty (`register.go:35`), even when `Embedding.BaseURL` points at local Ollama, which requires no key. If the operator deletes the quota-dead OpenAI key, local embeddings silently disable (nil client → nil metadatascorer → silent F1 downgrade, no warning). Conversely, `llmparser` (`register.go:65-68`) is hardcoded to the OpenAI default endpoint with no base_url override at all — the LLM tier (rerank, dedup Layer-3 review, metadata LLM review) cannot use the local qwen2.5:7b-instruct backend and will 429 against OpenAI until the backend-mode toggle exists. The `server.go:621-624` trust fix (#1739/#1740) only gates availability, not construction.

**Recommendation:** Relax the key gate: construct the embedding client when BaseURL is set regardless of key (pass a dummy key — Ollama ignores it). Give the LLM parser its own base_url/model config as part of the backend-mode design; log loudly when an AI tier is disabled by missing config.

**Citations:**
- internal/ai/register.go:35-49
- internal/ai/register.go:65-68
- internal/server/server.go:621-624

## MATCH-9 — LevenshteinDistance operates on bytes, inflating distances for non-ASCII titles (low)

`LevenshteinDistance` indexes `a[i-1]`/`b[j-1]` as bytes and uses byte `len()` (`fuzzy.go:23,40`), while `normalize` deliberately preserves all Unicode letters/digits (`fuzzy.go:144`). Accented or CJK characters are 2–4 bytes, so one substituted character counts as up to 4 edits and maxLen is byte-inflated — similarity scores for titles/authors like "Émile Zola" or Japanese titles are skewed low and asymmetric versus their ASCII-folded forms. The substring ratio at `fuzzy.go:77` has the same byte-length bias. Bounded impact: fuzzy scores feed 50–70-point tiers, so ASCII-dominant libraries are unaffected.

**Recommendation:** Convert to `[]rune` before the DP loop and use rune counts for all length ratios; optionally fold diacritics in normalize (`golang.org/x/text/unicode/norm`) for accent-insensitive matching.

**Citations:**
- internal/matcher/fuzzy.go:19-48
- internal/matcher/fuzzy.go:77
- internal/matcher/fuzzy.go:140-149

## TOGGLE-1 — Local Ollama embedding still requires a non-empty OpenAIAPIKey (high)

**Advisor: confirmed verbatim.**

`embedclient` Build returns nil when `cfg.OpenAIAPIKey==""` even if `Embedding.BaseURL` points at Ollama, which needs no key (`register.go:35`). `llmparser` has the same gate (`register.go:65`). With OpenAI quota-exhausted, the system only works because a now-useless OpenAI key is still configured; removing it would disable the local backend entirely. There is no mode concept — key presence is the de-facto backend selector.

**Recommendation:** Backend-mode factories must decouple client construction from `OpenAIAPIKey`: in local mode pass a placeholder key (Ollama ignores Authorization) and require only `LocalBaseURL`; require the real key only for openai/fallback modes.

**Citations:**
- internal/ai/register.go:35
- internal/ai/register.go:65

## TOGGLE-2 — EmbedBooksAsync (OpenAI Batch API) not gated on backend; skip-check ignores embedding model (high)

`EmbedBooksAsync` submits to `/v1/batches` via `CreateEmbeddingBatch` with no check that the client's baseURL is real OpenAI — Ollama does not implement the Batch API, so async embed-scan under local mode fails at runtime (or worse, would bill OpenAI if the SDK falls through to the default host). Separately, its already-embedded skip at `engine.go:2307` checks only TextHash equality, unlike `prepBookEmbed` (`engine.go:2094`) which also calls `embeddingModelMatches` — so the async path silently skips books carrying stale 3072-dim text-embedding-3-large vectors during the bge-m3 cutover, the exact bug #1738 fixed on the sync path.

**Recommendation:** Gate all `openai_batch.go`/`embedding_batch.go` entry points on effective backend==openai (typed `ErrBatchUnsupported`; embed-scan async downgrades to sync). Add `embeddingModelMatches` to the `EmbedBooksAsync` skip-check.

**Citations:**
- internal/dedup/engine.go:2307
- internal/dedup/engine.go:2318
- internal/plugins/dedup/embed_scan.go:77
- internal/ai/embedding_batch.go:32

## TOGGLE-3 — No per-config base URL for the LLM path (high)

`NewOpenAIParser` honors only the process-wide `OPENAI_BASE_URL` env (`openai_parser.go:85`). The embedding client deliberately avoids that env because it redirects ALL default clients (`embedding_client.go:107-112` comment), but the parser still uses it — so pointing the LLM at Ollama today would also redirect any client built without an explicit base URL, and there is no config field for an LLM endpoint at all. `LLMMode=local` for qwen2.5:7b-instruct is fully greenfield.

**Recommendation:** Add `NewOpenAIParserWithBaseURL` taking an explicit per-client base URL + model from `AIBackendConfig` (`LocalBaseURL`/`LocalLLMModel`), mirroring the embedding-client pattern; deprecate `OPENAI_BASE_URL` env reliance in the parser.

**Citations:**
- internal/ai/openai_parser.go:85
- internal/ai/embedding_client.go:107

## TOGGLE-4 — retry.go retries 429/insufficient_quota blindly; no classification, no fallback hook (medium)

`DoWithRetry` treats every error identically: quota-exhausted (429 insufficient_quota) and auth failures (401) are permanent but still get maxAttempts with quadratic backoff, wasting minutes per batch during the current OpenAI outage, and there is no signal a caller could use to trigger openai→local fallback. The fallback mode in the toggle design needs a permanent/transient classification here.

**Recommendation:** Add an error classifier (openai SDK `*APIError`: 401 + 429/insufficient_quota = permanent, 5xx/timeouts = transient). `DoWithRetry` short-circuits permanent errors; fallback-mode clients switch backend on permanent classification and set a sticky flag exposed via the backend-status endpoint.

**Citations:**
- internal/ai/retry.go:26
- internal/ai/retry.go:40

## TOGGLE-5 — Ollama availability is a one-shot startup trust with a non-atomic flag (medium)

`server.go:616-625` sets availability once at startup: binary-on-PATH probe OR unconditional trust of a non-empty base_url (#1739). A configured-but-down endpoint is "available" forever; a recovered endpoint after startup-probe failure stays blocked. `localOllamaOK` is a plain bool read by every `EmbedBatch` (`embedding_client.go:193`) and written by `SetOllamaAvailable` — a runtime settings toggle (the whole point of the backend-mode feature) would introduce a data race.

**Recommendation:** Replace with an HTTP probe (`GET {base}/api/tags`, 2s timeout) cached with TTL and re-probed on failure; store availability in `atomic.Bool`. Surface probe result in the new backends-status API so the FE selector can show live health.

**Citations:**
- internal/server/server.go:616
- internal/ai/embedding_client.go:82
- internal/ai/embedding_client.go:141

## TOGGLE-6 — metadatallmscorer built regardless of MetadataScoring.LLMEnabled (low)

The registry constructs `LLMScorer` whenever `llmparser` exists (`register.go:94-105`) without consulting `MetadataScoring.LLMEnabled`; the only enforcement is the runtime check at `service_search.go:537`. Any future consumer of the `metadatallmscorer` service bypasses the prod `llm_enabled=false` setting. The mode enum should become the single source of truth (`LLMMode=disabled` ⇒ nil scorer), matching how `metadatascorer` checks `EmbeddingEnabled` at build (`register.go:80`).

**Recommendation:** In the toggle implementation, have the `metadatallmscorer` Build return nil when effective `LLMMode==disabled` (and keep the call-site check for per-request opts), so config disablement is enforced at construction like the embedding scorer.

**Citations:**
- internal/ai/register.go:94
- internal/metafetch/service_search.go:537

## TOGGLE-7 — Model names are OpenAI-specific defaults with no per-backend mapping (low severity, medium impact)

Dedup `ReviewModel` defaults to "gpt-5-mini" (`config.go:137`), `OpenAIParser` defaultModel is "gpt-5-mini" (`openai_parser.go:51`), and `defaultEmbeddingModel` is "text-embedding-3-large" (`embedding_client.go:92`). Under local mode these names are sent verbatim to Ollama and 404. A mode toggle that only swaps base URLs will break every LLM feature (filename parse, cover art, metadata review, dedup Layer-3) unless model selection is backend-aware.

**Recommendation:** Resolve the effective model per backend at client-build time: local mode substitutes `LocalLLMModel`/`LocalEmbeddingModel` for all per-feature model fields (or exposes per-feature overrides later). The backends-status endpoint should verify the resolved model exists in Ollama `/api/tags` — this is the hook for the model-download prompt.

**Citations:**
- internal/config/config.go:137
- internal/ai/openai_parser.go:51
- internal/ai/embedding_client.go:92

## Design: LLM/Embedding Backend-Mode Toggle

Two design contributions were produced. The primary (from the backend-toggle specialist) follows; the matching specialist's sketch, which converges on the same architecture with a few additional wiring details, is appended after it.

### Primary design (TOGGLE report)

**Config shape** — new nested struct in the CFG blob:
`AIBackendConfig { EmbeddingMode, LLMMode string; LocalBaseURL, LocalEmbeddingModel, LocalLLMModel string }`, modes: `disabled | openai | local | openai-fallback-local`, independent per subsystem. Local defaults: bge-m3 / qwen2.5:7b-instruct, base `http://<gpu-host>:11434/v1`.

**Migration** (startup blob migration in persistence.go, plus legacy-key API shim per docs/reference/config-api-shape.md): if `EmbeddingMode` empty → `local` when `Embedding.BaseURL≠""` (copy to `LocalBaseURL`), else `openai` when `OpenAIAPIKey≠""&&Embedding.Enabled`, else `disabled`. `LLMMode` → `openai` when `OpenAIAPIKey≠""&&(EnableAIParsing||MetadataScoring.LLMEnabled)`, else `disabled`. Old fields remain readable shims; write-through both ways for one release.

**Runtime per mode** — register.go factories key off effective mode, not `OpenAIAPIKey`: local mode uses placeholder API key ("ollama") so no OpenAI key is required; openai mode ignores base_url. LLM gets a second constructor `NewOpenAIParserWithBaseURL` (per-client option, mirroring `embedding_client.go:116-123` — never `OPENAI_BASE_URL` env). Availability: replace LookPath/trust-hack with HTTP probe `GET {LocalBaseURL}/api/tags` (2s timeout), TTL-cached + re-probed on failure; store in `atomic.Bool` replacing `localOllamaOK`. Fallback (`openai-fallback-local`): classify errors in retry.go — 401/429-insufficient_quota = permanent → flip a sticky "openai-down" flag and route to local; 5xx/timeout = retry then per-request fallback. **Embeddings fallback is NOT per-request**: switching model changes dimensions (bge-m3 1024 vs 3-large 3072; CosineSimilarity→0, HNSW dim mismatch), so embedding fallback resolves once at client build/probe time, tags vectors with the actual model, and raises a "model changed — re-embed required" event (banner + one-click dedup.embed-scan); `embeddingModelMatches` + (hash,model) cache keys already make re-embed safe. **OpenAI-only paths** (openai_batch.go, `CreateEmbeddingBatch`, `EmbedBooksAsync`, `BatchPoller`) return typed `ErrBatchUnsupported` unless effective backend==openai; embed-scan async=true downgrades to sync with a log.

**FE** — "AI Backends" card in Settings (extend EmbeddingSettingsSection/MetadataScoringSection): two mode selects, local endpoint/model fields, Test Connection → new `GET /api/v1/ai/backends/status` (probes endpoint, lists pulled models via `/api/tags`, reports effective mode + last fallback reason).

**Model-download prompt** — when local selected and status shows model absent: dialog "qwen2.5:7b-instruct not pulled (4.7GB) — Pull now?" → `POST /api/v1/ai/backends/pull-model`, runs `ollama pull` through the existing managed external-tool lifecycle (managed Ollama PIDFile/port wiring at `server.go:595`; TODO "Managed External-Tool Lifecycle"), streaming progress as an op-registry operation; mode stays pending-unavailable until probe passes.

### Companion sketch (MATCH report)

**LLM/embedding backend-mode toggle (greenfield — no BackendMode symbol exists).**

Config: two independent enums, `embedding.backend_mode` and `llm.backend_mode` ∈ {`disabled`, `openai`, `local`, `local_with_openai_fallback`, `openai_with_local_fallback`} (covers "disable-all/OpenAI-only/local-only/fallback"). Per-backend blocks: `{base_url, model, api_key}` — local needs no key (fixes register.go:35 gate); local LLM target `qwen2.5:7b-instruct` at <gpu-host>:11434/v1.

Wiring: in register.go, replace the single `embedclient`/`llmparser` builders with a `BackendSelector` that constructs up to two `*EmbeddingClient`s (each pinned to its own model+baseURL, reusing `NewEmbeddingClientWithOptions`) and two chat clients. `Model()` already flows into cache keys (`embedding_client.go:212`) and dedup's `embeddingModelMatches` (`engine.go:2110-2115`) — fallback switching automatically forces model-tagged re-embeds and cache partitioning; no new tagging needed, but `EmbeddingScorer` must gain the same model check on its store fast-path (see MATCH-1) so a mid-fallback flip can't mix 3072/1024-dim vectors (CosineSimilarity→0, `embedding_store.go:1214`).

Fallback trigger: add error classification in `internal/ai/retry.go` — `IsPermanent(err)`: HTTP 429 `insufficient_quota`, 401/403, 404 model-not-found → do NOT retry (today `embedBatchRaw` retries these 3× at `embedding_client.go:287-310`); instead return a typed `ErrBackendUnavailable{Backend}` that the selector catches to switch to the fallback backend, with a cooldown/health flag like `localOllamaOK` (`embedding_client.go:82`). Transient errors keep current retry behavior.

OpenAI-only paths: `OpenAIParser.Batches.*` (`openai_batch.go:74,214,376`) and batch-file endpoints have no Ollama equivalent — under `local`/local-primary modes these must return a typed `ErrNotSupportedByBackend` (no-op with clear log), and EmbedBooksAsync/batch-submit UI actions should be hidden/disabled. Dedup Layer-3 review and metadata rerank go through the selected chat backend instead of the hardcoded OpenAI parser (`register.go:59-70` currently gates on `OpenAIAPIKey` only).

Availability gating: keep `server.go:616-624` trust-the-configured-base_url logic per backend; `disabled` mode short-circuits before any client construction so nil-scorer paths (metafetch F1 fallback, `service_scoring.go:470-507`) engage cleanly.

Migration: default mode derived from existing config (base_url set → `local`, else key set → `openai`) so no prod config change is required at deploy.

### Reconciling the two designs

Both converge on: independent embedding/LLM mode enums, keyless local client construction, per-client base URLs (never `OPENAI_BASE_URL`), error classification in retry.go driving fallback, typed errors gating OpenAI-only batch paths, and reuse of `embeddingModelMatches` + (hash,model) cache keys for re-embed safety. Differences to resolve at implementation time: (1) mode-enum vocabulary (primary uses `openai-fallback-local`; sketch adds `local_with_openai_fallback` — support both directions); (2) availability probing — the primary design's HTTP probe + atomic.Bool supersedes the sketch's keep-the-trust-hack; (3) the primary design's insight that **embedding fallback must resolve at build/probe time, not per-request** (dimension mismatch) should be treated as a hard constraint on either enum set.
