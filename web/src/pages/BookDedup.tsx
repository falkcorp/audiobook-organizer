// file: web/src/pages/BookDedup.tsx
// version: 3.30.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-book0dedup02
// last-edited: 2026-06-22

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useSearchParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useAsyncAction } from '../hooks/useAsyncAction';
import {
  Box,
  Typography,
  Paper,
  Button,
  Alert,
  Snackbar,
  Menu,
  MenuItem,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  Tooltip,
  Card,
  CardContent,
  CardActions,
  Stack,
  FormControlLabel,
  Tab,
  Tabs,
  Badge,
  LinearProgress,
  Checkbox,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogContentText,
  DialogActions,
  TextField,
  TablePagination,
  Drawer,
  Switch,
  Link,
  Table,
  TableHead,
  TableRow,
  TableCell,
  TableBody,
  Avatar,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import StarBorderIcon from '@mui/icons-material/StarBorder';
import DownloadIcon from '@mui/icons-material/Download';
import RefreshIcon from '@mui/icons-material/Refresh';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import PersonIcon from '@mui/icons-material/Person';
import ListIcon from '@mui/icons-material/List';
import CloseIcon from '@mui/icons-material/Close';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import * as api from '../services/api';
import type { Book, DedupCandidate, DedupStats, Operation } from '../services/api';
import { useOperationsStore } from '../stores/useOperationsStore';
import { FilterTagBar, type FilterTag } from '../components/common/FilterTagBar';
import HeadphonesIcon from '@mui/icons-material/Headphones';
import { AudioSampleCompare } from '../components/AudioSampleCompare';
import type { SampleBook } from '../components/AudioSampleCompare';
import ClearIcon from '@mui/icons-material/Clear';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import AutoAwesomeIcon from '@mui/icons-material/AutoAwesome';
import { DedupBookTab } from '../components/dedup/DedupBookTab';
import BuildIcon from '@mui/icons-material/Build';
import FingerprintIcon from '@mui/icons-material/Fingerprint';
import GraphicEqIcon from '@mui/icons-material/GraphicEq';
import { BookDedupScanTab } from '../components/dedup/DedupAdvancedScanTab';
import { AuthorDedupTab, RoleDetails } from '../components/dedup/DedupAuthorTab';
import { SeriesDedupTab } from '../components/dedup/DedupSeriesTab';
import { ReconcileTab } from '../components/dedup/DedupReconcileTab';
import { DedupSplitBookTab } from '../components/dedup/DedupSplitBookTab';
import CallSplitIcon from '@mui/icons-material/CallSplit';
import { cleanDisplayTitle } from '../components/dedup/dedupHelpers';
import { CoverLightbox } from '../components/CoverLightbox';
import { UnifiedDedupTab } from '../components/dedup/UnifiedDedupTab';
import ViewListIcon from '@mui/icons-material/ViewList';

// LSH backfill completed 2026-06-11 (275K files indexed, 15K dedup pairs scored).
// Feature gate removed — unified dedup tab is now always enabled.
function isUnifiedDedupEnabled(): boolean {
  return true;
}

// ULID pattern: 26-character alphanumeric (0-9, A-Z only)
const ULID_PATTERN = /^[0-9A-Z]{26}$/;

// ---- Book Dedup Tab ----
// Moved to web/src/components/dedup/DedupBookTab.tsx

// BookDedupScanTab extracted to web/src/components/dedup/DedupAdvancedScanTab.tsx

// normalizeGroups and AuthorDedupTab extracted to web/src/components/dedup/DedupAuthorTab.tsx

// SeriesDedupTab extracted to web/src/components/dedup/DedupSeriesTab.tsx

// ---- AI Author Sub-Page (self-contained per mode) ----
// ---- AI Author Pipeline Page (unified scan-based view) ----
function AIAuthorPipelinePage() {
  const [scan, setScan] = useState<api.AIScanDetail | null>(null);
  const [results, setResults] = useState<api.AIScanResult[]>([]);
  const [scans, setScans] = useState<api.AIScan[]>([]);
  const [batchMode, setBatchMode] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [agreementFilter, setAgreementFilter] = useState<string>('all');
  const [error, setError] = useState<string | null>(null);

  const { loading, run: startScanAction } = useAsyncAction(async () => {
    setError(null);
    const newScan = await api.startAIScan(batchMode ? 'batch' : 'realtime');
    const detail = await api.getAIScan(newScan.id);
    setScan(detail);
    // Refresh scan list
    api.listAIScans().then(setScans).catch(() => {});
  });

  const startScan = async () => {
    await startScanAction();
  };

  // Load scan list on mount
  useEffect(() => {
    api.listAIScans().then(setScans).catch(() => {});
  }, []);

  // Poll active scan status
  useEffect(() => {
    if (!scan || scan.status === 'complete' || scan.status === 'failed') return;
    let mounted = true;
    const interval = setInterval(async () => {
      try {
        const updated = await api.getAIScan(scan.id);
        if (mounted) {
          setScan(updated);
          if (updated.status === 'complete') {
            const res = await api.getAIScanResults(scan.id);
            if (mounted) setResults(res);
            clearInterval(interval);
          }
        }
      } catch { /* ignore polling errors */ }
    }, 5000);
    return () => {
      mounted = false;
      clearInterval(interval);
    };
    // scan?.id and scan?.status are the meaningful change signals; including
    // the full `scan` object would restart the interval on every poll update.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scan?.id, scan?.status]);

  const { run: loadScanAction } = useAsyncAction(async (...args: unknown[]) => {
    const scanId = args[0] as number;
    const detail = await api.getAIScan(scanId);
    setScan(detail);
    if (detail.status === 'complete') {
      const res = await api.getAIScanResults(scanId);
      setResults(res);
    }
  });

  const loadScan = async (scanId: number) => {
    await loadScanAction(scanId);
  };

  const applySelected = async () => {
    if (!scan || selected.size === 0) return;
    try {
      await api.applyAIScanResults(scan.id, Array.from(selected));
      const res = await api.getAIScanResults(scan.id);
      setResults(res);
      setSelected(new Set());
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to apply results');
    }
  };

  const filteredResults = agreementFilter === 'all'
    ? results
    : results.filter(r => r.agreement === agreementFilter);

  const toggleSelect = (id: number) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return (
    <Box>
      {/* Header Bar */}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, p: 2 }}>
        <Button
          variant="contained"
          onClick={startScan}
          disabled={loading || (scan != null && scan.status !== 'complete' && scan.status !== 'failed')}
          startIcon={<AutoAwesomeIcon />}
        >
          Run Scan
        </Button>
        <FormControlLabel
          control={<Switch checked={batchMode} onChange={(_, v) => setBatchMode(v)} />}
          label={batchMode ? 'Batch (cheaper, hours)' : 'Realtime (faster, more expensive)'}
        />
        <Box sx={{ flex: 1 }} />
        <Button variant="outlined" onClick={() => setHistoryOpen(true)}>
          Scan History
        </Button>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mx: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Active Scan Status */}
      {scan && scan.status !== 'complete' && scan.status !== 'failed' && scan.status !== 'canceled' && (
        <Paper
          elevation={3}
          sx={{
            position: 'sticky',
            top: 0,
            zIndex: 10,
            mx: 2,
            mb: 2,
            p: 2,
            borderRadius: 2,
          }}
        >
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
            <Typography variant="subtitle2">Scan #{scan.id} — {scan.status}</Typography>
            <Box sx={{ display: 'flex', gap: 1 }}>
              {(scan.phases || []).map(phase => (
                <Chip
                  key={phase.phase_type}
                  label={`${phase.phase_type.replace('_', ' ')}: ${phase.status}`}
                  color={phase.status === 'complete' ? 'success' : phase.status === 'failed' ? 'error' : 'default'}
                  size="small"
                />
              ))}
            </Box>
            <Box sx={{ flex: 1 }} />
            <Button
              variant="outlined"
              color="error"
              size="small"
              onClick={async () => {
                try {
                  await api.cancelAIScan(scan.id);
                  const updated = await api.getAIScan(scan.id);
                  setScan(updated);
                } catch (e: unknown) {
                  setError(e instanceof Error ? e.message : 'Failed to cancel scan');
                }
              }}
            >
              Cancel Scan
            </Button>
          </Box>
          <LinearProgress sx={{ mt: 1 }} />
        </Paper>
      )}

      {/* Canceled scan message */}
      {scan && scan.status === 'canceled' && (
        <Alert severity="warning" sx={{ mx: 2, mb: 2 }}>
          Scan #{scan.id} was canceled.
        </Alert>
      )}

      {/* No scan loaded */}
      {!scan && !loading && (
        <Paper sx={{ p: 4, mx: 2, textAlign: 'center' }}>
          <Typography variant="body1" color="text.secondary">
            Run a scan to discover author duplicates using multi-pass AI analysis, or load a previous scan from history.
          </Typography>
        </Paper>
      )}

      {loading && !scan && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
      )}

      {/* Scan failed */}
      {scan?.status === 'failed' && (
        <Alert severity="error" sx={{ mx: 2 }}>
          Scan #{scan.id} failed. Try running a new scan.
        </Alert>
      )}

      {/* Results */}
      {scan?.status === 'complete' && results.length > 0 && (
        <Box sx={{ px: 2 }}>
          {/* Filter Tabs */}
          <Tabs value={agreementFilter} onChange={(_, v) => setAgreementFilter(v)} sx={{ mb: 2 }}>
            <Tab value="all" label={`All (${results.length})`} />
            <Tab value="agreed" label={`Agreed (${results.filter(r => r.agreement === 'agreed').length})`} />
            <Tab value="groups_only" label={`Groups Only (${results.filter(r => r.agreement === 'groups_only').length})`} />
            <Tab value="full_only" label={`Full Only (${results.filter(r => r.agreement === 'full_only').length})`} />
            <Tab value="disagreed" label={`Disagreed (${results.filter(r => r.agreement === 'disagreed').length})`} />
          </Tabs>

          {/* Floating Apply Bar */}
          {selected.size > 0 && (
            <Paper
              elevation={4}
              sx={{
                position: 'sticky',
                bottom: 16,
                zIndex: 10,
                p: 1.5,
                mx: -2,
                mb: 2,
                display: 'flex',
                alignItems: 'center',
                gap: 2,
                borderRadius: 2,
                bgcolor: 'background.paper',
              }}
            >
              <Button variant="contained" color="primary" onClick={applySelected}>
                Apply Selected ({selected.size})
              </Button>
              <Button variant="outlined" size="small" onClick={() => setSelected(new Set())}>
                Clear Selection
              </Button>
              <Typography variant="body2" color="text.secondary" sx={{ ml: 'auto' }}>
                {selected.size} of {filteredResults.filter(r => !r.applied).length} selected
              </Typography>
            </Paper>
          )}

          {/* Result Cards */}
          {filteredResults.map(result => (
            <Card key={result.id} sx={{ mb: 1, opacity: result.applied ? 0.5 : 1 }} variant="outlined">
              <CardContent sx={{ py: 1, '&:last-child': { pb: 1 } }}>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <Checkbox
                    checked={selected.has(result.id)}
                    onChange={() => toggleSelect(result.id)}
                    disabled={result.applied}
                    size="small"
                  />
                  <Chip
                    label={result.agreement}
                    size="small"
                    color={result.agreement === 'agreed' ? 'success' : result.agreement === 'disagreed' ? 'error' : 'default'}
                  />
                  <Chip label={result.suggestion.action} size="small" variant="outlined"
                    color={result.suggestion.action === 'merge' ? 'primary' : result.suggestion.action === 'rename' ? 'warning' : result.suggestion.action === 'alias' ? 'info' : 'default'} />
                  <Chip label={result.suggestion.confidence} size="small" variant="outlined"
                    color={result.suggestion.confidence === 'high' ? 'success' : result.suggestion.confidence === 'medium' ? 'warning' : 'error'} />
                  <Box sx={{ flex: 1 }}>
                    <Typography variant="body2" fontWeight="bold">
                      {result.suggestion.canonical_name}
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                      {result.suggestion.reason}
                    </Typography>
                  </Box>
                  {result.applied && <Chip label="Applied" size="small" color="info" />}
                </Box>
                {result.suggestion.roles && (
                  <>
                    <Divider sx={{ my: 0.5, ml: 5 }} />
                    <RoleDetails roles={result.suggestion.roles} />
                  </>
                )}
              </CardContent>
            </Card>
          ))}

          {/* No results for this filter */}
          {filteredResults.length === 0 && (
            <Typography color="text.secondary" sx={{ p: 2, textAlign: 'center' }}>
              No results matching this filter.
            </Typography>
          )}
        </Box>
      )}

      {/* Scan complete but no results */}
      {scan?.status === 'complete' && results.length === 0 && (
        <Paper sx={{ p: 4, mx: 2, textAlign: 'center' }}>
          <Typography variant="body1" color="text.secondary">
            Scan complete — no duplicate authors found.
          </Typography>
        </Paper>
      )}

      {/* Scan History Drawer */}
      <Drawer anchor="right" open={historyOpen} onClose={() => setHistoryOpen(false)}>
        <Box sx={{ width: 400, p: 2 }}>
          <Typography variant="h6" gutterBottom>Scan History</Typography>
          {scans.map(s => (
            <Card
              key={s.id}
              sx={{ mb: 1, cursor: 'pointer', border: scan?.id === s.id ? 2 : undefined, borderColor: scan?.id === s.id ? 'primary.main' : undefined }}
              variant="outlined"
              onClick={() => { loadScan(s.id); setHistoryOpen(false); }}
            >
              <CardContent sx={{ py: 1, '&:last-child': { pb: 1 } }}>
                <Typography variant="body2" fontWeight="bold">
                  Scan #{s.id} — {s.status}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {new Date(s.created_at).toLocaleString()} · {s.author_count} authors · {s.mode}
                </Typography>
              </CardContent>
            </Card>
          ))}
          {scans.length === 0 && (
            <Typography color="text.secondary">No scan history yet.</Typography>
          )}
        </Box>
      </Drawer>
    </Box>
  );
}

