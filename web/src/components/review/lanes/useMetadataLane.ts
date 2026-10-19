// file: web/src/components/review/lanes/useMetadataLane.ts
// version: 1.45.0
// guid: 7c4e1a90-3b58-4d26-9a07-1e5a8b2c4f70
// last-edited: 2026-10-19
//
// The metadata lane's data layer, LIFTED out of MetadataReviewDialog.
//
// Same discipline as spine/rowState.ts: this is a move, not a rewrite. The
// dialog accumulated 27 state hooks, a filter chain, a grouping pass, a
// client-side paginator and a debounced apply pipeline, and every one of those
// encodes a decision somebody made for a reason. Retyping them from a reading is
// how a port loses the reasons.
//
// WHY ONE HOOK RATHER THAN STATE PER COMPONENT
//
// The derivations form a chain, and every link depends on both the filters and
// `rowStates`:
//
//   filters -> server page (+ facets) -> visible rows -> multiGroups
//           + { highConfidenceIds, ... }
//
// Split that across a component boundary and the halves either recompute the
// chain or prop-drill it. So the chain stays here and callers get slices: the
// rail takes `pageResults`, the spine takes `spineCtx`, the action bar takes the
// id sets. Nobody downstream re-derives anything. The server filters and pages
// (view=page); the lane holds one page of rows, never the library.
//
// WHAT IS DELIBERATELY NOT HERE
//
// `hasChangesRef` and `handleClose` do not survive the lift. They exist because
// a dialog closes and has one moment to tell the library to refresh; a route
// does not close, so there is no such moment. The workspace refreshes the
// library when an apply operation actually finishes instead -- strictly more
// accurate, since the dialog's version fired on close whether or not the
// background op had done anything yet. docs/port-inventory.md records this as a
// deliberate drop rather than leaving the row to rot.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  ApplyPin,
  BulkApplyMode,
  CandidatePin,
  CandidateResult,
  MetadataCandidate,
} from '../../../services/api';
import * as api from '../../../services/api';
import { isAbortError, isAuthRedirectError } from '../../../utils/apiFetch';
import { STORAGE_KEYS } from '../../../lib/storageKeys';
import type { CandidateGroup, SpineContext } from '../spine/CompareSpine';
import type { CandidateDetailState } from '../evidence/adapters';
import type { RowState } from '../spine/rowState';
import type { MetadataAction } from '../reviewActions';
import { compileTitleFilter } from '../../../utils/queryGrammar';

// Upper bound on how long a dispatched apply keeps its rows protected from
// reconciliation. See runApplyOp for why this is bounded at all.
const APPLY_INFLIGHT_MAX_MS = 60 * 60 * 1000;

/**
 * The pin for the candidate a row is showing. Every apply button on the review
 * page IS the owner's review (owner ruling 2026-09-27), and the pin is how the
 * server knows the candidate it applies is the one that was shown (see
 * api.CandidatePin). contentHash is the row's server-issued candidate_hash;
 * the identity fields ride along so a refusal can name what changed. origin
 * is 'row' for the single-row Apply and 'review_bulk' for the bulk buttons.
 */
export function pinOfCandidate(
  c: MetadataCandidate,
  contentHash: string,
  origin: CandidatePin['origin'] = 'row'
): CandidatePin {
  const pin: CandidatePin = {
    origin,
    content_hash: contentHash,
    source: c.source,
    title: c.title,
  };
  if (c.author) pin.author = c.author;
  if (c.asin) pin.asin = c.asin;
  if (c.isbn) pin.isbn = c.isbn;
  if (c.isbn10) pin.isbn10 = c.isbn10;
  if (c.isbn13) pin.isbn13 = c.isbn13;
  return pin;
}

/**
 * Ids per request when a bulk action spans more books than one request should
 * carry. The server's JSON body limit defaults to 1 MB (json_body_limit_mb),
 * and bulk apply is additionally refused above bulk_apply_max_items (default
 * 5,000, applycap). An apply request carries a pin per book (a few hundred
 * bytes each), so 500 keeps it far under both; a fetch request carries bare
 * ids (~40 bytes), so 1,000 is ~40 KB.
 */
export const APPLY_CHUNK_SIZE = 500;
export const FETCH_CHUNK_SIZE = 1000;
/** Concurrent per-book requests (reject, clear no-match): no bulk endpoint exists. */
export const PER_BOOK_CONCURRENCY = 4;
/** applycap.Default: used until (or unless) the server reports its setting. */
export const DEFAULT_BULK_APPLY_MAX_ITEMS = 5000;

/** The refusal shown when a bulk apply is larger than the server's cap. */
export function applyCapMessage(cap: number, requested: number): string {
  return (
    `Apply is limited to ${cap.toLocaleString()} books at a time (setting bulk_apply_max_items); ` +
    `${requested.toLocaleString()} selected — narrow the selection or raise the limit in Settings.`
  );
}

/**
 * The refusal shown when a page-mode bulk apply holds books the lane has no
 * candidate pin for: never on a page while selected, and not from a "select
 * all" list that came with pins (a list past the bulk apply cap comes without).
 * Sent anyway they would carry the hashless marker, which applies whatever
 * candidate is on top by then with no stale-candidate check.
 */
export function unpinnedRefusalMessage(count: number): string {
  const books =
    count === 1 ? '1 selected book was' : `${count.toLocaleString()} selected books were`;
  return (
    `${books} never shown with a candidate, so applying could not refuse a candidate that ` +
    'changed since. Nothing was applied: select all again with fewer matching books (at most ' +
    'the bulk apply limit), or apply from the pages the books are on.'
  );
}

/** The toast after a refused action re-listed "select all": what changed in the selection. */
export function relistSummary(left: number, joined: number): string {
  const parts: string[] = [];
  if (left > 0) {
    parts.push(
      left === 1
        ? '1 selected book left the queue and was unselected'
        : `${left.toLocaleString()} selected books left the queue and were unselected`
    );
  }
  if (joined > 0) {
    parts.push(
      joined === 1
        ? '1 book joined the queue and was not selected'
        : `${joined.toLocaleString()} books joined the queue and were not selected`
    );
  }
  return parts.length === 0
    ? 'The matching books were listed again; your selection is unchanged. Apply again when ready.'
    : `The matching books were listed again: ${parts.join('; ')}. Apply again when ready.`;
}

export function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

/** One bulk action's progress, shown in the action bar while it runs. */
export interface BulkProgress {
  label: string;
  done: number;
  total: number;
}

/** Rows per request when page mode pages through a whole result (an export). The server's maximum. */
const EXPORT_PAGE_SIZE = 500;
/** A page-mode export restarts once when the queue changes under it, then gives up. */
const EXPORT_ATTEMPTS = 2;
export const EXPORT_QUEUE_CHANGED_MESSAGE =
  'The queue changed while the export was being read, twice, so it was stopped rather than ' +
  'write a file with rows repeated or missing. Try again when fewer background jobs are writing.';
const NO_COUNTS: Record<string, number> = {};
// The summary before the first page has landed.
const EMPTY_SUMMARY: MetadataLaneSummary = {
  matched: 0,
  no_match: 0,
  errors: 0,
  total: 0,
  unreviewable: 0,
  stale: 0,
};

/** Stable identity for "no un-groupings on this page" -- see `ungroupedIds`. */
const EMPTY_IDS: ReadonlySet<string> = new Set<string>();

export type Toast = (
  message: string,
  severity?: 'success' | 'error' | 'warning' | 'info',
  action?: { label: string; onClick: () => void }
) => void;

// ---------------------------------------------------------------------------
// Persisted preferences
//
// All three loaders are lifted verbatim. `loadReviewPageSize` in particular is
// load-bearing and reads like paranoia: it CLAMPS a stored value and rewrites
// the correction. The history is that 250 was once an offered option, picking it
// froze the dialog hard enough that the size control itself could not be
// reached, and the control lives inside the dialog -- so the only escape was
// clearing localStorage by hand. Clamping on read is what makes that
// self-healing for anyone who still has the bad value stored.
// ---------------------------------------------------------------------------

/** Review rows are heavy. Deliberately NOT the activity log's 250/500 list. */
export const PAGE_SIZE_OPTIONS = [25, 50, 100];

/**
 * The size an UNRECOGNISED stored preference is replaced by.
 *
 * 🔴 It is NOT "the largest size a stored preference may restore", which is
 * what this constant was called (MAX_REVIEW_PAGE_SIZE) and what its comment
 * claimed. A stored 100 restores as 100 -- `loadReviewPageSize` returns any
 * value in PAGE_SIZE_OPTIONS before it ever reaches this constant, and 100 is
 * an offered option. Clamping it would be a bug in its own right: a reviewer
 * who picks 100 from the control must get 100 back on the next open.
 *
 * The name mattered because it was read as policy. It says 100-row pages are
 * not restorable; the goal this lane is measured against is explicitly "quick
 * and responsive even at 50 or 100 items", and the one test that could have
 * caught the contradiction asserted on 50 -- the single value where "the
 * ceiling" and "an offered option" give the same answer.
 */
export const PAGE_SIZE_FALLBACK = 50;

/**
 * The "Normal review" preset: three filters that were always being set together
 * by hand. It was called "Strict review" until 2026-09-27, when the owner made
 * the switch a four-stop slider and renamed this level. 190 is above 100 on
 * purpose -- candidate scores are sums that routinely exceed 100%, so 190 means
 * "several strong signals agree".
 */
export const NORMAL_PRESET = {
  hideSkipped: true,
  hideMultiBook: true,
  confidenceThreshold: 190,
} as const;

/** Default min-confidence when no review level is on. */
export const DEFAULT_CONFIDENCE = 85;

/**
 * The review-level slider (owner ruling 2026-09-27). The levels are
 * CUMULATIVE -- each turns on everything the level below it does:
 *
 *   off      no preset
 *   normal   NORMAL_PRESET (the old "Strict review" switch, unchanged)
 *   indepth  normal + Hide runtime differences
 *   strict   indepth + Has transcription + Transcription matched
 */
export type ReviewLevel = 'off' | 'normal' | 'indepth' | 'strict';
export const REVIEW_LEVELS: readonly ReviewLevel[] = ['off', 'normal', 'indepth', 'strict'];
/** In-depth: the owner asked for runtime differences hidden by default. */
export const DEFAULT_REVIEW_LEVEL: ReviewLevel = 'indepth';

export const REVIEW_LEVEL_LABELS: Record<ReviewLevel, string> = {
  off: 'Off',
  normal: 'Normal review',
  indepth: 'In-depth review',
  strict: 'Strict review',
};

/** The filters a review level owns. */
export type ReviewLevelFilters = Pick<
  MetadataFilters,
  | 'hideSkipped'
  | 'hideMultiBook'
  | 'confidenceThreshold'
  | 'hideRuntimeDifferences'
  | 'onlyWithTranscription'
  | 'onlyTranscriptionMatched'
>;

/**
 * Every filter a level owns, with its value at that level -- including the
 * ones the level leaves OFF. Setting a level writes all six, so moving from
 * Strict down to In-depth turns the transcription filters back off instead of
 * leaving them behind.
 */
export function reviewLevelFilters(level: ReviewLevel): ReviewLevelFilters {
  const rank = REVIEW_LEVELS.indexOf(level);
  const normal = rank >= 1;
  const indepth = rank >= 2;
  const strict = rank >= 3;
  return {
    hideSkipped: normal && NORMAL_PRESET.hideSkipped,
    hideMultiBook: normal && NORMAL_PRESET.hideMultiBook,
    confidenceThreshold: normal ? NORMAL_PRESET.confidenceThreshold : DEFAULT_CONFIDENCE,
    hideRuntimeDifferences: indepth,
    onlyWithTranscription: strict,
    onlyTranscriptionMatched: strict,
  };
}

/** Whether the filters still hold exactly what `level` sets. */
export function filtersMatchLevel(filters: MetadataFilters, level: ReviewLevel): boolean {
  const want = reviewLevelFilters(level);
  return (Object.keys(want) as Array<keyof ReviewLevelFilters>).every(
    (k) => filters[k] === want[k]
  );
}

function isReviewLevel(v: string | null): v is ReviewLevel {
  return v !== null && (REVIEW_LEVELS as readonly string[]).includes(v);
}

/**
 * The persisted level, per browser. Migrates the old Strict review boolean:
 * a stored 'true' becomes Normal (the same filters under the new name), a
 * stored 'false' -- only ever written when someone turned the switch off --
 * becomes Off. Nothing stored, an unrecognised value, or blocked storage reads
 * as the default, In-depth.
 */
export function loadReviewLevel(): ReviewLevel {
  if (typeof window === 'undefined') return DEFAULT_REVIEW_LEVEL;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL);
    if (isReviewLevel(raw)) return raw;
    const legacy = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET);
    if (legacy === 'true') return 'normal';
    if (legacy === 'false') return 'off';
    return DEFAULT_REVIEW_LEVEL;
  } catch {
    // Storage blocked (private mode, sandboxed frame): the default.
    return DEFAULT_REVIEW_LEVEL;
  }
}

export function saveReviewLevel(level: ReviewLevel): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LEVEL, level);
    // Migrated: the level key is authoritative from now on.
    window.localStorage.removeItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET);
  } catch {
    // Private-mode / quota failure: the level still applies this session.
  }
}

export function loadLanguageFilter(): boolean {
  if (typeof window === 'undefined') return true;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER);
    return raw === null ? true : raw === 'true';
  } catch {
    // Storage blocked: the default (on). Reading used to throw here and take
    // the whole lane down with it.
    return true;
  }
}

export function saveLanguageFilter(on: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER, String(on));
  } catch {
    // Private-mode / quota failure: the filter still applies this session.
  }
}

/**
 * The bulk-apply toggle (owner ruling 2026-09-27), per viewer. Anything but
 * an exact stored 'replace' reads as 'fill', the default: an overwrite must
 * never come from a missing, cleared or unreadable key.
 */
export function loadBulkApplyMode(): BulkApplyMode {
  if (typeof window === 'undefined') return 'fill';
  try {
    return window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_BULK_APPLY_MODE) === 'replace'
      ? 'replace'
      : 'fill';
  } catch {
    // Storage blocked (private mode, sandboxed frame): the safe default.
    return 'fill';
  }
}

/**
 * The one prompt a Replace-mode bulk apply shows, whichever entry point sent
 * it (action bar buttons, group Apply All, the workspace's Apply selected /
 * all fields): every one of them dispatches applySelected, and the lane asks
 * there, once.
 */
export function replaceConfirmMessage(count: number): string {
  return (
    `Apply metadata to ${count.toLocaleString()} book(s) and REPLACE existing values? ` +
    'Filled fields (description, narrator, publisher, cover and the rest) will be ' +
    "overwritten with the candidate's values; each overwrite is recorded in the " +
    'change history as an owner replace.'
  );
}

