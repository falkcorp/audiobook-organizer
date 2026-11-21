// file: web/src/components/audiobooks/ownerRejected.ts
// version: 1.0.0
// guid: 9b4e7d21-3a6c-4f58-8e10-7c2d5b9a6f13
// last-edited: 2026-10-10

import type { MetadataCandidate } from '../../services/api';

/** candidate with its owner-rejected marker cleared (after an Undo). */
export function clearOwnerRejected(candidate: MetadataCandidate): MetadataCandidate {
  return candidate.apply_check
    ? { ...candidate, apply_check: { ...candidate.apply_check, owner_rejected: false } }
    : candidate;
}

/**
 * The result list after the server answered an Undo for `unrejected`.
 *
 * The server's Undo removes the candidate's own rejection and also a
 * rejection of the same ASIN under another source, so every other row with
 * the same non-empty ASIN stops being owner-rejected with it. A row with no
 * ASIN matches only itself. With `stillRejected` the clicked row keeps its
 * marker (another rejection still refuses it) while its siblings are cleared.
 *
 * The sibling clear is a client-side inference: a sibling can itself still be
 * refused through a shared ISBN. Applying such a row is refused server-side
 * with the reason, and the next search shows the truth.
 */
export function clearUnrejected(
  results: MetadataCandidate[],
  unrejected: MetadataCandidate,
  opts: { stillRejected?: boolean } = {}
): MetadataCandidate[] {
  const asin = unrejected.asin;
  return results.map((r) => {
    if (r === unrejected) {
      return opts.stillRejected ? r : clearOwnerRejected(r);
    }
    return asin && r.asin === asin ? clearOwnerRejected(r) : r;
  });
}
