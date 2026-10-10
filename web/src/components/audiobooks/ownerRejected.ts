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