export function saveBulkApplyMode(mode: BulkApplyMode): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_BULK_APPLY_MODE, mode);
  } catch {
    // Private-mode / quota failure: the toggle still applies this session.
  }
}

/**
 * The Replace prompt's "Don't ask me again" (owner request 2026-09-27), per
 * viewer. Only an exact stored 'true' skips the prompt; a missing, cleared or
 * unreadable key means ask, because the prompt is the safe side to fail to.
 */
export function loadSkipReplaceConfirm(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return (
      window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM) === 'true'
    );
  } catch {
    return false;
  }
}

/**
 * Persist (or clear) the skip flag. Returns whether storage accepted the
 * write: the caller only stops asking when it did, so blocked storage keeps
 * the prompt rather than skipping it for a session the owner cannot see end.
 */
export function saveSkipReplaceConfirm(skip: boolean): boolean {
  if (typeof window === 'undefined') return false;
  try {
    if (skip) {
      window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM, 'true');
    } else {
      window.localStorage.removeItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM);
    }
    return true;
  } catch {
    return false;
  }
}

/**
 * A Replace bulk apply waiting on the prompt. Captured at the click, pins and
 * mode included: unlike `window.confirm` a dialog does not block, so a
 * poll-triggered refresh can land before the owner answers, and the apply must
 * still pin the rows the owner was looking at when they clicked.
 */
export interface PendingReplace {
  ids: string[];
  pins: Record<string, ApplyPin>;
  mode: BulkApplyMode;
}

export function loadReviewPageSize(): number {
  if (typeof window === 'undefined') return 25;
  let raw: string | null;
  try {
    raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE);
  } catch {
    // Storage blocked: the default rather than a crashed lane.
    return 25;
  }
  if (raw === null) return 25;

  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return 25;
  if (PAGE_SIZE_OPTIONS.includes(n)) return n;

  // Not an offered option. The replacement is PAGE_SIZE_FALLBACK outright, in
  // both directions -- a stored 250 comes down to it and a stored 30 goes up to
  // it -- because there is nothing else to fall back TO: `n` is neither
  // offerable nor meaningful.
  //
  // 🔴 This used to read `min(n, FALLBACK)` and then re-check the result
  // against PAGE_SIZE_OPTIONS, which LOOKS like a clamp with a fallback and is
  // provably neither: every `n` reaching this line is already known not to be
  // an option, so `min` either returns FALLBACK (n above it) or returns a
  // non-option (n below it) that the re-check then replaces with FALLBACK. The
  // expression could not evaluate to anything else, and swapping `min` for
  // `max` did not change its value either -- two tests asserted on it and
  // neither could have failed. It is written as the constant it is.
  //
  // Persist it, so the bad value is gone for good rather than corrected on
  // every open. That part was always real: the size control lived inside a
  // dialog that a stored 250 froze before you could reach it.
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, String(PAGE_SIZE_FALLBACK));
  } catch {
    // Private-mode / quota failure: the correction still applies this session.
  }
  return PAGE_SIZE_FALLBACK;
}

export function saveReviewPageSize(size: number): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, String(size));
  } catch {
    // Private-mode / quota failure: non-fatal.
  }
}

/**
 * Normalizes a language string to an ISO 639-1 code for comparison. Mirrors the
 * server's `metadataLanguageTag`: book and candidate can come from different
 * provider APIs that spell the same language three different ways.
 *
 * Unknown languages fall through lowercased so equality still works.
 */
export function normalizeLanguage(lang: string | undefined | null): string {
  if (!lang) return '';
  const s = lang.trim().toLowerCase();
  if (!s) return '';
  const canonical: Record<string, string> = {
    english: 'en',
    eng: 'en',
    spanish: 'es',
    spa: 'es',
    french: 'fr',
    fre: 'fr',
    fra: 'fr',
    german: 'de',
    ger: 'de',
    deu: 'de',
    italian: 'it',
    ita: 'it',
    japanese: 'ja',
    jpn: 'ja',
    chinese: 'zh',
    chi: 'zh',
    zho: 'zh',
    mandarin: 'zh',
    portuguese: 'pt',
    por: 'pt',
    russian: 'ru',
    rus: 'ru',
    dutch: 'nl',
    nld: 'nl',
    korean: 'ko',
    kor: 'ko',
    arabic: 'ar',
    ara: 'ar',
  };
  if (canonical[s]) return canonical[s];
  if (s.length === 2) return s;
  return s;
}

// reviewProviderID converts the display names persisted in candidate cache rows
// into the stable ids owned by QueueRail's provider chips. Cache data comes from
// several provider clients, so it is intentionally tolerant of their historical
// spelling variants; counts and filtering must use this same function.
// 'Audible Catalog (local)' (the local catalog stage's review-only candidates)
// is a copy of Audible's own catalog and counts under the Audible chip. Server
// twin: reviewProviderID in internal/server/handlers/metadata_cache_query.go.
export function reviewProviderID(source: string | undefined | null): string {
  const normalized = source?.trim().toLowerCase() ?? '';
  switch (normalized) {
    case 'audible':
    case 'audnexus':
    case 'audnexus (audible)':
    case 'audible catalog (local)':
      return 'audible';
    case 'google':
    case 'google books':
    case 'google_books':
      return 'google_books';
    case 'open library':
    case 'open_library':
    case 'openlibrary':
      return 'openlibrary';
    default:
      return normalized;
  }
}

/**
 * Group key for "these books were assigned the same candidate".
 * Priority: asin (most specific) -> isbn -> source+title+author.
 */
