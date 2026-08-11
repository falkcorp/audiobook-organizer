// file: web/src/components/dedup/UnifiedDedupTab.tsx
// version: 1.9.0
// guid: c8b9d0e1-f2a3-4567-bcde-cb8901234567
// last-edited: 2026-08-10
// UnifiedDedupTab is the T017 single surface that replaces the separate Books /
// Advanced-Scan / Acoustic tabs. It shows a paginated candidate table filtered
// by band (CERTAIN/HIGH/MEDIUM/REVIEW), with a side drawer for per-candidate
// comparison and score breakdown. The legacy tabs remain mounted behind a
// "show legacy" toggle for one release.
//
// Rows render acoustic-style rich cells: title (linked) + author + file path +
// a Rich/Partial/Poor metadata chip + a ★ Recommended-keep chip, with Keep A /
// Keep B / Compare / Dismiss actions. The book data arrives inline on each
// candidate (include_books=true) so there is no per-book getBook() fan-out;
// metadata-quality and the keep recommendation are computed client-side, ported
// from the legacy Acoustic tab.
//
// Memory-leak discipline (PR #1076):
//   - AbortController for every fetch, cancelled on cleanup / re-trigger.
//   - Timers cleared on unmount.
//   - No module-level mutable state.

import { type MouseEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams, Link as RouterLink } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  FormControlLabel,
  IconButton,
  Link,
  Paper,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import ClearIcon from '@mui/icons-material/Clear';
import HelpOutlineIcon from '@mui/icons-material/HelpOutline';
import * as api from '../../services/api';
import type { Book, DedupCandidate, DedupBand, DedupStats } from '../../services/api';
import { useOperationsStore } from '../../stores/useOperationsStore';
import { STORAGE_KEYS } from '../../lib/storageKeys';
import { BandFilterBar, type BandCounts } from './BandFilterBar';
import { ScoreBadgeRow } from './ScoreBadgeRow';
import { CandidateCompareDrawer } from './CandidateCompareDrawer';
import { BulkActionBar } from './BulkActionBar';
import { FolderFilesChip } from './FolderFilesChip';

// ---------- helpers ----------

/** Build BandCounts from the flat DedupStats[] array (which has status+band dims). */
function deriveBandCounts(stats: DedupStats[]): BandCounts {
  const pending = stats.filter((s) => s.status === 'pending');
  // Stats rows don't carry a "band" dimension in the existing schema, so we
  // derive total from the status counts. Band-specific counts will populate
  // when the API eventually returns them; until then we show 0 per-band and
  // the table results will reflect the filter accurately.
  const total = pending.reduce((sum, s) => sum + s.count, 0);
  return { CERTAIN: 0, HIGH: 0, MEDIUM: 0, REVIEW: 0, total };
}

// metadataQuality scores a Book's metadata completeness (0–10). Higher = more
// complete / reliable source, used to recommend which side of a duplicate pair
// to keep. Ported from the legacy Acoustic tab (BookDedup.tsx) so the unified
// view renders the same Rich/Partial/Poor judgement.
function metadataQuality(book: Book | null | undefined): number {
  if (!book) return 0;
  let score = 0;
  const title = book.title ?? '';
  // Title sanity: not empty, not literal "TITLE", not a bare ULID/UUID.
  const isGarbageTitle =
    !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
  if (!isGarbageTitle) score += 2;
  if (book.asin) score += 3;
  if (book.isbn13 || book.isbn) score += 2;
  if (book.cover_url) score += 1;
  if (book.narrator) score += 0.5;
  if (book.description) score += 0.5;
  if (book.publisher) score += 0.5;
  return score;
}

function qualityChip(score: number) {
  if (score >= 6)
    return <Chip label="Rich metadata" size="small" color="success" variant="outlined" />;
  if (score >= 3)
    return <Chip label="Partial metadata" size="small" color="warning" variant="outlined" />;
  return <Chip label="Poor metadata" size="small" color="error" variant="outlined" />;
}

/**
 * Single source of truth for the "recommended keep" decision.
 * Returns which entity to keep and its label ('A' or 'B'), or null when scores
 * are tied (no recommendation). Both the render and the `m` keyboard shortcut
 * call this function so they can never drift.
 */
function recommendedKeepSide(
  candidate: DedupCandidate
): { keepId: string; label: 'A' | 'B' } | null {
  const qA = metadataQuality(candidate.book_a);
  const qB = metadataQuality(candidate.book_b);
  if (qA > qB) return { keepId: candidate.entity_a_id, label: 'A' };
  if (qB > qA) return { keepId: candidate.entity_b_id, label: 'B' };
  return null; // tie — no recommendation
}

const DEDUP_SHORTCUTS = [
  { keys: 'j / k', action: 'Move to next / previous row' },
  { keys: 'm', action: 'Merge the focused candidate' },
  { keys: 'd', action: 'Dismiss the focused candidate' },
  { keys: 's', action: 'Select / deselect the focused row' },
  { keys: 'Enter', action: 'Open the compare drawer for the focused row' },
  { keys: 'Esc', action: 'Close the compare drawer' },
  { keys: 'Shift+A', action: 'Select all on the current page' },
  { keys: '?', action: 'Toggle this shortcut help' },
];

