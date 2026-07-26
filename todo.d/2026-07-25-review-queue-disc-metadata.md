- [ ] **REVIEW-QUEUE-CARDS** Bring the dedup tab's rich per-file compare view to
      the review-queue cards. Today "Multi-disc"/"Ambiguous folder" holds render
      only bare file-path strings (`ReviewQueue.tsx` `PayloadDetails`) — not enough
      to make an informed approve/reject call. Generalize dedup's
      `FileInfoCompare`/`BookFilesColumn` to a provider-agnostic shape, extract the
      shared `formatBytes`/`formatDuration`/quality-chip helpers out of
      `UnifiedDedupTab.tsx`, and render per-file rows (duration/format/bitrate/size/
      disc+track) enriched client-side via `getBook()` over `member_ids`. Groups can
      have >2 files, so use per-file rows, not a strict pairwise A/B layout. Also
      surface the proposed disc/track the classifier now emits (so a same-disc
      "Multi-disc" set visibly shows disc 0 + tracks 1..N, replacing the misleading
      "Multi-disc" label for that case).
- [ ] **REVIEW-QUEUE-PAYLOAD-KEY** Fix the `memberBookIDs` (payload, camelCase) vs
      `member_ids`/`member_count` (frontend `ReviewQueue.tsx`, snake_case) key
      mismatch — `memberCount()` silently falls back to `files.length` today. Do this
      as part of REVIEW-QUEUE-CARDS.
- [ ] **REGROUP-PARTCHAPTER-PARSER** The Mistborn-style "Ambiguous folder" case
      (`01 P0-C0.mp3`, `07 P1-C6.mp3` — Part/Chapter naming, non-contiguous numbers)
      has no parser and stays classified as ambiguous (unaffected by the disc/track
      fix). Consider a Part→disc / Chapter→track parser as a fast-follow so these
      collapse with correct numbering too.
