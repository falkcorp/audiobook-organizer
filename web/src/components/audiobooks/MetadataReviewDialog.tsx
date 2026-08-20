// file: web/src/components/audiobooks/MetadataReviewDialog.tsx
// version: 1.19.1
// guid: e7f8a9b0-c1d2-3e4f-5a6b-7c8d9e0f1a2b
// last-edited: 2026-08-19

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Avatar,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  FormControlLabel,
  IconButton,
  MenuItem,
  Pagination,
  DialogActions,
  DialogContent,
  DialogTitle,
  Slider,
  Stack,
  Switch,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { CandidateResult, MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';
import { isAuthRedirectError } from '../../utils/apiFetch';
import { STORAGE_KEYS } from '../../lib/storageKeys';

interface MetadataReviewDialogProps {
  open: boolean;
  onClose: () => void;
  onComplete: () => void;
  toast: (
    message: string,
    severity?: 'success' | 'error' | 'warning' | 'info',
    action?: { label: string; onClick: () => void }
  ) => void;
}

const SOURCE_COLORS: Record<string, 'primary' | 'secondary' | 'success' | 'warning' | 'info'> = {
  openlibrary: 'primary',
  google_books: 'secondary',
  audible: 'success',
  goodreads: 'warning',
  manual: 'info',
};

// Review rows are heavy — each renders a full candidate comparison — so this
// selector deliberately does NOT match ActivityLog's, which lists 250 and 500.
// A stored size of 250 froze the dialog hard enough that the size control
// itself could not be reached to change it back (see loadReviewPageSize).
// 100 is kept because only 250 was observed to lock up; if 100 turns out to
// stall too, lower MAX_REVIEW_PAGE_SIZE rather than only trimming this list —
// the clamp is what actually protects users, this list just avoids offering
// the footgun.
const PAGE_SIZE_OPTIONS = [25, 50, 100];

// Largest size a stored preference may restore. Anything above is clamped down
// and rewritten, so a value saved before this cap existed (or by a future build
// that offers more) can never re-freeze the dialog on open.
const MAX_REVIEW_PAGE_SIZE = 50;

// Language filter: when enabled (default), candidates whose
// language disagrees with the book's are hidden. Preference
// persists in localStorage so users don't re-enable it on
// every dialog open.

function loadLanguageFilter(): boolean {
  if (typeof window === 'undefined') return true;
  const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER);
  return raw === null ? true : raw === 'true';
}

// Normalizes a language string to an ISO 639-1 2-letter code
// for comparison. Mirrors the server-side metadataLanguageTag
// logic — the review dialog filter compares book.language
// against candidate.language, and both sides might come from
// different source APIs that return the language in different
// formats (full name, 2-letter code, 3-letter code).
//
// Returns the lowercased input for unknown languages so the
// filter still works via string equality.
function normalizeLanguage(lang: string | undefined | null): string {
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

// Persist the review page size in localStorage so users don't have
// to re-select it every time they open the dialog. The activity log
// uses session-only state, but the review dialog is opened ad-hoc
// many times per session — re-picking "250 per page" on every open
// is annoying.

// A stored size is CLAMPED, not just validated. The previous version accepted
// any member of PAGE_SIZE_OPTIONS, and 250 was a member — so a user who picked
// 250 froze the dialog, and every subsequent open restored 250 and froze it
// again. The size control is inside the dialog, so there was no way to undo it
// from the UI: the only escape was clearing localStorage by hand.
//
// Clamping on READ (rather than only trimming the options list) is what makes
// this self-healing for users who already have 250 or 500 persisted from an
// earlier build — they get 50 on next open without touching devtools.
// The "Strict review" preset. Three filters that were always being set together
// by hand on every single dialog open — hide skipped rows, hide multi-book
// groups, and only show high-confidence matches. One switch sets all three, and
// the switch itself persists, so the dialog opens the way you left it.
//
// 190 is deliberately above 100: candidate scores are sums that routinely exceed
// 100%, so 190 means "several strong signals agree", not "impossible".
export const STRICT_PRESET = {
  hideSkipped: true,
  hideMultiBook: true,
  confidenceThreshold: 190,
} as const;

/** Default min-confidence when the preset is OFF. */
export const DEFAULT_CONFIDENCE = 85;

export function loadStrictPreset(): boolean {
  if (typeof window === 'undefined') return false;
  return window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET) === 'true';
}

function saveStrictPreset(on: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET, String(on));
  } catch {
    // Private-mode / quota failure: the preset still applies this session.
  }
}

export function loadReviewPageSize(): number {
  if (typeof window === 'undefined') return 25;
  const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE);
  if (raw === null) return 25;

  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return 25;
  if (PAGE_SIZE_OPTIONS.includes(n)) return n;

  // Out of range or no longer offered. Persist the correction so the bad value
  // is gone for good rather than being re-clamped on every open.
  const safe = Math.min(n, MAX_REVIEW_PAGE_SIZE);
  const corrected = PAGE_SIZE_OPTIONS.includes(safe) ? safe : MAX_REVIEW_PAGE_SIZE;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, String(corrected));
  } catch {
    // Private-mode / quota failures are non-fatal: the clamp still applies to
    // this session, it just re-clamps next open instead of persisting.
  }
  return corrected;
}

function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function formatFileSize(bytes: number): string {
  if (bytes >= 1073741824) return `${(bytes / 1073741824).toFixed(1)} GB`;
  if (bytes >= 1048576) return `${(bytes / 1048576).toFixed(0)} MB`;
  return `${(bytes / 1024).toFixed(0)} KB`;
}

