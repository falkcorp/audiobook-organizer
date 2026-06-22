// file: web/src/components/dedup/DedupEmbeddingTab.tsx
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567891
// last-edited: 2026-06-22

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Tooltip,
  Card,
  CardContent,
  CardActions,
  Stack,
  Checkbox,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
  TablePagination,
  Menu,
  MenuItem,
  Snackbar,
  TextField,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import RefreshIcon from '@mui/icons-material/Refresh';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import CloseIcon from '@mui/icons-material/Close';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import HeadphonesIcon from '@mui/icons-material/Headphones';
import ClearIcon from '@mui/icons-material/Clear';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import AutoAwesomeIcon from '@mui/icons-material/AutoAwesome';
import DownloadIcon from '@mui/icons-material/Download';
import StarBorderIcon from '@mui/icons-material/StarBorder';
import * as api from '../../services/api';
import type { Book, DedupCandidate, DedupStats, Operation } from '../../services/api';
import { useOperationsStore } from '../../stores/useOperationsStore';
import { FilterTagBar, type FilterTag } from '../common/FilterTagBar';
import { AudioSampleCompare } from '../AudioSampleCompare';
import type { SampleBook } from '../AudioSampleCompare';
import { cleanDisplayTitle } from './dedupHelpers';

/** Cached book details for candidate display */
const bookCache = new Map<string, Book>();
/** Cached book file lists. Files are fetched in parallel with book details so
 * hovering a file path can show every segment without waiting on a network
 * round trip. An empty array means "we tried and got none", `undefined`
 * means "not fetched yet". */
const bookFilesCache = new Map<string, string[]>();

export async function fetchBookCached(id: string): Promise<Book | null> {
  if (bookCache.has(id)) return bookCache.get(id)!;
  try {
    const book = await api.getBook(id);
    bookCache.set(id, book);
    return book;
  } catch {
    return null;
  }
}

export async function fetchBookFilesCached(id: string): Promise<string[]> {
  const cached = bookFilesCache.get(id);
  if (cached) return cached;
  try {
    const { files } = await api.getBookFiles(id);
    const paths = (files || []).map((f) => f.file_path).filter(Boolean);
    bookFilesCache.set(id, paths);
    return paths;
  } catch {
    bookFilesCache.set(id, []);
    return [];
  }
}

const LAYER_COLORS: Record<string, 'error' | 'primary' | 'secondary'> = {
  exact: 'error',
  embedding: 'primary',
  llm: 'secondary',
};

/**
 * A cluster groups candidate pairs that share books via connected components.
 * A 2-way cluster is a single pair; a 3+ way cluster is what happens when
 * (A,B), (B,C), (A,C) all hit — previously shown as three duplicate-looking
 * rows, now collapsed into one multi-book card.
 */
interface BookCluster {
  key: string;
  bookIds: string[];
  candidateIds: number[];
  layer: string;
  maxSimilarity: number | null;
  hasPending: boolean;
  overallStatus: string;
  llmInfo: string | null;
}

const LAYER_RANK: Record<string, number> = { exact: 3, llm: 2, embedding: 1 };

/**
 * Group candidates into clusters using union-find. Each cluster's layer is
 * the strongest layer seen across its pairs (exact > llm > embedding) so
 * the visual chip reflects the most trustworthy signal in the group.
 */
function buildClusters(candidates: DedupCandidate[]): BookCluster[] {
  const parent = new Map<string, string>();
  const find = (x: string): string => {
    let root = x;
    while (parent.get(root) !== root) root = parent.get(root)!;
    let cur = x;
    while (parent.get(cur) !== root) {
      const next = parent.get(cur)!;
      parent.set(cur, root);
      cur = next;
    }
    return root;
  };
  const union = (a: string, b: string) => {
    const ra = find(a);
    const rb = find(b);
    if (ra !== rb) parent.set(ra, rb);
  };
  for (const c of candidates) {
    if (!parent.has(c.entity_a_id)) parent.set(c.entity_a_id, c.entity_a_id);
    if (!parent.has(c.entity_b_id)) parent.set(c.entity_b_id, c.entity_b_id);
    union(c.entity_a_id, c.entity_b_id);
  }
  const groups = new Map<string, BookCluster>();
  for (const c of candidates) {
    const root = find(c.entity_a_id);
    let g = groups.get(root);
    if (!g) {
      g = {
        key: root,
        bookIds: [],
        candidateIds: [],
        layer: c.layer,
        maxSimilarity: c.similarity ?? null,
        hasPending: false,
        overallStatus: c.status,
        llmInfo: null,
      };
      groups.set(root, g);
    }
    if (!g.bookIds.includes(c.entity_a_id)) g.bookIds.push(c.entity_a_id);
    if (!g.bookIds.includes(c.entity_b_id)) g.bookIds.push(c.entity_b_id);
    g.candidateIds.push(c.id);
    if ((LAYER_RANK[c.layer] ?? 0) > (LAYER_RANK[g.layer] ?? 0)) g.layer = c.layer;
    if (c.similarity != null && (g.maxSimilarity == null || c.similarity > g.maxSimilarity)) {
      g.maxSimilarity = c.similarity;
    }
    if (c.status === 'pending') g.hasPending = true;
    if (g.overallStatus !== c.status) g.overallStatus = 'mixed';
    if (c.llm_reason && !g.llmInfo) g.llmInfo = `${c.llm_verdict ?? ''}: ${c.llm_reason}`;
  }
  // Order clusters by the lowest candidate id they contain so the page
  // order stays stable across refreshes.
  return Array.from(groups.values()).sort((a, b) => {
    const minA = Math.min(...a.candidateIds);
    const minB = Math.min(...b.candidateIds);
    return minA - minB;
  });
}

/**
 * Strip everything up to and including "audiobook-organizer/" so long
 * production paths don't blow out the card width. Falls back to the full
 * path if the marker isn't present (e.g. during tests or odd mounts).
 */
