<!-- file: changelog.d/review-queue-rich-cards.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e8b1d92-5a47-4c60-9f21-6d0a4c7e2b83 -->
<!-- last-edited: 2026-07-26 -->

### Changed

#### Review-queue cards now show rich per-file metadata (and read the real payload)

Review-queue cards ("Multi-disc", "Ambiguous folder", …) previously rendered only a
bare list of file-path strings — not enough to make an informed approve/reject call.
Expanding a card now shows a per-member row for each file with cover art, title,
author, series/position, format, duration, size, bitrate, codec, and the **proposed
disc/track** the classifier will write on approve (so a same-disc chapter set visibly
shows disc 0 + tracks 1..N, and a real disc set shows its true disc numbers). Member
metadata is fetched client-side via `getBook` in bounded batches, lazily on expand, so
opening the queue never fans out hundreds of requests up front.

Also fixes a latent payload-key mismatch: the card reader used snake_case keys
(`proposed_action`, `member_ids`, `derived_title`) and expected `confidence` as a
number, but the Go producer (`buildRegroupPayload`) emits camelCase
(`proposedAction`, `memberBookIDs`, `survivorTitle`) with `confidence` as a string —
so previously only the folder and raw file list rendered, and the member count
silently fell back to the file-array length. The payload parsing/zipping now lives in
a unit-tested `web/src/lib/reviewPayload.ts` (camelCase primary, snake_case fallback),
and the byte-size/duration formatters were extracted to a shared
`web/src/utils/mediaFormat.ts` reused by both the review cards and the dedup compare
view.