function isKeyboardShortcutSuppressed(): boolean {
  const active = document.activeElement;
  if (!active) return false;
  const tag = active.tagName.toLowerCase();
  // Block shortcuts when a form element has focus.
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true;
  const activeElement = active as HTMLElement;
  if (activeElement.isContentEditable) return true;
  // Block shortcuts when focus is anywhere inside a modal dialog (MUI renders
  // div[role="dialog"], not a native <dialog> element).
  return Boolean(activeElement.closest('[role="dialog"]'));
}

interface BookCardOpts {
  /** Score/band/status chips rendered above the title — only supplied for Book A. */
  badgesNode?: ReactNode;
  /** Quality chip shown inline after the title. */
  qualityChipNode?: ReactNode;
  /** "★ Recommended keep" chip shown inline after quality. */
  recommendedNode?: ReactNode;
}

// renderBookCard renders a candidate book with:
//   • cover on the LEFT spanning full row height
//   • score/status badges above the title (Book A only, via badgesNode)
//   • title (bigger) + quality chip inline on the same line
//   • author + monospace path below
function renderBookCard(book: Book | null | undefined, id: string, opts: BookCardOpts = {}) {
  const missing = !book;
  const path = book?.file_path ?? '';
  const title = book?.title ?? '';
  const coverUrl = book?.cover_url ?? '';
  const isGarbageTitle =
    !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
  return (
    <Stack direction="row" alignItems="stretch" spacing={1.5} sx={{ minWidth: 0 }}>
      {/* Cover — left-anchored, full row height */}
      <Box
        sx={{
          width: 56,
          flexShrink: 0,
          borderRadius: 0.5,
          overflow: 'hidden',
          alignSelf: 'stretch',
          minHeight: 68,
          bgcolor: 'action.selected',
        }}
      >
        {coverUrl && (
          <Box
            component="img"
            src={coverUrl}
            alt=""
            loading="lazy"
            sx={{ width: 56, height: '100%', objectFit: 'cover', display: 'block' }}
          />
        )}
      </Box>

      {/* Text content */}
      <Stack spacing={0.4} sx={{ minWidth: 0, flex: 1 }}>
        {/* Badges row — only rendered when supplied (Book A) */}
        {opts.badgesNode && (
          <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap alignItems="center">
            {opts.badgesNode}
          </Stack>
        )}

        {/* Title + inline quality + recommended chips */}
        <Stack direction="row" alignItems="center" flexWrap="wrap" spacing={0.5} useFlexGap>
          {missing ? (
            <Typography variant="body1" sx={{ color: 'error.main', fontStyle: 'italic' }}>
              (missing book — {id.slice(-8)})
            </Typography>
          ) : (
            <Link
              component={RouterLink}
              to={`/library/${id}`}
              underline="hover"
              sx={[{
                fontWeight: 600,
                fontSize: '1rem',
                textTransform: 'none',
                textAlign: 'left',
                whiteSpace: 'normal',
                wordBreak: 'break-word'
              }, isGarbageTitle ? {
                color: 'warning.main'
              } : {
                color: 'primary.main'
              }, isGarbageTitle ? {
                fontStyle: 'italic'
              } : {
                fontStyle: 'normal'
              }]}
              onClick={(e: MouseEvent) => e.stopPropagation()}
            >
              {isGarbageTitle ? title || '(no title)' : title}
            </Link>
          )}
          {opts.qualityChipNode}
          {opts.recommendedNode}
        </Stack>

        {book?.author_name && (
          <Typography variant="caption" sx={{ color: 'text.secondary' }}>
            {book.author_name}
          </Typography>
        )}

        {path && (
          <Tooltip title={path} placement="bottom-start">
            <Typography
              variant="caption"
              sx={{
                color: 'text.secondary',
                fontFamily: 'monospace',
                fontSize: '0.72rem',
                lineHeight: 1.2,
                wordBreak: 'break-all',
                opacity: 0.75,
              }}
            >
              {path}
            </Typography>
          </Tooltip>
        )}

        {!missing && (
          <Box>
            <FolderFilesChip bookId={id} />
          </Box>
        )}
      </Stack>
    </Stack>
  );
}

// ---------- component ----------

interface UnifiedDedupTabProps {
  /** When true, the component is mounted but replaced by legacy tabs. */
  hidden?: boolean;
}