function truncateAudiobookPath(path: string | undefined | null): string {
  if (!path) return '';
  const marker = 'audiobook-organizer/';
  const idx = path.indexOf(marker);
  return idx >= 0 ? path.slice(idx + marker.length) : path;
}

export function EmbeddingDedupTab() {
  const navigate = useNavigate();
  const [stats, setStats] = useState<DedupStats[]>([]);
  const [candidates, setCandidates] = useState<DedupCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<string>('pending');
  const [layerFilter, setLayerFilter] = useState<string>('');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);
  // Client-side search over the currently-loaded page of
  // candidates. Searches title, author, and file path on both
  // sides of each cluster. Case-insensitive substring match.
  // For a broader search, bump rowsPerPage first or export to
  // CSV and grep.
  const [searchQuery, setSearchQuery] = useState('');
  const [bookDetails, setBookDetails] = useState<Map<string, Book>>(new Map());
  const [bookFiles, setBookFiles] = useState<Map<string, string[]>>(new Map());
  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanMsg, setScanMsg] = useState<string | null>(null);
  const [bulkMergeOpen, setBulkMergeOpen] = useState(false);
  const [pageMergeOpen, setPageMergeOpen] = useState(false);
  const [exportMenuAnchor, setExportMenuAnchor] = useState<HTMLElement | null>(null);
  const [moreMenuAnchor, setMoreMenuAnchor] = useState<HTMLElement | null>(null);
  const [seriesMergeOpen, setSeriesMergeOpen] = useState(false);
  const [seriesMergeLoading, setSeriesMergeLoading] = useState(false);
  const [seriesSummary, setSeriesSummary] = useState<api.DedupSeriesSummary[]>([]);
  const [seriesMergeRunning, setSeriesMergeRunning] = useState<number | null>(null);
  // Per-cluster multi-select state for the split-cluster workflow.
  // Key: cluster.key → set of selected bookIds. When at least one
  // book is selected for a cluster, the split-cluster action bar
  // appears at the bottom of that card.
  const [splitSelections, setSplitSelections] = useState<Map<string, Set<string>>>(new Map());
  const [pageMerging, setPageMerging] = useState(false);
  const [bulkMerging, setBulkMerging] = useState(false);
  const [compareCluster, setCompareCluster] = useState<{ a: SampleBook; b: SampleBook } | null>(null);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  // Load stats
  const loadStats = useCallback(async () => {
    try {
      const { stats: s } = await api.getDedupStats();
      setStats(s);
    } catch {
      // stats are optional
    }
  }, []);

  // Load candidates
  const loadCandidates = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const params: Parameters<typeof api.getDedupCandidates>[0] = {
        status: statusFilter || undefined,
        layer: layerFilter || undefined,
        limit: rowsPerPage,
        offset: page * rowsPerPage,
      };
      const resp = await api.getDedupCandidates(params);
      setCandidates(resp.candidates || []);
      setTotal(resp.total || 0);

      // Fetch book details + file lists in parallel for every candidate
      // side. File lists are what makes the "hover for all files" tooltip
      // instant — without them a 4-way cluster would trigger a burst of
      // network requests on mouse-over.
      const ids = new Set<string>();
      for (const c of resp.candidates || []) {
        ids.add(c.entity_a_id);
        ids.add(c.entity_b_id);
      }
      const details = new Map<string, Book>();
      const filesMap = new Map<string, string[]>();
      await Promise.all(
        Array.from(ids).flatMap((id) => [
          fetchBookCached(id).then((book) => {
            if (book) details.set(id, book);
          }),
          fetchBookFilesCached(id).then((paths) => {
            filesMap.set(id, paths);
          }),
        ])
      );
      setBookDetails(details);
      setBookFiles(filesMap);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load candidates');
    } finally {
      setLoading(false);
    }
  }, [statusFilter, layerFilter, page, rowsPerPage]);

  useEffect(() => { loadStats(); }, [loadStats]);
  useEffect(() => { loadCandidates(); }, [loadCandidates]);

  // Cleanup scan/LLM timeouts on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  // Open the Merge Series dialog, which fetches the list of series
  // with pending cluster candidates and lets the user fire a
  // per-series bulk merge. Re-fetches on every open so the counts
  // match current state.
  const handleOpenSeriesMerge = async () => {
    setSeriesMergeOpen(true);
    setSeriesMergeLoading(true);
    try {
      const summary = await api.listDedupCandidateSeries();
      setSeriesSummary(summary);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load series summary');
      setSeriesSummary([]);
    } finally {
      setSeriesMergeLoading(false);
    }
  };

  const handleMergeSeries = async (seriesId: number) => {
    setSeriesMergeRunning(seriesId);
    try {
      const result = await api.mergeDedupCandidateSeries(seriesId);
      setScanMsg(
        `Series merge complete: ${result.clusters_merged} cluster(s) merged, ${result.books_merged} books`
      );
      // Refresh the summary so the just-merged series disappears.
      const fresh = await api.listDedupCandidateSeries();
      setSeriesSummary(fresh);
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Series merge failed');
    } finally {
      setSeriesMergeRunning(null);
    }
  };

  // Download the current filtered candidate set as CSV or JSON. Builds
  // the query string with whatever filters the user has active (status,
  // layer) so what they export matches what they see. Navigates to the
  // endpoint via an anchor click so the browser handles the file save.
  const handleExport = (format: 'csv' | 'json') => {
    const params = new URLSearchParams({ format });
    if (statusFilter) params.set('status', statusFilter);
    if (layerFilter) params.set('layer', layerFilter);
    const url = `/api/v1/dedup/candidates/export?${params.toString()}`;
    const a = document.createElement('a');
    a.href = url;
    a.download = ''; // let the server Content-Disposition pick the name
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  };

  const handleMergeCluster = async (cluster: BookCluster, primaryBookId?: string) => {
    setActionLoading(primaryBookId ? `${cluster.key}:primary:${primaryBookId}` : cluster.key);
    try {
      await api.mergeDedupCluster(cluster.bookIds, primaryBookId);
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setActionLoading(null);
    }
  };

  const handleDismissCluster = async (cluster: BookCluster) => {
    setActionLoading(cluster.key);
    try {
      await api.dismissDedupCluster(cluster.bookIds);
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setActionLoading(null);
    }
  };

  // Remove a single book from a 3+ way cluster. Dismisses just the edges
  // between this book and the other cluster members, leaving the rest as
  // a smaller cluster the user can still merge.
  const handleRemoveFromCluster = async (cluster: BookCluster, bookId: string) => {
    setActionLoading(`${cluster.key}:${bookId}`);
    try {
      await api.removeFromDedupCluster(cluster.bookIds, bookId);
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Remove from cluster failed');
    } finally {
      setActionLoading(null);
    }
  };

  // Toggle whether a specific book is selected for multi-select split
  // on a given cluster. Immutable map update so React re-renders the
  // cluster card.
  const toggleSplitSelection = (cluster: BookCluster, bookId: string) => {
    setSplitSelections((prev) => {
      const next = new Map(prev);
      const current = new Set(next.get(cluster.key) ?? []);
      if (current.has(bookId)) {
        current.delete(bookId);
      } else {
        current.add(bookId);
      }
      if (current.size === 0) {
        next.delete(cluster.key);
      } else {
        next.set(cluster.key, current);
      }
      return next;
    });
  };

  // Remove every selected book from a cluster in one backend call.
  // This is what the split-cluster multi-select workflow commits:
  // "this 6-way cluster is really two groups, let me kick out three
  // of the books in one action instead of clicking × three times".
  const handleRemoveSelectedFromCluster = async (cluster: BookCluster) => {
    const selected = splitSelections.get(cluster.key);
    if (!selected || selected.size === 0) return;
    const removeIds = Array.from(selected);
    setActionLoading(`${cluster.key}:split`);
    try {
      await api.removeFromDedupCluster(cluster.bookIds, removeIds);
      // Clear selection for this cluster on success.
      setSplitSelections((prev) => {
        const next = new Map(prev);
        next.delete(cluster.key);
        return next;
      });
      loadCandidates();
      loadStats();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Remove from cluster failed');
    } finally {
      setActionLoading(null);
    }
  };

  const handleOpenCompare = (cluster: BookCluster) => {
    if (cluster.bookIds.length < 2) return;
    const toSample = (id: string): SampleBook => {
      const book = bookDetails.get(id);
      return {
        id,
        title: book?.title ?? id,
        authors: book?.authors?.map((a) => a.name).join(', '),
        filePath: book?.file_path,
        duration: book?.duration ?? undefined,
      };
    };
    setCompareCluster({ a: toSample(cluster.bookIds[0]), b: toSample(cluster.bookIds[1]) });
  };

  // trackOp registers the returned op with the operations store so the bell
  // icon and Activity page surface it within one poll cycle, instead of
  // waiting up to 15s for the next background ActiveOperations sweep.
  // Returns a user-facing message that names the op type and id.
  const trackOp = (op: Operation, label: string): string => {
    if (op?.id && op?.type) {
      useOperationsStore.getState().startPolling(op.id, op.type);
      return `${label} started — see bell icon for progress (op ${op.id.slice(-6)})`;
    }
    return `${label} started`;
  };

  const handleScan = async () => {
    setScanning(true);
    setScanMsg(null);
    try {
      const op = await api.triggerDedupScan();
      setScanMsg(trackOp(op, 'Dedup scan'));
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadCandidates();
          loadStats();
        }
        timeoutRef.current = null;
      }, 2000);
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  };

  const handleLLM = async () => {
    setScanning(true);
    setScanMsg(null);
    try {
      const op = await api.triggerDedupLLM();
      setScanMsg(trackOp(op, 'AI review'));
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadCandidates();
          loadStats();
        }
        timeoutRef.current = null;
      }, 3000);
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'AI review failed');
    } finally {
      setScanning(false);
    }
  };

  const handleAcoustID = async () => {
    setScanning(true);
    setScanMsg(null);
    try {
      const op = await api.triggerDedupAcoustID();
      setScanMsg(trackOp(op, 'AcoustID scan'));
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadCandidates();
          loadStats();
        }
      }, 3000);
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'AcoustID scan failed');
    } finally {
      setScanning(false);
    }
  };

  const handleEmbed = async () => {
    setScanning(true);
    setScanMsg(null);
    try {
      const op = await api.triggerEmbedScan();
      setScanMsg(trackOp(op, 'Embedding rescan'));
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'Embedding scan failed');
    } finally {
      setScanning(false);
    }
  };

  // clusters must be computed before the page-merge handler so the
  // handler closure can read it directly.
  const allClusters = useMemo(() => buildClusters(candidates), [candidates]);

  // Apply the client-side search filter. Searches title,
  // every author on book.authors, and file path on every book
  // in every cluster. A cluster is kept if ANY of its books
  // matches — search "Foundation" and you want the whole
  // cluster for Foundation to show up, not just one side.
  // When searchQuery is empty, returns allClusters unchanged.
  const clusters = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return allClusters;
    return allClusters.filter((cluster) => {
      for (const bookId of cluster.bookIds) {
        const book = bookDetails.get(bookId);
        if (!book) continue;
        if ((book.title || '').toLowerCase().includes(q)) return true;
        if ((book.file_path || '').toLowerCase().includes(q)) return true;
        const authors = book.authors || [];
        for (const a of authors) {
          if ((a.name || '').toLowerCase().includes(q)) return true;
        }
      }
      return false;
    });
  }, [allClusters, searchQuery, bookDetails]);

  const handleBulkMerge = async () => {
    setBulkMerging(true);
    setBulkMergeOpen(false);
    setScanMsg(null);
    try {
      const result = await api.bulkMergeDedupCandidates({
        entity_type: 'book',
        status: statusFilter || 'pending',
        layer: layerFilter || undefined,
      });
      setScanMsg(
        `Bulk merge complete: ${result.merged} merged, ${result.failed} failed (of ${result.attempted} matched)`
      );
      loadCandidates();
      loadStats();
    } catch (err) {
      setScanMsg(err instanceof Error ? err.message : 'Bulk merge failed');
    } finally {
      setBulkMerging(false);
    }
  };

  // Merge every cluster currently visible on the page. This is the
  // incremental-review path: the user skims what's on-screen, trusts the
  // lot, and wants to commit just those without also merging every
  // off-page candidate the filter matches. Iterates buildClusters
  // output and calls mergeDedupCluster serially — for a 25-item page
  // that's typically 5-15 clusters, well under a second each.
  const handleMergePage = async () => {
    setPageMerging(true);
    setPageMergeOpen(false);
    setScanMsg(null);
    let merged = 0;
    let failed = 0;
    const firstError: { msg?: string } = {};
    for (const cluster of clusters) {
      if (!cluster.hasPending) continue;
      try {
        await api.mergeDedupCluster(cluster.bookIds);
        merged++;
      } catch (err) {
        failed++;
        if (!firstError.msg) {
          firstError.msg = err instanceof Error ? err.message : String(err);
        }
      }
    }
    const summary =
      failed === 0
        ? `Page merge complete: ${merged} cluster${merged === 1 ? '' : 's'} merged`
        : `Page merge: ${merged} merged, ${failed} failed${firstError.msg ? ` (${firstError.msg})` : ''}`;
    setScanMsg(summary);
    loadCandidates();
    loadStats();
    setPageMerging(false);
  };

  // Aggregate stats for display
  // Status-dimension counts. The layer-dimension counts below intentionally
  // aggregate ACROSS statuses so "10 exact" means "10 exact-layer candidates
  // of any status", matching the existing semantics users have seen. Status
  // counts only count rows in that specific status bucket.
  const pendingCount = stats.filter(s => s.status === 'pending').reduce((sum, s) => sum + s.count, 0);
  const mergedCount = stats.filter(s => s.status === 'merged').reduce((sum, s) => sum + s.count, 0);
  const dismissedCount = stats.filter(s => s.status === 'dismissed').reduce((sum, s) => sum + s.count, 0);
  const exactCount = stats.filter(s => s.layer === 'exact').reduce((sum, s) => sum + s.count, 0);
  const embeddingCount = stats.filter(s => s.layer === 'embedding').reduce((sum, s) => sum + s.count, 0);
  const llmCount = stats.filter(s => s.layer === 'llm').reduce((sum, s) => sum + s.count, 0);

  // renderBookSide takes the cluster it belongs to so the per-side
  // "Not a duplicate" button can scope its dismiss to that cluster's
  // pairs only. The button only appears for 3+ way clusters — in a 2-way
  // cluster, removing one side is the same as dismissing the whole
  // cluster, so we show the existing cluster-level Dismiss button instead.
  const renderBookSide = (id: string, cluster: BookCluster) => {
    const book = bookDetails.get(id);
    if (!book) {
      return (
        <Typography variant="body2" color="text.secondary">
          Book #{id}
        </Typography>
      );
    }
    const isMultiWay = cluster.bookIds.length > 2;
    const removeBusy = actionLoading === `${cluster.key}:${id}`;
    const anyActionBusy = actionLoading != null;
    const allFiles = bookFiles.get(id) ?? [];
    // Prefer the full file list (book_files table) over the Book.file_path
    // column because multi-file audiobooks only track the first file on the
    // Book row. When the list is empty (iTunes-linked, unorganized, or
    // haven't-loaded-yet) we fall back to Book.file_path so something shows.
    const primaryPath = allFiles[0] ?? book.file_path ?? '';
    const shortPath = truncateAudiobookPath(primaryPath);
    const extraCount = Math.max(0, allFiles.length - 1);
    // Build a multi-line tooltip that lists every file with the repo-root
    // prefix stripped. This is what lets the user tell near-identical
    // cluster sides apart — "Turn Coat / Turn Coat - 1" vs
    // "Turn Coat / Turn Coat - 1" looks identical until you see the full
    // file lists diverge.
    const tooltipContent =
      allFiles.length > 0 ? (
        <Box sx={{ maxWidth: 600 }}>
          <Typography variant="caption" sx={{ fontWeight: 600, display: 'block', mb: 0.5 }}>
            {allFiles.length} file{allFiles.length === 1 ? '' : 's'}:
          </Typography>
          {allFiles.map((p, idx) => (
            <Typography
              key={idx}
              variant="caption"
              sx={{ display: 'block', fontFamily: 'monospace', fontSize: '0.7rem', whiteSpace: 'pre' }}
            >
              {truncateAudiobookPath(p)}
            </Typography>
          ))}
        </Box>
      ) : (
        <Typography variant="caption" sx={{ fontFamily: 'monospace' }}>
          {primaryPath || '(no file path)'}
        </Typography>
      );
    return (
      <Box sx={{ minWidth: 0, position: 'relative' }}>
        <Box
          sx={{ cursor: 'pointer', minWidth: 0, '&:hover .dedup-side-title': { textDecoration: 'underline' } }}
          onClick={() => navigate(`/library/${book.id}`)}
        >
          <Typography
            className="dedup-side-title"
            variant="body2"
            fontWeight="medium"
            noWrap
            title={book.title}
            sx={{ pr: isMultiWay ? 3 : 0 }} // leave room for the button
          >
            {cleanDisplayTitle(book.title)}
          </Typography>
          {book.author_name && (
            <Typography variant="caption" color="text.secondary" noWrap title={book.author_name}>
              {book.author_name}
            </Typography>
          )}
          {book.book_sig_coverage_pct != null && book.book_sig_coverage_pct < 100 && (
            <Tooltip
              title={`Book signature was synthesized from partial audio (${book.book_sig_coverage_pct}% real, rest is silence-padded). Similarity matches against this book may be less reliable than full-coverage matches.`}
            >
              <Chip
                label={`partial fp ${book.book_sig_coverage_pct}%`}
                size="small"
                color="warning"
                variant="outlined"
                onClick={(e) => e.stopPropagation()}
                sx={{
                  height: 16,
                  fontSize: '0.6rem',
                  mt: 0.25,
                  mr: 0.5,
                  '& .MuiChip-label': { px: 0.5 },
                }}
              />
            </Tooltip>
          )}
          {shortPath && (
            <Tooltip
              title={tooltipContent}
              enterDelay={300}
              placement="bottom-start"
              componentsProps={{ tooltip: { sx: { maxWidth: 'none' } } }}
            >
              <Typography
                variant="caption"
                color="text.disabled"
                noWrap
                sx={{ display: 'block', fontFamily: 'monospace', fontSize: '0.7rem' }}
                onClick={(e) => e.stopPropagation()}
              >
                {shortPath}
                {extraCount > 0 && (
                  <Box component="span" sx={{ ml: 0.5, color: 'primary.main', fontWeight: 600 }}>
                    +{extraCount} more
                  </Box>
                )}
              </Typography>
            </Tooltip>
          )}
        </Box>
        {cluster.hasPending && (
          <Tooltip title="Merge cluster — keep THIS book as primary (overrides auto-pick)">
            <span>
              <IconButton
                size="small"
                onClick={(e) => {
                  e.stopPropagation();
                  handleMergeCluster(cluster, id);
                }}
                disabled={anyActionBusy}
                sx={{
                  position: 'absolute',
                  top: -4,
                  right: isMultiWay ? 22 : -4,
                  padding: '2px',
                  color: 'text.disabled',
                  '&:hover': { color: 'warning.main' },
                }}
              >
                {actionLoading === `${cluster.key}:primary:${id}` ? (
                  <CircularProgress size={14} />
                ) : (
                  <StarBorderIcon sx={{ fontSize: 16 }} />
                )}
              </IconButton>
            </span>
          </Tooltip>
        )}
        {isMultiWay && cluster.hasPending && (
          <Tooltip title="Not a duplicate — remove this book from the cluster">
            <span>
              <IconButton
                size="small"
                onClick={(e) => {
                  e.stopPropagation();
                  handleRemoveFromCluster(cluster, id);
                }}
                disabled={anyActionBusy}
                sx={{
                  position: 'absolute',
                  top: -4,
                  right: -4,
                  padding: '2px',
                  color: 'text.disabled',
                  '&:hover': { color: 'error.main' },
                }}
              >
                {removeBusy ? <CircularProgress size={14} /> : <CloseIcon sx={{ fontSize: 16 }} />}
              </IconButton>
            </span>
          </Tooltip>
        )}
        {isMultiWay && cluster.hasPending && (
          <Tooltip title="Select for multi-remove">
            <Checkbox
              size="small"
              checked={splitSelections.get(cluster.key)?.has(id) ?? false}
              onClick={(e) => e.stopPropagation()}
              onChange={() => toggleSplitSelection(cluster, id)}
              disabled={anyActionBusy}
              sx={{
                position: 'absolute',
                top: -8,
                left: -8,
                padding: '4px',
              }}
            />
          </Tooltip>
        )}
      </Box>
    );
  };

  return (
    <Box>
      {/* Toolbar — primary "find duplicates" actions, then merge actions
          (defined further down). The "Force Re-embed All" maintenance
          action used to live up here as a peer button but it competed
          visually with the primary actions despite being a once-in-a-
          while task — moved into the More menu so it's still reachable
          but doesn't fight for attention. */}
      <Stack direction="row" spacing={1} sx={{ mb: 2 }} alignItems="center">
        <Tooltip title="Re-embed any stale books, then re-run exact + similarity matching to find new duplicate candidates. This is the standard 'find dupes again' button.">
          <span>
            <Button
              variant="contained"
              startIcon={scanning ? <CircularProgress size={16} /> : <RefreshIcon />}
              onClick={handleScan}
              disabled={scanning || bulkMerging}
              size="small"
            >
              Find Duplicates
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="Compare acoustic fingerprints (AcoustID) across all books to find audio-level duplicates. Catches re-encodes and chapter splits that text-similarity would miss.">
          <span>
            <Button
              variant="outlined"
              startIcon={scanning ? <CircularProgress size={16} /> : <FingerprintIcon />}
              onClick={handleAcoustID}
              disabled={scanning || bulkMerging}
              size="small"
            >
              Find Audio Duplicates
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="Run an LLM verdict (merge / dismiss / undecided) on existing pending candidates. Use after Find Duplicates surfaces a batch you want auto-classified. Costs OpenAI tokens.">
          <span>
            <Button
              variant="outlined"
              startIcon={scanning ? <CircularProgress size={16} /> : <AutoAwesomeIcon />}
              onClick={handleLLM}
              disabled={scanning || bulkMerging}
              size="small"
            >
              Run AI Review
            </Button>
          </span>
        </Tooltip>
        <Tooltip title="More actions">
          <span>
            <IconButton
              size="small"
              onClick={(e) => setMoreMenuAnchor(e.currentTarget)}
              disabled={scanning || bulkMerging}
              aria-label="more dedup actions"
            >
              <MoreVertIcon fontSize="small" />
            </IconButton>
          </span>
        </Tooltip>
        <Menu
          anchorEl={moreMenuAnchor}
          open={Boolean(moreMenuAnchor)}
          onClose={() => setMoreMenuAnchor(null)}
        >
          <MenuItem
            onClick={() => {
              setMoreMenuAnchor(null);
              void handleEmbed();
            }}
          >
            <Box>
              <Typography variant="body2">Force Re-embed All</Typography>
              <Typography variant="caption" color="text.secondary" display="block">
                Regenerate embeddings for every book. Only needed once
                after adding an OpenAI key — Find Duplicates already
                re-embeds stale books on its own.
              </Typography>
            </Box>
          </MenuItem>
        </Menu>
        <Button
          variant="outlined"
          color="warning"
          startIcon={bulkMerging ? <CircularProgress size={16} /> : <MergeIcon />}
          onClick={() => setBulkMergeOpen(true)}
          disabled={scanning || bulkMerging || pageMerging || total === 0 || statusFilter !== 'pending'}
          size="small"
          title={statusFilter !== 'pending' ? 'Switch to Pending filter to enable bulk merge' : ''}
        >
          Merge Filtered ({total})
        </Button>
        <Button
          variant="outlined"
          color="primary"
          startIcon={pageMerging ? <CircularProgress size={16} /> : <MergeIcon />}
          onClick={() => setPageMergeOpen(true)}
          disabled={scanning || bulkMerging || pageMerging || clusters.length === 0 || statusFilter !== 'pending'}
          size="small"
          title={statusFilter !== 'pending' ? 'Switch to Pending filter to enable page merge' : 'Merge only clusters visible on this page'}
        >
          Merge Page ({clusters.filter((c) => c.hasPending).length})
        </Button>
        <Button
          variant="outlined"
          color="secondary"
          startIcon={<MergeIcon />}
          onClick={handleOpenSeriesMerge}
          disabled={scanning || bulkMerging || pageMerging}
          size="small"
          title="Merge every pending cluster within a chosen series"
        >
          Merge Series
        </Button>
        <Button
          variant="outlined"
          color="inherit"
          startIcon={<DownloadIcon />}
          onClick={(e) => setExportMenuAnchor(e.currentTarget)}
          size="small"
          title="Download the current filtered candidate set as CSV or JSON"
        >
          Export
        </Button>
        <Menu
          anchorEl={exportMenuAnchor}
          open={Boolean(exportMenuAnchor)}
          onClose={() => setExportMenuAnchor(null)}
        >
          <MenuItem onClick={() => { handleExport('csv'); setExportMenuAnchor(null); }}>
            Download as CSV
          </MenuItem>
          <MenuItem onClick={() => { handleExport('json'); setExportMenuAnchor(null); }}>
            Download as JSON
          </MenuItem>
        </Menu>
      </Stack>

      {/* Scan/merge status lives in a bottom-right Snackbar instead of
          shoving an inline Alert into the toolbar. The inline version
          squeezed the toolbar and made the whole row look busted when
          a status fired. */}
      <Snackbar
        open={scanMsg !== null}
        autoHideDuration={6000}
        onClose={(_, reason) => {
          if (reason === 'clickaway') return;
          setScanMsg(null);
        }}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
      >
        <Alert
          severity="info"
          variant="filled"
          onClose={() => setScanMsg(null)}
          sx={{ minWidth: 280 }}
        >
          {scanMsg}
        </Alert>
      </Snackbar>

      {/* Bulk merge confirmation dialog */}
      <Dialog open={bulkMergeOpen} onClose={() => setBulkMergeOpen(false)}>
        <DialogTitle>Merge all filtered candidates?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            You are about to merge <strong>{total}</strong> candidate
            {total === 1 ? '' : 's'} matching the current filter
            {layerFilter ? ` (layer: ${layerFilter})` : ''}. Each candidate
            becomes a version group; this is irreversible.
          </DialogContentText>
          <DialogContentText sx={{ mt: 2 }}>
            <strong>Warning:</strong> Bulk merging trusts the scorer entirely.
            Review a sample first if you are not confident in the current
            filter's precision.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setBulkMergeOpen(false)}>Cancel</Button>
          <Button onClick={handleBulkMerge} color="warning" variant="contained">
            Merge {total}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Page merge confirmation dialog. Narrower-scope than the bulk
          merge — only touches clusters currently rendered on the page,
          which is the incremental-review path for users who trust what
          they see but not necessarily everything the filter matches. */}
      <Dialog open={pageMergeOpen} onClose={() => setPageMergeOpen(false)}>
        <DialogTitle>Merge clusters on this page?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            You are about to merge{' '}
            <strong>{clusters.filter((c) => c.hasPending).length}</strong>{' '}
            cluster{clusters.filter((c) => c.hasPending).length === 1 ? '' : 's'}{' '}
            currently visible on this page. Each cluster becomes one version
            group; this is irreversible.
          </DialogContentText>
          <DialogContentText sx={{ mt: 2 }}>
            Off-page candidates matching the same filter are <strong>not</strong>{' '}
            touched — use Merge Filtered for that. This lets you commit a
            reviewed subset without also merging everything the filter catches.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPageMergeOpen(false)}>Cancel</Button>
          <Button onClick={handleMergePage} color="primary" variant="contained">
            Merge {clusters.filter((c) => c.hasPending).length} cluster
            {clusters.filter((c) => c.hasPending).length === 1 ? '' : 's'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Series merge dialog — one row per series that has pending
          same-series cluster candidates. User clicks a row to merge
          every cluster in that series at once. Different from
          Merge Filtered because it's series-scoped regardless of
          the current status/layer filter. */}
      <Dialog
        open={seriesMergeOpen}
        onClose={() => setSeriesMergeOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Merge clusters by series</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            Each row below is a series that has pending duplicate
            clusters entirely within it. Click a row to merge every
            cluster in that series — each becomes its own version
            group. Cross-series candidates (pairs where the two sides
            belong to different series) are not touched.
          </DialogContentText>
          {seriesMergeLoading ? (
            <Box sx={{ textAlign: 'center', py: 3 }}><CircularProgress /></Box>
          ) : seriesSummary.length === 0 ? (
            <Typography color="text.secondary">
              No series with pending same-series clusters right now.
            </Typography>
          ) : (
            <Stack spacing={1}>
              {seriesSummary.map((row) => {
                const running = seriesMergeRunning === row.series_id;
                return (
                  <Box
                    key={row.series_id}
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      p: 1.5,
                      border: 1,
                      borderColor: 'divider',
                      borderRadius: 1,
                    }}
                  >
                    <Box sx={{ minWidth: 0, flex: 1 }}>
                      <Typography variant="body2" fontWeight="medium" noWrap>
                        {row.series_name || `(series #${row.series_id})`}
                      </Typography>
                      <Typography variant="caption" color="text.secondary">
                        {row.cluster_count} cluster{row.cluster_count === 1 ? '' : 's'} ·{' '}
                        {row.book_count} book{row.book_count === 1 ? '' : 's'} ·{' '}
                        {row.candidate_count} candidate{row.candidate_count === 1 ? '' : 's'}
                      </Typography>
                    </Box>
                    <Button
                      size="small"
                      variant="contained"
                      color="secondary"
                      onClick={() => handleMergeSeries(row.series_id)}
                      disabled={seriesMergeRunning != null}
                      startIcon={running ? <CircularProgress size={14} /> : <MergeIcon />}
                    >
                      Merge
                    </Button>
                  </Box>
                );
              })}
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSeriesMergeOpen(false)}>Close</Button>
        </DialogActions>
      </Dialog>

      {/* Stat chips — clickable. Each chip applies its corresponding
          filter (status or layer); the active filter chip is rendered
          filled instead of outlined so the user sees which slice they
          are viewing. The "showing" chip is informational only. The
          previous Tabs + secondary chip-toggle row below was redundant
          with this control and was removed — active filters now show
          up below as removable tags via FilterTagBar. */}
      <Stack direction="row" spacing={1} sx={{ mb: 2 }} flexWrap="wrap" useFlexGap>
        <Chip
          label={`${pendingCount} pending`}
          size="small"
          color="warning"
          variant={statusFilter === 'pending' ? 'filled' : 'outlined'}
          onClick={() => { setStatusFilter('pending'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip
          label={`${mergedCount} merged`}
          size="small"
          color="success"
          variant={statusFilter === 'merged' ? 'filled' : 'outlined'}
          onClick={() => { setStatusFilter('merged'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip
          label={`${dismissedCount} dismissed`}
          size="small"
          color="default"
          variant={statusFilter === 'dismissed' ? 'filled' : 'outlined'}
          onClick={() => { setStatusFilter('dismissed'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip
          label={`${exactCount} exact`}
          size="small"
          color="error"
          variant={layerFilter === 'exact' ? 'filled' : 'outlined'}
          onClick={() => { setLayerFilter(layerFilter === 'exact' ? '' : 'exact'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip
          label={`${embeddingCount} embedding`}
          size="small"
          color="primary"
          variant={layerFilter === 'embedding' ? 'filled' : 'outlined'}
          onClick={() => { setLayerFilter(layerFilter === 'embedding' ? '' : 'embedding'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip
          label={`${llmCount} LLM`}
          size="small"
          color="secondary"
          variant={layerFilter === 'llm' ? 'filled' : 'outlined'}
          onClick={() => { setLayerFilter(layerFilter === 'llm' ? '' : 'llm'); setPage(0); }}
          sx={{ cursor: 'pointer' }}
        />
        <Chip label={`${total} showing`} size="small" variant="outlined" />
      </Stack>

      {/* Active filters (removable). The bar hides itself when no
          filters are active, so it doesn't reserve empty visual space. */}
      <FilterTagBar
        tags={(() => {
          const tags: FilterTag[] = [];
          if (statusFilter) {
            tags.push({
              id: `status:${statusFilter}`,
              label: `Status: ${statusFilter}`,
              color:
                statusFilter === 'pending'
                  ? 'warning'
                  : statusFilter === 'merged'
                  ? 'success'
                  : 'default',
              onRemove: () => { setStatusFilter(''); setPage(0); },
            });
          }
          if (layerFilter) {
            tags.push({
              id: `layer:${layerFilter}`,
              label: `Layer: ${layerFilter}`,
              color: LAYER_COLORS[layerFilter] || 'default',
              onRemove: () => { setLayerFilter(''); setPage(0); },
            });
          }
          if (searchQuery.trim()) {
            tags.push({
              id: 'search',
              label: `Search: "${searchQuery.trim()}"`,
              color: 'info',
              onRemove: () => setSearchQuery(''),
            });
          }
          return tags;
        })()}
        onClearAll={() => {
          setStatusFilter('');
          setLayerFilter('');
          setSearchQuery('');
          setPage(0);
        }}
      />

      {/* Search box — live-filters the current page. The query is also
          mirrored into the FilterTagBar above so it can be cleared via
          the same X gesture as other filters. */}
      <Box sx={{ mb: 2, display: 'flex', alignItems: 'center', gap: 2 }}>
        <TextField
          size="small"
          placeholder="Search title, author, path…"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          sx={{ minWidth: 280 }}
          InputProps={{
            endAdornment: searchQuery ? (
              <IconButton
                size="small"
                onClick={() => setSearchQuery('')}
                aria-label="clear search"
              >
                <ClearIcon fontSize="small" />
              </IconButton>
            ) : null,
          }}
          helperText={
            searchQuery
              ? `${clusters.length} of ${allClusters.length} on page match`
              : 'Searches the current page only'
          }
        />
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}

      {loading ? (
        <Box sx={{ textAlign: 'center', py: 4 }}><CircularProgress /></Box>
      ) : candidates.length === 0 ? (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">No candidates found matching the current filters.</Typography>
        </Paper>
      ) : (
        <>
          <Stack spacing={1}>
            {clusters.map((cluster) => {
              const busy = actionLoading === cluster.key;
              const isMultiWay = cluster.bookIds.length > 2;
              // Horizontal cramming stops being readable around 4 sides —
              // dividing the card width by 5+ produces columns too narrow
              // to fit a full title. Switch to a stacked vertical layout
              // (one book per row, full-width file paths) for large
              // clusters so every side stays legible.
              const isLargeCluster = cluster.bookIds.length >= 5;
              return (
                <Card key={cluster.key} variant="outlined">
                  <CardContent sx={{ pb: 1 }}>
                    {/* Top info row: layer, similarity, cluster size */}
                    <Stack
                      direction="row"
                      spacing={1}
                      alignItems="center"
                      sx={{ mb: 1 }}
                    >
                      <Chip
                        label={cluster.layer}
                        size="small"
                        color={LAYER_COLORS[cluster.layer] || 'default'}
                      />
                      {cluster.maxSimilarity != null && (
                        <Typography variant="caption" color="text.secondary">
                          {(cluster.maxSimilarity * 100).toFixed(1)}%
                        </Typography>
                      )}
                      {isMultiWay && (
                        <Chip
                          label={`${cluster.bookIds.length}-way cluster`}
                          size="small"
                          color="warning"
                          variant="outlined"
                        />
                      )}
                      <Box sx={{ flex: 1 }} />
                      <MergeIcon color="action" fontSize="small" />
                    </Stack>

                    {/* Book sides — horizontal for small clusters (2-4 sides
                        fit comfortably side-by-side), vertical for large ones
                        so a 19-way cluster is still mergeable. */}
                    <Stack
                      direction={isLargeCluster ? 'column' : 'row'}
                      spacing={isLargeCluster ? 1 : 2}
                      alignItems="stretch"
                      divider={
                        <Divider
                          orientation={isLargeCluster ? 'horizontal' : 'vertical'}
                          flexItem
                        />
                      }
                      sx={isLargeCluster ? undefined : { overflowX: 'auto' }}
                    >
                      {cluster.bookIds.map((bookId) => (
                        <Box
                          key={bookId}
                          sx={
                            isLargeCluster
                              ? { minWidth: 0 }
                              : { flex: 1, minWidth: 0, maxWidth: `${100 / cluster.bookIds.length}%` }
                          }
                        >
                          {renderBookSide(bookId, cluster)}
                        </Box>
                      ))}
                    </Stack>

                    {cluster.llmInfo && (
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ mt: 0.5, display: 'block', fontStyle: 'italic' }}
                      >
                        LLM: {cluster.llmInfo}
                      </Typography>
                    )}
                  </CardContent>
                  <CardActions sx={{ pt: 0 }}>
                    {cluster.hasPending ? (
                      <>
                        <Button
                          size="small"
                          color="primary"
                          startIcon={busy ? <CircularProgress size={14} /> : <MergeIcon />}
                          onClick={() => handleMergeCluster(cluster)}
                          disabled={actionLoading != null}
                        >
                          {isMultiWay ? `Merge ${cluster.bookIds.length} Books` : 'Merge'}
                        </Button>
                        <Button
                          size="small"
                          color="inherit"
                          startIcon={busy ? <CircularProgress size={14} /> : <VisibilityOffIcon />}
                          onClick={() => handleDismissCluster(cluster)}
                          disabled={actionLoading != null}
                        >
                          Dismiss
                        </Button>
                        {cluster.bookIds.length === 2 && (
                          <Tooltip title="Listen to a sample from each version and pick which to keep">
                            <Button
                              size="small"
                              color="secondary"
                              startIcon={<HeadphonesIcon />}
                              onClick={() => handleOpenCompare(cluster)}
                              disabled={actionLoading != null}
                            >
                              Compare
                            </Button>
                          </Tooltip>
                        )}
                        {(splitSelections.get(cluster.key)?.size ?? 0) > 0 && (
                          <Button
                            size="small"
                            color="error"
                            variant="outlined"
                            startIcon={
                              actionLoading === `${cluster.key}:split`
                                ? <CircularProgress size={14} />
                                : <CloseIcon />
                            }
                            onClick={() => handleRemoveSelectedFromCluster(cluster)}
                            disabled={actionLoading != null}
                            sx={{ ml: 'auto' }}
                          >
                            Remove {splitSelections.get(cluster.key)?.size ?? 0} Selected
                          </Button>
                        )}
                      </>
                    ) : (
                      <Chip
                        label={cluster.overallStatus}
                        size="small"
                        color={cluster.overallStatus === 'merged' ? 'success' : 'default'}
                        variant="outlined"
                      />
                    )}
                  </CardActions>
                </Card>
              );
            })}
          </Stack>

          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, p) => setPage(p)}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => { setRowsPerPage(parseInt(e.target.value, 10)); setPage(0); }}
            rowsPerPageOptions={[10, 25, 50, 100, 250, 500, 1000]}
          />
        </>
      )}

      {compareCluster && (
        <AudioSampleCompare
          open
          bookA={compareCluster.a}
          bookB={compareCluster.b}
          onClose={() => setCompareCluster(null)}
          onKeep={(winnerId, loserId) => {
            setCompareCluster(null);
            // Find the cluster and merge with the winner as primary.
            const cluster = allClusters.find(
              (c) => c.bookIds.includes(winnerId) && c.bookIds.includes(loserId)
            );
            if (cluster) handleMergeCluster(cluster, winnerId);
          }}
        />
      )}
    </Box>
  );
}
