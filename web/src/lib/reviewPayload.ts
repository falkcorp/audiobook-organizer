// file: web/src/lib/reviewPayload.ts
// version: 1.0.0
// guid: 2f9c7b41-6d38-4a05-8e17-0b3d5c9a2e68
// last-edited: 2026-07-26

// Parsing + shaping for a review-queue item's JSON payload. Kept out of the
// ReviewQueue component file so these pure helpers are unit-testable and don't trip
// react-refresh's "component files should only export components" rule.
//
// The producer (internal/plugins/maintenance/regroup_shattered_ai.go,
// buildRegroupPayload) writes camelCase keys: folder, files, proposedAction,
// memberBookIDs, survivorTitle, confidence ("high" | "review"), and — for confident
// multidisc collapses — the parallel discNumbers / trackNumbers arrays (index-aligned
// with files/memberBookIDs). Snake_case aliases are kept as defensive fallbacks in
// case a future producer emits a different shape; render only what is present.

export interface ReviewPayload {
  folder?: string;
  proposedAction?: string;
  proposed_action?: string;
  survivorTitle?: string;
  derived_title?: string;
  title?: string;
  memberBookIDs?: string[];
  member_ids?: string[];
  member_count?: number;
  confidence?: string | number;
  files?: string[];
  discNumbers?: number[];
  trackNumbers?: number[];
  [k: string]: unknown;
}

export function parsePayload(raw: string): ReviewPayload | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as ReviewPayload) : null;
  } catch {
    return null;
  }
}

/** memberIDs returns the member book IDs, tolerating the producer's camelCase key
 *  and the older snake_case alias. */
export function memberIDs(payload: ReviewPayload | null): string[] {
  if (!payload) return [];
  if (Array.isArray(payload.memberBookIDs)) return payload.memberBookIDs;
  if (Array.isArray(payload.member_ids)) return payload.member_ids;
  return [];
}

export function memberCount(payload: ReviewPayload | null): number | undefined {
  if (!payload) return undefined;
  if (typeof payload.member_count === 'number') return payload.member_count;
  const ids = memberIDs(payload);
  if (ids.length > 0) return ids.length;
  if (Array.isArray(payload.files)) return payload.files.length;
  return undefined;
}

// A single member of a review group: its original file path, the (still-present)
// source book ID, and the play-order the classifier proposes to write on approve.
export interface MemberEntry {
  filePath: string;
  bookId?: string;
  disc?: number;
  track?: number;
}

/** memberEntries zips the payload's parallel arrays (files / memberBookIDs /
 *  discNumbers / trackNumbers) into per-member records. Files is the spine; the
 *  other arrays are optional and index-aligned. */
export function memberEntries(payload: ReviewPayload | null): MemberEntry[] {
  if (!payload) return [];
  const files = Array.isArray(payload.files) ? payload.files : [];
  const ids = memberIDs(payload);
  const discs = Array.isArray(payload.discNumbers) ? payload.discNumbers : [];
  const tracks = Array.isArray(payload.trackNumbers) ? payload.trackNumbers : [];
  // Fall back to the member-ID list as the spine when no file paths were recorded.
  const n = Math.max(files.length, ids.length);
  const out: MemberEntry[] = [];
  for (let i = 0; i < n; i++) {
    out.push({
      filePath: files[i] ?? '',
      bookId: ids[i],
      disc: discs[i],
      track: tracks[i],
    });
  }
  return out;
}
