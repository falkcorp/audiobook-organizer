// file: web/src/lib/reviewPayload.ts
// version: 1.1.0
// guid: 2f9c7b41-6d38-4a05-8e17-0b3d5c9a2e68
// last-edited: 2026-08-06

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

/** RecommendationEvidence mirrors itunesservice.RecommendationEvidence — the
 *  arithmetic behind a recommendation, so a reviewer can CHECK the machine instead
 *  of trusting it. Every field is optional here because a hold written before
 *  2026-08-06 carries no evidence block at all. */
export interface RecommendationEvidence {
  members?: number;
  durationsKnown?: number;
  bookLengthMembers?: number;
  medianKnownSec?: number;
  longestKnownSec?: number;
  distinctStems?: number;
  numberedMembers?: number;
  structure?: string;
}

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
  /** What the classifier thinks a human should DO — a REVIEW_ACTION key. Absent on
   *  every hold written before 2026-08-06; the backend reads that absence as
   *  "insufficient-evidence" and refuses to approve without an explicit choice. */
  recommendedAction?: string;
  /** The one-sentence case for recommendedAction, quoting its own numbers. This is
   *  what replaces proposedAction, which was the SAME generic string on 762 of 777
   *  holds. */
  recommendationReason?: string;
  recommendationEvidence?: RecommendationEvidence;
  [k: string]: unknown;
}

/** The closed action vocabulary, mirroring internal/itunes/service's Action*
 *  constants. `approvable: false` means the backend deliberately 400s it — see
 *  INSUFFICIENT_EVIDENCE below. */
export interface ReviewActionSpec {
  /** The wire value sent as {"action": ...}. */
  value: string;
  label: string;
  /** One line explaining what picking this DOES, shown next to the choice. */
  description: string;
  /** Whether a human may approve with it. */
  approvable: boolean;
  /** True when the backend accepts the choice but has no implementation and answers
   *  501. Offered anyway — hiding it would misrepresent the vocabulary, and faking
   *  success would mark a hold decided while doing nothing. */
  unimplemented?: boolean;
  /** True when the action rewrites library rows (and, for combine, hard-deletes the
   *  absorbed ones). Drives the destructive-action styling. */
  destructive?: boolean;
}

export const ACTION_INSUFFICIENT_EVIDENCE = 'insufficient-evidence';

export const REVIEW_ACTIONS: ReviewActionSpec[] = [
  {
    value: 'combine',
    label: 'Combine',
    description: 'These files are parts of ONE book — merge them into a single audiobook.',
    approvable: true,
    destructive: true,
  },
  {
    value: 'separate',
    label: 'Keep separate',
    description:
      'These are already distinct books — leave them apart. Nothing is merged or deleted; ' +
      'the folder is just marked decided so future scans skip it.',
    approvable: true,
  },
  {
    value: 'version-group',
    label: 'Link as editions',
    description:
      'Different editions of one work (abridged / unabridged). Links them as a version ' +
      'group; no file or book row is destroyed.',
    approvable: true,
  },
  {
    value: 'duplicate-of',
    label: 'Duplicate of an existing book',
    description:
      'Not implemented yet — the duplicate-detection track owns it. Choosing this returns ' +
      'an error rather than silently marking the hold decided.',
    approvable: true,
    unimplemented: true,
  },
  {
    value: ACTION_INSUFFICIENT_EVIDENCE,
    label: 'Not enough evidence',
    description:
      'A statement BY the classifier, not a choice: it cannot tell what these files are. ' +
      'A human has to decide.',
    approvable: false,
  },
];

const ACTION_BY_VALUE = new Map(REVIEW_ACTIONS.map((a) => [a.value, a]));

export function actionSpec(value: string | undefined): ReviewActionSpec | undefined {
  return value ? ACTION_BY_VALUE.get(value) : undefined;
}

/** labelForAction renders an action for display, falling back to the raw string so a
 *  vocabulary the backend grows before the UI does never shows a blank. */
export function labelForAction(value: string | undefined): string {
  if (!value) return '';
  return ACTION_BY_VALUE.get(value)?.label ?? value;
}