export function candidateKey(c: MetadataCandidate): string {
  if (c.asin) return `asin:${c.asin}`;
  if (c.isbn) return `isbn:${c.isbn}`;
  return `${c.source}:${c.title.trim().toLowerCase()}:${c.author.trim().toLowerCase()}`;
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

/**
 * The eight switches, the provider filter, the title regex and the threshold,
 * as one object.
 *
 * Grouped rather than kept as eleven `useState`s because they move together:
 * the strict preset sets three at once, and the "reset to page 1" effect wants
 * one dependency rather than eleven. The dialog had both problems.
 */
/**
 * The rail's counts. Named rather than inlined because the hook's state and the
 * hook's return type have to stay the same shape -- when this was written twice
 * they drifted, and only the compiler noticed.
 */
export interface MetadataLaneSummary {
  matched: number;
  no_match: number;
  errors: number;
  total: number;
  /** Cache rows that exist but cannot be reviewed. */
  unreviewable: number;
  /** Reviewable rows whose cached candidate is past the server's TTL. */
  stale: number;
  /**
   * The same total split by cause. Optional because a server that predates the
   * split omits it, and "no breakdown available" is a different claim from
   * "every cause is zero".
   */
  unreviewable_by_cause?: {
    orphaned: number;
    no_candidates: number;
    decode_errors: number;
    /** Pending books retitled since their search (absent from an older server). */
    identity_stale?: number;
  };
  /**
   * Books already ruled on whose stored candidate is gone. Optional for the
   * same reason as the breakdown above: a server that predates the split omits
   * it, and these books used to be counted inside `unreviewable`.
   */
  resolved_no_candidates?: number;
  /** Rows whose last fallback lookup was deferred (server `deferred`). */
  deferred?: number;
  /** Identification state counts (server `ident_counts`), absent from an older server. */
  ident_counts?: api.IdentCounts;
}

/**
 * A summary chip the reviewer clicked to see exactly the books it counts.
 * While one is active the list is those books and nothing else -- every other
 * filter is paused -- so the number on the chip is the number of rows shown.
 *
 *   matched / no_match / total    reviewable rows (status matched / no_match / all)
 *   errors                        candidates that will not decode
 *   no_candidates                 nobody has ruled on it, no candidate stored
 *   resolved_no_candidates        ruled on, candidate gone
 *   identity_stale                nobody has ruled on it, retitled since its search
 *   stale                         rows the server flags `stale` (past the TTL and
 *                                 searchable by a refetch)
 *
 * The last four need the server's unreviewable bucket, loaded on first use.
 */
export type ChipFilter =
  | 'matched'
  | 'no_match'
  | 'total'
  | 'errors'
  | 'no_candidates'
  | 'resolved_no_candidates'
  | 'identity_stale'
  | 'stale'
  | 'deferred';

const CHIPS_NEEDING_UNREVIEWABLE: ReadonlySet<ChipFilter> = new Set<ChipFilter>([
  'errors',
  'no_candidates',
  'resolved_no_candidates',
  'identity_stale',
  'stale',
  'deferred',
]);

/** Whether a row came from the unreviewable bucket (it has no candidate). */
export function isUnreviewableRow(r: CandidateResult): boolean {
  return (
    r.status === 'no_candidates' ||
    r.status === 'resolved_no_candidates' ||
    r.status === 'decode_error' ||
    r.status === 'identity_stale'
  );
}

/**
 * Whether "Search again" would be skipped for this book because it is marked
 * no-match: the fetch op never searches for a book the owner ruled has no
 * match, force or not. A reviewable `no_match` row IS that mark (the server
 * derives the status from it); an unreviewable row carries it in
 * review_status; and a row rejected in this session carries it in row state.
 */
export function isMarkedNoMatch(r: CandidateResult | undefined, state?: RowState): boolean {
  if (state === 'rejected') return true;
  if (!r) return false;
  return r.status === 'no_match' || r.review_status === 'no_match';
}

export interface MetadataFilters {
  sourceFilter: string | null;
  confidenceThreshold: number;
  titleFilter: string;
  hideApplied: boolean;
  hideRejected: boolean;
  hideSkipped: boolean;
  hideNoMatch: boolean;
  /** Hide rows whose candidate runtime is known to differ materially. */
  hideRuntimeDifferences: boolean;
  /**
   * Hides any book that shares a match with another book, AND takes those books
   * out of Apply Selected. The second half is behaviour, not description --
   * see the deselect effect below.
   */
  hideMultiBook: boolean;
  matchLanguage: boolean;
  onlyWithTranscription: boolean;
  /** NOT the same as `onlyWithTranscription`: this one means the score was boosted by it. */
  onlyTranscriptionMatched: boolean;
}

function initialFilters(level: ReviewLevel): MetadataFilters {
  return {
    sourceFilter: null,
    titleFilter: '',
    hideApplied: true,
    hideRejected: true,
    hideNoMatch: true,
    matchLanguage: loadLanguageFilter(),
    // hideSkipped, hideMultiBook, confidenceThreshold, hideRuntimeDifferences
    // and the two transcription filters all come from the level.
    ...reviewLevelFilters(level),
  };
}

/**
 * Milliseconds a filter change waits for the next one before its query is
 * sent. A burst of keystrokes inside the window sends one request.
 */
export const PAGE_QUERY_DEBOUNCE_MS = 150;

/**
 * Most ids the `skipped` list of one page-mode query carries. The list rides
 * in the URL (the endpoint is a GET) and a long one is refused by proxies well
 * before the server's own limit, so past this the most recently skipped ids
 * are kept and the lane says so once. Only a mass skip (Skip all unmatched)
 * reaches it.
 */
export const MAX_QUERY_SKIPPED_IDS = 200;

/** The inputs of a page query that come from the lane's state. */
export interface ReviewQueryInput {
  filters: MetadataFilters;
  chip: ChipFilter | null;
  /** The Title text to send: '' when the box is empty or not a valid pattern yet. */
  title: string;
  /** Books in the lane's client-only `skipped` row state. */
  skipped: readonly string[];
  /** Books the reviewer pulled out of a multi-book group on this page. */
  ungrouped: readonly string[];
}

/**
 * The server filters for this state. Only what changes the answer is sent: a chip view pauses every filter
 * but the Title (so that is all it carries), a switch that is off is omitted,
 * and the skipped / ungrouped lists ride along only under the switches that
 * read them. Two states that list the same rows therefore send the same
 * request, which is what lets the server reuse one evaluation for them.
 */
export function reviewQueryFor(input: ReviewQueryInput): api.ReviewQueryParams {
  const { filters, chip, title } = input;
  const out: api.ReviewQueryParams = {};
  if (title) out.q = title;
  if (chip !== null) {
    out.chip = chip;
    return out;
  }
  if (filters.sourceFilter) out.source = filters.sourceFilter;
  if (filters.confidenceThreshold > 0) out.min_confidence = filters.confidenceThreshold;
  if (filters.hideApplied) out.hide_applied = true;
  if (filters.hideRejected) out.hide_rejected = true;
  if (filters.hideSkipped) out.hide_skipped = true;
  if (filters.hideNoMatch) out.hide_no_match = true;
  if (filters.hideRuntimeDifferences) out.hide_runtime = true;
  if (filters.matchLanguage) out.match_language = true;
  if (filters.onlyWithTranscription) out.has_transcription = true;
  if (filters.onlyTranscriptionMatched) out.transcription_matched = true;
  if (filters.hideMultiBook) {
    out.hide_multi_book = true;
    if (input.ungrouped.length > 0) out.ungrouped = [...input.ungrouped].sort();
  }
  if ((filters.hideSkipped || filters.hideRejected) && input.skipped.length > 0) {
    out.skipped = input.skipped.slice(-MAX_QUERY_SKIPPED_IDS).sort();
  }
  return out;
}

/**
 * Marks a book skipped at the END of the row-state map (and returns the map).
 * Map order is insertion order, and re-setting a key keeps its old place, so
 * without the delete a book skipped again (or skipped after an earlier
 * pending/unskip) would keep its first position. Page mode sends only the
 * MAX_QUERY_SKIPPED_IDS most recently skipped books, read off the end of this
 * order, so it has to be the order of the skips.
 */
export function markSkipped(m: Map<string, RowState>, id: string): Map<string, RowState> {
  m.delete(id);
  m.set(id, 'skipped');
  return m;
}

/** The rail's counts, from the server facets of a page-mode response. */
function summaryFromFacets(f: api.ReviewFacets): MetadataLaneSummary {
  return {
    matched: f.matched,
    no_match: f.no_match,
    errors: f.errors,
    total: f.total,
    unreviewable: f.unreviewable,
    stale: f.stale,
    // Left undefined rather than zero-filled when the server omits it (an
    // older server): zero would read as "none", undefined as "not reported".
    unreviewable_by_cause: f.unreviewable_by_cause,
    resolved_no_candidates: f.resolved_no_candidates,
    deferred: f.deferred,
    ident_counts: f.ident_counts,
  };
}

export interface MetadataLane {
  loading: boolean;
  /**
   * Why the last load failed, or null.
   *
   * This lane swallowed load failures entirely -- `.catch(() => setLoading(false))`
   * with no state, no toast and no console line. A 500 and an empty cache were
   * therefore indistinguishable, and the spine told the reviewer "No metadata
   * matches to review. Search providers from the Metadata menu to find some."
   * That advice is actively wrong when the request failed: there may be
   * thousands of matches waiting behind a server that is simply down, and
   * following it does nothing. The other two lanes have carried an `error` for
   * exactly this reason; this one was the outlier.
   */
  error: string | null;
  /**
   * The current page's rows. The lane never holds the whole set (the server
   * filters and pages it): `filteredCount` / `exportRows` are how to reach
   * the rest.
   */
  results: CandidateResult[];
  /**
   * The current page (the server did the filtering), minus rows this session
   * already applied, rejected or skipped under their switches.
   */
  filteredResults: CandidateResult[];
  /**
   * How many books the current filters (or chip) match across all pages: the
   * number "Select all N matching" shows: the server's total.
   */
  filteredCount: number;
  /** Reviewable books with no match or an error: the "Skip all unmatched (N)" count. */
  unmatchedCount: number;
  /**
   * Row objects the lane holds in state, as measured rather than assumed:
   * `list` is the rows of the current page and `details` the full rows
   * fetched for it.
   */
  heldRows: { list: number; details: number };
  /**
   * Every row the current filters match, for an export: pages through the
   * server, 500 rows at a time.
   */
  exportRows: () => Promise<CandidateResult[]>;
  /**
   * An export is reading pages. The workspace disables Export while
   * it is; a second call would abort the first.
   */
  exporting: boolean;
  /** The current page. */
  pageResults: CandidateResult[];
  /** Multi-book groups on the current page. Singletons render as rows. */
  groups: CandidateGroup[];
  /** Book ids belonging to a group on this page -- excluded from `rows`. */
  groupedBookIds: Set<string>;
  /** Rows to hand the spine: the page minus anything rendered as a group. */
  rows: CandidateResult[];
  /**
   * Book ids on the current page whose candidate is still the index row: the
   * per-page detail fetch (`ids=`) that carries its breakdown has not been
   * applied yet. True from the moment the page's index rows are shown. Keyed
   * by book id, which is what the fetch asks for and what the spine addresses
   * rows by; a group's books share one candidate but are fetched one by one.
   */
  detailPending: ReadonlySet<string>;
  /**
   * Book ids on the current page whose detail fetch rejected, or whose detail
   * the server answered with a different candidate (or none) than the index
   * row's. Distinct from `detailPending`: nothing is in flight for these. A
   * refresh asks again (so does a page change, after a rejected fetch; a
   * book the server left out keeps its ask so the mismatch guard cannot loop).
   */
  detailFailed: ReadonlySet<string>;

  sourceCounts: Record<string, number>;
  summary: MetadataLaneSummary;

  page: number;
  totalPages: number;
  pageSize: number;
  setPage: (p: number) => void;
  setPageSize: (n: number) => void;

  filters: MetadataFilters;
  setFilters: (patch: Partial<MetadataFilters>) => void;
  /**
   * Why the Title filter is not a valid regex, or null. An invalid pattern
   * filters nothing (the rows stay as they were), so the rail must show this
   * -- otherwise a typo looks exactly like a filter that matched everything.
   */
  titleFilterError: string | null;
  /**
   * The review-level slider. Picking a level writes every filter it owns (see
   * reviewLevelFilters). Flipping one of those switches by hand afterwards
   * leaves the level where it is; `levelCustomised` then says the switches no
   * longer match it.
   */
  reviewLevel: ReviewLevel;
  setReviewLevel: (level: ReviewLevel) => void;
  levelCustomised: boolean;
  /**
   * Rows the Hide runtime differences filter is hiding right now: rows that
   * pass every earlier filter and are dropped by that one. 0 when it is off.
   */
  runtimeHiddenCount: number;

  /** The summary chip whose books the list is showing, or null. */
  chipFilter: ChipFilter | null;
  /** Show exactly the books a chip counts; the same chip again clears it. */
  toggleChipFilter: (chip: ChipFilter) => void;
  clearChipFilter: () => void;

  /**
   * The selection minus the books known to have no candidate (unreviewable
   * rows). Apply selected sends these; a selection with no unreviewable rows
   * in it is the selection unchanged.
   */
  applicableSelectedIds: string[];
  /** Select (or deselect) many books at once -- the rail's "select this page". */
  setSelection: (ids: string[], selected: boolean) => void;
  /** Select every book the current filters (or chip) match, across all pages. */
  selectAllMatching: () => void;
  clearSelection: () => void;
  /** True when every book the current view matches is selected. */
  allMatchingSelected: boolean;
  /** Mark every selected book skipped (client-side, like the row Skip). */
  skipSelected: () => void;
  /**
   * Mark every selected book no-match on the server, PER_BOOK_CONCURRENCY at
   * a time (there is no bulk endpoint). Returns how many failed.
   */
  rejectSelected: () => Promise<number>;
  /** The running bulk action's progress, or null. */
  bulkProgress: BulkProgress | null;
  /** True while a Search again request (and its no-match clears) is in flight. */
  searching: boolean;
  /**
   * Search again for these books using each book's current title and author:
   * one forced metadata fetch op for all of them (the server runs it with a
   * bounded worker pool and a provider rate limit). Books marked no-match are
   * never searched by that op, so when some are included `confirm` is asked
   * first and, on yes, their no-match marks are cleared (4 at a time) before
   * the fetch. When the op finishes the lane reloads and the new candidates
   * appear as ordinary rows to review. Resolves to the op id, or null.
   */
  searchAgain: (
    ids: string[],
    confirm: (message: string) => Promise<boolean>
  ) => Promise<string | null>;
  /**
   * The bulk buttons' toggle: 'fill' writes only empty fields (the default),
   * 'replace' overwrites filled ones. Read by every applySelected dispatch
   * (Apply selected, Apply page, Apply high confidence, group Apply All);
   * never by the single-row Apply.
   */
  bulkApplyMode: BulkApplyMode;
  setBulkApplyMode: (mode: BulkApplyMode) => void;

  /**
   * The Replace bulk apply waiting on the owner's answer, or null. Every
   * applySelected dispatch in Replace mode parks here instead of sending
   * (unless `skipReplaceConfirm`), so every bulk entry point asks exactly once;
   * the workspace renders the dialog from it.
   */
  pendingReplace: PendingReplace | null;
  /** Send the parked apply. `dontAskAgain` persists the skip flag. */
  confirmReplace: (dontAskAgain: boolean) => void;
  /** Drop the parked apply. Never persists the skip flag. */
  cancelReplace: () => void;
  /** True once the owner ticked "Don't ask me again" and storage kept it. */
  skipReplaceConfirm: boolean;
  /** Clears the skip flag so the Replace prompt shows again. */
  resetReplaceConfirm: () => void;

  selectedIds: Set<string>;
  applying: boolean;

  /** Matched, above threshold, has a narrator, still undecided. */
  highConfidenceIds: string[];
  /** Every undecided matched row on this page. */
  allVisiblePendingIds: string[];

  previewCover: string | null;
  setPreviewCover: (url: string | null) => void;

  /**
   * True while a refetch REQUEST is in flight -- not while the resulting
   * operation runs. The op is a background job tracked by the operations list;
   * this flag exists only to stop a second click landing before the first
   * request has been answered.
   */
  refetching: boolean;
  /**
   * Start a metadata refetch for the given books. Resolves to the operation id,
   * or null when nothing was started (empty input, an already-running fetch, or
   * a failure -- all three are reported to the user by toast).
   */
  refetchBooks: (ids: string[]) => Promise<string | null>;
  /**
   * Refetch every book the summary counts as stale (`summary.stale`). The set
   * is resolved on the server with the same predicate as that count
   * (POST {stale: true}); the client cannot build it, because it only holds
   * the reviewable bucket -- the chip read "3,511 stale" while a client-built
   * set refetched 10. Resolves like refetchBooks.
   */
  refetchStale: () => Promise<string | null>;

  /** Satisfies the spine's contract directly -- see the note on SpineContext. */
  spineCtx: SpineContext;
  dispatch: (action: MetadataAction) => void;
  refresh: () => void;
}

/**
 * Whether detail row `d` is the full row for index row `r`: it carries a
 * candidate and that candidate's hash is the one the index served. The page
 * swap and the detail state are both decided by this, so they cannot disagree.
 */
function detailMatches(
  r: CandidateResult,
  d: CandidateResult | undefined
): d is CandidateResult & { candidate: MetadataCandidate } {
  return !!d?.candidate && d.candidate_hash === r.candidate_hash;
}

/**
 * @param active  Whether to fetch. The workspace passes `lane === 'metadata'`
 *                so switching lanes does not keep three fetches in flight.
 */
export function useMetadataLane(toast: Toast, active = true): MetadataLane {
  // The list state: the rows of the CURRENT page only, the size of the
  // filtered list, the server's facet counts and the snapshot generation.
  const [pageRows, setPageRows] = useState<CandidateResult[]>([]);
  const [pageTotal, setPageTotal] = useState(0);
  const [pageFacets, setPageFacets] = useState<api.ReviewFacets | null>(null);
  // The server's message for a Title pattern the lane's own compiler accepted.
  const [serverTitleError, setServerTitleError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // The load effect reads toast through a ref: adding `toast` to its deps would
  // re-fetch the whole review set whenever a caller passed an unmemoized toast.
  const toastRef = useRef(toast);
  useEffect(() => {
    toastRef.current = toast;
  }, [toast]);
  const [rowStates, setRowStates] = useState<Map<string, RowState>>(new Map());
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  // Read once: the level seeds the filters, and a lazy initializer per piece
  // of state would read storage twice.
  const [initialLevel] = useState(loadReviewLevel);
  const [filters, setFiltersState] = useState<MetadataFilters>(() => initialFilters(initialLevel));
  const [reviewLevel, setReviewLevelState] = useState<ReviewLevel>(initialLevel);
  const [chipFilter, setChipFilter] = useState<ChipFilter | null>(null);
  // Full rows (with candidate descriptions) for books the page has shown,
  // keyed by book id. The page's rows come without candidate descriptions; the
  // lane fetches the full rows for what it shows and swaps their candidates in.
  // Reset with every new snapshot generation so a refreshed list never shows an
  // older detail row.
  const [detailsById, setDetailsById] = useState<Map<string, CandidateResult>>(() => new Map());
  // Book ids whose detail fetch rejected, or whose book the detail response
  // left out. Without this a failed fetch is indistinguishable from one still
  // in flight. Cleared for the ids of every new ask, and with the index.
  const [detailFailedIds, setDetailFailedIds] = useState<Set<string>>(() => new Set());
  const [searching, setSearching] = useState(false);
  const [applyCap, setApplyCap] = useState(DEFAULT_BULK_APPLY_MAX_ITEMS);
  const [bulkProgress, setBulkProgress] = useState<BulkProgress | null>(null);
  const [bulkApplyMode, setBulkApplyModeState] = useState<BulkApplyMode>(loadBulkApplyMode);
  const [skipReplaceConfirm, setSkipReplaceConfirm] = useState<boolean>(loadSkipReplaceConfirm);
  const [pendingReplace, setPendingReplace] = useState<PendingReplace | null>(null);
  const [requestedPage, setPage] = useState(1);
  const [pageSize, setPageSizeState] = useState<number>(loadReviewPageSize);
  const [applying, setApplying] = useState(false);
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  // Un-groupings are per-page: a group is a set of books on the CURRENT page
  // that share a candidate, so navigating away makes them meaningless. Carrying
  // the page they belong to makes that automatic -- the alternative, an effect
  // that empties the set whenever `page` changes, costs a second render pass on
  // every page turn and briefly renders the new page with the old page's
  // un-groupings still applied.
  const [ungrouped, setUngrouped] = useState<{ page: number; ids: Set<string> }>({
    page: 1,
    ids: new Set(),
  });
  // Keyed on `requestedPage`, NOT on the clamped `page` below: `page` is derived
  // from totalPages <- filteredResults <- multiBookIds <- ungroupedIds, so
  // keying on it would close a cycle.
  const ungroupedIds = useMemo(
    () => (ungrouped.page === requestedPage ? ungrouped.ids : EMPTY_IDS),
    [ungrouped, requestedPage]
  );
  const [refreshKey, setRefreshKey] = useState(0);

  // Discards out-of-order responses. Without it a slow page-1 fetch that
  // resolves after page 2 overwrites page 2's rows, and the user is looking at
  // the wrong page with the right page number.
  const fetchIdRef = useRef(0);

  // Book ids whose apply has been DISPATCHED but whose background op has not
  // reached a terminal state yet.
  //
  // This is what lets a refresh distinguish "the server has not caught up yet"
  // from "the server has spoken and the book was not applied". Without it the
  // seeding below can only ever ADD row states, so an optimistic `applied` is
  // permanent: a book the background worker failed on stays hidden behind the
  // default Hide-applied filter forever, and the reviewer is never told. That
  // is worse than a visible failure, because the queue looks finished.
  // REFCOUNTED, not a plain Set. A book can be in two applies at once -- a
  // bulk "Apply Selected" and a per-row Apply inside the 500ms debounce window
  // -- and with a Set the first op to finish deleted the shared entry and
  // reverted the row while the second was still genuinely running.
  // Which rows hold a state the RECONCILE wrote, rather than a person.
  //
  // Without this the reconcile cannot tell the two apart, because they are the
  // same string. Every no_match row is seeded `rejected` here on first load, so
  // `rejected` means either "the server reports no match" or "a reviewer
  // rejected this" -- and the two need opposite treatment: the first is a
  // mirror of server state and must stay correctable, the second is a decision
  // the server has no better answer for and must be protected.
  //
  // Guessing either way breaks a real flow. Protecting all of them freezes the
  // manual Search-and-apply escape hatch: Search is offered ONLY on no_match
  // rows (QueueRail), it applies server-side for real, and MetadataPanel then
  // calls refresh() -- so the row comes back `applied` from the server and the
  // guard would discard that, leaving a successfully applied book hidden behind
  // hideRejected with no sign the apply worked. Reconciling all of them
  // reintroduces the skipped/pending clobber this reconcile exists to fix.
  //
  // So it is recorded rather than inferred. Any human action on a row clears
  // its entry; the reconcile re-adds it whenever it writes the row itself.
  const serverDerivedRef = useRef<Set<string>>(new Set());
  const clearServerDerived = useCallback((ids: string[]) => {
    ids.forEach((id) => serverDerivedRef.current.delete(id));
  }, []);

  const inFlightApplyRef = useRef<Map<string, number>>(new Map());
  const retainInFlight = useCallback((ids: string[]) => {
    ids.forEach((id) =>
      inFlightApplyRef.current.set(id, (inFlightApplyRef.current.get(id) ?? 0) + 1)
    );
  }, []);
  const releaseInFlight = useCallback((ids: string[]) => {
    ids.forEach((id) => {
      const n = (inFlightApplyRef.current.get(id) ?? 0) - 1;
      if (n > 0) inFlightApplyRef.current.set(id, n);
      else inFlightApplyRef.current.delete(id);
    });
  }, []);

  // Guards the deferred refresh below: the apply op outlives the component if
  // the user navigates away mid-apply, and setting state then warns.
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const refresh = useCallback(() => setRefreshKey((k) => k + 1), []);

  // The page response does not carry the bulk apply cap, so it is read once,
  // when the lane first becomes active, from the config. Anything but a usable
  // number (the call failing, a 403 -- GET /config needs settings.manage, which
  // a reviewer may not have --, no config) leaves the default cap; nothing waits
  // on this read.
  const capReadRef = useRef(false);
  useEffect(() => {
    if (!active || capReadRef.current) return;
    capReadRef.current = true;
    void Promise.resolve()
      .then(() => api.getConfig())
      .then(
        (cfg) => {
          if (!aliveRef.current) return;
          if (typeof cfg?.bulk_apply_max_items === 'number' && cfg.bulk_apply_max_items > 0) {
            setApplyCap(cfg.bulk_apply_max_items);
          }
        },
        () => undefined
      );
  }, [active]);

  // Reconciles row state with the rows the server just returned: the current
  // page (a book that is not on the page keeps what it has until it appears on
  // one).
  const reconcileRowStates = useCallback((rows: CandidateResult[]) => {
    // Reconcile row state with the server, without clobbering decisions the
    // user made in this session.
    //
    // This used to merge ADD-ONLY (`if (!merged.has(k))`), which quietly
    // made every optimistic state permanent. applyMany marks each dispatched
    // book `applied` and its comment promises "the terminal poll still
    // refreshes and restores any book the worker ultimately did not apply"
    // -- but the add-only merge could not restore anything, because every
    // book it had just marked was already a key. A book the background apply
    // failed on stayed hidden behind the default Hide-applied filter with no
    // way back. Bulk apply made that hundreds of books at a time.
    //
    // The rule now:
    //   - a book still in flight keeps its optimistic state (reverting it
    //     mid-op is the flicker ActionBar.tsx explains at length);
    //   - otherwise the server is authoritative for the two states it
    //     actually tracks, applied and no_match;
    //   - and a stale optimistic `applied` on a book the server does NOT
    //     report as applied is dropped, which is the whole point.
    setRowStates((prev) => {
      const next = new Map(prev);
      for (const r of rows) {
        const id = r.book.id;
        if ((inFlightApplyRef.current.get(id) ?? 0) > 0) continue;

        // ONLY an absent state or the optimistic `applied` may be
        // reconciled. Everything else is a deliberate in-session decision
        // and the server has no better answer for it.
        //
        // An earlier version of this loop wrote `rejected` unconditionally
        // whenever the server said no_match, and that was a data-loss bug
        // of exactly the kind this reconcile exists to fix. `skip` and
        // `skipAllUnmatched` set the client-only `skipped` on rows whose
        // server status IS no_match (skip never calls the server, unlike
        // reject). So every skipped row flipped to `rejected` on the very
        // next refresh. Deterministic, not a race.
        //
        // Where that is visible: no_match rows are hidden by default
        // (`hideNoMatch: true`), so the reviewer meets them by turning that
        // filter off -- which is precisely the pass in which skip is used.
        // In that view `hideSkipped` is false (it is only true under the
        // strict preset, default off) while `hideRejected` is true, so the
        // clobber did not merely relabel the row: it disappeared, and the
        // unskip affordance went with it.
        //
        // It also clobbered `unreject`: that sets `pending` client-side
        // after clearing the no-match, so a refresh landing before the
        // server caught up flipped the undo straight back to rejected.
        const local = next.get(id);

        // `applied` is a FACT about what the server did, not an opinion
        // about what to do, so it outranks every local state including a
        // human one. A book whose metadata has been written is applied even
        // if this session had earlier skipped or rejected it; showing it
        // otherwise contradicts the library.
        if (r.status === 'applied') {
          next.set(id, 'applied');
          serverDerivedRef.current.add(id);
          continue;
        }

        // Everything below is the server's OPINION about a row nobody has
        // judged. A human decision outranks it -- unless the state on the
        // row was written by this same reconcile, in which case there is no
        // human decision to protect.
        const humanDecision =
          local !== undefined && local !== 'applied' && !serverDerivedRef.current.has(id);
        if (humanDecision) continue;

        if (r.status === 'no_match') {
          next.set(id, 'rejected');
          serverDerivedRef.current.add(id);
        } else if (local !== undefined) {
          // Either a stale optimistic `applied` the server does not confirm,
          // or a server-derived `rejected` whose no-match has since been
          // cleared (a refetch or a manual Search found a candidate). Both
          // must fall back to pending rather than persist.
          next.delete(id);
          serverDerivedRef.current.delete(id);
        }
      }
      return next;
    });
  }, []);

  const setFilters = useCallback((patch: Partial<MetadataFilters>) => {
    setFiltersState((prev) => {
      const next = { ...prev, ...patch };
      if (patch.matchLanguage !== undefined) saveLanguageFilter(patch.matchLanguage);
      return next;
    });
    setPage(1); // a filter change always returns to the first page of results
  }, []);

  const setBulkApplyMode = useCallback((mode: BulkApplyMode) => {
    setBulkApplyModeState(mode);
    saveBulkApplyMode(mode);
  }, []);

  const setReviewLevel = useCallback((level: ReviewLevel) => {
    setReviewLevelState(level);
    saveReviewLevel(level);
    setFiltersState((prev) => ({ ...prev, ...reviewLevelFilters(level) }));
    setPage(1);
  }, []);

  const levelCustomised = useMemo(
    () => !filtersMatchLevel(filters, reviewLevel),
    [filters, reviewLevel]
  );

  const toggleChipFilter = useCallback((chip: ChipFilter) => {
    setChipFilter((prev) => (prev === chip ? null : chip));
    setPage(1);
  }, []);
  const clearChipFilter = useCallback(() => {
    setChipFilter(null);
    setPage(1);
  }, []);

  // The unreviewable bucket's ids are fetched only when a chip needs them: it
  // is thousands of books the review queue itself never shows, so the default
  // load does not pay for it.
  const needUnreviewable = chipFilter !== null && CHIPS_NEEDING_UNREVIEWABLE.has(chipFilter);

  // The lane keeps only the IDS of the unreviewable bucket, for one purpose:
  // Apply selected leaves out books known to have no candidate. Loaded when a
  // chip needs it, stale after a refresh. Failure leaves the filter off: the
  // server skips a book with no candidate on apply, it never applies one.
  const [pageUnreviewableIds, setPageUnreviewableIds] = useState<Set<string>>(() => new Set());
  const [pageUnreviewableFor, setPageUnreviewableFor] = useState(-1);
  useEffect(() => {
    if (!active || !needUnreviewable || pageUnreviewableFor === refreshKey) return;
    const controller = new AbortController();
    api
      .getReviewMatchingIds({ bucket: 'unreviewable' }, controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return;
        setPageUnreviewableIds(new Set(data.ids));
        setPageUnreviewableFor(refreshKey);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted || isAbortError(err)) return;
        setPageUnreviewableFor(refreshKey);
      });
    return () => controller.abort();
  }, [active, needUnreviewable, pageUnreviewableFor, refreshKey]);

  const setPageSize = useCallback((n: number) => {
    setPageSizeState(n);
    saveReviewPageSize(n);
    setPage(1);
  }, []);

  // --- the derivation chain -------------------------------------------------

  // The Title box takes the SAME grammar as `title:` in the Library search bar
  // (owner decision 2026-10-06: one syntax, RE2 regex): `text` = contains,
  // `a*` = wildcard, `/re/` = RE2 regex (case-insensitive), `"x"` = literal,
  // and `title:` tokens with `-` negation. compileTitleFilter translates RE2
  // to JS (\p{L} needs the u flag) and rejects what RE2 rejects (lookahead,
  // backreferences) instead of silently running it.
  //
  // A half-typed or invalid value does not filter yet, but it IS reported:
  // the rail shows `titleFilterError` under the field, so a typo never
  // silently reads as "everything matched". `titleRegex` keeps its name for
  // the chip/prune call sites; it is any object with test().
  const { titleRegex, titleFilterError } = useMemo((): {
    titleRegex: { test: (s: string) => boolean } | null;
    titleFilterError: string | null;
  } => {
    const compiled = compileTitleFilter(filters.titleFilter);
    if (!compiled.active) return { titleRegex: null, titleFilterError: null };
    if (compiled.error) return { titleRegex: null, titleFilterError: compiled.error };
    return { titleRegex: compiled, titleFilterError: null };
  }, [filters.titleFilter]);

  // --- one server query per settled filter ----------------------------------
  //
  // The lane never loads the review set. The filters become a server query
  // (reviewQueryFor); the response is the visible page, the filtered total, the
  // rail's facet counts and the snapshot generation.

  // Books whose row state is `skipped`: client-only, so the server only knows
  // them if told (the `skipped` parameter).
  const skippedIds = useMemo(() => {
    const out: string[] = [];
    rowStates.forEach((state, id) => {
      if (state === 'skipped') out.push(id);
    });
    return out;
  }, [rowStates]);
  const skippedTruncated =
    skippedIds.length > MAX_QUERY_SKIPPED_IDS &&
    (filters.hideSkipped || filters.hideRejected) &&
    chipFilter === null;

  // An invalid or half-typed pattern is not sent: it filters nothing, and the
  // rail shows `titleFilterError` for it.
  const queryParams = useMemo(
    () =>
      reviewQueryFor({
        filters,
        chip: chipFilter,
        title: titleRegex ? filters.titleFilter : '',
        skipped: skippedIds,
        ungrouped: [...ungroupedIds],
      }),
    [filters, chipFilter, titleRegex, skippedIds, ungroupedIds]
  );
  const queryKey = useMemo(() => JSON.stringify(queryParams), [queryParams]);
  // The part of the query that decides which books the filters admit: the
  // query minus this session's own skipped / ungrouped lists, whose effect the
  // rows on screen already show (visiblePageRows). What refuseUnsettled
  // compares, so a skip does not hold up a bulk action until the next page.
  const filterQueryKey = useMemo(() => {
    const { skipped: _skipped, ungrouped: _ungrouped, ...rest } = queryParams;
    return JSON.stringify(rest);
  }, [queryParams]);

  // Every filter, as one comparable value: what the selection prunes key on.
  const filtersKey = useMemo(() => JSON.stringify(filters), [filters]);
  // Page mode: the filtersKey the selection has been checked against. The
  // prune below is asynchronous (a debounce and an ids request), and until it
  // settles for the current filters the selection may still hold books they
  // exclude, so bulk apply and reject are refused (refuseUnsettled).
  const pruneSettledKeyRef = useRef(filtersKey);

  // What the effects below need to read at the moment they fire without
  // re-running when it changes.
  const latestQueryRef = useRef({ params: queryParams, chip: chipFilter });
  const latestQueryKeyRef = useRef(queryKey);
  const selectedIdsRef = useRef(selectedIds);
  useEffect(() => {
    latestQueryRef.current = { params: queryParams, chip: chipFilter };
    latestQueryKeyRef.current = queryKey;
    selectedIdsRef.current = selectedIds;
  });

  const skippedWarnedRef = useRef(false);
  useEffect(() => {
    if (!skippedTruncated || skippedWarnedRef.current) return;
    skippedWarnedRef.current = true;
    // Both directions, because the server reads a skipped book it is not told
    // about as whatever its own state says: with Hide skipped on, an older
    // skipped book is listed again; with Hide rejected on, an older skipped
    // book with no match counts as rejected and stays hidden. "Most recently"
    // is accurate because every skip moves the book to the end of rowStates
    // (markSkipped), which is the order skippedIds keeps.
    toastRef.current(
      `More than ${MAX_QUERY_SKIPPED_IDS} books are skipped, and the queue is filtered on only the ` +
        `${MAX_QUERY_SKIPPED_IDS} most recently skipped. Older skipped books are treated as not ` +
        'skipped: with Hide skipped on they show again, and with Hide rejected on those with no ' +
        'match are hidden as rejected.',
      'warning'
    );
  }, [skippedTruncated]);

  // The generation the rows on screen were listed at, and the one request that
  // may still update state. A response for any other request is dropped.
  const generationRef = useRef<string | null>(null);
  const lastIssuedRef = useRef<{ key: string; refresh: number } | null>(null);
  // The filters the rows on screen answer (a filterQueryKey). Until it equals
  // the current one the page still shows the previous filters' rows (the
  // debounce, the request, or a request that failed), and a bulk action over
  // them is refused (refuseUnsettled).
  const landedKeyRef = useRef<string | null>(null);
  // The filterQueryKey whose page request failed (null after a success), so a
  // refused bulk action can say the page did not load rather than "has not
  // caught up".
  const failedKeyRef = useRef<string | null>(null);
  // TODO(02-PR15): docs/audits/2026-10-09-review-tab-heap-snapshot.md is the
  // Review tab heap-snapshot note this effect chain was to be checked against
  // for a closure retaining an old results array. Its Findings section is
  // still empty, so nothing was changed on its account; revisit this chain
  // once it is filled in.
  useEffect(() => {
    if (!active) return;
    const params = JSON.parse(queryKey) as api.ReviewQueryParams;
    const offset = (requestedPage - 1) * pageSize;
    const last = lastIssuedRef.current;
    // The first request, a refresh and a page turn go straight out; only a
    // change to the filters waits, so a burst of keystrokes sends one request.
    const immediate = last === null || last.refresh !== refreshKey || last.key === queryKey;
    const controller = new AbortController();
    const fetchId = ++fetchIdRef.current;

    const issue = () => {
      lastIssuedRef.current = { key: queryKey, refresh: refreshKey };
      setLoading(true);
      // Cleared on every attempt, so a successful Retry clears the failure Alert.
      setError(null);
      api
        .getReviewPage({ ...params, limit: pageSize, offset }, controller.signal)
        .then((data) => {
          // Both checks: a mock or a proxy may deliver a response after its
          // request was aborted, and an aborted request never updates state.
          if (controller.signal.aborted || fetchId !== fetchIdRef.current) return;
          const rows = data.results || [];
          const total = data.total_count ?? rows.length;
          setPageTotal(total);
          setPageFacets(data.facets ?? null);
          // A filter (or an apply) can shrink the list beneath the page asked
          // for. Move to the last page that exists and let that request fill
          // the rows; rendering this empty one would flash an empty spine.
          const lastPage = Math.max(1, Math.ceil(total / pageSize));
          if (requestedPage > lastPage) {
            setPage(lastPage);
            return;
          }
          reconcileRowStates(rows);
          if (generationRef.current !== data.generation) {
            // A new snapshot: every detail row fetched for the old one is
            // suspect.
            generationRef.current = data.generation;
            detailEpochRef.current += 1;
            detailAskedRef.current = new Set();
            setDetailsById(new Map());
            setDetailFailedIds(new Set());
          } else {
            // The same snapshot, a different page: keep what the page still
            // shows and let go of the rest, so the details the lane holds are
            // bounded by one page and not by every page visited.
            const onPage = new Set(rows.map((r) => r.book.id));
            detailAskedRef.current = new Set(
              [...detailAskedRef.current].filter((id) => onPage.has(id))
            );
            setDetailsById((prev) => {
              const next = new Map([...prev].filter(([id]) => onPage.has(id)));
              return next.size === prev.size ? prev : next;
            });
            setDetailFailedIds((prev) => {
              const next = new Set([...prev].filter((id) => onPage.has(id)));
              return next.size === prev.size ? prev : next;
            });
          }
          setPageRows(rows);
          landedKeyRef.current = filterQueryKey;
          failedKeyRef.current = null;
          setServerTitleError(null);
          setLoading(false);
        })
        .catch((err: unknown) => {
          if (controller.signal.aborted || fetchId !== fetchIdRef.current || isAbortError(err)) {
            return;
          }
          const message =
            err instanceof Error && err.message
              ? err.message
              : 'Could not load the metadata review queue.';
          // A 400 the server attributes to the Title pattern (field "q": too
          // long, too slow, a syntax RE2 rejects that the lane's compiler let
          // through) is reported under the field, keeps the rows on screen,
          // and lets the next edit try again. Any other 400 (a refused limit,
          // a skipped list the server will not take) is a page error: showing
          // it under the Title box would send the reviewer to edit the wrong
          // thing.
          failedKeyRef.current = filterQueryKey;
          const refused = err as { status?: number; data?: { field?: unknown } } | null;
          if (refused?.status === 400 && refused.data?.field === 'q') {
            setServerTitleError(message);
          } else {
            setError(message);
          }
          setLoading(false);
        });
    };

    if (immediate) {
      issue();
      return () => controller.abort();
    }
    const timer = setTimeout(issue, PAGE_QUERY_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
    // setPage and the reconcile helper are stable; params come from queryKey,
    // and filterQueryKey changes only with it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, queryKey, filterQueryKey, requestedPage, pageSize, refreshKey]);

  // The server already cut the page, and the size of the whole filtered list is
  // its total.
  const filteredCount = pageTotal;
  const totalPages = Math.max(1, Math.ceil(filteredCount / pageSize));

  // Clamp rather than auto-advance: filters can shrink the set below the current
  // page index.
  //
  // DERIVED, not synced in an effect. An effect would render one frame at the
  // out-of-range page before correcting it, and it would cost a second render
  // pass every time a filter changed. `requestedPage` is what the reviewer
  // asked for; `page` is what is actually reachable.
  const page = Math.min(requestedPage, totalPages);

  // This session's own decisions take effect on the rows on screen at once,
  // without waiting for the refresh that follows an apply. (A chip view pauses every filter, these included.)
  // The page then runs short until that refresh refills it.
  const visiblePageRows = useMemo(() => {
    if (chipFilter !== null) return pageRows;
    return pageRows.filter((r) => {
      const state = rowStates.get(r.book.id);
      if (filters.hideApplied && state === 'applied') return false;
      if (filters.hideRejected && state === 'rejected') return false;
      if (filters.hideSkipped && state === 'skipped') return false;
      return true;
    });
  }, [
    pageRows,
    chipFilter,
    rowStates,
    filters.hideApplied,
    filters.hideRejected,
    filters.hideSkipped,
  ]);

  // The page's rows with their full candidates swapped in once fetched. Only
  // the candidate is taken from the detail row, and only when its hash is the
  // index row's: status, stale flag and book info stay the index's, so a
  // detail fetched a moment later can never contradict the filters and counts
  // the page was built from.
  const pageResults = useMemo(
    () =>
      visiblePageRows.map((r) => {
        const d = detailsById.get(r.book.id);
        return detailMatches(r, d) ? { ...r, candidate: d.candidate } : r;
      }),
    [visiblePageRows, detailsById]
  );

  // Per visible book: is its candidate the full row yet? Derived from the
  // same two inputs as the swap above, so it can never say 'loaded' for a row
  // the swap left as the index row. A detail that arrived for a DIFFERENT
  // candidate is 'failed', not 'loaded': the row shows the index candidate,
  // whose breakdown the server no longer has.
  const detailState = useMemo(() => {
    const out = new Map<string, CandidateDetailState>();
    for (const r of visiblePageRows) {
      if (!r.candidate) continue;
      const d = detailsById.get(r.book.id);
      out.set(
        r.book.id,
        d
          ? detailMatches(r, d)
            ? 'loaded'
            : 'failed'
          : detailFailedIds.has(r.book.id)
            ? 'failed'
            : 'pending'
      );
    }
    return out;
  }, [visiblePageRows, detailsById, detailFailedIds]);
  const detailPending = useMemo(() => {
    const out = new Set<string>();
    detailState.forEach((state, id) => state === 'pending' && out.add(id));
    return out;
  }, [detailState]);
  const detailFailed = useMemo(() => {
    const out = new Set<string>();
    detailState.forEach((state, id) => state === 'failed' && out.add(id));
    return out;
  }, [detailState]);

  // Fetch the full rows for the visible page's books that have not been asked
  // for since the list last changed. One request per page change, sized by the
  // page, never by the library. `detailEpochRef` is bumped with every page
  // load (below), so a response that started before it is dropped instead of
  // mixing an older index's details into the new one.
  const detailAskedRef = useRef<Set<string>>(new Set());
  const detailEpochRef = useRef(0);
  // Set when a detail mismatch refetched the index; cleared by a detail
  // response that agrees with its index. A mismatch that survives the
  // refetch warns instead of refetching again, so this can never loop.
  const mismatchRefetchedRef = useRef(false);
  useEffect(() => {
    if (!active) return;
    const missing = visiblePageRows
      .filter((r) => r.candidate && !detailAskedRef.current.has(r.book.id))
      .map((r) => r.book.id);
    if (missing.length === 0) return;
    missing.forEach((id) => detailAskedRef.current.add(id));
    // Asking again puts a previously failed id back in flight.
    setDetailFailedIds((prev) => {
      if (!missing.some((id) => prev.has(id))) return prev;
      const next = new Set(prev);
      missing.forEach((id) => next.delete(id));
      return next;
    });
    const epoch = detailEpochRef.current;
    api
      .getCachedReviewResults(0, 0, false, 'reviewable', { ids: missing })
      .then((data) => {
        if (epoch !== detailEpochRef.current) return;
        const fetched = data.results || [];
        setDetailsById((prev) => {
          const next = new Map(prev);
          for (const r of fetched) next.set(r.book.id, r);
          return next;
        });
        // A book the response left out has nothing to swap in and nothing
        // in flight: it is failed, not pending. (Below, it is also a
        // `changed` row, which refetches the index once.)
        const absent = missing.filter((id) => !fetched.some((r) => r.book.id === id));
        if (absent.length > 0) {
          setDetailFailedIds((prev) => new Set([...prev, ...absent]));
        }
        // A detail whose hash is not the index row's, or a book the server no
        // longer lists, means the server's candidates moved since the index
        // was loaded. The row would show without its description and its
        // Apply would be refused as stale -- so never leave it silently:
        // refetch the index once, and if that still disagrees, say so.
        const byId = new Map(fetched.map((r) => [r.book.id, r]));
        const indexHash = new Map(visiblePageRows.map((r) => [r.book.id, r.candidate_hash]));
        const changed = missing.filter((id) => {
          const d = byId.get(id);
          return !d || d.candidate_hash !== indexHash.get(id);
        });
        if (changed.length === 0) {
          mismatchRefetchedRef.current = false;
          return;
        }
        if (!mismatchRefetchedRef.current) {
          mismatchRefetchedRef.current = true;
          refresh();
          return;
        }
        toast(
          `${changed.length} row(s) on this page changed on the server since the list loaded — refresh to see their current candidates.`,
          'warning'
        );
      })
      .catch(() => {
        // The index rows are still shown, just without their descriptions and
        // breakdowns. Say so (`detailFailed`): left as pending, the evidence
        // panel would spin for ever. Forget the ask so the next page change
        // or refresh tries again.
        if (epoch !== detailEpochRef.current) return;
        missing.forEach((id) => detailAskedRef.current.delete(id));
        setDetailFailedIds((prev) => new Set([...prev, ...missing]));
      });
    // refresh and toast are stable callbacks.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, visiblePageRows]);

  const { groups, groupedBookIds } = useMemo(() => {
    const groupMap = new Map<string, CandidateGroup>();
    for (const r of pageResults) {
      if (!r.candidate || r.status !== 'matched' || ungroupedIds.has(r.book.id)) continue;
      const key = candidateKey(r.candidate);
      if (!groupMap.has(key)) groupMap.set(key, { key, candidate: r.candidate, results: [] });
      groupMap.get(key)!.results.push(r);
    }
    // Only multi-book groups are groups; singletons fall through to rows.
    const multi: CandidateGroup[] = [];
    const ids = new Set<string>();
    for (const g of groupMap.values()) {
      if (g.results.length > 1) {
        multi.push(g);
        g.results.forEach((r) => ids.add(r.book.id));
      }
    }
    return { groups: multi, groupedBookIds: ids };
  }, [pageResults, ungroupedIds]);

  const rows = useMemo(
    () => pageResults.filter((r) => !groupedBookIds.has(r.book.id)),
    [pageResults, groupedBookIds]
  );

  const undecided = useCallback(
    (id: string) => !['applied', 'skipped', 'rejected'].includes(rowStates.get(id) || ''),
    [rowStates]
  );

  const highConfidenceIds = useMemo(
    () =>
      pageResults
        .filter(
          (r) =>
            r.status === 'matched' &&
            r.candidate &&
            r.candidate.score * 100 >= filters.confidenceThreshold &&
            r.candidate.narrator &&
            undecided(r.book.id)
        )
        .map((r) => r.book.id),
    [pageResults, filters.confidenceThreshold, undecided]
  );

  const allVisiblePendingIds = useMemo(
    () =>
      pageResults
        .filter((r) => r.status === 'matched' && r.candidate && undecided(r.book.id))
        .map((r) => r.book.id),
    [pageResults, undecided]
  );

  // --- the apply pipeline ---------------------------------------------------

  const applyQueueRef = useRef<string[]>([]);
  const applyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // The pin of each queued single apply, captured when the row is MARKED, not
  // when the debounced flush runs: a refresh inside the 500ms window can
  // replace the row's candidate, and the pin must be what the owner saw.
  const applyPinsRef = useRef<Map<string, CandidatePin>>(new Map());

  // The pin for the candidate one row is showing right now. A row with no
  // candidate, or no server-issued candidate_hash, gets no pin from the
  // single-row Apply, and the server hard-gates it.
  const rowPinFor = useCallback(
    (id: string): CandidatePin | undefined => {
      const r = pageRows.find((x) => x.book.id === id);
      if (!r?.candidate || !r.candidate_hash) return undefined;
      return pinOfCandidate(r.candidate, r.candidate_hash);
    },
    [pageRows]
  );

  // The pins a bulk button sends: every book it applies is the owner's manual
  // apply (owner ruling 2026-09-27). A book whose row is loaded with a
  // candidate_hash gets a 'review_bulk' pin of the candidate it shows, so a
  // cache refetched since the page loaded is still refused as stale. A book
  // the lane holds no hash for (the selection outlived a refresh that dropped
  // its row, or the row came without a hash) gets the hashless owner marker:
  // still owner-reviewed, on whatever the server's top candidate is -- except
  // that the server never applies a review-only (Open Library / Google Books)
  // candidate or lifts a no_match mark on it: nobody saw that candidate.
  const bulkPinsFor = useCallback(
    (ids: string[]): Record<string, ApplyPin> => {
      const byId = new Map(pageRows.map((r) => [r.book.id, r]));
      const pins: Record<string, ApplyPin> = {};
      for (const id of ids) {
        const r = byId.get(id);
        pins[id] =
          r?.candidate && r.candidate_hash
            ? pinOfCandidate(r.candidate, r.candidate_hash, 'review_bulk')
            : { origin: 'review_bulk' };
      }
      return pins;
    },
    [pageRows]
  );
  // Every in-flight apply arms an hour-long bound (see runApplyOp). They are
  // cleared when their race settles, but an unmount while an op is still
  // running would otherwise leave one armed for the rest of the hour.
  const boundTimeoutsRef = useRef<Set<ReturnType<typeof setTimeout>>>(new Set());

  useEffect(
    () => () => {
      if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
      boundTimeoutsRef.current.forEach((t) => clearTimeout(t));
      boundTimeoutsRef.current.clear();
    },
    []
  );

  // An auth bounce during a batch apply does NOT mean nothing was applied.
  //
  // This used to assert that it did -- every row reverted to 'pending' with a
  // "nothing was applied" toast -- reasoning that a Cloudflare Access bounce
  // never reaches the origin. That holds only while the request is short.
  // Measured in production: a 250-book apply ran 2m0s, the origin returned 200,
  // 67 files were written, and the user was told nothing had happened.
  // Reverting then invites re-applying work that already succeeded. So: keep the
  // detection, report it accurately, and re-read server state rather than guess.
  const handleApplyError = useCallback(
    (err: unknown, requestedIds: string[]) => {
      // A dispatch can fail after some ids were marked in flight (or the poll
      // never runs its finally). Clearing here keeps the refresh below able to
      // correct them rather than leaving them permanently uncorrectable.
      releaseInFlight(requestedIds);
      refresh();
      if (isAuthRedirectError(err)) {
        toast(
          'Session expired — sign in again. Any books already applied were kept; the list has been refreshed.',
          'error'
        );
        return;
      }
      toast(`Failed to start applying ${requestedIds.length} book(s)`, 'error');
    },
    [toast, refresh, releaseInFlight]
  );

  // Follows already-dispatched apply ops to the end and re-reads the list once,
  // however many ops there were: a flush that sent several requests arms ONE
  // bound and ONE refresh, not one per request.
  const followApplyOps = useCallback(
    (opIds: string[], requestedIds: string[]): void => {
      // pollOperationV2 loops forever with no timeout, so an op that never
      // reaches a terminal status (a crashed worker, an unrecognised status)
      // would leave these ids retained permanently -- freezing their optimistic
      // `applied` and reproducing the exact permanent-stale-state bug this
      // reconcile exists to fix, just gated on op completion instead of on an
      // add-only merge. So the protection is bounded.
      //
      // The bound is deliberately generous. Releasing early on a genuinely
      // long apply lets the reconcile revert rows the worker is still
      // processing, which is the flicker ActionBar.tsx disclaims. Between the
      // two failure modes, a visible flicker on a very long op beats rows that
      // are silently hidden forever, so it is bounded rather than left open.
      // Measured reference: 250 books took 2m0s in production.
      let boundTimeout: ReturnType<typeof setTimeout> | undefined;
      void Promise.race([
        Promise.all(opIds.map((opId) => api.pollOperationV2(opId).catch(() => undefined))),
        new Promise((resolve) => {
          boundTimeout = setTimeout(resolve, APPLY_INFLIGHT_MAX_MS);
          boundTimeoutsRef.current.add(boundTimeout);
        }),
      ]).finally(() => {
        // Cancelled on the ordinary path. The race settles as soon as the poll
        // returns, but an uncancelled timer still holds its closure for the
        // full hour, and each per-row apply arms its own. The unmount cleanup
        // clears any that are still armed when the hook goes away.
        if (boundTimeout !== undefined) {
          clearTimeout(boundTimeout);
          boundTimeoutsRef.current.delete(boundTimeout);
        }
        // Released BEFORE the refresh, so the refresh it triggers is the one
        // that gets to correct these rows. Releasing after would let the
        // reconcile skip them and the stale `applied` would survive the very
        // refresh that exists to fix it.
        releaseInFlight(requestedIds);
        if (!aliveRef.current) return;
        refresh();
      });
    },
    [refresh, releaseInFlight]
  );

  // Dispatches the background apply and returns; the bell owns progress.
  // Re-reads the list when the op finishes rather than diffing a client guess.
  const runApplyOp = useCallback(
    async (
      requestedIds: string[],
      pins?: Record<string, ApplyPin>,
      mode?: BulkApplyMode,
      writeBack?: boolean,
      // False when applyMany sends one chunk of a larger apply and announces
      // the whole batch itself, once.
      announce = true
    ): Promise<void> => {
      // mode is passed only by the bulk path (applyMany); the single-row
      // flush sends none, so the server never reads a toggle into it.
      const dispatched =
        mode === undefined
          ? await api.batchApplyFromCache(requestedIds, writeBack, pins)
          : await api.batchApplyFromCache(requestedIds, writeBack, pins, mode);
      if (announce) {
        toast(
          `Metadata apply queued for ${requestedIds.length.toLocaleString()} book(s) — watch the bell for progress.`,
          'success'
        );
      }
      followApplyOps([dispatched.op_id], requestedIds);
    },
    [toast, followApplyOps]
  );

  const flushApplyQueue = useCallback(async () => {
    const ids = [...applyQueueRef.current];
    applyQueueRef.current = [];
    if (ids.length === 0) return;
    // A 'row' pin is one reviewer's look at one book, and the server refuses a
    // request that pairs one with other books (a row pin lifts gates a bulk
    // apply must not). So each row-pinned book goes out as its own request;
    // books with no pin still share one. The dispatches return as
    // soon as the op is queued, so splitting costs a few round trips, not a
    // wait on each apply.
    const rowPinned: Array<[string, ApplyPin]> = [];
    const rest: string[] = [];
    for (const id of ids) {
      const pin = applyPinsRef.current.get(id);
      applyPinsRef.current.delete(id);
      if (pin && pin.origin === 'row') rowPinned.push([id, pin]);
      else rest.push(id);
    }
    const groups: Array<{ ids: string[]; pins: Record<string, ApplyPin> }> = rowPinned.map(
      ([id, pin]) => ({ ids: [id], pins: { [id]: pin } })
    );
    if (rest.length > 0) groups.push({ ids: rest, pins: {} });
    // Dispatch every group, then report ONCE: one follow (one bound, one
    // refresh when the ops end) for what went out, and one failure report for
    // what did not. An auth bounce ends the loop -- every later request would
    // be bounced too -- and the books not yet sent are released with the
    // failed one, so their optimistic mark is corrected by the refresh.
    const opIds: string[] = [];
    const queuedIds: string[] = [];
    const failedIds: string[] = [];
    let failure: unknown;
    for (let i = 0; i < groups.length; i++) {
      try {
        const dispatched = await api.batchApplyFromCache(groups[i].ids, undefined, groups[i].pins);
        opIds.push(dispatched.op_id);
        queuedIds.push(...groups[i].ids);
      } catch (err) {
        failedIds.push(...groups[i].ids);
        if (isAuthRedirectError(err)) {
          failure = err;
          for (const rest of groups.slice(i + 1)) failedIds.push(...rest.ids);
          break;
        }
        failure ??= err;
      }
    }
    if (queuedIds.length > 0) {
      toast(
        `Metadata apply queued for ${queuedIds.length.toLocaleString()} book(s) — watch the bell for progress.`,
        'success'
      );
      followApplyOps(opIds, queuedIds);
    }
    if (failedIds.length > 0) handleApplyError(failure, failedIds);
  }, [followApplyOps, handleApplyError, toast]);

  const applyOne = useCallback(
    (bookId: string) => {
      // A single-row Apply is the owner's review of what the row shows: pin
      // it. No apply goes out without a pin (see pagePinsFor), so a row with no
      // candidate hash is refused here rather than sent unpinned, which would
      // apply whatever candidate is on top through the certainty gate with no
      // stale-candidate check.
      const pin = rowPinFor(bookId);
      if (!pin) {
        toast(
          'This book has no candidate the page can pin, so it was not applied. Refresh and try again.',
          'warning'
        );
        return;
      }
      // Retained at the OPTIMISTIC MARK, not after the dispatch resolves.
      // applyOne marks the row synchronously but its dispatch is behind a 500ms
      // debounce, so retaining later left a window in which any refresh (another
      // apply's terminal poll, a filter change) reverted the row -- the precise
      // flicker ActionBar.tsx rejects useOptimistic to avoid. applyMany has no
      // such window because it retains before dispatching.
      retainInFlight([bookId]);
      clearServerDerived([bookId]);
      setRowStates((prev) => new Map(prev).set(bookId, 'applied'));
      // Several quick clicks share one debounce timer. The flush sends each
      // row-pinned book as its own request (a row pin is one reviewer's look
      // at one book, and the server refuses it beside other books).
      if (pin) applyPinsRef.current.set(bookId, pin);
      applyQueueRef.current.push(bookId);
      if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
      applyTimerRef.current = setTimeout(() => void flushApplyQueue(), 500);
    },
    [flushApplyQueue, retainInFlight, clearServerDerived, rowPinFor, toast]
  );

  const applyMany = useCallback(
    async (
      bookIds: string[],
      // A confirmed Replace passes what it captured at the click (see
      // PendingReplace); every other bulk apply captures them here, now.
      captured?: { pins: Record<string, ApplyPin>; mode: BulkApplyMode }
    ) => {
      if (bookIds.length === 0) return;
      setApplying(true);
      retainInFlight(bookIds);
      clearServerDerived(bookIds);
      try {
        // Apply page, Apply high confidence, group Apply All and Apply
        // selected are the owner's manual apply too (owner ruling
        // 2026-09-27): every book is pinned, so the server lifts the same
        // certainty refusals a single-row Apply lifts. Unlike a single row,
        // a bulk apply is fill-only (A3#3) unless the owner switched the
        // bulk toggle to 'replace' (owner ruling 2026-09-27), which is sent
        // with every bulk request. Pins are captured now, at the click, from
        // the rows the owner is looking at.
        //
        // Sent in APPLY_CHUNK_SIZE requests: "select all matching" can hand
        // this thousands of books, past both the JSON body limit and the
        // server's bulk apply cap. Each chunk is its own background op; the
        // bar shows one progress count across them.
        const pins = captured?.pins ?? bulkPinsFor(bookIds);
        const mode = captured?.mode ?? bulkApplyMode;
        const chunks = chunk(bookIds, APPLY_CHUNK_SIZE);
        let sent = 0;
        try {
          for (const ids of chunks) {
            if (chunks.length > 1) {
              setBulkProgress({ label: 'Queuing apply', done: sent, total: bookIds.length });
            }
            const chunkPins: Record<string, ApplyPin> = {};
            ids.forEach((id) => {
              if (pins[id]) chunkPins[id] = pins[id];
            });
            await runApplyOp(ids, chunkPins, mode, undefined, chunks.length === 1);
            sent += ids.length;
          }
        } catch (err) {
          // The chunks already sent belong to their ops (each releases its own
          // ids when it settles); only the unsent remainder is released here.
          const unsent = bookIds.slice(sent);
          if (sent > 0) {
            setRowStates((prev) => {
              const next = new Map(prev);
              bookIds.slice(0, sent).forEach((id) => next.set(id, 'applied'));
              return next;
            });
            toast(
              `Queued ${sent.toLocaleString()} of ${bookIds.length.toLocaleString()} book(s) before a request failed.`,
              'warning'
            );
          }
          handleApplyError(err, unsent);
          return;
        } finally {
          setBulkProgress(null);
        }
        if (chunks.length > 1) {
          toast(
            `Metadata apply queued for ${bookIds.length.toLocaleString()} book(s) in ` +
              `${chunks.length} batches — watch the bell for progress.`,
            'success'
          );
        }
        // Dispatch acceptance is the point at which this batch belongs to the
        // background worker. Mark each row now so the default Hide applied
        // filter clears it immediately; the terminal poll then refreshes and
        // restores any book the worker ultimately did not apply.
        //
        // That restore is real now. It was not before: the refresh seeded row
        // state add-only, so it could never overwrite the `applied` set here and
        // a failed book stayed hidden forever. See the reconcile in the fetch
        // effect and inFlightApplyRef.
        setRowStates((prev) => {
          const next = new Map(prev);
          bookIds.forEach((bookId) => next.set(bookId, 'applied'));
          return next;
        });
        // The refresh re-derives every row from the server, so clearing the
        // whole selection is safe: anything that did not apply comes back
        // pending and can be re-selected.
        setSelectedIds(new Set());
      } catch (err) {
        handleApplyError(err, bookIds);
      } finally {
        setApplying(false);
      }
    },
    [
      runApplyOp,
      handleApplyError,
      retainInFlight,
      clearServerDerived,
      bulkPinsFor,
      bulkApplyMode,
      toast,
    ]
  );

  const reject = useCallback(
    async (bookId: string) => {
      try {
        await api.markNoMatch(bookId);
        clearServerDerived([bookId]);
        setRowStates((prev) => new Map(prev).set(bookId, 'rejected'));
        toast('Candidate rejected — will be excluded from future fetches', 'info', {
          label: 'Undo',
          onClick: () => {
            void (async () => {
              try {
                await api.clearMetadataNoMatch(bookId);
                setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
                toast('Rejection undone', 'success');
              } catch {
                toast('Failed to undo rejection', 'error');
              }
            })();
          },
        });
      } catch {
        toast('Failed to reject', 'error');
      }
    },
    [toast, clearServerDerived]
  );

  const unreject = useCallback(
    async (bookId: string) => {
      try {
        await api.clearMetadataNoMatch(bookId);
        clearServerDerived([bookId]);
        setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
        toast('Rejection undone', 'success');
      } catch {
        toast('Failed to undo rejection', 'error');
      }
    },
    [toast, clearServerDerived]
  );

  // "Select all N matching" in page mode holds the ids the server listed, the
  // candidate pin it listed each one with (ids=all&pins=true; null when the
  // list was past the bulk apply cap and the server sent none), and the
  // generation of the PAGE on screen when they arrived. State, not refs, so the
  // list and the selection it filled commit together.
  const [matchingIds, setMatchingIds] = useState<{
    key: string;
    ids: string[];
    pins: Record<string, Omit<CandidatePin, 'origin'>> | null;
    pageGeneration: string | null;
    /**
     * The listAllMatching request a refused action started to list these books
     * again. It counts as "still being listed" only while it is the newest
     * list request (selectAllSeqRef) and has not settled: every way it ends
     * clears it, and a newer request (a Select all) supersedes it, so the lane
     * can never be left refusing on it.
     */
    relistSeq?: number;
  } | null>(null);
  const matchingIdSet = useMemo(
    () => (matchingIds ? new Set(matchingIds.ids) : null),
    [matchingIds]
  );

  // Page mode: the pin of every selected book seen on screen (see the effect
  // that fills it, below).
  const tickedPinsRef = useRef<Map<string, CandidatePin | null>>(new Map());

  // Page mode: lists every id the current filters match, with pins, and adds
  // them to the selection. A filter change while the request is out discards
  // it: its ids answer a question the reviewer no longer asked.
  //
  // 'add' is the reviewer's own "Select all N matching": every listed id is
  // added. 'intersect' is the re-list after a refusal (refuseStaleSelectAll):
  // it NEVER widens the selection. A book the reviewer unticked after
  // selecting all stays unticked, a book that joined the queue since is not
  // added, and a book of the old list that left the queue is unselected; the
  // toast counts the last two. Hand-picked books outside the old list are
  // kept as they are.
  const selectAllSeqRef = useRef(0);
  const listAllMatching = useCallback(
    (mode: 'add' | 'intersect' = 'add', previous?: ReadonlySet<string>): number => {
      const seq = ++selectAllSeqRef.current;
      const { params } = latestQueryRef.current;
      const key = latestQueryKeyRef.current;
      // A re-list that does not land keeps the list it was replacing, pins and
      // all: dropping it would turn every off-page book into "no pin" and send
      // the reviewer to Select all, which re-adds what they unticked. Only the
      // "being listed" mark is cleared, so the next click tries again.
      const endRelist = () =>
        setMatchingIds((prev) =>
          prev && prev.relistSeq === seq ? { ...prev, relistSeq: undefined } : prev
        );
      api
        .getReviewMatchingIds(params, undefined, { pins: true })
        .then((data) => {
          if (seq !== selectAllSeqRef.current) {
            endRelist();
            return;
          }
          if (key !== latestQueryKeyRef.current) {
            // The filters changed while the list was out; its ids answer a
            // question the reviewer no longer asked. The selection prune
            // re-checks membership, and the next click re-lists if needed.
            if (mode === 'intersect') endRelist();
            return;
          }
          if (mode === 'intersect' && selectedIdsRef.current.size === 0) {
            // Cleared while the re-list was out: there is nothing to narrow,
            // and no list to hold for an empty selection.
            setMatchingIds(null);
            return;
          }
          setMatchingIds({
            key,
            ids: data.ids,
            // No pins field and no pins_omitted is a server that does not serve
            // pins: treated as "no pin for any of them", so an apply is refused
            // rather than sent hashless.
            pins: data.pins_omitted ? null : (data.pins ?? null),
            pageGeneration: generationRef.current,
          });
          if (mode === 'add') {
            setSelectedIds((prev) => {
              const next = new Set(prev);
              data.ids.forEach((id) => next.add(id));
              return next;
            });
            return;
          }
          const old = previous ?? new Set<string>();
          const listed = new Set(data.ids);
          const selected = selectedIdsRef.current;
          let left = 0;
          selected.forEach((id) => {
            if (old.has(id) && !listed.has(id)) left += 1;
          });
          // Joined AND not selected: a book that joined and was ticked by
          // hand meanwhile is in the selection, so it is not reported as left
          // out.
          let joined = 0;
          data.ids.forEach((id) => {
            if (!old.has(id) && !selected.has(id)) joined += 1;
          });
          setSelectedIds((prev) => {
            const next = new Set([...prev].filter((id) => !old.has(id) || listed.has(id)));
            return next.size === prev.size ? prev : next;
          });
          toast(relistSummary(left, joined), 'info');
        })
        .catch(() => {
          if (seq !== selectAllSeqRef.current) {
            endRelist();
            return;
          }
          if (mode === 'intersect') {
            endRelist();
            toast(
              'Could not list the matching books again; your selection is unchanged. Apply again to retry.',
              'error'
            );
            return;
          }
          toast('Could not list the matching books.', 'error');
        });
      return seq;
    },
    [toast]
  );

  // A "select all" list is a statement about the queue as the page showed it
  // when the list arrived. If a page of another generation has landed since
  // (a refresh, a page turn after background writes), the list may name books
  // that left the queue or miss ones that joined it. So an action that relies
  // on the list -- it holds a book that is in the list, is not on the page and
  // was never on screen while selected (tickedPinsRef), i.e. whose only pin
  // is the list's -- is refused ONCE while the list is fetched again and the
  // selection narrowed to it (listAllMatching 'intersect', never widened); the
  // next click goes through. Apply page, Apply high confidence and a group's
  // Apply All send rows on the page, which carry fresh pins, so they never
  // trip this. Membership only: each book's candidate staleness is the pins'
  // job, and the server refuses a pin whose candidate changed
  // (stale_candidate), including after writes later than any page fetch.
  const refuseStaleSelectAll = useCallback(
    (ids: string[]): boolean => {
      if (!matchingIds || !matchingIdSet) return false;
      // Rows the reviewer can see: a row this session hid (Hide applied,
      // rejected or skipped) is on the page but not shown.
      const onPage = new Set(visiblePageRows.map((r) => r.book.id));
      const seen = tickedPinsRef.current;
      const relies = ids.some((id) => matchingIdSet.has(id) && !onPage.has(id) && !seen.has(id));
      if (!relies) return false;
      if (
        matchingIds.relistSeq !== undefined &&
        matchingIds.relistSeq === selectAllSeqRef.current
      ) {
        toast(
          'The matching books are still being listed again for the current queue; apply again in a moment.',
          'warning'
        );
        return true;
      }
      if (matchingIds.pageGeneration === generationRef.current) return false;
      const relistSeq = listAllMatching('intersect', matchingIdSet);
      setMatchingIds({ ...matchingIds, relistSeq });
      toast(
        'The queue changed after you selected all, so the matching books are being listed again ' +
          'for the current queue. Your selection is only narrowed, never added to. Check it, then ' +
          'apply again.',
        'warning'
      );
      return true;
    },
    [matchingIds, matchingIdSet, visiblePageRows, listAllMatching, toast]
  );
  // The remembered list belongs to a selection; an emptied selection (Clear,
  // an apply, a search) forgets it.
  useEffect(() => {
    if (selectedIds.size > 0) return;
    setMatchingIds((prev) => (prev === null ? prev : null));
  }, [selectedIds]);

  // A bulk action right after a filter change would act on books
  // the new filters exclude, because both halves of the view catch up
  // asynchronously. The selection is pruned against the server after a
  // debounce and an ids request (pruneSettledKeyRef), and the page keeps the
  // previous filters' rows until the new page lands (landedKeyRef), which is
  // what Apply page / Apply high confidence / a group's Apply All act on.
  // Until both have caught up, bulk apply, Replace and reject are refused with
  // a message; the next click after they settle goes through.
  const refuseUnsettled = useCallback((): boolean => {
    if (pruneSettledKeyRef.current !== filtersKey) {
      toast(
        'The selection is being checked against the new filters; apply again in a moment.',
        'warning'
      );
      return true;
    }
    if (landedKeyRef.current !== filterQueryKey) {
      if (failedKeyRef.current === filterQueryKey) {
        toast(
          'The page for the current filters did not load, so there is nothing current to act on. ' +
            'Retry, or change the filters.',
          'warning'
        );
        return true;
      }
      toast(
        'The list has not caught up with the new filters yet; apply again once it has.',
        'warning'
      );
      return true;
    }
    return false;
  }, [filtersKey, filterQueryKey, toast]);

  // The pin of every book that was selected while its row was on screen, as
  // last shown (null: the row had no candidate to pin). A bulk apply of a book
  // on another page sends this pin, so the server still refuses it as stale if
  // its candidate has changed since. Only this effect writes the map, and it is
  // bounded by the rows visited, not the selection.
  useEffect(() => {
    const ticked = tickedPinsRef.current;
    if (selectedIds.size === 0) {
      ticked.clear();
      return;
    }
    for (const id of [...ticked.keys()]) {
      if (!selectedIds.has(id)) ticked.delete(id);
    }
    // Every selected row on screen is (re)captured, so the pin is the
    // candidate the reviewer saw LAST: a refresh that replaces a row's
    // candidate while it is on the page replaces its pin too.
    // visiblePageRows, not pageRows: a row the session hid was not seen.
    for (const r of visiblePageRows) {
      const id = r.book.id;
      if (!selectedIds.has(id)) continue;
      ticked.set(
        id,
        r.candidate && r.candidate_hash
          ? pinOfCandidate(r.candidate, r.candidate_hash, 'review_bulk')
          : null
      );
    }
  }, [selectedIds, visiblePageRows]);

  // The pins a page-mode bulk apply sends. Each book's pin comes from, in
  // order: its row on the current page (what the reviewer is looking at now),
  // the pin captured when it was ticked, or the pin the "select all" list
  // came with. A book none of them knows is `unknown`: the lane never sends
  // the hashless review_bulk marker, because that
  // marker applies whatever the top candidate is by then with no staleness
  // check, and it would be sent for every book off the page. A book
  // known to have no candidate is `noCandidate`: there is nothing to apply.
  const pagePinsFor = useCallback(
    (ids: string[]) => {
      const onPage = new Map(pageRows.map((r) => [r.book.id, r]));
      const ticked = tickedPinsRef.current;
      const pins: Record<string, ApplyPin> = {};
      const unknown: string[] = [];
      const noCandidate: string[] = [];
      for (const id of ids) {
        const r = onPage.get(id);
        if (r) {
          if (r.candidate && r.candidate_hash) {
            pins[id] = pinOfCandidate(r.candidate, r.candidate_hash, 'review_bulk');
          } else {
            noCandidate.push(id);
          }
          continue;
        }
        const t = ticked.get(id);
        if (t !== undefined) {
          if (t) pins[id] = t;
          else noCandidate.push(id);
          continue;
        }
        if (matchingIds?.pins && matchingIdSet?.has(id)) {
          const p = matchingIds.pins[id];
          if (p) pins[id] = { ...p, origin: 'review_bulk' };
          else noCandidate.push(id);
          continue;
        }
        unknown.push(id);
      }
      return { pins, unknown, noCandidate };
    },
    [pageRows, matchingIds, matchingIdSet]
  );

  // Skip all unmatched, page mode: the unmatched books are not in the page, so
  // their ids come from the server (ids only).
  const skipAllUnmatchedPage = useCallback(async () => {
    try {
      const { ids } = await api.getReviewMatchingIds({ chip: 'no_match' });
      clearServerDerived(ids);
      setRowStates((prev) => {
        const next = new Map(prev);
        ids.forEach((id) => markSkipped(next, id));
        return next;
      });
    } catch {
      toast('Could not list the unmatched books.', 'error');
    }
  }, [clearServerDerived, toast]);

  /**
   * Every action the metadata lane supports, in one place.
   *
   * The union is exhaustive and the switch has no `default`, so an action added
   * to `MetadataAction` and not handled here is a compile error rather than a
   * button that silently does nothing.
   */
  const dispatch = useCallback(
    (action: MetadataAction) => {
      switch (action.type) {
        case 'apply':
          applyOne(action.id);
          return;
        case 'applySelected': {
          if (refuseUnsettled() || refuseStaleSelectAll(action.ids)) return;
          // The server's bulk apply fail-safe applies to the WHOLE apply, not
          // to each request: applyMany chunks for the body limit, and that
          // must never become a way around the cap. Refused here, before any
          // request or Replace prompt, for every bulk entry point.
          if (action.ids.length > applyCap) {
            toast(applyCapMessage(applyCap, action.ids.length), 'error');
            return;
          }
          // Every book is pinned or nothing is sent (pagePinsFor): a book
          // with no pin would go out with the hashless marker and lose the
          // stale-candidate refusal. Checked here, before a Replace is parked,
          // so neither path can send one.
          let ids = action.ids;
          const r = pagePinsFor(ids);
          if (r.unknown.length > 0) {
            toast(unpinnedRefusalMessage(r.unknown.length), 'warning');
            return;
          }
          if (r.noCandidate.length > 0) {
            const left = new Set(r.noCandidate);
            ids = ids.filter((id) => !left.has(id));
            toast(
              r.noCandidate.length === 1
                ? '1 selected book has no candidate to apply and was left out.'
                : `${r.noCandidate.length.toLocaleString()} selected books have no candidate to ` +
                    'apply and were left out.',
              'info'
            );
            if (ids.length === 0) return;
          }
          const pins = r.pins;
          // Replace overwrites values the owner may have curated, on every
          // book in the batch, so it asks first -- here, not in each button,
          // so every bulk entry point confirms exactly once. The request is
          // parked for the workspace's dialog; confirmReplace sends it. Fill
          // applies as before, without a prompt, and so does Replace once the
          // owner has ticked "Don't ask me again".
          if (bulkApplyMode === 'replace' && ids.length > 0 && !skipReplaceConfirm) {
            setPendingReplace({
              ids,
              pins,
              mode: bulkApplyMode,
            });
            return;
          }
          void applyMany(ids, { pins, mode: bulkApplyMode });
          return;
        }
        case 'reject':
          void reject(action.id);
          return;
        case 'unreject':
          void unreject(action.id);
          return;
        case 'skip':
          clearServerDerived([action.id]);
          setRowStates((prev) => markSkipped(new Map(prev), action.id));
          return;
        case 'unskip':
          clearServerDerived([action.id]);
          setRowStates((prev) => new Map(prev).set(action.id, 'pending'));
          return;
        case 'rejectGroup':
          // Its ids are rows on the page, which may still answer the
          // previous filters (refuseUnsettled); reject writes no-match marks.
          if (refuseUnsettled()) return;
          void (async () => {
            try {
              await Promise.all(action.ids.map((id) => api.markNoMatch(id)));
              clearServerDerived(action.ids);
              setRowStates((prev) => {
                const next = new Map(prev);
                action.ids.forEach((id) => next.set(id, 'rejected'));
                return next;
              });
            } catch {
              toast('Failed to reject group', 'error');
            }
          })();
          return;
        case 'skipAllUnmatched':
          void skipAllUnmatchedPage();
          return;
        case 'ungroup':
          setUngrouped((prev) => ({
            page: requestedPage,
            ids: new Set(prev.page === requestedPage ? prev.ids : []).add(action.id),
          }));
          return;
      }
    },
    [
      applyOne,
      applyMany,
      reject,
      unreject,
      skipAllUnmatchedPage,
      refuseUnsettled,
      refuseStaleSelectAll,
      pagePinsFor,
      toast,
      requestedPage,
      clearServerDerived,
      bulkApplyMode,
      skipReplaceConfirm,
      applyCap,
    ]
  );

  const confirmReplace = useCallback(
    (dontAskAgain: boolean) => {
      const pending = pendingReplace;
      setPendingReplace(null);
      if (!pending) return;
      if (refuseUnsettled() || refuseStaleSelectAll(pending.ids)) return;
      // Only stop asking when storage kept the flag: blocked storage means
      // the prompt stays, the safe default.
      if (dontAskAgain && saveSkipReplaceConfirm(true)) setSkipReplaceConfirm(true);
      void applyMany(pending.ids, { pins: pending.pins, mode: pending.mode });
    },
    [pendingReplace, applyMany, refuseUnsettled, refuseStaleSelectAll]
  );

  const cancelReplace = useCallback(() => setPendingReplace(null), []);

  const resetReplaceConfirm = useCallback(() => {
    saveSkipReplaceConfirm(false);
    setSkipReplaceConfirm(false);
  }, []);

  const toggleSelect = useCallback((bookId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(bookId)) next.delete(bookId);
      else next.add(bookId);
      return next;
    });
  }, []);

  const setSelection = useCallback((ids: string[], selected: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      ids.forEach((id) => (selected ? next.add(id) : next.delete(id)));
      return next;
    });
  }, []);

  // The ids (never rows) and their pins come from the server (listAllMatching).
  const selectAllMatching = useCallback(() => {
    listAllMatching();
  }, [listAllMatching]);

  const clearSelection = useCallback(() => setSelectedIds(new Set()), []);

  // Every id of the list the server returned for THESE filters is selected, and
  // that list is the whole filtered set.
  const allMatchingSelected = useMemo(
    () =>
      matchingIds !== null &&
      matchingIds.key === queryKey &&
      matchingIds.ids.length > 0 &&
      matchingIds.ids.length === pageTotal &&
      matchingIds.ids.every((id) => selectedIds.has(id)),
    [matchingIds, queryKey, pageTotal, selectedIds]
  );

  // SELECTION ACROSS FILTER CHANGES. The selection is a plain id set, so it
  // survives page turns and chip toggles untouched (a chip is a view onto the
  // lane, and "select in the no-candidates chip, then look at the stale chip"
  // must not lose the first). When a FILTER changes -- a switch, the level,
  // the title regex, the provider, the threshold -- ids the new filter no
  // longer matches are dropped, so a bulk action never reaches books the
  // reviewer can no longer see.
  //
  // The server answers it; the lane holds no list to check against. Outside a
  // chip, ids the new filters no longer list are dropped: the selection is
  // checked against the ids the filters match now (one ids-only request, after
  // the same debounce as the page). If that check cannot be made the selection
  // is cleared, because keeping books the reviewer can no longer see is what
  // this prune exists to prevent. While a chip is active every filter but the
  // Title regex is paused, so only a Title change prunes, and only books of
  // that chip: those the narrowed chip no longer lists.
  // An empty or invalid regex prunes nothing, so half-typing a pattern never
  // wipes a selection.
  const prunedPageKeyRef = useRef(filtersKey);
  const prunedPageTitleRef = useRef(filters.titleFilter);
  useEffect(() => {
    if (prunedPageKeyRef.current === filtersKey) return;
    prunedPageKeyRef.current = filtersKey;
    const titleChanged = prunedPageTitleRef.current !== filters.titleFilter;
    prunedPageTitleRef.current = filters.titleFilter;
    // Every way this run ends without being superseded marks the selection as
    // checked for THESE filters, so refuseUnsettled lets bulk actions through
    // again; a superseded run (aborted) leaves that to the run that replaced it.
    const key = filtersKey;
    const settle = () => {
      pruneSettledKeyRef.current = key;
    };
    // Nothing selected: nothing the new filters could exclude. Books selected
    // from here on are picked from the new view.
    if (selectedIdsRef.current.size === 0) {
      settle();
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(() => {
      if (selectedIdsRef.current.size === 0) {
        settle();
        return;
      }
      const { params, chip } = latestQueryRef.current;
      const run = async () => {
        try {
          if (chip === null) {
            const { ids } = await api.getReviewMatchingIds(params, controller.signal);
            if (controller.signal.aborted) return;
            const keep = new Set(ids);
            setSelectedIds((prev) => {
              const next = new Set([...prev].filter((id) => keep.has(id)));
              return next.size === prev.size ? prev : next;
            });
            settle();
            return;
          }
          if (!titleChanged || !params.q) {
            settle();
            return;
          }
          const [base, narrowed] = await Promise.all([
            api.getReviewMatchingIds({ chip }, controller.signal),
            api.getReviewMatchingIds(params, controller.signal),
          ]);
          if (controller.signal.aborted) return;
          const inChip = new Set(base.ids);
          const keep = new Set(narrowed.ids);
          setSelectedIds((prev) => {
            const next = new Set([...prev].filter((id) => !inChip.has(id) || keep.has(id)));
            return next.size === prev.size ? prev : next;
          });
          settle();
        } catch (err) {
          if (controller.signal.aborted || isAbortError(err)) return;
          settle();
          // A chip view keeps its selection when the check fails, as it does for
          // a pattern that does not compile; an unchecked filtered selection is
          // not kept.
          if (chip !== null) return;
          setSelectedIds(new Set());
          toastRef.current(
            'Could not re-check the selection against the new filters, so it was cleared.',
            'warning'
          );
        }
      };
      void run();
    }, PAGE_QUERY_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [filtersKey, filters.titleFilter]);

  const skipSelected = useCallback(() => {
    const ids = [...selectedIds];
    clearServerDerived(ids);
    setRowStates((prev) => {
      const next = new Map(prev);
      ids.forEach((id) => markSkipped(next, id));
      return next;
    });
    setSelectedIds(new Set());
  }, [selectedIds, clearServerDerived]);

  const rejectSelected = useCallback(async (): Promise<number> => {
    const ids = [...selectedIds];
    if (ids.length === 0) return 0;
    if (refuseUnsettled() || refuseStaleSelectAll(ids)) return 0;
    const failed: string[] = [];
    let done = 0;
    try {
      for (const batch of chunk(ids, PER_BOOK_CONCURRENCY)) {
        setBulkProgress({ label: 'Rejecting', done, total: ids.length });
        const settled = await Promise.allSettled(batch.map((id) => api.markNoMatch(id)));
        settled.forEach((s, j) => {
          if (s.status === 'rejected') failed.push(batch[j]);
        });
        done += batch.length;
      }
    } finally {
      setBulkProgress(null);
    }
    const ok = ids.filter((id) => !failed.includes(id));
    clearServerDerived(ok);
    setRowStates((prev) => {
      const next = new Map(prev);
      ok.forEach((id) => next.set(id, 'rejected'));
      return next;
    });
    setSelectedIds(new Set(failed));
    toast(
      failed.length
        ? `Rejected ${ok.length.toLocaleString()}; ${failed.length.toLocaleString()} failed and stay selected.`
        : `Rejected ${ok.length.toLocaleString()} book(s).`,
      failed.length ? 'warning' : 'info'
    );
    return failed.length;
  }, [selectedIds, clearServerDerived, toast, refuseUnsettled, refuseStaleSelectAll]);

  const toggleExpand = useCallback(
    (bookId: string) => setExpandedId((prev) => (prev === bookId ? null : bookId)),
    []
  );

  const spineCtx: SpineContext = useMemo(
    () => ({
      rowState: (id) => rowStates.get(id),
      isSelected: (id) => selectedIds.has(id),
      onToggleSelect: toggleSelect,
      onPreviewCover: setPreviewCover,
      onAction: dispatch,
      expandedId,
      onToggleExpand: toggleExpand,
      // A book not on this page (or without a candidate) has no detail to
      // wait for; 'loaded' lets the panel judge the candidate it was given.
      detailState: (id) => detailState.get(id) ?? 'loaded',
      bulkApplyMode,
    }),
    [
      rowStates,
      selectedIds,
      toggleSelect,
      dispatch,
      expandedId,
      toggleExpand,
      detailState,
      bulkApplyMode,
    ]
  );

  const [refetching, setRefetching] = useState(false);
  // The one request path for both refetch entry points. The toast reports the
  // server's book_count, not a client count: for {stale: true} the client never
  // held the set, and for explicit ids the server may have left some to a
  // fetch already running (`skipped`).
  const startRefetch = useCallback(
    async (req: api.BatchFetchRequest): Promise<string | null> => {
      setRefetching(true);
      try {
        const resp = await api.batchFetchCandidates(req);
        if (!resp.operation_id) {
          // The server declining to start is not a failure: it means these
          // books are already in a running fetch, or there is nothing stale.
          // Say which it was.
          toast(resp.message ?? 'Those books are already being fetched.', 'info');
          return null;
        }
        const count = resp.book_count ?? resp.total_books ?? 0;
        const skipped = resp.skipped ?? 0;
        toast(
          `Refetching metadata for ${count.toLocaleString()} book${count !== 1 ? 's' : ''}` +
            (skipped > 0 ? ` (${skipped.toLocaleString()} already being fetched)` : '') +
            ' — watch the operations list for progress.',
          'info'
        );
        return resp.operation_id;
      } catch {
        toast('Failed to start the metadata refetch.', 'error');
        return null;
      } finally {
        setRefetching(false);
      }
    },
    [toast]
  );

  const refetchBooks = useCallback(
    async (ids: string[]): Promise<string | null> => {
      // Defensive depth on the hook's public surface, not a UI path: the only
      // caller is the per-row refresh, which always passes one id. It stays
      // because `refetchBooks` is exported on MetadataLane and an empty POST is
      // a worse failure than an early return.
      if (!ids.length) return null;
      return startRefetch({ book_ids: ids });
    },
    [startRefetch]
  );

  const refetchStale = useCallback(() => startRefetch({ stale: true }), [startRefetch]);

  // Books known to have no candidate (pageUnreviewableIds) are kept out of
  // Apply selected: there is nothing to send for them.
  const applicableSelectedIds = useMemo(
    () => [...selectedIds].filter((id) => !pageUnreviewableIds.has(id)),
    [selectedIds, pageUnreviewableIds]
  );

  const searchAgain = useCallback(
    async (
      ids: string[],
      confirm: (message: string) => Promise<boolean>
    ): Promise<string | null> => {
      if (!ids.length) return null;
      // The lane knows only the page's rows: a book selected across pages that
      // is marked no-match is not recognised here, so the search skips it (the
      // fetch op never searches a no-match book) without the prompt.
      const byId = new Map<string, CandidateResult>();
      for (const r of pageRows) byId.set(r.book.id, r);
      let marked = ids.filter((id) => isMarkedNoMatch(byId.get(id), rowStates.get(id)));
      if (marked.length > 0) {
        const others = ids.length - marked.length;
        const ok = await confirm(
          `${marked.length.toLocaleString()} of the ${ids.length.toLocaleString()} selected ` +
            `book${ids.length !== 1 ? 's are' : ' is'} marked no-match, and a search skips ` +
            'those.\n\n' +
            `OK: clear their no-match mark and search all ${ids.length.toLocaleString()}.\n` +
            (others > 0
              ? `Cancel: search only the other ${others.toLocaleString()}.`
              : 'Cancel: search nothing.')
        );
        if (!ok) {
          if (others === 0) return null;
          const skip = new Set(marked);
          ids = ids.filter((id) => !skip.has(id));
          marked = [];
        }
      }
      setSearching(true);
      try {
        // Cleared four at a time: one request per book, and the owner may have
        // selected hundreds.
        const failed: string[] = [];
        let cleared = 0;
        for (const batch of chunk(marked, PER_BOOK_CONCURRENCY)) {
          setBulkProgress({
            label: 'Clearing no-match marks',
            done: cleared,
            total: marked.length,
          });
          const settled = await Promise.allSettled(batch.map((id) => api.clearMetadataNoMatch(id)));
          settled.forEach((s, j) => {
            if (s.status === 'rejected') failed.push(batch[j]);
          });
          cleared += batch.length;
        }
        if (marked.length > 0) {
          clearServerDerived(marked);
          setRowStates((prev) => {
            const next = new Map(prev);
            marked.forEach((id) => {
              if (!failed.includes(id)) next.delete(id);
            });
            return next;
          });
        }
        if (failed.length > 0) {
          toast(
            `Could not clear the no-match mark on ${failed.length.toLocaleString()} book(s); ` +
              'the search will skip them.',
            'warning'
          );
        }
        // FETCH_CHUNK_SIZE ids per request, one background op each; the
        // list reloads once, after every op has settled.
        const opIds: string[] = [];
        const chunks = chunk(ids, FETCH_CHUNK_SIZE);
        let sent = 0;
        let lastMessage: string | undefined;
        for (const part of chunks) {
          if (chunks.length > 1) {
            setBulkProgress({ label: 'Queuing search', done: sent, total: ids.length });
          }
          // One book searched is a person waiting on it: interactive, so it
          // may use the reserved Google Books quota. Decided by the whole
          // set, never per chunk (a large set's last chunk can hold one).
          const resp = await api.batchFetchCandidates({
            book_ids: part,
            force: true,
            ...(ids.length === 1 ? { interactive: true } : {}),
          });
          if (resp.operation_id) opIds.push(resp.operation_id);
          else lastMessage = resp.message;
          sent += part.length;
        }
        if (opIds.length === 0) {
          toast(lastMessage ?? 'Those books are already being searched.', 'info');
          return null;
        }
        toast(
          `Searching again for ${ids.length.toLocaleString()} book${ids.length !== 1 ? 's' : ''} ` +
            "with each book's title and author" +
            (opIds.length > 1 ? ` in ${opIds.length} batches` : '') +
            ' — watch the bell for progress. The list reloads when the search finishes.',
          'info'
        );
        setSelectedIds(new Set());
        void Promise.allSettled(opIds.map((id) => api.pollOperationV2(id))).finally(() => {
          if (!aliveRef.current) return;
          refresh();
        });
        return opIds[0];
      } catch (err) {
        if (isAuthRedirectError(err)) {
          toast('Session expired — sign in again, then search again.', 'error');
        } else {
          toast('Failed to start the search.', 'error');
        }
        return null;
      } finally {
        setSearching(false);
        setBulkProgress(null);
      }
    },
    [pageRows, rowStates, toast, refresh, clearServerDerived]
  );

  const unmatchedCount = pageFacets?.no_match ?? 0;

  const heldRows = useMemo(
    () => ({ list: pageRows.length, details: detailsById.size }),
    [pageRows, detailsById]
  );

  // Every row the filters match, for an export. Page mode pages through the
  // server at the largest page it allows; the evaluation is shared, so this is
  // one evaluation and a run of cheap cuts -- as long as nothing writes. Each
  // page is its own request, and a write between two of them moves the
  // generation and shifts the offsets, so the pages would repeat or skip rows.
  // So every page must carry the first page's generation: on a change the
  // export starts over once, and a second change stops it with an error
  // rather than a file that is quietly wrong. Only one export runs: starting
  // another aborts the one in flight (the workspace also disables the command
  // while `exporting`), and an unmount aborts it too.
  const [exporting, setExporting] = useState(false);
  const exportAbortRef = useRef<AbortController | null>(null);
  useEffect(() => () => exportAbortRef.current?.abort(), []);
  const exportRows = useCallback(async (): Promise<CandidateResult[]> => {
    exportAbortRef.current?.abort();
    const controller = new AbortController();
    exportAbortRef.current = controller;
    setExporting(true);
    const { params } = latestQueryRef.current;
    try {
      for (let attempt = 0; attempt < EXPORT_ATTEMPTS; attempt++) {
        const out: CandidateResult[] = [];
        let generation: string | null = null;
        let moved = false;
        for (let offset = 0; ; offset += EXPORT_PAGE_SIZE) {
          const data = await api.getReviewPage(
            { ...params, limit: EXPORT_PAGE_SIZE, offset },
            controller.signal
          );
          if (generation === null) generation = data.generation;
          else if (data.generation !== generation) {
            moved = true;
            break;
          }
          for (const r of data.results) out.push(r);
          if (data.results.length === 0 || offset + data.results.length >= data.total_count) break;
        }
        if (!moved) return out;
      }
      throw new Error(EXPORT_QUEUE_CHANGED_MESSAGE);
    } finally {
      if (exportAbortRef.current === controller) {
        exportAbortRef.current = null;
        if (aliveRef.current) setExporting(false);
      }
    }
  }, []);

  return {
    loading,
    error,
    results: pageRows,
    filteredResults: visiblePageRows,
    filteredCount,
    unmatchedCount,
    heldRows,
    exportRows,
    exporting,
    pageResults,
    groups,
    groupedBookIds,
    rows,
    detailPending,
    detailFailed,
    sourceCounts: pageFacets?.sources ?? NO_COUNTS,
    summary: pageFacets ? summaryFromFacets(pageFacets) : EMPTY_SUMMARY,
    page,
    totalPages,
    pageSize,
    setPage,
    setPageSize,
    filters,
    setFilters,
    titleFilterError: titleFilterError ?? serverTitleError,
    reviewLevel,
    setReviewLevel,
    levelCustomised,
    runtimeHiddenCount: pageFacets?.runtime_hidden ?? 0,
    chipFilter,
    toggleChipFilter,
    clearChipFilter,
    applicableSelectedIds,
    setSelection,
    selectAllMatching,
    clearSelection,
    allMatchingSelected,
    skipSelected,
    rejectSelected,
    bulkProgress,
    searching,
    searchAgain,
    bulkApplyMode,
    setBulkApplyMode,
    pendingReplace,
    confirmReplace,
    cancelReplace,
    skipReplaceConfirm,
    resetReplaceConfirm,
    selectedIds,
    applying,
    highConfidenceIds,
    allVisiblePendingIds,
    previewCover,
    setPreviewCover,
    refetching,
    refetchBooks,
    refetchStale,
    spineCtx,
    dispatch,
    refresh,
  };
}