// ---- AI Review Top-Level Tab ----
function AIReviewTab() {
  const [searchParams, setSearchParams] = useSearchParams();
  const aiSub = searchParams.get('aisub') || 'authors';
  const setAiSub = (v: string) => {
    const next = new URLSearchParams(searchParams);
    next.set('aisub', v);
    setSearchParams(next, { replace: true });
  };

  return (
    <Box>
      <Tabs value={aiSub} onChange={(_, v) => setAiSub(v)} sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}>
        <Tab value="authors" label="Authors" icon={<PersonIcon />} iconPosition="start" />
        <Tab value="books" label="Books" icon={<MenuBookIcon />} iconPosition="start" />
      </Tabs>

      {aiSub === 'authors' && <AIAuthorPipelinePage />}
      {aiSub === 'books' && (
        <Box sx={{ p: 4, textAlign: 'center' }}>
          <Typography color="text.secondary">Book deduplication coming soon.</Typography>
        </Box>
      )}
    </Box>
  );
}

// ReconcileTab extracted to web/src/components/dedup/DedupReconcileTab.tsx

// ---- Embedding Dedup Tab ----

/** Cached book details for candidate display */
const bookCache = new Map<string, Book>();
/** Cached book file lists. Files are fetched in parallel with book details so
 * hovering a file path can show every segment without waiting on a network
 * round trip. An empty array means "we tried and got none", `undefined`
 * means "not fetched yet". */