/** defaultActionFor is what the per-item selector starts on.
 *
 *  🔴 IT IS EMPTY WHEN THE RECOMMENDATION IS NOT APPROVABLE. `insufficient-evidence`
 *  is every hold currently in prod's queue and ~70 of 356 even after a re-scan; the
 *  backend 400s it on purpose. Pre-selecting anything for those holds would either
 *  guess `combine` — a merge nobody chose, on precisely the holds with the least
 *  evidence — or hand the reviewer a button that always fails. Empty means the UI
 *  must ask. */
export function defaultActionFor(payload: ReviewPayload | null): string {
  const rec = payload?.recommendedAction;
  const spec = actionSpec(rec);
  return spec?.approvable ? spec.value : '';
}

/** humanRuntime mirrors the Go reason sentences' formatting (hours for a book,
 *  minutes for a chapter) so the chips and the reason never disagree about a number. */
export function humanRuntime(sec: number | undefined): string {
  if (!sec || sec <= 0) return '—';
  if (sec >= 3600) return `${(sec / 3600).toFixed(1)} h`;
  return `${Math.round(sec / 60)} min`;
}

export interface EvidenceFact {
  label: string;
  value: string;
  /** Longer explanation for a tooltip — what the number means, not just its name. */
  hint: string;
  /** True when this fact is the reason a recommendation could not be decisive. */
  warn?: boolean;
}

/** evidenceFacts turns the evidence block into the chips a reviewer reads.
 *
 *  Returns [] when there is no evidence at all (a pre-2026-08-06 hold), so the UI can
 *  say "no evidence recorded" rather than render a row of confident-looking zeros. */
export function evidenceFacts(ev: RecommendationEvidence | undefined): EvidenceFact[] {
  if (!ev || typeof ev.members !== 'number' || ev.members <= 0) return [];
  const members = ev.members;
  const known = ev.durationsKnown ?? 0;
  const bookLength = ev.bookLengthMembers ?? 0;
  const facts: EvidenceFact[] = [
    {
      label: `${members} member${members === 1 ? '' : 's'}`,
      value: '',
      hint: 'Files grouped into this hold.',
    },
    {
      // The gap between known and members is the single most important number here:
      // an absent runtime is not evidence, and a majority of unknowns is exactly why
      // a recommendation lands on insufficient-evidence.
      label: `${known}/${members} runtimes known`,
      value: '',
      hint:
        'Members with a real duration. An absent duration is never treated as evidence, ' +
        'so a group where most runtimes are unknown cannot get a decisive recommendation.',
      warn: known * 2 <= members,
    },
    {
      label: `${bookLength} book-length`,
      value: '',
      hint:
        'Members running 90 minutes or more — long enough to BE a book on their own. ' +
        'A majority here means these are probably separate novels, not chapters.',
    },
    {
      label: `median ${humanRuntime(ev.medianKnownSec)}`,
      value: '',
      hint: 'Median runtime across members with a known duration (unknowns excluded).',
    },
    {
      label: `longest ${humanRuntime(ev.longestKnownSec)}`,
      value: '',
      hint: 'The longest single member. "15.8 h" reads as "that is a novel" on its own.',
    },
  ];
  if (typeof ev.distinctStems === 'number') {
    facts.push({
      label: `${ev.distinctStems} distinct title${ev.distinctStems === 1 ? '' : 's'}`,
      value: '',
      hint:
        'Distinct title stems among the members — the anthology / over-merge signal. ' +
        'Many distinct titles in one folder means combining would fuse different works.',
    });
  }
  if (typeof ev.numberedMembers === 'number' && ev.numberedMembers > 0) {
    facts.push({
      label: `${ev.numberedMembers} numbered`,
      value: '',
      hint: 'Members carrying a parseable chapter or track ordinal.',
    });
  }
  if (ev.structure) {
    facts.push({
      label: ev.structure,
      value: '',
      hint: 'The group\'s dominant physical shape on disk: disc set, chapter run, or flat folder.',
    });
  }
  return facts;
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