export function UnifiedDedupTab({ hidden }: UnifiedDedupTabProps) {
  const [searchParams, setSearchParams] = useSearchParams();

  // --- filter state (synced to URL params for deep-link) ---
  const bandFromURL = (searchParams.get('band') as DedupBand | null) ?? null;
  const bookFromURL = searchParams.get('book') ?? null; // deep-link from FingerprintVisualsColumn

  const [bandFilter, setBandFilter] = useState<DedupBand | null>(bandFromURL);
  const [statusFilter] = useState<string>('pending');
  const [searchQuery, setSearchQuery] = useState('');
  // When true, only show pairs where NEITHER book has matched metadata
  // (both low-quality, need manual matching) — server-side filter.
  const [bothUnmatched, setBothUnmatched] = useState(false);
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState<number>(() =>
    parseInt(localStorage.getItem(STORAGE_KEYS.DEDUP_PAGE_SIZE) ?? '25', 10),
  );
  const [showMultiSelect, setShowMultiSelect] = useState<boolean>(
    () => localStorage.getItem(STORAGE_KEYS.DEDUP_MULTI_SELECT) === 'true',
  );
  const lastClickedIdxRef = useRef<number | null>(null);

  // --- data state ---
  const [candidates, setCandidates] = useState<DedupCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<DedupStats[]>([]);
  const [loading, setLoading] = useState(false);
  const [statsLoading, setStatsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);

  // --- selection state ---
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [focusedRowIndex, setFocusedRowIndex] = useState(0);
  const rowRefs = useRef<Array<HTMLTableRowElement | null>>([]);
  const [shortcutHelpOpen, setShortcutHelpOpen] = useState(false);

  // --- drawer state ---
  const [drawerCandidateId, setDrawerCandidateId] = useState<number | null>(null);

  // --- bulk action state ---
  const [bulkBusy, setBulkBusy] = useState(false);
  const [rescoringOpen, setRescoringOpen] = useState(false);
  const [rescanOpen, setRescanOpen] = useState(false);

  // --- abort controller refs ---
  const fetchAbortRef = useRef<AbortController | null>(null);
  const statsAbortRef = useRef<AbortController | null>(null);

  // Post-scan refresh timers (fire ~2s later to pick up new candidates). Tracked
  // so they're cancelled on unmount — otherwise the deferred loadCandidates/
  // loadStats would call setState on an unmounted component.
  const refreshTimeoutsRef = useRef<number[]>([]);
  useEffect(
    () => () => {
      refreshTimeoutsRef.current.forEach((id) => window.clearTimeout(id));
      refreshTimeoutsRef.current = [];
    },
    [],
  );

  // --- sync band filter to URL ---
  const handleBandChange = useCallback(
    (band: DedupBand | null) => {
      setBandFilter(band);
      setPage(0);
      setSelected(new Set());
      const next = new URLSearchParams(searchParams);
      if (band) {
        next.set('band', band);
      } else {
        next.delete('band');
      }
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams]
  );

  // --- load stats ---
  const loadStats = useCallback(() => {
    statsAbortRef.current?.abort();
    const ctrl = new AbortController();
    statsAbortRef.current = ctrl;
    setStatsLoading(true);
    fetch('/api/v1/dedup/stats', { signal: ctrl.signal })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`stats ${r.status}`))))
      .then((data) => {
        if (!ctrl.signal.aborted) {
          setStats(data?.data?.stats ?? []);
        }
      })
      .catch(() => {
        /* stats are optional */
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setStatsLoading(false);
      });
  }, []);

  // --- load candidates ---
  const loadCandidates = useCallback(() => {
    fetchAbortRef.current?.abort();
    const ctrl = new AbortController();
    fetchAbortRef.current = ctrl;
    setLoading(true);
    setError(null);

    const params: Parameters<typeof api.getDedupCandidates>[0] = {
      status: statusFilter || undefined,
      limit: rowsPerPage,
      offset: page * rowsPerPage,
      include_breakdown: true,
      include_books: true,
    };
    if (bandFilter) params.band = bandFilter;
    if (bothUnmatched) params.both_unmatched = true;

    // Use fetch directly to pass the signal (api.getDedupCandidates doesn't accept AbortSignal yet).
    const qs = new URLSearchParams();
    if (params.status) qs.set('status', params.status);
    if (params.band) qs.set('band', params.band);
    if (params.include_breakdown) qs.set('include_breakdown', 'true');
    // include_books surfaces title/author/path/metadata inline per row so the
    // cards render without a per-book getBook() fan-out (handled server-side).
    if (params.include_books) qs.set('include_books', 'true');
    // both_unmatched: server returns only pairs where neither book is matched.
    if (params.both_unmatched) qs.set('both_unmatched', 'true');
    qs.set('limit', String(params.limit));
    qs.set('offset', String(params.offset ?? 0));

    fetch(`/api/v1/dedup/candidates?${qs}`, { signal: ctrl.signal })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`candidates ${r.status}`))))
      .then((data) => {
        if (!ctrl.signal.aborted) {
          const resp = data?.data ?? data;
          setCandidates(resp.candidates ?? []);
          setTotal(resp.total ?? 0);
        }
      })
      .catch((err) => {
        if (!ctrl.signal.aborted) {
          setError(err instanceof Error ? err.message : 'Failed to load candidates');
        }
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
  }, [bandFilter, statusFilter, page, rowsPerPage, bothUnmatched]);

  useEffect(() => {
    loadStats();
    return () => {
      statsAbortRef.current?.abort();
    };
  }, [loadStats]);

  useEffect(() => {
    loadCandidates();
    return () => {
      fetchAbortRef.current?.abort();
    };
  }, [loadCandidates]);

  // --- pre-filter by deep-link book ID ---
  const filteredCandidates = useMemo(() => {
    let result = candidates;
    if (bookFromURL) {
      result = result.filter((c) => c.entity_a_id === bookFromURL || c.entity_b_id === bookFromURL);
    }
    if (searchQuery.trim()) {
      // Client-side search over the loaded page. With include_books the rows
      // carry title/author/path inline, so search those too — not just the
      // raw entity IDs.
      const q = searchQuery.trim().toLowerCase();
      const hay = (c: DedupCandidate) =>
        [
          c.entity_a_id,
          c.entity_b_id,
          c.layer,
          c.band ?? '',
          c.book_a?.title ?? '',
          c.book_b?.title ?? '',
          c.book_a?.author_name ?? '',
          c.book_b?.author_name ?? '',
          c.book_a?.file_path ?? '',
          c.book_b?.file_path ?? '',
        ]
          .join(' ')
          .toLowerCase();
      result = result.filter((c) => hay(c).includes(q));
    }
    return result;
  }, [candidates, bookFromURL, searchQuery]);

  const bandCounts = useMemo(() => deriveBandCounts(stats), [stats]);

  useEffect(() => {
    setFocusedRowIndex((idx) => {
      if (filteredCandidates.length === 0) return 0;
      return Math.min(idx, filteredCandidates.length - 1);
    });
  }, [filteredCandidates.length]);

  useEffect(() => {
    rowRefs.current[focusedRowIndex]?.scrollIntoView?.({
      block: 'nearest',
      inline: 'nearest',
    });
  }, [focusedRowIndex, filteredCandidates.length]);

  // --- selection helpers ---
  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const selectAll = () => {
    setSelected(new Set(filteredCandidates.map((c) => c.id)));
  };

  const clearSelection = () => {
    setSelected(new Set());
    lastClickedIdxRef.current = null;
  };

  // Click-to-select a row. Supports shift-click range selection.
  const handleRowClick = (e: MouseEvent, id: number, idx: number) => {
    // Don't steal clicks from buttons/links inside the row.
    if ((e.target as HTMLElement).closest('a, button')) return;
    if (e.shiftKey && lastClickedIdxRef.current !== null) {
      const lo = Math.min(lastClickedIdxRef.current, idx);
      const hi = Math.max(lastClickedIdxRef.current, idx);
      setSelected((prev) => {
        const next = new Set(prev);
        filteredCandidates.slice(lo, hi + 1).forEach((c) => next.add(c.id));
        return next;
      });
    } else {
      toggleSelect(id);
    }
    lastClickedIdxRef.current = idx;
  };

  // --- bulk actions ---
  const handleMergeSelected = async () => {
    if (selected.size === 0) return;
    setBulkBusy(true);
    let merged = 0;
    let failed = 0;
    for (const id of selected) {
      try {
        await api.mergeDedupCandidate(id);
        merged++;
      } catch {
        failed++;
      }
    }
    setToast(
      failed === 0
        ? `Merged ${merged} candidate${merged === 1 ? '' : 's'}`
        : `Merged ${merged}, failed ${failed}`
    );
    clearSelection();
    loadCandidates();
    loadStats();
    setBulkBusy(false);
  };

  const handleDismissSelected = async () => {
    if (selected.size === 0) return;
    setBulkBusy(true);
    let dismissed = 0;
    let failed = 0;
    for (const id of selected) {
      try {
        await api.dismissDedupCandidate(id);
        dismissed++;
      } catch {
        failed++;
      }
    }
    setToast(
      failed === 0
        ? `Dismissed ${dismissed} candidate${dismissed === 1 ? '' : 's'}`
        : `Dismissed ${dismissed}, failed ${failed}`
    );
    clearSelection();
    loadCandidates();
    loadStats();
    setBulkBusy(false);
  };

  // --- per-row actions (acoustic-style Keep A / Keep B / Dismiss) ---
  // Keep <keepId>: merge the pair keeping that book as the primary (the other
  // is merged into it). keepId must be the candidate's entity_a_id or
  // entity_b_id — the backend validates and 400s otherwise.
  const handleKeep = async (id: number, keepId: string, label: string) => {
    setBulkBusy(true);
    try {
      await api.mergeDedupCandidate(id, keepId);
      setToast(`Merged — kept ${label}`);
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setBulkBusy(false);
    }
  };

  const handleDismissOne = async (id: number) => {
    setBulkBusy(true);
    try {
      await api.dismissDedupCandidate(id);
      setToast('Dismissed');
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setBulkBusy(false);
    }
  };

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === '?' && shortcutHelpOpen) {
        event.preventDefault();
        setShortcutHelpOpen(false);
        return;
      }

      if (hidden || isKeyboardShortcutSuppressed()) return;

      if (event.key === '?') {
        event.preventDefault();
        setShortcutHelpOpen((open) => !open);
        return;
      }

      if (event.key === 'Escape') {
        if (drawerCandidateId !== null) {
          event.preventDefault();
          setDrawerCandidateId(null);
        }
        return;
      }

      if (filteredCandidates.length === 0) return;
      const focusedCandidate = filteredCandidates[focusedRowIndex];
      if (!focusedCandidate) return;

      if (event.key === 'j') {
        event.preventDefault();
        setFocusedRowIndex((idx) => Math.min(idx + 1, filteredCandidates.length - 1));
        return;
      }

      if (event.key === 'k') {
        event.preventDefault();
        setFocusedRowIndex((idx) => Math.max(idx - 1, 0));
        return;
      }

      if (event.key === 's') {
        event.preventDefault();
        toggleSelect(focusedCandidate.id);
        lastClickedIdxRef.current = focusedRowIndex;
        return;
      }

      if (event.key === 'A' && event.shiftKey) {
        event.preventDefault();
        selectAll();
        return;
      }

      if (event.key === 'Enter') {
        event.preventDefault();
        setDrawerCandidateId(focusedCandidate.id);
        return;
      }

      if (event.key === 'd' && focusedCandidate.status === 'pending') {
        event.preventDefault();
        void handleDismissOne(focusedCandidate.id);
        return;
      }

      if (event.key === 'm' && focusedCandidate.status === 'pending') {
        event.preventDefault();
        // Use the shared helper so this decision can never drift from the ★ chip shown in render.
        const rec = recommendedKeepSide(focusedCandidate);
        // On a quality tie (rec === null) default to keeping A, matching the Keep A button order.
        const keepId = rec?.keepId ?? focusedCandidate.entity_a_id;
        const label = rec?.label ?? 'A';
        void handleKeep(focusedCandidate.id, keepId, label);
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [
    drawerCandidateId,
    filteredCandidates,
    focusedRowIndex,
    handleDismissOne,
    handleKeep,
    hidden,
    selectAll,
    shortcutHelpOpen,
    toggleSelect,
  ]);

  const handleMergeAllFiltered = async () => {
    setBulkBusy(true);
    try {
      const result = await api.bulkMergeDedupCandidates({
        entity_type: 'book',
        status: statusFilter || 'pending',
      });
      setToast(
        `Bulk merge: ${result.merged} merged, ${result.failed} failed of ${result.attempted}`
      );
      clearSelection();
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Bulk merge failed');
    } finally {
      setBulkBusy(false);
    }
  };

  // --- rescore ---
  const handleRescore = async (apply: boolean) => {
    setBulkBusy(true);
    try {
      const result = await api.rescoreDedupCandidates(apply);
      const deltaStr = Object.entries(result.band_deltas ?? {})
        .map(([k, v]) => `${k}: ${v}`)
        .join(', ');
      setToast(
        `Rescore: ${result.inspected} inspected, ${result.changed} changed${
          deltaStr ? ` (${deltaStr})` : ''
        }${apply ? '' : ' [dry-run]'}`
      );
      if (apply) {
        loadCandidates();
        loadStats();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rescore failed');
    } finally {
      setBulkBusy(false);
      setRescoringOpen(false);
    }
  };

  // --- op tracking helper ---
  const trackOp = (op: api.Operation, label: string): string => {
    if (op?.id && op?.type) {
      useOperationsStore.getState().startPolling(op.id, op.type);
      return `${label} started — see bell for progress (op ${op.id.slice(-6)})`;
    }
    return `${label} started`;
  };

  // --- scan trigger ---
  const handleScan = async () => {
    setBulkBusy(true);
    try {
      const op = await api.triggerDedupScan();
      setToast(trackOp(op, 'Dedup scan'));
      // Refresh after short delay for new candidates to appear.
      const refreshTimeout = window.setTimeout(() => {
        loadCandidates();
        loadStats();
      }, 2000);
      refreshTimeoutsRef.current.push(refreshTimeout);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Scan failed');
    } finally {
      setBulkBusy(false);
    }
  };

  // --- force full rescan (modal-selected layer) ---
  // Each option maps to a specific backend scan op. These are heavier than the
  // incremental "Find Duplicates" scan — they re-run a whole detection layer.
  const RESCAN_OPTIONS: {
    kind: string;
    label: string;
    desc: string;
    run: () => Promise<api.Operation>;
  }[] = [
    {
      kind: 'full',
      label: 'Everything (embeddings + exact + similarity)',
      desc: 'Full pipeline re-scan. Slowest, but rebuilds every layer of candidates.',
      run: () => api.triggerDedupScan(),
    },
    {
      kind: 'embeddings',
      label: 'Embeddings only',
      desc: 'Re-embed and re-compare semantic vectors. Catches near-duplicate titles/metadata.',
      run: () => api.triggerEmbedScan(),
    },
    {
      kind: 'acoustic',
      label: 'Acoustic fingerprints only',
      desc: 'Compare stored audio fingerprints. Fast — no file I/O. Requires fingerprints to exist.',
      run: () => api.triggerDedupAcoustID(),
    },
    {
      kind: 'fingerprint',
      label: 'Fingerprint all books (read audio)',
      desc: 'Re-read every audio file and recompute chromaprints. Multi-hour. Run before acoustic if fingerprints are missing/stale.',
      run: () => api.triggerFingerprintBackfill('all'),
    },
    {
      kind: 'llm',
      label: 'LLM verdicts',
      desc: 'Re-run the LLM judgement pass over ambiguous pending candidates.',
      run: () => api.triggerDedupLLM(),
    },
  ];

  const handleForceRescan = async (opt: (typeof RESCAN_OPTIONS)[number]) => {
    setRescanOpen(false);
    setBulkBusy(true);
    try {
      const op = await opt.run();
      setToast(trackOp(op, `Force rescan (${opt.kind})`));
      const refreshTimeout = window.setTimeout(() => {
        loadCandidates();
        loadStats();
      }, 2000);
      refreshTimeoutsRef.current.push(refreshTimeout);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rescan failed');
    } finally {
      setBulkBusy(false);
    }
  };

  if (hidden) return null;

  return (
    <Box data-testid="unified-dedup-tab">
      {/* Toolbar — three primary actions:
          1. Find Duplicates  — incremental scan for new dupes
          2. Rescore          — recompute scores from stored signals (no re-scan)
          3. Force Full Rescan — opens a modal to re-run a specific detection layer */}
      <Stack
        direction="row"
        spacing={1}
        sx={{ mb: 2 }}
        alignItems="center"
        flexWrap="wrap"
        useFlexGap
      >
        <Tooltip title="Scan for new duplicate candidates (embed + exact + similarity matching)">
          <span>
            <Button
              variant="contained"
              size="small"
              startIcon={bulkBusy ? <CircularProgress size={16} /> : <RefreshIcon />}
              onClick={handleScan}
              disabled={bulkBusy}
            >
              Find Duplicates
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="Recompute scores for all pending candidates from stored signals (no re-scan)">
          <span>
            <Button
              variant="outlined"
              size="small"
              disabled={bulkBusy}
              onClick={() => setRescoringOpen(true)}
              data-testid="rescore-btn"
            >
              Rescore
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="Force a full re-scan of a chosen detection layer (embeddings, acoustic, fingerprints, LLM)">
          <span>
            <Button
              variant="outlined"
              size="small"
              color="warning"
              disabled={bulkBusy}
              onClick={() => setRescanOpen(true)}
              data-testid="force-rescan-btn"
            >
              Force Full Rescan
            </Button>
          </span>
        </Tooltip>
        <Box sx={{ flex: 1 }} />
        <Tooltip title="Show checkboxes for bulk operations. Click any row to select; shift-click to range-select.">
          <Button
            size="small"
            variant={showMultiSelect ? 'contained' : 'outlined'}
            onClick={() => {
              const next = !showMultiSelect;
              setShowMultiSelect(next);
              localStorage.setItem(STORAGE_KEYS.DEDUP_MULTI_SELECT, String(next));
              if (!next) clearSelection();
            }}
            data-testid="multi-select-toggle"
          >
            {showMultiSelect ? 'Multi-select ON' : 'Multi-select'}
          </Button>
        </Tooltip>
      </Stack>

      {/* Band filter */}
      <BandFilterBar
        selected={bandFilter}
        counts={bandCounts}
        loading={statsLoading}
        onChange={handleBandChange}
      />

      {/* Deep-link book filter indicator */}
      {bookFromURL && (
        <Alert
          severity="info"
          sx={{ mb: 2 }}
          action={
            <IconButton
              size="small"
              onClick={() => {
                const next = new URLSearchParams(searchParams);
                next.delete('book');
                setSearchParams(next, { replace: true });
              }}
              aria-label="clear book filter"
            >
              <ClearIcon fontSize="small" />
            </IconButton>
          }
        >
          Showing candidates for book <strong>{bookFromURL}</strong>
        </Alert>
      )}

      {/* Search + filters */}
      <Box
        sx={{
          mb: 2,
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          flexWrap: 'wrap',
        }}
      >
        <TextField
          size="small"
          placeholder="Search by book ID, layer, band…"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          sx={{ minWidth: 280 }}
          InputProps={{
            endAdornment: searchQuery ? (
              <IconButton size="small" onClick={() => setSearchQuery('')} aria-label="clear search">
                <ClearIcon fontSize="small" />
              </IconButton>
            ) : null,
          }}
        />
        <Tooltip title="Show only pairs where NEITHER book has matched metadata — both are low-quality and need manual matching.">
          <FormControlLabel
            control={
              <Checkbox
                size="small"
                checked={bothUnmatched}
                onChange={(e) => {
                  setBothUnmatched(e.target.checked);
                  setPage(0);
                }}
              />
            }
            label="Both need manual matching"
          />
        </Tooltip>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Table */}
      {loading ? (
        <Box sx={{ textAlign: 'center', py: 4 }}>
          <CircularProgress />
        </Box>
      ) : filteredCandidates.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">
            No candidates found for the current filter.
          </Typography>
        </Paper>
      ) : (
        <>
          <Paper variant="outlined">
            <Table size="small">
              <TableHead>
                <TableRow>
                  {showMultiSelect && (
                    <TableCell padding="checkbox">
                      <Checkbox
                        size="small"
                        indeterminate={selected.size > 0 && selected.size < filteredCandidates.length}
                        checked={
                          filteredCandidates.length > 0 && selected.size === filteredCandidates.length
                        }
                        onChange={(e) => (e.target.checked ? selectAll() : clearSelection())}
                      />
                    </TableCell>
                  )}
                  <TableCell>Book A</TableCell>
                  <TableCell>Book B</TableCell>
                  <TableCell align="center">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {filteredCandidates.map((c, idx) => {
                  const busy = bulkBusy;
                  const bookA = c.book_a;
                  const bookB = c.book_b;
                  const qA = metadataQuality(bookA);
                  const qB = metadataQuality(bookB);
                  // Use the shared helper so the ★ chip and `m` shortcut always agree.
                  const rec = recommendedKeepSide(c);
                  const recommendA = rec?.label === 'A';
                  const recommendB = rec?.label === 'B';
                  const isSelected = selected.has(c.id);
                  const isFocused = idx === focusedRowIndex;
                  return (
                    <TableRow
                      key={c.id}
                      ref={(node) => {
                        rowRefs.current[idx] = node;
                      }}
                      hover
                      selected={isSelected}
                      onClick={(e) => handleRowClick(e, c.id, idx)}
                      data-testid={`dedup-candidate-row-${c.id}`}
                      aria-current={isFocused ? 'true' : undefined}
                      sx={[{
                        cursor: 'pointer',
                        outlineColor: 'primary.main',
                        outlineOffset: -2
                      }, busy ? {
                        opacity: 0.7
                      } : {
                        opacity: 1
                      }, isSelected ? {
                        bgcolor: null
                      } : {
                        bgcolor: idx % 2 === 1
                            ? 'action.hover'
                            : 'transparent'
                      }, isFocused ? {
                        outline: 2
                      } : {
                        outline: 0
                      }]}
                    >
                      {showMultiSelect && (
                        <TableCell padding="checkbox">
                          <Checkbox
                            size="small"
                            checked={isSelected}
                            onChange={() => toggleSelect(c.id)}
                            onClick={(e) => e.stopPropagation()}
                          />
                        </TableCell>
                      )}
                      {/* Book A: cover left + score badges above title + quality chip inline */}
                      <TableCell sx={{ verticalAlign: 'middle', minWidth: 300 }}>
                        {renderBookCard(bookA, c.entity_a_id, {
                          badgesNode: (
                            <>
                              <ScoreBadgeRow
                                band={c.band}
                                score={c.score}
                                layer={c.layer}
                                similarity={c.similarity}
                              />
                              <Chip
                                label={c.status}
                                size="small"
                                color={
                                  c.status === 'merged'
                                    ? 'success'
                                    : c.status === 'dismissed'
                                      ? 'default'
                                      : 'warning'
                                }
                                variant="outlined"
                              />
                            </>
                          ),
                          qualityChipNode: qualityChip(qA),
                          recommendedNode: recommendA ? (
                            <Chip label="★ Recommended keep" size="small" color="primary" />
                          ) : undefined,
                        })}
                      </TableCell>
                      {/* Book B: cover left + quality chip inline */}
                      <TableCell sx={{ verticalAlign: 'middle', minWidth: 280 }}>
                        {renderBookCard(bookB, c.entity_b_id, {
                          qualityChipNode: qualityChip(qB),
                          recommendedNode: recommendB ? (
                            <Chip label="★ Recommended keep" size="small" color="primary" />
                          ) : undefined,
                        })}
                      </TableCell>
                      <TableCell sx={{ verticalAlign: 'top' }}>
                        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                          {c.status === 'pending' && (
                            <>
                              <Tooltip title="Keep Book A, merge B into it">
                                <Button
                                  size="small"
                                  variant={recommendA ? 'contained' : 'outlined'}
                                  color="primary"
                                  disabled={busy}
                                  onClick={() => handleKeep(c.id, c.entity_a_id, 'A')}
                                >
                                  Keep A
                                </Button>
                              </Tooltip>
                              <Tooltip title="Keep Book B, merge A into it">
                                <Button
                                  size="small"
                                  variant={recommendB ? 'contained' : 'outlined'}
                                  color="primary"
                                  disabled={busy}
                                  onClick={() => handleKeep(c.id, c.entity_b_id, 'B')}
                                >
                                  Keep B
                                </Button>
                              </Tooltip>
                            </>
                          )}
                          <Tooltip title="Compare side-by-side with full score breakdown">
                            <Button
                              size="small"
                              variant="outlined"
                              onClick={() => setDrawerCandidateId(c.id)}
                            >
                              Compare
                            </Button>
                          </Tooltip>
                          {c.status === 'pending' && (
                            <Tooltip title="Not a duplicate — dismiss">
                              <Button
                                size="small"
                                variant="text"
                                color="inherit"
                                disabled={busy}
                                onClick={() => handleDismissOne(c.id)}
                              >
                                Dismiss
                              </Button>
                            </Tooltip>
                          )}
                        </Stack>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </Paper>

          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, p) => {
              setPage(p);
              setSelected(new Set());
            }}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => {
              const val = parseInt(e.target.value, 10);
              setRowsPerPage(val);
              localStorage.setItem(STORAGE_KEYS.DEDUP_PAGE_SIZE, String(val));
              setPage(0);
              setSelected(new Set());
            }}
            rowsPerPageOptions={[10, 25, 50, 100, 250]}
          />
        </>
      )}

      {/* Bulk action bar (sticky bottom) */}
      <BulkActionBar
        selectedCount={selected.size}
        total={total}
        bandFilter={bandFilter}
        isBusy={bulkBusy}
        onMergeSelected={handleMergeSelected}
        onDismissSelected={handleDismissSelected}
        onMergeAllFiltered={handleMergeAllFiltered}
        onClearSelection={clearSelection}
      />

      {/* Comparison drawer */}
      <CandidateCompareDrawer
        candidateId={drawerCandidateId}
        onClose={() => setDrawerCandidateId(null)}
        onMerged={() => {
          loadCandidates();
          loadStats();
        }}
        onDismissed={() => {
          loadCandidates();
          loadStats();
        }}
      />

      {/* Dedup keyboard shortcut help */}
      <Dialog
        open={shortcutHelpOpen}
        onClose={() => setShortcutHelpOpen(false)}
        maxWidth="xs"
        fullWidth
      >
        <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <HelpOutlineIcon fontSize="small" />
          <Box sx={{ flex: 1 }}>Dedup Keyboard Shortcuts</Box>
          <IconButton
            size="small"
            onClick={() => setShortcutHelpOpen(false)}
            aria-label="close shortcuts help"
          >
            <ClearIcon fontSize="small" />
          </IconButton>
        </DialogTitle>
        <DialogContent dividers>
          <Stack spacing={1}>
            {DEDUP_SHORTCUTS.map((shortcut, idx) => (
              <Box key={shortcut.keys}>
                {idx > 0 && <Divider sx={{ mb: 1 }} />}
                <Stack direction="row" spacing={2} alignItems="center" justifyContent="space-between">
                  <Typography variant="body2">{shortcut.action}</Typography>
                  <Chip
                    label={shortcut.keys}
                    size="small"
                    variant="outlined"
                    sx={{ fontFamily: 'monospace', flexShrink: 0 }}
                  />
                </Stack>
              </Box>
            ))}
          </Stack>
        </DialogContent>
      </Dialog>

      {/* Force Full Rescan modal — pick which detection layer to re-run */}
      <Dialog open={rescanOpen} onClose={() => setRescanOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Force full rescan</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            Pick which detection layer to re-run from scratch. These are heavier than the
            incremental “Find Duplicates” scan — each rebuilds a whole layer of candidates and runs
            in the background (watch the bell for progress).
          </DialogContentText>
          <Stack spacing={1}>
            {RESCAN_OPTIONS.map((opt) => (
              <Button
                key={opt.kind}
                variant="outlined"
                fullWidth
                disabled={bulkBusy}
                onClick={() => handleForceRescan(opt)}
                data-testid={`force-rescan-${opt.kind}`}
                sx={{
                  justifyContent: 'flex-start',
                  textAlign: 'left',
                  textTransform: 'none',
                  flexDirection: 'column',
                  alignItems: 'flex-start',
                  py: 1,
                }}
              >
                <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
                  {opt.label}
                </Typography>
                <Typography variant="caption" color="text.secondary" sx={{ whiteSpace: 'normal' }}>
                  {opt.desc}
                </Typography>
              </Button>
            ))}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRescanOpen(false)}>Cancel</Button>
        </DialogActions>
      </Dialog>

      {/* Rescore dialog */}
      <Dialog open={rescoringOpen} onClose={() => setRescoringOpen(false)}>
        <DialogTitle>Rescore dedup candidates</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Re-runs the unified scoring formula over stored signal sets for all pending candidates.
            No re-embedding or re-collection is performed — only candidates that already have stored
            signals (T015+) will be updated. Pre-T015 rows are counted as skipped.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRescoringOpen(false)}>Cancel</Button>
          <Button
            onClick={() => handleRescore(false)}
            disabled={bulkBusy}
            data-testid="rescore-dry-run-btn"
          >
            Dry Run
          </Button>
          <Button
            onClick={() => handleRescore(true)}
            variant="contained"
            color="warning"
            disabled={bulkBusy}
            data-testid="rescore-apply-btn"
          >
            Apply
          </Button>
        </DialogActions>
      </Dialog>

      {/* Toast */}
      <Snackbar
        open={toast !== null}
        autoHideDuration={5000}
        onClose={(_, reason) => {
          if (reason !== 'clickaway') setToast(null);
        }}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
      >
        <Alert severity="info" variant="filled" onClose={() => setToast(null)}>
          {toast}
        </Alert>
      </Snackbar>
    </Box>
  );
}