const bookFilesCache = new Map<string, string[]>();

async function fetchBookCached(id: string): Promise<Book | null> {
  if (bookCache.has(id)) return bookCache.get(id)!;
  try {
    const book = await api.getBook(id);
    bookCache.set(id, book);
    return book;
  } catch {
    return null;
  }
}

async function fetchBookFilesCached(id: string): Promise<string[]> {
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

function EmbeddingDedupTab() {
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

// ---- Acoustic Compare Panel ----
// Manual two-book fingerprint comparison tool.
function formatDuration(seconds: number): string {
  if (!seconds) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function bookCoverSrc(book: Book): string {
  if (!book.cover_url) return '';
  return book.cover_url.startsWith('/api/')
    ? book.cover_url
    : `/api/v1/covers/proxy?url=${encodeURIComponent(book.cover_url)}`;
}

// Helper function to render book metadata (reusable in AcousticComparePanel)
export function AcousticBookMetadata({ book, filePath }: { book: Book; filePath?: string }) {
  const navigate = useNavigate();
  return (
    <Box sx={{ minWidth: 0 }}>
      <Typography
        variant="body2"
        fontWeight={600}
        sx={{ cursor: 'pointer', '&:hover': { textDecoration: 'underline' } }}
        onClick={() => navigate(`/library/${book.id}`)}
        noWrap
      >
        {book.title || <em style={{ opacity: 0.5 }}>Untitled</em>}
      </Typography>
      {book.author_name && (
        <Typography variant="caption" color="text.secondary" noWrap>
          {book.author_name}
        </Typography>
      )}
      {book.series_name && (
        <Typography variant="caption" color="text.secondary" noWrap display="block">
          {book.series_name}{book.series_position ? ` · Book ${book.series_position}` : ''}
        </Typography>
      )}
      <Stack direction="row" spacing={0.5} sx={{ mt: 0.5 }} flexWrap="wrap" useFlexGap>
        {book.format && <Chip label={book.format.toUpperCase()} size="small" />}
        {book.duration && <Chip label={formatDuration(book.duration)} size="small" variant="outlined" />}
      </Stack>
      {filePath && (
        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ display: 'block', mt: 0.5, wordBreak: 'break-all', fontSize: '0.65rem', fontFamily: 'monospace' }}
        >
          {filePath}
        </Typography>
      )}
    </Box>
  );
}

// Legacy function for backward compatibility (if used elsewhere)
export function AcousticBookCard({ book, label }: { book: Book; label: string }) {
  return (
    <Box sx={{ flex: 1, minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
        {label}
      </Typography>
      <Stack direction="row" spacing={1.5} sx={{ mt: 0.5 }} alignItems="flex-start">
        <Avatar
          src={bookCoverSrc(book)}
          variant="rounded"
          sx={{ width: 56, height: 72, flexShrink: 0, bgcolor: 'action.selected' }}
        >
          <GraphicEqIcon />
        </Avatar>
        <AcousticBookMetadata book={book} filePath={book.file_path} />
      </Stack>
    </Box>
  );
}

interface AcousticComparePanelProps {
  initialA?: string;
  initialB?: string;
}

function AcousticComparePanel({ initialA = '', initialB = '' }: AcousticComparePanelProps) {
  const [bookAID, setBookAID] = useState(initialA);
  const [bookBID, setBookBID] = useState(initialB);
  const [result, setResult] = useState<api.AcoustIDCompareResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);
  const [idAError, setIdAError] = useState<string | null>(null);
  const [idBError, setIdBError] = useState<string | null>(null);

  const handleOpenCoverLightbox = (src: string | null) => {
    setLightboxSrc(src);
    setLightboxOpen(true);
  };

  const handleCloseLightbox = () => {
    setLightboxOpen(false);
    setLightboxSrc(null);
  };

  useEffect(() => {
    if (initialA) setBookAID(initialA);
    if (initialB) setBookBID(initialB);
  }, [initialA, initialB]);

  // Validate ULID format
  const validateBookID = (id: string): string | null => {
    const trimmed = id.trim();
    if (!trimmed) return 'Book ID is required';
    if (!ULID_PATTERN.test(trimmed)) {
      return 'Invalid book ID format. Must be 26-character alphanumeric (0-9, A-Z only).';
    }
    return null;
  };

  const handleCompare = async () => {
    // Validate both IDs
    const aError = validateBookID(bookAID);
    const bError = validateBookID(bookBID);

    setIdAError(aError);
    setIdBError(bError);

    if (aError || bError) return;

    setLoading(true);
    setError(null);
    setResult(null);
    try {
      const resp = await api.compareAcoustID(bookAID.trim(), bookBID.trim());
      setResult(resp);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Comparison failed');
    } finally {
      setLoading(false);
    }
  };

  const segLabels: Record<string, string> = {
    seg0: 'Intro', seg1: 'Body 1', seg2: 'Body 2',
    seg3: 'Body 3', seg4: 'Body 4', seg5: 'Body 5', seg6: 'Outro',
  };

  const hasAnySegments = result
    ? result.segment_scores.some((s) => s.hash_a || s.hash_b)
    : false;

  const handleBookAIDChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setBookAID(e.target.value);
    setIdAError(null);
  };

  const handleBookBIDChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setBookBID(e.target.value);
    setIdBError(null);
  };

  return (
    <Paper sx={{ p: 2 }}>
      <Typography variant="subtitle1" sx={{ mb: 1.5, fontWeight: 600 }}>Fingerprint Comparison</Typography>
      <Stack direction="row" spacing={2} sx={{ mb: 2 }} alignItems="flex-start">
        <TextField
          label="Book A ID"
          size="small"
          value={bookAID}
          onChange={handleBookAIDChange}
          error={idAError !== null}
          helperText={idAError}
          sx={{ flex: 1 }}
          placeholder="Paste book ID…"
        />
        <TextField
          label="Book B ID"
          size="small"
          value={bookBID}
          onChange={handleBookBIDChange}
          error={idBError !== null}
          helperText={idBError}
          sx={{ flex: 1 }}
          placeholder="Paste book ID…"
        />
        <Button variant="contained" onClick={handleCompare} disabled={loading || !bookAID.trim() || !bookBID.trim()} sx={{ mt: 0.5 }}>
          {loading ? 'Comparing…' : 'Compare'}
        </Button>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 1 }}>{error}</Alert>}

      {result && (
        <Box>
          {/* Cover images and metadata side by side */}
          <Stack direction="row" spacing={3} sx={{ mb: 3 }}>
            {/* Book A */}
            <Box sx={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
                Book A
              </Typography>
              {/* Cover image (clickable) */}
              <Box
                onClick={() => handleOpenCoverLightbox(bookCoverSrc(result.book_a as Book))}
                sx={{
                  width: 180,
                  height: 240,
                  borderRadius: 1,
                  overflow: 'hidden',
                  cursor: result.book_a?.cover_url ? 'pointer' : 'default',
                  bgcolor: 'action.disabledBackground',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  '&:hover': result.book_a?.cover_url ? { opacity: 0.8, boxShadow: 3 } : {},
                  transition: 'all 0.2s',
                }}
              >
                {result.book_a?.cover_url ? (
                  <img
                    src={bookCoverSrc(result.book_a as Book)}
                    alt={result.book_a?.title}
                    style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  />
                ) : (
                  <GraphicEqIcon sx={{ fontSize: 60, opacity: 0.3 }} />
                )}
              </Box>
              {/* Metadata */}
              <AcousticBookMetadata book={result.book_a as Book} filePath={(result.book_a as any)?.file_path} />
            </Box>

            <Divider orientation="vertical" flexItem />

            {/* Book B (same structure as Book A) */}
            <Box sx={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
                Book B
              </Typography>
              <Box
                onClick={() => handleOpenCoverLightbox(bookCoverSrc(result.book_b as Book))}
                sx={{
                  width: 180,
                  height: 240,
                  borderRadius: 1,
                  overflow: 'hidden',
                  cursor: result.book_b?.cover_url ? 'pointer' : 'default',
                  bgcolor: 'action.disabledBackground',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  '&:hover': result.book_b?.cover_url ? { opacity: 0.8, boxShadow: 3 } : {},
                  transition: 'all 0.2s',
                }}
              >
                {result.book_b?.cover_url ? (
                  <img
                    src={bookCoverSrc(result.book_b as Book)}
                    alt={result.book_b?.title}
                    style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  />
                ) : (
                  <GraphicEqIcon sx={{ fontSize: 60, opacity: 0.3 }} />
                )}
              </Box>
              <AcousticBookMetadata book={result.book_b as Book} filePath={(result.book_b as any)?.file_path} />
            </Box>
          </Stack>

          {/* Lightbox modal */}
          <CoverLightbox open={lightboxOpen} src={lightboxSrc} onClose={handleCloseLightbox} />

          {/* Similarity score */}
          <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 2 }}>
            <Chip
              label={hasAnySegments ? `${Math.round(result.overall_score * 100)}% match` : 'No fingerprint data'}
              color={
                !hasAnySegments ? 'default'
                  : result.overall_score >= 0.85 ? 'error'
                  : result.overall_score >= 0.6 ? 'warning'
                  : 'default'
              }
              icon={<GraphicEqIcon />}
            />
            {!hasAnySegments && (
              <Typography variant="caption" color="text.secondary">
                Run "Fingerprint Books" first to populate segment data
              </Typography>
            )}
          </Stack>

          {/* Segment table */}
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Segment</TableCell>
                <TableCell>Book A fingerprint</TableCell>
                <TableCell>Book B fingerprint</TableCell>
                <TableCell align="center">Match</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {result.segment_scores.map((seg) => (
                <TableRow
                  key={seg.segment}
                  sx={{
                    bgcolor: seg.match
                      ? 'success.light'
                      : seg.hash_a && seg.hash_b
                      ? 'error.light'
                      : undefined,
                    opacity: 0.9,
                  }}
                >
                  <TableCell><strong>{segLabels[seg.segment] ?? seg.segment}</strong></TableCell>
                  <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                    {seg.hash_a ? seg.hash_a.slice(0, 16) + '…' : <em style={{ opacity: 0.4 }}>not fingerprinted</em>}
                  </TableCell>
                  <TableCell sx={{ fontFamily: 'monospace', fontSize: '0.7rem' }}>
                    {seg.hash_b ? seg.hash_b.slice(0, 16) + '…' : <em style={{ opacity: 0.4 }}>not fingerprinted</em>}
                  </TableCell>
                  <TableCell align="center">
                    {!seg.hash_a || !seg.hash_b ? (
                      <Chip label="n/a" size="small" variant="outlined" />
                    ) : seg.match ? (
                      <Chip label="✓ match" size="small" color="success" />
                    ) : (
                      <Chip label="✗ differ" size="small" color="error" />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Box>
      )}
    </Paper>
  );
}

// metadataQuality scores a Book's metadata completeness (0–10).
// Higher = more complete / reliable source.
function metadataQuality(book: Book | undefined): number {
  if (!book) return 0;
  let score = 0;
  const title = book.title ?? '';
  // Title sanity: not empty, not literal "TITLE", not looks like a ULID/UUID
  const isGarbageTitle =
    !title ||
    title.toUpperCase() === 'TITLE' ||
    /^[0-9A-Z]{26}$/.test(title.trim());
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
  if (score >= 6) return <Chip label="Rich metadata" size="small" color="success" variant="outlined" />;
  if (score >= 3) return <Chip label="Partial metadata" size="small" color="warning" variant="outlined" />;
  return <Chip label="Poor metadata" size="small" color="error" variant="outlined" />;
}

// ---- Acoustic Dedup Tab ----
function AcousticDedupTab() {
  const [candidates, setCandidates] = useState<DedupCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [fingerprinting, setFingerprinting] = useState(false);
  const [statusMsg, setStatusMsg] = useState<string | null>(null);
  const [statusSeverity, setStatusSeverity] = useState<'info' | 'error'>('info');
  const [page, setPage] = useState(0);
  // Bigger default than 25 and exposes 50/100/250 because 12K candidates at
  // 25/page is 512 clicks — the user understandably refuses to triage that
  // way. Multiselect bulk Keep-A / Keep-B / Dismiss is a follow-up.
  const [rowsPerPage, setRowsPerPage] = useState(100);
  const [bookCache, setBookCache] = useState<Map<string, Book>>(new Map());
  const [selectedCandIds, setSelectedCandIds] = useState<Set<number>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [purging, setPurging] = useState(false);
  const [resolving, setResolving] = useState<Set<number>>(new Set());
  const [compareA, setCompareA] = useState('');
  const [compareB, setCompareB] = useState('');
  const comparePanelRef = useCallback((el: HTMLDivElement | null) => {
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, []);
  const [showComparePanel, setShowComparePanel] = useState(false);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const loadCandidates = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await api.getDedupCandidates({
        layer: 'acoustid',
        limit: rowsPerPage,
        offset: page * rowsPerPage,
      });
      const cands = resp.candidates || [];
      setCandidates(cands);
      setTotal(resp.total || 0);

      const ids = new Set<string>();
      for (const c of cands) { ids.add(c.entity_a_id); ids.add(c.entity_b_id); }
      const cache = new Map<string, Book>();
      await Promise.all(Array.from(ids).map(async (id) => {
        try {
          const book = await fetchBookCached(id);
          if (book) cache.set(id, book);
        } catch { /* ignore */ }
      }));
      setBookCache(cache);
    } catch {
      // handled by empty state
    } finally {
      setLoading(false);
    }
  }, [page, rowsPerPage]);

  useEffect(() => { loadCandidates(); }, [loadCandidates]);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleFingerprint = async () => {
    setFingerprinting(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerFingerprintBackfill('missing');
      setStatusMsg(`Fingerprinting queued — see bell icon for progress (op ${op.id.slice(-6)})`);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Fingerprint job failed to start');
    } finally {
      setFingerprinting(false);
    }
  };

  const handleScan = async () => {
    setScanning(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerDedupAcoustID();
      setStatusMsg(`Duplicate scan queued — see bell icon for progress (op ${op.id.slice(-6)})`);
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          loadCandidates();
        }
      }, 5000);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Scan failed to start');
    } finally {
      setScanning(false);
    }
  };

  const handleMerge = async (candidateId: number, keepId?: string) => {
    // The sync /dedup/candidates/:id/merge endpoint performs the merge,
    // updates candidate status, publishes the event, and cleans up orphan
    // candidates (PR #1167). Previously we also fired /audiobooks/merge
    // (async) here, which caused a race + UI flicker + spurious 409 from
    // the sync call when the async one won. (B1)
    //
    // keepId, when provided, tells the backend which side of the pair to
    // keep as the merge primary. Without it the backend auto-selects by
    // format/bitrate/size — which historically ignored the user's
    // Keep A / Keep B click.
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.mergeDedupCandidate(candidateId, keepId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Merge failed');
    } finally {
      setResolving((s) => { const next = new Set(s); next.delete(candidateId); return next; });
    }
  };

  const handleDismiss = async (candidateId: number) => {
    setResolving((s) => new Set(s).add(candidateId));
    try {
      await api.dismissDedupCandidate(candidateId);
      setCandidates((prev) => prev.filter((c) => c.id !== candidateId));
    } catch (err) {
      setStatusSeverity('error');
      setStatusMsg(err instanceof Error ? err.message : 'Dismiss failed');
    } finally {
      setResolving((s) => { const next = new Set(s); next.delete(candidateId); return next; });
    }
  };

  // One-shot cleanup of pending candidates that are no longer real duplicates:
  // chapter files of one multi-file book (same parent directory), books in the
  // same version group, distinct numbered series volumes. Runs server-side via
  // Engine.PurgeStaleCandidates; no rescan required.
  const handlePurgeStale = async () => {
    setPurging(true);
    setStatusMsg(null);
    try {
      const { op_id } = await api.purgeStaleCandidates();
      setStatusMsg(
        op_id
          ? `Cleanup queued — see bell for progress (op ${op_id.slice(-6)}).`
          : 'Cleanup queued — see bell for progress.',
      );
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) loadCandidates();
      }, 3000);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Cleanup failed');
    } finally {
      setPurging(false);
    }
  };

  // Nuke every stored AcoustID fingerprint + drop acoustid candidates + force
  // a full rescan. Use when prior fingerprints are suspected bad (e.g. the
  // "AQAAAA" sentinel pollution that made every book a 100% match against
  // one anchor). Heavy: 5–10 minute clear, then a multi-hour rescan.
  // Online AcoustID lookup — sends every fingerprint to acoustid.org's
  // /v2/lookup and stores the top MusicBrainz recording match. Rate-
  // limited (~3 req/sec) so this takes hours on a large library.
  // Audiobook hit rate is modest (5–15%); the value is the "free wins"
  // when a chapter happens to be in MusicBrainz.
  const [onlineLookingUp, setOnlineLookingUp] = useState(false);
  const handleAcoustIDOnline = async () => {
    setOnlineLookingUp(true);
    setStatusMsg(null);
    try {
      const op = await api.triggerAcoustIDOnlineLookup();
      setStatusMsg(`AcoustID.org lookup queued — see bell (op ${op.id.slice(-6)}).`);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'AcoustID online lookup failed to start');
    } finally {
      setOnlineLookingUp(false);
    }
  };

  // AcoustID API key form. Loads the masked value from /api/v1/config so
  // the user can see "•••• …xyz" without re-entering it, then PUTs the
  // new value when they save. The server stores it in the settings DB
  // and the lookup-online op reads from config.AppConfig.AcoustIDAPIKey
  // before falling back to the env var.
  const [acoustidKey, setAcoustidKey] = useState('');
  const [acoustidKeyMask, setAcoustidKeyMask] = useState('');
  const [acoustidKeySaving, setAcoustidKeySaving] = useState(false);
  useEffect(() => {
    let cancelled = false;
    api.getConfig().then((cfg) => {
      if (!cancelled) setAcoustidKeyMask(cfg.acoustid_api_key || '');
    }).catch(() => { /* leave blank */ });
    return () => { cancelled = true; };
  }, []);
  const handleSaveAcoustIDKey = async () => {
    if (!acoustidKey.trim()) return;
    setAcoustidKeySaving(true);
    setStatusMsg(null);
    try {
      const cfg = await api.updateConfig({ acoustid_api_key: acoustidKey.trim() });
      setAcoustidKeyMask(cfg.acoustid_api_key || '');
      setAcoustidKey('');
      setStatusMsg('AcoustID API key saved.');
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Failed to save AcoustID API key');
    } finally {
      setAcoustidKeySaving(false);
    }
  };

  const [resetting, setResetting] = useState(false);
  const handleResetAcoustID = async () => {
    if (!window.confirm(
      'This clears EVERY stored AcoustID fingerprint and re-enqueues a full library rescan (multi-hour). Continue?',
    )) return;
    setResetting(true);
    setStatusMsg(null);
    try {
      const { reset_op_id, rescan_op_id } = await api.resetAcoustIDFingerprints();
      setStatusMsg(
        `Reset queued (op ${reset_op_id.slice(-6)}); rescan will follow (op ${rescan_op_id.slice(-6) || 'pending'}). Watch the bell.`,
      );
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : 'Reset failed');
    } finally {
      setResetting(false);
    }
  };

  // Bulk dismiss N candidates in parallel (capped concurrency to be polite to
  // the backend). Refreshes the list once at the end instead of per-call so
  // the UI doesn't thrash. Selecting nothing is a no-op.
  const bulkApply = async (
    action: 'dismiss' | 'keep-a' | 'keep-b',
  ) => {
    if (selectedCandIds.size === 0) return;
    setBulkBusy(true);
    const ids = Array.from(selectedCandIds);
    const failed: number[] = [];
    const CONCURRENCY = 5;
    for (let i = 0; i < ids.length; i += CONCURRENCY) {
      const batch = ids.slice(i, i + CONCURRENCY);
      await Promise.all(batch.map(async (id) => {
        const c = candidates.find((x) => x.id === id);
        if (!c) return;
        try {
          if (action === 'dismiss') {
            await api.dismissDedupCandidate(id);
          } else if (action === 'keep-a') {
            await api.mergeDedupCandidate(id, c.entity_a_id);
          } else {
            await api.mergeDedupCandidate(id, c.entity_b_id);
          }
        } catch {
          failed.push(id);
        }
      }));
    }
    setSelectedCandIds(new Set(failed));
    setBulkBusy(false);
    setStatusMsg(failed.length === 0
      ? `Bulk ${action}: ${ids.length} candidate(s) processed`
      : `Bulk ${action}: ${ids.length - failed.length} ok, ${failed.length} failed`);
    await loadCandidates();
  };

  const simPct = (c: DedupCandidate) =>
    c.similarity != null ? `${Math.round(c.similarity * 100)}%` : '—';

  const bookTitle = (id: string) => {
    const b = bookCache.get(id);
    if (!b) return <em style={{ opacity: 0.5 }}>{id.slice(-8)}</em>;
    const title = b.title;
    const isGarbage = !title || title.toUpperCase() === 'TITLE' || /^[0-9A-Z]{26}$/.test(title.trim());
    if (isGarbage) return <em style={{ color: 'orange' }}>{title || '(no title)'}</em>;
    return title;
  };

  // Renders the title + file path for a candidate cell. Title opens the book
  // detail page in a new tab so reviewers don't lose their position in the
  // dedup list. File path lives directly under the title so reviewers can
  // disambiguate when titles are missing, identical, or wrong — the case the
  // user has been screaming about. If the book row 404s out of the backend
  // (merged/deleted/orphaned candidate) the cell shows a clear "(missing)"
  // marker and a Dismiss-orphan action is implied via the row's Dismiss
  // button.
  const renderBookCell = (id: string) => {
    const b = bookCache.get(id);
    const missing = !b;
    const path = b?.file_path ?? '';
    return (
      <Stack spacing={0.25} sx={{ minWidth: 0 }}>
        {missing ? (
          <Typography variant="body2" sx={{ color: 'error.main', fontStyle: 'italic' }}>
            (missing book — {id.slice(-8)})
          </Typography>
        ) : (
          // SPA navigation via react-router Link (NOT target="_blank"). The
          // new-tab version forced a full bundle reload, which kicked the
          // SSE connection — causing "Client unregistered" + HTTP/3 TLS
          // handshake EOF noise every click. In-app nav is instant and
          // preserves the SSE. Ctrl/Cmd-click still opens in a new tab if
          // the user wants to keep their place in the candidate list.
          <Link
            component={RouterLink}
            to={`/library/${id}`}
            underline="hover"
            sx={{
              color: 'primary.main',
              fontWeight: 500,
              fontSize: '0.95rem',
              textTransform: 'none',
              textAlign: 'left',
              display: 'block',
              whiteSpace: 'normal',
              wordBreak: 'break-word',
            }}
            onClick={(e) => e.stopPropagation()}
          >
            {bookTitle(id)}
          </Link>
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
      </Stack>
    );
  };

  return (
    <Box>
      <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 1 }} flexWrap="wrap" useFlexGap>
        <Typography variant="h6">Acoustic Duplicates</Typography>

        <Tooltip title="Read every audio file and compute 7-segment chromaprint fingerprints. Required before duplicate scanning. Runs overnight; safe to trigger manually for new files.">
          <Button variant="outlined" startIcon={<FingerprintIcon />} onClick={handleFingerprint} disabled={fingerprinting}>
            {fingerprinting ? 'Queuing…' : 'Fingerprint Books'}
          </Button>
        </Tooltip>

        <Tooltip title="Compare already-stored fingerprints across all books to find audio-level duplicate pairs. Fast — no file I/O.">
          <Button variant="outlined" startIcon={<GraphicEqIcon />} onClick={handleScan} disabled={scanning}>
            {scanning ? 'Queuing…' : 'Find Acoustic Duplicates'}
          </Button>
        </Tooltip>

        <Tooltip title="Delete pending candidates that are no longer valid duplicates: chapter files of one multi-file book, same-version-group books, distinct series volumes. Fast — no rescan.">
          <Button variant="outlined" color="warning" onClick={handlePurgeStale} disabled={purging}>
            {purging ? 'Cleaning…' : 'Cleanup Stale (same-folder, etc)'}
          </Button>
        </Tooltip>

        <Tooltip title="Nuke every stored AcoustID fingerprint and force a full rescan. Use when stored fingerprints are suspected bad (e.g. every book matching one anchor at 100%). Multi-hour.">
          <Button variant="outlined" color="error" onClick={handleResetAcoustID} disabled={resetting}>
            {resetting ? 'Queuing…' : 'Reset & Rescan All AcoustID'}
          </Button>
        </Tooltip>

        <Tooltip title="Send every file's whole-file chromaprint to acoustid.org's /v2/lookup and store the top MusicBrainz recording_id (score ≥ 0.85). Requires ACOUSTID_API_KEY. Rate-limited to ~3 req/sec; takes hours over a full library. Audiobook coverage in AcoustID's DB is sparse — expect a 5–15% hit rate.">
          <Button variant="outlined" color="info" onClick={handleAcoustIDOnline} disabled={onlineLookingUp}>
            {onlineLookingUp ? 'Queuing…' : 'Look Up on AcoustID.org'}
          </Button>
        </Tooltip>

        <IconButton onClick={() => loadCandidates()} size="small" title="Refresh"><RefreshIcon /></IconButton>
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ mb: 2, display: 'block' }}>
        Workflow: <strong>Fingerprint Books</strong> (reads audio, ~hours) → <strong>Find Acoustic Duplicates</strong> (compares hashes, seconds). Merge direction: prefer the book with richer metadata (ASIN/ISBN → cover → sane title).
      </Typography>

      {/* AcoustID online lookup — API key form. Saved to the settings
          DB via PUT /api/v1/config; the server-side op reads from
          AppConfig.AcoustIDAPIKey before falling back to env. */}
      <Paper variant="outlined" sx={{ mb: 2, p: 1.5 }}>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
          <Typography variant="body2" sx={{ fontWeight: 500, minWidth: 0 }}>
            AcoustID.org API key
          </Typography>
          <TextField
            size="small"
            type="password"
            placeholder={acoustidKeyMask ? `Saved: ${acoustidKeyMask}` : 'Get a free key at acoustid.org/login'}
            value={acoustidKey}
            onChange={(e) => setAcoustidKey(e.target.value)}
            sx={{ flex: 1, minWidth: 280 }}
            inputProps={{ autoComplete: 'off' }}
          />
          <Button
            variant="outlined"
            size="small"
            onClick={handleSaveAcoustIDKey}
            disabled={acoustidKeySaving || !acoustidKey.trim()}
          >
            {acoustidKeySaving ? 'Saving…' : 'Save'}
          </Button>
        </Stack>
        <Typography variant="caption" color="text.secondary" sx={{ mt: 0.5, display: 'block' }}>
          Required for "Look Up on AcoustID.org". Stored in the settings database (masked when read back). Falls back to ACOUSTID_API_KEY env var if unset.
        </Typography>
      </Paper>

      {statusMsg && <Alert severity={statusSeverity} sx={{ mb: 2 }} onClose={() => { setStatusMsg(null); setStatusSeverity('info'); }}>{statusMsg}</Alert>}

      {loading ? (
        <LinearProgress />
      ) : candidates.length === 0 ? (
        <Alert severity="info">No acoustic duplicate candidates found. Run "Fingerprint Books" then "Find Acoustic Duplicates".</Alert>
      ) : (
        <Paper>
          {/* Bulk action toolbar — visible whenever any row is selected. */}
          {selectedCandIds.size > 0 && (
            <Stack
              direction="row"
              spacing={1}
              alignItems="center"
              sx={{ px: 2, py: 1, bgcolor: 'action.selected', borderBottom: '1px solid', borderColor: 'divider' }}
            >
              <Typography variant="body2" sx={{ fontWeight: 600 }}>
                {selectedCandIds.size} selected
              </Typography>
              <Box sx={{ flexGrow: 1 }} />
              <Button size="small" variant="outlined" disabled={bulkBusy}
                onClick={() => bulkApply('keep-a')}>
                Keep A on {selectedCandIds.size}
              </Button>
              <Button size="small" variant="outlined" disabled={bulkBusy}
                onClick={() => bulkApply('keep-b')}>
                Keep B on {selectedCandIds.size}
              </Button>
              <Button size="small" variant="outlined" color="warning" disabled={bulkBusy}
                onClick={() => bulkApply('dismiss')}>
                Dismiss {selectedCandIds.size}
              </Button>
              <Button size="small" variant="text" disabled={bulkBusy}
                onClick={() => setSelectedCandIds(new Set())}>
                Clear
              </Button>
            </Stack>
          )}
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell padding="checkbox">
                  <Checkbox
                    size="small"
                    indeterminate={selectedCandIds.size > 0 && selectedCandIds.size < candidates.length}
                    checked={candidates.length > 0 && selectedCandIds.size === candidates.length}
                    onChange={(e) => {
                      if (e.target.checked) {
                        setSelectedCandIds(new Set(candidates.map((c) => c.id)));
                      } else {
                        setSelectedCandIds(new Set());
                      }
                    }}
                  />
                </TableCell>
                <TableCell>Book A</TableCell>
                <TableCell>Book B</TableCell>
                <TableCell align="center">Similarity</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {candidates.map((c) => {
                const bookA = bookCache.get(c.entity_a_id);
                const bookB = bookCache.get(c.entity_b_id);
                const qA = metadataQuality(bookA);
                const qB = metadataQuality(bookB);
                const recommendA = qA > qB;
                const recommendB = qB > qA;
                const busy = resolving.has(c.id);
                const selected = selectedCandIds.has(c.id);
                return (
                  <TableRow
                    key={c.id}
                    hover
                    selected={selected}
                    sx={{ opacity: busy ? 0.5 : 1 }}
                  >
                    <TableCell padding="checkbox">
                      <Checkbox
                        size="small"
                        checked={selected}
                        onChange={(e) => {
                          setSelectedCandIds((prev) => {
                            const next = new Set(prev);
                            if (e.target.checked) next.add(c.id);
                            else next.delete(c.id);
                            return next;
                          });
                        }}
                      />
                    </TableCell>
                    <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                      <Stack spacing={0.5}>
                        {renderBookCell(c.entity_a_id)}
                        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                          {qualityChip(qA)}
                          {recommendA && <Chip label="★ Recommended keep" size="small" color="primary" />}
                        </Stack>
                      </Stack>
                    </TableCell>
                    <TableCell sx={{ verticalAlign: 'top', minWidth: 280 }}>
                      <Stack spacing={0.5}>
                        {renderBookCell(c.entity_b_id)}
                        <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                          {qualityChip(qB)}
                          {recommendB && <Chip label="★ Recommended keep" size="small" color="primary" />}
                        </Stack>
                      </Stack>
                    </TableCell>
                    <TableCell align="center">
                      <Chip label={simPct(c)} size="small" color={(c.similarity ?? 0) >= 0.9 ? 'error' : 'warning'} />
                    </TableCell>
                    <TableCell>
                      <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                        <Tooltip title="Keep Book A, merge B into it">
                          <Button size="small" variant={recommendA ? 'contained' : 'outlined'} color="primary"
                            disabled={busy} onClick={() => handleMerge(c.id, c.entity_a_id)}>
                            Keep A
                          </Button>
                        </Tooltip>
                        <Tooltip title="Keep Book B, merge A into it">
                          <Button size="small" variant={recommendB ? 'contained' : 'outlined'} color="primary"
                            disabled={busy} onClick={() => handleMerge(c.id, c.entity_b_id)}>
                            Keep B
                          </Button>
                        </Tooltip>
                        <Tooltip title="Compare fingerprint segments side-by-side">
                          <Button size="small" variant="outlined" startIcon={<GraphicEqIcon />}
                            onClick={() => { setCompareA(c.entity_a_id); setCompareB(c.entity_b_id); setShowComparePanel(true); }}>
                            Compare
                          </Button>
                        </Tooltip>
                        <Tooltip title="Not a duplicate — dismiss">
                          <Button size="small" variant="text" color="inherit" disabled={busy}
                            onClick={() => handleDismiss(c.id)}>
                            Dismiss
                          </Button>
                        </Tooltip>
                      </Stack>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          <TablePagination
            component="div"
            count={total}
            page={page}
            onPageChange={(_, p) => { setPage(p); setSelectedCandIds(new Set()); }}
            rowsPerPage={rowsPerPage}
            onRowsPerPageChange={(e) => { setRowsPerPage(parseInt(e.target.value, 10)); setPage(0); setSelectedCandIds(new Set()); }}
            rowsPerPageOptions={[25, 50, 100, 250]}
          />
        </Paper>
      )}

      <Box sx={{ mt: 3 }} ref={showComparePanel ? comparePanelRef : undefined}>
        <AcousticComparePanel initialA={compareA} initialB={compareB} />
      </Box>
    </Box>
  );
}

// ---- Main Dedup Page ----
const TAB_NAMES = ['books', 'book-duplicates', 'authors', 'series', 'ai', 'reconcile', 'embedding', 'acoustic', 'split-books'] as const;

export function BookDedup() {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = useMemo(() => {
    const t = searchParams.get('tab');
    const idx = TAB_NAMES.indexOf(t as typeof TAB_NAMES[number]);
    return idx >= 0 ? idx : 0;
  }, [searchParams]);

  // T017: unified dedup tab feature flag + legacy toggle.
  // Feature is default-off; enable via localStorage or VITE_ENABLE_UNIFIED_DEDUP.
  const unifiedEnabled = isUnifiedDedupEnabled();
  // When unified is enabled the user can still opt into the legacy tabs via
  // the toggle. State is persisted in sessionStorage so it doesn't reset on
  // every navigation but resets on a new session (matches "one release" semantics).
  const [showLegacy, setShowLegacy] = useState<boolean>(() => {
    try {
      return sessionStorage.getItem('dedup_show_legacy') === '1';
    } catch {
      return false;
    }
  });
  const handleToggleLegacy = () => {
    setShowLegacy((prev) => {
      const next = !prev;
      try { sessionStorage.setItem('dedup_show_legacy', next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  };

  const setTab = (v: number) => {
    setSearchParams({ tab: TAB_NAMES[v] || 'books' }, { replace: true });
  };

  // When the unified tab is active (enabled + not toggled to legacy),
  // show only the unified surface with a "Show legacy" toggle button.
  const showUnified = unifiedEnabled && !showLegacy;

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 2, gap: 2 }}>
        <Typography variant="h5">Deduplication</Typography>
        {unifiedEnabled && (
          <Tooltip
            title={
              showLegacy
                ? 'Switch back to the new unified view'
                : 'Show legacy tab view (Books / Scan / Acoustic tabs)'
            }
          >
            <Button
              size="small"
              variant="outlined"
              color="inherit"
              startIcon={<ViewListIcon />}
              onClick={handleToggleLegacy}
              data-testid="legacy-toggle-btn"
            >
              {showLegacy ? 'New View' : 'Legacy View'}
            </Button>
          </Tooltip>
        )}
      </Box>

      {/* T017 Unified Dedup Tab — shown when feature is enabled and legacy toggle is off */}
      {showUnified && (
        <Box data-testid="unified-dedup-tab-wrapper">
          <UnifiedDedupTab />
        </Box>
      )}

      {/* Legacy tabs — kept mounted when legacy toggle is on or feature is disabled */}
      {!showUnified && (
        <>
          <Tabs value={tab} onChange={(_, v) => setTab(v)} variant="scrollable" scrollButtons="auto" allowScrollButtonsMobile sx={{ mb: 3, borderBottom: 1, borderColor: 'divider' }}>
            <Tab icon={<Badge color="default"><MenuBookIcon /></Badge>} label="Version Groups" iconPosition="start" />
            <Tab icon={<Badge color="default"><ContentCopyIcon /></Badge>} label="Duplicate Scan" iconPosition="start" />
            <Tab icon={<Badge color="default"><PersonIcon /></Badge>} label="Authors" iconPosition="start" />
            <Tab icon={<Badge color="default"><ListIcon /></Badge>} label="Series" iconPosition="start" />
            <Tab icon={<Badge color="default"><AutoAwesomeIcon /></Badge>} label="AI Review" iconPosition="start" />
            <Tab icon={<Badge color="default"><BuildIcon /></Badge>} label="Reconcile" iconPosition="start" />
            <Tab icon={<Badge color="default"><FingerprintIcon /></Badge>} label="Embedding" iconPosition="start" />
            <Tab icon={<Badge color="default"><GraphicEqIcon /></Badge>} label="Acoustic" iconPosition="start" />
            <Tab icon={<Badge color="default"><CallSplitIcon /></Badge>} label="Split Books" iconPosition="start" />
          </Tabs>

          {tab === 0 && <DedupBookTab />}
          {tab === 1 && <BookDedupScanTab />}
          {tab === 2 && <AuthorDedupTab />}
          {tab === 3 && <SeriesDedupTab />}
          {tab === 4 && <AIReviewTab />}
          {tab === 5 && <ReconcileTab />}
          {tab === 6 && <EmbeddingDedupTab />}
          {tab === 7 && <AcousticDedupTab />}
          {tab === 8 && <DedupSplitBookTab />}
        </>
      )}
    </Box>
  );
}