export function MetadataReviewDialog({
  open,
  onClose,
  onComplete,
  toast,
}: MetadataReviewDialogProps) {
  const [results, setResults] = useState<CandidateResult[]>([]);
  const [loading, setLoading] = useState(true);
  const [totalCount, setTotalCount] = useState(0);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [rowStates, setRowStates] = useState<
    Map<string, 'pending' | 'applied' | 'rejected' | 'skipped'>
  >(new Map());
  const [sourceFilter, setSourceFilter] = useState<string | null>(null);
  // These three are the preset's members: they seed from the persisted preset
  // so a checked preset survives a reload without re-setting each control.
  const [strictPreset, setStrictPreset] = useState(loadStrictPreset);
  const [confidenceThreshold, setConfidenceThreshold] = useState(() =>
    loadStrictPreset() ? STRICT_PRESET.confidenceThreshold : DEFAULT_CONFIDENCE
  );
  const [viewMode, setViewMode] = useState<'compact' | 'two-column'>('compact');
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  const [summary, setSummary] = useState({ matched: 0, no_match: 0, errors: 0, total: 0 });
  const [totalSummary] = useState<{ matched: number; no_match: number; errors: number } | null>(
    null
  );
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  // serverPage: which page to fetch from the API. Unified with the display page —
  // one paginator, one concept of "page".
  const [serverPage, setServerPage] = useState(1);
  const [reviewPageSize, setReviewPageSize] = useState<number>(loadReviewPageSize);
  const [hideApplied, setHideApplied] = useState(true);
  const [hideRejected, setHideRejected] = useState(true);
  const [hideNoMatch, setHideNoMatch] = useState(true);
  const [hideSkipped, setHideSkipped] = useState(
    () => loadStrictPreset() && STRICT_PRESET.hideSkipped
  );
  // Hide books that share a candidate with another book — the cards that render
  // with "Skip All". Those are the ambiguous ones (two parts of one book, or
  // genuine duplicates) and need a judgement call per group; this toggle gets
  // them out of the way so the unambiguous one-book-one-candidate rows can be
  // worked through on their own.
  const [hideMultiBook, setHideMultiBook] = useState(
    () => loadStrictPreset() && STRICT_PRESET.hideMultiBook
  );
  const [matchLanguage, setMatchLanguage] = useState<boolean>(loadLanguageFilter);
  const [titleFilter, setTitleFilter] = useState('');
  const [onlyWithTranscription, setOnlyWithTranscription] = useState(false);
  const [onlyTranscriptionMatched, setOnlyTranscriptionMatched] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  // Apply progress is NOT rendered here any more. The dialog dispatches the
  // background op and returns immediately, so there is nothing to show a bar
  // for — the bell (OperationsIndicator, fed by useOperationsStore) owns
  // progress for every background op, this one included.
  //
  // Guards the deferred list refresh below: once the dialog unmounts there is
  // no state left to refresh, and setting it would warn.
  const applyWatchAliveRef = useRef(true);
  useEffect(() => {
    applyWatchAliveRef.current = true;
    return () => {
      applyWatchAliveRef.current = false;
    };
  }, []);
  // Books in this set have been manually split from their duplicate group and
  // render as standalone rows. Reset when the page changes.
  const [ungroupedIds, setUngroupedIds] = useState<Set<string>>(new Set());
  // Calls onComplete (to refresh the library) only when changes were made,
  // then closes the dialog. Avoids mid-review library refreshes that reset
  // the user's scroll position and show a disorienting loading spinner.
  const handleClose = useCallback(() => {
    if (hasChangesRef.current) {
      onComplete();
      hasChangesRef.current = false;
    }
    onClose();
  }, [onComplete, onClose]);

  // fetchIdRef prevents stale responses from overwriting newer ones when the
  // user changes page or page size quickly.
  const fetchIdRef = useRef(0);

  // Tracks whether any apply actions occurred so the library is refreshed
  // exactly once when the dialog closes, rather than on every individual apply.
  const hasChangesRef = useRef(false);

  // Fetch the entire cached review set once. The dialog now paginates,
  // filters, and sorts purely client-side — refetching only on manual
  // refresh or dialog reopen. limit=0 tells the server "return all rows."
  useEffect(() => {
    if (!open) return;
    setLoading(true);
    const fetchId = ++fetchIdRef.current;
    api
      .getCachedReviewResults(0, 0)
      .then((data) => {
        if (fetchId !== fetchIdRef.current) return; // stale — a newer fetch is in flight
        const allResults = data.results || [];

        const seedStates = new Map<string, 'pending' | 'applied' | 'rejected' | 'skipped'>();
        for (const r of allResults) {
          if (r.status === 'applied') {
            seedStates.set(r.book.id, 'applied');
          } else if (r.status === 'no_match') {
            seedStates.set(r.book.id, 'rejected');
          }
        }
        setRowStates((prev) => {
          const merged = new Map(prev);
          seedStates.forEach((v, k) => {
            if (!merged.has(k)) merged.set(k, v);
          });
          return merged;
        });

        setResults(allResults);
        const tc = data.total_count ?? allResults.length;
        setTotalCount(tc);
        setSummary({
          matched: data.matched ?? allResults.filter((r) => r.status === 'matched').length,
          no_match: data.no_match ?? allResults.filter((r) => r.status === 'no_match').length,
          errors: data.errors ?? 0,
          total: tc,
        });
        setLoading(false);
      })
      .catch(() => {
        if (fetchId !== fetchIdRef.current) return;
        setLoading(false);
      });
  }, [open, refreshKey]);

  // Reset page and un-groupings when the dialog opens.
  useEffect(() => {
    if (open) {
      setServerPage(1);
      setUngroupedIds(new Set());
    }
  }, [open]);

  // Reset un-groupings when navigating to a new page (groups are per-page).
  useEffect(() => {
    setUngroupedIds(new Set());
  }, [serverPage]);

  // Compute unique sources with counts
  const sourceCounts = results.reduce<Record<string, number>>((acc, r) => {
    if (r.candidate?.source) {
      acc[r.candidate.source] = (acc[r.candidate.source] || 0) + 1;
    }
    return acc;
  }, {});

  const titleRegex = (() => {
    if (!titleFilter) return null;
    try {
      return new RegExp(titleFilter, 'i');
    } catch {
      return null;
    }
  })();

  const preGroupFiltered = results
    .filter((r) => !titleRegex || titleRegex.test(r.book.title || ''))
    .filter((r) => !sourceFilter || r.candidate?.source === sourceFilter)
    .filter(
      (r) =>
        (r.status === 'matched' && r.candidate && r.candidate.score * 100 >= confidenceThreshold) ||
        r.status !== 'matched'
    )
    .filter((r) => !hideApplied || rowStates.get(r.book.id) !== 'applied')
    .filter((r) => !hideRejected || rowStates.get(r.book.id) !== 'rejected')
    .filter((r) => !hideSkipped || rowStates.get(r.book.id) !== 'skipped')
    .filter((r) => !hideNoMatch || (r.status !== 'no_match' && r.status !== 'error'))
    .filter((r) => {
      // Language filter: hide candidates whose language
      // doesn't match the book's. Only active when the toggle
      // is on AND both the book and candidate have a language
      // set — an unknown language on either side is a
      // no-op (show the row) so new books without metadata
      // still get candidates offered to them.
      if (!matchLanguage) return true;
      if (!r.candidate) return true;
      const bookLang = normalizeLanguage(r.book.language);
      const candLang = normalizeLanguage(r.candidate.language);
      if (!bookLang || !candLang) return true;
      return bookLang === candLang;
    })
    // Transcription filters: optional narrow-down to books where Whisper
    // intro data exists, and/or where it was the deciding scoring factor.
    .filter((r) => !onlyWithTranscription || !!r.book.transcribed_title)
    .filter((r) => !onlyTranscriptionMatched || !!r.candidate?.transcription_boosted);

  // Book ids that share a candidate with at least one other book.
  //
  // This is deliberately computed over the WHOLE filtered set rather than the
  // current page, unlike the `multiGroups` map further down which drives
  // rendering and is per-page by design. Two files of the same book that land on
  // opposite sides of a page boundary would each look like a singleton to a
  // per-page pass and would survive the hide — which is exactly the case this
  // toggle exists to remove. `candidateKey` is a hoisted function declaration
  // defined with the grouping code below.
  const multiBookIds = (() => {
    if (!hideMultiBook) return new Set<string>();
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
  })();

  const filteredResults = hideMultiBook
    ? preGroupFiltered.filter((r) => !multiBookIds.has(r.book.id))
    : preGroupFiltered;

  // Deselect anything the multi-book filter has hidden. selectedIds is a Set the
  // user builds by hand and it does not shrink when a filter hides its members,
  // so without this "Apply Selected" would still apply a grouped book that was
  // ticked before the toggle went on — the precise opposite of what the toggle
  // is for — and the button's count would disagree with what it does.
  useEffect(() => {
    if (!hideMultiBook || multiBookIds.size === 0) return;
    setSelectedIds((prev) => {
      const next = new Set([...prev].filter((id) => !multiBookIds.has(id)));
      return next.size === prev.size ? prev : next;
    });
    // multiBookIds is rebuilt each render; key the effect on its contents.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hideMultiBook, [...multiBookIds].sort().join(',')]);

  // Reset to page 1 when filters change so the user sees the first filtered result.
  useEffect(() => {
    setServerPage(1);
  }, [
    sourceFilter,
    confidenceThreshold,
    hideApplied,
    hideRejected,
    hideSkipped,
    hideNoMatch,
    hideMultiBook,
    matchLanguage,
    titleFilter,
    onlyWithTranscription,
    onlyTranscriptionMatched,
  ]);

  // Go back to page 1 on manual refresh.
  useEffect(() => {
    setServerPage(1);
  }, [refreshKey]);

  // Coalesce rapid Apply clicks into one batched API call
  const applyQueueRef = useRef<string[]>([]);
  const applyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Both apply paths follow the same rule: report what the SERVER applied, and
  // only mark rows the server named in applied_ids. Previously every requested
  // id was marked 'applied' and the toast reported ids.length — so a book the
  // server skipped (expired cache entry, apply error) disappeared from the
  // queue as if it had worked, then came back on the next load. Skipped rows
  // are reverted to 'pending' so they stay visible and re-appliable.
  // An auth bounce during a batch apply does NOT mean nothing was applied.
  //
  // The code here used to assert that it did — every row was reverted to
  // 'pending' with a "nothing was applied" toast — on the reasoning that when
  // Cloudflare Access bounces a request to a login page the origin never sees
  // it. That holds only while the request is short. Measured on production: a
  // 250-book apply ran 2m0s, the origin returned HTTP 200, 67 files were
  // written in that window, and the user was told nothing had been applied.
  // Reverting the rows then invites a re-apply of work that already succeeded.
  //
  // So: keep the detection (an expired session really is indistinguishable from
  // success without it), report it accurately, and re-read server state rather
  // than guessing. Dispatch is now a sub-second request, which makes the bounce
  // far less likely to begin with.
  const handleApplyError = useCallback(
    (err: unknown, requestedIds: string[]) => {
      setRefreshKey((k) => k + 1);
      if (isAuthRedirectError(err)) {
        toast(
          'Session expired — sign in again. Any books already applied were kept; the list has been refreshed.',
          'error'
        );
        return;
      }
      toast(`Failed to start applying ${requestedIds.length} book(s)`, 'error');
    },
    [toast]
  );

  // runApplyOp dispatches the background apply, polls it, and then RE-READS the
  // review list rather than diffing a client-side guess.
  //
  // The previous version trusted an applied_ids/skipped list returned inline.
  // That list no longer exists: at dispatch time nothing has been applied yet.
  // Re-reading is also strictly more honest — it reflects what the server
  // actually did, including work finished after any client-side timeout.
  const runApplyOp = useCallback(
    async (requestedIds: string[], writeBack?: boolean): Promise<boolean> => {
      const dispatch = await api.batchApplyFromCache(requestedIds, writeBack);

      // Hand the op off and return. The apply already runs as a background
      // operation server-side; awaiting it here was what made the dialog feel
      // synchronous — you clicked Apply on 100 books and then watched a
      // progress bar instead of getting on with your evening.
      //
      // The bell picks this up for free: OperationsIndicator renders whatever
      // is in useOperationsStore.activeOperations, and the dispatched op is
      // there. Same shape as the bulk metadata fetch on the Library page.
      toast(
        `Metadata apply queued for ${requestedIds.length.toLocaleString()} book(s) — watch the bell for progress.`,
        'success'
      );

      // Still re-read server state rather than diffing a client-side guess —
      // just when the op FINISHES instead of blocking on it. If the dialog is
      // closed by then this resolves against an unmounted component, so the
      // refresh is guarded by the same ref the unmount path clears.
      hasChangesRef.current = true;
      void api
        .pollOperationV2(dispatch.op_id)
        .catch(() => undefined)
        .finally(() => {
          if (!applyWatchAliveRef.current) return;
          setRefreshKey((k) => k + 1);
        });

      return true;
    },
    [toast]
  );

  const flushApplyQueue = useCallback(async () => {
    const ids = [...applyQueueRef.current];
    applyQueueRef.current = [];
    if (ids.length === 0) return;
    try {
      await runApplyOp(ids);
    } catch (err) {
      handleApplyError(err, ids);
    }
  }, [runApplyOp, handleApplyError]);

  const handleApplyOne = (bookId: string) => {
    // Optimistic UI update
    setRowStates((prev) => new Map(prev).set(bookId, 'applied'));
    // Queue for batched API call
    applyQueueRef.current.push(bookId);
    if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
    applyTimerRef.current = setTimeout(flushApplyQueue, 500);
  };

  const handleBulkApply = async (bookIds: string[]) => {
    if (bookIds.length === 0) return;
    setApplying(true);
    try {
      await runApplyOp(bookIds);
      // The refresh triggered by runApplyOp re-derives every row from the
      // server, so clearing the whole selection is safe: anything that did not
      // apply comes back as pending and can be re-selected.
      setSelectedIds(new Set());
    } catch (err) {
      handleApplyError(err, bookIds);
    } finally {
      setApplying(false);
    }
  };

  const handleSkip = (bookId: string) => {
    setRowStates((prev) => {
      const current = prev.get(bookId);
      // Toggle: skip ↔ pending
      return new Map(prev).set(bookId, current === 'skipped' ? 'pending' : 'skipped');
    });
  };

  const handleReject = async (bookId: string) => {
    try {
      await api.markNoMatch(bookId);
      setRowStates((prev) => new Map(prev).set(bookId, 'rejected'));
      toast('Candidate rejected — will be excluded from future fetches', 'info', {
        label: 'Undo',
        onClick: async () => {
          try {
            await api.clearMetadataNoMatch(bookId);
            setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
            toast('Rejection undone', 'success');
          } catch {
            toast('Failed to undo rejection', 'error');
          }
        },
      });
    } catch {
      toast('Failed to reject', 'error');
    }
  };

  const handleUnreject = async (bookId: string) => {
    try {
      await api.clearMetadataNoMatch(bookId);
      setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
      toast('Rejection undone', 'success');
    } catch {
      toast('Failed to undo rejection', 'error');
    }
  };

  const handleUngroup = (bookId: string) => setUngroupedIds((prev) => new Set(prev).add(bookId));

  // Paginate the filtered set client-side. Because the server returned every
  // cached row and sorted pending-matched first, page 1 reliably fills with
  // the most relevant rows without any auto-advance dance.
  const totalPages = Math.max(1, Math.ceil(filteredResults.length / reviewPageSize));
  const pageStart = (serverPage - 1) * reviewPageSize;
  const pageResults = filteredResults.slice(pageStart, pageStart + reviewPageSize);

  // Group books that were assigned the same candidate metadata. Key priority:
  // asin (most specific) → isbn13/isbn → source+title+author (normalized fallback).
  function candidateKey(c: MetadataCandidate): string {
    if (c.asin) return `asin:${c.asin}`;
    if (c.isbn) return `isbn:${c.isbn}`;
    return `${c.source}:${c.title.trim().toLowerCase()}:${c.author.trim().toLowerCase()}`;
  }

  interface CandidateGroup {
    key: string;
    candidate: MetadataCandidate;
    results: CandidateResult[];
  }

  const groupMap = new Map<string, CandidateGroup>();
  for (const r of pageResults) {
    if (!r.candidate || r.status !== 'matched' || ungroupedIds.has(r.book.id)) continue;
    const key = candidateKey(r.candidate);
    if (!groupMap.has(key)) groupMap.set(key, { key, candidate: r.candidate, results: [] });
    groupMap.get(key)!.results.push(r);
  }
  // Only multi-book groups are actual groups; singletons fall through to standalone rendering.
  const multiGroups = new Map<string, CandidateGroup>();
  for (const [key, g] of groupMap) {
    if (g.results.length > 1) multiGroups.set(key, g);
  }
  const groupedBookIds = new Set<string>();
  for (const g of multiGroups.values()) g.results.forEach((r) => groupedBookIds.add(r.book.id));

  // Clamp the current page when filters shrink the result set below the
  // current page index. Previously the dialog auto-advanced through empty
  // server pages; with client-side pagination there is no empty page to
  // skip — we just clamp instead.
  useEffect(() => {
    if (serverPage > totalPages) setServerPage(totalPages);
  }, [serverPage, totalPages]);

  const titleFilteredPendingIds = filteredResults
    .filter(
      (r) =>
        titleRegex &&
        r.status === 'matched' &&
        r.candidate &&
        !['applied', 'skipped', 'rejected'].includes(rowStates.get(r.book.id) || '')
    )
    .map((r) => r.book.id);

  const highConfidenceIds = pageResults
    .filter(
      (r) =>
        r.status === 'matched' &&
        r.candidate &&
        r.candidate.score * 100 >= confidenceThreshold &&
        r.candidate.narrator &&
        !['applied', 'skipped', 'rejected'].includes(rowStates.get(r.book.id) || '')
    )
    .map((r) => r.book.id);

  const allVisiblePendingIds = pageResults
    .filter(
      (r) =>
        r.status === 'matched' &&
        r.candidate &&
        !['applied', 'skipped', 'rejected'].includes(rowStates.get(r.book.id) || '')
    )
    .map((r) => r.book.id);

  const handleSkipAllUnmatched = () => {
    const newStates = new Map(rowStates);
    results
      .filter((r) => r.status === 'no_match' || r.status === 'error')
      .forEach((r) => newStates.set(r.book.id, 'skipped'));
    setRowStates(newStates);
  };

  const toggleSelected = (bookId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(bookId)) next.delete(bookId);
      else next.add(bookId);
      return next;
    });
  };

  const getRowSx = (bookId: string) => {
    const state = rowStates.get(bookId);
    if (state === 'applied')
      return { bgcolor: 'success.main', opacity: 0.6, borderRadius: 1, transition: 'all 0.3s' };
    if (state === 'skipped')
      return {
        bgcolor: 'action.disabledBackground',
        opacity: 0.5,
        borderRadius: 1,
        transition: 'all 0.3s',
      };
    return { borderRadius: 1, transition: 'all 0.3s' };
  };

  const isRowActionable = (bookId: string) => {
    const state = rowStates.get(bookId);
    return state !== 'applied' && state !== 'rejected';
  };

  const renderGroupedCard = (group: CandidateGroup) => {
    const c = group.candidate;
    const actionableIds = group.results
      .filter((r) => isRowActionable(r.book.id))
      .map((r) => r.book.id);
    const allApplied = group.results.every((r) => rowStates.get(r.book.id) === 'applied');
    const allRejected = group.results.every((r) => rowStates.get(r.book.id) === 'rejected');

    const handleRejectGroup = async () => {
      try {
        await Promise.all(actionableIds.map((id) => api.markNoMatch(id)));
        setRowStates((prev) => {
          const next = new Map(prev);
          actionableIds.forEach((id) => next.set(id, 'rejected'));
          return next;
        });
        toast(`Rejected ${actionableIds.length} books`, 'info');
      } catch {
        toast('Failed to reject', 'error');
      }
    };

    return (
      <Box
        key={group.key}
        sx={{ p: 2, mb: 1, border: 2, borderColor: 'primary.dark', borderRadius: 1 }}
      >
        <Typography
          variant="caption"
          color="primary"
          sx={{ fontWeight: 700, mb: 1, display: 'block' }}
        >
          {group.results.length} files matched to the same book
        </Typography>
        <Stack direction="row" spacing={2}>
          {/* Left: stacked book rows, each with an X to split from group */}
          <Box sx={{ flex: 1 }}>
            <Stack spacing={1.5}>
              {group.results.map((r) => (
                <Stack key={r.book.id} direction="row" spacing={1} alignItems="flex-start">
                  <Tooltip title="Separate from group">
                    <IconButton size="small" onClick={() => handleUngroup(r.book.id)}>
                      <CloseIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                  <Avatar
                    src={r.book.cover_url || ''}
                    variant="rounded"
                    sx={{ width: 40, height: 50 }}
                  />
                  <Box sx={{ minWidth: 0 }}>
                    <Typography variant="body2" fontWeight="bold">
                      {r.book.title}
                    </Typography>
                    <Stack direction="row" spacing={0.5} flexWrap="wrap">
                      {r.book.format && <Chip label={r.book.format} size="small" />}
                      {r.book.duration_seconds && (
                        <Typography variant="caption">
                          {formatDuration(r.book.duration_seconds)}
                        </Typography>
                      )}
                      {r.book.file_size_bytes && (
                        <Typography variant="caption">
                          · {formatFileSize(r.book.file_size_bytes)}
                        </Typography>
                      )}
                    </Stack>
                    <Typography
                      variant="caption"
                      display="block"
                      sx={{ wordBreak: 'break-all', color: 'text.secondary' }}
                    >
                      {r.book.file_path}
                    </Typography>
                    {r.book.itunes_path && (
                      <Typography
                        variant="caption"
                        color="info.main"
                        display="block"
                        sx={{ wordBreak: 'break-all' }}
                      >
                        iTunes: {r.book.itunes_path}
                      </Typography>
                    )}
                    {rowStates.get(r.book.id) === 'applied' && (
                      <Chip label="Applied" size="small" color="success" sx={{ mt: 0.5 }} />
                    )}
                    {rowStates.get(r.book.id) === 'rejected' && (
                      <Chip label="Rejected" size="small" color="error" sx={{ mt: 0.5 }} />
                    )}
                    {rowStates.get(r.book.id) === 'skipped' && (
                      <Chip label="Skipped" size="small" sx={{ mt: 0.5 }} />
                    )}
                  </Box>
                </Stack>
              ))}
            </Stack>
          </Box>

          {/* Right: shared candidate */}
          <Box sx={{ flex: 1 }}>
            <Stack direction="row" spacing={1} alignItems="flex-start">
              <Avatar
                src={c.cover_url || ''}
                variant="rounded"
                sx={{ width: 60, height: 80, cursor: c.cover_url ? 'pointer' : 'default' }}
                onClick={() => c.cover_url && setPreviewCover(c.cover_url)}
              />
              <Box sx={{ minWidth: 0, flex: 1 }}>
                <Typography variant="body2" fontWeight="bold">
                  {c.title}
                </Typography>
                <Typography variant="body2">{c.author}</Typography>
                {c.narrator && (
                  <Typography variant="body2" color="text.secondary">
                    Narrated by {c.narrator}
                  </Typography>
                )}
                {c.series && (
                  <Typography variant="body2">
                    Series: {c.series}
                    {c.series_position ? ` · Book ${c.series_position}` : ''}
                  </Typography>
                )}
                {c.year && (
                  <Typography variant="caption" display="block">
                    {c.year}
                  </Typography>
                )}
                {c.publisher && (
                  <Typography variant="caption" display="block">
                    {c.publisher}
                  </Typography>
                )}
                <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }}>
                  <Chip
                    label={`${Math.round(c.score * 100)}%`}
                    size="small"
                    color={c.score >= 0.85 ? 'success' : c.score >= 0.6 ? 'warning' : 'default'}
                  />
                  <Chip
                    label={c.source}
                    size="small"
                    color={SOURCE_COLORS[c.source] || 'default'}
                    variant="outlined"
                  />
                </Stack>
                {!allApplied && !allRejected && actionableIds.length > 0 && (
                  <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
                    <Button
                      size="small"
                      variant="contained"
                      color="success"
                      onClick={() => handleBulkApply(actionableIds)}
                    >
                      Apply All ({actionableIds.length})
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      color="error"
                      onClick={handleRejectGroup}
                    >
                      Reject All
                    </Button>
                    <Button
                      size="small"
                      variant="text"
                      onClick={() => group.results.forEach((r) => handleSkip(r.book.id))}
                    >
                      Skip All
                    </Button>
                  </Stack>
                )}
                {allApplied && (
                  <Chip label="All Applied" size="small" color="success" sx={{ mt: 1 }} />
                )}
                {allRejected && (
                  <Chip label="All Rejected" size="small" color="error" sx={{ mt: 1 }} />
                )}
              </Box>
            </Stack>
          </Box>
        </Stack>
      </Box>
    );
  };

  const renderCompactRow = (r: CandidateResult) => {
    const bookId = r.book.id;
    const isExpanded = expandedId === bookId;

    return (
      <Box key={bookId}>
        <Stack
          direction="row"
          alignItems="center"
          spacing={1}
          sx={{
            p: 1,
            cursor: 'pointer',
            '&:hover': { bgcolor: 'action.hover' },
            ...getRowSx(bookId),
          }}
          onClick={() => setExpandedId(isExpanded ? null : bookId)}
        >
          <Checkbox
            size="small"
            checked={selectedIds.has(bookId)}
            onClick={(e) => e.stopPropagation()}
            onChange={() => toggleSelected(bookId)}
            disabled={!isRowActionable(bookId)}
          />
          <Avatar
            src={r.candidate?.cover_url || r.book.cover_url || ''}
            variant="rounded"
            sx={{ width: 40, height: 50, cursor: 'pointer' }}
            onClick={(e) => {
              e.stopPropagation();
              setPreviewCover(r.candidate?.cover_url || r.book.cover_url || '');
            }}
          />
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="body2" noWrap>
              {r.book.title}
              {r.candidate ? (
                <>
                  {' \u2192 '}
                  <strong>{r.candidate.title}</strong>
                </>
              ) : r.status === 'no_match' ? (
                <Chip label="No match" size="small" sx={{ ml: 1 }} />
              ) : r.status === 'error' ? (
                <Chip label="Error" size="small" color="error" sx={{ ml: 1 }} />
              ) : null}
            </Typography>
          </Box>
          {r.candidate && (
            <>
              <Chip
                label={`${Math.round(r.candidate.score * 100)}%`}
                size="small"
                color={
                  r.candidate.score >= 0.85
                    ? 'success'
                    : r.candidate.score >= 0.6
                      ? 'warning'
                      : 'default'
                }
              />
              <Chip
                label={r.candidate.source}
                size="small"
                color={SOURCE_COLORS[r.candidate.source] || 'default'}
                variant="outlined"
              />
              {(r.candidate.audible_rating_overall ?? 0) > 0 && (
                <Chip
                  label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                  size="small"
                  variant="outlined"
                  sx={{ fontWeight: 500 }}
                />
              )}
              {(r.candidate.google_rating_average ?? 0) > 0 && (
                <Chip
                  label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                  size="small"
                  variant="outlined"
                  sx={{ fontWeight: 500 }}
                />
              )}
              {Math.abs(r.candidate?.duration_delta_sec ?? 0) > 600 && (
                <Chip
                  label={`⚠ runtime differs by ${formatDuration(Math.abs(r.candidate.duration_delta_sec!))}`}
                  color="warning"
                  size="small"
                  sx={{ fontWeight: 500 }}
                />
              )}
            </>
          )}
          {isRowActionable(bookId) && r.candidate && (
            <>
              <Button
                size="small"
                variant="contained"
                color="success"
                onClick={(e) => {
                  e.stopPropagation();
                  handleApplyOne(bookId);
                }}
              >
                Apply
              </Button>
              <Button
                size="small"
                variant="outlined"
                color="error"
                onClick={(e) => {
                  e.stopPropagation();
                  handleReject(bookId);
                }}
              >
                Reject
              </Button>
              <Button
                size="small"
                variant="text"
                onClick={(e) => {
                  e.stopPropagation();
                  handleSkip(bookId);
                }}
              >
                Skip
              </Button>
            </>
          )}
          {rowStates.get(bookId) === 'skipped' && (
            <Chip
              label="Skipped"
              size="small"
              onClick={(e) => {
                e.stopPropagation();
                handleSkip(bookId);
              }}
              sx={{ cursor: 'pointer' }}
            />
          )}
          {rowStates.get(bookId) === 'applied' && (
            <Chip label="Applied" size="small" color="success" />
          )}
          {rowStates.get(bookId) === 'rejected' && (
            <Chip
              label="Rejected — click to undo"
              size="small"
              color="error"
              onClick={(e) => {
                e.stopPropagation();
                handleUnreject(bookId);
              }}
              sx={{ cursor: 'pointer' }}
            />
          )}
        </Stack>

        {/* Expanded two-column detail for this row */}
        {isExpanded && r.candidate && (
          <Stack
            direction="row"
            spacing={2}
            sx={{ p: 2, pl: 7, bgcolor: 'action.hover', borderRadius: 1 }}
          >
            <Box sx={{ flex: 1 }}>
              <Typography variant="subtitle2" gutterBottom>
                Current
              </Typography>
              <Stack direction="row" spacing={1} alignItems="flex-start">
                <Avatar
                  src={r.book.cover_url || ''}
                  variant="rounded"
                  sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
                  onClick={() => r.book.cover_url && setPreviewCover(r.book.cover_url)}
                />
                <Box>
                  <Typography variant="body2" fontWeight="bold">
                    {r.book.title}
                  </Typography>
                  <Typography variant="body2">{r.book.author}</Typography>
                  {r.book.format && <Chip label={r.book.format} size="small" sx={{ mt: 0.5 }} />}
                  {r.book.duration_seconds && (
                    <Typography variant="caption" display="block">
                      {formatDuration(r.book.duration_seconds)}
                    </Typography>
                  )}
                  {r.book.file_size_bytes && (
                    <Typography variant="caption" display="block">
                      {formatFileSize(r.book.file_size_bytes)}
                    </Typography>
                  )}
                  <Typography variant="caption" sx={{ wordBreak: 'break-all' }}>
                    {r.book.file_path}
                  </Typography>
                  {r.book.itunes_path && (
                    <Typography
                      variant="caption"
                      color="info.main"
                      display="block"
                      sx={{ wordBreak: 'break-all' }}
                    >
                      iTunes: {r.book.itunes_path}
                    </Typography>
                  )}
                </Box>
              </Stack>
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="subtitle2" gutterBottom>
                Proposed
              </Typography>
              <Stack direction="row" spacing={1} alignItems="flex-start">
                <Avatar
                  src={r.candidate.cover_url || ''}
                  variant="rounded"
                  sx={{
                    width: 60,
                    height: 80,
                    cursor: r.candidate?.cover_url ? 'pointer' : 'default',
                  }}
                  onClick={() => r.candidate?.cover_url && setPreviewCover(r.candidate.cover_url)}
                />
                <Box>
                  <Typography variant="body2" fontWeight="bold">
                    {r.candidate.title}
                  </Typography>
                  <Typography variant="body2">{r.candidate.author}</Typography>
                  {r.candidate.narrator && (
                    <Typography variant="body2" color="text.secondary">
                      Narrated by {r.candidate.narrator}
                    </Typography>
                  )}
                  {r.candidate.series && (
                    <Typography variant="body2">
                      Series: {r.candidate.series}
                      {r.candidate.series_position
                        ? ` \u00b7 Book ${r.candidate.series_position}`
                        : ''}
                    </Typography>
                  )}
                  {r.candidate.year && (
                    <Typography variant="caption" display="block">
                      {r.candidate.year}
                    </Typography>
                  )}
                  {r.candidate.publisher && (
                    <Typography variant="caption" display="block">
                      {r.candidate.publisher}
                    </Typography>
                  )}
                  <Chip
                    label={`${Math.round(r.candidate.score * 100)}%`}
                    size="small"
                    color={
                      r.candidate.score >= 0.85
                        ? 'success'
                        : r.candidate.score >= 0.6
                          ? 'warning'
                          : 'default'
                    }
                    sx={{ mt: 0.5, mr: 0.5 }}
                  />
                  <Chip
                    label={r.candidate.source}
                    size="small"
                    color={SOURCE_COLORS[r.candidate.source] || 'default'}
                    variant="outlined"
                    sx={{ mt: 0.5, mr: 0.5 }}
                  />
                  {(r.candidate.audible_rating_overall ?? 0) > 0 && (
                    <Chip
                      label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                      size="small"
                      variant="outlined"
                      sx={{ mt: 0.5, mr: 0.5, fontWeight: 500 }}
                    />
                  )}
                  {(r.candidate.google_rating_average ?? 0) > 0 && (
                    <Chip
                      label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                      size="small"
                      variant="outlined"
                      sx={{ mt: 0.5, fontWeight: 500 }}
                    />
                  )}
                </Box>
              </Stack>
            </Box>
          </Stack>
        )}
      </Box>
    );
  };

  const renderTwoColumnCard = (r: CandidateResult) => {
    const bookId = r.book.id;

    return (
      <Box
        key={bookId}
        sx={{
          p: 2,
          mb: 1,
          border: 1,
          borderColor: 'divider',
          ...getRowSx(bookId),
        }}
      >
        <Stack direction="row" spacing={2}>
          {/* Left: current book info */}
          <Box sx={{ flex: 1 }}>
            <Stack direction="row" spacing={1} alignItems="flex-start">
              <Checkbox
                size="small"
                checked={selectedIds.has(bookId)}
                onChange={() => toggleSelected(bookId)}
                disabled={!isRowActionable(bookId)}
              />
              <Avatar
                src={r.book.cover_url || ''}
                variant="rounded"
                sx={{ width: 60, height: 80, cursor: r.book.cover_url ? 'pointer' : 'default' }}
                onClick={() => r.book.cover_url && setPreviewCover(r.book.cover_url)}
              />
              <Box sx={{ minWidth: 0 }}>
                <Typography variant="body2" fontWeight="bold">
                  {r.book.title}
                </Typography>
                <Typography variant="body2">{r.book.author}</Typography>
                {r.book.format && <Chip label={r.book.format} size="small" sx={{ mt: 0.5 }} />}
                {r.book.duration_seconds && (
                  <Typography variant="caption" display="block">
                    {formatDuration(r.book.duration_seconds)}
                  </Typography>
                )}
                {r.book.file_size_bytes && (
                  <Typography variant="caption" display="block">
                    {formatFileSize(r.book.file_size_bytes)}
                  </Typography>
                )}
                <Typography variant="caption" sx={{ wordBreak: 'break-all' }}>
                  {r.book.file_path}
                </Typography>
                {r.book.itunes_path && (
                  <Typography
                    variant="caption"
                    color="info.main"
                    display="block"
                    sx={{ wordBreak: 'break-all' }}
                  >
                    iTunes: {r.book.itunes_path}
                  </Typography>
                )}
              </Box>
            </Stack>
          </Box>

          {/* Right: proposed match */}
          <Box sx={{ flex: 1 }}>
            {r.candidate ? (
              <Stack direction="row" spacing={1} alignItems="flex-start">
                <Avatar
                  src={r.candidate.cover_url || ''}
                  variant="rounded"
                  sx={{
                    width: 60,
                    height: 80,
                    cursor: r.candidate?.cover_url ? 'pointer' : 'default',
                  }}
                  onClick={() => r.candidate?.cover_url && setPreviewCover(r.candidate.cover_url)}
                />
                <Box sx={{ minWidth: 0, flex: 1 }}>
                  <Typography variant="body2" fontWeight="bold">
                    {r.candidate.title}
                  </Typography>
                  <Typography variant="body2">{r.candidate.author}</Typography>
                  {r.candidate.narrator && (
                    <Typography variant="body2" color="text.secondary">
                      Narrated by {r.candidate.narrator}
                    </Typography>
                  )}
                  {r.candidate.series && (
                    <Typography variant="body2">
                      Series: {r.candidate.series}
                      {r.candidate.series_position
                        ? ` \u00b7 Book ${r.candidate.series_position}`
                        : ''}
                    </Typography>
                  )}
                  {r.candidate.year && (
                    <Typography variant="caption" display="block">
                      {r.candidate.year}
                    </Typography>
                  )}
                  {r.candidate.publisher && (
                    <Typography variant="caption" display="block">
                      {r.candidate.publisher}
                    </Typography>
                  )}
                  {(r.candidate.duration_sec ?? 0) > 0 && (
                    <Typography variant="caption" display="block">
                      Duration: {formatDuration(r.candidate.duration_sec!)}
                    </Typography>
                  )}
                  <Stack direction="row" spacing={0.5} flexWrap="wrap" sx={{ mt: 0.5 }}>
                    <Chip
                      label={`${Math.round(r.candidate.score * 100)}%`}
                      size="small"
                      color={
                        r.candidate.score >= 0.85
                          ? 'success'
                          : r.candidate.score >= 0.6
                            ? 'warning'
                            : 'default'
                      }
                    />
                    <Chip
                      label={r.candidate.source}
                      size="small"
                      color={SOURCE_COLORS[r.candidate.source] || 'default'}
                      variant="outlined"
                    />
                    {(r.candidate.audible_rating_overall ?? 0) > 0 && (
                      <Chip
                        label={`★ ${r.candidate.audible_rating_overall!.toFixed(1)}${(r.candidate.audible_rating_count ?? 0) > 0 ? ` (${r.candidate.audible_rating_count!.toLocaleString()})` : ''}`}
                        size="small"
                        variant="outlined"
                        sx={{ fontWeight: 500 }}
                      />
                    )}
                    {(r.candidate.google_rating_average ?? 0) > 0 && (
                      <Chip
                        label={`G★ ${r.candidate.google_rating_average!.toFixed(1)}${(r.candidate.google_rating_count ?? 0) > 0 ? ` (${r.candidate.google_rating_count!.toLocaleString()})` : ''}`}
                        size="small"
                        variant="outlined"
                        sx={{ fontWeight: 500 }}
                      />
                    )}
                  </Stack>
                  {isRowActionable(bookId) && (
                    <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
                      <Button
                        size="small"
                        variant="contained"
                        color="success"
                        onClick={() => handleApplyOne(bookId)}
                      >
                        Apply
                      </Button>
                      <Button
                        size="small"
                        variant="outlined"
                        color="error"
                        onClick={() => handleReject(bookId)}
                      >
                        Reject
                      </Button>
                      <Button size="small" variant="text" onClick={() => handleSkip(bookId)}>
                        Skip
                      </Button>
                    </Stack>
                  )}
                  {rowStates.get(bookId) === 'skipped' && (
                    <Chip
                      label="Skipped — click to undo"
                      size="small"
                      onClick={() => handleSkip(bookId)}
                      sx={{ cursor: 'pointer', mt: 1 }}
                    />
                  )}
                  {rowStates.get(bookId) === 'rejected' && (
                    <Chip
                      label="Rejected — click to undo"
                      size="small"
                      color="error"
                      onClick={() => handleUnreject(bookId)}
                      sx={{ cursor: 'pointer', mt: 1 }}
                    />
                  )}
                  {rowStates.get(bookId) === 'applied' && (
                    <Chip label="Applied" size="small" color="success" sx={{ mt: 1 }} />
                  )}
                </Box>
              </Stack>
            ) : (
              <Box sx={{ display: 'flex', alignItems: 'center', height: '100%' }}>
                <Chip
                  label={
                    r.status === 'no_match'
                      ? 'No match found'
                      : `Error: ${r.error_message || 'Unknown'}`
                  }
                  color={r.status === 'error' ? 'error' : 'default'}
                />
              </Box>
            )}
          </Box>
        </Stack>
      </Box>
    );
  };

  return (
    <>
      <Dialog
        open={open}
        // Ignore backdrop clicks so the dialog stays open while
        // the user is reviewing. The only way to close is the
        // explicit × button or the Done action. This prevents
        // the "accidentally clicked outside, lost all my review
        // state, had to re-query and wait for it to load again"
        // problem. Escape key is also suppressed — reviewing
        // 1000 candidates is a long workflow where the user
        // reaches for the keyboard often and hitting Esc by
        // accident should not blow away their session.
        onClose={(_event, reason) => {
          if (reason === 'backdropClick' || reason === 'escapeKeyDown') return;
          handleClose();
        }}
        disableEscapeKeyDown
        maxWidth="xl"
        fullWidth
      >
        <DialogTitle
          sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}
        >
          <span>Review Metadata Matches &mdash; {summary.total} books</span>
          <Stack direction="row" spacing={0.5} alignItems="center">
            <Tooltip title="Reload current page from server">
              <IconButton
                onClick={() => setRefreshKey((k) => k + 1)}
                size="small"
                aria-label="refresh results"
              >
                <RefreshIcon />
              </IconButton>
            </Tooltip>
            <IconButton
              onClick={handleClose}
              size="small"
              aria-label="close review dialog"
              sx={{ ml: 1 }}
            >
              <CloseIcon />
            </IconButton>
          </Stack>
        </DialogTitle>
        <DialogContent>
          {loading ? (
            <Box sx={{ display: 'flex', justifyContent: 'center', p: 4 }}>
              <CircularProgress />
            </Box>
          ) : (
            <>
              {/* Stats chips */}
              <Stack direction="row" spacing={1} flexWrap="wrap" sx={{ mb: 2, rowGap: 1 }}>
                {totalSummary ? (
                  <>
                    <Chip
                      label={`${totalSummary.matched} matched (total)`}
                      color="success"
                      size="small"
                    />
                    <Chip label={`${totalSummary.no_match} no match (total)`} size="small" />
                    {totalSummary.errors > 0 && (
                      <Chip
                        label={`${totalSummary.errors} errors (total)`}
                        color="error"
                        size="small"
                      />
                    )}
                    <Chip
                      label={`${summary.matched} matched (page)`}
                      color="success"
                      variant="outlined"
                      size="small"
                    />
                    <Chip
                      label={`${summary.no_match} no match (page)`}
                      variant="outlined"
                      size="small"
                    />
                  </>
                ) : (
                  <>
                    <Chip label={`${summary.matched} matched`} color="success" size="small" />
                    <Chip label={`${summary.no_match} no match`} size="small" />
                    <Chip label={`${summary.errors} errors`} color="error" size="small" />
                  </>
                )}
              </Stack>

              {/* Strict review preset. Sets the three filters that were being
                  re-applied by hand on every open, and persists so they stay
                  set. Turning it OFF restores the default confidence but leaves
                  the hide-toggles where they are — flipping it off is usually
                  "let me widen this a bit", not "reset everything". */}
              <FormControlLabel
                sx={{ mb: 1 }}
                control={
                  <Switch
                    size="small"
                    checked={strictPreset}
                    onChange={(e) => {
                      const on = e.target.checked;
                      setStrictPreset(on);
                      saveStrictPreset(on);
                      if (on) {
                        setHideSkipped(STRICT_PRESET.hideSkipped);
                        setHideMultiBook(STRICT_PRESET.hideMultiBook);
                        setConfidenceThreshold(STRICT_PRESET.confidenceThreshold);
                      } else {
                        setConfidenceThreshold(DEFAULT_CONFIDENCE);
                      }
                    }}
                  />
                }
                label={
                  <Typography variant="body2" sx={{ fontWeight: 600 }}>
                    Strict review — hide skipped &amp; multi-book, min confidence{' '}
                    {STRICT_PRESET.confidenceThreshold}%
                  </Typography>
                }
              />

              {/* Confidence slider */}
              <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 2 }}>
                <Typography variant="body2" sx={{ whiteSpace: 'nowrap' }}>
                  Min confidence: {confidenceThreshold}%
                </Typography>
                <Slider
                  value={confidenceThreshold}
                  onChange={(_, v) => setConfidenceThreshold(v as number)}
                  min={0}
                  max={300}
                  sx={{ maxWidth: 300 }}
                />
              </Stack>

              {/* Source filter chips */}
              <Stack direction="row" spacing={1} sx={{ mb: 2, flexWrap: 'wrap' }}>
                <Chip
                  label={`All (${results.length})`}
                  size="small"
                  variant={sourceFilter === null ? 'filled' : 'outlined'}
                  onClick={() => setSourceFilter(null)}
                />
                {Object.entries(sourceCounts).map(([source, count]) => (
                  <Chip
                    key={source}
                    label={`${source} (${count})`}
                    size="small"
                    color={SOURCE_COLORS[source] || 'default'}
                    variant={sourceFilter === source ? 'filled' : 'outlined'}
                    onClick={() => setSourceFilter(sourceFilter === source ? null : source)}
                  />
                ))}
              </Stack>

              {/* Title regex filter */}
              <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 2 }}>
                <TextField
                  size="small"
                  placeholder="Filter by title regex (e.g. Boxcar Children)"
                  value={titleFilter}
                  onChange={(e) => setTitleFilter(e.target.value)}
                  error={titleFilter !== '' && titleRegex === null}
                  helperText={titleFilter !== '' && titleRegex === null ? 'Invalid regex' : ''}
                  sx={{ flex: 1, maxWidth: 500 }}
                  inputProps={{ 'aria-label': 'filter by title regex' }}
                />
                {titleRegex && titleFilteredPendingIds.length > 0 && (
                  <Tooltip
                    title={`Apply all ${titleFilteredPendingIds.length} pending matched results visible through this filter`}
                  >
                    <span>
                      <Button
                        size="small"
                        variant="contained"
                        color="success"
                        disabled={applying}
                        onClick={() => handleBulkApply(titleFilteredPendingIds)}
                      >
                        Apply Matching ({titleFilteredPendingIds.length})
                      </Button>
                    </span>
                  </Tooltip>
                )}
                {titleFilter && (
                  <Button size="small" onClick={() => setTitleFilter('')}>
                    Clear
                  </Button>
                )}
              </Stack>

              {/* View toggle + hide filters */}
              <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 2 }} flexWrap="wrap">
                <ToggleButtonGroup
                  size="small"
                  value={viewMode}
                  exclusive
                  onChange={(_, v) => v && setViewMode(v)}
                >
                  <ToggleButton value="compact">Compact</ToggleButton>
                  <ToggleButton value="two-column">Two-Column</ToggleButton>
                </ToggleButtonGroup>
                <FormControlLabel
                  control={
                    <Switch
                      size="small"
                      checked={hideApplied}
                      onChange={(e) => setHideApplied(e.target.checked)}
                    />
                  }
                  label={<Typography variant="body2">Hide Applied</Typography>}
                />
                <FormControlLabel
                  control={
                    <Switch
                      size="small"
                      checked={hideRejected}
                      onChange={(e) => setHideRejected(e.target.checked)}
                    />
                  }
                  label={<Typography variant="body2">Hide Rejected</Typography>}
                />
                <FormControlLabel
                  control={
                    <Switch
                      size="small"
                      checked={hideSkipped}
                      onChange={(e) => setHideSkipped(e.target.checked)}
                    />
                  }
                  label={<Typography variant="body2">Hide Skipped</Typography>}
                />
                <FormControlLabel
                  control={
                    <Switch
                      size="small"
                      checked={hideNoMatch}
                      onChange={(e) => setHideNoMatch(e.target.checked)}
                    />
                  }
                  label={<Typography variant="body2">Hide No Match</Typography>}
                />
                <Tooltip title="Hide any book that shares a match with another book — the grouped cards with 'Skip All'. Those are the ambiguous ones (two parts of the same book, or duplicates) and need a decision per group. Turning this on leaves only the straightforward one-book-one-match rows, and takes the hidden books out of Apply Selected too.">
                  <FormControlLabel
                    control={
                      <Switch
                        size="small"
                        checked={hideMultiBook}
                        onChange={(e) => setHideMultiBook(e.target.checked)}
                      />
                    }
                    label={
                      <Typography variant="body2">
                        Hide Multi-Book Matches
                        {hideMultiBook && multiBookIds.size > 0 ? ` (${multiBookIds.size})` : ''}
                      </Typography>
                    }
                  />
                </Tooltip>
                <Tooltip title="Hide candidates whose language doesn't match the book's current language. Books without a language set still show all candidates.">
                  <FormControlLabel
                    control={
                      <Switch
                        size="small"
                        checked={matchLanguage}
                        onChange={(e) => {
                          const next = e.target.checked;
                          setMatchLanguage(next);
                          if (typeof window !== 'undefined') {
                            window.localStorage.setItem(
                              STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER,
                              String(next)
                            );
                          }
                        }}
                      />
                    }
                    label={<Typography variant="body2">Match Language</Typography>}
                  />
                </Tooltip>
                <Tooltip title="Show only books that have Whisper-transcribed intro data (title extracted from audio).">
                  <FormControlLabel
                    control={
                      <Switch
                        size="small"
                        checked={onlyWithTranscription}
                        onChange={(e) => setOnlyWithTranscription(e.target.checked)}
                      />
                    }
                    label={<Typography variant="body2">Has Transcription</Typography>}
                  />
                </Tooltip>
                <Tooltip title="Show only books where the candidate score was boosted by matching the Whisper-transcribed title, author, or narrator.">
                  <FormControlLabel
                    control={
                      <Switch
                        size="small"
                        checked={onlyTranscriptionMatched}
                        onChange={(e) => setOnlyTranscriptionMatched(e.target.checked)}
                      />
                    }
                    label={<Typography variant="body2">Transcription Matched</Typography>}
                  />
                </Tooltip>
              </Stack>

              {/* Smart action buttons */}
              <Stack direction="row" spacing={1} sx={{ mb: 2 }}>
                <Tooltip
                  title={`Apply ${highConfidenceIds.length} high-confidence matches with narrator`}
                >
                  <span>
                    <Button
                      size="small"
                      variant="outlined"
                      color="success"
                      disabled={applying || highConfidenceIds.length === 0}
                      onClick={() => handleBulkApply(highConfidenceIds)}
                    >
                      Apply High Confidence ({highConfidenceIds.length})
                    </Button>
                  </span>
                </Tooltip>
                <Tooltip
                  title={`Apply all ${allVisiblePendingIds.length} pending matches on this page`}
                >
                  <span>
                    <Button
                      size="small"
                      variant="outlined"
                      disabled={applying || allVisiblePendingIds.length === 0}
                      onClick={() => handleBulkApply(allVisiblePendingIds)}
                    >
                      Apply Page ({allVisiblePendingIds.length})
                    </Button>
                  </span>
                </Tooltip>
                <Button
                  size="small"
                  variant="outlined"
                  color="warning"
                  onClick={handleSkipAllUnmatched}
                >
                  Skip All Unmatched
                </Button>
              </Stack>

              {/* Single unified paginator — one page concept, one control. */}
              {(filteredResults.length > 0 || totalCount > 0) && (
                <Stack
                  direction="row"
                  justifyContent="space-between"
                  alignItems="center"
                  sx={{ mb: 1 }}
                >
                  <Typography variant="caption" color="text.secondary">
                    {filteredResults.length < results.length
                      ? `${pageResults.length} on page ${serverPage} of ${totalPages} · ${filteredResults.length} visible of ${totalCount} total`
                      : `${pageResults.length} on page ${serverPage} of ${totalPages} · ${totalCount} total`}
                  </Typography>
                  <Stack direction="row" spacing={1} alignItems="center">
                    <Pagination
                      count={totalPages}
                      page={serverPage}
                      onChange={(_, p) => setServerPage(p)}
                      size="small"
                      siblingCount={2}
                    />
                    <TextField
                      select
                      size="small"
                      value={reviewPageSize}
                      onChange={(e) => {
                        const next = Number(e.target.value);
                        setReviewPageSize(next);
                        setServerPage(1);
                        if (typeof window !== 'undefined') {
                          window.localStorage.setItem(
                            STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE,
                            String(next)
                          );
                        }
                      }}
                      sx={{ minWidth: 100 }}
                    >
                      {PAGE_SIZE_OPTIONS.map((n) => (
                        <MenuItem key={n} value={n}>
                          {n} / page
                        </MenuItem>
                      ))}
                    </TextField>
                  </Stack>
                </Stack>
              )}
              <Box sx={{ maxHeight: '60vh', overflow: 'auto' }}>
                {filteredResults.length === 0 ? (
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ p: 2, textAlign: 'center' }}
                  >
                    No results match current filters
                  </Typography>
                ) : (
                  (() => {
                    // Render in original order: grouped cards appear at first occurrence,
                    // subsequent group members are skipped, standalones render normally.
                    const renderedGroupKeys = new Set<string>();
                    return pageResults.map((r) => {
                      if (groupedBookIds.has(r.book.id)) {
                        const key = r.candidate ? candidateKey(r.candidate) : '';
                        const group = multiGroups.get(key);
                        if (group && !renderedGroupKeys.has(key)) {
                          renderedGroupKeys.add(key);
                          return renderGroupedCard(group);
                        }
                        return null; // already rendered as part of group
                      }
                      return viewMode === 'compact' ? renderCompactRow(r) : renderTwoColumnCard(r);
                    });
                  })()
                )}
              </Box>
            </>
          )}
        </DialogContent>
        <DialogActions sx={{ gap: 2 }}>
          {/* No inline apply progress: the dialog no longer waits on the op, so
              there is nothing here to track. Progress lives in the bell. */}
          <Button onClick={handleClose}>Close</Button>
          <Button
            variant="contained"
            disabled={selectedIds.size === 0 || applying}
            onClick={() => handleBulkApply(Array.from(selectedIds))}
          >
            {applying ? <CircularProgress size={20} sx={{ mr: 1 }} /> : null}
            Apply Selected ({selectedIds.size})
          </Button>
        </DialogActions>
      </Dialog>

      {/* Cover preview lightbox */}
      {previewCover && (
        <Box
          onClick={() => setPreviewCover(null)}
          sx={{
            position: 'fixed',
            inset: 0,
            zIndex: 2000,
            bgcolor: 'rgba(0,0,0,0.85)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            cursor: 'pointer',
          }}
        >
          <Box
            component="img"
            src={previewCover}
            alt="Cover preview"
            sx={{ maxWidth: '80vw', maxHeight: '80vh', borderRadius: 2, boxShadow: 8 }}
          />
        </Box>
      )}
    </>
  );
}
