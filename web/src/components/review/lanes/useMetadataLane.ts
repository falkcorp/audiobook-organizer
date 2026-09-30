// file: web/src/components/review/lanes/useMetadataLane.ts
// version: 1.25.0
// guid: 7c4e1a90-3b58-4d26-9a07-1e5a8b2c4f70
// last-edited: 2026-09-30
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
//   filters -> preGroupFiltered -> multiBookIds -> filteredResults
//           -> pageResults -> multiGroups + { highConfidenceIds, ... }
//
// Split that across a component boundary and the halves either recompute the
// chain or prop-drill it. So the chain stays here and callers get slices: the
// rail takes `pageResults`, the spine takes `spineCtx`, the action bar takes the
// id sets. Nobody downstream re-derives anything.
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
import { isAuthRedirectError } from '../../../utils/apiFetch';
import { STORAGE_KEYS } from '../../../lib/storageKeys';
import type { CandidateGroup, SpineContext } from '../spine/CompareSpine';
import { runtimeHiddenBySwitch, type RowState } from '../spine/rowState';
import type { MetadataAction } from '../reviewActions';

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
    return window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_SKIP_REPLACE_CONFIRM) === 'true';
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
export function reviewProviderID(source: string | undefined | null): string {
  const normalized = source?.trim().toLowerCase() ?? '';
  switch (normalized) {
    case 'audible':
    case 'audnexus':
    case 'audnexus (audible)':
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
  unreviewable_by_cause?: { orphaned: number; no_candidates: number; decode_errors: number };
  /**
   * Books already ruled on whose stored candidate is gone. Optional for the
   * same reason as the breakdown above: a server that predates the split omits
   * it, and these books used to be counted inside `unreviewable`.
   */
  resolved_no_candidates?: number;
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
 *   stale                         every non-orphaned row past the cache TTL,
 *                                 minus books the owner marked no-match
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
  | 'stale';

const CHIPS_NEEDING_UNREVIEWABLE: ReadonlySet<ChipFilter> = new Set<ChipFilter>([
  'errors',
  'no_candidates',
  'resolved_no_candidates',
  'stale',
]);

/** Whether a row came from the unreviewable bucket (it has no candidate). */
export function isUnreviewableRow(r: CandidateResult): boolean {
  return (
    r.status === 'no_candidates' ||
    r.status === 'resolved_no_candidates' ||
    r.status === 'decode_error'
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
  /** Every cached row the server returned, unfiltered. */
  results: CandidateResult[];
  /** After filters, before pagination. */
  filteredResults: CandidateResult[];
  /** The current page. */
  pageResults: CandidateResult[];
  /** Multi-book groups on the current page. Singletons render as rows. */
  groups: CandidateGroup[];
  /** Book ids belonging to a group on this page -- excluded from `rows`. */
  groupedBookIds: Set<string>;
  /** Rows to hand the spine: the page minus anything rendered as a group. */
  rows: CandidateResult[];

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
  /** Rows from the server's unreviewable bucket, once loaded. */
  unreviewableResults: CandidateResult[];
  unreviewableLoading: boolean;
  unreviewableError: string | null;

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
 * @param active  Whether to fetch. The workspace passes `lane === 'metadata'`
 *                so switching lanes does not keep three fetches in flight.
 */
export function useMetadataLane(toast: Toast, active = true): MetadataLane {
  const [results, setResults] = useState<CandidateResult[]>([]);
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
  const [unreviewableResults, setUnreviewableResults] = useState<CandidateResult[]>([]);
  const [unreviewableError, setUnreviewableError] = useState<string | null>(null);
  // The refreshKey the loaded bucket belongs to, or -1 for "never loaded". A
  // refresh invalidates it, and the load effect fetches again only if a chip
  // still needs it.
  const [unreviewableFor, setUnreviewableFor] = useState(-1);
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
  const [summary, setSummary] = useState<MetadataLaneSummary>({
    matched: 0,
    no_match: 0,
    errors: 0,
    total: 0,
    unreviewable: 0,
    stale: 0,
  });
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

  // Fetch the entire cached review set once; paginate and filter client-side.
  // `limit=0` tells the server "return all rows".
  useEffect(() => {
    if (!active) return;
    setLoading(true);
    // Cleared on every attempt, not only on success. Leaving it set would mean a
    // successful Retry still rendered the failure Alert forever.
    setError(null);
    const fetchId = ++fetchIdRef.current;
    api
      // all=true is required, not incidental: the server caps an unpaged
      // request to a default page, and every derivation below (filters,
      // grouping, chip views) must see the whole library, not its first page.
      .getCachedReviewResults(0, 0, true)
      .then((data) => {
        if (fetchId !== fetchIdRef.current) return; // stale -- a newer fetch is in flight
        const allResults = data.results || [];

        // all=true should always return the whole set, but if the server still
        // says the response is truncated, every derivation below (filters,
        // grouping, chip views) is over a partial set. Say so rather than present
        // it as the library. A warning, not `error`: the load did succeed, and
        // `error` is the failure Alert with a Retry that would not help.
        if (data.truncated === true) {
          toastRef.current(
            `Only ${allResults.length} of ${data.total_count ?? 'unknown'} metadata review rows were returned; filters, groups and chip views cover only these rows.`,
            'warning'
          );
        }

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
          for (const r of allResults) {
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

        setResults(allResults);
        const tc = data.total_count ?? allResults.length;
        setSummary({
          matched: data.matched ?? allResults.filter((r) => r.status === 'matched').length,
          no_match: data.no_match ?? allResults.filter((r) => r.status === 'no_match').length,
          errors: data.errors ?? 0,
          total: tc,
          unreviewable: data.unreviewable ?? 0,
          stale: data.stale ?? 0,
          // Left undefined rather than zero-filled when the server omits it:
          // "no breakdown available" and "every cause is zero" are different
          // claims, and the rail renders them differently.
          unreviewable_by_cause: data.unreviewable_by_cause,
          resolved_no_candidates: data.resolved_no_candidates,
        });
        if (typeof data.bulk_apply_max_items === 'number' && data.bulk_apply_max_items > 0) {
          setApplyCap(data.bulk_apply_max_items);
        }
        setLoading(false);
      })
      .catch((err: unknown) => {
        // The stale guard runs FIRST. A request the user has already superseded
        // can still reject later, and letting that write `error` would paint a
        // failure banner over a page that loaded perfectly well.
        if (fetchId !== fetchIdRef.current) return;
        setError(
          err instanceof Error && err.message
            ? err.message
            : 'Could not load the metadata review queue.'
        );
        setLoading(false);
      });
  }, [active, refreshKey]);

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

  // The unreviewable bucket is fetched only when a chip needs it: it is
  // thousands of rows the review queue itself never shows, so the default load
  // does not pay for it.
  const needUnreviewable = chipFilter !== null && CHIPS_NEEDING_UNREVIEWABLE.has(chipFilter);
  const unreviewableFetchRef = useRef(0);
  useEffect(() => {
    if (!active || !needUnreviewable || unreviewableFor === refreshKey) return;
    const fetchId = ++unreviewableFetchRef.current;
    api
      .getCachedReviewResults(0, 0, true, 'unreviewable')
      .then((data) => {
        if (fetchId !== unreviewableFetchRef.current) return;
        setUnreviewableResults(data.results || []);
        setUnreviewableError(null);
        setUnreviewableFor(refreshKey);
      })
      .catch((err: unknown) => {
        if (fetchId !== unreviewableFetchRef.current) return;
        // Marked loaded so the effect does not retry in a loop; the rail
        // shows the error and the refresh button tries again.
        setUnreviewableFor(refreshKey);
        setUnreviewableError(
          err instanceof Error && err.message
            ? err.message
            : 'Could not load the books with no candidate.'
        );
      });
  }, [active, needUnreviewable, unreviewableFor, refreshKey]);
  // Derived, not state: loading is exactly "a chip needs the bucket and the
  // bucket held is not this refresh's".
  const unreviewableLoading = active && needUnreviewable && unreviewableFor !== refreshKey;

  const setPageSize = useCallback((n: number) => {
    setPageSizeState(n);
    saveReviewPageSize(n);
    setPage(1);
  }, []);

  // --- the derivation chain -------------------------------------------------

  const sourceCounts = useMemo(
    () =>
      results.reduce<Record<string, number>>((acc, r) => {
        const sourceID = reviewProviderID(r.candidate?.source);
        if (sourceID) acc[sourceID] = (acc[sourceID] || 0) + 1;
        return acc;
      }, {}),
    [results]
  );

  const titleRegex = useMemo(() => {
    if (!filters.titleFilter) return null;
    try {
      return new RegExp(filters.titleFilter, 'i');
    } catch {
      // A half-typed regex is not an error state -- it just does not filter yet.
      return null;
    }
  }, [filters.titleFilter]);

  // Split at the runtime filter so the rail can say how many rows that one
  // filter is hiding: `beforeRuntime` is every row the earlier filters keep.
  const beforeRuntime = useMemo(
    () =>
      results
        .filter((r) => !titleRegex || titleRegex.test(r.book.title || ''))
        .filter(
          (r) =>
            !filters.sourceFilter || reviewProviderID(r.candidate?.source) === filters.sourceFilter
        )
        .filter(
          (r) =>
            (r.status === 'matched' &&
              r.candidate &&
              r.candidate.score * 100 >= filters.confidenceThreshold) ||
            r.status !== 'matched'
        )
        .filter((r) => !filters.hideApplied || rowStates.get(r.book.id) !== 'applied')
        .filter((r) => !filters.hideRejected || rowStates.get(r.book.id) !== 'rejected')
        .filter((r) => !filters.hideSkipped || rowStates.get(r.book.id) !== 'skipped')
        .filter((r) => !filters.hideNoMatch || (r.status !== 'no_match' && r.status !== 'error')),
    [results, rowStates, titleRegex, filters]
  );

  const runtimeHidden = useMemo(
    () =>
      filters.hideRuntimeDifferences
        ? new Set(beforeRuntime.filter((r) => runtimeHiddenBySwitch(r)).map((r) => r.book.id))
        : new Set<string>(),
    [beforeRuntime, filters.hideRuntimeDifferences]
  );

  const preGroupFiltered = useMemo(
    () =>
      beforeRuntime
        // runtimeHiddenBySwitch: the apply gate's 10% rule OR the spine's
        // ten-minute warning chip, so no warned row survives the switch. An
        // unknown runtime on either side is not evidence of a mismatch, so
        // those rows stay reviewable.
        .filter((r) => !runtimeHidden.has(r.book.id))
        .filter((r) => {
          // An unknown language on EITHER side is a no-op, not a hide: a book
          // with no language set must still be offered its candidates.
          if (!filters.matchLanguage) return true;
          if (!r.candidate) return true;
          const bookLang = normalizeLanguage(r.book.language);
          const candLang = normalizeLanguage(r.candidate.language);
          if (!bookLang || !candLang) return true;
          return bookLang === candLang;
        })
        .filter((r) => !filters.onlyWithTranscription || !!r.book.transcribed_title)
        .filter((r) => !filters.onlyTranscriptionMatched || !!r.candidate?.transcription_boosted),
    [beforeRuntime, runtimeHidden, filters]
  );

  // The rows a chip counts, when one is active: exactly those, with every
  // other filter paused, so the chip's number is the number of rows shown.
  const chipRows = useMemo((): CandidateResult[] | null => {
    if (chipFilter === null) return null;
    switch (chipFilter) {
      case 'matched':
        return results.filter((r) => r.status === 'matched');
      case 'no_match':
        return results.filter((r) => r.status === 'no_match');
      case 'total':
        return results;
      case 'errors':
        return unreviewableResults.filter((r) => r.status === 'decode_error');
      case 'no_candidates':
        return unreviewableResults.filter((r) => r.status === 'no_candidates');
      case 'resolved_no_candidates':
        return unreviewableResults.filter((r) => r.status === 'resolved_no_candidates');
      case 'stale':
        // Explicitly false, as everywhere else: no age is not stale. Books the
        // owner marked "no match" are left out, as the server's count leaves
        // them out (cacheRowStale): the candidate fetch never searches them.
        // Reviewable rows carry that verdict as status 'no_match', unreviewable
        // rows as their raw review_status.
        return [
          ...results.filter((r) => r.status !== 'no_match'),
          ...unreviewableResults.filter((r) => r.review_status !== 'no_match'),
        ].filter((r) => r.is_fresh === false);
    }
  }, [chipFilter, results, unreviewableResults]);

  // Book ids sharing a candidate with at least one other book.
  //
  // Computed over the WHOLE filtered set, not the current page -- unlike the
  // per-page `groups` below. Two files of one book landing on opposite sides of
  // a page boundary each look like a singleton to a per-page pass and would
  // survive the hide, which is the exact case this toggle exists to remove.
  const multiBookIds = useMemo(() => {
    // A chip view pauses every filter, this one included.
    if (!filters.hideMultiBook || chipRows !== null) return new Set<string>();
    const byKey = new Map<string, string[]>();
    for (const r of preGroupFiltered) {
      if (!r.candidate || r.status !== 'matched' || ungroupedIds.has(r.book.id)) continue;
      const key = candidateKey(r.candidate);
      const ids = byKey.get(key);
      if (ids) ids.push(r.book.id);
      else byKey.set(key, [r.book.id]);
    }
    const out = new Set<string>();
    for (const ids of byKey.values()) {
      if (ids.length > 1) ids.forEach((id) => out.add(id));
    }
    return out;
  }, [filters.hideMultiBook, preGroupFiltered, ungroupedIds, chipRows]);

  const filteredResults = useMemo(
    () =>
      chipRows ??
      (filters.hideMultiBook
        ? preGroupFiltered.filter((r) => !multiBookIds.has(r.book.id))
        : preGroupFiltered),
    [chipRows, filters.hideMultiBook, preGroupFiltered, multiBookIds]
  );

  // Deselect anything the multi-book filter has hidden. `selectedIds` is built
  // by hand and does not shrink when a filter hides its members, so without this
  // "Apply Selected" would still apply a grouped book that was ticked before the
  // toggle went on -- the precise opposite of what the toggle is for -- and the
  // button's count would disagree with what it does.
  const multiBookKey = useMemo(() => [...multiBookIds].sort().join(','), [multiBookIds]);
  useEffect(() => {
    // multiBookIds is empty in a chip view, so this is already a no-op there.
    if (!filters.hideMultiBook || multiBookIds.size === 0) return;
    setSelectedIds((prev) => {
      const next = new Set([...prev].filter((id) => !multiBookIds.has(id)));
      return next.size === prev.size ? prev : next;
    });
    // multiBookIds is rebuilt each render; key the effect on its contents.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.hideMultiBook, multiBookKey]);

  const totalPages = Math.max(1, Math.ceil(filteredResults.length / pageSize));

  // Clamp rather than auto-advance: filters can shrink the set below the current
  // page index, and with client-side pagination there is no empty page to skip
  // past.
  //
  // DERIVED, not synced in an effect. An effect would render one frame at the
  // out-of-range page before correcting it -- `pageResults` would slice past the
  // end and the spine would flash empty -- and it would cost a second render
  // pass every time a filter changed. `requestedPage` is what the reviewer
  // asked for; `page` is what is actually reachable.
  const page = Math.min(requestedPage, totalPages);

  const pageResults = useMemo(() => {
    const start = (page - 1) * pageSize;
    return filteredResults.slice(start, start + pageSize);
  }, [filteredResults, page, pageSize]);

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
      const r = results.find((x) => x.book.id === id);
      if (!r?.candidate || !r.candidate_hash) return undefined;
      return pinOfCandidate(r.candidate, r.candidate_hash);
    },
    [results]
  );

  // The pins a bulk button sends: every book it applies is the owner's manual
  // apply (owner ruling 2026-09-27). A book whose row is loaded with a
  // candidate_hash gets a 'review_bulk' pin of the candidate it shows, so a
  // cache refetched since the page loaded is still refused as stale. A book
  // the lane holds no hash for (the selection outlived a refresh that dropped
  // its row, or the row came without a hash) gets the hashless owner marker:
  // still owner-reviewed, on whatever the server's top candidate is.
  const bulkPinsFor = useCallback(
    (ids: string[]): Record<string, ApplyPin> => {
      const byId = new Map(results.map((r) => [r.book.id, r]));
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
    [results]
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
        api.pollOperationV2(dispatched.op_id).catch(() => undefined),
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
    [toast, refresh, releaseInFlight]
  );

  const flushApplyQueue = useCallback(async () => {
    const ids = [...applyQueueRef.current];
    applyQueueRef.current = [];
    if (ids.length === 0) return;
    const pins: Record<string, ApplyPin> = {};
    for (const id of ids) {
      const pin = applyPinsRef.current.get(id);
      if (pin) pins[id] = pin;
      applyPinsRef.current.delete(id);
    }
    try {
      await runApplyOp(ids, pins);
    } catch (err) {
      handleApplyError(err, ids);
    }
  }, [runApplyOp, handleApplyError]);

  const applyOne = useCallback(
    (bookId: string) => {
      // Retained at the OPTIMISTIC MARK, not after the dispatch resolves.
      // applyOne marks the row synchronously but its dispatch is behind a 500ms
      // debounce, so retaining later left a window in which any refresh (another
      // apply's terminal poll, a filter change) reverted the row -- the precise
      // flicker ActionBar.tsx rejects useOptimistic to avoid. applyMany has no
      // such window because it retains before dispatching.
      retainInFlight([bookId]);
      clearServerDerived([bookId]);
      setRowStates((prev) => new Map(prev).set(bookId, 'applied'));
      // A single-row Apply is the owner's review of that row: pin what it
      // shows. Several quick clicks share one debounced request, which is
      // fine because pins are per book.
      const pin = rowPinFor(bookId);
      if (pin) applyPinsRef.current.set(bookId, pin);
      applyQueueRef.current.push(bookId);
      if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
      applyTimerRef.current = setTimeout(() => void flushApplyQueue(), 500);
    },
    [flushApplyQueue, retainInFlight, clearServerDerived, rowPinFor]
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
        case 'applySelected':
          // The server's bulk apply fail-safe applies to the WHOLE apply, not
          // to each request: applyMany chunks for the body limit, and that
          // must never become a way around the cap. Refused here, before any
          // request or Replace prompt, for every bulk entry point.
          if (action.ids.length > applyCap) {
            toast(applyCapMessage(applyCap, action.ids.length), 'error');
            return;
          }
          // Replace overwrites values the owner may have curated, on every
          // book in the batch, so it asks first -- here, not in each button,
          // so every bulk entry point confirms exactly once. The request is
          // parked for the workspace's dialog; confirmReplace sends it. Fill
          // applies as before, without a prompt, and so does Replace once the
          // owner has ticked "Don't ask me again".
          if (bulkApplyMode === 'replace' && action.ids.length > 0 && !skipReplaceConfirm) {
            setPendingReplace({
              ids: action.ids,
              pins: bulkPinsFor(action.ids),
              mode: bulkApplyMode,
            });
            return;
          }
          void applyMany(action.ids);
          return;
        case 'reject':
          void reject(action.id);
          return;
        case 'unreject':
          void unreject(action.id);
          return;
        case 'skip':
          clearServerDerived([action.id]);
          setRowStates((prev) => new Map(prev).set(action.id, 'skipped'));
          return;
        case 'unskip':
          clearServerDerived([action.id]);
          setRowStates((prev) => new Map(prev).set(action.id, 'pending'));
          return;
        case 'rejectGroup':
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
          clearServerDerived(
            results
              .filter((r) => r.status === 'no_match' || r.status === 'error')
              .map((r) => r.book.id)
          );
          setRowStates((prev) => {
            const next = new Map(prev);
            results
              .filter((r) => r.status === 'no_match' || r.status === 'error')
              .forEach((r) => next.set(r.book.id, 'skipped'));
            return next;
          });
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
      results,
      toast,
      requestedPage,
      clearServerDerived,
      bulkApplyMode,
      skipReplaceConfirm,
      bulkPinsFor,
      applyCap,
    ]
  );

  const confirmReplace = useCallback(
    (dontAskAgain: boolean) => {
      const pending = pendingReplace;
      setPendingReplace(null);
      if (!pending) return;
      // Only stop asking when storage kept the flag: blocked storage means
      // the prompt stays, the safe default.
      if (dontAskAgain && saveSkipReplaceConfirm(true)) setSkipReplaceConfirm(true);
      void applyMany(pending.ids, { pins: pending.pins, mode: pending.mode });
    },
    [pendingReplace, applyMany]
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

  const selectAllMatching = useCallback(() => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      filteredResults.forEach((r) => next.add(r.book.id));
      return next;
    });
  }, [filteredResults]);

  const clearSelection = useCallback(() => setSelectedIds(new Set()), []);

  const allMatchingSelected = useMemo(
    () => filteredResults.length > 0 && filteredResults.every((r) => selectedIds.has(r.book.id)),
    [filteredResults, selectedIds]
  );

  // SELECTION ACROSS FILTER CHANGES. The selection is a plain id set, so it
  // survives page turns and chip toggles untouched (a chip is a view onto the
  // lane, and "select in the no-candidates chip, then look at the stale chip"
  // must not lose the first). When a FILTER changes -- a switch, the level,
  // the title regex, the provider, the threshold -- ids the new filter no
  // longer matches are dropped, so a bulk action never reaches books the
  // reviewer can no longer see. That prune is skipped while a chip is active,
  // because a chip pauses the filters and the rows shown do not change.
  const filtersKey = useMemo(() => JSON.stringify(filters), [filters]);
  const prunedForKeyRef = useRef(filtersKey);
  useEffect(() => {
    if (prunedForKeyRef.current === filtersKey) return;
    prunedForKeyRef.current = filtersKey;
    if (chipFilter !== null) return;
    const keep = new Set(filteredResults.map((r) => r.book.id));
    setSelectedIds((prev) => {
      const next = new Set([...prev].filter((id) => keep.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [filtersKey, chipFilter, filteredResults]);

  const skipSelected = useCallback(() => {
    const ids = [...selectedIds];
    clearServerDerived(ids);
    setRowStates((prev) => {
      const next = new Map(prev);
      ids.forEach((id) => next.set(id, 'skipped'));
      return next;
    });
    setSelectedIds(new Set());
  }, [selectedIds, clearServerDerived]);

  const rejectSelected = useCallback(async (): Promise<number> => {
    const ids = [...selectedIds];
    if (ids.length === 0) return 0;
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
  }, [selectedIds, clearServerDerived, toast]);

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
      bulkApplyMode,
    }),
    [rowStates, selectedIds, toggleSelect, dispatch, expandedId, toggleExpand, bulkApplyMode]
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

  // Books known to have no candidate: the unreviewable bucket's rows. Apply
  // has nothing to send for them, so they are kept out of Apply selected.
  const unreviewableIds = useMemo(
    () => new Set(unreviewableResults.map((r) => r.book.id)),
    [unreviewableResults]
  );
  const applicableSelectedIds = useMemo(
    () => [...selectedIds].filter((id) => !unreviewableIds.has(id)),
    [selectedIds, unreviewableIds]
  );

  const searchAgain = useCallback(
    async (
      ids: string[],
      confirm: (message: string) => Promise<boolean>
    ): Promise<string | null> => {
      if (!ids.length) return null;
      const byId = new Map<string, CandidateResult>();
      for (const r of results) byId.set(r.book.id, r);
      for (const r of unreviewableResults) byId.set(r.book.id, r);
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
          setBulkProgress({ label: 'Clearing no-match marks', done: cleared, total: marked.length });
          const settled = await Promise.allSettled(
            batch.map((id) => api.clearMetadataNoMatch(id))
          );
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
          const resp = await api.batchFetchCandidates({ book_ids: part, force: true });
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
    [results, unreviewableResults, rowStates, toast, refresh, clearServerDerived]
  );

  return {
    loading,
    error,
    results,
    filteredResults,
    pageResults,
    groups,
    groupedBookIds,
    rows,
    sourceCounts,
    summary,
    page,
    totalPages,
    pageSize,
    setPage,
    setPageSize,
    filters,
    setFilters,
    reviewLevel,
    setReviewLevel,
    levelCustomised,
    runtimeHiddenCount: runtimeHidden.size,
    chipFilter,
    toggleChipFilter,
    clearChipFilter,
    unreviewableResults,
    unreviewableLoading,
    unreviewableError,
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
